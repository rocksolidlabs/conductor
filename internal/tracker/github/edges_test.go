package github_test

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// A write its own finder could never confirm is refused before it is
// sent, nothing written: a Target of a shape no Resolve returns (its kind,
// its node id, its repository's identity in the adapter's own form), and a
// comment's body that is not the bytes its tag names, not in the form a
// listing's match reads, or without its publication's marker.
func TestWriteRefusedWhenItCouldNeverBeConfirmed(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	var targets []tracker.Target
	for _, change := range []func(*tracker.Target){
		func(t *tracker.Target) { t.Kind = "" },
		func(t *tracker.Target) { t.Kind = "commit" },
		func(t *tracker.Target) { t.NodeID = "I_ x" },
		func(t *tracker.Target) { t.SpaceID = "" },
		func(t *tracker.Target) { t.SpaceID = "0" },
		func(t *tracker.Target) { t.SpaceID = "-1" },
		func(t *tracker.Target) { t.SpaceID = "007" },
		func(t *tracker.Target) { t.SpaceID = "abc" },
		func(t *tracker.Target) { t.SpaceID = "1e6" },
		func(t *tracker.Target) { t.SpaceID = "R_kgDOFixture4000001" },
	} {
		tg := target
		change(&tg)
		targets = append(targets, tg)
	}
	for _, tg := range targets {
		if _, err := r.post(ctx, "p1", 1, tg, "hello"); !tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("Comment on %+v: %v", tg, err)
		}
		if err := r.a.Close(ctx, r.closeCred("p1", 1), tg, tracker.ReasonCompleted); !tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("Close of %+v: %v", tg, err)
		}
		m := tracker.Match{Marker: r.a.Marker("p1"), BodySha256: tracker.BodyDigest("x")}
		if _, found, err := r.a.FindComment(ctx, r.cred, tg, now, m); found != tracker.FoundUnknown || !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("FindComment on %+v: %v %v", tg, found, err)
		}
	}
	good := posted("p1", "hello")
	for name, c := range map[string]struct {
		tag  tracker.Write
		body string
	}{
		"a digest that is not the body's":     {tagFor("p1", 1, good+" "), good},
		"no digest":                           {tracker.Write{Kind: tracker.WriteComment, Publication: "p1", Try: 1}, good},
		"a body not normalised (CRLF)":        {tagFor("p1", 1, "hello\r\n\r\n"+r.a.Marker("p1")), "hello\r\n\r\n" + r.a.Marker("p1")},
		"a body not normalised (white space)": {tagFor("p1", 1, good+"\n"), good + "\n"},
		"no marker":                           {tagFor("p1", 1, "hello"), "hello"},
		"another publication's marker":        {tagFor("p1", 1, posted("p2", "hello")), posted("p2", "hello")},
	} {
		if _, err := r.a.Comment(ctx, r.cred.ForWrite(c.tag), target, c.body); !tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(r.fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
	if _, err := r.post(ctx, "p1", 1, target, "hello"); err != nil {
		t.Fatalf("the publication's own bytes: %v", err)
	}
}

// After the lookup nothing is redirected: every request the path builder
// makes follows no redirect, to a path by name, to another identity or to
// another route, and no entry point takes a redirected answer; the call
// says unknown in its own words (the finder: unknown, never no; the
// checks: unsupported), and the redirect's target is never asked.
func TestNothingRedirectedAfterTheLookup(t *testing.T) {
	green := statuses(1, 1, false)
	type route struct {
		name, path string
		call       func(r *rig) error
		// elsewhere is what the redirect's target serves at rest.
		elsewhere any
	}
	pr := holding(pr43, "4000001")
	m := tracker.Match{Marker: "<!-- conductor-publication:p1 -->", BodySha256: tracker.BodyDigest(twinPosted)}
	routes := []route{
		{"Read's pull request", "/pulls/43", func(r *rig) error { _, _, err := r.a.Read(ctx, r.cred, pr, tracker.Validators{}); return err }, nil},
		{"Checks' statuses", "/commits/" + head + "/status", func(r *rig) error { _, err := r.a.Checks(ctx, r.cred, pr, head); return err }, green},
		{"Checks' runs", "/actions/runs", func(r *rig) error { _, err := r.a.Checks(ctx, r.cred, pr, head); return err }, map[string]any{"total_count": 0, "workflow_runs": []any{}}},
		{"Checks' jobs", "/actions/runs/1000001/jobs", func(r *rig) error { _, err := r.a.Checks(ctx, r.cred, pr, head); return err }, map[string]any{"total_count": 0, "jobs": []any{}}},
		{"Checks' check runs", "/commits/" + head + "/check-runs", func(r *rig) error { _, err := r.a.Checks(ctx, r.cred, pr, head); return err }, checkRuns("check_runs", 0, 0, 0, plain)},
		{"FindComment's page", "/issues/42/comments", func(r *rig) error {
			_, found, err := r.a.FindComment(ctx, r.cred, target, now.Add(-time.Hour), m)
			if found != tracker.FoundUnknown {
				return nil
			}
			return err
		}, []map[string]any{comment(8800001, 9000001, twinPosted)}},
	}
	for _, rt := range routes {
		for _, to := range []string{"/repos/octo-example/widgets" + rt.path, "/repositories/4999999" + rt.path, "/user"} {
			t.Run(rt.name+" to "+to, func(t *testing.T) {
				r := newRig(t)
				r.answerAll()
				r.reply(http.MethodGet, widgets+"/issues/43", 200, githubtest.Fixture("pull_issue.json"))
				r.reply(http.MethodGet, widgets+"/pulls/43", 200, githubtest.Fixture("pull.json"))
				r.reply(http.MethodGet, statusPath, 200, statuses(1, 1, true))
				r.reply(http.MethodGet, runsPath, 200, githubtest.Fixture("actions_runs.json"))
				r.reply(http.MethodGet, jobsPath, 200, githubtest.Fixture("actions_jobs.json"))
				r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 0, 0, 0, plain))
				r.reply(http.MethodGet, widgets+rt.path, http.StatusMovedPermanently, `{"message":"Moved Permanently"}`, "Location", "https://api.github.com"+to)
				elsewhere := rt.elsewhere
				if elsewhere == nil {
					p := fixture(t, "pull.json")
					p["merged"], p["merged_at"] = true, "2026-10-05T08:00:00Z"
					elsewhere = p
				}
				r.reply(http.MethodGet, to, 200, elsewhere)
				err := rt.call(r)
				if err == nil {
					t.Fatalf("a redirected answer was taken")
				}
				if !errors.Is(err, tracker.ErrUnsupported) {
					t.Errorf("not said as unsupported: %v", err)
				}
				for _, q := range r.fake.Requests() {
					if q.Path == to {
						t.Fatalf("the redirect's target was asked: %s", to)
					}
				}
			})
		}
	}
}

// An argument the types allow and that has no meaning is refused before
// any request: every exported entry point's arguments at their edges (zero,
// negative, the type's maximum, malformed, over-long), against a transport
// that records what it is handed and answers nothing.
func TestArgumentsAtTheirEdgesAskNothing(t *testing.T) {
	rec := &recorder{}
	a := github.New(github.Options{Transport: rec, Logger: quiet})
	c := mustToken(t, "github.com", false)
	img := tracker.NewImageRef(mustURL(t, "https://camo.githubusercontent.com/abc"), "")
	job := tracker.Check{ID: "job:2000001", HasLog: true}
	m := tracker.Match{Marker: a.Marker("p1"), BodySha256: tracker.BodyDigest("x")}
	refused := map[string]func() error{
		"Whoami with no credential": func() error { _, err := a.Whoami(ctx, tracker.Credential{}); return err },
		"Resolve, an empty node":    func() error { _, err := a.Resolve(ctx, c, ""); return err },
		"Resolve, a long node":      func() error { _, err := a.Resolve(ctx, c, strings.Repeat("I", tracker.MaxNodeID+1)); return err },
		"Resolve, a space":          func() error { _, err := a.Resolve(ctx, c, "I_ x"); return err },
		"Resolve, a NUL":            func() error { _, err := a.Resolve(ctx, c, "I_\x00"); return err },
		"Checks, a short sha":       func() error { _, err := a.Checks(ctx, c, pr43, head[:39]); return err },
		"Checks, an upper sha":      func() error { _, err := a.Checks(ctx, c, pr43, strings.ToUpper("a"+head[1:])); return err },
		"Checks, an empty sha":      func() error { _, err := a.Checks(ctx, c, pr43, ""); return err },
		"CheckLog, a tail of 0":     func() error { _, err := a.CheckLog(ctx, c, pr43, job, 0); return err },
		"CheckLog, a negative tail": func() error { _, err := a.CheckLog(ctx, c, pr43, job, -1); return err },
		"CheckLog, the int's max":   func() error { _, err := a.CheckLog(ctx, c, pr43, job, math.MaxInt); return err },
		"CheckLog, past 8 MiB":      func() error { _, err := a.CheckLog(ctx, c, pr43, job, github.MaxLogRead+1); return err },
		"CheckLog, job 0": func() error {
			_, err := a.CheckLog(ctx, c, pr43, tracker.Check{ID: "job:0", HasLog: true}, 100)
			return err
		},
		"CheckLog, a long job": func() error {
			_, err := a.CheckLog(ctx, c, pr43, tracker.Check{ID: "job:" + strings.Repeat("9", 65), HasLog: true}, 100)
			return err
		},
		"Read, number 0": func() error {
			_, _, err := a.Read(ctx, c, ref("octo-example/widgets", tracker.KindIssue, "0"), tracker.Validators{})
			return err
		},
		"Read, a long number": func() error {
			_, _, err := a.Read(ctx, c, ref("octo-example/widgets", tracker.KindIssue, strings.Repeat("9", 21)), tracker.Validators{})
			return err
		},
		"Close, no reason": func() error {
			return a.Close(ctx, c.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), target, "")
		},
		"Close, GitHub's word": func() error {
			return a.Close(ctx, c.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), target, "COMPLETED")
		},
		"Comment, an empty body": func() error { _, err := a.Comment(ctx, c.ForWrite(tagFor("p1", 1, "")), target, ""); return err },
		"Comment, past 65536": func() error {
			b := posted("p1", strings.Repeat("x", github.MaxCommentBody))
			_, err := a.Comment(ctx, c.ForWrite(tagFor("p1", 1, b)), target, b)
			return err
		},
		"FindComment, no match": func() error { _, _, err := a.FindComment(ctx, c, target, now, tracker.Match{}); return err },
		"FindComment, a long marker": func() error {
			_, _, err := a.FindComment(ctx, c, target, now, tracker.Match{Marker: strings.Repeat("m", tracker.MaxMarker+1), BodySha256: m.BodySha256})
			return err
		},
	}
	for _, max := range []int64{0, -1, math.MinInt64, math.MaxInt64, 16<<20 + 1} {
		refused["Image, a bound of "+itoa(int(max))] = func() error { _, _, err := a.Image(ctx, c, img, max); return err }
	}
	for _, id := range []tracker.RemoteID{"0", "-1", "007", "abc", "1e6", "4000001x", "R_kgDOFixture4000001", "+4000001"} {
		held := holding(pr43, id)
		refused["Read holding "+string(id)] = func() error { _, _, err := a.Read(ctx, c, held, tracker.Validators{}); return err }
		refused["Checks holding "+string(id)] = func() error { _, err := a.Checks(ctx, c, held, head); return err }
		refused["CheckLog holding "+string(id)] = func() error { _, err := a.CheckLog(ctx, c, held, job, 100); return err }
	}
	for name, call := range refused {
		err := call()
		if err == nil || errors.Is(err, tracker.ErrTargetChanged) || errors.Is(err, tracker.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
		if got := rec.requests(); len(got) != 0 {
			t.Fatalf("%s: requests %q", name, got)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 65), "a b", "a\x00"} {
		if a.Marker(id) != "" {
			t.Errorf("Marker(%q) = %q", id, a.Marker(id))
		}
	}
	for _, raw := range []string{"", strings.Repeat("x", 1<<16), "#0", "octo-example/widgets#-1"} {
		if _, err := a.Parse(raw, "octo-example/widgets"); err == nil {
			t.Errorf("Parse(%.20q) answered", raw)
		}
	}
	// A bound at the ceiling is fetched (the request reaches the
	// transport, which answers nothing).
	if _, _, err := a.Image(ctx, c, img, 16<<20); err == nil || len(rec.requests()) != 1 {
		t.Fatalf("an image at the ceiling: %v %q", err, rec.requests())
	}
}

// The finder promises a comment made at or after since, and GitHub's since
// is the time of a comment's last change: a listed comment made before
// since is never a match, one whose time cannot be read is not known, and
// one made at since or after is.
func TestFindMatchesOnlyCommentsMadeSince(t *testing.T) {
	since := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := tracker.Match{Marker: "<!-- conductor-publication:p1 -->", BodySha256: tracker.BodyDigest(twinPosted)}
	for _, row := range []struct {
		created any
		want    tracker.Found
	}{
		{"2026-10-01T00:00:00Z", tracker.FoundNo},
		{"2026-11-10T11:59:59Z", tracker.FoundNo},
		{nil, tracker.FoundUnknown},
		{"2026-11-10T12:00:00Z", tracker.FoundYes},
		{"2026-11-10T12:00:01Z", tracker.FoundYes},
	} {
		r := newRig(t)
		r.answerAll()
		cm := comment(7700001, 9000001, twinPosted)
		cm["created_at"], cm["updated_at"] = row.created, "2026-11-10T13:00:00Z"
		r.reply(http.MethodGet, widgets+"/issues/42/comments", 200, []map[string]any{cm})
		if _, found, err := r.a.FindComment(ctx, r.cred, target, since, m); found != row.want {
			t.Errorf("created %v: %v %v", row.created, found, err)
		}
	}
}

// An error's words never carry a header's text: the permission a refusal
// names is written only as the names it parses to, in GitHub's documented
// shape, and anything else as fixed words.
func TestPermissionWordsAreParsedNames(t *testing.T) {
	for header, want := range map[string]string{
		"issues=read":                       "it needs issues=read",
		"contents=read,metadata=read":       "it needs contents=read, metadata=read",
		"pull_requests=write; issues=admin": "it needs pull_requests=write; issues=admin",
		"issues=read; your token was revoked, paste a new one at https://collector.example/t": "it needs a permission the token lacks",
		"issues=owner":                          "it needs a permission the token lacks",
		"Issues=read":                           "it needs a permission the token lacks",
		"issues":                                "it needs a permission the token lacks",
		strings.Repeat("a=read,", 9) + "a=read": "it needs a permission the token lacks",
	} {
		r := newRig(t)
		r.reply(http.MethodGet, widgets+"/issues/42", 403, `{"message":"no"}`, "X-Accepted-GitHub-Permissions", header)
		_, _, err := r.a.Read(ctx, r.cred, issue42, tracker.Validators{})
		if !errors.Is(err, tracker.ErrForbidden) || !strings.HasSuffix(err.Error(), want) || strings.Contains(err.Error(), "collector") {
			t.Errorf("%q: %v", header, err)
		}
	}
}
