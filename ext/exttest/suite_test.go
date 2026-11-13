package exttest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// recT is a T that records what a check says.
type recT struct {
	t    *testing.T
	mu   sync.Mutex
	errs []string
}

func (r *recT) Helper() {}
func (r *recT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recT) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}
func (r *recT) TempDir() string { return r.t.TempDir() }

// record runs check with a recording T and answers what it said.
func record(t *testing.T, check func(T)) []string {
	t.Helper()
	r := &recT{t: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		check(r)
	}()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

// said reports whether one of errs holds every one of parts.
func said(errs []string, parts ...string) bool {
	for _, e := range errs {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(e, p)
		}
		if all {
			return true
		}
	}
	return false
}

// TestSuitePassesEveryDefault runs the suite over ext/local's defaults: in
// the typed slots, as local.Fill puts them, and as entries of All, the
// entitlement source's among them, as a registry holding them would. With
// C1's TestPlainBuildRegistersNothing, this is the R0 gate's second
// clause: every default passes the contract suite.
func TestSuitePassesEveryDefault(t *testing.T) {
	n := NewNetwork()
	errs := record(t, func(t T) {
		RunAll(t, n, local.Fill(ext.Providers{}))
		RunAll(t, n, ext.Providers{All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: local.Name, Value: local.Identity{}},
			{Kind: ext.KindDecisions, Name: local.Name, Value: local.Router{}},
			{Kind: ext.KindSync, Name: local.Name, Value: local.Continuity{}},
			{Kind: ext.KindEntitlements, Name: local.Name, Value: local.AllowAll{}},
			{Kind: ext.KindMedia, Name: local.Name, Value: local.Media{}},
		}})
		// Each check alone too, with no network: the defaults reach none.
		Identity(t, nil, local.Identity{}, nil, []string{UnknownToken, "x"})
		Router(t, nil, local.Router{}, SampleDecision())
		Router(t, nil, local.Router{}, SampleDecisionRequest())
		Continuity(t, nil, local.Continuity{})
		Entitlements(t, nil, local.AllowAll{})
		Media(t, nil, local.Media{})
	})
	for _, e := range errs {
		t.Error(e)
	}
	if dials, _ := n.Dials(); len(dials) != 0 {
		t.Errorf("the defaults dialled %d times; they reach no service", len(dials))
	}
}

// keyService is a stand-in for an identity provider's service: it answers
// the keys, here a list of the tokens it admits.
func keyService(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tok-admin\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// keyedIdentity is a provider as article 8 wants one: Start begins a
// goroutine that fetches its keys through the Network and launches a
// nightly run through its host, and Authenticate answers from the keys it
// holds.
type keyedIdentity struct {
	*FakeIdentity
	client  *http.Client
	url     string
	fetched atomic.Int32
}

func (k *keyedIdentity) Start(ctx context.Context, h ext.Host) error {
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
		if err != nil {
			return
		}
		if resp, err := k.client.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			for tok := range strings.FieldsSeq(string(b)) {
				k.Add(tok, ada)
			}
			k.fetched.Add(1)
			k.tell() // the count is part of what a test awaits: Await is asked again only at a tell
		}
		_, _ = h.Launch(ctx, ext.LaunchRequest{Crew: "nightly", By: ada})
		if _, err := h.DataDir(); err != nil && !errors.Is(err, context.Canceled) {
			panic(err) // a closed host's refusal is context.Canceled
		}
	}()
	return nil
}

// TestSuitePassesEveryFake runs the suite over every fake, scripted with
// valid answers, and over a provider that reaches its service only on its
// own goroutine, begun in Start: its calls on a value started as the
// server starts it (RunAll told so), its Start on a value of its own.
func TestSuitePassesEveryFake(t *testing.T) {
	n := NewNetwork()
	rc := helperCase(t)
	srv := keyService(t)
	keyed := func() *keyedIdentity {
		return &keyedIdentity{FakeIdentity: NewFakeIdentity("fake-identity"), client: &http.Client{Transport: n.Transport()}, url: srv.URL}
	}
	id := keyed()
	h := NewFakeHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := id.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := Await(waitCtx(t), id, func() bool { return id.fetched.Load() == 1 }); err != nil {
		t.Fatalf("the provider never fetched its keys: %v", err)
	}

	router := NewFakeRouter("fake-router")
	d := SampleDecision()
	// The route of the suite's first question (Router asks d's ID with -1).
	router.SetRoute(d.ID+"-1", ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}, Rule: "owns main.go", Answer: &ext.Answer{Input: "1", By: ext.Principal{Subject: "ada", Name: "Ada"}}})
	cx := NewFakeContext("fake-context")
	cx.SetBlocks([]ext.ContextBlock{{Title: "Callers of Build", Ref: "main.go", Body: "main calls Build."}, {Title: "Recent change", Body: "Build gained a flag."}})
	sink := NewFakeSink("fake-sink", []string{"needs_input", ext.TypeProvider})
	sync := NewFakeContinuity("fake-sync")
	sync.SetArchive([]byte("PK an archive"))
	plans := NewFakeEntitlements("fake-plans")
	plans.Set("exttest-feature", ext.Entitlement{Allowed: true, Limit: 3})
	media := NewFakeMedia("fake-media")
	media.SetServers([]ext.ICEServer{{URLs: []string{"turn:turn.example.com:3478?transport=udp"}, Username: "u", Credential: "c"}})
	media.SetRoom(ext.RoomJoin{URL: "https://rooms.example.com/r1"}, true)
	rt := NewFakeRuntime("fake-runtime", ext.WhereNative)
	rt.SetBase(local.Native())

	p := ext.Providers{
		Identity: id, Router: router, Context: []ext.ContextProvider{cx},
		Notifications: []ext.NotificationSink{sink, NewFakeSink("fake-all", nil)},
		Continuity:    sync, Media: media, Runtime: rt,
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "fake-identity", Value: id},
			{Kind: ext.KindEntitlements, Name: "fake-plans", Value: plans},
		},
	}
	// A provider that offers a sign-in, its BeginSignIn moving it to a code.
	signIn := NewFakeSignIn("fake-signin")
	signIn.Add("tok-admin", ada)
	signIn.OnBegin(ext.SignInStatus{State: ext.SignInCode, Code: "WDJB-MJHT", URL: "https://id.example.com/device"})
	errs := record(t, func(t T) {
		Starter(t, n, keyed())
		RunAll(t, n, p, Started(), WithRuntimeCase(rc))
		Identity(t, n, id, []string{"tok-admin"}, []string{UnknownToken})
		Identity(t, n, signIn, []string{"tok-admin"}, []string{UnknownToken})
	})
	for _, e := range errs {
		t.Error(e)
	}
	if signIn.Begins() == 0 || signIn.SignOuts() == 0 {
		t.Errorf("Identity did not check the sign-in: %d begins, %d sign-outs", signIn.Begins(), signIn.SignOuts())
	}
	// The suite reached each of them.
	if len(router.Decisions()) == 0 || len(router.Resolutions()) == 0 || len(cx.Queries()) == 0 || len(cx.Touches()) == 0 ||
		len(sink.Notifications()) == 0 || len(sync.Docs()) == 0 || len(sync.Restores()) == 0 || len(plans.Asked()) == 0 ||
		len(media.ICERequests()) == 0 || len(media.RoomRequests()) == 0 || id.Calls() == 0 {
		t.Errorf("the suite left a fake unasked")
	}
}

// The breaches TestSuiteCatchesABreach plants.

// dialIdentity dials its service under the call's context.
type dialIdentity struct {
	n    *Network
	addr string
}

func (dialIdentity) Name() string { return "dialler" }
func (p dialIdentity) Authenticate(ctx context.Context, _ string) (ext.Principal, bool, error) {
	conn, err := p.n.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return ext.Principal{}, false, err
	}
	conn.Close()
	return ext.Principal{}, false, nil
}

// waitIdentity asks its service on the call's path under a context of its
// own, which the mark never reaches.
type waitIdentity struct {
	client *http.Client
	url    string
}

func (waitIdentity) Name() string { return "waiter" }
func (p waitIdentity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.url, nil)
	if err != nil {
		return ext.Principal{}, false, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ext.Principal{}, false, err
	}
	resp.Body.Close()
	return ext.Principal{}, false, nil
}

// quitterIdentity knows "tok-q" while its service answers and says ok
// false, not an error, when it cannot reach it.
type quitterIdentity struct {
	n    *Network
	addr string
}

func (quitterIdentity) Name() string { return "quitter" }
func (p quitterIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	conn, err := p.n.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return ext.Principal{}, false, nil
	}
	conn.Close()
	return ada, tok == "tok-q", nil
}

// dialSignIn begins its sign-in by dialling its service under the call's
// context.
type dialSignIn struct {
	*FakeSignIn
	n    *Network
	addr string
}

func (p dialSignIn) BeginSignIn(ctx context.Context) error {
	conn, err := p.n.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// emptyIdentity admits every token, the empty one too.
type emptyIdentity struct{}

func (emptyIdentity) Name() string { return "lenient" }
func (emptyIdentity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ada, true, nil
}

// nineBlocks answers one block past MaxContextBlocks.
type nineBlocks struct{ *FakeContext }

func newNineBlocks() nineBlocks {
	f := NewFakeContext("nine")
	bs := make([]ext.ContextBlock, ext.MaxContextBlocks+1)
	for i := range bs {
		bs[i] = ext.ContextBlock{Title: fmt.Sprintf("Block %d", i), Body: "text"}
	}
	f.SetBlocks(bs)
	return nineBlocks{f}
}

// hostStarter waits on its host inside Start.
type hostStarter struct{ *FakeIdentity }

func (hostStarter) Start(ctx context.Context, h ext.Host) error {
	_, err := h.Launch(ctx, ext.LaunchRequest{Crew: "nightly", By: ada})
	return err
}

// strayRouter answers a choice its decision does not offer.
type strayRouter struct{ *FakeRouter }

func (strayRouter) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{Answer: &ext.Answer{Input: "9", By: ada}}, nil
}

// recordStrayRouter answers a session's question well, and a durable
// decision's record (Request set) with keys that are none of its places.
type recordStrayRouter struct{ *FakeRouter }

func (recordStrayRouter) Route(_ context.Context, d ext.Decision) (ext.Route, error) {
	if d.Request == "" {
		return ext.Route{Answer: &ext.Answer{Input: d.Options[0].Input, By: ada}}, nil
	}
	return ext.Route{Answer: &ext.Answer{Input: "y", By: ada}}, nil
}

// panicMedia panics in ICEServers.
type panicMedia struct{ *FakeMedia }

func (panicMedia) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	panic("boom")
}

// heldIdentity makes a round trip to its service on every call, over one
// connection it dialled before any call, as a gRPC or websocket client
// does: no dial and no request is made under the call's context.
type heldIdentity struct {
	mu   sync.Mutex
	conn net.Conn
}

func (*heldIdentity) Name() string { return "holder" }
func (p *heldIdentity) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.conn.Write([]byte("?")); err != nil {
		return ext.Principal{}, false, err
	}
	if _, err := io.ReadFull(p.conn, make([]byte, 1)); err != nil {
		return ext.Principal{}, false, err
	}
	return ext.Principal{}, false, nil
}

// echoServer answers every byte sent to it, on every connection, until the
// test ends.
func echoServer(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go io.Copy(c, c)
		}
	}()
	return l.Addr().String()
}

// nameFetcher asks its service for its name, under a context of its own.
type nameFetcher struct {
	*FakeIdentity
	client *http.Client
	url    string
}

func (p nameFetcher) Name() string {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, p.url, nil)
	if resp, err := p.client.Do(req); err == nil {
		resp.Body.Close()
	}
	return "fetcher"
}

// reusingSink answers the same array every time, changed in place.
type reusingSink struct {
	*FakeSink
	mu  sync.Mutex
	buf []string
	n   int
}

func (s *reusingSink) Events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.buf == nil {
		s.buf = make([]string, 1)
	}
	s.buf[0] = fmt.Sprint("e", s.n)
	return s.buf
}

// lateRestore's first Restore returns at once and leaves a goroutine that
// writes once its context ends; its second returns only once that write is
// made, so the write comes before the check's end.
type lateRestore struct {
	*FakeContinuity
	calls atomic.Int32
	wrote chan struct{}
}

func (p *lateRestore) Restore(ctx context.Context, _ ext.RestoreRequest, w io.Writer) error {
	if p.calls.Add(1) == 1 {
		go func() {
			<-ctx.Done()
			w.Write([]byte("late"))
			close(p.wrote)
		}()
		return ext.ErrNothingToRestore
	}
	<-p.wrote
	return ext.ErrNothingToRestore
}

// fickleSink answers another list of events every time.
type fickleSink struct {
	*FakeSink
	n atomic.Int32
}

func (s *fickleSink) Events() []string { return []string{fmt.Sprint("e", s.n.Add(1))} }

// doneSink takes only done, and asks its service on the call's path for
// each: a notification it does not take it lets go at once.
type doneSink struct {
	*FakeSink
	client *http.Client
	url    string
}

func (doneSink) Events() []string { return []string{"done"} }
func (s doneSink) Notify(ctx context.Context, n ext.Notification) error {
	if n.Event != "done" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// cachingRouter fetches a question's route from its service the first
// time it is told of it, under a context of its own, and answers it from
// its cache after.
type cachingRouter struct {
	*FakeRouter
	client *http.Client
	url    string
	mu     sync.Mutex
	cache  map[string]ext.Route
}

func (r *cachingRouter) Route(_ context.Context, d ext.Decision) (ext.Route, error) {
	r.mu.Lock()
	rt, ok := r.cache[d.ID]
	r.mu.Unlock()
	if ok {
		return rt, nil
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, r.url, nil)
	if err != nil {
		return ext.Route{}, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return ext.Route{}, err
	}
	resp.Body.Close()
	r.mu.Lock()
	r.cache[d.ID] = ext.Route{}
	r.mu.Unlock()
	return ext.Route{}, nil
}

// TestSuiteCatchesABreach plants one breach of a rule in each provider and
// checks that the suite fails it, naming the check and the rule, through a
// recording T. The waits are the guard's own (IdentityWait, StartWait):
// the cases run in parallel so the test takes the longest of them.
func TestSuiteCatchesABreach(t *testing.T) {
	srv := keyService(t)
	echo := echoServer(t)
	cases := []struct {
		name  string
		check func(t T, n *Network)
		want  []string
	}{
		{"a dial under the call's context", func(t T, n *Network) {
			Identity(t, n, dialIdentity{n, srv.Listener.Addr().String()}, nil, nil)
		}, []string{`exttest: Identity "dialler": Authenticate dialled tcp`, "under its call's context", "article 8"}},
		{"a wait on the network under a context of its own", func(t T, n *Network) {
			Identity(t, n, waitIdentity{&http.Client{Transport: n.Transport()}, srv.URL}, nil, nil)
		}, []string{`exttest: Identity "waiter": Authenticate did not return within 1s with the network blocked`, "article 8"}},
		{"a sign-in that dials under the call's context", func(t T, n *Network) {
			Identity(t, n, dialSignIn{NewFakeSignIn("dial-signin"), n, srv.Listener.Addr().String()}, nil, nil)
		}, []string{`exttest: SignIn "dial-signin": BeginSignIn dialled tcp`, "under its call's context", "article 8"}},
		{"a sign-in's page over http", func(t T, n *Network) {
			f := NewFakeSignIn("http-signin")
			f.OnBegin(ext.SignInStatus{State: ext.SignInCode, Code: "WDJB-MJHT", URL: "http://id.example.com/device"})
			Identity(t, n, f, nil, nil)
		}, []string{`exttest: SignIn "http-signin": SignInStatus answered a status that is not valid`, "url is not https"}},
		{"ok for the empty token", func(t T, n *Network) {
			Identity(t, n, emptyIdentity{}, nil, nil)
		}, []string{`exttest: Identity "lenient": Authenticate said ok for the empty token: nothing is recognised for an empty token`}},
		{"ok false for a token it cannot tell", func(t T, n *Network) {
			Identity(t, n, quitterIdentity{n, srv.Listener.Addr().String()}, []string{"tok-q"}, nil)
		}, []string{`exttest: Identity "quitter": Authenticate said ok false for known token 0 with the network blocked`, "for an admission that has ended"}},
		{"nine blocks", func(t T, n *Network) {
			Context(t, n, newNineBlocks(), SampleQuery(t.TempDir()))
		}, []string{`exttest: Context "nine": Context answered 9 blocks`, "MaxContextBlocks (8)"}},
		{"a Start that waits on its host", func(t T, n *Network) {
			Starter(t, n, hostStarter{NewFakeIdentity("starter")})
		}, []string{`exttest: Starter "starter": Start (try 1) did not return within StartWait (5s) with its host and the network blocked`}},
		{"a route its decision does not offer", func(t T, n *Network) {
			Router(t, n, strayRouter{NewFakeRouter("stray")}, SampleDecision())
		}, []string{`exttest: Router "stray": Route answered a route not valid for its decision`, `"9"`}},
		{"a record's route its decision does not offer", func(t T, n *Network) {
			Router(t, n, recordStrayRouter{NewFakeRouter("records")}, SampleDecision())
		}, []string{`exttest: Router "records": Route answered a route not valid for its decision`, `"y"`}},
		{"a record's route its decision does not offer, asked from a record", func(t T, n *Network) {
			Router(t, n, recordStrayRouter{NewFakeRouter("records")}, SampleDecisionRequest())
		}, []string{`exttest: Router "records": Route answered a route not valid for its decision`, `"y"`}},
		{"a panic", func(t T, n *Network) {
			Media(t, n, panicMedia{NewFakeMedia("panicky")})
		}, []string{`exttest: Media "panicky": ICEServers panicked: boom`}},
		{"Events that change", func(t T, n *Network) {
			Sink(t, n, &fickleSink{FakeSink: NewFakeSink("fickle", nil)})
		}, []string{`exttest: Sink "fickle": Events answered`, "must not change"}},
		{"a round trip over a connection held from before the call", func(t T, n *Network) {
			conn, err := n.DialContext(context.Background(), "tcp", echo)
			if err != nil {
				t.Fatalf("dial the echo service: %v", err)
			}
			defer conn.Close()
			Identity(t, n, &heldIdentity{conn: conn}, nil, nil)
		}, []string{`exttest: Identity "holder": Authenticate did not return within 1s with the network blocked`, "article 8"}},
		{"a Name that asks its service", func(t T, n *Network) {
			Identity(t, n, nameFetcher{NewFakeIdentity("fetcher"), &http.Client{Transport: n.Transport()}, srv.URL}, nil, nil)
		}, []string{`exttest: Identity "fetcher": Name did not return within 5s with the network blocked`, "article 8"}},
		{"Events that change in place", func(t T, n *Network) {
			Sink(t, n, &reusingSink{FakeSink: NewFakeSink("reuser", nil)})
		}, []string{`exttest: Sink "reuser": Events answered ["e2"]`, `after ["e1"]`, "must not change"}},
		{"a Restore that writes once its context ends", func(t T, n *Network) {
			Continuity(t, n, &lateRestore{FakeContinuity: NewFakeContinuity("late"), wrote: make(chan struct{})})
		}, []string{`exttest: Continuity "late": Restore wrote 4 bytes after it had returned`}},
		{"a round trip only for the event the sink takes", func(t T, n *Network) {
			Sink(t, n, doneSink{NewFakeSink("done-only", nil), &http.Client{Transport: n.Transport()}, srv.URL})
		}, []string{`exttest: Sink "done-only": Notify sent GET http://` + srv.Listener.Addr().String(), "under its call's context", "article 8"}},
		{"a route fetched the first time a question is routed", func(t T, n *Network) {
			Router(t, n, &cachingRouter{FakeRouter: NewFakeRouter("cacher"), client: &http.Client{Transport: n.Transport()}, url: srv.URL, cache: map[string]ext.Route{}}, SampleDecision())
		}, []string{`exttest: Router "cacher": Route did not return within 5s with the network blocked`, "article 8"}},
		{"a bad name", func(t T, n *Network) {
			Identity(t, n, NewFakeIdentity("Not A Name"), nil, nil)
		}, []string{`exttest: Identity "Not A Name": Name "Not A Name" is not a provider's name`}},
		{"a nil provider", func(t T, n *Network) {
			Identity(t, n, (*FakeIdentity)(nil), nil, nil)
		}, []string{"exttest: Identity: a nil provider"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			n := NewNetwork()
			errs := record(t, func(rt T) { c.check(rt, n) })
			if !said(errs, c.want...) {
				t.Errorf("the suite did not say %q; it said:\n%s", c.want, strings.Join(errs, "\n"))
			}
		})
	}
}

// TestRunAll checks what RunAll runs: every kind's check once per value,
// Starter once per value that is one, and a failure for an entry of a kind
// the suite has no check for or whose value is not its kind's interface.
func TestRunAll(t *testing.T) {
	t.Parallel()
	n := NewNetwork()
	both := &twoKinds{NewFakeIdentity("both"), NewFakeSink("both", nil)}
	errs := record(t, func(rt T) {
		RunAll(rt, n, ext.Providers{
			Identity:      both,
			Notifications: []ext.NotificationSink{both},
			All: []ext.Entry{
				{Kind: ext.KindIdentity, Name: "both", Value: both},
				{Kind: "telepathy", Name: "psychic", Value: NewFakeRuntime("psychic", ext.WhereNative)},
				{Kind: ext.KindMedia, Name: "not-media", Value: NewFakeRuntime("not-media", ext.WhereNative)},
			},
		})
	})
	if !said(errs, `the entry "psychic" is of the kind "telepathy", which no check of the suite knows`) {
		t.Errorf("an unknown kind passed: %q", errs)
	}
	if !said(errs, `the media provider "not-media"`, "is not an implementation of its kind's interface") {
		t.Errorf("a value not of its kind's interface passed: %q", errs)
	}
	// both is a Starter that waits on its host, in two kinds and in All
	// too: one value, so one try of Start, once.
	starts := 0
	for _, e := range errs {
		if strings.Contains(e, "Start (try 1) did not return") {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("Starter ran %d times for one value, want 1: %q", starts, errs)
	}
	if both.Calls() == 0 || len(both.Notifications()) == 0 {
		t.Errorf("a kind's check never ran: %d authentications, %d notifications", both.Calls(), len(both.Notifications()))
	}

	// Started: the values are running, so RunAll starts none of them again.
	started := &twoKinds{NewFakeIdentity("both"), NewFakeSink("both", nil)}
	errs = record(t, func(rt T) {
		RunAll(rt, n, ext.Providers{Identity: started, Notifications: []ext.NotificationSink{started}}, Started())
	})
	if said(errs, "Start (try") || started.Calls() == 0 {
		t.Errorf("RunAll told the values are started ran Starter, or no check: %q", errs)
	}
}

// twoKinds is one value in two slots, an identity provider and a sink,
// that waits on its host in Start.
type twoKinds struct {
	*FakeIdentity
	*FakeSink
}

func (*twoKinds) Name() string { return "both" }
func (*twoKinds) Start(ctx context.Context, h ext.Host) error {
	_, err := h.Launch(ctx, ext.LaunchRequest{Crew: "nightly", By: ada})
	return err
}

// payload is what the restore server streams: more than one write.
var payload = bytes.Repeat([]byte("PK an archive's bytes\n"), 16<<10)

func restoreServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _ := w.(http.Flusher)
		for i := 0; i < len(payload); i += 32 << 10 {
			w.Write(payload[i:min(i+32<<10, len(payload))])
			if f != nil {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// nilRestore downloads under its context, as it should, but answers nil
// once that context ends.
type nilRestore struct {
	*FakeContinuity
	client *http.Client
	url    string
}

func (p nilRestore) Restore(ctx context.Context, _ ext.RestoreRequest, w io.Writer) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

// TestRestoreMayStream checks Restore, the one call the network may serve
// on its own path: a fake Restore streaming from an httptest server
// through the Network passes the suite; ended by its context while the
// gate is shut, it answers the context's error; one that answers nil
// then fails the suite.
func TestRestoreMayStream(t *testing.T) {
	srv := restoreServer(t)
	n := NewNetwork()
	c := NewFakeContinuity("fake-sync")
	c.RestoreFrom(srv.URL, &http.Client{Transport: n.Transport()})

	// Passed: the suite's open and blocked Restores.
	if errs := record(t, func(rt T) { Continuity(rt, n, c) }); len(errs) != 0 {
		t.Errorf("the suite failed a Restore that streams: %q", errs)
	}

	// It streams the whole archive into its writer, under its own context.
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "restore")
	var w bytes.Buffer
	if err := c.Restore(ctx, ext.RestoreRequest{Dir: t.TempDir()}, &w); err != nil || !bytes.Equal(w.Bytes(), payload) {
		t.Fatalf("Restore = %v with %d bytes, want nil and %d", err, w.Len(), len(payload))
	}
	dials, _ := n.Dials()
	if len(dials) == 0 || dials[len(dials)-1].Method != http.MethodGet || dials[len(dials)-1].Ctx.Value(key{}) != "restore" {
		t.Errorf("the network did not record the download under Restore's context: %+v", dials)
	}

	// Ended by its context while its download waits on the shut gate.
	n.Block()
	cctx, cancel := context.WithCancel(mayWait(context.Background(), "the test's restore"))
	waiting := n.whenWaiting("the test's restore")
	answered := make(chan error, 1)
	go func() { answered <- c.Restore(cctx, ext.RestoreRequest{Dir: t.TempDir()}, io.Discard) }()
	select {
	case <-waiting:
	case err := <-answered:
		t.Fatalf("Restore answered %v with the gate shut, before it waited on it", err)
	}
	cancel()
	if err := <-answered; !errors.Is(err, context.Canceled) {
		t.Errorf("Restore ended by its context answered %v, not its error", err)
	}
	n.Unblock()

	// A Restore that answers nil once its context ended fails the suite.
	bad := nilRestore{NewFakeContinuity("forgetful"), &http.Client{Transport: n.Transport()}, srv.URL}
	errs := record(t, func(rt T) { Continuity(rt, n, bad) })
	if !said(errs, `exttest: Continuity "forgetful": Restore answered <nil> once its context ended with the network blocked, not its context's error`) {
		t.Errorf("the suite passed a Restore that hides its context's end: %q", errs)
	}
}

// deadlineRestore downloads over a connection it holds from before its
// call, as a gRPC client does, and ends a Restore whose context ends by
// setting the connection's deadline, as net.Conn lets it.
type deadlineRestore struct {
	*FakeContinuity
	conn net.Conn
}

func (p deadlineRestore) Restore(ctx context.Context, _ ext.RestoreRequest, w io.Writer) error {
	if err := p.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { p.conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	b := []byte("?")
	if _, err := p.conn.Write(b); err != nil {
		return cmp.Or(ctx.Err(), err)
	}
	if _, err := io.ReadFull(p.conn, b); err != nil {
		return cmp.Or(ctx.Err(), err)
	}
	_, err := w.Write(b)
	return err
}

// TestRestoreEndedByADeadline checks that a Restore ended by its
// connection's deadline, set once its context ends, passes the suite:
// the deadline lets its Write go from the shut gate.
func TestRestoreEndedByADeadline(t *testing.T) {
	t.Parallel()
	n := NewNetwork()
	// Dialled under the label of the suite's blocked Restore, so the suite
	// sees its Write wait on the gate and ends its context then.
	conn, err := n.DialContext(mayWait(context.Background(), "Continuity deadliner Restore blocked"), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	errs := record(t, func(rt T) { Continuity(rt, n, deadlineRestore{NewFakeContinuity("deadliner"), conn}) })
	if len(errs) != 0 {
		t.Errorf("the suite failed a Restore that its connection's deadline ends: %q", errs)
	}
}

// TestNetwork checks the Network itself: a dial and a request under a
// call's context refused and recorded, one under another context through
// while open, held while blocked and let go by Unblock or its context.
func TestNetwork(t *testing.T) {
	srv := keyService(t)
	n := NewNetwork()
	client := &http.Client{Transport: n.Transport()}
	get := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	marked := onCall(context.Background(), "Identity x Authenticate")
	if err := get(marked); !errors.Is(err, ErrOnCallPath) {
		t.Errorf("a request on a call's path answered %v", err)
	}
	if _, err := n.DialContext(marked, "tcp", srv.Listener.Addr().String()); !errors.Is(err, ErrOnCallPath) {
		t.Errorf("a dial on a call's path answered %v", err)
	}
	if got := n.takeBreaches("Identity x "); len(got) != 2 {
		t.Errorf("%d breaches recorded, want 2", len(got))
	}
	if err := get(context.Background()); err != nil {
		t.Fatalf("an open network refused a request: %v", err)
	}
	conn, err := n.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("an open network refused a dial: %v", err)
	}
	conn.Close()

	n.Block()
	n.Block() // again: nothing
	ctx := mayWait(context.Background(), "held")
	done := make(chan error, 1)
	go func() { done <- get(ctx) }()
	<-n.whenWaiting("held")
	n.Unblock()
	if err := <-done; err != nil {
		t.Errorf("a request let go answered %v", err)
	}
	n.Block()
	cctx, cancel := context.WithCancel(ctx)
	go func() {
		_, err := n.DialContext(cctx, "tcp", srv.Listener.Addr().String())
		done <- err
	}()
	<-n.whenWaiting("held")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("a held dial whose context ended answered %v", err)
	}
	n.Unblock()
	n.Unblock() // again: nothing
	dials, dropped := n.Dials()
	if len(dials) != 6 || dropped != 0 {
		t.Errorf("%d dials recorded (%d dropped), want 6", len(dials), dropped)
	}
	var nilNet *Network
	nilNet.Block()
	nilNet.Unblock()
	if d, _ := nilNet.Dials(); d != nil {
		t.Errorf("a nil Network recorded %v", d)
	}
}

// queryIdentity sends the token it is asked about in its request's query
// string, escaped, beside a key of its own, with a password in the URL's
// user information, under the call's context, and answers the client's
// error, which names that URL, inside words naming the token as it is and
// escaped both ways.
type queryIdentity struct {
	client *http.Client
	base   *url.URL
}

func (queryIdentity) Name() string { return "querier" }
func (p queryIdentity) Authenticate(ctx context.Context, token string) (ext.Principal, bool, error) {
	u := *p.base
	u.User = url.UserPassword("svc", "pass-secret")
	u.Path = "/check"
	u.RawQuery = url.Values{"token": {token}, "key": {"svc-key-secret"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ext.Principal{}, false, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ext.Principal{}, false, fmt.Errorf("checking %s (%s, %s): %w", token, url.QueryEscape(token), url.PathEscape(token), err)
	}
	resp.Body.Close()
	return ext.Principal{}, false, nil
}

// TestBreachWordsKeepTokens checks that what the suite says of a breach
// names a request by its method, scheme, host and path only, and never
// prints a token it was given, even inside the provider's own error.
func TestBreachWordsKeepTokens(t *testing.T) {
	t.Parallel()
	srv := keyService(t)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n := NewNetwork()
	// A token that query- and path-escaping both change.
	const secret = "tok-secret+admin/="
	errs := record(t, func(rt T) {
		Identity(rt, n, queryIdentity{&http.Client{Transport: n.Transport()}, base}, []string{secret}, []string{UnknownToken})
	})
	if !said(errs, `exttest: Identity "querier": Authenticate sent GET http://`+base.Host+`/check under its call's context`) {
		t.Errorf("the breach was not said, by method, scheme, host and path: %q", errs)
	}
	// The provider's own words: the token stood in for in each form, the
	// client's error naming its request by scheme, host and path.
	if !said(errs, `Authenticate answered an error for known token 0 with the network open: checking [token] ([token], [token]): Get "http://`+base.Host+`/check": `) {
		t.Errorf("the provider's error was not said with its token and its URL's query kept out: %q", errs)
	}
	for _, e := range errs {
		for _, kept := range []string{
			secret, url.QueryEscape(secret), url.PathEscape(secret),
			UnknownToken, "pass-secret", "svc-key-secret", "token=", "key=", "svc:",
		} {
			if strings.Contains(e, kept) {
				t.Errorf("the suite printed %q: %s", kept, e)
			}
		}
	}
	dials, _ := n.Dials()
	for _, d := range dials {
		if strings.ContainsAny(d.URL, "?@#") {
			t.Errorf("the Network recorded %q, with its query or user information", d.URL)
		}
	}

	// The sign-in's findings keep out the same tokens: its methods fail and
	// panic with words that hold one.
	leaky := leakySignIn{NewFakeSignIn("leaky"), secret}
	leaky.Add(secret, ada)
	errs = record(t, func(rt T) {
		Identity(rt, n, leaky, []string{secret}, []string{UnknownToken})
	})
	for _, m := range []string{"SignInStatus", "BeginSignIn"} {
		if !said(errs, `exttest: SignIn "leaky": `+m+` panicked: refused [token] ([token], [token])`) {
			t.Errorf("%s's panic was not said with its token stood in for: %q", m, errs)
		}
	}
	for _, e := range errs {
		for _, kept := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if strings.Contains(e, kept) {
				t.Errorf("the suite printed %q: %s", kept, e)
			}
		}
	}

	// The sign-in's first call, its name, keeps them out too: a provider
	// whose name, once it has authenticated, panics with the token or is
	// words that hold it.
	for _, c := range []struct {
		panics bool
		want   string
	}{
		{true, `exttest: SignIn "?": Name panicked: refused [token] ([token], [token])`},
		{false, `exttest: SignIn "refused [token] ([token], [token])": Name "refused [token] ([token], [token])" is not a provider's name`},
	} {
		named := nameLeak{leakySignIn{NewFakeSignIn("leaky"), secret}, c.panics, new(atomic.Bool)}
		named.Add(secret, ada)
		errs = record(t, func(rt T) {
			Identity(rt, n, named, []string{secret}, []string{UnknownToken})
		})
		if !said(errs, c.want) {
			t.Errorf("the sign-in's name was not said with its token stood in for (panics %v): %q", c.panics, errs)
		}
		for _, e := range errs {
			for _, kept := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				if strings.Contains(e, kept) {
					t.Errorf("the suite printed %q: %s", kept, e)
				}
			}
		}
	}

	// Two tokens, one the start of the other, the shorter given first: the
	// longer is stood in for whole, in each of its forms, and nothing of
	// what follows the shorter in it is printed.
	const short, long = "tok-pair", "tok-pair+tailpart/="
	pair := leakySignIn{NewFakeSignIn("leaky"), long}
	pair.Add(short, ada)
	pair.Add(long, ada)
	errs = record(t, func(rt T) {
		Identity(rt, n, pair, []string{short, long}, []string{UnknownToken})
	})
	if !said(errs, `exttest: SignIn "leaky": SignInStatus panicked: refused [token] ([token], [token])`) {
		t.Errorf("the longer token was not stood in for whole: %q", errs)
	}
	for _, e := range errs {
		for _, kept := range []string{short, long, url.QueryEscape(long), url.PathEscape(long), "tailpart"} {
			if strings.Contains(e, kept) {
				t.Errorf("the suite printed %q: %s", kept, e)
			}
		}
	}
}

// A signed-in who that shows nothing once cleaned as the server cleans it
// (without Unicode's Cf, Zl and Zp, trimmed of white space at both ends)
// is one finding of the sign-in's check; one that shows something is none.
func TestSignInBlankWho(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, who string
		found     bool
	}{
		{"a zero-width space", string(rune(0x200b)), true},
		{"spaces", "   ", true},
		{"a soft hyphen", string(rune(0xad)), true},
		{"a name beside a soft hyphen", "ana" + string(rune(0xad)), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := NewFakeSignIn("blank-signin")
			st := ext.SignInStatus{State: ext.SignedIn, Who: c.who}
			f.SetStatus(st)
			f.OnBegin(st)
			f.OnSignOut(st)
			errs := record(t, func(rt T) { SignIn(rt, NewNetwork(), f) })
			want := 0
			if c.found {
				want = 1
				if !said(errs, `exttest: SignIn "blank-signin": SignInStatus answered a signed-in who that shows nothing once cleaned of Unicode's Cf, Zl and Zp and of white space at both ends: the server does not take it`) {
					t.Errorf("the blank who was not said: %q", errs)
				}
			}
			if len(errs) != want {
				t.Errorf("%d findings, want %d: %q", len(errs), want, errs)
			}
		})
	}
}

// nameLeak is a leakySignIn whose name, once it has authenticated a
// token, panics with words that hold its token (panics) or is those words.
type nameLeak struct {
	leakySignIn
	panics bool
	authed *atomic.Bool
}

func (p nameLeak) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	p.authed.Store(true)
	return p.FakeSignIn.Authenticate(ctx, tok)
}

func (p nameLeak) Name() string {
	switch {
	case !p.authed.Load():
		return "leaky"
	case p.panics:
		panic(p.words())
	}
	return p.words()
}

// leakySignIn's sign-in fails and panics with words that hold token, in
// each of its forms.
type leakySignIn struct {
	*FakeSignIn
	token string
}

func (p leakySignIn) words() string {
	return fmt.Sprintf("refused %s (%s, %s)", p.token, url.QueryEscape(p.token), url.PathEscape(p.token))
}

func (p leakySignIn) SignInStatus() ext.SignInStatus { panic(p.words()) }
func (p leakySignIn) BeginSignIn(context.Context) error {
	panic(p.words())
}
func (p leakySignIn) SignOut(context.Context) error { return errors.New(p.words()) }

// refusedStarter's Start begins a goroutine whose Launch its host holds,
// and returns once the host holds it; the goroutine hands back what the
// Launch answered.
type refusedStarter struct {
	*FakeIdentity
	mu    sync.Mutex
	hosts []*FakeHost
	got   chan error
}

func (p *refusedStarter) Start(ctx context.Context, h ext.Host) error {
	fh := h.(*FakeHost)
	p.mu.Lock()
	p.hosts = append(p.hosts, fh)
	p.mu.Unlock()
	go func() {
		_, err := h.Launch(ctx, ext.LaunchRequest{Crew: "nightly", By: ada})
		p.got <- err
	}()
	return Await(ctx, fh, func() bool { return fh.Waiting() == 1 })
}

// TestStarterDeliversTheRefusals checks that Starter goes on only once
// every call the closed host held has been answered: when it returns, no
// host it made holds a call, and each held Launch was answered the closed
// host's refusal.
func TestStarterDeliversTheRefusals(t *testing.T) {
	t.Parallel()
	n := NewNetwork()
	p := &refusedStarter{FakeIdentity: NewFakeIdentity("refused"), got: make(chan error, 2)}
	if errs := record(t, func(rt T) { Starter(rt, n, p) }); len(errs) != 0 {
		t.Errorf("Starter failed a Start that returns at once: %q", errs)
	}
	p.mu.Lock()
	hosts := append([]*FakeHost(nil), p.hosts...)
	p.mu.Unlock()
	if len(hosts) != 2 {
		t.Fatalf("Start was tried %d times, want 2", len(hosts))
	}
	for i, h := range hosts {
		if !h.Closed() || h.Waiting() != 0 {
			t.Errorf("try %d's host, once Starter returned: closed %v, holding %d calls; want closed, holding none", i+1, h.Closed(), h.Waiting())
		}
	}
	ctx := waitCtx(t)
	for i := range hosts {
		select {
		case err := <-p.got:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("a held Launch answered %v, not the closed host's refusal", err)
			}
		case <-ctx.Done():
			t.Fatalf("held Launch %d was never answered", i+1)
		}
	}
}

// TestNetworkHoldsAConnection checks that no byte crosses a connection
// DialContext made while the network is blocked: a Write waits, a Read
// that took bytes from the wire hands them over only once the gate opens,
// and Close lets a held one go.
func TestNetworkHoldsAConnection(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			accepted <- c
		}
	}()
	n := NewNetwork()
	conn, err := n.DialContext(mayWait(context.Background(), "conn"), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := waitCtx(t)
	held := func(t *testing.T) {
		t.Helper()
		select {
		case <-n.whenWaiting("conn"):
		case <-ctx.Done():
			t.Fatal("no Read or Write of the connection ever waited on the shut gate")
		}
	}
	var svc net.Conn
	select {
	case svc = <-accepted:
	case <-ctx.Done():
		t.Fatal("the service never accepted the connection")
	}
	defer svc.Close()

	// Open: bytes cross.
	if _, err := conn.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(svc, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	// Blocked: a Write waits until Unblock.
	n.Block()
	wrote := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("b"))
		wrote <- err
	}()
	held(t)
	select {
	case err := <-wrote:
		t.Fatalf("a Write crossed the blocked network: %v", err)
	default:
	}
	n.Unblock()
	if err := <-wrote; err != nil {
		t.Fatalf("a Write let go answered %v", err)
	}
	if _, err := io.ReadFull(svc, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	// Blocked: what the service sends is handed over only once the gate opens.
	n.Block()
	read := make(chan string, 1)
	go func() {
		b := make([]byte, 1)
		k, _ := conn.Read(b)
		read <- string(b[:k])
	}()
	if _, err := svc.Write([]byte("c")); err != nil {
		t.Fatal(err)
	}
	held(t)
	select {
	case got := <-read:
		t.Fatalf("a Read handed over %q with the network blocked", got)
	default:
	}
	n.Unblock()
	if got := <-read; got != "c" {
		t.Errorf("a Read let go answered %q, want c", got)
	}

	// Blocked: a deadline set while a Read waits lets it go, as net.Conn's
	// does, and so does one that passes while a Write waits.
	n.Block()
	timedOut := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		timedOut <- err
	}()
	held(t)
	if err := conn.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	timeout := func(err error, what string) {
		t.Helper()
		var ne net.Error
		if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &ne) || !ne.Timeout() {
			t.Errorf("a held %s whose deadline passed answered %v, want os.ErrDeadlineExceeded", what, err)
		}
	}
	let := func(ch <-chan error, what string) {
		t.Helper()
		select {
		case err := <-ch:
			timeout(err, what)
		case <-ctx.Done():
			t.Fatalf("a held %s whose deadline passed was never let go", what)
		}
	}
	let(timedOut, "Read")
	if err := conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := conn.Write([]byte("d"))
		timedOut <- err
	}()
	let(timedOut, "Write")
	// Its deadlines cleared, the connection carries bytes again once open.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	n.Unblock()
	if _, err := conn.Write([]byte("e")); err != nil {
		t.Fatalf("a Write after the deadlines were cleared answered %v", err)
	}
	if b := make([]byte, 1); func() error { _, err := io.ReadFull(svc, b); return err }() != nil || string(b) != "e" {
		t.Errorf("the service did not get the byte written after the deadlines were cleared: %q", b)
	}

	// Blocked: Close lets a held Read go.
	n.Block()
	failed := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		failed <- err
	}()
	held(t)
	conn.Close()
	if err := <-failed; !errors.Is(err, net.ErrClosed) {
		t.Errorf("a held Read of a closed connection answered %v, want net.ErrClosed", err)
	}
	n.Unblock()
	<-n.settled()
}

// The benchmarks measure what a check never does: how long a call takes.
// A provider's repository writes its own beside its contract test, as
// these do for the defaults and the fakes.

func BenchmarkDefaults(b *testing.B) {
	ctx := context.Background()
	d := SampleDecision()
	b.Run("Authenticate", func(b *testing.B) {
		for b.Loop() {
			local.Identity{}.Authenticate(ctx, UnknownToken)
		}
	})
	b.Run("Route", func(b *testing.B) {
		for b.Loop() {
			local.Router{}.Route(ctx, d)
		}
	})
	b.Run("ICEServers", func(b *testing.B) {
		for b.Loop() {
			local.Media{}.ICEServers(ctx, ext.ICERequest{Purpose: "viewer"})
		}
	})
}

func BenchmarkFakes(b *testing.B) {
	ctx := context.Background()
	id := NewFakeIdentity("fake-identity")
	id.Add("tok", ada)
	cx := NewFakeContext("fake-context")
	cx.SetBlocks([]ext.ContextBlock{{Title: "t", Body: "b"}})
	q := SampleQuery(b.TempDir())
	b.Run("Authenticate", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				id.Authenticate(ctx, "tok")
			}
		})
	})
	b.Run("Context", func(b *testing.B) {
		for b.Loop() {
			cx.Context(ctx, q)
		}
	})
}
