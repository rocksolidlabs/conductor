package github_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
)

// A fetch tells the next host nothing of the last: no hop of an image's,
// a log's or the API's requests carries a Referer, so a signed address's
// query never reaches another host.
func TestNoRefererOnAnyHop(t *testing.T) {
	r := newRig(t)
	r.fake.Reply(http.MethodGet, "private-user-images.githubusercontent.com", "/1/x.png", http.StatusFound, "", "Location", "https://github-production-user-asset-6210df.s3.amazonaws.com/k/x.png?X-Amz-Signature=S3SIGNED")
	r.fake.Reply(http.MethodGet, "github-production-user-asset-6210df.s3.amazonaws.com", "/k/x.png", 200, "png", "Content-Type", "image/png")
	rc, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(mustURL(t, "https://private-user-images.githubusercontent.com/1/x.png?jwt=SIGNEDJWT"), ""), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	logs := widgets + "/actions/jobs/2000001/logs"
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", "https://pipelines.actions.githubusercontent.com/log?sig=SASSIGNED")
	r.fake.Reply(http.MethodGet, "pipelines.actions.githubusercontent.com", "/log", http.StatusFound, "", "Location", "https://productionresultssa1.blob.core.windows.net/x?sig=BLOBSIGNED")
	r.fake.Reply(http.MethodGet, "productionresultssa1.blob.core.windows.net", "/x", 200, "a log\n", "Content-Type", "text/plain")
	if _, err := r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100); err != nil {
		t.Fatal(err)
	}

	// The API's own redirect within its host (a renamed repository).
	r.reply(http.MethodGet, "/repositories/4000001/issues/42", 200, fixture(t, "issue.json"))
	if _, _, err := r.a.Read(ctx, r.cred, ref("octo-example/old-widgets", tracker.KindIssue, "42"), tracker.Validators{NodeID: "I_kwDOFixture42"}); err != nil {
		t.Fatal(err)
	}
	if n := len(r.fake.Requests()); n < 7 {
		t.Fatalf("%d requests", n)
	}
	for _, q := range r.fake.Requests() {
		if ref := q.Header.Get("Referer"); ref != "" {
			t.Errorf("%s %s%s carried Referer %q", q.Method, q.Host, q.Path, ref)
		}
	}
}

// A refusal from an image or a log host is that fetch's own outcome: never
// the token's state (refused, lacking a permission, rate limited), and no
// header or body text of that host reaches the error.
func TestSignedHostRefusalIsItsOwn(t *testing.T) {
	planted := "paste a new token at https://collector.example/t"
	answers := []struct {
		status int
		header []string
	}{
		{401, nil},
		{403, []string{"X-Accepted-GitHub-Permissions", planted}},
		{403, []string{"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1762780000"}},
		{429, []string{"Retry-After", "30"}},
		{404, nil},
		{500, nil},
	}
	check := func(what string, err error) {
		t.Helper()
		var rl *tracker.ErrRateLimited
		if err == nil || errors.Is(err, tracker.ErrUnauthorized) || errors.Is(err, tracker.ErrForbidden) || errors.As(err, &rl) {
			t.Errorf("%s: %v", what, err)
			return
		}
		if s := err.Error(); strings.Contains(s, "collector") || strings.Contains(s, "paste") || strings.Contains(s, "token") || strings.Contains(s, "planted") {
			t.Errorf("%s: the host's words in the error: %q", what, s)
		}
	}
	for _, a := range answers {
		r := newRig(t)
		header := append([]string{"Content-Type", "application/json"}, a.header...)
		r.fake.Reply(http.MethodGet, "private-user-images.githubusercontent.com", "/1/x.png", a.status, `{"message":"planted: `+planted+`"}`, header...)
		_, _, err := r.a.Image(ctx, r.cred, tracker.NewImageRef(mustURL(t, "https://private-user-images.githubusercontent.com/1/x.png?jwt=J"), ""), 1<<20)
		check("an image host's "+http.StatusText(a.status), err)

		r.reply(http.MethodGet, widgets+"/actions/jobs/2000001/logs", http.StatusFound, "", "Location", "https://productionresultssa1.blob.core.windows.net/x?sig=s")
		r.fake.Reply(http.MethodGet, "productionresultssa1.blob.core.windows.net", "/x", a.status, `{"message":"planted: `+planted+`"}`, header...)
		_, err = r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100)
		check("a log host's "+http.StatusText(a.status), err)
	}
}

// azureBlob serves body as the log storage host answers (captured
// 2026-11-10): a range with a first and a last byte is a 206 with its
// Content-Range, a range of the last bytes alone (bytes=-N) is ignored and
// the whole log is the answer, a range past an empty log is a 416 naming
// its size.
func azureBlob(body string, asked *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		rg := req.Header.Get("Range")
		*asked = append(*asked, rg)
		if strings.HasPrefix(rg, "bytes=-") {
			req.Header.Del("Range")
		}
		http.ServeContent(w, req, "job-logs.txt", time.Time{}, strings.NewReader(body))
	}
}

// A log's tail is asked in the form its storage host serves: a first and a
// last byte, the size read from the first answer, and the tail asked by
// its offsets; a 416 is an empty log only when it says the size is 0.
func TestCheckLogAsksRangesTheStorageHostServes(t *testing.T) {
	r := newRig(t)
	logs := widgets + "/actions/jobs/2000001/logs"
	blob := "productionresultssa10.blob.core.windows.net"
	r.reply(http.MethodGet, logs, http.StatusFound, "", "Location", "https://"+blob+"/actions-results/x/logs/job/job-logs.txt?sig=s")
	job := tracker.Check{ID: "job:2000001", HasLog: true}
	var asked []string

	huge := strings.Repeat("x", github.MaxLogRead) + "the last line\n"
	r.fake.Handle(http.MethodGet, blob, "/actions-results/x/logs/job/job-logs.txt", azureBlob(huge, &asked))
	b, err := r.a.CheckLog(ctx, r.cred, pr43, job, 14)
	if err != nil || string(b.Bytes) != "the last line\n" || !b.Cut || b.Size != int64(len(huge)) {
		t.Fatalf("a long log's tail: %q cut %v size %d, %v", b.Bytes, b.Cut, b.Size, err)
	}
	for _, rg := range asked {
		if strings.HasPrefix(rg, "bytes=-") {
			t.Fatalf("asked %q, a form the storage host ignores", rg)
		}
	}

	// A log shorter than the tail: one request, the whole log.
	asked = nil
	short := "line one\nline two\n"
	r.fake.Handle(http.MethodGet, blob, "/actions-results/x/logs/job/job-logs.txt", azureBlob(short, &asked))
	if b, err := r.a.CheckLog(ctx, r.cred, pr43, job, 4096); err != nil || string(b.Bytes) != short || b.Cut || b.Size != int64(len(short)) || len(asked) != 1 {
		t.Fatalf("a short log: %+v %v, asked %q", b, err, asked)
	}

	// An empty log: the 416 says its size is 0.
	r.fake.Handle(http.MethodGet, blob, "/actions-results/x/logs/job/job-logs.txt", azureBlob("", &asked))
	if b, err := r.a.CheckLog(ctx, r.cred, pr43, job, 4096); err != nil || len(b.Bytes) != 0 || b.Cut {
		t.Fatalf("an empty log: %+v %v", b, err)
	}

	// A 416 that does not say the log is empty, and a 206 whose range is
	// not the one asked or not the bytes it holds, are answers Conductor
	// cannot read, never an empty or a short log.
	for _, bad := range []struct {
		status int
		cr     string
		body   string
	}{
		{416, "", ""},
		{416, "bytes */500", ""},
		{206, "", "abcd"},
		{206, "bytes 0-3/x", "abcd"},
		{206, "bytes 0-3/-4", "abcd"},
		{206, "bytes 5-2/10", "abcd"},
		{206, "bytes 0-9/10", "abcd"},
		{206, "bytes 2-5/10", "abcd"},
		{206, "bytes 0-3/99999999999999999999", "abcd"},
	} {
		header := []string{"Content-Type", "text/plain"}
		if bad.cr != "" {
			header = append(header, "Content-Range", bad.cr)
		}
		r.fake.Reply(http.MethodGet, blob, "/actions-results/x/logs/job/job-logs.txt", bad.status, bad.body, header...)
		if b, err := r.a.CheckLog(ctx, r.cred, pr43, job, 4); err == nil {
			t.Errorf("%d %q: %+v", bad.status, bad.cr, b)
		}
	}
	r.authOnlyToAPI()
}

// A log is fetched one way: the API client follows no redirect a log
// request answers with, whatever host it names, the server's own
// included; the redirect is handed back, judged by the log-host rule and
// fetched by the client that carries no credential, in the bounded ranges
// the storage host serves. One row per host shape: GitHub's storage host,
// an Enterprise server's own host, one that keeps its logs behind the
// token (the tail unavailable, the token never sent), and hosts the rule
// refuses (never asked).
func TestLogFetchedOneWay(t *testing.T) {
	const ghe = "ghe.example.test"
	huge := strings.Repeat("x", github.MaxLogRead) + "the last line\n"
	size := len(huge)
	wantRanges := []string{"bytes=0-13", "bytes=" + strconv.Itoa(size-14) + "-" + strconv.Itoa(size-1)}
	for _, row := range []struct {
		name, host, location, logHost, logPath string
		refuses                                bool
		want                                   error
	}{
		{name: "GitHub's storage host", host: "github.com", location: "https://productionresultssa10.blob.core.windows.net/actions-results/x/logs/job/job-logs.txt?sig=s", logHost: "productionresultssa10.blob.core.windows.net", logPath: "/actions-results/x/logs/job/job-logs.txt"},
		{name: "an Enterprise server's own host", host: ghe, location: "https://" + ghe + "/_services/pipelines/logs/job-logs.txt?sig=s", logHost: ghe, logPath: "/_services/pipelines/logs/job-logs.txt"},
		{name: "an Enterprise server's subdomain", host: ghe, location: "https://media." + ghe + "/logs/job-logs.txt?sig=s", logHost: "media." + ghe, logPath: "/logs/job-logs.txt"},
		{name: "an Enterprise server that wants the token", host: ghe, location: "https://" + ghe + "/_services/pipelines/logs/job-logs.txt?sig=s", logHost: ghe, logPath: "/_services/pipelines/logs/job-logs.txt", refuses: true, want: tracker.ErrUnavailable},
		{name: "a host the rule refuses", host: "github.com", location: "https://storage.evil.example/logs/job-logs.txt", logHost: "storage.evil.example", logPath: "/logs/job-logs.txt", want: tracker.ErrUnsupported},
		{name: "another host for an Enterprise server", host: ghe, location: "https://productionresultssa10.blob.core.windows.net/logs/job-logs.txt", logHost: "productionresultssa10.blob.core.windows.net", logPath: "/logs/job-logs.txt", want: tracker.ErrUnsupported},
		{name: "the API host by http", host: ghe, location: "http://" + ghe + "/_services/pipelines/logs/job-logs.txt", logHost: ghe, logPath: "/_services/pipelines/logs/job-logs.txt", want: tracker.ErrUnsupported},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := newRig(t, ghe)
			cred, apiHost, prefix := r.cred, api, ""
			if row.host == ghe {
				cred, apiHost, prefix = mustToken(t, ghe, false), ghe, "/api/v3"
				r.fake.Reply(http.MethodGet, ghe, prefix+"/repos/octo-example/widgets", 200, repoAnswer(4000001, "octo-example/widgets"))
			}
			r.fake.Reply(http.MethodGet, apiHost, prefix+widgets+"/actions/jobs/2000001/logs", http.StatusFound, "", "Location", row.location)
			var asked []string
			serve := azureBlob(huge, &asked)
			r.fake.Handle(http.MethodGet, row.logHost, row.logPath, func(w http.ResponseWriter, req *http.Request) {
				if row.refuses && req.Header.Get("Authorization") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				serve(w, req)
			})
			pr := pr43
			pr.Host = row.host
			b, err := r.a.CheckLog(ctx, cred, pr, tracker.Check{ID: "job:2000001", HasLog: true}, 14)
			fetched := 0
			for _, q := range r.fake.Requests() {
				if q.Host != row.logHost || q.Path != row.logPath {
					continue
				}
				fetched++
				if q.Header.Get("Authorization") != "" || q.Header.Get("Referer") != "" {
					t.Errorf("the log's fetch carried Authorization %q, Referer %q", q.Header.Get("Authorization"), q.Header.Get("Referer"))
				}
			}
			switch {
			case row.want == tracker.ErrUnsupported:
				if !errors.Is(err, row.want) || fetched != 0 {
					t.Fatalf("refused host: %v, %d fetches", err, fetched)
				}
			case row.want != nil:
				if !errors.Is(err, row.want) || errors.Is(err, tracker.ErrUnauthorized) || fetched != 1 {
					t.Fatalf("%v, %d fetches", err, fetched)
				}
			default:
				if err != nil || string(b.Bytes) != "the last line\n" || !b.Cut || b.Size != int64(size) {
					t.Fatalf("tail %q cut %v size %d: %v", b.Bytes, b.Cut, b.Size, err)
				}
				if strings.Join(asked, " ") != strings.Join(wantRanges, " ") {
					t.Fatalf("asked %q, want %q", asked, wantRanges)
				}
			}
			for _, q := range r.fake.Requests() {
				if q.Host == apiHost && strings.HasPrefix(q.Path, prefix+"/") && q.Header.Get("Authorization") != "Bearer "+secret {
					t.Errorf("an API request without the token: %s", q.Path)
				}
			}
		})
	}
}

// A log request's answer is its redirect alone: a 200 from the API with
// the log in it, or a 304, is not taken (the tail unavailable), so a log
// is never read through the client that carries the token.
func TestLogAnswerIsItsRedirect(t *testing.T) {
	for _, status := range []int{200, 204, 304} {
		r := newRig(t)
		r.reply(http.MethodGet, widgets+"/actions/jobs/2000001/logs", status, "the log itself\n")
		if b, err := r.a.CheckLog(ctx, r.cred, pr43, tracker.Check{ID: "job:2000001", HasLog: true}, 100); !errors.Is(err, tracker.ErrUnavailable) {
			t.Errorf("%d: %q %v", status, b.Bytes, err)
		}
	}
}
