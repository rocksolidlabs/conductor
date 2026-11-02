package exttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
// own goroutine, begun in Start.
func TestSuitePassesEveryFake(t *testing.T) {
	n := NewNetwork()
	srv := keyService(t)
	id := &keyedIdentity{FakeIdentity: NewFakeIdentity("fake-identity"), client: &http.Client{Transport: n.Transport()}, url: srv.URL}
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
	router.SetRoute(d.ID, ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}, Rule: "owns main.go", Answer: &ext.Answer{Input: "1", By: ext.Principal{Subject: "ada", Name: "Ada"}}})
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

	p := ext.Providers{
		Identity: id, Router: router, Context: []ext.ContextProvider{cx},
		Notifications: []ext.NotificationSink{sink, NewFakeSink("fake-all", nil)},
		Continuity:    sync, Media: media, Runtime: rt,
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "fake-identity", Value: id},
			{Kind: ext.KindEntitlements, Name: "fake-plans", Value: plans},
		},
	}
	errs := record(t, func(t T) {
		RunAll(t, n, p)
		Identity(t, n, id, []string{"tok-admin"}, []string{UnknownToken})
	})
	for _, e := range errs {
		t.Error(e)
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

// panicMedia panics in ICEServers.
type panicMedia struct{ *FakeMedia }

func (panicMedia) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	panic("boom")
}

// fickleSink answers another list of events every time.
type fickleSink struct {
	*FakeSink
	n atomic.Int32
}

func (s *fickleSink) Events() []string { return []string{fmt.Sprint("e", s.n.Add(1))} }

// TestSuiteCatchesABreach plants one breach of a rule in each provider and
// checks that the suite fails it, naming the check and the rule, through a
// recording T. The waits are the guard's own (IdentityWait, StartWait):
// the cases run in parallel so the test takes the longest of them.
func TestSuiteCatchesABreach(t *testing.T) {
	srv := keyService(t)
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
		{"ok for the empty token", func(t T, n *Network) {
			Identity(t, n, emptyIdentity{}, nil, nil)
		}, []string{`exttest: Identity "lenient": Authenticate said ok for the empty token: nothing is recognised for an empty token`}},
		{"nine blocks", func(t T, n *Network) {
			Context(t, n, newNineBlocks(), SampleQuery(t.TempDir()))
		}, []string{`exttest: Context "nine": Context answered 9 blocks`, "MaxContextBlocks (8)"}},
		{"a Start that waits on its host", func(t T, n *Network) {
			Starter(t, n, hostStarter{NewFakeIdentity("starter")})
		}, []string{`exttest: Starter "starter": Start (try 1) did not return within StartWait (5s) with its host and the network blocked`}},
		{"a route its decision does not offer", func(t T, n *Network) {
			Router(t, n, strayRouter{NewFakeRouter("stray")}, SampleDecision())
		}, []string{`exttest: Router "stray": Route answered a route not valid for its decision`, `"9"`}},
		{"a panic", func(t T, n *Network) {
			Media(t, n, panicMedia{NewFakeMedia("panicky")})
		}, []string{`exttest: Media "panicky": ICEServers panicked: boom`}},
		{"Events that change", func(t T, n *Network) {
			Sink(t, n, &fickleSink{FakeSink: NewFakeSink("fickle", nil)})
		}, []string{`exttest: Sink "fickle": Events answered`, "must not change"}},
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
