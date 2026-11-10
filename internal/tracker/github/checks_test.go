package github_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

const (
	statusPath = "/repos/octo-example/widgets/commits/" + head + "/status"
	runsPath   = "/repos/octo-example/widgets/actions/runs"
	jobsPath   = "/repos/octo-example/widgets/actions/runs/1000001/jobs"
	crPath     = "/repos/octo-example/widgets/commits/" + head + "/check-runs"
)

var pr43 = ref("octo-example/widgets", tracker.KindChange, "43")

func (r *rig) fixtureChecks() {
	r.reply(http.MethodGet, statusPath, 200, githubtest.Fixture("status.json"))
	r.reply(http.MethodGet, runsPath, 200, githubtest.Fixture("actions_runs.json"))
	r.reply(http.MethodGet, jobsPath, 200, githubtest.Fixture("actions_jobs.json"))
	r.reply(http.MethodGet, crPath, 200, githubtest.Fixture("check_runs.json"))
}

func names(s tracker.CheckSet) string {
	var out []string
	for _, c := range s.Checks {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestChecksAtCommit(t *testing.T) {
	r := newRig(t)
	r.fixtureChecks()
	set, err := r.a.Checks(ctx, r.cred, pr43, head)
	if err != nil {
		t.Fatal(err)
	}
	// Failures first, then pending, then the rest, each by name; the
	// Actions jobs' own check runs listed once.
	if got := names(set); got != "ci/lint,test,coverage,build,ci/build" {
		t.Fatalf("order %s", got)
	}
	if len(set.Incomplete) != 0 || set.Omitted != 0 || set.Passed() {
		t.Fatalf("set %+v", set)
	}
	byName := map[string]tracker.Check{}
	for _, c := range set.Checks {
		byName[c.Name] = c
	}
	if c := byName["test"]; c.ID != "job:2000001" || !c.HasLog || c.App != "github-actions" || c.Conclusion != "failure" || c.HeadSHA != head || c.URL != "https://github.com/octo-example/widgets/actions/runs/1000001/job/2000001" {
		t.Fatalf("job %+v", c)
	}
	if c := byName["coverage"]; c.ID != "run:2500001" || c.HasLog || c.App != "codecov-example" || c.Status != "in_progress" || !c.Pending() {
		t.Fatalf("check run %+v", c)
	}
	if c := byName["ci/build"]; c.ID != "status:3000001" || c.Conclusion != "success" || c.URL != "https://ci.example/builds/1" || c.HasLog {
		t.Fatalf("status %+v", c)
	}
	for _, q := range r.fake.Requests() {
		if q.Path == runsPath && q.RawQuery != "head_sha="+head+"&per_page=20" {
			t.Fatalf("runs asked as %s", q.RawQuery)
		}
	}

	// A fine-grained token's 403 at the Checks API is an Incomplete entry,
	// not an error; the jobs still come from the Actions API.
	r.reply(http.MethodGet, crPath, 403, `{"message":"Resource not accessible by personal access token"}`)
	set, err = r.a.Checks(ctx, r.cred, pr43, head)
	if err != nil || len(set.Incomplete) != 1 || set.Incomplete[0] != "other apps' check runs need a classic token" || names(set) != "ci/lint,test,build,ci/build" || set.Passed() {
		t.Fatalf("fine-grained: %+v %v", set, err)
	}
	// A rate limit there is an error, not an entry.
	r.reply(http.MethodGet, crPath, 403, "", "X-RateLimit-Remaining", "0")
	var rl *tracker.ErrRateLimited
	if _, err := r.a.Checks(ctx, r.cred, pr43, head); !errors.As(err, &rl) {
		t.Fatalf("rate limited: %v", err)
	}
	if _, err := r.a.Checks(ctx, r.cred, pr43, "not-a-sha"); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("a bad commit: %v", err)
	}
}

// statuses makes n combined statuses of a total, the last a failure when
// failLast.
func statuses(n, total int, failLast bool) map[string]any {
	var list []map[string]any
	for i := 1; i <= n; i++ {
		state := "success"
		if failLast && i == n {
			state = "failure"
		}
		list = append(list, map[string]any{"id": 3100000 + i, "context": fmt.Sprintf("s%03d", i), "state": state, "target_url": nil, "created_at": "2026-10-04T12:00:00Z", "updated_at": "2026-10-04T12:01:00Z"})
	}
	return map[string]any{"state": "success", "total_count": total, "statuses": list}
}

func checkRuns(key string, n, total int, failAt int, name func(int) string) map[string]any {
	var list []map[string]any
	for i := 1; i <= n; i++ {
		concl := "success"
		if i == failAt {
			concl = "failure"
		}
		list = append(list, map[string]any{"id": 2600000 + i, "name": name(i), "status": "completed", "conclusion": concl, "head_sha": head, "html_url": "https://github.com/octo-example/widgets/runs/" + fmt.Sprint(2600000+i), "app": map[string]any{"slug": "other-ci"}})
	}
	return map[string]any{"total_count": total, key: list}
}

func plain(i int) string { return fmt.Sprintf("c%03d", i) }

func TestChecksIncomplete(t *testing.T) {
	empty := func(r *rig) {
		r.reply(http.MethodGet, statusPath, 200, statuses(0, 0, false))
		r.reply(http.MethodGet, runsPath, 200, `{"total_count":0,"workflow_runs":[]}`)
		r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 0, 0, 0, plain))
	}
	cases := []struct {
		name  string
		setup func(r *rig)
		says  string
		// inSet: the failure past the bound is in the set (kept first)
		// rather than only said.
		inSet   bool
		omitted int
	}{
		{"the 21st run", func(r *rig) {
			var runs []map[string]any
			for i := 1; i <= 20; i++ {
				runs = append(runs, map[string]any{"id": 1000000 + i, "name": fmt.Sprintf("wf%02d", i)})
				r.reply(http.MethodGet, fmt.Sprintf("/repos/octo-example/widgets/actions/runs/%d/jobs", 1000000+i), 200, `{"total_count":0,"jobs":[]}`)
			}
			r.reply(http.MethodGet, runsPath, 200, map[string]any{"total_count": 21, "workflow_runs": runs})
		}, "21 workflow runs at this commit; the first 20 read", false, 0},
		{"a run's 101st job", func(r *rig) {
			r.reply(http.MethodGet, runsPath, 200, githubtest.Fixture("actions_runs.json"))
			r.reply(http.MethodGet, jobsPath, 200, checkRuns("jobs", 100, 101, 0, plain))
		}, "101 jobs in workflow run CI; the first 100 read", false, 1},
		{"the 101st check run", func(r *rig) {
			r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 100, 101, 0, plain))
		}, "101 check runs at this commit; the first 100 read", false, 1},
		{"the 101st status", func(r *rig) {
			r.reply(http.MethodGet, statusPath, 200, statuses(100, 101, false))
		}, "101 statuses at this commit; the first 100 read", false, 1},
		{"a next page named", func(r *rig) {
			r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 100, 100, 0, plain), "Link", `<https://api.github.com/x?page=2>; rel="next"`)
		}, "more check runs at this commit than the first 100 read", false, 0},
		{"the 101st check of the merged set", func(r *rig) {
			r.reply(http.MethodGet, statusPath, 200, statuses(51, 51, true))
			r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 50, 50, 0, plain))
		}, "101 checks at this commit; the first 100 kept, failures first", true, 1},
		{"a set past 32 KiB encoded", func(r *rig) {
			long := func(i int) string { return fmt.Sprintf("%03d-%s", i, strings.Repeat("n", 190)) }
			r.reply(http.MethodGet, crPath, 200, checkRuns("check_runs", 100, 100, 100, long))
		}, "the checks' list is past 32 KiB", true, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			empty(r)
			c.setup(r)
			set, err := r.a.Checks(ctx, r.cred, pr43, head)
			if err != nil {
				t.Fatal(err)
			}
			said := false
			for _, w := range set.Incomplete {
				if strings.HasPrefix(w, c.says) {
					said = true
				}
			}
			if !said || set.Passed() {
				t.Fatalf("not said: %q", set.Incomplete)
			}
			if c.inSet && (len(set.Checks) == 0 || !set.Checks[0].Failed()) {
				t.Fatalf("the failure was cut: %s", names(set))
			}
			if c.omitted >= 0 && set.Omitted != c.omitted {
				t.Fatalf("omitted %d", set.Omitted)
			}
			if c.omitted < 0 && set.Omitted < 1 {
				t.Fatalf("omitted %d", set.Omitted)
			}
			if len(set.Checks) > tracker.MaxChecks {
				t.Fatalf("%d checks", len(set.Checks))
			}
		})
	}
}

func TestCheckLogTail(t *testing.T) {
	r := newRig(t)
	logs := "/repos/octo-example/widgets/actions/jobs/2000001/logs"
	// The shape of github.com's answer: a 302 to the job's log on Azure
	// blob storage with a signed query.
	blob := "productionresultssa0.blob.core.windows.net"
	loc := "https://" + blob + "/actions-results/0d4c2e1a-1111-4a22-8b33-5c6d7e8f9a0b/workflow-job-run-2c3d4e5f-2222-4b33-9c44-6d7e8f9a0b1c/logs/job/job-logs.txt?rsct=text%2Fplain&se=2026-11-10T12%3A10%3A00Z&sig=REDACTED&sp=r&spr=https&sr=b&st=2026-11-10T12%3A00%3A00Z&sv=2025-05-05"
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", loc)
	full := "2026-10-04T12:00:41Z line one\n2026-10-04T12:07:59Z FAIL TestEmptyList\n"
	r.fake.Reply(http.MethodGet, blob, "/actions-results/0d4c2e1a-1111-4a22-8b33-5c6d7e8f9a0b/workflow-job-run-2c3d4e5f-2222-4b33-9c44-6d7e8f9a0b1c/logs/job/job-logs.txt", 200, full)
	job := tracker.Check{ID: "job:2000001", HasLog: true}
	b, err := r.a.CheckLog(ctx, r.cred, pr43, job, 18)
	if err != nil || string(b) != "AIL TestEmptyList\n" {
		t.Fatalf("tail %q %v", b, err)
	}
	if b, _ := r.a.CheckLog(ctx, r.cred, pr43, job, 1<<20); string(b) != full {
		t.Fatalf("a tail longer than the log: %q", b)
	}
	r.authOnlyToAPI()

	// A log kept on a host Conductor does not fetch from.
	before := len(r.fake.Requests())
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", "https://storage.evil.example/log.txt")
	if _, err := r.a.CheckLog(ctx, r.cred, pr43, job, 100); !errors.Is(err, tracker.ErrUnsupported) || !strings.Contains(err.Error(), "open it on GitHub") {
		t.Fatalf("another host: %v", err)
	}
	if n := len(r.fake.Requests()) - before; n != 1 {
		t.Fatalf("%d requests", n)
	}
	// A check of another app, or a status, has no log: no request.
	for _, c := range []tracker.Check{{ID: "run:2500001"}, {ID: "status:3000001"}, {ID: "job:x", HasLog: true}, {ID: "job:1"}} {
		if _, err := r.a.CheckLog(ctx, r.cred, pr43, c, 100); !errors.Is(err, tracker.ErrUnsupported) {
			t.Fatalf("%s: %v", c.ID, err)
		}
	}
	if n := len(r.fake.Requests()) - before; n != 1 {
		t.Fatalf("%d requests", n)
	}
}
