package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

type statusesJSON struct {
	TotalCount int `json:"total_count"`
	Statuses   []struct {
		ID        json.Number `json:"id"`
		Context   string      `json:"context"`
		State     string      `json:"state"`
		TargetURL *string     `json:"target_url"`
		CreatedAt time.Time   `json:"created_at"`
		UpdatedAt time.Time   `json:"updated_at"`
	} `json:"statuses"`
}

type runsJSON struct {
	TotalCount   int `json:"total_count"`
	WorkflowRuns []struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	} `json:"workflow_runs"`
}

// checkJSON is a job's or a check run's answer: the two share a shape.
type checkJSON struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	Status      string      `json:"status"`
	Conclusion  *string     `json:"conclusion"`
	HTMLURL     *string     `json:"html_url"`
	DetailsURL  *string     `json:"details_url"`
	StartedAt   *time.Time  `json:"started_at"`
	CompletedAt *time.Time  `json:"completed_at"`
	HeadSHA     string      `json:"head_sha"`
	App         *struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type jobsJSON struct {
	TotalCount int         `json:"total_count"`
	Jobs       []checkJSON `json:"jobs"`
}

type checkRunsJSON struct {
	TotalCount int         `json:"total_count"`
	CheckRuns  []checkJSON `json:"check_runs"`
}

// actionsApp is the GitHub Actions app's slug: its check runs are the
// jobs read from the Actions API.
const actionsApp = "github-actions"

// validSHA reports a commit's id: 40 or 64 lower-case hex digits.
func validSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}

// Checks reads a change's checks at a commit: the combined statuses (as
// checks without logs), the GitHub Actions jobs (at most MaxRuns runs and
// MaxJobs jobs a run), and the check runs of other apps (a fine-grained
// token's 403 there is an Incomplete entry, not an error), each job once.
// No bound cuts silently: each listing's own count is compared with what
// was read, and each cut, a next page named, and the merged set past
// tracker.MaxChecks or MaxCheckSetEncoded add an Incomplete entry, with
// Omitted counting what GitHub's totals say was left. What a cut keeps is
// fixed: failed first, then pending, then the rest, each by name and id.
func (a *Adapter) Checks(ctx context.Context, c tracker.Credential, r tracker.Ref, sha string) (tracker.CheckSet, error) {
	if r.System != tracker.GitHub || !validSHA(sha) {
		return tracker.CheckSet{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "a commit is 40 or 64 hex digits"}
	}
	ep, err := a.endpointFor(c, r.Host)
	if err != nil {
		return tracker.CheckSet{}, err
	}
	base := "/repos/" + r.Space
	var set tracker.CheckSet
	var all []tracker.Check

	// The combined statuses.
	var st statusesJSON
	ans, err := a.page(ctx, c, ep, base+"/commits/"+sha+"/status", url.Values{"per_page": {strconv.Itoa(MaxStatuses)}}, &st)
	switch {
	case forbidden(err):
		set.AddIncomplete("commit statuses could not be read: the token lacks Commit statuses read")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		for _, s := range st.Statuses {
			ch := tracker.Check{ID: "status:" + s.ID.String(), Name: cut(s.Context, 200), Status: "completed", URL: link(s.TargetURL), StartedAt: s.CreatedAt, HeadSHA: sha, App: "status"}
			switch s.State {
			case "pending":
				ch.Status = "pending"
			case "success", "failure", "error":
				ch.Conclusion = s.State
				ch.CompletedAt = s.UpdatedAt
			default:
				ch.Conclusion = cut(s.State, 32)
			}
			all = append(all, ch)
		}
		cutSaid(&set, st.TotalCount, len(st.Statuses), ans, "statuses at this commit")
	}

	// The GitHub Actions jobs.
	jobs := map[string]bool{}
	actionsRead := false
	var runs runsJSON
	ans, err = a.page(ctx, c, ep, base+"/actions/runs", url.Values{"head_sha": {sha}, "per_page": {strconv.Itoa(MaxRuns)}}, &runs)
	switch {
	case forbidden(err):
		set.AddIncomplete("GitHub Actions jobs could not be read: the token lacks Actions read")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		actionsRead = true
		// The runs left are not counted in Omitted: their jobs are not
		// known, only that they are there.
		switch {
		case runs.TotalCount > len(runs.WorkflowRuns):
			set.AddIncomplete(fmt.Sprintf("%d workflow runs at this commit; the first %d read", runs.TotalCount, len(runs.WorkflowRuns)))
		case hasNext(ans):
			set.AddIncomplete(fmt.Sprintf("more workflow runs at this commit than the first %d read", len(runs.WorkflowRuns)))
		}
		for _, run := range runs.WorkflowRuns {
			var js jobsJSON
			ans, err := a.page(ctx, c, ep, base+"/actions/runs/"+run.ID.String()+"/jobs", url.Values{"per_page": {strconv.Itoa(MaxJobs)}}, &js)
			if err != nil {
				return tracker.CheckSet{}, err
			}
			for _, j := range js.Jobs {
				ch := toCheck("job:", j, sha)
				ch.App, ch.HasLog = actionsApp, true
				jobs[j.ID.String()] = true
				all = append(all, ch)
			}
			cutSaid(&set, js.TotalCount, len(js.Jobs), ans, "jobs in workflow run "+cut(run.Name, 40))
		}
	}

	// Other apps' check runs.
	var crs checkRunsJSON
	ans, err = a.page(ctx, c, ep, base+"/commits/"+sha+"/check-runs", url.Values{"per_page": {strconv.Itoa(MaxCheckRuns)}}, &crs)
	switch {
	case forbidden(err):
		set.AddIncomplete("other apps' check runs need a classic token")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		for _, cr := range crs.CheckRuns {
			slug := ""
			if cr.App != nil {
				slug = cr.App.Slug
			}
			if slug == actionsApp && actionsRead && jobs[cr.ID.String()] {
				continue
			}
			ch := toCheck("run:", cr, sha)
			ch.App = cut(slug, 100)
			all = append(all, ch)
		}
		cutSaid(&set, crs.TotalCount, len(crs.CheckRuns), ans, "check runs at this commit")
	}

	slices.SortStableFunc(all, func(x, y tracker.Check) int {
		return cmp.Or(cmp.Compare(rank(x), rank(y)), strings.Compare(x.Name, y.Name), strings.Compare(x.ID, y.ID))
	})
	if len(all) > tracker.MaxChecks {
		set.AddIncomplete(fmt.Sprintf("%d checks at this commit; the first %d kept, failures first", len(all), tracker.MaxChecks))
		set.Omitted += len(all) - tracker.MaxChecks
		all = all[:tracker.MaxChecks]
	}
	dropped := 0
	for len(all) > 0 {
		b, _ := json.Marshal(all)
		if len(b) <= MaxCheckSetEncoded {
			break
		}
		all = all[:len(all)-1]
		dropped++
	}
	if dropped > 0 {
		set.AddIncomplete(fmt.Sprintf("the checks' list is past %d KiB; %d left out, failures kept first", MaxCheckSetEncoded>>10, dropped))
		set.Omitted += dropped
	}
	set.Checks = all
	if set.Checks == nil {
		set.Checks = []tracker.Check{}
	}
	return set, nil
}

// rank orders a cut's keeping: failed, then pending, then the rest.
func rank(c tracker.Check) int {
	switch {
	case c.Failed():
		return 0
	case c.Pending():
		return 1
	}
	return 2
}

func toCheck(prefix string, j checkJSON, sha string) tracker.Check {
	ch := tracker.Check{ID: prefix + j.ID.String(), Name: cut(j.Name, 200), Status: cut(j.Status, 32), URL: link(j.HTMLURL), HeadSHA: j.HeadSHA}
	if ch.URL == "" {
		ch.URL = link(j.DetailsURL)
	}
	if j.Conclusion != nil {
		ch.Conclusion = cut(*j.Conclusion, 32)
	}
	if j.StartedAt != nil {
		ch.StartedAt = *j.StartedAt
	}
	if j.CompletedAt != nil {
		ch.CompletedAt = *j.CompletedAt
	}
	if !validSHA(ch.HeadSHA) {
		ch.HeadSHA = sha
	}
	return ch
}

// link is an https URL GitHub gave, without its query, or ”.
func link(s *string) string {
	if s == nil {
		return ""
	}
	u, err := url.Parse(*s)
	if err != nil || u.Scheme != "https" || u.User != nil || len(*s) > 2048 {
		return ""
	}
	u.RawQuery, u.ForceQuery = "", false
	return u.String()
}

// cutSaid adds the Incomplete entry of a listing whose total is past what
// was read, or which names a next page.
func cutSaid(set *tracker.CheckSet, total, read int, ans answer, what string) {
	if total > read {
		set.AddIncomplete(fmt.Sprintf("%d %s; the first %d read", total, what, read))
		set.Omitted += total - read
		return
	}
	if hasNext(ans) {
		set.AddIncomplete(fmt.Sprintf("more %s than the first %d read", what, read))
	}
}

// hasNext reports an answer whose Link header names a next page.
func hasNext(ans answer) bool {
	return strings.Contains(ans.header.Get("Link"), `rel="next"`)
}

// forbidden reports a refusal for a permission (not a rate limit).
func forbidden(err error) bool {
	var rl *tracker.ErrRateLimited
	return errors.Is(err, tracker.ErrForbidden) && !errors.As(err, &rl)
}

// page reads one page of a listing into v.
func (a *Adapter) page(ctx context.Context, c tracker.Credential, ep endpoint, path string, q url.Values, v any) (answer, error) {
	r := request{method: http.MethodGet, path: path, query: q, max: MaxPageAnswer}
	ans, err := a.do(ctx, c, ep, r)
	if err != nil {
		return ans, err
	}
	return ans, decode(ep, r, ans.body, v)
}

// CheckLog reads a GitHub Actions job's log, its last tail bytes: GET
// …/actions/jobs/{id}/logs by the API client, whose redirect is handed
// back, its Location admitted by tracker.LogHost and fetched by the
// signed-address client with no credential, at most MaxLogRead bytes. A
// check of another app has no log (tracker.ErrUnsupported), nor has a log
// kept on a host Conductor does not fetch from.
func (a *Adapter) CheckLog(ctx context.Context, c tracker.Credential, r tracker.Ref, check tracker.Check, tail int) ([]byte, error) {
	id, ok := strings.CutPrefix(check.ID, "job:")
	if _, err := strconv.ParseUint(id, 10, 64); !ok || err != nil || !check.HasLog {
		return nil, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "only a GitHub Actions job has a log"}
	}
	if tail <= 0 || tail > MaxLogRead {
		return nil, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "a log's tail is 1 byte to 8 MiB"}
	}
	ep, err := a.endpointFor(c, r.Host)
	if err != nil {
		return nil, err
	}
	req := request{method: http.MethodGet, path: "/repos/" + r.Space + "/actions/jobs/" + id + "/logs", max: MaxLogRead, keep3xx: true}
	ans, err := a.do(ctx, c, ep, req)
	if err != nil {
		return nil, err
	}
	data := ans.body
	if ans.status >= 300 {
		loc, err := url.Parse(ans.header.Get("Location"))
		admit := func(u *url.URL) bool { return tracker.LogHost(tracker.GitHub, ep.host, u) }
		if err != nil || !loc.IsAbs() || !admit(loc) {
			return nil, &tracker.RequestError{System: tracker.GitHub, Method: req.method, Path: ep.errPath(req), Status: ans.status, Err: tracker.ErrUnsupported, Words: "the log is kept where Conductor does not fetch from: open it on GitHub"}
		}
		if data, err = a.fetchSigned(ctx, loc, admit, MaxLogRead); err != nil {
			return nil, err
		}
	}
	if len(data) > tail {
		data = data[len(data)-tail:]
	}
	return data, nil
}

// fetchSigned GETs a signed address with no credential, at most max bytes.
func (a *Adapter) fetchSigned(ctx context.Context, u *url.URL, admit func(*url.URL) bool, max int64) ([]byte, error) {
	path := u.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Err: tracker.ErrUnsupported}
	}
	req.Header.Set("User-Agent", a.ua)
	resp, err := a.signedClient(ImageTimeout, admit).Do(req)
	if err != nil {
		return nil, a.transportError(ctx, http.MethodGet, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxErrorAnswer))
		if resp.StatusCode < 400 {
			return nil, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported}
		}
		return nil, a.statusError(http.MethodGet, path, resp)
	}
	data, err := readBounded(resp.Body, max)
	if errors.Is(err, tracker.ErrBound) {
		return nil, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrBound, Words: "past " + strconv.FormatInt(max, 10) + " bytes"}
	}
	if err != nil {
		return nil, a.transportError(ctx, http.MethodGet, path, err)
	}
	return data, nil
}
