package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// What a capture lets in (Program item PV1, docs/plans/browser-evidence.md,
// "The evidence rules"): judged in one form, every relation the rules
// state the store's own, each refused in checkCapture with a row below.

// refusedAs is checkCapture's answer as a refusal's code, "" for taken.
func refusedAs(t *testing.T, n NewCapture, f CaptureFacts, image bool) string {
	t.Helper()
	_, _, err := checkCapture(n, f, image)
	var ref *Refusal
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ref):
		return ref.Code
	}
	t.Fatalf("not a refusal: %v", err)
	return ""
}

// linkCapture is a capture of a link: no run, no tree, no scope.
func linkCapture(image bool) (NewCapture, CaptureFacts) {
	n, _ := testCapture(image)
	n.Label = TreeRead{}
	moment(&n)
	return n, CaptureFacts{Origin: OriginURL}
}

// candidateCapture is a capture of a candidate's preview checkout.
func candidateCapture() (NewCapture, CaptureFacts) {
	n, f := testCapture(true)
	f.Origin, f.Tree, f.Mutable = OriginCandidate, TreePreview, false
	f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, f.Commit)
	return n, f
}

// The privacy rule compares names in one form: an element's tag and an
// attribute's name are refused unless they are names (no white space or
// control character anywhere), and an input's (its local name, without
// regard to case, with a prefix or none) value, or a password field's, is
// never kept; legal names of every case are kept as given.
func TestThePrivacyRuleComparesNamesInOneForm(t *testing.T) {
	type attr = [2]string
	value := attr{"value", "hunter2"}
	var cases []struct {
		tag   string
		attrs [][2]string
		taken bool
	}
	add := func(taken bool, tag string, attrs ...[2]string) {
		cases = append(cases, struct {
			tag   string
			attrs [][2]string
			taken bool
		}{tag, attrs, taken})
	}
	// Not names: white space of each kind before, inside and after.
	for _, ws := range []string{" ", "\t", "\n", "\r", " ", " ", "　", "\x00", "\x1b"} {
		add(false, ws+"input", value)
		add(false, "input"+ws, value)
		add(false, "in"+ws+"put", value)
		add(false, "div", attr{ws + "value", "v"})
		add(false, "div", attr{"value" + ws, "v"})
		add(false, "div", attr{"va" + ws + "lue", "v"})
	}
	add(false, "", value)
	// An input in any case and with any prefix keeps no value, whatever
	// its type says (missing, misspelt, a password in any case), the value
	// named in any case, with a prefix, or twice.
	for _, tag := range []string{"input", "INPUT", "Input", "iNpUt", "html:input", "x:INPUT", "a:b:Input"} {
		for _, name := range []string{"value", "VALUE", "VaLuE", "xml:value", "x:VALUE"} {
			add(false, tag, attr{name, "hunter2"})
		}
		add(false, tag, attr{"type", "passwrod"}, value)
		add(false, tag, attr{"TYPE", "PassWord"}, value)
		add(false, tag, value, attr{"VALUE", "again"})
		add(true, tag, attr{"type", "text"}, attr{"data-value", "x"}, attr{"valueAsNumber", "1"})
	}
	// A password field that is no input: its type in any case and spelling
	// of its name, its value refused.
	for _, typ := range []attr{{"type", "password"}, {"TYPE", "PASSWORD"}, {"Type", " Password "}, {"html:type", "password"}} {
		add(false, "div", typ, value)
		add(false, "custom-field", value, typ)
	}
	// Legal names are kept as the page has them: a value on no secret
	// field, an SVG element's mixed-case attribute, a prefixed one.
	add(true, "div", attr{"type", "text"}, attr{"value", "shown"})
	add(true, "svg", attr{"viewBox", "0 0 1 1"}, attr{"xlink:href", "#a"})
	add(true, "svg:foreignObject", attr{"xml:lang", "en"})
	for _, c := range cases {
		n, f := testCapture(false)
		e := element(&n)
		e.Tag, e.Attributes = c.tag, c.attrs
		moment(&n)
		got := refusedAs(t, n, f, false)
		if c.taken != (got == "") || (!c.taken && got != RefusalInvalid) {
			t.Errorf("tag %q attributes %q: refused %q, taken wanted %v", c.tag, c.attrs, got, c.taken)
		}
	}
	// Through the store: nothing refused is kept anywhere.
	s := openStore(t, storeDir(t))
	for i, c := range cases {
		if c.taken {
			continue
		}
		n, f := testCapture(false)
		e := element(&n)
		e.Tag, e.Attributes = c.tag, c.attrs
		moment(&n)
		rc, err := create(t, s, opID("secret", i), n, f, nil)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_annotations WHERE element LIKE '%hunter2%'"); n != 0 {
		t.Fatalf("%d annotations hold a secret field's value", n)
	}
}

// Every relation the evidence rules state is the store's, refused in
// checkCapture: stability only with pixels and never in motion; a link's
// capture with no run, no tree and no scope; the attempt's and the
// candidate's origins; one capture one moment, each of its parts; the
// drift's own words; a changed path by its field rule; the frame's scroll
// at what a browser scrolls to; how the screenshot was taken, the client's
// word, checked; and a note's bound through EditCapture.
func TestEveryRelationOfTheEvidenceIsTheStores(t *testing.T) {
	yes, no := true, false
	type mutate func(n *NewCapture, f *CaptureFacts)
	rows := []struct {
		name  string
		base  func() (NewCapture, CaptureFacts)
		image bool
		set   mutate
		code  string
	}{
		// Stability.
		{"stable with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &yes }, RefusalInvalid},
		{"unstable with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &no }, RefusalInvalid},
		{"stable in motion", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Stable, element(n).State.Motion = &yes, 1
		}, RefusalInvalid},
		{"unstable in motion", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Stable, element(n).State.Motion = &no, 1
		}, ""},
		{"stable and still", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Stable = &yes }, ""},
		// A link's capture.
		{"a link", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true, func(*NewCapture, *CaptureFacts) {}, ""},
		{"a link with a crew", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Crew = "crew" }, RefusalInvalid},
		{"a link with a repository", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "/repo" }, RefusalInvalid},
		{"a link with a project", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Scope.Project = "PRJ" }, RefusalInvalid},
		{"a link naming a run", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-0123abcd" }, RefusalInvalid},
		{"a link naming a tree", func() (NewCapture, CaptureFacts) { return linkCapture(true) }, true,
			func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreeWorktree }, RefusalInvalid},
		// The attempt's and the candidate's origins.
		{"an attempt with a candidate", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, f.Commit)
		}, RefusalInvalid},
		{"an attempt of a preview", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreePreview }, RefusalInvalid},
		{"an attempt not mutable", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.Mutable = false }, RefusalInvalid},
		{"a candidate", candidateCapture, true, func(*NewCapture, *CaptureFacts) {}, ""},
		{"a candidate of a worktree", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) { f.Tree = TreeWorktree }, RefusalInvalid},
		{"a candidate of another attempt", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt+1, f.Commit)
		}, RefusalInvalid},
		{"a candidate of another commit", candidateCapture, true, func(_ *NewCapture, f *CaptureFacts) {
			f.Candidate = fmt.Sprintf("%s/%s/%d@%s", f.Run, f.Task, f.Attempt, strings.Repeat("b", 40))
		}, RefusalInvalid},
		// One capture, one moment: each part on its own.
		{"observed at another URL", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.URL += "?b" }, RefusalInvalid},
		{"observed in another viewport", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Viewport.Width++ }, RefusalInvalid},
		{"observed at another scroll", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Viewport.ScrollY = 9 }, RefusalInvalid},
		{"observed at another epoch", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Epoch++ }, RefusalInvalid},
		{"observed at another revision", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Observed.Revision = "abcdef0" }, RefusalInvalid},
		{"observed at another commit", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Label.Commit = strings.Repeat("b", 40)
		}, RefusalInvalid},
		{"observed at another content", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Annotations[0].Observed.Label.Content = strings.Repeat("d", 40)
		}, RefusalInvalid},
		// The drift's own words.
		{"a read counting fewer than none", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.Changed = -1 }, RefusalInvalid},
		{"a drift kept with no time", nil, true, func(n *NewCapture, _ *CaptureFacts) {
			n.Label.Content, n.Label.ReadAt = strings.Repeat("d", 40), time.Time{}
			moment(n)
		}, RefusalInvalid},
		{"no drift and no time", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Label.ReadAt = time.Time{} }, ""},
		// A changed path, by its field rule.
		{"a changed path with an escape", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{"web/\x1b[31mred.vue"} }, RefusalInvalid},
		{"a changed path with a NUL", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{"web/a\x00.vue"} }, RefusalInvalid},
		{"an empty changed path", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{""} }, RefusalInvalid},
		{"a changed path at its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) { f.ChangedPaths = []string{rep("p", MaxChangedPathsBytes)} }, ""},
		{"a changed path past its bound", nil, true, func(_ *NewCapture, f *CaptureFacts) {
			f.ChangedPaths = []string{rep("p", MaxChangedPathsBytes+1)}
		}, RefusalBound},
		// The frame's scroll: what a browser scrolls to.
		{"a scroll past a rectangle's bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollY = MaxRectCoord * 4; moment(n) }, ""},
		{"a scroll at its bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollY = MaxScroll; moment(n) }, ""},
		{"a scroll past its bound", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Viewport.ScrollX = -MaxScroll - 1; moment(n) }, RefusalInvalid},
		// How the screenshot was taken.
		{"a tab's screenshot from the desktop", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via = CaptureViaDesktop }, ""},
		{"the desktop's from the desktop", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.Via, n.ImageHow = CaptureViaDesktop, ImageDesktop }, ""},
		{"the desktop's from the web", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageDesktop }, RefusalInvalid},
		{"none with a screenshot", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageNone }, RefusalInvalid},
		{"no word with a screenshot", nil, true, func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = "" }, RefusalInvalid},
		{"a tab's with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = ImageTab }, RefusalInvalid},
		{"no word with no screenshot", func() (NewCapture, CaptureFacts) { return testCapture(false) }, false,
			func(n *NewCapture, _ *CaptureFacts) { n.ImageHow = "" }, RefusalInvalid},
	}
	for _, r := range rows {
		base := r.base
		if base == nil {
			base = func() (NewCapture, CaptureFacts) { return testCapture(true) }
		}
		n, f := base()
		r.set(&n, &f)
		if got := refusedAs(t, n, f, r.image); got != r.code {
			t.Errorf("%s: refused %q, want %q", r.name, got, r.code)
		}
	}
	// A note's bound through an edit, at its edge and one past.
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 30))
	edits := kept(captureOf(t, s, id))
	edits[0].Note = rep("é", MaxAnnotationNote+1)
	rc, err := reedit(t, s, "past", id, 1, edits)
	want(t, rc, err, StatusFailed, RefusalBound)
	edits[0].Note = rep("é", MaxAnnotationNote)
	rc, err = reedit(t, s, "at", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	edits = kept(captureOf(t, s, id))
	edits[1].Note = "a\x1bb"
	rc, err = reedit(t, s, "control", id, 2, edits)
	want(t, rc, err, StatusFailed, RefusalInvalid)
}

// The serving state and the drift are the store's reading of the facts:
// serving declared only for the tree's commit (and its content when it has
// changes) over content that was read, stale for another, unconfirmed for
// none, a commit alone over changes, content not read, or a link; the
// drift kept when the selection's commit or content differs from the
// save's or either was not read, and never for a link.
func TestTheServingStateAndTheDriftFromTheFacts(t *testing.T) {
	commit7, content7 := capCommit[:7], contentA[:7]
	other := strings.Repeat("b", 40)
	for _, c := range []struct {
		name    string
		page    string
		content string
		changed int
		link    bool
		want    string
	}{
		{"a commit over a clean tree", commit7, contentA, 0, false, ServingDeclared},
		{"a commit and its content", commit7 + "+" + content7, contentA, 1, false, ServingDeclared},
		{"the full ids", capCommit + "+" + contentA, contentA, 1, false, ServingDeclared},
		{"another commit", other[:7], contentA, 0, false, ServingStale},
		{"another commit with content", other[:7] + "+" + content7, contentA, 1, false, ServingStale},
		{"another content", commit7 + "+" + other[:7], contentA, 1, false, ServingStale},
		{"a commit alone over changes", commit7, contentA, 1, false, ServingUnconfirmed},
		{"a commit and content not read", commit7 + "+" + content7, "", 1, false, ServingUnconfirmed},
		{"a commit alone over content not read", commit7, "", 0, false, ServingUnconfirmed},
		{"nothing declared", "", contentA, 0, false, ServingUnconfirmed},
		{"a link", commit7, "", 0, true, ServingUnconfirmed},
	} {
		n, f := testCapture(true)
		f.Content, f.Changed, f.ChangedPaths = c.content, c.changed, nil
		if c.link {
			n, f = linkCapture(true)
		}
		n.Revision = c.page
		moment(&n)
		got, _, err := checkCapture(n, f, true)
		if err != nil || got.Origin.Serving != (Serving{State: c.want, Page: c.page}) {
			t.Errorf("%s: %+v %v, want %s", c.name, got.Origin.Serving, err, c.want)
		}
	}
	readAt := momentAt.Add(-time.Minute)
	for _, c := range []struct {
		name            string
		commit, content string
		saveContent     string
		link            bool
		kept            bool
	}{
		{"all equal", capCommit, contentA, contentA, false, false},
		{"the commit differs", other, contentA, contentA, false, true},
		{"the content differs", capCommit, other, contentA, false, true},
		{"the selection read no content", capCommit, "", contentA, false, true},
		{"the save read no content", capCommit, contentA, "", false, true},
		{"neither read content", capCommit, "", "", false, true},
		{"a link", "", "", "", true, false},
	} {
		n, f := testCapture(true)
		f.Content = c.saveContent
		if c.link {
			n, f = linkCapture(true)
		}
		n.Label = TreeRead{Commit: c.commit, Content: c.content, Changed: 3, ReadAt: readAt}
		moment(&n)
		got, _, err := checkCapture(n, f, true)
		want := (*TreeRead)(nil)
		if c.kept {
			want = &TreeRead{Commit: c.commit, Content: c.content, Changed: 3, ReadAt: readAt}
		}
		if err != nil || (got.Origin.Drift == nil) != (want == nil) || (want != nil && *got.Origin.Drift != *want) {
			t.Errorf("%s: %+v %v, want %+v", c.name, got.Origin.Drift, err, want)
		}
	}
}

// Two addresses are of one origin when their scheme, their host without
// regard to case and their effective port agree.
func TestPageOriginsInOneForm(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"http://localhost/a", "http://localhost:80/b", true},
		{"https://Example.COM/a", "https://example.com:443/", true},
		{"HTTP://h:080/", "http://H/x?y#z", true},
		{"http://[::1]:5173/", "http://[::1]:5173/x", true},
		{"http://localhost:5173/", "http://localhost:5174/", false},
		{"http://localhost/", "https://localhost/", false},
		{"http://localhost:443/", "https://localhost/", false},
		{"http://a.example/", "http://b.example/", false},
	} {
		if got := pageOrigin(c.a) == pageOrigin(c.b) && pageOrigin(c.a) != ""; got != c.same {
			t.Errorf("%s and %s (%q, %q): one origin %v, want %v", c.a, c.b, pageOrigin(c.a), pageOrigin(c.b), got, c.same)
		}
	}
}

// A retake is related by the store: run for the same run and task, repo
// for the same repository; a link's retakes a link's of the same origin
// (written in another form among them) and records no relation; a deleted
// capture, another repository's, another origin's and a link and a run's
// retaking each other are refused, nothing written.
func TestARetakeIsRelatedByTheStore(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(false)
	first := mustCreate(t, s, "first", n, f, nil)
	ln, lf := linkCapture(false)
	ln.URL = "http://Localhost:80/page"
	moment(&ln)
	link := mustCreate(t, s, "link", ln, lf, nil)
	gone := mustCreate(t, s, "gone", n, f, nil)
	rc, err := s.DeleteCapture(ctx, testOp("delete"), gone)
	want(t, rc, err, StatusDone, "")
	retake := func(opID, of string, n NewCapture, f CaptureFacts) (Receipt, error) {
		n.RetakeOf = of
		return create(t, s, opID, n, f, nil)
	}
	for i, c := range []struct {
		name string
		of   string
		set  func(n *NewCapture, f *CaptureFacts)
		via  string
		code string
	}{
		{"the same run and task", first, func(*NewCapture, *CaptureFacts) {}, RetakeRun, ""},
		{"another attempt of the task", first, func(_ *NewCapture, f *CaptureFacts) { f.Attempt = 3 }, RetakeRun, ""},
		{"another task", first, func(_ *NewCapture, f *CaptureFacts) { f.Task = "test" }, RetakeRepo, ""},
		{"another run of the repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-89abcdef" }, RetakeRepo, ""},
		{"another repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "/other" }, "", RefusalInvalid},
		{"no repository", first, func(_ *NewCapture, f *CaptureFacts) { f.Scope.Repo = "" }, "", RefusalInvalid},
		{"a deleted capture", gone, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"no capture", newID(), func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
		{"a run's retaking a link's", link, func(*NewCapture, *CaptureFacts) {}, "", RefusalInvalid},
	} {
		n, f := testCapture(false)
		c.set(&n, &f)
		t.Log(c.name)
		rc, err := retake(opID("retake", i), c.of, n, f)
		if c.code != "" {
			want(t, rc, err, StatusFailed, c.code)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if got := captureOf(t, s, rc.ID); got.RetakeOf != c.of || got.RetakeVia != c.via {
			t.Errorf("%s: retake of %s via %q, want %q", c.name, got.RetakeOf, got.RetakeVia, c.via)
		}
	}
	for i, c := range []struct {
		name, url, of, code string
	}{
		{"a link of the same origin", "http://localhost/other?x#y", link, ""},
		{"a link of another port", "http://localhost:8080/page", link, RefusalInvalid},
		{"a link of another scheme", "https://localhost/page", link, RefusalInvalid},
		{"a link's retaking a run's", "http://localhost/page", first, RefusalInvalid},
	} {
		n, f := linkCapture(false)
		n.URL = c.url
		moment(&n)
		t.Log(c.name)
		rc, err := retake(opID("link-retake", i), c.of, n, f)
		if c.code != "" {
			want(t, rc, err, StatusFailed, c.code)
			continue
		}
		want(t, rc, err, StatusDone, "")
		if got := captureOf(t, s, rc.ID); got.RetakeOf != c.of || got.RetakeVia != "" {
			t.Errorf("%s: retake of %s via %q", c.name, got.RetakeOf, got.RetakeVia)
		}
	}
}
