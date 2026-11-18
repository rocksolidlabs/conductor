package exttest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

var (
	headSHA  = strings.Repeat("a", 40)
	otherSHA = strings.Repeat("b", 40)
)

// sampleResolved is a resolved launch of the crew web, for the project
// CND, its manifest the repository's file.
func sampleResolved() ext.ResolvedLaunch {
	dir := filepath.Join(os.TempDir(), "cnd")
	return ext.ResolvedLaunch{
		Crew: "web", Name: "Web", Managed: true, Tasks: 2, Where: ext.RunsOnServer, Project: "CND",
		ProjectCwd: dir, Cwd: filepath.Join(dir, "web"), InProject: true,
		Manifest: ext.ResolvedManifest{Source: ext.ManifestFile, Blob: strings.Repeat("c", 40)},
		Members: []ext.ResolvedMember{
			{Name: "planner", Agent: "claude", Start: "task", Selection: strings.Repeat("d", 64)},
			{Name: "builder", Agent: "codex", Start: "task", Selection: strings.Repeat("e", 64)},
		},
	}
}

// heldLaunch is a provider's launch of web held to digest, for CND.
func heldLaunch(op, digest, commit string) ext.LaunchRequest {
	r := providerLaunch("web", op)
	r.Project, r.ResolvedDigest, r.StartCommit = "CND", digest, commit
	return r
}

func TestFakeHostResolvesWithItsSettings(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	if _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web"}); !errors.Is(err, ext.ErrNotFound) {
		t.Errorf("a resolve of a crew the fake was not given = %v, want ErrNotFound", err)
	}
	h.SetResolved("web", sampleResolved())
	got, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Valid(); err != nil || got.Digest == "" {
		t.Fatalf("the fake's resolved launch: %v (digest %q)", err, got.Digest)
	}
	base := got.Digest

	// The digest is ext.ResolvedDigest's over the record as answered.
	again, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})
	if again.Digest != base {
		t.Errorf("two resolves with nothing changed: %s and %s", base, again.Digest)
	}

	// Each setting but the capacity views changes the digest, and what it
	// sets is in the record.
	for name, c := range map[string]struct {
		set   func(*FakeHost)
		check func(ext.ResolvedLaunch) bool
	}{
		"the server's yolo default": {func(h *FakeHost) { h.SetServerYolo(true) }, func(r ext.ResolvedLaunch) bool { return r.Members[0].Yolo && r.Members[1].Yolo }},
		"an agent's selection": {func(h *FakeHost) { h.SetSelection("codex", strings.Repeat("f", 64)) }, func(r ext.ResolvedLaunch) bool {
			return r.Members[1].Selection == strings.Repeat("f", 64) && r.Members[0].Selection == strings.Repeat("d", 64)
		}},
		"the manifest's blob at the commit": {func(h *FakeHost) { h.SetManifestBlob(headSHA, strings.Repeat("9", 40)) }, func(r ext.ResolvedLaunch) bool { return r.Manifest.Blob == strings.Repeat("9", 40) }},
		"a member's binding": {func(h *FakeHost) { h.SetBinding("builder", "a-abcdefgh2345", ext.AccountAccount, "gpt-5.4") }, func(r ext.ResolvedLaunch) bool {
			m := r.Members[1]
			return m.Account == "a-abcdefgh2345" && m.AccountSource == ext.AccountAccount && m.Model == "gpt-5.4"
		}},
		"the crew saved again": {func(h *FakeHost) { h.SetResolved("web", sampleResolved()) }, func(ext.ResolvedLaunch) bool { return true }},
	} {
		h := NewFakeHost(t)
		h.SetResolved("web", sampleResolved())
		before, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})
		c.set(h)
		after, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})
		if err != nil || after.Digest == before.Digest || !c.check(after) {
			t.Errorf("%s: the digest %s, then %s (%v); the record %+v", name, before.Digest, after.Digest, err, after.Members)
		}
	}

	// The manifest's blob is the one at the commit asked: another commit's
	// is its own.
	h.SetManifestBlob(otherSHA, strings.Repeat("7", 40))
	if at, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA}); at.Digest != base {
		t.Errorf("another commit's blob changed the digest at %s", headSHA)
	}
	if at, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: otherSHA}); at.Manifest.Blob != strings.Repeat("7", 40) || at.Digest == base {
		t.Errorf("the resolve at %s = %+v", otherSHA, at.Manifest)
	}

	// No project: none in the record, and no project folder.
	none, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web"})
	if err != nil || none.Project != "" || none.ProjectCwd != "" || none.InProject || none.Valid() != nil {
		t.Errorf("a resolve with no project = %+v, %v", none, err)
	}

	// Refusals: a request its Valid refuses, a commit the folder lacks, a
	// record past its bound.
	if _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Commit: "HEAD~1"}); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Errorf("a resolve at HEAD~1 = %v", err)
	}
	h.SetCommits(headSHA)
	if _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Commit: otherSHA}); ext.RefusalCode(err) != ext.RefusalStartCommitMissing {
		t.Errorf("a resolve at a commit the folder lacks = %v", err)
	}
	big := sampleResolved()
	big.Cwd = "/" + strings.Repeat("<", ext.MaxFolder-1)
	big.ProjectCwd = "/" + strings.Repeat("<", ext.MaxFolder-1)
	h.SetResolved("big", big)
	if _, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "big", Project: "CND"}); ext.RefusalCode(err) != ext.RefusalResolvedTooLarge {
		t.Errorf("a resolve past its bound = %v", err)
	}
	if n := len(h.Resolves()); n != 9 {
		t.Errorf("%d resolves recorded, want 9", n)
	}
	// The record answered is the caller's own.
	got.Members[0].Name = "changed"
	if again, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA}); again.Members[0].Name != "planner" {
		t.Error("a change to an answered record reached the fake")
	}
}

func TestFakeHostRefusesAChangedLaunch(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	h.SetResolved("web", sampleResolved())
	r, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})

	// The digest as resolved launches.
	ref, err := h.Launch(ctx, heldLaunch("op-1", r.Digest, headSHA))
	if err != nil {
		t.Fatalf("a launch held to its digest: %v", err)
	}
	st, _ := h.Run(ctx, ref.ID)
	if st.Origin.ResolvedDigest != r.Digest || st.Origin.StartCommit != headSHA || st.Origin.Project != "CND" {
		t.Errorf("the run's origin %+v", st.Origin)
	}

	// Each change after the resolve refuses the launch launch_changed, and
	// makes no run.
	for name, change := range map[string]func(*FakeHost){
		"the server's yolo default": func(h *FakeHost) { h.SetServerYolo(true) },
		"a selection":               func(h *FakeHost) { h.SetSelection("claude", strings.Repeat("0", 64)) },
		"the manifest":              func(h *FakeHost) { h.SetManifestBlob(headSHA, strings.Repeat("1", 40)) },
		"a binding":                 func(h *FakeHost) { h.SetBinding("planner", "default", ext.AccountOwn, "") },
		"the crew":                  func(h *FakeHost) { h.SetResolved("web", sampleResolved()) },
	} {
		h := NewFakeHost(t)
		h.SetResolved("web", sampleResolved())
		r, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND", Commit: headSHA})
		change(h)
		_, err := h.Launch(ctx, heldLaunch("op-"+strings.ReplaceAll(name, " ", "-")[:8], r.Digest, headSHA))
		if ext.RefusalCode(err) != ext.RefusalLaunchChanged {
			t.Errorf("%s changed: Launch = %v, want launch_changed", name, err)
		}
	}
	// At another commit the manifest's blob is that commit's: the same blob
	// is the same launch, another blob launch_changed. A commit the folder
	// lacks is refused as such, and a digest with no project as invalid.
	if _, err := h.Launch(ctx, heldLaunch("op-2", r.Digest, otherSHA)); err != nil {
		t.Errorf("the digest at another commit whose manifest is the same = %v", err)
	}
	h.SetManifestBlob(otherSHA, strings.Repeat("7", 40))
	if _, err := h.Launch(ctx, heldLaunch("op-2b", r.Digest, otherSHA)); ext.RefusalCode(err) != ext.RefusalLaunchChanged {
		t.Errorf("the digest at another commit whose manifest is another = %v", err)
	}
	h.SetCommits(headSHA)
	if _, err := h.Launch(ctx, heldLaunch("op-3", r.Digest, otherSHA)); ext.RefusalCode(err) != ext.RefusalStartCommitMissing {
		t.Errorf("a start commit the folder lacks = %v", err)
	}
	noProject := heldLaunch("op-4", r.Digest, headSHA)
	noProject.Project = ""
	if _, err := h.Launch(ctx, noProject); ext.RefusalCode(err) != ext.RefusalInvalidRequest {
		t.Errorf("a digest with no project = %v", err)
	}
	// A retry of the launch that was made answers its run, whatever moved.
	h.SetServerYolo(true)
	if again, err := h.Launch(ctx, heldLaunch("op-1", r.Digest, headSHA)); err != nil || again != ref {
		t.Errorf("a retry of the launch made = %+v, %v; want %+v", again, err, ref)
	}
}

func TestFakeHostCapacityLeavesTheDigest(t *testing.T) {
	ctx := context.Background()
	h := NewFakeHost(t)
	h.SetResolved("web", sampleResolved())
	h.SetBinding("builder", "a-abcdefgh2345", ext.AccountAccount, "")
	before, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND"})
	on := true
	view := ext.CapacityView{
		Agent: "codex", Account: "a-abcdefgh2345", Label: "Work", State: "ok", Fresh: ext.CapacityFresh, Running: 2,
		Windows: []ext.CapacityWindow{{Kind: "primary", Name: "5-hour", Percent: 80, Severity: "warning"}},
		Credits: []ext.CapacityCredit{{Kind: "credits", Enabled: &on, Balance: "3.10"}},
	}
	h.SetCapacity(view)
	after, err := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND"})
	if err != nil || after.Digest != before.Digest || len(after.Capacity) != 1 || after.Capacity[0].Windows[0].Percent != 80 {
		t.Fatalf("with a capacity view: %+v, %v; the digest %s, before %s", after.Capacity, err, after.Digest, before.Digest)
	}
	if err := after.Valid(); err != nil {
		t.Errorf("the record with its view: %v", err)
	}
	// A launch held to the digest resolved before the views launches.
	if _, err := h.Launch(ctx, heldLaunch("op-1", before.Digest, "")); err != nil {
		t.Errorf("a launch after the views changed: %v", err)
	}
	// What the fake answered is the caller's own, and what it was given
	// is its own.
	after.Capacity[0].Windows[0].Percent = 1
	*after.Capacity[0].Credits[0].Enabled = false
	view.Windows[0].Percent = 2
	if again, _ := h.ResolveLaunch(ctx, ext.ResolveRequest{Crew: "web", Project: "CND"}); again.Capacity[0].Windows[0].Percent != 80 || !*again.Capacity[0].Credits[0].Enabled {
		t.Errorf("a change to an answered or a given view reached the fake: %+v", again.Capacity)
	}
}
