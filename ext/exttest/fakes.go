package exttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

// Every fake implements its interface.
var (
	_ ext.IdentityProvider  = (*FakeIdentity)(nil)
	_ ext.DecisionRouter    = (*FakeRouter)(nil)
	_ ext.ContextProvider   = (*FakeContext)(nil)
	_ ext.NotificationSink  = (*FakeSink)(nil)
	_ ext.ContinuityStore   = (*FakeContinuity)(nil)
	_ ext.EntitlementSource = (*FakeEntitlements)(nil)
	_ ext.MediaRoomProvider = (*FakeMedia)(nil)
	_ ext.Runtime           = (*FakeRuntime)(nil)
	_ ext.Host              = (*FakeHost)(nil)
)

// changes is what a fake that records embeds: Changed, closed at the next
// record, so a test waits on the fake rather than on a clock (Await).
type changes struct {
	cmu sync.Mutex
	ch  chan struct{}
}

// Every accessor of what a fake recorded answers copies, nested slices
// too, so that a test that changes what it was answered changes nothing
// the fake holds or answers later.

// Changed is closed at the fake's next record (a call it was given, a
// script changed): take it, read what the fake holds, and wait on it when
// that is not yet enough, as Await does.
func (c *changes) Changed() <-chan struct{} {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if c.ch == nil {
		c.ch = make(chan struct{})
	}
	return c.ch
}

func (c *changes) tell() {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if c.ch != nil {
		close(c.ch)
		c.ch = nil
	}
}

// Await waits until cond holds, asking it again at every change of f (a
// fake, or anything with Changed), or until ctx ends (its error).
func Await(ctx context.Context, f interface{ Changed() <-chan struct{} }, cond func() bool) error {
	for {
		ch := f.Changed()
		if cond() {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// FakeIdentity is an identity provider that maps tokens to principals.
// The empty token is never one of them.
type FakeIdentity struct {
	changes
	name string

	mu     sync.Mutex
	tokens map[string]ext.Principal
	err    error
	calls  int
}

// NewFakeIdentity is a FakeIdentity named name that knows no token.
func NewFakeIdentity(name string) *FakeIdentity {
	return &FakeIdentity{name: name, tokens: map[string]ext.Principal{}}
}

// Name answers the fake's name.
func (f *FakeIdentity) Name() string { return f.name }

// Add has the fake admit token as p; an empty token is never admitted.
func (f *FakeIdentity) Add(token string, p ext.Principal) {
	f.mu.Lock()
	if token != "" {
		f.tokens[token] = copyPrincipal(p)
	}
	f.mu.Unlock()
	f.tell()
}

// Remove has the fake no longer admit token: ok false, a revocation.
func (f *FakeIdentity) Remove(token string) {
	f.mu.Lock()
	delete(f.tokens, token)
	f.mu.Unlock()
	f.tell()
}

// SetErr has every Authenticate answer err (nil: none), as a provider that
// cannot tell does.
func (f *FakeIdentity) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// Calls answers how many times Authenticate was asked. The tokens it was
// asked about are not kept, as a provider keeps none.
func (f *FakeIdentity) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Authenticate answers the principal token maps to; ok false for one it
// does not know and for the empty token; the error set, if any.
func (f *FakeIdentity) Authenticate(_ context.Context, token string) (ext.Principal, bool, error) {
	f.mu.Lock()
	f.calls++
	p, ok := f.tokens[token]
	err := f.err
	f.mu.Unlock()
	f.tell()
	switch {
	case err != nil:
		return ext.Principal{}, false, err
	case !ok: // the empty token is never added
		return ext.Principal{}, false, nil
	}
	return copyPrincipal(p), true, nil
}

// FakeRouter is a decision router that records the decisions it is given
// and their resolutions, and answers the route scripted for a question's
// id, the empty route for the rest.
type FakeRouter struct {
	changes
	name string

	mu          sync.Mutex
	routes      map[string]ext.Route
	err         error
	decisions   []ext.Decision
	resolutions []ext.Resolution
}

// NewFakeRouter is a FakeRouter named name that answers the empty route.
func NewFakeRouter(name string) *FakeRouter {
	return &FakeRouter{name: name, routes: map[string]ext.Route{}}
}

// Name answers the fake's name.
func (f *FakeRouter) Name() string { return f.name }

// SetRoute has Route answer r for the question id.
func (f *FakeRouter) SetRoute(id string, r ext.Route) {
	f.mu.Lock()
	f.routes[id] = copyRoute(r)
	f.mu.Unlock()
	f.tell()
}

// SetErr has every Route answer err (nil: none).
func (f *FakeRouter) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// Route records d and answers the route set for its id.
func (f *FakeRouter) Route(_ context.Context, d ext.Decision) (ext.Route, error) {
	f.mu.Lock()
	f.decisions = append(f.decisions, copyDecision(d))
	r, err := copyRoute(f.routes[d.ID]), f.err
	f.mu.Unlock()
	f.tell()
	if err != nil {
		return ext.Route{}, err
	}
	return r, nil
}

// Resolved records r.
func (f *FakeRouter) Resolved(_ context.Context, r ext.Resolution) {
	f.mu.Lock()
	f.resolutions = append(f.resolutions, r)
	f.mu.Unlock()
	f.tell()
}

// Decisions answers the decisions Route was given, in order.
func (f *FakeRouter) Decisions() []ext.Decision {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyAll(f.decisions, copyDecision)
}

// Resolutions answers the resolutions Resolved was told, in order.
func (f *FakeRouter) Resolutions() []ext.Resolution {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ext.Resolution(nil), f.resolutions...)
}

// FakeContext is a context provider that records the touches and the
// queries it is given and answers the blocks scripted for every query.
type FakeContext struct {
	changes
	name string

	mu      sync.Mutex
	blocks  []ext.ContextBlock
	err     error
	touches []ext.TouchedFile
	queries []ext.ContextQuery
}

// NewFakeContext is a FakeContext named name that answers no block.
func NewFakeContext(name string) *FakeContext { return &FakeContext{name: name} }

// Name answers the fake's name.
func (f *FakeContext) Name() string { return f.name }

// SetBlocks has Context answer bs, for every query.
func (f *FakeContext) SetBlocks(bs []ext.ContextBlock) {
	f.mu.Lock()
	f.blocks = append([]ext.ContextBlock(nil), bs...)
	f.mu.Unlock()
	f.tell()
}

// SetErr has every Context answer err (nil: none).
func (f *FakeContext) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// NoteTouch records t.
func (f *FakeContext) NoteTouch(_ context.Context, t ext.TouchedFile) {
	f.mu.Lock()
	f.touches = append(f.touches, t)
	f.mu.Unlock()
	f.tell()
}

// Context records q and answers the blocks set.
func (f *FakeContext) Context(_ context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	f.mu.Lock()
	f.queries = append(f.queries, copyQuery(q))
	bs, err := append([]ext.ContextBlock(nil), f.blocks...), f.err
	f.mu.Unlock()
	f.tell()
	if err != nil {
		return nil, err
	}
	return bs, nil
}

// Touches answers the touches NoteTouch was told, in order.
func (f *FakeContext) Touches() []ext.TouchedFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ext.TouchedFile(nil), f.touches...)
}

// Queries answers the queries Context was asked, in order.
func (f *FakeContext) Queries() []ext.ContextQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyAll(f.queries, copyQuery)
}

// FakeSink is a notification sink that records what it is handed and,
// while blocked, holds each Notify until it is let go or its context ends.
type FakeSink struct {
	changes
	name   string
	events []string

	mu      sync.Mutex
	held    []ext.Notification
	err     error
	gate    chan struct{} // non-nil while blocked
	waiting int
}

// NewFakeSink is a FakeSink named name that takes events (nil: every
// entry), as its Events answers them every time.
func NewFakeSink(name string, events []string) *FakeSink {
	var evs []string
	if events != nil {
		evs = append([]string{}, events...)
	}
	return &FakeSink{name: name, events: evs}
}

// Name answers the fake's name.
func (f *FakeSink) Name() string { return f.name }

// Events answers what the sink takes: the list it was made with (nil, every
// entry), the same every time.
func (f *FakeSink) Events() []string {
	if f.events == nil {
		return nil
	}
	return append([]string{}, f.events...)
}

// SetErr has every Notify answer err (nil: none) once it is let go.
func (f *FakeSink) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// Block has every Notify from now on wait, recorded, until Unblock or its
// context's end, as a sink whose service hangs.
func (f *FakeSink) Block() {
	f.mu.Lock()
	if f.gate == nil {
		f.gate = make(chan struct{})
	}
	f.mu.Unlock()
	f.tell()
}

// Unblock lets every waiting Notify go.
func (f *FakeSink) Unblock() {
	f.mu.Lock()
	if f.gate != nil {
		close(f.gate)
		f.gate = nil
	}
	f.mu.Unlock()
	f.tell()
}

// Waiting answers how many Notify calls are held by Block now.
func (f *FakeSink) Waiting() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waiting
}

// Notify records n and answers the error set, at once or, while the sink
// is blocked, once it is let go; ctx's error if ctx ends first.
func (f *FakeSink) Notify(ctx context.Context, n ext.Notification) error {
	f.mu.Lock()
	f.held = append(f.held, n)
	gate := f.gate
	if gate != nil {
		f.waiting++
	}
	f.mu.Unlock()
	f.tell()
	if gate != nil {
		var err error
		select {
		case <-gate:
		case <-ctx.Done():
			err = ctx.Err()
		}
		f.mu.Lock()
		f.waiting--
		f.mu.Unlock()
		f.tell()
		if err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// Notifications answers what Notify was handed, in order.
func (f *FakeSink) Notifications() []ext.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ext.Notification(nil), f.held...)
}

// FakeDoc is one document a FakeContinuity was told of.
type FakeDoc struct {
	Deleted bool // told by Deleted, not Saved
	Doc     ext.Doc
}

// FakeContinuity is a sync store that records the docs it is told of and
// restores a scripted archive: bytes it holds, or a download from a URL
// through a client a test gives it (a Network's Transport, say).
type FakeContinuity struct {
	changes
	name string

	mu       sync.Mutex
	docs     []FakeDoc
	err      error
	archive  []byte
	url      string
	client   *http.Client
	restores []ext.RestoreRequest
}

// NewFakeContinuity is a FakeContinuity named name that keeps nothing to
// restore.
func NewFakeContinuity(name string) *FakeContinuity { return &FakeContinuity{name: name} }

// Name answers the fake's name.
func (f *FakeContinuity) Name() string { return f.name }

// SetErr has every Saved and Deleted answer err (nil: none).
func (f *FakeContinuity) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// SetArchive has Restore write b (nil: ErrNothingToRestore), from what the
// store holds.
func (f *FakeContinuity) SetArchive(b []byte) {
	f.mu.Lock()
	f.archive, f.url, f.client = nil, "", nil
	if b != nil {
		f.archive = append(make([]byte, 0, len(b)), b...)
	}
	f.mu.Unlock()
	f.tell()
}

// RestoreFrom has Restore download url with c under its own context and
// stream the body into its writer, as a store fetching its copy from its
// service does.
func (f *FakeContinuity) RestoreFrom(url string, c *http.Client) {
	f.mu.Lock()
	f.archive, f.url, f.client = nil, url, c
	f.mu.Unlock()
	f.tell()
}

// Saved records d (a copy of its bytes).
func (f *FakeContinuity) Saved(_ context.Context, d ext.Doc) error { return f.record(false, d) }

// Deleted records d.
func (f *FakeContinuity) Deleted(_ context.Context, d ext.Doc) error { return f.record(true, d) }

func (f *FakeContinuity) record(deleted bool, d ext.Doc) error {
	f.mu.Lock()
	f.docs = append(f.docs, FakeDoc{Deleted: deleted, Doc: copyDoc(d)})
	err := f.err
	f.mu.Unlock()
	f.tell()
	return err
}

// Docs answers the docs Saved and Deleted were told of, in order.
func (f *FakeContinuity) Docs() []FakeDoc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyAll(f.docs, func(d FakeDoc) FakeDoc { d.Doc = copyDoc(d.Doc); return d })
}

// Restores answers the requests Restore was given, in order.
func (f *FakeContinuity) Restores() []ext.RestoreRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ext.RestoreRequest(nil), f.restores...)
}

// Restore writes the archive set into w, or downloads the URL set and
// streams its body into w, ending with ctx (its error); with neither,
// ErrNothingToRestore.
func (f *FakeContinuity) Restore(ctx context.Context, r ext.RestoreRequest, w io.Writer) error {
	f.mu.Lock()
	f.restores = append(f.restores, r)
	archive, url, client := f.archive, f.url, f.client
	f.mu.Unlock()
	f.tell()
	switch {
	case url != "":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("exttest: the archive's download answered %s", resp.Status)
		}
		if _, err := io.Copy(w, resp.Body); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		return nil
	case archive != nil:
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := w.Write(archive)
		return err
	}
	return ext.ErrNothingToRestore
}

// FakeEntitlements is an entitlement source that answers what a test sets
// per feature, and records the features it is asked about. A feature set
// to nothing is not allowed.
type FakeEntitlements struct {
	changes
	name string

	mu       sync.Mutex
	features map[string]ext.Entitlement
	err      error
	asked    []string
}

// NotSetWhy is FakeEntitlements' why for a feature it was told nothing of.
const NotSetWhy = "not allowed: exttest's fake was told nothing of this feature"

// NewFakeEntitlements is a FakeEntitlements named name that allows nothing.
func NewFakeEntitlements(name string) *FakeEntitlements {
	return &FakeEntitlements{name: name, features: map[string]ext.Entitlement{}}
}

// Name answers the fake's name.
func (f *FakeEntitlements) Name() string { return f.name }

// Set has Entitled answer e for feature.
func (f *FakeEntitlements) Set(feature string, e ext.Entitlement) {
	f.mu.Lock()
	f.features[feature] = e
	f.mu.Unlock()
	f.tell()
}

// SetErr has every Entitled answer err (nil: none).
func (f *FakeEntitlements) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// Entitled records feature and answers what was set for it, or not
// allowed, NotSetWhy.
func (f *FakeEntitlements) Entitled(_ context.Context, feature string) (ext.Entitlement, error) {
	f.mu.Lock()
	f.asked = append(f.asked, feature)
	e, ok := f.features[feature]
	err := f.err
	f.mu.Unlock()
	f.tell()
	switch {
	case err != nil:
		return ext.Entitlement{}, err
	case !ok:
		return ext.Entitlement{Why: NotSetWhy}, nil
	}
	return e, nil
}

// Asked answers the features Entitled was asked about, in order.
func (f *FakeEntitlements) Asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// FakeMedia is a media room provider that answers the ICE servers and the
// room a test sets, and records what it is asked.
type FakeMedia struct {
	changes
	name string

	mu      sync.Mutex
	servers []ext.ICEServer
	room    ext.RoomJoin
	hasRoom bool
	err     error
	ice     []ext.ICERequest
	rooms   []ext.RoomRequest
}

// NewFakeMedia is a FakeMedia named name that adds no server and has no
// room.
func NewFakeMedia(name string) *FakeMedia { return &FakeMedia{name: name} }

// Name answers the fake's name.
func (f *FakeMedia) Name() string { return f.name }

// SetServers has ICEServers answer ss.
func (f *FakeMedia) SetServers(ss []ext.ICEServer) {
	f.mu.Lock()
	f.servers = copyServers(ss)
	f.mu.Unlock()
	f.tell()
}

// SetRoom has Room answer j and ok.
func (f *FakeMedia) SetRoom(j ext.RoomJoin, ok bool) {
	f.mu.Lock()
	f.room, f.hasRoom = j, ok
	f.mu.Unlock()
	f.tell()
}

// SetErr has every ICEServers and Room answer err (nil: none).
func (f *FakeMedia) SetErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	f.tell()
}

// ICEServers records r and answers the servers set.
func (f *FakeMedia) ICEServers(_ context.Context, r ext.ICERequest) ([]ext.ICEServer, error) {
	f.mu.Lock()
	f.ice = append(f.ice, r)
	ss, err := copyServers(f.servers), f.err
	f.mu.Unlock()
	f.tell()
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// Room records r and answers the room set.
func (f *FakeMedia) Room(_ context.Context, r ext.RoomRequest) (ext.RoomJoin, bool, error) {
	r.By = copyPrincipal(r.By)
	f.mu.Lock()
	f.rooms = append(f.rooms, r)
	j, ok, err := f.room, f.hasRoom, f.err
	f.mu.Unlock()
	f.tell()
	if err != nil {
		return ext.RoomJoin{}, false, err
	}
	return j, ok, nil
}

// ICERequests answers what ICEServers was asked, in order.
func (f *FakeMedia) ICERequests() []ext.ICERequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ext.ICERequest(nil), f.ice...)
}

// RoomRequests answers what Room was asked, in order.
func (f *FakeMedia) RoomRequests() []ext.RoomRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyAll(f.rooms, func(r ext.RoomRequest) ext.RoomRequest { r.By = copyPrincipal(r.By); return r })
}

// FakeRuntime is a runtime that answers the Where a test sets, and
// records and answers the interface's other methods as runtime.go says.
type FakeRuntime struct {
	changes
	name string

	mu     sync.Mutex
	where  ext.Where
	script runtimeScript // runtime.go
}

// NewFakeRuntime is a FakeRuntime named name that answers w.
func NewFakeRuntime(name string, w ext.Where) *FakeRuntime {
	return &FakeRuntime{name: name, where: w}
}

// Name answers the fake's name.
func (f *FakeRuntime) Name() string { return f.name }

// SetWhere has Where answer w.
func (f *FakeRuntime) SetWhere(w ext.Where) {
	f.mu.Lock()
	f.where = w
	f.mu.Unlock()
}

// Where answers the Where set.
func (f *FakeRuntime) Where() ext.Where {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.where
}

// copyAll is a copy of xs, each element copied by each; nil stays nil.
func copyAll[E any](xs []E, each func(E) E) []E {
	if xs == nil {
		return nil
	}
	out := make([]E, len(xs))
	for i, x := range xs {
		out[i] = each(x)
	}
	return out
}

func copyDecision(d ext.Decision) ext.Decision {
	d.Options = append([]ext.DecisionOption(nil), d.Options...)
	return d
}

func copyQuery(q ext.ContextQuery) ext.ContextQuery {
	q.Inputs = append([]ext.ContextInput(nil), q.Inputs...)
	return q
}

func copyDoc(d ext.Doc) ext.Doc {
	d.Data = append([]byte(nil), d.Data...)
	return d
}

func copyPrincipal(p ext.Principal) ext.Principal {
	p.Grants = append([]ext.Grant(nil), p.Grants...)
	return p
}

func copyRoute(r ext.Route) ext.Route {
	r.To = append([]ext.Destination(nil), r.To...)
	if r.Answer != nil {
		a := *r.Answer
		a.By = copyPrincipal(a.By)
		r.Answer = &a
	}
	return r
}

func copyServers(ss []ext.ICEServer) []ext.ICEServer {
	if ss == nil {
		return nil
	}
	out := make([]ext.ICEServer, len(ss))
	for i, s := range ss {
		s.URLs = append([]string(nil), s.URLs...)
		out[i] = s
	}
	return out
}
