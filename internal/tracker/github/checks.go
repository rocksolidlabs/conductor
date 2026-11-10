package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// CheckRunURL is a job's check run (…/check-runs/{id}): the Actions
	// jobs' documented link to the check run each is.
	CheckRunURL string `json:"check_run_url"`
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
// No bound cuts silently: each listing's own count (held to 0 to MaxCount;
// outside, not known) is compared with what was read, and each cut, a next
// page named, and the merged set past tracker.MaxChecks or
// MaxCheckSetEncoded add an Incomplete entry, with Omitted counting what
// GitHub's totals say was left. What a cut keeps is fixed: failed first,
// then pending, then the rest, each by name and id. What it asks is
// bounded as well: each listing is cut to its own bound as soon as it is
// read, whatever an answer holds, so it asks the jobs of at most MaxRuns
// runs (at most MaxRuns+3 requests in all), and a cut is said. The whole
// call has its own deadline, ChecksTimeout: when it passes (or the
// caller's context ends) while a listing is read, the listings read before
// it are the answer, with an Incomplete entry saying what was not read, so
// the set never passes. r is held to tracker.Ref.Check, and a run's id to
// its digits, before any of it goes into a path.
func (a *Adapter) Checks(ctx context.Context, c tracker.Credential, r tracker.Ref, sha string) (tracker.CheckSet, error) {
	if r.Check() != nil || !validSHA(sha) {
		return tracker.CheckSet{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "a GitHub item and a commit of 40 or 64 hex digits"}
	}
	ep, err := a.endpointFor(c, r.Host)
	if err != nil {
		return tracker.CheckSet{}, err
	}
	cctx, done := a.deadline(ctx, ChecksTimeout)
	defer done()
	base := "/repos/" + r.Space
	var set tracker.CheckSet
	var all []tracker.Check
	// ranOut is a listing's error when it is the call's time that ended:
	// what was read is kept, and what was not is said.
	ranOut := func(err error, left string) bool {
		if err == nil || cctx.Err() == nil {
			return false
		}
		set.AddIncomplete("the read's time ran out: " + left)
		return true
	}

	// The combined statuses.
	var st statusesJSON
	ans, err := a.page(cctx, c, ep, base+"/commits/"+sha+"/status", url.Values{"per_page": {strconv.Itoa(MaxStatuses)}}, &st)
	switch {
	case ranOut(err, "the statuses, the GitHub Actions jobs and the check runs not read"):
		return finish(set, all), nil
	case forbidden(err):
		set.AddIncomplete("commit statuses could not be read: the token lacks Commit statuses read")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		listed := len(st.Statuses)
		st.Statuses = st.Statuses[:min(len(st.Statuses), MaxStatuses)]
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
		cutSaid(&set, st.TotalCount, listed, len(st.Statuses), ans, "statuses at this commit")
	}

	// The GitHub Actions jobs.
	jobs := map[string]bool{}
	actionsRead := false
	var runs runsJSON
	ans, err = a.page(cctx, c, ep, base+"/actions/runs", url.Values{"head_sha": {sha}, "per_page": {strconv.Itoa(MaxRuns)}}, &runs)
	switch {
	case ranOut(err, "the GitHub Actions jobs and the check runs not read"):
		return finish(set, all), nil
	case forbidden(err):
		set.AddIncomplete("GitHub Actions jobs could not be read: the token lacks Actions read")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		actionsRead = true
		// The runs left are not counted in Omitted: their jobs are not
		// known, only that they are there.
		listed := len(runs.WorkflowRuns)
		runs.WorkflowRuns = runs.WorkflowRuns[:min(len(runs.WorkflowRuns), MaxRuns)]
		total, known := count(runs.TotalCount, listed)
		switch {
		case known && total > len(runs.WorkflowRuns):
			set.AddIncomplete(fmt.Sprintf("%d workflow runs at this commit; the first %d read", total, len(runs.WorkflowRuns)))
		case listed > len(runs.WorkflowRuns) || hasNext(ans):
			set.AddIncomplete(fmt.Sprintf("more workflow runs at this commit than the first %d read", len(runs.WorkflowRuns)))
		}
		for i, run := range runs.WorkflowRuns {
			id := remoteID(run.ID)
			if id == "" {
				return tracker.CheckSet{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: ep.errPath(request{path: base + "/actions/runs"}), Status: http.StatusOK, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read"}
			}
			var js jobsJSON
			ans, err := a.page(cctx, c, ep, base+"/actions/runs/"+string(id)+"/jobs", url.Values{"per_page": {strconv.Itoa(MaxJobs)}}, &js)
			if ranOut(err, fmt.Sprintf("%d of %d workflow runs' jobs read, the check runs not read", i, len(runs.WorkflowRuns))) {
				return finish(set, all), nil
			}
			if err != nil {
				return tracker.CheckSet{}, err
			}
			jlisted := len(js.Jobs)
			js.Jobs = js.Jobs[:min(len(js.Jobs), MaxJobs)]
			for _, j := range js.Jobs {
				ch := toCheck("job:", j, sha)
				ch.App, ch.HasLog = actionsApp, true
				if cr := checkRunOf(j.CheckRunURL); cr != "" {
					jobs[cr] = true
				}
				all = append(all, ch)
			}
			cutSaid(&set, js.TotalCount, jlisted, len(js.Jobs), ans, "jobs in workflow run "+cut(run.Name, 40))
		}
	}

	// Other apps' check runs.
	var crs checkRunsJSON
	ans, err = a.page(cctx, c, ep, base+"/commits/"+sha+"/check-runs", url.Values{"per_page": {strconv.Itoa(MaxCheckRuns)}}, &crs)
	switch {
	case ranOut(err, "the check runs not read"):
		return finish(set, all), nil
	case forbidden(err):
		set.AddIncomplete("other apps' check runs need a classic token")
	case err != nil:
		return tracker.CheckSet{}, err
	default:
		listed := len(crs.CheckRuns)
		crs.CheckRuns = crs.CheckRuns[:min(len(crs.CheckRuns), MaxCheckRuns)]
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
		cutSaid(&set, crs.TotalCount, listed, len(crs.CheckRuns), ans, "check runs at this commit")
	}
	return finish(set, all), nil
}

// finish orders and cuts a check set's checks (failed first, then pending,
// then the rest; tracker.MaxChecks, MaxCheckSetEncoded), each cut said.
func finish(set tracker.CheckSet, all []tracker.Check) tracker.CheckSet {
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
	return set
}

// checkRunOf is the id of the check run a job's check_run_url names (its
// last path segment, digits), or ”.
func checkRunOf(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	seg := u.Path[strings.LastIndexByte(u.Path, '/')+1:]
	if !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"+seg), "/check-runs") {
		return ""
	}
	return string(decimal(seg))
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

// count is a listing's total_count held to its range: the larger of it and
// the items listed when it is 0 to MaxCount, and not known otherwise.
func count(total, listed int) (int, bool) {
	if total < 0 || total > MaxCount {
		return 0, false
	}
	return max(total, listed), true
}

// cutSaid adds the Incomplete entry of a listing cut from listed items to
// read, whose total (its total_count) is past what was read, or which
// names a next page; a total not known says only that there were more.
func cutSaid(set *tracker.CheckSet, totalCount, listed, read int, ans answer, what string) {
	if total, known := count(totalCount, listed); known && total > read {
		set.AddIncomplete(fmt.Sprintf("%d %s; the first %d read", total, what, read))
		set.Omitted += total - read
		return
	}
	if listed > read || hasNext(ans) {
		set.AddIncomplete(fmt.Sprintf("more %s than the first %d read", what, read))
		set.Omitted += listed - read
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
// signed-address client with no credential, asking only the last tail
// bytes in the form its storage host serves (fetchLog). A log longer than
// what is given says so (LogTail.Cut). A check of another app has no log
// (tracker.ErrUnsupported), nor has a log kept on a host Conductor does
// not fetch from.
func (a *Adapter) CheckLog(ctx context.Context, c tracker.Credential, r tracker.Ref, check tracker.Check, tail int) (tracker.LogTail, error) {
	id, ok := strings.CutPrefix(check.ID, "job:")
	if !ok || decimal(id) == "" || !check.HasLog || r.Check() != nil {
		return tracker.LogTail{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "only a GitHub Actions job has a log"}
	}
	if tail <= 0 || tail > MaxLogRead {
		return tracker.LogTail{}, &tracker.RequestError{System: tracker.GitHub, Method: "-", Path: "-", Err: tracker.ErrUnsupported, Words: "a log's tail is 1 byte to 8 MiB"}
	}
	ep, err := a.endpointFor(c, r.Host)
	if err != nil {
		return tracker.LogTail{}, err
	}
	req := request{method: http.MethodGet, path: "/repos/" + r.Space + "/actions/jobs/" + id + "/logs", max: MaxLogRead, keep3xx: true}
	ans, err := a.do(ctx, c, ep, req)
	if err != nil {
		return tracker.LogTail{}, err
	}
	if ans.status < 300 {
		return tailOf(ans.body, tail, int64(len(ans.body))), nil
	}
	loc, err := url.Parse(ans.header.Get("Location"))
	admit := func(u *url.URL) bool { return tracker.LogHost(tracker.GitHub, ep.host, u) }
	if err != nil || !loc.IsAbs() || !admit(loc) {
		return tracker.LogTail{}, &tracker.RequestError{System: tracker.GitHub, Method: req.method, Path: ep.errPath(req), Status: ans.status, Err: tracker.ErrUnsupported, Words: "the log is kept where Conductor does not fetch from: open it on GitHub"}
	}
	return a.fetchLog(ctx, loc, admit, tail)
}

// tailOf is the last tail bytes of a log of size bytes held whole in data.
func tailOf(data []byte, tail int, size int64) tracker.LogTail {
	out := tracker.LogTail{Bytes: data, Size: size}
	if len(data) > tail {
		out.Bytes = data[len(data)-tail:]
	}
	out.Cut = int64(len(out.Bytes)) < size
	return out
}

// MaxLogSize is the largest size a log's Content-Range is taken to give;
// past it the answer is one Conductor cannot read.
const MaxLogSize = 1 << 40

// fetchLog GETs a signed log address with no credential, within
// ImageTimeout, asking its last tail bytes in the form GitHub's log
// storage serves (captured 2026-11-10: it answers a range with a first and
// a last byte with a 206, and ignores a range of the last bytes alone,
// answering the whole log): bytes 0 to tail-1 first, then, when the
// Content-Range says the log is longer, its last tail bytes by their
// offsets. A whole 200 is kept to its last tail bytes when it fits in
// MaxLogRead (tracker.ErrBound past it). A 416 is an empty log only when it
// says the log's size is 0. A 206 is taken only when its Content-Range,
// each number held to its range, is the range asked and the bytes it
// holds; any other is an answer Conductor cannot read.
func (a *Adapter) fetchLog(ctx context.Context, u *url.URL, admit func(*url.URL) bool, tail int) (tracker.LogTail, error) {
	rctx, done := a.deadline(ctx, ImageTimeout)
	defer done()
	client := a.signedClient(admit)
	part, err := a.logRange(ctx, rctx, client, u, 0, int64(tail)-1)
	switch {
	case err != nil:
		return tracker.LogTail{}, err
	case part.whole:
		return tailOf(part.data, tail, int64(len(part.data))), nil
	case part.size <= int64(tail):
		// The whole log (an empty one's 416 included).
		return tracker.LogTail{Bytes: part.data, Size: part.size}, nil
	}
	size := part.size
	last, err := a.logRange(ctx, rctx, client, part.at, size-int64(tail), size-1)
	switch {
	case err != nil:
		return tracker.LogTail{}, err
	case last.whole:
		return tailOf(last.data, tail, int64(len(last.data))), nil
	case last.size != size:
		return tracker.LogTail{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: u.Path, Status: http.StatusPartialContent, Err: tracker.ErrUnavailable, Words: "the log changed while it was read"}
	}
	return tracker.LogTail{Bytes: last.data, Size: size, Cut: true}, nil
}

// logPart is one answer of a log's storage host: the whole log (a 200), or
// the bytes of the range asked and the log's size (a 206, or a 416 naming
// size 0), and the address that answered.
type logPart struct {
	data  []byte
	whole bool
	size  int64
	at    *url.URL
}

// logRange asks u for bytes first to last.
func (a *Adapter) logRange(ctx, rctx context.Context, client *http.Client, u *url.URL, first, last int64) (logPart, error) {
	path := u.Path
	unreadable := func(status int) error {
		return &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: status, Err: tracker.ErrUnsupported, Words: "an answer Conductor cannot read"}
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil || req.URL.Host != u.Host {
		return logPart{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Err: tracker.ErrUnsupported}
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Range", "bytes="+strconv.FormatInt(first, 10)+"-"+strconv.FormatInt(last, 10))
	resp, err := client.Do(req)
	if err != nil {
		return logPart{}, a.transportError(ctx, rctx, http.MethodGet, path, err)
	}
	defer resp.Body.Close()
	at := resp.Request.URL
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		if resp.Header.Get("Content-Range") != "bytes */0" {
			return logPart{}, unreadable(resp.StatusCode)
		}
		return logPart{data: []byte{}, at: at}, nil
	default:
		if resp.StatusCode < 400 {
			return logPart{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrUnsupported}
		}
		return logPart{}, signedRefusal(path, resp.StatusCode, "log")
	}
	data, err := readBounded(resp.Body, MaxLogRead)
	if errors.Is(err, tracker.ErrBound) {
		return logPart{}, &tracker.RequestError{System: tracker.GitHub, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Err: tracker.ErrBound, Words: "past " + strconv.Itoa(MaxLogRead) + " bytes"}
	}
	if err != nil {
		return logPart{}, a.transportError(ctx, rctx, http.MethodGet, path, err)
	}
	if resp.StatusCode == http.StatusOK {
		return logPart{data: data, whole: true, at: at}, nil
	}
	// A 206: Content-Range bytes <first>-<last>/<size>, the range asked
	// (its last byte the log's last when the log is shorter).
	lo, hi, size, ok := contentRange(resp.Header.Get("Content-Range"))
	if !ok || lo != first || hi > last || (hi < last && hi != size-1) || int64(len(data)) != hi-lo+1 {
		return logPart{}, unreadable(resp.StatusCode)
	}
	return logPart{data: data, size: size, at: at}, nil
}

// contentRange reads "bytes <first>-<last>/<size>", each a decimal number
// held to its range: first <= last < size <= MaxLogSize.
func contentRange(s string) (first, last, size int64, ok bool) {
	rest, ok := strings.CutPrefix(s, "bytes ")
	if !ok {
		return 0, 0, 0, false
	}
	span, total, ok1 := strings.Cut(rest, "/")
	lo, hi, ok2 := strings.Cut(span, "-")
	if !ok1 || !ok2 {
		return 0, 0, 0, false
	}
	num := func(d string) (int64, bool) {
		if d == "" || len(d) > 13 {
			return 0, false
		}
		for i := 0; i < len(d); i++ {
			if d[i] < '0' || d[i] > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseInt(d, 10, 64)
		return n, err == nil && n <= MaxLogSize
	}
	f, okf := num(lo)
	l, okl := num(hi)
	z, okz := num(total)
	if !okf || !okl || !okz || f > l || l >= z {
		return 0, 0, 0, false
	}
	return f, l, z, true
}
