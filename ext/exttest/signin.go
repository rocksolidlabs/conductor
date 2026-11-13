package exttest

import (
	"context"
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

var _ ext.SignIn = (*FakeSignIn)(nil)

// FakeSignIn is a FakeIdentity with a scripted sign-in (ext.SignIn): it
// answers the status a test sets, moves to the status scripted for
// BeginSignIn (starting unless a test says otherwise) and for SignOut
// (signed out), and counts the calls of each. A test moves it on as the
// provider's own goroutines would (SetStatus: a code, then signed in), and
// waits on what it was asked through Changed, never a clock. A plain
// FakeIdentity has no sign-in.
type FakeSignIn struct {
	*FakeIdentity

	smu                sync.Mutex
	status             ext.SignInStatus
	onBegin, onSignOut ext.SignInStatus
	signInErr          error
	begins, signOuts   int
}

// NewFakeSignIn is a FakeSignIn named name, signed out, that knows no
// token.
func NewFakeSignIn(name string) *FakeSignIn {
	return &FakeSignIn{
		FakeIdentity: NewFakeIdentity(name),
		status:       ext.SignInStatus{State: ext.SignedOut},
		onBegin:      ext.SignInStatus{State: ext.SignInStarting},
		onSignOut:    ext.SignInStatus{State: ext.SignedOut},
	}
}

// SetStatus has SignInStatus answer s from now on.
func (f *FakeSignIn) SetStatus(s ext.SignInStatus) {
	f.smu.Lock()
	f.status = s
	f.smu.Unlock()
	f.tell()
}

// OnBegin is the status a BeginSignIn moves the fake to.
func (f *FakeSignIn) OnBegin(s ext.SignInStatus) {
	f.smu.Lock()
	f.onBegin = s
	f.smu.Unlock()
	f.tell()
}

// OnSignOut is the status a SignOut moves the fake to.
func (f *FakeSignIn) OnSignOut(s ext.SignInStatus) {
	f.smu.Lock()
	f.onSignOut = s
	f.smu.Unlock()
	f.tell()
}

// SetSignInErr has BeginSignIn and SignOut answer err (nil: none),
// changing nothing.
func (f *FakeSignIn) SetSignInErr(err error) {
	f.smu.Lock()
	f.signInErr = err
	f.smu.Unlock()
	f.tell()
}

// Begins and SignOuts answer how many times each was asked.
func (f *FakeSignIn) Begins() int {
	f.smu.Lock()
	defer f.smu.Unlock()
	return f.begins
}

func (f *FakeSignIn) SignOuts() int {
	f.smu.Lock()
	defer f.smu.Unlock()
	return f.signOuts
}

// SignInStatus answers the status as it stands.
func (f *FakeSignIn) SignInStatus() ext.SignInStatus {
	f.smu.Lock()
	defer f.smu.Unlock()
	return f.status
}

// BeginSignIn moves the fake to OnBegin's status, or answers the error set.
func (f *FakeSignIn) BeginSignIn(context.Context) error { return f.act(&f.begins, &f.onBegin) }

// SignOut moves the fake to OnSignOut's status, or answers the error set.
func (f *FakeSignIn) SignOut(context.Context) error { return f.act(&f.signOuts, &f.onSignOut) }

func (f *FakeSignIn) act(count *int, next *ext.SignInStatus) error {
	f.smu.Lock()
	*count++
	err := f.signInErr
	if err == nil {
		f.status = *next
	}
	f.smu.Unlock()
	f.tell()
	return err
}

// SignIn checks an identity provider's sign-in (ext.SignIn), which
// Identity runs for a provider that offers one: SignInStatus, BeginSignIn
// and SignOut each returning within ext.IdentityWait, the guard's wait,
// with the network open, from Goroutines goroutines at once, with the
// network blocked and with the call's context cancelled; every status a
// valid one (SignInStatus.Valid), whatever the calls before it; and
// article 8 (Network): nothing dialled or requested under a call's
// context. An error from BeginSignIn or SignOut is not judged: a sign-in
// may refuse to begin. What a status holds is never printed, only what
// is wrong with it.
func SignIn(t T, n *Network, s ext.SignIn) {
	t.Helper()
	signIn(t, n, s, nil)
}

// signIn is SignIn, its findings keeping out secrets as Identity's do:
// Identity's tokens when Identity runs it.
func signIn(t T, n *Network, s ext.SignIn, secrets []string) {
	t.Helper()
	p, isProvider := s.(ext.Provider)
	if !isProvider && s != nil {
		t.Errorf("exttest: SignIn: the value is no ext.Provider: a sign-in is an identity provider's")
		return
	}
	c, ok := begin(t, n, "SignIn", p)
	if !ok {
		return
	}
	c.secrets = secrets
	defer c.done()
	status := func(ph phase) {
		st, returned := run(c, "SignInStatus", ext.IdentityWait, ph, func(context.Context) ext.SignInStatus { return s.SignInStatus() })
		if !returned {
			return
		}
		if err := st.Valid(); err != nil {
			c.found("SignInStatus answered a status that is not valid%s: %v", ph.words(), err)
		}
	}
	all := func(ph phase) {
		status(ph)
		run(c, "BeginSignIn", ext.IdentityWait, ph, func(ctx context.Context) error { return s.BeginSignIn(ctx) })
		status(ph)
		run(c, "SignOut", ext.IdentityWait, ph, func(ctx context.Context) error { return s.SignOut(ctx) })
		status(ph)
	}
	all(open)
	together(func() { all(open) })
	c.n.Block()
	all(blocked)
	all(cancelled)
	c.n.Unblock()
}
