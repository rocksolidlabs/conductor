package api

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
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
		}}})
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
