package api

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
)

// wordLog keeps each link a publisher's connection said its registered no
// longer names (hostagent.Uplink.LinkUnnamed), with what the connection
// carries, once the publisher has taken the word.
type wordLog struct {
	mu    sync.Mutex
	words []string
}

func (w *wordLog) note(linkID, sessionID, runID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.words = append(w.words, linkID+"|"+sessionID+"|"+runID)
}

// saw says whether the publisher has taken the word for linkID on the
// connection of sessionID or of runID's desk.
func (w *wordLog) saw(linkID, sessionID, runID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Contains(w.words, linkID+"|"+sessionID+"|"+runID)
}

// uplink is the serve command's uplink to url for local, with the host
// token, each word of a link not named noted in w once local has taken it.
func (w *wordLog) uplink(url string, local *testEnv) *hostagent.Uplink {
	return &hostagent.Uplink{ServerURL: url, Token: "test-host-token", HostName: "office-server", RelayOnly: true,
		Decisions: local.srv.Lane(), LinkGone: local.srv.RemoteLinkGone, LinkUnnamed: func(linkID, sessionID, runID string) {
			local.srv.RemoteLinkUnnamed(linkID, sessionID, runID)
			w.note(linkID, sessionID, runID)
		}}
}

// replacedCrew is a managed crew of two tasks that start at once: hold for
// keeper, listed first, so that its publication carries the run's syncs
// and its ordinary links, and build for builder, whose check fails until a
// file named fixed is committed, so that its first attempt is replaced.
func replacedCrew(root string) crew.Crew {
	return crew.Crew{ID: "replaced", Name: "Replaced", Goal: "build it", Cwd: root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "keeper", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}, {Name: "builder", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks: []crew.Task{{ID: "hold", Member: "keeper", Kind: crew.TaskWork, Instructions: "Hold."},
			{ID: "build", Member: "builder", Kind: crew.TaskWork, Instructions: "Build it.", Checks: []crew.Check{{ID: "fixed", Argv: []string{"test", "-f", "fixed"}}}}}}
}

// launchReplaced launches replacedCrew on local, a ready environment in a
// repository with a publisher, and waits for both first attempts to run
// and be published: the run, and build's first attempt.
func launchReplaced(t *testing.T, local *testEnv) (string, map[string]any) {
	t.Helper()
	run, release, err := local.srv.runs.LaunchNamed(t.Context(), replacedCrew(local.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopEveryRun(local.srv) })
	runningAttempt(t, local, run.ID, "hold", 1)
	first := runningAttempt(t, local, run.ID, "build", 1)
	waitMembersPublished(t, local, run.ID, 2)
	if r, _ := local.srv.runs.Get(run.ID); r.Members[0].Name != "keeper" {
		t.Fatalf("the run's members: %+v", r.Members)
	}
	return run.ID, first
}

// replace has build's first attempt submit a result its check fails, and
// returns the session of the attempt that replaces it once that runs. The
// engine still has the first attempt's session in the run: the former
// attempt is of the run as the publisher asks (runOf).
func replace(t *testing.T, local *testEnv, runID string, first map[string]any) string {
	t.Helper()
	commitFile(t, first["worktree"].(string), "a.txt", "one\n")
	sid := first["sessionId"].(string)
	if resp, out := local.do("POST", "/api/sessions/"+sid+"/task/result", local.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	next := runningAttempt(t, local, runID, "build", 2)["sessionId"].(string)
	if got, ok := local.srv.runOf(sid); !ok || got != runID || next == sid {
		t.Fatalf("the first attempt %s, replaced by %s, is of run %q (%v)", sid, next, got, ok)
	}
	return next
}

// mintRemoteToken is mintRemote with the link's token, which joins it at
// the switchyard.
func mintRemoteToken(t *testing.T, local *testEnv, path string) (string, string) {
	t.Helper()
	resp, out := local.do("POST", path, adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated || out["remote"] != true {
		t.Fatalf("a link at %s: %d %v", path, resp.StatusCode, out)
	}
	u := out["url"].(string)
	return out["link"].(map[string]any)["id"].(string), u[strings.LastIndex(u, "/")+1:]
}

// A managed run's attempt replaced by the next (#820): its session is
// still the run's as the engine has it, and its connection, cut before the
// replacement, registers again once the run's group at the switchyard
// names the next attempt, so that its registered names none of the run's
// links it held. The run's decision link and its ordinary link stay as
// they were: listed, owing no revoke, with no retry begun, the decision
// link's lane open here, and both held at the switchyard, each joining as
// a live link does.
func TestAFormerAttemptRegisteringAgainLeavesItsRunsLinks(t *testing.T) {
	yard := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), nil)
	yard.srv.hosts.SetDecisions(true)
	front := newYardFront(t, yard)
	local := readyEnv(t, nil)
	local.gitRepo(t)
	words := &wordLog{}
	local.srv.SetPublisher(uplinkPublisher{words.uplink(front.URL, local)})
	next := retries(local)
	runID, first := launchReplaced(t, local)
	former := first["sessionId"].(string)
	runPath := "/api/runs/" + runID + "/links"
	decision, decisionToken := remoteDecisionLink(t, local, runPath, "phone")
	ordinary, ordinaryToken := mintRemoteToken(t, local, runPath)

	yardF := local.srv.publishedOf(former).CurrentID()
	hosted := func() bool {
		hs, ok := yard.srv.hosts.Get(yardF)
		return ok && hs.Connected()
	}
	// The first attempt holds both of the run's links: its registered names
	// them while the run's group names it.
	front.cut(t, yardF)
	until(t, "the first attempt away", func() bool { return !hosted() })
	front.let()
	until(t, "the first attempt holds the run's links", func() bool {
		held, _ := local.srv.publishedOf(former).HeldLinks()
		return slices.Contains(held, decision) && slices.Contains(held, ordinary)
	})

	// Its connection away, its reconnect held at the front (the next
	// attempt's publication with it) while the attempt is replaced and the
	// run's members are told the switchyard.
	front.cut(t, yardF)
	until(t, "the first attempt away again", func() bool { return !hosted() })
	replace(t, local, runID, first)
	local.srv.syncRunLinks(runID)
	until(t, "the run's group there no longer names the first attempt", func() bool {
		for _, id := range []string{decision, ordinary} {
			if l, ok := yard.srv.links.Get(id); !ok || l.Group == nil || l.Group.Names(yardF) {
				return false
			}
		}
		return true
	})
	front.let()
	until(t, "the first attempt registered again, its registered naming neither link", func() bool {
		return words.saw(decision, former, "") && words.saw(ordinary, former, "")
	})
	// Whatever the words started here has ended before the judgement.
	local.srv.WaitBackground(t.Context())

	byID := map[string]map[string]any{}
	for _, l := range linksOf(t, local, runPath) {
		byID[l["id"].(string)] = l
	}
	for _, id := range []string{decision, ordinary} {
		if l := byID[id]; l == nil || l["revokePending"] == true {
			t.Errorf("the run's link %s after the former attempt registered again: %v", id, l)
		}
		if _, ok := yard.srv.links.Get(id); !ok {
			t.Errorf("the switchyard no longer holds the run's link %s", id)
		}
	}
	select {
	case <-next:
		t.Error("a revoke of a run's link was begun")
	default:
	}
	if !local.srv.Lane().DecisionLink(decision, "", runID) {
		t.Error("the run's decision link opens no lane here")
	}
	for _, token := range []string{decisionToken, ordinaryToken} {
		if code, out := joinStatus(t, yard, token); code != http.StatusOK {
			t.Errorf("a join through a run's link: %d %v", code, out)
		}
	}
}

// The walk of what a word on a connection ends (#820, the whole table): a
// link a connection held that its registered, as it registers again, no
// longer names is ended here only where not being named can mean nothing
// else, a session's own link on its session's connection and a run's link
// on the run's desk; a link the switchyard says it ended (link_gone, ended)
// is ended on every connection that can list it, a member's and a former
// attempt's of the run too. Where the table says a link ends, the
// switchyard has ended it first; where it says nothing, the link is live
// there and stays as it was: listed, owing no revoke, a decision link's
// lane open, held at the switchyard, no revoke begun. Every connection and
// every link takes its side in ends: one added to the walk has to say what
// a word on it does.
func TestALinkNotNamedIsEndedOnlyWhereItCanMeanNothingElse(t *testing.T) {
	yard := keptSwitchyard(t, filepath.Join(t.TempDir(), "data"), nil)
	yard.srv.hosts.SetDecisions(true)
	local := readyEnv(t, nil)
	local.gitRepo(t)
	local.srv.SetPublisher(uplinkPublisher{(&wordLog{}).uplink(yard.http.URL, local)})
	next := retries(local)
	// The two words, as the host hands them to its publisher.
	words := []struct {
		name string
		tell func(linkID, sessionID, runID string)
	}{
		{"not named at a registration", local.srv.RemoteLinkUnnamed},
		{"told ended", func(linkID, sessionID, runID string) { local.srv.RemoteLinkGone(linkID, sessionID, runID, true) }},
	}
	// Two sessions, so that no connection is asked for more links in a
	// minute than the switchyard mints (proto.LinkRequestsPerMinute): the
	// session links a word ends are minted on the first for one word and on
	// the second for the other.
	sessions := []string{publishedSession(t, local), publishedSession(t, local)}
	runID, first := launchReplaced(t, local)
	former := first["sessionId"].(string)
	current := replace(t, local, runID, first)
	runPath := "/api/runs/" + runID + "/links"

	type link struct {
		name, session string // session: "" for the run's
		decide        bool
	}
	links := []link{{"the session's decision link", sessions[0], true}, {"the session's link", sessions[0], false}, {"the run's decision link", "", true}, {"the run's link", "", false}}
	conns := []struct{ name, sessionID, runID string }{
		{"the session's connection", "", ""}, // the session's own link's, or the first session's
		{"a current member's connection", current, ""},
		{"a former attempt's connection", former, ""},
		{"the run's desk's connection", "", runID},
	}
	// Whether a word on the connection ends the link here: {not named at a
	// registration, told ended}.
	ends := map[string]map[string][2]bool{
		"the session's connection": {
			"the session's decision link": {true, true}, "the session's link": {true, true},
			"the run's decision link": {false, false}, "the run's link": {false, false}},
		"a current member's connection": {
			"the session's decision link": {false, false}, "the session's link": {false, false},
			"the run's decision link": {false, true}, "the run's link": {false, true}},
		"a former attempt's connection": {
			"the session's decision link": {false, false}, "the session's link": {false, false},
			"the run's decision link": {false, true}, "the run's link": {false, true}},
		"the run's desk's connection": {
			"the session's decision link": {false, false}, "the session's link": {false, false},
			"the run's decision link": {true, true}, "the run's link": {true, true}},
	}

	path := func(l link) string {
		if l.session == "" {
			return runPath
		}
		return "/api/sessions/" + l.session + "/links"
	}
	mint := func(t *testing.T, l link) string {
		if l.decide {
			id, _ := remoteDecisionLink(t, local, path(l), "walk")
			return id
		}
		return mintRemote(t, local, path(l))
	}
	listed := func(l link, linkID string) map[string]any {
		for _, got := range linksOf(t, local, path(l)) {
			if got["id"] == linkID {
				return got
			}
		}
		return nil
	}
	opens := func(l link, linkID string) bool {
		if l.session == "" {
			return local.srv.Lane().DecisionLink(linkID, "", runID)
		}
		return local.srv.Lane().DecisionLink(linkID, l.session, "")
	}
	// eventually waits as a run's link's end waits for the run's turn.
	eventually := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(runSyncWait + testWait)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("never: %s", what)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	// asWas says the link stands as it was, here and at the switchyard.
	asWas := func(t *testing.T, l link, linkID string) {
		t.Helper()
		if got := listed(l, linkID); got == nil || got["revokePending"] == true {
			t.Errorf("%s: %v", l.name, got)
		}
		if l.decide && !opens(l, linkID) {
			t.Errorf("%s opens no lane", l.name)
		}
		if _, ok := yard.srv.links.Get(linkID); !ok {
			t.Errorf("the switchyard no longer holds %s", l.name)
		}
	}
	standing := map[string]string{}
	for _, l := range links {
		standing[l.name] = mint(t, l)
	}

	for w, word := range words {
		for _, c := range conns {
			for _, l := range links {
				side, ok := ends[c.name][l.name]
				if !ok {
					t.Fatalf("the table takes no side for %s on %s", l.name, c.name)
				}
				t.Run(word.name+" on "+c.name+": "+l.name, func(t *testing.T) {
					if !side[w] {
						linkID := standing[l.name]
						sessionID := c.sessionID
						if c.name == "the session's connection" {
							sessionID = sessions[0]
						}
						word.tell(linkID, sessionID, c.runID)
						asWas(t, l, linkID)
						select {
						case <-next:
							t.Error("a revoke was begun")
						default:
						}
						return
					}
					if l.session != "" {
						l.session = sessions[w]
					}
					linkID := mint(t, l)
					sessionID := c.sessionID
					if c.name == "the session's connection" {
						sessionID = l.session
					}
					if _, ok := yard.srv.links.Drop(linkID); !ok {
						t.Fatalf("the switchyard holds no %s", l.name)
					}
					yard.srv.dropLinkFile(linkID)
					word.tell(linkID, sessionID, c.runID)
					if !l.decide {
						eventually(t, "the ended link left the list", func() bool { return listed(l, linkID) == nil })
						return
					}
					eventually(t, "the ended link listed owing its revoke", func() bool {
						got := listed(l, linkID)
						return got != nil && got["revokePending"] == true
					})
					if opens(l, linkID) {
						t.Error("the ended link still opens a lane here")
					}
					select {
					case try := <-next:
						try()
					case <-time.After(testWait):
						t.Fatal("no revoke there was begun")
					}
					until(t, "the switchyard answered the revoke", func() bool { return listed(l, linkID) == nil })
				})
			}
		}
	}

	// Whatever the words started here has ended: the links no word was to
	// end stand as they were.
	local.srv.WaitBackground(t.Context())
	for _, l := range links {
		asWas(t, l, standing[l.name])
	}
	select {
	case <-next:
		t.Error("a revoke was begun")
	default:
	}
}
