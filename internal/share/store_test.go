package share

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

func TestCreateResolveRevoke(t *testing.T) {
	s := NewStore()
	var revoked []string
	s.OnRevoke = func(sessionID, linkID string) { revoked = append(revoked, sessionID+"/"+linkID) }
	link, tok, err := s.Create("sess", session.RoleView, "qa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 {
		t.Fatalf("token length %d", len(tok))
	}
	got, err := s.Resolve(tok)
	if err != nil || got.ID != link.ID || got.Role != session.RoleView {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if _, err := s.Resolve("nope"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("unknown: %v", err)
	}
	if !found(s.Revoke("sess", link.ID)) {
		t.Fatal("revoke failed")
	}
	if found(s.Revoke("other", link.ID)) {
		t.Fatal("revoke must check session ownership")
	}
	if _, err := s.Resolve(tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after revoke: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "sess/"+link.ID {
		t.Fatalf("hook %v", revoked)
	}
	// A second revoke tells the hook again: whoever is still attached
	// through the link is closed then too.
	s.Revoke("sess", link.ID)
	if len(revoked) != 2 || revoked[1] != "sess/"+link.ID {
		t.Fatalf("hook after a second revoke %v", revoked)
	}
	if list := s.ListBySession("sess"); len(list) != 1 || !list[0].Revoked {
		t.Fatalf("list %+v", list)
	}
}

func TestExpiry(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	_, tok, _ := s.Create("sess", session.RoleControl, "", time.Minute)
	if _, err := s.Resolve(tok); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Resolve(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestDeleteSessionAndRole(t *testing.T) {
	s := NewStore()
	if _, _, err := s.Create("sess", "admin", "", 0); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("role: %v", err)
	}
	_, tok, _ := s.Create("sess", session.RoleView, "", 0)
	s.DeleteSession("sess", true)
	if _, err := s.Resolve(tok); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestEqualConstantTime(t *testing.T) {
	if !Equal("abc", "abc") || Equal("abc", "abd") || Equal("", "x") {
		t.Fatal("Equal misbehaves")
	}
}

// A run link belongs to its run alone: listed, revoked and capped by the run,
// never by a session, with its own revoke hook.
func TestRunLinks(t *testing.T) {
	s := NewStore()
	var sessionRevokes, runRevokes []string
	s.OnRevoke = func(sessionID, linkID string) { sessionRevokes = append(sessionRevokes, sessionID+"/"+linkID) }
	s.OnRevokeRun = func(runID, linkID string) { runRevokes = append(runRevokes, runID+"/"+linkID) }
	if _, _, err := s.CreateRunLink("run", "admin", "", 0); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("role: %v", err)
	}
	link, tok, err := s.CreateRunLink("run", session.RoleView, "standup", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if link.RunID != "run" || link.SessionID != "" || link.Label != "standup" || link.ExpiresAt == nil {
		t.Fatalf("link %+v", link)
	}
	got, err := s.Resolve(tok)
	if err != nil || got.ID != link.ID || got.RunID != "run" || got.SessionID != "" {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if l := s.ListByRun("run"); len(l) != 1 || l[0].ID != link.ID {
		t.Fatalf("list by run %+v", l)
	}
	if l := s.ListBySession(""); len(l) != 0 {
		t.Fatalf("a run link is listed by session: %+v", l)
	}
	if found(s.Revoke("", link.ID)) || found(s.Revoke("run", link.ID)) {
		t.Fatal("a run link is revoked as a session's")
	}
	if found(s.RevokeRun("other", link.ID)) {
		t.Fatal("revoke must check run ownership")
	}
	sl, _, _ := s.Create("sess", session.RoleView, "", 0)
	if found(s.RevokeRun("", sl.ID)) || found(s.RevokeRun("sess", sl.ID)) {
		t.Fatal("a session link is revoked as a run's")
	}
	if !found(s.RevokeRun("run", link.ID)) {
		t.Fatal("revoke run failed")
	}
	if _, err := s.Resolve(tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after revoke: %v", err)
	}
	s.RevokeRun("run", link.ID)
	if len(runRevokes) != 2 || runRevokes[0] != "run/"+link.ID || runRevokes[1] != "run/"+link.ID || len(sessionRevokes) != 0 {
		t.Fatalf("hooks: run %v session %v", runRevokes, sessionRevokes)
	}
	if l := s.ListByRun("run"); len(l) != 1 || !l[0].Revoked {
		t.Fatalf("list after revoke %+v", l)
	}
	// Deleting a session leaves the run links alone.
	s.DeleteSession("", true)
	if _, ok := s.Get(link.ID); !ok {
		t.Fatal("a session's delete dropped a run link")
	}
}

// The per-scope cap applies to each run as it does to each session.
func TestRunLinksAreCappedPerRun(t *testing.T) {
	s := NewStore()
	for i := range MaxLinksPerSession {
		if _, _, err := s.CreateRunLink("run", session.RoleView, "", 0); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}
	if _, _, err := s.CreateRunLink("run", session.RoleView, "", 0); !errors.Is(err, ErrTooManyLinks) {
		t.Fatalf("past the cap: %v", err)
	}
	if _, _, err := s.CreateRunLink("other", session.RoleView, "", 0); err != nil {
		t.Fatalf("another run: %v", err)
	}
	if _, _, err := s.Create("run", session.RoleView, "", 0); err != nil {
		t.Fatalf("a session named like the run: %v", err)
	}
}

// DeleteRun forgets every link of a run and returns the IDs of those not
// revoked, whose viewers may still be attached; other runs' links and
// sessions' links stay.
func TestDeleteRun(t *testing.T) {
	s := NewStore()
	live, liveTok, _ := s.CreateRunLink("run", session.RoleView, "", 0)
	revoked, revokedTok, _ := s.CreateRunLink("run", session.RoleControl, "", 0)
	s.RevokeRun("run", revoked.ID)
	other, otherTok, _ := s.CreateRunLink("other", session.RoleView, "", 0)
	sl, sessTok, _ := s.Create("run", session.RoleView, "", 0)

	if ids := s.DeleteRun("run"); !slices.Equal(ids, []string{live.ID}) {
		t.Fatalf("deleted %v, want [%s]", ids, live.ID)
	}
	for _, tok := range []string{liveTok, revokedTok} {
		if _, err := s.Resolve(tok); !errors.Is(err, ErrUnknownToken) {
			t.Fatalf("after delete: %v", err)
		}
	}
	if _, ok := s.Get(live.ID); ok {
		t.Fatal("the link is still found by ID")
	}
	if l := s.ListByRun("run"); len(l) != 0 {
		t.Fatalf("still listed %+v", l)
	}
	if got, err := s.Resolve(otherTok); err != nil || got.ID != other.ID {
		t.Fatalf("another run's link: %+v %v", got, err)
	}
	if got, err := s.Resolve(sessTok); err != nil || got.ID != sl.ID {
		t.Fatalf("a session's link: %+v %v", got, err)
	}
	if ids := s.DeleteRun("run"); len(ids) != 0 {
		t.Fatalf("deleted again %v", ids)
	}
}

// Resolve reads a link under the store's lock, so it may run while the link
// is revoked (go test -race).
func TestResolveWhileRevoking(t *testing.T) {
	s := NewStore()
	link, tok, _ := s.CreateRunLink("run", session.RoleView, "", 0)
	sl, sessTok, _ := s.Create("sess", session.RoleView, "", 0)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				_, _ = s.Resolve(tok)
				_, _ = s.Resolve(sessTok)
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			s.RevokeRun("run", link.ID)
			s.Revoke("sess", sl.ID)
		}
	})
	wg.Wait()
	if _, err := s.Resolve(tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after revoke: %v", err)
	}
}

// found is the first result of a revoke: whether the link was the caller's.
func found(ok, _ bool) bool { return ok }

// A revoke says whether it revoked the link: a second one finds it revoked
// already (and tells the hook again, TestCreateResolveRevoke).
func TestRevokeSaysWhetherItRevoked(t *testing.T) {
	s := NewStore()
	link, _, _ := s.CreateRunLink("run", session.RoleView, "", 0)
	if ok, revoked := s.RevokeRun("run", link.ID); !ok || !revoked {
		t.Fatalf("first: %v %v", ok, revoked)
	}
	if ok, revoked := s.RevokeRun("run", link.ID); !ok || revoked {
		t.Fatalf("second: %v %v", ok, revoked)
	}
	sl, _, _ := s.Create("sess", session.RoleView, "", 0)
	if ok, revoked := s.Revoke("sess", sl.ID); !ok || !revoked {
		t.Fatalf("session first: %v %v", ok, revoked)
	}
	if ok, revoked := s.Revoke("sess", sl.ID); !ok || revoked {
		t.Fatalf("session second: %v %v", ok, revoked)
	}
}

// A session's end marks its link revoked as a revoke does, so nothing opens
// with it after, but tells no hook: whoever ends the session closes the
// link's viewers as its end. Another session's link, or a run's, is not its.
func TestEndMarksASessionsLinkAndTellsNoHook(t *testing.T) {
	s := NewStore()
	told := 0
	s.OnRevoke = func(string, string) { told++ }
	s.OnRevokeRun = func(string, string) { told++ }
	link, tok, _ := s.Create("sess", session.RoleView, "", 0)
	run, _, _ := s.CreateRunLink("run", session.RoleView, "", 0)
	if s.End("other", link.ID) || s.End("sess", run.ID) {
		t.Fatal("ended a link that is not the session's")
	}
	if !s.End("sess", link.ID) {
		t.Fatal("the session's link was not found")
	}
	if _, err := s.Resolve(tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("resolve after the end: %v", err)
	}
	if _, err := s.Live(link.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("live after the end: %v", err)
	}
	if told != 0 {
		t.Fatalf("the end told the hooks %d times", told)
	}
	if s.End("sess", link.ID) {
		t.Fatal("a second end took the link again")
	}
	// A link revoked first is the revoke's to close, whose hook may still be
	// on its way: the end does not take it.
	revoked, _, _ := s.Create("sess", session.RoleView, "", 0)
	s.Revoke("sess", revoked.ID)
	if s.End("sess", revoked.ID) {
		t.Fatal("the end took a link revoked before it")
	}
}

// A switchyard's group links: indexed by owner and run, kept current by
// their owner alone, revoked by it alone, and found by the sessions they name.
func TestGroupLinks(t *testing.T) {
	s := NewStore()
	var revokedRun string
	s.OnRevokeRun = func(runID, linkID string) { revokedRun = runID }
	g := Group{Name: "api", Members: []GroupMember{{Name: "lead", SessionID: "s1", AgentID: "claude", Status: "running"}}}
	l, tok, err := s.CreateGroupLink("api-1", "owner-a", g, session.RoleView, "", 0)
	if err != nil || l.Group == nil || !l.Group.Names("s1") || l.Owner != "owner-a" {
		t.Fatalf("create: %+v %v", l, err)
	}
	if got, err := s.Resolve(tok); err != nil || got.RunID != "api-1" || !got.Group.Names("s1") {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	g.Members = append(g.Members, GroupMember{Name: "core", SessionID: "s2", AgentID: "claude", Status: "running"})
	if changed, _, _ := s.SetGroup("owner-b", "api-1", g); len(changed) != 0 {
		t.Fatalf("another owner changed %v", changed)
	}
	if changed, left, _ := s.SetGroup("owner-a", "api-1", g); len(changed) != 1 || changed[0] != l.ID || len(left) != 0 {
		t.Fatalf("its owner changed %v, left %v", changed, left)
	}
	if ids := s.ListNaming("s2"); len(ids) != 1 || ids[0].ID != l.ID {
		t.Fatalf("naming s2: %v", ids)
	}
	if found, _ := s.RevokeGroup("owner-b", l.ID); found {
		t.Fatal("another owner revoked it")
	}
	if found, revoked := s.RevokeGroup("owner-a", l.ID); !found || !revoked || revokedRun != "api-1" {
		t.Fatalf("revoke: %v %v %q", found, revoked, revokedRun)
	}
	if _, err := s.Resolve(tok); err != ErrRevoked {
		t.Fatalf("after revoke: %v", err)
	}
}

// Live answers a link by id while it still opens what it was made for, and
// says why once it does not: revoked, expired, or forgotten.
func TestLive(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	sl, _, _ := s.Create("sess", session.RoleControl, "", time.Minute)
	rl, _, _ := s.CreateRunLink("run", session.RoleView, "", 0)
	if got, err := s.Live(sl.ID); err != nil || got.ID != sl.ID || got.Role != session.RoleControl {
		t.Fatalf("live: %+v %v", got, err)
	}
	if got, err := s.Live(rl.ID); err != nil || got.RunID != "run" {
		t.Fatalf("live run link: %+v %v", got, err)
	}
	if _, err := s.Live("nope"); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("unknown: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Live(sl.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("at its expiry: %v", err)
	}
	s.RevokeRun("run", rl.ID)
	if _, err := s.Live(rl.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	s.DeleteRun("run")
	if _, err := s.Live(rl.ID); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("forgotten: %v", err)
	}
}

// Drop returns the link as it stood when it went, its group's sessions
// then, and nothing for a link it does not hold.
func TestDropReturnsTheLinkAsItWent(t *testing.T) {
	s := NewStore()
	g := Group{Name: "api", Members: []GroupMember{{Name: "lead", SessionID: "s1"}}}
	l, _, _ := s.CreateGroupLink("api-1", "owner", g, session.RoleView, "", 0)
	g.Members = append(g.Members, GroupMember{Name: "core", SessionID: "s2"})
	s.SetGroup("owner", "api-1", g)
	gone, ok := s.Drop(l.ID)
	if !ok || gone.ID != l.ID || !gone.Group.Names("s1") || !gone.Group.Names("s2") {
		t.Fatalf("dropped %+v %v", gone, ok)
	}
	if _, err := s.Live(l.ID); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("after the drop: %v", err)
	}
	if _, ok := s.Drop(l.ID); ok {
		t.Fatal("dropped twice")
	}
}

// SetOwner makes a link durable only while the store holds it unrevoked,
// and says whether it did.
func TestSetOwnerSaysWhetherTheLinkIsKept(t *testing.T) {
	s := NewStore()
	live, _, _ := s.Create("s1", session.RoleView, "", 0)
	revoked, _, _ := s.Create("s1", session.RoleView, "", 0)
	dropped, _, _ := s.Create("s1", session.RoleView, "", 0)
	s.Revoke("s1", revoked.ID)
	s.Drop(dropped.ID)
	if !s.SetOwner(live.ID, "owner", "addr") {
		t.Fatal("a live link was not kept")
	}
	if l, _ := s.Get(live.ID); l.Owner != "owner" || l.Addr != "addr" {
		t.Fatalf("kept as %+v", l)
	}
	if s.SetOwner(revoked.ID, "owner", "addr") || s.SetOwner(dropped.ID, "owner", "addr") {
		t.Fatal("a revoked or dropped link was kept")
	}
	if l, _ := s.Get(revoked.ID); l.Owner != "" {
		t.Fatalf("the revoked link took an owner: %+v", l)
	}
	if len(s.Durable()) != 1 {
		t.Fatalf("durable: %d", len(s.Durable()))
	}
}

// ExpireDue tells the revoke hooks of each link that expired, once, and
// leaves alone the links not expired yet, those without an expiry and those
// revoked before.
func TestExpireDue(t *testing.T) {
	s := NewStore()
	var sessions, runs []string
	s.OnRevoke = func(sessionID, linkID string) { sessions = append(sessions, sessionID+"/"+linkID) }
	s.OnRevokeRun = func(runID, linkID string) { runs = append(runs, runID+"/"+linkID) }
	start := time.Now()
	soon, _, _ := s.Create("sess", session.RoleView, "", time.Minute)
	later, _, _ := s.Create("sess", session.RoleView, "", time.Hour)
	s.Create("sess", session.RoleView, "", 0)
	revoked, _, _ := s.Create("sess", session.RoleView, "", time.Minute)
	s.Revoke("sess", revoked.ID)
	sessions = nil
	run, _, _ := s.CreateRunLink("run", session.RoleControl, "", time.Minute)

	if n := s.ExpireDue(start); n != 0 || len(sessions) != 0 || len(runs) != 0 {
		t.Fatalf("before any expiry: %d %v %v", n, sessions, runs)
	}
	if n := s.ExpireDue(start.Add(2 * time.Minute)); n != 2 {
		t.Fatalf("told %d, want 2", n)
	}
	if !slices.Equal(sessions, []string{"sess/" + soon.ID}) || !slices.Equal(runs, []string{"run/" + run.ID}) {
		t.Fatalf("hooks: sessions %v runs %v", sessions, runs)
	}
	if n := s.ExpireDue(start.Add(3 * time.Minute)); n != 0 {
		t.Fatalf("told again: %d", n)
	}
	if n := s.ExpireDue(start.Add(2 * time.Hour)); n != 1 || sessions[len(sessions)-1] != "sess/"+later.ID {
		t.Fatalf("the later one: %d %v", n, sessions)
	}
}

// SetGroup says which links it changed and which sessions they no longer
// name, so the viewers attached to those through them can be closed; the
// links name the new group's sessions alone from then on.
func TestSetGroupSaysWhichSessionsLeft(t *testing.T) {
	s := NewStore()
	g := Group{Name: "api", Members: []GroupMember{{Name: "lead", SessionID: "s1"}, {Name: "core", SessionID: "s2"}, {Name: "docs"}}}
	a, _, _ := s.CreateGroupLink("api-1", "owner", g, session.RoleView, "", 0)
	b, _, _ := s.CreateGroupLink("api-1", "owner", g, session.RoleControl, "", 0)
	gone, _, _ := s.CreateGroupLink("api-1", "owner", g, session.RoleControl, "", 0)
	s.RevokeGroup("owner", gone.ID)

	g2 := Group{Name: "api", Members: []GroupMember{{Name: "lead", SessionID: "s1"}, {Name: "core"}, {Name: "docs", SessionID: "s3"}}}
	changed, left, _ := s.SetGroup("owner", "api-1", g2)
	want := []string{a.ID, b.ID}
	slices.Sort(want)
	if !slices.Equal(changed, want) || !slices.Equal(left, []string{"s2"}) {
		t.Fatalf("changed %v left %v", changed, left)
	}
	for _, id := range want {
		l, err := s.Live(id)
		if err != nil || l.Group.Names("s2") || !l.Group.Names("s3") {
			t.Fatalf("link %s after the update: %+v %v", id, l, err)
		}
	}
	if changed, left, _ := s.SetGroup("owner", "api-1", g2); len(changed) != 2 || len(left) != 0 {
		t.Fatalf("the same group again: changed %v left %v", changed, left)
	}
}

// A group with a revision changes the run's links only when it is above
// every revision they hold, a revoked link's included: one sent before
// those they hold changes nothing, and neither does a link made with it. A
// group without one (an older host's) is taken whatever they hold, which
// they keep. Another owner's run of the same id is another run.
func TestSetGroupRefusesAnOlderRevision(t *testing.T) {
	s := NewStore()
	group := func(rev int64, ids ...string) Group {
		g := Group{Name: "api", Revision: rev}
		for _, id := range ids {
			g.Members = append(g.Members, GroupMember{Name: "m-" + id, SessionID: id})
		}
		return g
	}
	l, _, err := s.CreateGroupLink("api-1", "owner", group(10, "s1", "s2"), session.RoleView, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed, left, err := s.SetGroup("owner", "api-1", group(12, "s1")); err != nil || len(changed) != 1 || !slices.Equal(left, []string{"s2"}) {
		t.Fatalf("a newer group: changed %v left %v err %v", changed, left, err)
	}
	for _, rev := range []int64{11, 12} {
		if changed, left, err := s.SetGroup("owner", "api-1", group(rev, "s1", "s2")); !errors.Is(err, ErrStaleGroup) || changed != nil || left != nil {
			t.Fatalf("revision %d after 12: changed %v left %v err %v", rev, changed, left, err)
		}
	}
	if got, _ := s.Live(l.ID); got.Group.Names("s2") || got.Group.Revision != 12 {
		t.Fatalf("after the older groups: %+v", got.Group)
	}
	if _, _, err := s.CreateGroupLink("api-1", "owner", group(11, "s1", "s2"), session.RoleView, "", 0); !errors.Is(err, ErrStaleGroup) {
		t.Fatalf("a link with an older group: %v", err)
	}
	if n := s.Count(); n != 1 {
		t.Fatalf("links after the refused one: %d", n)
	}
	if _, _, err := s.CreateGroupLink("api-1", "another-owner", group(1, "s9"), session.RoleView, "", 0); err != nil {
		t.Fatalf("another owner's run: %v", err)
	}
	// An older host's group, without a revision, is taken; the links keep 12.
	if changed, _, err := s.SetGroup("owner", "api-1", group(0, "s1", "s2")); err != nil || len(changed) != 1 {
		t.Fatalf("a group without a revision: changed %v err %v", changed, err)
	}
	if got, _ := s.Live(l.ID); !got.Group.Names("s2") || got.Group.Revision != 12 {
		t.Fatalf("after a group without a revision: %+v", got.Group)
	}
	if _, _, err := s.SetGroup("owner", "api-1", group(12, "s1")); !errors.Is(err, ErrStaleGroup) {
		t.Fatalf("12 again: %v", err)
	}
	// A revoked link's revision still counts; with no live link the run has
	// nothing to change, whatever the revision.
	s.RevokeGroup("owner", l.ID)
	if _, _, err := s.CreateGroupLink("api-1", "owner", group(12, "s1"), session.RoleView, "", 0); !errors.Is(err, ErrStaleGroup) {
		t.Fatalf("a link with an older group than a revoked link's: %v", err)
	}
	if changed, _, err := s.SetGroup("owner", "api-1", group(5, "s1")); err != nil || len(changed) != 0 {
		t.Fatalf("no live link: changed %v err %v", changed, err)
	}
	if nl, _, err := s.CreateGroupLink("api-1", "owner", group(13, "s1"), session.RoleView, "", 0); err != nil || nl.Group.Revision != 13 {
		t.Fatalf("a link with a newer group: %+v %v", nl, err)
	}
}

// A group's revision is held in memory alone: a kept link's file, which
// writes its group as JSON, never carries it.
func TestAGroupsRevisionIsNotWritten(t *testing.T) {
	b, err := json.Marshal(Group{Name: "api", Revision: 42, Members: []GroupMember{{Name: "lead"}}})
	if err != nil || strings.Contains(string(b), "42") || strings.Contains(strings.ToLower(string(b)), "revision") {
		t.Fatalf("written as %s (%v)", b, err)
	}
}

// A decision link is a control link: a view one is refused, nothing made;
// a control one carries Decide, resolves as a link does and is revoked as
// one is. A plain link does not carry it.
func TestADecisionLinkNeedsControl(t *testing.T) {
	s := NewStore()
	if _, _, err := s.CreateDecision("s1", session.RoleView, "", 0); !errors.Is(err, ErrDecideNeedsControl) {
		t.Fatalf("a view decision link: %v", err)
	}
	if _, _, err := s.CreateRunDecision("r1", session.RoleView, "", 0); !errors.Is(err, ErrDecideNeedsControl) {
		t.Fatalf("a view run decision link: %v", err)
	}
	if n := len(s.ListBySession("s1")) + len(s.ListByRun("r1")); n != 0 {
		t.Fatalf("%d links made", n)
	}
	l, tok, err := s.CreateDecision("s1", session.RoleControl, "phone", 0)
	if err != nil || !l.Decide || l.Role != session.RoleControl {
		t.Fatalf("a decision link: %+v %v", l, err)
	}
	got, err := s.Resolve(tok)
	if err != nil || !got.Decide {
		t.Fatalf("resolved: %+v %v", got, err)
	}
	rl, _, err := s.CreateRunDecision("r1", session.RoleControl, "", 0)
	if err != nil || !rl.Decide || rl.RunID != "r1" {
		t.Fatalf("a run decision link: %+v %v", rl, err)
	}
	plain, _, _ := s.Create("s1", session.RoleControl, "", 0)
	if plain.Decide {
		t.Fatal("a plain link decides")
	}
	if found, _ := s.Revoke("s1", l.ID); !found {
		t.Fatal("not revoked")
	}
	if _, err := s.Resolve(tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after the revoke: %v", err)
	}
}

// A decision link's Recent keeps the newest MaxRecent, one entry per ref
// and attempt, and goes with the link; a plain link keeps none.
func TestADecisionLinksRecent(t *testing.T) {
	s := NewStore()
	l, _, _ := s.CreateRunDecision("r1", session.RoleControl, "", 0)
	plain, _, _ := s.CreateRunLink("r1", session.RoleControl, "", 0)
	if s.NoteRecent(plain.ID, Recent{Ref: "task:r1/a", Attempt: "x"}) {
		t.Fatal("a plain link noted")
	}
	at := time.Now()
	for i := range MaxRecent + 5 {
		s.NoteRecent(l.ID, Recent{Ref: "task:r1/a", Receipt: "c", Attempt: "op-" + strconv.Itoa(i), At: at})
	}
	s.NoteRecent(l.ID, Recent{Ref: "task:r1/a", Receipt: "c", Attempt: "op-10", At: at.Add(time.Second)})
	got := s.RecentOf(l.ID)
	if len(got) != MaxRecent || got[0].Attempt != "op-5" || got[len(got)-1].Attempt != "op-10" {
		t.Fatalf("recent: %d, first %+v last %+v", len(got), got[0], got[len(got)-1])
	}
	if c, _ := s.Get(l.ID); c.recent != nil {
		t.Fatal("a copy carries the recent list")
	}
	s.DeleteRun("r1")
	if got := s.RecentOf(l.ID); got != nil {
		t.Fatalf("after the run's links went: %v", got)
	}
}

// A link's act is held while it is written (Hold): a revoke waits for the
// act under way and returns once it is let go, a hold asked while the
// revoke waits is refused at once, as one after it is; an expired link is
// refused ErrExpired; a link revoked on its session's end (End) does not
// wait, its session refusing what comes after on its own.
func TestALinksActIsHeldAgainstItsRevoke(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	l, _, _ := s.CreateRunDecision("r1", session.RoleControl, "phone", time.Minute)
	got, release, err := s.Hold(l.ID)
	if err != nil || got.ID != l.ID {
		t.Fatalf("hold: %+v %v", got, err)
	}
	// Two holds at once are fine.
	_, again, err := s.Hold(l.ID)
	if err != nil {
		t.Fatal(err)
	}
	again()
	revoked := make(chan struct{})
	go func() {
		s.RevokeRun("r1", l.ID)
		close(revoked)
	}()
	// The revoke waits: holds are refused meanwhile, the act's still held.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, rel, err := s.Hold(l.ID); err != nil {
			if !errors.Is(err, ErrRevoked) {
				t.Fatalf("a hold while the revoke waits: %v", err)
			}
			break
		} else {
			rel()
		}
		if time.Now().After(deadline) {
			t.Fatal("the revoke never began")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-revoked:
		t.Fatal("the revoke returned with an act held")
	default:
	}
	release()
	release() // once
	<-revoked
	if _, _, err := s.Hold(l.ID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("a hold after the revoke: %v", err)
	}
	e, _, _ := s.CreateRunDecision("r1", session.RoleControl, "", time.Minute)
	now = now.Add(time.Minute)
	if _, _, err := s.Hold(e.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("a hold of an expired link: %v", err)
	}
	if _, _, err := s.Hold("nope"); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("a hold of no link: %v", err)
	}
	sl, _, _ := s.CreateDecision("s1", session.RoleControl, "", 0)
	_, held, _ := s.Hold(sl.ID)
	defer held()
	if !s.End("s1", sl.ID) {
		t.Fatal("not ended")
	}
}

// An opId names one operation of a link: the same request again finds the
// first, under way or settled; another request under it is a conflict,
// before and after it settles; one let go (nothing begun) frees it; past
// MaxOps a new one is refused, an old one still answered; another link's
// opIds are its own; a plain link takes none; and they go with the link.
func TestAnOpIDNamesOneOperation(t *testing.T) {
	s := NewStore()
	l, _, _ := s.CreateRunDecision("r1", session.RoleControl, "", 0)
	other, _, _ := s.CreateRunDecision("r1", session.RoleControl, "", 0)
	op, mine, err := s.ClaimOp(l.ID, "op-1", "fp-a")
	if err != nil || !mine || op.Settled {
		t.Fatalf("a new opId: %+v %v %v", op, mine, err)
	}
	twin, mine, err := s.ClaimOp(l.ID, "op-1", "fp-a")
	if err != nil || mine || twin.Settled {
		t.Fatalf("the twin under way: %+v %v %v", twin, mine, err)
	}
	if _, _, err := s.ClaimOp(l.ID, "op-1", "fp-b"); !errors.Is(err, ErrOpConflict) {
		t.Fatalf("another request under way: %v", err)
	}
	if _, mine, err := s.ClaimOp(other.ID, "op-1", "fp-b"); err != nil || !mine {
		t.Fatalf("another link's opId: %v %v", mine, err)
	}
	s.SettleOp(l.ID, "op-1", true, "kept")
	<-twin.Done
	if got, mine, err := s.ClaimOp(l.ID, "op-1", "fp-a"); err != nil || mine || !got.Settled || got.Outcome != "kept" {
		t.Fatalf("settled: %+v %v %v", got, mine, err)
	}
	if _, _, err := s.ClaimOp(l.ID, "op-1", "fp-b"); !errors.Is(err, ErrOpConflict) {
		t.Fatalf("another request after: %v", err)
	}
	s.ClaimOp(l.ID, "op-2", "fp-a")
	s.SettleOp(l.ID, "op-2", false, "")
	if _, mine, err := s.ClaimOp(l.ID, "op-2", "fp-b"); err != nil || !mine {
		t.Fatalf("an opId let go: %v %v", mine, err)
	}
	for i := 3; i <= MaxOps; i++ {
		if _, _, err := s.ClaimOp(l.ID, "op-"+strconv.Itoa(i), "fp"); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
	if _, _, err := s.ClaimOp(l.ID, "op-new", "fp"); !errors.Is(err, ErrTooManyOps) {
		t.Fatalf("past MaxOps: %v", err)
	}
	if got, _, err := s.ClaimOp(l.ID, "op-1", "fp-a"); err != nil || got.Outcome != "kept" {
		t.Fatalf("an old one past MaxOps: %+v %v", got, err)
	}
	plain, _, _ := s.CreateRunLink("r1", session.RoleControl, "", 0)
	if _, _, err := s.ClaimOp(plain.ID, "op-1", "fp"); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("a plain link: %v", err)
	}
	if c, _ := s.Get(l.ID); c.ops != nil {
		t.Fatal("a copy carries the ops")
	}
	s.DeleteRun("r1")
	if _, _, err := s.ClaimOp(l.ID, "op-1", "fp-a"); !errors.Is(err, ErrUnknownLink) {
		t.Fatalf("after the run's links went: %v", err)
	}
}
