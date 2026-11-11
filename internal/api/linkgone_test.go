package api

import (
	"bufio"
	"context"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// endedPair is a switchyard that keeps links, open hosts on and bound
// links each, and a server publishing to it with the host token (the
// serve command's uplink): told is each link_gone the publisher was handed,
// once it handled it.
func endedPair(t *testing.T, bound int) (yard, local *testEnv, told chan string) {
	t.Helper()
	yard = keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), func(c *config.Config) { c.Switchyard.OpenHostLinks = bound })
	yard.srv.hosts.SetDecisions(true)
	local = newTestEnv(t, nil)
	told = make(chan string, 16)
	local.srv.SetPublisher(uplinkPublisher{&hostagent.Uplink{ServerURL: yard.http.URL, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: func(linkID, sessionID, runID string, ended bool) {
			local.srv.RemoteLinkGone(linkID, sessionID, runID, ended)
			told <- linkID
		}, LinkUnnamed: local.srv.RemoteLinkUnnamed}})
	return yard, local, told
}

// publishedSession starts a session on local and waits for its publication.
func publishedSession(t *testing.T, local *testEnv) string {
	t.Helper()
	id := local.createSession("cat")
	until(t, "the session published", func() bool { return local.srv.publishedOf(id) != nil })
	return id
}

// mintRemote mints a link at path (a session's or a run's links route) of
// local, which the switchyard mints: its id.
func mintRemote(t *testing.T, local *testEnv, path string) string {
	t.Helper()
	resp, out := local.do("POST", path, adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a link at %s: %d %v", path, resp.StatusCode, out)
	}
	return out["link"].(map[string]any)["id"].(string)
}

// admitOpen admits the instance that published session id at yard as an
// open host, as another of its sessions registering with no token does
// (hostUp, the admission's step), until the test ends.
func admitOpen(t *testing.T, yard, local *testEnv, id string) {
	t.Helper()
	hs, ok := yard.srv.hosts.Get(local.srv.publishedOf(id).CurrentID())
	if !ok {
		t.Fatal("the switchyard has no such session")
	}
	t.Cleanup(yard.srv.hostUp(hs.Owner(), "203.0.113.7"))
}

// heard is the next link the publisher was told went.
func heard(t *testing.T, told chan string) string {
	t.Helper()
	select {
	case id := <-told:
		return id
	case <-time.After(testWait):
		t.Fatal("the publisher was told of no link going")
		return ""
	}
}

// linkIDs are the ids the links route path lists.
func linkIDs(t *testing.T, e *testEnv, path string) []string {
	t.Helper()
	var ids []string
	for _, l := range linksOf(t, e, path) {
		ids = append(ids, l["id"].(string))
	}
	slices.Sort(ids)
	return ids
}

// The links of a publisher that its switchyard ends at an open admission
// of the publisher's instance, past its bound (#820), unused: the
// session's link leaves its Share list and the run's link the run's links,
// without the publication registering again, and the newer links stay. A
// link_gone for a link the publisher never held, on the session's
// connection and on the run's member's, changes nothing.
func TestLinksEndedAtAnOpenAdmissionLeaveThePublishersLists(t *testing.T) {
	yard, local, told := endedPair(t, 2)
	id := publishedSession(t, local)
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	sessionPath, runPath := "/api/sessions/"+id+"/links", "/api/runs/"+runID+"/links"
	oldSession := mintRemote(t, local, sessionPath)
	oldRun := mintRemote(t, local, runPath)
	keptSession := mintRemote(t, local, sessionPath)
	keptRun := mintRemote(t, local, runPath)

	admitOpen(t, yard, local, id)
	until(t, "the ended links left the publisher's lists", func() bool {
		return slices.Equal(linkIDs(t, local, sessionPath), []string{keptSession}) && slices.Equal(linkIDs(t, local, runPath), []string{keptRun})
	})
	got := []string{heard(t, told), heard(t, told)}
	slices.Sort(got)
	want := []string{oldRun, oldSession}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("told %v went, want the session's and the run's ended links %v", got, want)
	}

	// A link it never held, told on both connections.
	run, _ := local.srv.runs.Get(runID)
	for _, sid := range []string{id, run.Members[0].SessionID} {
		hs, _ := yard.srv.hosts.Get(local.srv.publishedOf(sid).CurrentID())
		hs.DisconnectHeldLink("never-held-0123456789")
		if got := heard(t, told); got != "never-held-0123456789" {
			t.Fatalf("told %s went", got)
		}
	}
	local.srv.WaitBackground(t.Context())
	if got := linkIDs(t, local, sessionPath); !slices.Equal(got, []string{keptSession}) {
		t.Fatalf("the session's links after a link it never held went: %v", got)
	}
	if got := linkIDs(t, local, runPath); !slices.Equal(got, []string{keptRun}) {
		t.Fatalf("the run's links after a link it never held went: %v", got)
	}
}

// A run link the switchyard ends as its mint is still being answered here
// (its link_gone right behind its link_created, on the member's
// connection) is forgotten after the mint has recorded it: the forget
// waits for the run's sync turn, which the mint holds until it has. One
// for another run, or for a session that is no member, forgets nothing.
func TestARunLinkEndedAsItIsMintedIsForgottenOnceRecorded(t *testing.T) {
	local := newTestEnv(t, nil)
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	member := local.waitRunning(t, runID, "lead")
	local.srv.recordRemoteRunLink(runID, proto.LinkCreated{LinkID: "kept-link", Role: "view"}, false)

	waiting := make(chan string, 4)
	local.srv.runLinkEndedSeam = func(runID string) { waiting <- runID }

	unlock := local.srv.lockRunSync(runID) // the mint's turn
	local.srv.RemoteLinkGone("kept-link", sessionHex("not-a-member"), "", true)
	local.srv.RemoteLinkGone("kept-link", "", "another-run", true)
	local.srv.RemoteLinkGone("minted-link", member, "", true)
	var turns []string
	for range 2 {
		select {
		case got := <-waiting:
			turns = append(turns, got)
		case <-time.After(testWait):
			t.Fatalf("the forgets waiting for a run's turn: %v", turns)
		}
	}
	if !slices.Contains(turns, runID) || !slices.Contains(turns, "another-run") {
		t.Fatalf("the forgets wait for the turns of %v", turns)
	}
	local.srv.recordRemoteRunLink(runID, proto.LinkCreated{LinkID: "minted-link", Role: "view"}, false)
	unlock()
	until(t, "the ended link forgotten", func() bool {
		return slices.Equal(linkIDs(t, local, "/api/runs/"+runID+"/links"), []string{"kept-link"})
	})
	local.srv.WaitBackground(t.Context())
	if got := linkIDs(t, local, "/api/runs/"+runID+"/links"); !slices.Equal(got, []string{"kept-link"}) {
		t.Fatalf("the run's links: %v", got)
	}
}

// A decision link the switchyard ends at an open admission is owed its
// revoke there (forgetRemote): its record stays, listed revokePending,
// after the publication's held links no longer name it, until the
// switchyard answers the revoke; the ordinary link newer than it stays.
func TestADecisionLinkEndedAtAnOpenAdmissionKeepsItsRecordOwed(t *testing.T) {
	yard, local, told := endedPair(t, 1)
	next := retries(local)
	id := publishedSession(t, local)
	path := "/api/sessions/" + id + "/links"
	decision, _ := remoteDecisionLink(t, local, path, "phone")
	kept := mintRemote(t, local, path)

	admitOpen(t, yard, local, id)
	if got := heard(t, told); got != decision {
		t.Fatalf("told %s went, not the decision link %s", got, decision)
	}
	pub := local.srv.publishedOf(id)
	until(t, "the publication holds the decision link no more", func() bool {
		held, known := pub.HeldLinks()
		return known && !slices.Contains(held, decision) && slices.Contains(held, kept)
	})
	byID := map[string]map[string]any{}
	for _, l := range linksOf(t, local, path) {
		byID[l["id"].(string)] = l
	}
	if len(byID) != 2 || byID[decision]["revokePending"] != true || byID[kept] == nil {
		t.Fatalf("the session's links, the decision link's revoke not yet answered: %v", byID)
	}
	select {
	case try := <-next:
		try()
	case <-time.After(testWait):
		t.Fatal("no revoke there was armed")
	}
	until(t, "the switchyard answered the revoke", func() bool { return slices.Equal(linkIDs(t, local, path), []string{kept}) })
}

// endingPub is a publication whose mints hand the link they made to at,
// when set, before the server goes on with it: where a link_gone that came
// right behind the link's link_created is taken.
type endingPub struct {
	PublishedSession
	at *atomic.Pointer[func(string)]
}

func (p *endingPub) Decisions() bool { return carriesDecisions(p.PublishedSession) }

func (p *endingPub) Link(ctx context.Context, role string, ttl time.Duration, label string) (proto.LinkCreated, error) {
	res, err := p.PublishedSession.Link(ctx, role, ttl, label)
	handTo(p.at, res, err)
	return res, err
}

func (p *endingPub) RunLink(ctx context.Context, role string, ttl time.Duration, label string, g proto.RunGroup) (proto.LinkCreated, error) {
	res, err := p.PublishedSession.RunLink(ctx, role, ttl, label, g)
	handTo(p.at, res, err)
	return res, err
}

// endingDesk is endingPub for a run's desk.
type endingDesk struct {
	Desk
	at *atomic.Pointer[func(string)]
}

func (d *endingDesk) RunLink(ctx context.Context, role string, ttl time.Duration, label string, g proto.RunGroup) (proto.LinkCreated, error) {
	res, err := d.Desk.RunLink(ctx, role, ttl, label, g)
	handTo(d.at, res, err)
	return res, err
}

func handTo(at *atomic.Pointer[func(string)], res proto.LinkCreated, err error) {
	if f := at.Load(); f != nil && err == nil {
		(*f)(res.LinkID)
	}
}

// The walk (#820): a link the switchyard ends of itself is applied to what
// the link is when the word is taken, whatever moment of its mint the word
// comes at and whichever connection that can list it carries it: the
// session's, the run's desk's, or a member's (the switchyard tells the
// members when the run's desk is away or gone). A decision link is ended
// here (its lane opens nothing), listed owing its revoke, and the revoke is
// carried there again until the switchyard answers; any other link leaves
// the list. The other links of the session and the run stay as they were.
// Judged by the links routes the Share dialog and the run page read.
func TestAnEndedLinkIsAppliedToWhatItIsThen(t *testing.T) {
	yard, local, told := endedPair(t, 2)
	next := retries(local)
	id := publishedSession(t, local)
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	run, _ := local.srv.runs.Get(runID)
	member := run.Members[0].SessionID
	sessionPath, runPath := "/api/sessions/"+id+"/links", "/api/runs/"+runID+"/links"
	keptSession := mintRemote(t, local, sessionPath)
	keptRun := mintRemote(t, local, runPath)
	keptDecision, _ := remoteDecisionLink(t, local, runPath, "kept")

	var at, held atomic.Pointer[func(string)]
	local.srv.pubMu.Lock()
	for _, sid := range []string{id, member} {
		local.srv.published[sid] = &endingPub{PublishedSession: local.srv.published[sid], at: &at}
	}
	rd := local.srv.desks[runID]
	rd.desk = &endingDesk{Desk: rd.desk, at: &at}
	local.srv.remoteHeldSeam = func(linkID string) {
		if f := held.Load(); f != nil {
			(*f)(linkID)
		}
	}
	local.srv.pubMu.Unlock()

	conn := func(sid string) func() *signal.HostedSession {
		return func() *signal.HostedSession {
			hs, _ := yard.srv.hosts.Get(local.srv.publishedOf(sid).CurrentID())
			return hs
		}
	}
	desk := func() *signal.HostedSession {
		d, _ := yard.srv.hosts.Desk(local.srv.liveDesk(runID).CurrentID())
		return d
	}
	listed := func(path, linkID string) map[string]any {
		for _, l := range linksOf(t, local, path) {
			if l["id"] == linkID {
				return l
			}
		}
		return nil
	}
	rows := []struct {
		name   string
		path   string
		decide bool
		when   string // minting: before the mint goes on; held: held, not recorded; made
		conn   func() *signal.HostedSession
	}{
		{"a session's decision link ended as its mint is answered", sessionPath, true, "minting", conn(id)},
		{"a session's decision link ended held and not yet recorded", sessionPath, true, "held", conn(id)},
		{"a session's decision link ended once made", sessionPath, true, "made", conn(id)},
		{"a session's link ended as its mint is answered", sessionPath, false, "minting", conn(id)},
		{"a run's decision link ended on its desk as its mint is answered", runPath, true, "minting", desk},
		{"a run's decision link ended on its desk once made", runPath, true, "made", desk},
		{"a run's decision link ended on its member's connection, its desk away or gone", runPath, true, "made", conn(member)},
		{"a run's link ended on its member's connection as its mint is answered", runPath, false, "minting", conn(member)},
		{"a run's link ended on its desk once made", runPath, false, "made", desk},
		{"a run's link ended on its member's connection, its desk away or gone", runPath, false, "made", conn(member)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			end := func(linkID string) {
				row.conn().DisconnectHeldLink(linkID)
				if got := heard(t, told); got != linkID {
					t.Errorf("told %s went, not %s", got, linkID)
				}
			}
			switch row.when {
			case "minting":
				at.Store(&end)
			case "held":
				held.Store(&end)
			}
			var linkID string
			if row.decide {
				linkID, _ = remoteDecisionLink(t, local, row.path, "walk")
			} else {
				linkID = mintRemote(t, local, row.path)
			}
			at.Store(nil)
			held.Store(nil)
			if row.when == "made" {
				end(linkID)
			}
			if !row.decide {
				until(t, "the ended link left the list", func() bool { return listed(row.path, linkID) == nil })
			} else {
				until(t, "the ended link listed owing its revoke", func() bool {
					l := listed(row.path, linkID)
					return l != nil && l["revokePending"] == true
				})
				sid, rid := id, ""
				if row.path == runPath {
					sid, rid = "", runID
				}
				if local.srv.Lane().DecisionLink(linkID, sid, rid) {
					t.Error("the ended link still opens a lane here")
				}
				select {
				case try := <-next:
					try()
				case <-time.After(testWait):
					t.Fatal("no revoke there was started")
				}
				until(t, "the switchyard answered the revoke", func() bool { return listed(row.path, linkID) == nil })
			}
			for _, k := range []struct{ path, id string }{{sessionPath, keptSession}, {runPath, keptRun}, {runPath, keptDecision}} {
				if l := listed(k.path, k.id); l == nil || l["revokePending"] == true {
					t.Errorf("the link %s that was not ended: %v", k.id, l)
				}
			}
			if !local.srv.Lane().DecisionLink(keptDecision, "", runID) {
				t.Error("the run's decision link that was not ended opens no lane")
			}
		})
	}
}

// yardFront stands between a publisher and its switchyard as a network
// does: it knows each host connection by the session id the switchyard's
// registered names (written unmasked and uncompressed), cuts the ones the
// test names, and holds every connection made after a cut from reaching
// the switchyard until the test lets them through. It also holds back the
// registered of the sessions the test names (hold) until it releases them,
// so one connection's registration is taken before another's.
type yardFront struct {
	*httptest.Server
	mu    sync.Mutex
	conns map[string]net.Conn
	gate  chan struct{}
	holds map[string]chan struct{}
}

func newYardFront(t *testing.T, yard *testEnv) *yardFront {
	t.Helper()
	f := &yardFront{conns: map[string]net.Conn{}}
	h := yard.http.Config.Handler
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		h.ServeHTTP(frontWriter{w, f}, r)
	}))
	t.Cleanup(f.Close)
	t.Cleanup(f.let)
	t.Cleanup(func() {
		f.mu.Lock()
		ids := slices.Collect(maps.Keys(f.holds))
		f.mu.Unlock()
		f.release(ids...)
	})
	return f
}

// hold holds back the registered the switchyard writes for its sessions
// ids from then on, until release.
func (f *yardFront) hold(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holds == nil {
		f.holds = map[string]chan struct{}{}
	}
	for _, id := range ids {
		if f.holds[id] == nil {
			f.holds[id] = make(chan struct{})
		}
	}
}

// knows says whether a connection of the switchyard's session id has been
// written its registered through the front since it was last cut.
func (f *yardFront) knows(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[id] != nil
}

// release lets the registered held back for ids through.
func (f *yardFront) release(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		if c := f.holds[id]; c != nil {
			close(c)
			delete(f.holds, id)
		}
	}
}

// cut closes the connections of the switchyard's sessions ids, the next
// ones held until let.
func (f *yardFront) cut(t *testing.T, ids ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate == nil {
		f.gate = make(chan struct{})
	}
	for _, id := range ids {
		c := f.conns[id]
		if c == nil {
			t.Fatalf("no connection of %s through the front", id)
		}
		delete(f.conns, id)
		c.Close()
	}
}

// let lets the connections held since the cut through.
func (f *yardFront) let() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate != nil {
		close(f.gate)
		f.gate = nil
	}
}

type frontWriter struct {
	http.ResponseWriter
	f *yardFront
}

func (w frontWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w frontWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	fc := &frontConn{Conn: c, f: w.f}
	return fc, bufio.NewReadWriter(rw.Reader, bufio.NewWriter(fc)), nil
}

// frontConn notes its connection under the session id of the registered
// written on it.
type frontConn struct {
	net.Conn
	f *yardFront
}

var registeredAs = regexp.MustCompile(`"t":"registered","sessionId":"([^"]+)"`)

func (c *frontConn) Write(p []byte) (int, error) {
	if m := registeredAs.FindSubmatch(p); m != nil {
		c.f.mu.Lock()
		c.f.conns[string(m[1])] = c.Conn
		held := c.f.holds[string(m[1])]
		c.f.mu.Unlock()
		if held != nil {
			<-held
		}
	}
	return c.Conn.Write(p)
}

// The walk, its connections away (#820): a link the switchyard ends while
// none of the publisher's connections that could be told of it is
// connected is ended here as the connection whose registered can say so
// registers again, its registered no longer naming it: a session's
// decision link as its session registers, a run's decision link and a
// run's link as the run's desk does, its member's registration held back.
// The member's registration, the desk's held back, ends nothing: the link
// is listed until the desk's is taken. An end told on a member's
// connection that is no longer the run's (a member replaced) is not
// applied there: the link is listed until the run's desk registers again,
// and gone after. The other links stay as they were. Judged by the links
// routes, as the walk.
func TestAnEndedLinkIsCaughtUpAtTheNextRegistration(t *testing.T) {
	yard := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), nil)
	yard.srv.hosts.SetDecisions(true)
	front := newYardFront(t, yard)
	local := newTestEnv(t, nil)
	words := &wordLog{}
	local.srv.SetPublisher(uplinkPublisher{words.uplink(front.URL, local)})
	next := retries(local)
	id := publishedSession(t, local)
	runID := local.launchCrew(t, "Pair", catMember("lead", "immediately"))
	waitMembersPublished(t, local, runID, 1)
	run, _ := local.srv.runs.Get(runID)
	member := run.Members[0].SessionID
	sessionPath, runPath := "/api/sessions/"+id+"/links", "/api/runs/"+runID+"/links"
	keptSession := mintRemote(t, local, sessionPath)
	keptRun := mintRemote(t, local, runPath)
	keptDecision, _ := remoteDecisionLink(t, local, runPath, "kept")

	there := func(sid string) string { return local.srv.publishedOf(sid).CurrentID() }
	yardS, yardM, yardD := there(id), there(member), local.srv.liveDesk(runID).CurrentID()
	// What each connection carries, as its words to the publisher say it.
	wordOn := map[string][2]string{yardS: {id, ""}, yardM: {member, ""}, yardD: {"", runID}}
	hosted := func(yid string) *signal.HostedSession {
		if hs, ok := yard.srv.hosts.Get(yid); ok {
			return hs
		}
		hs, _ := yard.srv.hosts.Desk(yid)
		return hs
	}
	listed := func(path, linkID string) map[string]any {
		for _, l := range linksOf(t, local, path) {
			if l["id"] == linkID {
				return l
			}
		}
		return nil
	}
	// away cuts the connections and waits for the switchyard to see them
	// gone; back lets them register again.
	away := func(t *testing.T, ids ...string) {
		front.cut(t, ids...)
		for _, yid := range ids {
			until(t, "the connection away", func() bool { return !hosted(yid).Connected() })
		}
	}
	back := func(t *testing.T, ids ...string) {
		front.let()
		for _, yid := range ids {
			until(t, "the connection back", func() bool { return hosted(yid).Connected() && front.knows(yid) })
		}
	}
	// endThere ends the link at the switchyard as an open admission does.
	endThere := func(t *testing.T, linkID string) {
		l, ok := yard.srv.links.Drop(linkID)
		if !ok {
			t.Fatalf("the switchyard holds no link %s", linkID)
		}
		yard.srv.dropLinkFile(linkID)
		yard.srv.endHeldLink(l)
	}
	// caughtUp waits for what a registration again does: a run's link is
	// ended in the run's turn, which a sync of the run's members over a
	// connection that was away holds for as long as it waits for its
	// answer (runSyncWait).
	caughtUp := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(runSyncWait + testWait)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("never: %s", what)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	unchanged := func(t *testing.T) {
		for _, k := range []struct{ path, id string }{{sessionPath, keptSession}, {runPath, keptRun}, {runPath, keptDecision}} {
			if l := listed(k.path, k.id); l == nil || l["revokePending"] == true {
				t.Errorf("the link %s that was not ended: %v", k.id, l)
			}
		}
		if !local.srv.Lane().DecisionLink(keptDecision, "", runID) {
			t.Error("the run's decision link that was not ended opens no lane")
		}
	}

	// held says whether the run's desk and its member both hold linkID.
	held := func(linkID string) bool {
		m, _ := local.srv.publishedOf(member).HeldLinks()
		d, _ := local.srv.liveDesk(runID).(interface{ HeldLinks() ([]string, bool) }).HeldLinks()
		return slices.Contains(m, linkID) && slices.Contains(d, linkID)
	}
	// holdingBoth has the run's desk and its member register again, so
	// that each holds the run's links (its registered names them),
	// whichever connection they were minted over.
	holdingBoth := func(t *testing.T, ids ...string) {
		away(t, yardD, yardM)
		back(t, yardD, yardM)
		until(t, "the run's desk and its member hold the links", func() bool {
			for _, linkID := range ids {
				if !held(linkID) {
					return false
				}
			}
			return true
		})
	}

	rows := []struct {
		name   string
		path   string
		decide bool
		away   []string // away as the link is ended
		first  string   // whose registration is taken first, the others' held back until it has been
	}{
		{"a session's decision link ended while its session is away", sessionPath, true, []string{yardS}, yardS},
		{"a run's decision link ended while its desk and its member are away: the desk's registration ends it", runPath, true, []string{yardD, yardM}, yardD},
		{"a run's link ended while its desk and its member are away: the desk's registration ends it", runPath, false, []string{yardD, yardM}, yardD},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var linkID string
			if row.decide {
				linkID, _ = remoteDecisionLink(t, local, row.path, "walk")
			} else {
				linkID = mintRemote(t, local, row.path)
			}
			if row.path == runPath {
				holdingBoth(t, linkID)
			}
			others := slices.DeleteFunc(slices.Clone(row.away), func(yid string) bool { return yid == row.first })
			away(t, row.away...)
			endThere(t, linkID)
			// The first's registration is taken (its word for the link
			// handed to the publisher) before the others' registered is
			// let through: an end that follows is the first's.
			front.hold(others...)
			back(t, row.first)
			on := wordOn[row.first]
			until(t, "the first registration taken", func() bool { return words.saw(linkID, on[0], on[1]) })
			front.release(others...)
			back(t, others...)
			if !row.decide {
				caughtUp(t, "the ended link left the list", func() bool { return listed(row.path, linkID) == nil })
			} else {
				caughtUp(t, "the ended link listed owing its revoke", func() bool {
					l := listed(row.path, linkID)
					return l != nil && l["revokePending"] == true
				})
				sid, rid := id, ""
				if row.path == runPath {
					sid, rid = "", runID
				}
				if local.srv.Lane().DecisionLink(linkID, sid, rid) {
					t.Error("the ended link still opens a lane here")
				}
				select {
				case try := <-next:
					try()
				case <-time.After(testWait):
					t.Fatal("no revoke there was started")
				}
				until(t, "the switchyard answered the revoke", func() bool { return listed(row.path, linkID) == nil })
			}
			unchanged(t)
		})
	}

	// The member's registration, the desk's held back, ends none of the
	// run's links its registered no longer names, among them one whose
	// end was told to a member replaced (a session the engine no longer
	// has in the run), which is not applied as it comes either; the
	// desk's registration then ends them all. Last: whatever the words
	// started is waited for before the judgement (WaitBackground), which
	// ends the revokes' retries, so the decision link stays owed its
	// revoke.
	t.Run("a run's links ended while its desk and its member are away: the member's registration ends none of them", func(t *testing.T) {
		decision, _ := remoteDecisionLink(t, local, runPath, "member")
		ordinary, replaced := mintRemote(t, local, runPath), mintRemote(t, local, runPath)
		ended := []string{decision, ordinary, replaced}
		holdingBoth(t, ended...)
		away(t, yardD, yardM)
		for _, linkID := range ended {
			endThere(t, linkID)
		}
		local.srv.RemoteLinkGone(replaced, sessionHex("replaced"), "", true)
		front.hold(yardD)
		back(t, yardM)
		until(t, "the member's registration taken", func() bool {
			for _, linkID := range ended {
				if !words.saw(linkID, member, "") {
					return false
				}
			}
			return true
		})
		local.srv.WaitBackground(t.Context())
		for _, linkID := range ended {
			if l := listed(runPath, linkID); l == nil || l["revokePending"] == true {
				t.Errorf("the run's link %s after its member's registration alone: %v", linkID, l)
			}
		}
		if !local.srv.Lane().DecisionLink(decision, "", runID) {
			t.Error("the run's decision link opens no lane after its member's registration alone")
		}
		front.release(yardD)
		back(t, yardD)
		caughtUp(t, "the run's links ended at its desk's next registration", func() bool {
			l := listed(runPath, decision)
			return l != nil && l["revokePending"] == true && listed(runPath, ordinary) == nil && listed(runPath, replaced) == nil
		})
		if local.srv.Lane().DecisionLink(decision, "", runID) {
			t.Error("the ended link still opens a lane here")
		}
		unchanged(t)
	})
}
