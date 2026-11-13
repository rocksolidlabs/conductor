// Package exttest holds a fake of each of package ext's interfaces and of
// ext.Host, and the contract suite: one check function per interface, and
// RunAll, which checks every provider a set holds against the rules
// package ext writes (docs/plans/ext.md, C9).
//
// # The fakes
//
// FakeIdentity, FakeRouter, FakeContext, FakeSink, FakeContinuity,
// FakeEntitlements, FakeMedia and FakeRuntime each implement their
// interface, answer what a test scripts and record what they are asked;
// FakeSignIn is a FakeIdentity with a scripted sign-in (ext.SignIn);
// FakeHost is an ext.Host that records every Launch, Submit and Notify. Each
// is safe for concurrent use, and each that records has Changed, so a test
// waits on what the fake was given (Await), never on a clock.
//
// # The suite
//
// Identity, Router, Context, Sink, Continuity, Entitlements, Media,
// Runtime and Starter each check one implementation, and Identity runs
// SignIn for a provider that offers a sign-in. Each takes a T (the
// part of *testing.T it uses, so a test can record what fails) and the
// *Network the implementation was built with: nil for one that reaches no
// service, as ext/local's defaults do. Among what they check: a valid
// name; the answers' bounds (a principal with a grant whenever ok,
// nothing recognised for the empty token, a route valid for its decision,
// at most ext.MaxContextBlocks valid blocks, Events stable, a valid
// Where, at most ext.MaxICEServers valid servers); the methods that must
// be safe for concurrent use called from Goroutines goroutines at once
// (run the suite under the race detector); a call with a cancelled
// context returning; Name returning with the network blocked too; and
// Start returning with its host and the network blocked, and the closed
// host's refusals reaching the provider while the check runs.
//
// Above all they check article 8 of the Conductor Program: every call
// answers from what the provider holds on this machine, never with a round
// trip to its service on the call's path. The suite checks it by watching
// the provider's network, never a clock. Every call it makes has a context
// marked as that call's, and a dial or request the Network sees under it
// fails the check. Then the suite blocks the network (every dial and
// request waits on a gate it holds shut) and makes the calls again: each
// must return, from what the provider holds, with the gate still shut. A
// provider that reaches its service under a context of its own waits on
// the gate, and is caught when the wait the server's guard gives that
// method ends (ext.IdentityWait and the rest: the product's cap, never a
// threshold of the suite's). How long a call takes is measured apart, by
// this package's benchmarks, never by a check that fails. ContinuityStore's
// Restore, the one call that may reach the service on its own path, is
// checked apart: it streams into its writer, and returns its context's
// error once that ends while its download waits on the shut gate.
//
// # Running it in another module's CI
//
// A provider's repository builds its provider with a Network in place of
// its transport (Network.Transport for HTTP, Network.DialContext for
// anything else, a gRPC or websocket connection among them) and runs
// RunAll, or a check function per interface, in an ordinary go test under
// -race. A Starter's Start is checked on a value of its own, never
// started (Starter starts it twice, with its host and the network
// blocked); the checks of its calls run on a value started as the server
// starts it, with a FakeHost, and RunAll is told so (Started), so that it
// does not start a running value again. It runs the suite twice: with its
// hosted service reachable (a test instance, or an httptest stand-in
// reached through the Network), and unreachable (the service's address
// answering nothing, or the Network blocked before Start), since a
// provider answers as the free default while its service is out of reach
// and must pass both:
//
//	func TestContract(t *testing.T) {
//		for _, reachable := range []bool{true, false} {
//			n := exttest.NewNetwork()
//			opts := provider.Options{Transport: n.Transport(), Service: service(t, reachable)}
//			exttest.Starter(t, n, provider.New(opts))
//			p := provider.New(opts)
//			if !reachable {
//				n.Block()
//			}
//			ctx, cancel := context.WithCancel(context.Background())
//			if err := p.Start(ctx, exttest.NewFakeHost(t)); err != nil {
//				t.Fatal(err)
//			}
//			n.Unblock()
//			exttest.RunAll(t, n, ext.Providers{Identity: p, Notifications: []ext.NotificationSink{p}}, exttest.Started())
//			cancel()
//		}
//	}
//
// A provider built in its package's init, as ext's registry wants it, takes
// its transport from a package variable its test sets before ext.Freeze,
// and the test hands RunAll what Freeze answers.
//
// # The Runtime's execution checks
//
// Runtime checks what a Runtime answers without starting anything (its
// Name and Where, constant; Takes from Goroutines goroutines at once
// within ext.TakesWait). RuntimeExec checks the methods that start
// processes, by driving a helper program through the Runtime where the
// Runtime runs it: RunHelper is that program, its modes (what it does on
// which argument) in its documentation, and a RuntimeCase names it (its
// Argv, what it needs in its environment, the bound of each wait).
// FakeRuntime with a base passes calls through to another Runtime, so a
// test does real work under a Where of its own; NewFakeTerminal is a
// Terminal over pipes for a scripted StartTerminal.
//
// RunAll runs RuntimeExec with the case WithRuntimeCase gives, and fails a
// Runtime whose Where is not ext.WhereNative given none: the native one
// alone may be checked without starting anything there (ext/local's tests
// start its processes). A test binary is its own helper by Go's
// helper-process pattern, its TestMain running RunHelper when it is given
// HelperArg first; the premium repository passes its case so, the helper
// found where its Runtime runs it (a guest given the test binary, built
// for it):
//
//	func TestMain(m *testing.M) {
//		if len(os.Args) > 1 && os.Args[1] == exttest.HelperArg {
//			os.Exit(exttest.RunHelper(os.Args[2:]))
//		}
//		os.Exit(m.Run())
//	}
//
//	func TestRuntimeContract(t *testing.T) {
//		exe, _ := os.Executable()
//		rc := exttest.RuntimeCase{Argv: []string{exe, exttest.HelperArg}, Env: []string{"GORACE=atexit_sleep_ms=0"}}
//		exttest.RunAll(t, nil, ext.Providers{Runtime: provider.New()}, exttest.WithRuntimeCase(rc))
//	}
//
// GORACE there keeps a race-built helper from waiting a second at each
// exit, as the race detector does unless told; the helper is given
// exactly the case's Env and the checks' own variables (EXTTEST_), so what
// else it needs goes in Env too.
//
// # A new interface
//
// A ninth interface of ext joins the suite with its fake here, its check
// function, and a row in byKind (RunAll's table of the checks by kind);
// RunAll fails a set holding a kind no row knows, so a provider of a new
// kind is never passed unchecked. RunAll's options (RunOption) are where a
// check that needs more than a provider takes it.
//
// Package exttest imports the standard library, ext and ext/local only.
package exttest

import (
	"github.com/rocksolidlabs/conductor/ext"
)

// RunOption changes what RunAll checks: Started, and WithRuntimeCase for
// the checks that start processes.
type RunOption func(*runConfig)

// runConfig is what RunAll's options set.
type runConfig struct {
	started bool         // the values are running: Starter is not run on them
	runtime *RuntimeCase // the Runtime's execution checks' case (WithRuntimeCase)
}

// Started tells RunAll that the providers it is given are started
// already, as the server starts them: it runs no Starter on them, since
// starting a running value again checks nothing (a Start that does its
// work once returns at once the second time) and may race the workers the
// first began. Check Start with Starter on a value of its own, never
// started.
func Started() RunOption { return func(c *runConfig) { c.started = true } }

// byKind is the check RunAll runs for a provider of each kind, in the order
// RunAll runs them, with the suite's own inputs where a check takes some.
// A new interface joins the suite with a row here.
var byKind = []struct {
	kind  string
	check func(t T, n *Network, v ext.Provider, c *runConfig) bool // false: v is not the kind's interface
}{
	{ext.KindIdentity, as(func(t T, n *Network, p ext.IdentityProvider, _ *runConfig) {
		Identity(t, n, p, nil, []string{UnknownToken})
	})},
	{ext.KindDecisions, as(func(t T, n *Network, r ext.DecisionRouter, _ *runConfig) {
		Router(t, n, r, SampleDecision())
	})},
	{ext.KindContext, as(func(t T, n *Network, p ext.ContextProvider, _ *runConfig) {
		Context(t, n, p, SampleQuery(t.TempDir()))
	})},
	{ext.KindNotifications, as(func(t T, n *Network, s ext.NotificationSink, _ *runConfig) { Sink(t, n, s) })},
	{ext.KindSync, as(func(t T, n *Network, c ext.ContinuityStore, _ *runConfig) { Continuity(t, n, c) })},
	{ext.KindEntitlements, as(func(t T, n *Network, e ext.EntitlementSource, _ *runConfig) { Entitlements(t, n, e) })},
	{ext.KindMedia, as(func(t T, n *Network, m ext.MediaRoomProvider, _ *runConfig) { Media(t, n, m) })},
	{ext.KindRuntime, as(func(t T, n *Network, r ext.Runtime, c *runConfig) {
		name, w, ok := runtimeStatic(t, n, r)
		switch {
		case c.runtime != nil:
			RuntimeExec(t, r, *c.runtime)
		case ok && w != ext.WhereNative:
			t.Errorf("exttest: RunAll: the Runtime %q answers the Where %q and RunAll was given no WithRuntimeCase: a Runtime that is not the native one is checked starting processes too (RuntimeExec), with the helper its case names", name, w)
		}
	})},
}

// as is a row's check of a provider of interface I.
func as[I any](check func(T, *Network, I, *runConfig)) func(T, *Network, ext.Provider, *runConfig) bool {
	return func(t T, n *Network, v ext.Provider, c *runConfig) bool {
		p, ok := v.(I)
		if ok {
			check(t, n, p, c)
		}
		return ok
	}
}

// RunAll checks every provider p holds with its kind's check function:
// the value of each typed slot (Identity, Router, the Context and
// Notifications lists, Continuity, Media, Runtime) and of every entry of
// p.All (the entitlement source's among them), each value once per kind,
// in the order of the kinds; and Starter for every distinct value that is
// an ext.Starter, after the rest, unless Started says the values are
// running (Starter starts a value itself). ext.Freeze's answer holds every
// registered provider in All; local.Fill's puts the free defaults in the
// typed slots. An entry whose value is not its kind's interface, or of a
// kind the suite knows no check for, fails.
func RunAll(t T, n *Network, p ext.Providers, opts ...RunOption) {
	t.Helper()
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}
	type item struct {
		kind string
		v    ext.Provider
	}
	var items []item
	has := func(kind string, v ext.Provider) bool {
		for _, it := range items {
			if it.kind == kind && sameValue(it.v, v) {
				return true
			}
		}
		return false
	}
	add := func(kind string, v ext.Provider) {
		if v == nil || nilValue(v) || has(kind, v) {
			return
		}
		items = append(items, item{kind, v})
	}
	if p.Identity != nil {
		add(ext.KindIdentity, p.Identity)
	}
	if p.Router != nil {
		add(ext.KindDecisions, p.Router)
	}
	for _, c := range p.Context {
		if c != nil {
			add(ext.KindContext, c)
		}
	}
	for _, s := range p.Notifications {
		if s != nil {
			add(ext.KindNotifications, s)
		}
	}
	if p.Continuity != nil {
		add(ext.KindSync, p.Continuity)
	}
	if p.Media != nil {
		add(ext.KindMedia, p.Media)
	}
	if p.Runtime != nil {
		add(ext.KindRuntime, p.Runtime)
	}
	for _, e := range p.All {
		if e.Value == nil || nilValue(e.Value) {
			t.Errorf("exttest: RunAll: the %s entry %q holds a nil provider", e.Kind, e.Name)
			continue
		}
		known := false
		for _, row := range byKind {
			known = known || row.kind == e.Kind
		}
		if !known {
			t.Errorf("exttest: RunAll: the entry %q is of the kind %q, which no check of the suite knows: a new interface joins the suite with its check and its row in byKind", e.Name, e.Kind)
			continue
		}
		add(e.Kind, e.Value)
	}
	var starters []ext.Starter
	for _, row := range byKind {
		for _, it := range items {
			if it.kind != row.kind {
				continue
			}
			if !row.check(t, n, it.v, cfg) {
				t.Errorf("exttest: RunAll: the %s provider %q (%T) is not an implementation of its kind's interface", it.kind, it.v.Name(), it.v)
				continue
			}
			s, ok := it.v.(ext.Starter)
			if !ok {
				continue
			}
			seen := false
			for _, o := range starters {
				seen = seen || sameValue(o, s)
			}
			if !seen {
				starters = append(starters, s)
			}
		}
	}
	if cfg.started {
		return
	}
	for _, s := range starters {
		Starter(t, n, s)
	}
}

// sameValue reports whether a and b are one value: of one comparable
// dynamic type and ==. An incomparable value is never the same as another.
func sameValue(a, b any) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}
