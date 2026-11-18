package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// ghItem serves item 41 of octo/app on the fake's loopback host, its title
// holding a control sequence, and its repository's lookup.
func (g *githubFake) item41(title string) string {
	url := "http://" + g.host + "/octo/app/issues/41"
	g.Reply("GET", g.host, "/api/v3/repos/octo/app", http.StatusOK, map[string]any{"id": 4000001, "full_name": "octo/app"})
	g.Handle("GET", g.host, "/api/v3/repositories/4000001/issues/41", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+ghToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := json.Marshal(map[string]any{"id": 5000041, "node_id": "I_kwDOitem41", "number": 41, "html_url": url, "title": title,
			"body": "It breaks.\r\n\r\n### Steps to reproduce\r\n\r\n1. Start it\r\n", "body_html": "", "state": "open", "labels": []any{map[string]any{"name": "bug"}},
			"assignees": []any{}, "user": map[string]any{"login": "mona", "id": 9000001}, "created_at": "2026-11-01T09:00:00Z", "updated_at": "2026-11-01T10:00:00Z"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	return url
}

// conductor dispatch import reads a GitHub item with the token the server
// keeps and makes it a ticket: --preview writes nothing, the import
// prints the key and GitHub's title with its control sequence made
// harmless, a second import of the same item is refused, and refresh,
// authority, unlink-issue and link-issue each do what their route does.
// Usage errors are refused before any request.
func TestDispatchImportCommand(t *testing.T) {
	a, g := newTrackerAPI(t)
	a.admin(t, "POST", "/api/trackers/github", map[string]any{"host": g.host, "token": ghToken}, http.StatusOK)
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor"}, http.StatusCreated)
	item := g.item41("It breaks\x1b[2J on start")

	code, out, errOut, err := a.dispatchCmd(t, "", "import", item, "--project", "CND", "--preview")
	if code != 0 || err != nil || !strings.HasPrefix(out, "# It breaks") || strings.Contains(out, "\x1b") || !strings.Contains(out, "nothing was written") || !strings.Contains(out, "## Steps to reproduce\n\n1. Start it\n") {
		var seen []string
		for _, q := range g.Requests() {
			seen = append(seen, q.Method+" "+q.Host+q.Path)
		}
		t.Fatalf("the preview: %d %v\n%s%s\nGitHub was asked %q", code, err, out, errOut, seen)
	}
	if list := a.admin(t, "GET", "/api/dispatch/tickets?project=CND", nil, http.StatusOK)["tickets"].([]any); len(list) != 0 {
		t.Fatalf("the preview made %d tickets", len(list))
	}
	code, out, errOut, err = a.dispatchCmd(t, "", "import", item, "--project", "CND", "--no-images")
	tk := a.ticket(t, "CND-1")
	if code != 0 || err != nil || out != "CND-1 imported from "+item+": "+oneLine(tk.Title)+"\n" || strings.Contains(out, "\x1b") || !strings.HasPrefix(tk.Title, "It breaks") {
		t.Fatalf("the import: %d %v\n%q%s", code, err, out, errOut)
	}
	if tk.ExternalURL != item {
		t.Fatalf("the ticket's item: %q", tk.ExternalURL)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "import", item, "--project", "CND"); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "external_linked: ") {
		t.Fatalf("a second import: %d %v", code, err)
	}
	if out := a.mustDispatch(t, "refresh", "CND-1"); out != "CND-1 refreshed: nothing changed\n" {
		t.Fatalf("the refresh: %q", out)
	}
	if out := a.mustDispatch(t, "authority", "CND-1", "title", "dispatch"); !strings.Contains(out, "its title is Dispatch's now") {
		t.Fatalf("the authority: %q", out)
	}
	if au := a.ticket(t, "CND-1").Authority; strings.Contains(string(au), `"title":"github"`) || !strings.Contains(string(au), `"description":"github"`) {
		t.Fatalf("the ticket's authority: %s", au)
	}
	if out := a.mustDispatch(t, "unlink-issue", "CND-1"); !strings.Contains(out, "CND-1 unlinked") {
		t.Fatalf("the unlink: %q", out)
	}
	if tk := a.ticket(t, "CND-1"); tk.ExternalID != "" {
		t.Fatalf("the unlinked ticket keeps %q", tk.ExternalID)
	}
	if out := a.mustDispatch(t, "link-issue", "CND-1", item); out != "CND-1 linked to "+item+": every field stays Dispatch's\n" {
		t.Fatalf("the link: %q", out)
	}
	for _, args := range [][]string{{"import", item}, {"import", "--project", "CND"}, {"authority", "CND-1", "status", "github"}, {"authority", "CND-1", "title", "nobody"},
		{"link-issue", "CND-1"}, {"read", "CND-1"}, {"refresh", "nokey"}} {
		if code, _, _, err := a.dispatchCmd(t, "", args[0], args[1:]...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v", args, code, err)
		}
	}
}

// publicationStub is a Conductor server's publication routes as the
// protocol gives them: the offer, a prepare answering the exact bytes and
// their SHA-256, a send taking that digest alone, and the ticket's read
// with one delivery link. It keeps the bodies it was sent.
type publicationStub struct {
	mu       sync.Mutex
	prepared map[string]any
	sends    []string
	preview  string
}

func (p *publicationStub) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	answer := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/dispatch/tickets/CND-1/publish/offer":
		answer(http.StatusOK, map[string]any{"parts": []any{map[string]any{"name": "key", "markdown": "Conductor ticket CND-1: It breaks"}}, "targets": []any{}})
	case "GET /api/dispatch/tickets/CND-1":
		answer(http.StatusOK, map[string]any{"ticket": map[string]any{"key": "CND-1", "links": []any{map[string]any{"id": "aaaaaaaaaaaaaaaa", "kind": "delivery", "url": "https://github.com/octo/app/pull/3"}}}})
	case "POST /api/dispatch/tickets/CND-1/publications":
		var b map[string]any
		_ = decodeBody(r, &b)
		p.prepared = b
		p.preview = b["body"].(string) + "\n\n<!-- conductor-publication:0123456789abcdef -->"
		sum := sha256.Sum256([]byte(p.preview))
		answer(http.StatusCreated, map[string]any{"receipt": map[string]any{"id": "0123456789abcdef"}, "preview": p.preview, "postSha256": hex.EncodeToString(sum[:]),
			"publication": map[string]any{"id": "0123456789abcdef", "state": "draft", "targetUrl": "https://github.com/octo/app/issues/41", "closeIssue": b["closeIssue"]}})
	case "POST /api/dispatch/publications/0123456789abcdef/send":
		var b struct{ PostSha256 string }
		_ = decodeBody(r, &b)
		p.sends = append(p.sends, b.PostSha256)
		sum := sha256.Sum256([]byte(p.preview))
		if b.PostSha256 != hex.EncodeToString(sum[:]) {
			answer(http.StatusConflict, map[string]any{"error": map[string]any{"code": "stale_revision", "message": "the digest is not the preview's"}})
			return
		}
		answer(http.StatusOK, map[string]any{"publication": map[string]any{"id": "0123456789abcdef", "state": "sent", "remoteUrl": "https://github.com/octo/app/issues/41#issuecomment-7", "sentAsLogin": "mona"}})
	default:
		answer(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": r.URL.Path}})
	}
}

// conductor dispatch publish prepares and prints the exact text a send
// posts with its digest, posting nothing; publish send posts only with
// --digest, the digest the prepare printed is taken and another refused;
// a link named by its URL is sent as its id; the flags are checked before
// any request.
func TestDispatchPublishPreviewThenSend(t *testing.T) {
	clearConductorEnv(t)
	p := &publicationStub{}
	srv := handlerServer(t, p.serve)
	run := func(stdin string, args ...string) (int, string, error) {
		t.Helper()
		var out, errb strings.Builder
		code, err := Run(t.Context(), append(append([]string{"dispatch"}, args...), "--server", srv, "--token", dispatchToken), strings.NewReader(stdin), &out, &errb)
		return code, out.String() + errb.String(), err
	}
	code, out, err := run("It is fixed.\n", "publish", "CND-1", "--target", "linked", "--body-file", "-", "--with", "key", "--close", "completed")
	if code != 0 || err != nil {
		t.Fatalf("the prepare: %d %v\n%s", code, err, out)
	}
	if b := p.prepared; b["target"] != "linked" || b["body"] != "It is fixed.\n\nConductor ticket CND-1: It breaks" || b["closeIssue"] != "completed" || !slices.Equal(anyStrings(b["parts"]), []string{"key"}) {
		t.Fatalf("the prepare sent %v", b)
	}
	sum := sha256.Sum256([]byte(p.preview))
	digest := hex.EncodeToString(sum[:])
	if !strings.Contains(out, "nothing posted") || !strings.Contains(out, p.preview) || !strings.Contains(out, "conductor dispatch publish send 0123456789abcdef --digest "+digest) {
		t.Fatalf("the prepare printed:\n%s", out)
	}
	for _, args := range [][]string{{"publish", "send", "0123456789abcdef"}, {"publish", "send", "0123456789abcdef", "--digest", "abc"}, {"publish", "send", "0123456789abcdef", "--digest", strings.ToUpper(digest)}} {
		if code, _, err := run("", args...); code != 2 || err == nil {
			t.Fatalf("%q: exit %d %v", args, code, err)
		}
	}
	if len(p.sends) != 0 {
		t.Fatalf("a send without its digest was sent: %q", p.sends)
	}
	wrong := strings.Repeat("0", 64)
	if code, _, err := run("", "publish", "send", "0123456789abcdef", "--digest", wrong); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "stale_revision: ") {
		t.Fatalf("a wrong digest: %d %v", code, err)
	}
	code, out, err = run("", "publish", "send", "0123456789abcdef", "--digest", digest)
	if code != 0 || err != nil || !strings.Contains(out, "publication 0123456789abcdef: sent as https://github.com/octo/app/issues/41#issuecomment-7 by @mona") {
		t.Fatalf("the send: %d %v\n%s", code, err, out)
	}
	if !slices.Equal(p.sends, []string{wrong, digest}) {
		t.Fatalf("the sends: %q", p.sends)
	}
	if code, out, err := run("", "publish", "CND-1", "--target", "https://github.com/octo/app/pull/3", "--with", "key"); code != 0 || err != nil || p.prepared["target"] != "link:aaaaaaaaaaaaaaaa" {
		t.Fatalf("a link by its URL: %d %v %v\n%s", code, err, p.prepared, out)
	}
	if code, _, err := run("", "publish", "CND-1", "--target", "https://example.com/nothing", "--with", "key"); code != 1 || err == nil {
		t.Fatalf("a link the ticket has not: %d %v", code, err)
	}
	for _, args := range [][]string{{"publish", "CND-1", "--with", "key"}, {"publish", "CND-1", "--target", "linked"}, {"publish", "CND-1", "--target", "linked", "--with", "bogus"},
		{"publish", "CND-1", "--target", "linked", "--with", "key,key"}, {"publish", "CND-1", "--target", "linked", "--with", "key", "--close", "maybe"}, {"publish", "send", "nope", "--digest", digest}} {
		if code, _, err := run("", args...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v", args, code, err)
		}
	}
}

// anyStrings is a JSON array of strings as []string.
func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// Each command waits its route's own deadline and a request's time
// beside it, derived from the route's exported bound: as each request
// reaches the server, the command's remaining wait is longer than the
// whole deadline the server holds itself to, so a server that answers
// within it, a second before its end among others, is answered and not
// cut off. An edit to done waits the merged close's; any other edit a
// request's time alone.
func TestTrackerRequestTimeouts(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", dispatchToken)
	var mu sync.Mutex
	deadlines := map[string]time.Time{}
	waits := map[string]time.Duration{}
	old := requestSeam
	t.Cleanup(func() { requestSeam = old })
	requestSeam = func(method, path string, timeout time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		deadlines[method+" "+path] = time.Now().Add(timeout)
		waits[method+" "+path] = timeout
	}
	left := map[string]time.Duration{}
	srv := handlerServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		left[r.Method+" "+r.URL.Path] = time.Until(deadlines[r.Method+" "+r.URL.Path])
		mu.Unlock()
		if r.URL.Path == "/api/dispatch/external/preview" {
			_, _ = w.Write([]byte(`{"preview":{"title":"t","state":"open"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"receipt":{"id":"0123456789abcdef","status":"done"},"ticket":{"key":"CND-1","status":"done"},"publication":{"id":"0123456789abcdef","state":"sent"},"preview":"p","postSha256":"x","tracker":{"host":"github.com","fingerprint":"f","scopes":[],"source":"settings","state":"ok"}}`))
	})
	digest := strings.Repeat("a", 64)
	for _, c := range []struct {
		args  []string
		stdin string
		route string
		bound time.Duration
	}{
		{[]string{"dispatch", "import", "https://github.com/octo/app/issues/1", "--project", "CND"}, "", "POST /api/dispatch/projects/CND/import", api.ImportTimeout},
		{[]string{"dispatch", "import", "https://github.com/octo/app/issues/1", "--project", "CND", "--preview"}, "", "POST /api/dispatch/external/preview", api.TrackerReadTimeout},
		{[]string{"dispatch", "refresh", "CND-1"}, "", "POST /api/dispatch/tickets/CND-1/external/refresh", api.ImportTimeout},
		{[]string{"dispatch", "link-issue", "CND-1", "https://github.com/octo/app/issues/1"}, "", "POST /api/dispatch/tickets/CND-1/external", api.TrackerReadTimeout},
		{[]string{"dispatch", "unlink-issue", "CND-1"}, "", "POST /api/dispatch/tickets/CND-1/external/delete", api.TrackerReadTimeout},
		{[]string{"dispatch", "authority", "CND-1", "title", "github"}, "", "POST /api/dispatch/tickets/CND-1/external/authority", api.TrackerReadTimeout},
		{[]string{"dispatch", "read", "CND-1", "aaaaaaaaaaaaaaaa"}, "", "POST /api/dispatch/tickets/CND-1/links/aaaaaaaaaaaaaaaa/read", api.TrackerReadTimeout},
		{[]string{"dispatch", "publish", "CND-1", "--target", "linked", "--body-file", "-"}, "text", "POST /api/dispatch/tickets/CND-1/publications", api.PrepareTimeout},
		{[]string{"dispatch", "publish", "send", "0123456789abcdef", "--digest", digest}, "", "POST /api/dispatch/publications/0123456789abcdef/send", api.PublishTimeout},
		{[]string{"dispatch", "publish", "check", "0123456789abcdef"}, "", "POST /api/dispatch/publications/0123456789abcdef/check", api.CheckTimeout},
		{[]string{"dispatch", "publish", "close", "0123456789abcdef"}, "", "POST /api/dispatch/publications/0123456789abcdef/close", api.CloseNowTimeout},
		{[]string{"dispatch", "edit", "CND-1", "--status", "done"}, "", "POST /api/dispatch/tickets/CND-1/edit", api.MergedCloseTimeout},
		{[]string{"trackers", "set"}, "ghp_x\n", "POST /api/trackers/github", api.TrackerSaveTimeout},
	} {
		var out, errb strings.Builder
		if code, err := Run(t.Context(), append(c.args, "--server", srv), strings.NewReader(c.stdin), &out, &errb); code != 0 || err != nil {
			t.Fatalf("%q: exit %d %v\n%s%s", c.args, code, err, out.String(), errb.String())
		}
		mu.Lock()
		wait := waits[c.route]
		rest, ok := left[c.route]
		mu.Unlock()
		if !ok {
			t.Fatalf("%q: no request to %s", c.args, c.route)
		}
		if wait != c.bound+requestTimeout {
			t.Errorf("%q waits %v, not its route's %v and a request's %v", c.args, wait, c.bound, requestTimeout)
		}
		if rest <= c.bound {
			t.Errorf("%q: %v left of its wait as the request arrived, not more than the route's %v", c.args, rest, c.bound)
		}
	}
	if code, err := Run(t.Context(), []string{"dispatch", "edit", "CND-1", "--status", "ready", "--server", srv}, strings.NewReader(""), io.Discard, io.Discard); code != 0 || err != nil {
		t.Fatalf("an edit to ready: %d %v", code, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if w := waits["POST /api/dispatch/tickets/CND-1/edit"]; w != requestTimeout {
		t.Errorf("an edit to ready waits %v", w)
	}
}

// The guide's CLI forms of a ticket's GitHub operations and of the
// tokens are commands: each one's -h exits 0, and its usage carries the
// form as far as its first flag and that flag's value.
func TestTheTrackerOperationsCLIFormsAreCommands(t *testing.T) {
	clearConductorEnv(t)
	names := []string{"ticket_import_preview", "ticket_import", "ticket_link_issue", "ticket_unlink_issue", "ticket_refresh", "ticket_authority", "link_read",
		"publication_prepare", "publication_send", "publication_check", "publication_close", "tracker_status", "tracker_set", "tracker_remove"}
	for _, op := range api.Operations() {
		if !slices.Contains(names, op.Name) {
			continue
		}
		names = slices.DeleteFunc(names, func(n string) bool { return n == op.Name })
		if len(op.CLI) < 2 || op.CLI[0] != "conductor" {
			t.Errorf("%s: no CLI form: %q", op.Name, op.CLI)
			continue
		}
		var words []string
		for i, w := range op.CLI[1:] {
			if w != strings.ToLower(w) || strings.HasPrefix(w, "-") || strings.Contains(w, "://") || len(words) != i {
				break
			}
			words = append(words, w)
		}
		var errb strings.Builder
		if code, err := Run(t.Context(), append(words, "-h"), strings.NewReader(""), io.Discard, &errb); code != 0 || err != nil {
			t.Errorf("%s: %q -h: exit %d %v", op.Name, words, code, err)
		}
		if form := "conductor " + strings.Join(words, " "); !strings.Contains(errb.String(), form) {
			t.Errorf("%s: the usage does not say %q:\n%s", op.Name, form, errb.String())
		}
		for _, w := range op.CLI {
			if strings.HasPrefix(w, "--") && !regexp.MustCompile(`(?m)^  -`+strings.TrimPrefix(w, "--")+`\b`).MatchString(errb.String()) {
				t.Errorf("%s: %q -h has no flag %s", op.Name, words, w)
			}
		}
	}
	if len(names) > 0 {
		t.Fatalf("not in the table: %q", names)
	}
}

// Two live links to one URL (a delivery and an external link to the same
// pull request): naming the URL is refused, and the refusal names each
// link's id, kind and ref, so the person can name one; nothing is read or
// prepared. Named by an id, the link is read.
func TestTwoLinksToOneURLAreNamedByTheirIDs(t *testing.T) {
	clearConductorEnv(t)
	const pr = "https://github.com/octo/app/pull/5"
	var mu sync.Mutex
	var posts []string
	srv := handlerServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			posts = append(posts, r.URL.Path)
			_, _ = w.Write([]byte(`{"read":null}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": map[string]any{"key": "CND-1", "links": []any{
			map[string]any{"id": "aaaaaaaaaaaaaaaa", "kind": "delivery", "ref": "release-1", "url": pr},
			map[string]any{"id": "bbbbbbbbbbbbbbbb", "kind": "external", "ref": pr, "url": pr},
		}}})
	})
	run := func(args ...string) (int, error) {
		t.Helper()
		return Run(t.Context(), append(append([]string{"dispatch"}, args...), "--server", srv, "--token", dispatchToken), strings.NewReader("text"), io.Discard, io.Discard)
	}
	for _, args := range [][]string{{"read", "CND-1", pr}, {"publish", "CND-1", "--target", pr, "--body-file", "-"}} {
		code, err := run(args...)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "name one by its id, aaaaaaaaaaaaaaaa (delivery, release-1), bbbbbbbbbbbbbbbb (external)") {
			t.Fatalf("%q: %d %v", args, code, err)
		}
	}
	mu.Lock()
	if len(posts) != 0 {
		t.Fatalf("an ambiguous link was acted on: %q", posts)
	}
	mu.Unlock()
	if code, err := run("read", "CND-1", "bbbbbbbbbbbbbbbb"); code != 0 || err != nil {
		t.Fatalf("by its id: %d %v", code, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 1 || posts[0] != "/api/dispatch/tickets/CND-1/links/bbbbbbbbbbbbbbbb/read" {
		t.Fatalf("the reads: %q", posts)
	}
}

// A link's read whose checks were kept from an earlier read says when they
// were read on the checks' line, beside the read's own time; a read of its
// checks says none (#989).
func TestWriteLinkReadKeptChecksSayWhenRead(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	head := strings.Repeat("a", 40)
	r := dispatch.LinkRead{Kind: "change", State: "open", HeadSha: head, ReadAt: now,
		Checks:     []dispatch.LinkCheck{{ID: "status:1", Name: "lint", Status: "pending"}},
		Incomplete: &dispatch.ChecksIncomplete{Words: []string{"the checks could not be read: tracker_unavailable"}, ReadAt: now.Add(-12 * time.Minute)}}
	var b strings.Builder
	writeLinkRead(&b, "CND-1", "l1", r)
	if !strings.Contains(b.String(), "read 2026-11-18 12:00 UTC\n") || !strings.Contains(b.String(), "checks at "+short(head)+", as read 2026-11-18 11:48 UTC:\n") {
		t.Fatalf("kept checks:\n%s", b.String())
	}
	r.Incomplete = nil
	b.Reset()
	writeLinkRead(&b, "CND-1", "l1", r)
	if !strings.Contains(b.String(), "checks at "+short(head)+":\n") {
		t.Fatalf("checks read with the read:\n%s", b.String())
	}
}
