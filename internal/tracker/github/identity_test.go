package github_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// twinBlob is the storage host the twin's log is kept on.
const twinBlob = "productionresultssa2.blob.core.windows.net"

// twin answers every request by name under octo-example/widgets/ (the
// repository's lookup itself aside) as another repository that holds the
// name now, a fork among them, would: the same numbers and the same
// commits, another identity, the pull request merged and every check
// green, a matching comment, a log of its own, and a 304 to any validator.
// Asking it at all fails the test: after its lookup the adapter names the
// repository by the identity the lookup read.
func (r *rig) twin(t *testing.T) {
	merged := fixture(t, "pull.json")
	merged["merged"], merged["merged_at"], merged["state"], merged["node_id"] = true, "2026-10-05T08:00:00Z", "closed", "PR_kwDOTwin43"
	for _, side := range []string{"head", "base"} {
		merged[side].(map[string]any)["repo"] = repoAnswer(4999999, "octo-example/widgets")
	}
	pullIssue := fixture(t, "pull_issue.json")
	pullIssue["node_id"], pullIssue["state"] = "PR_kwDOTwin43", "closed"
	issue := fixture(t, "issue.json")
	issue["node_id"] = "I_kwDOTwin42"
	green := func(key string) map[string]any {
		return map[string]any{"total_count": 1, key: []map[string]any{{"id": 2999999, "name": "test", "status": "completed", "conclusion": "success", "head_sha": head, "app": map[string]any{"slug": "github-actions"}}}}
	}
	answers := map[string]any{
		"issues/43":                       pullIssue,
		"pulls/43":                        merged,
		"issues/42":                       issue,
		"issues/42/comments":              []map[string]any{comment(8800001, 9000001, twinPosted)},
		"commits/" + head + "/status":     map[string]any{"state": "success", "total_count": 1, "statuses": []map[string]any{{"id": 3999999, "context": "ci/build", "state": "success", "created_at": "2026-10-04T12:00:00Z", "updated_at": "2026-10-04T12:01:00Z"}}},
		"actions/runs":                    map[string]any{"total_count": 1, "workflow_runs": []map[string]any{{"id": 1000001, "name": "CI"}}},
		"actions/runs/1000001/jobs":       green("jobs"),
		"commits/" + head + "/check-runs": green("check_runs"),
	}
	r.fake.Otherwise(func(w http.ResponseWriter, req *http.Request) {
		rest, ok := strings.CutPrefix(req.URL.Path, "/repos/octo-example/widgets/")
		if req.Host != api || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("asked by name after the repository's lookup: %s %s", req.Method, req.URL.Path)
		if req.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if rest == "actions/jobs/2000001/logs" {
			w.Header().Set("Location", "https://"+twinBlob+"/twin/job-logs.txt?sig=s")
			w.WriteHeader(http.StatusFound)
			return
		}
		body, ok := answers[rest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"twin"`)
		_, _ = w.Write(githubtest.Body(body))
	})
	var asked []string
	r.fake.Handle(http.MethodGet, twinBlob, "/twin/job-logs.txt", azureBlob("the twin's log\n", &asked))
}

// twinPosted is the publication the twin's #42 holds and the target's does
// not.
var twinPosted = "Fixed in the delivery.\n\n<!-- conductor-publication:p1 -->"

// After its lookup, the adapter speaks of a repository by its identity and
// never by its name: with every request by name answered by a twin (a
// repository that took the name, the same numbers and commits, another
// identity, merged and green), every entry point answers what the target
// repository holds, the issue's and the pull request's caches fresh,
// stale or mixed, and no request names the repository by its owner and
// name but the lookup itself.
func TestTwinNeverAnswersForTheRepository(t *testing.T) {
	r := newRig(t)
	r.answerAll()
	r.twin(t)
	r.reply(http.MethodGet, "/user", 200, githubtest.Fixture("user.json"))
	r.etagged(widgets+"/issues/43", `"i43"`, githubtest.Fixture("pull_issue.json"))
	r.etagged(widgets+"/pulls/43", `"p43"`, githubtest.Fixture("pull.json"))
	r.reply(http.MethodGet, widgets+"/issues/42", 200, githubtest.Fixture("issue.json"))
	r.reply(http.MethodGet, widgets+"/issues/42/comments", 200, []map[string]any{})
	r.reply(http.MethodGet, statusPath, 200, statuses(1, 1, true))
	r.reply(http.MethodGet, runsPath, 200, githubtest.Fixture("actions_runs.json"))
	r.reply(http.MethodGet, jobsPath, 200, githubtest.Fixture("actions_jobs.json"))
	r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 0, 0, 0, plain))
	blob := "productionresultssa1.blob.core.windows.net"
	r.reply(http.MethodGet, widgets+"/actions/jobs/2000001/logs", http.StatusFound, "", "Location", "https://"+blob+"/true/job-logs.txt?sig=s")
	var asked []string
	r.fake.Handle(http.MethodGet, blob, "/true/job-logs.txt", azureBlob("the target's log\n", &asked))
	img := "private-user-images.githubusercontent.com"
	r.fake.Reply(http.MethodGet, img, "/1/x.png", 200, "png", "Content-Type", "image/png")

	if acct, err := r.a.Whoami(ctx, r.cred); err != nil || acct.ID != "9000001" {
		t.Errorf("Whoami: %+v %v", acct, err)
	}
	if tg, err := r.a.Resolve(ctx, r.cred, target.NodeID); err != nil || tg.SpaceID != "4000001" {
		t.Errorf("Resolve: %+v %v", tg, err)
	}
	if rc, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(mustURL(t, "https://"+img+"/1/x.png?jwt=J"), ""), 1<<20); err != nil {
		t.Errorf("Image: %v", err)
	} else {
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "png" {
			t.Errorf("Image: %q", b)
		}
	}
	// The pull request: no cache, the issue's fresh beside the pull's
	// changed (the mixed path: one 304, one 200), the pull's fresh beside
	// the issue's changed, and both fresh.
	for _, v := range []tracker.Validators{
		{},
		{Issue: `"i43"`, NodeID: "PR_kwDOFixture43"},
		{Pull: `"p43"`, NodeID: "PR_kwDOFixture43"},
		{Issue: `"i43"`, Pull: `"stale"`},
		{Issue: `"stale"`, Pull: `"p43"`},
	} {
		it, notMod, err := r.a.Read(ctx, r.cred, pr43, v)
		if err != nil || notMod || it.Merged || it.State != tracker.StateOpen || it.NodeID != "PR_kwDOFixture43" || it.HeadSHA != head || it.MergeSHA != "" {
			t.Errorf("Read %+v: %+v %v %v", v, it, notMod, err)
		}
	}
	if _, notMod, err := r.a.Read(ctx, r.cred, pr43, tracker.Validators{Issue: `"i43"`, Pull: `"p43"`}); err != nil || !notMod {
		t.Errorf("Read, both fresh: %v %v", notMod, err)
	}
	if it, _, err := r.a.Read(ctx, r.cred, issue42, tracker.Validators{}); err != nil || it.NodeID != "I_kwDOFixture42" {
		t.Errorf("Read #42: %+v %v", it, err)
	}
	if set, err := r.a.Checks(ctx, r.cred, pr43, head); err != nil || set.Passed() || !strings.Contains(names(set), "s001") || strings.Contains(names(set), "ci/build") {
		t.Errorf("Checks: %s %+v %v", names(set), set, err)
	}
	if b, err := r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100); err != nil || string(b.Bytes) != "the target's log\n" {
		t.Errorf("CheckLog: %q %v", b.Bytes, err)
	}
	if _, err := r.a.Comment(ctx, r.commentCred("p1", 1), target, "hello"); err != nil {
		t.Errorf("Comment: %v", err)
	}
	m := tracker.Match{Marker: r.a.Marker("p1"), BodySha256: tracker.BodyDigest(twinPosted)}
	if got, found, err := r.a.FindComment(ctx, r.cred, target, now, m); err != nil || found != tracker.FoundNo {
		t.Errorf("FindComment: %+v %v %v", got, found, err)
	}
	if err := r.a.Close(ctx, r.closeCred("p1", 1), target, tracker.ReasonCompleted); err != nil {
		t.Errorf("Close: %v", err)
	}
	for _, q := range r.fake.Requests() {
		if strings.HasPrefix(q.Path, "/repos/") && q.Path != "/repos/octo-example/widgets" {
			t.Errorf("a request names the repository by its name: %s %s", q.Method, q.Path)
		}
	}
	r.authOnlyToAPI()
}

// A pull request's answer is combined with its issue's only when it names
// itself the same item: its base repository's id the one looked up and its
// number the one asked. One that names another, or none, is an answer
// Conductor cannot read, never merge state taken from it.
func TestPullAnswerTiedToTheRepository(t *testing.T) {
	for name, change := range map[string]func(p map[string]any){
		"another repository": func(p map[string]any) {
			p["base"].(map[string]any)["repo"] = repoAnswer(4999999, "octo-example/widgets")
		},
		"no repository":     func(p map[string]any) { delete(p["base"].(map[string]any), "repo") },
		"a null repository": func(p map[string]any) { p["base"].(map[string]any)["repo"] = nil },
		"another number":    func(p map[string]any) { p["number"] = 44 },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.reply(http.MethodGet, widgets+"/issues/43", 200, githubtest.Fixture("pull_issue.json"))
			p := fixture(t, "pull.json")
			p["merged"], p["merged_at"] = true, "2026-10-05T08:00:00Z"
			change(p)
			r.reply(http.MethodGet, widgets+"/pulls/43", 200, p)
			if it, _, err := r.a.Read(ctx, r.cred, pr43, tracker.Validators{}); err == nil || it.Merged {
				t.Fatalf("combined: %+v %v", it, err)
			}
		})
	}
}

// A pull request whose issue answers from another place (a redirect from
// the path asked by the repository's id) is not combined: a pull request
// is never transferred, so its pull would be another repository's. A
// renamed repository's pull request, whose lookup alone was redirected, is
// read.
func TestPullAnsweredFromAnotherPlaceRefused(t *testing.T) {
	r := newRig(t)
	r.reply(http.MethodGet, widgets+"/issues/43", http.StatusMovedPermanently, `{"message":"Moved Permanently"}`, "Location", "https://api.github.com/repositories/4999999/issues/43")
	r.reply(http.MethodGet, "/repositories/4999999/issues/43", 200, githubtest.Fixture("pull_issue.json"))
	r.reply(http.MethodGet, widgets+"/pulls/43", 200, githubtest.Fixture("pull.json"))
	if it, _, err := r.a.Read(ctx, r.cred, pr43, tracker.Validators{}); err == nil {
		t.Fatalf("combined: %+v", it)
	}
	r.reply(http.MethodGet, widgets+"/issues/43", 200, githubtest.Fixture("pull_issue.json"))
	renamed := ref("octo-example/old-widgets", tracker.KindChange, "43")
	if it, _, err := r.a.Read(ctx, r.cred, renamed, tracker.Validators{NodeID: "PR_kwDOFixture43"}); err != nil || it.Ref.Space != "octo-example/widgets" || it.HeadSHA != head {
		t.Fatalf("a renamed repository's pull request: %+v %v", it.Ref, err)
	}
}

// A lookup whose answer gives no repository id that reads as one asks
// nothing after it: no path is made by a name, or by an id not read.
func TestLookupWithoutAnIDAsksNothingMore(t *testing.T) {
	for _, answer := range []any{
		map[string]any{"full_name": "octo-example/widgets"},
		map[string]any{"id": "4000001/../../user"},
		map[string]any{"id": 0},
		map[string]any{"id": -4000001},
		map[string]any{"id": 4.5},
		`[]`,
	} {
		r := newRig(t)
		r.reply(http.MethodGet, "/repos/octo-example/widgets", 200, answer)
		_, _, err := r.a.Read(ctx, r.cred, pr43, tracker.Validators{})
		_, cerr := r.a.Checks(ctx, r.cred, pr43, head)
		_, lerr := r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100)
		if !errors.Is(err, tracker.ErrUnsupported) || !errors.Is(cerr, tracker.ErrUnsupported) || !errors.Is(lerr, tracker.ErrUnsupported) {
			t.Errorf("%v: %v / %v / %v", answer, err, cerr, lerr)
		}
		for _, q := range r.fake.Requests() {
			if q.Path != "/repos/octo-example/widgets" {
				t.Errorf("%v: asked %s", answer, q.Path)
			}
		}
	}
}
