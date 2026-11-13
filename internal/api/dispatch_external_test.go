package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// The tests of a ticket's GitHub item (dispatch_external.go). Every token
// is a made-up one (plantedToken) and every GitHub the tests' own fake: no
// request leaves the machine, and nothing of this machine's own
// credentials is read.

// The fake's repository: octo/app, of id 4000001.
const (
	itemsRepo   = "/repositories/4000001"
	imageHost   = "private-user-images.githubusercontent.com"
	plantedJWT  = "PLANTEDjwtSIGNATURE0123456789"
	pngBytes    = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00"
	svgBytes    = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`
	headSHA     = "1111111111111111111111111111111111111111"
	mergeSHA    = "2222222222222222222222222222222222222222"
	itemsLayout = "2006-01-02T15:04:05Z"
)

// ghItem is an issue or a pull request of octo/app as the fake serves it.
// Fail, when set, is answered in its place: a status, the body of a
// refusal echoing the Authorization it was sent.
type ghItem struct {
	Node, Title, Body, State, Reason string
	Updated                          time.Time
	Pull, Merged                     bool
	Images                           []string // the body's images, by path on imageHost
	Fail                             int
	Huge                             bool     // an answer past the adapter's bound
	Labels                           []string // the labels, ["bug"] when nil
	ETag                             string   // the issue's ETag, the body's digest when ""
}

// ghItems scripts the fake's items and images. With poison set, every
// text of its answers (each string but those of a fixed form, formKeys)
// gets the poison after it, the checks a check run and eight workflow runs
// whose job listings are cut, and the item a milestone, so every string a
// read takes, and every listing the adapter reads, is in it.
type ghItems struct {
	f      *fakeGH
	mu     sync.Mutex
	items  map[int]*ghItem
	images map[string][]byte
	poison string
}

// poisons are the hostile fillers the walk puts after every text, each
// alone and all together (TestOutsideTextStorable).
var poisons = func() map[string]string {
	m := map[string]string{"a control character": "\x1b[2J\x07", "a reordering character": "\u202e\u2066", "a quote": strings.Repeat(`"`, 60),
		"a backslash": strings.Repeat(`\`, 60), "a short scheme token": strings.Repeat("x:// ", 12), "a tag character": "\U000E0041\U000E0049",
		"an over-long value": strings.Repeat("z", 2100)}
	all := ""
	for _, k := range slices.Sorted(maps.Keys(m)) {
		all += m[k]
	}
	m["all together"] = all
	return m
}()

// formKeys are the fields of the fake's answers in a fixed form the
// adapter parses (an id, an address, a time, a commit), not text.
var formKeys = map[string]bool{"node_id": true, "html_url": true, "url": true, "target_url": true, "details_url": true, "created_at": true, "updated_at": true,
	"closed_at": true, "merged_at": true, "completed_at": true, "started_at": true, "sha": true, "merge_commit_sha": true, "head_sha": true, "check_run_url": true}

// poisoned is v with poison after each of its texts.
func poisoned(v any, key, poison string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = poisoned(e, k, poison)
		}
		return out
	case []any:
		out := []any{}
		for _, e := range x {
			out = append(out, poisoned(e, key, poison))
		}
		return out
	case string:
		if !formKeys[key] {
			return x + poison
		}
	}
	return v
}

// answer writes v as JSON, poisoned when the fake is.
func (g *ghItems) answer(w http.ResponseWriter, v map[string]any) {
	if p := g.poisonWith(); p != "" {
		v = poisoned(v, "", p).(map[string]any)
	}
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

func newGHItems(f *fakeGH) *ghItems {
	g := &ghItems{f: f, items: map[int]*ghItem{}, images: map[string][]byte{}}
	f.Reply("GET", "api.github.com", "/repos/octo/app", http.StatusOK, map[string]any{"id": 4000001, "full_name": "octo/app"})
	return g
}

// set puts item n on the fake (and its routes, the first time).
func (g *ghItems) set(n int, it ghItem) {
	g.mu.Lock()
	_, had := g.items[n]
	g.items[n] = &it
	g.mu.Unlock()
	if had {
		return
	}
	num := strconv.Itoa(n)
	g.f.Handle("GET", "api.github.com", itemsRepo+"/issues/"+num, func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, n, false) })
	g.f.Handle("GET", "api.github.com", itemsRepo+"/pulls/"+num, func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, n, true) })
	g.f.Handle("GET", "api.github.com", itemsRepo+"/commits/"+headSHA+"/status", func(w http.ResponseWriter, r *http.Request) {
		g.answer(w, map[string]any{"state": "success", "total_count": 1, "statuses": []any{map[string]any{"id": 31, "context": "lint", "state": "success",
			"target_url": "https://ci.example.com/31", "created_at": itemsLayout, "updated_at": "2026-11-01T10:00:00Z"}}})
	})
	g.f.Handle("GET", "api.github.com", itemsRepo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		runs := []any{}
		if g.poisonOn() {
			for id := range 8 {
				runs = append(runs, map[string]any{"id": 6100 + id, "name": "r"})
			}
		}
		g.answer(w, map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})
	for id := range 8 {
		g.f.Handle("GET", "api.github.com", fmt.Sprintf("%s/actions/runs/%d/jobs", itemsRepo, 6100+id), func(w http.ResponseWriter, r *http.Request) {
			// One job of a thousand listed: the listing is cut, and says so.
			g.answer(w, map[string]any{"total_count": 1000, "jobs": []any{map[string]any{"id": 6200 + id, "name": "job", "status": "completed", "conclusion": "success",
				"head_sha": headSHA, "html_url": "https://github.com/octo/app/actions/runs/1/job/2", "started_at": itemsLayout, "completed_at": itemsLayout,
				"check_run_url": fmt.Sprintf("https://api.github.com/repos/octo/app/check-runs/%d", 6200+id)}}})
		})
	}
	g.f.Handle("GET", "api.github.com", itemsRepo+"/commits/"+headSHA+"/check-runs", func(w http.ResponseWriter, r *http.Request) {
		runs := []any{}
		if g.poisonOn() {
			runs = append(runs, map[string]any{"id": 41, "name": "build", "status": "completed", "conclusion": "success", "head_sha": headSHA,
				"html_url": "https://ci.example.com/41", "started_at": itemsLayout, "completed_at": itemsLayout, "app": map[string]any{"slug": "ci-app"}})
		}
		g.answer(w, map[string]any{"total_count": len(runs), "check_runs": runs})
	})
}

func (g *ghItems) poisonOn() bool { return g.poisonWith() != "" }

func (g *ghItems) poisonWith() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.poison
}

// edit changes item n on the fake.
func (g *ghItems) edit(n int, fn func(*ghItem)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fn(g.items[n])
}

// image serves an image's bytes at path on the image host.
func (g *ghItems) image(path string, b []byte) {
	g.mu.Lock()
	first := g.images[path] == nil
	g.images[path] = b
	g.mu.Unlock()
	if first {
		g.f.Handle("GET", imageHost, path, func(w http.ResponseWriter, r *http.Request) {
			g.mu.Lock()
			b := g.images[path]
			g.mu.Unlock()
			if b == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		})
	}
}

// gone makes an image's address answer 404.
func (g *ghItems) gone(path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.images[path] = nil
}

func (g *ghItems) serve(w http.ResponseWriter, r *http.Request, n int, pull bool) {
	g.mu.Lock()
	it := *g.items[n]
	g.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+plantedToken && auth != "Bearer "+plantedEnv {
		it.Fail = http.StatusUnauthorized
	}
	if it.Fail != 0 {
		// A refusal that echoes what it was sent, in its body and a header.
		w.Header().Set("X-Echo", auth)
		if it.Fail == http.StatusTooManyRequests {
			w.Header().Set("X-RateLimit-Remaining", "0")
		}
		w.WriteHeader(it.Fail)
		fmt.Fprintf(w, `{"message":"refused for %s"}`, auth)
		return
	}
	var v map[string]any
	num := strconv.Itoa(n)
	if pull {
		v = map[string]any{"number": n, "merged": it.Merged, "head": map[string]any{"sha": headSHA}, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 4000001}}}
		if it.Merged {
			v["merged_at"], v["merge_commit_sha"] = "2026-11-02T10:00:00Z", mergeSHA
		}
	} else {
		kind := "issues"
		if it.Pull {
			kind = "pull"
		}
		var html strings.Builder
		for _, p := range it.Images {
			fmt.Fprintf(&html, `<p><img src="https://%s%s?jwt=%s" alt="shot"></p>`, imageHost, p, plantedJWT)
		}
		labels := []any{map[string]any{"name": "bug"}}
		if it.Labels != nil {
			labels = []any{}
			for _, l := range it.Labels {
				labels = append(labels, map[string]any{"name": l})
			}
		}
		v = map[string]any{"id": 5000000 + n, "node_id": it.Node, "number": n, "html_url": "https://github.com/octo/app/" + kind + "/" + num, "title": it.Title,
			"body": it.Body, "body_html": html.String(), "state": it.State, "state_reason": it.Reason, "labels": labels,
			"assignees": []any{map[string]any{"login": "hubot"}}, "user": map[string]any{"login": "mona", "id": 9000001},
			"created_at": itemsLayout, "updated_at": it.Updated.UTC().Format(time.RFC3339)}
		if g.poisonOn() {
			v["milestone"] = map[string]any{"title": "v1"}
		}
		if it.State == "" {
			v["state"] = "open"
		}
		if it.Pull {
			v["pull_request"] = map[string]any{"url": "https://api.github.com/repos/octo/app/pulls/" + num}
		}
		if it.Huge {
			v["body_html"] = strings.Repeat("x", 1<<20)
		}
	}
	if p := g.poisonWith(); p != "" {
		v = poisoned(v, "", p).(map[string]any)
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	if it.ETag != "" && !pull {
		tag = it.ETag
	}
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(b)
}

// count is the fake's requests whose host and path are those given (the
// path's start, for a prefix ending in '/').
func (g *ghItems) count(host, path string) int {
	n := 0
	for _, r := range g.f.Requests() {
		if r.Host == host && (r.Path == path || strings.HasSuffix(path, "/") && strings.HasPrefix(r.Path, path)) {
			n++
		}
	}
	return n
}

// externalEnv is a server with a Dispatch store whose adapter reaches the
// fake, a made-up token kept for github.com, and the fake's items.
func externalEnv(t *testing.T) (*testEnv, *ghItems) {
	t.Helper()
	e, f := trackerEnv(t, func(c *config.Config) { c.DataDir = filepath.Join(t.TempDir(), "data") })
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token: %d %s", status, data)
	}
	return e, newGHItems(f)
}

// issue is a typical issue of the fake: its steps to reproduce, two images
// (a PNG and an SVG) and its node id.
func issue(node, title string, updated time.Time) ghItem {
	return ghItem{Node: node, Title: title, Body: "It breaks.\r\n\r\n### Steps to reproduce\r\n\r\n1. Open it\r\n2. See it break\r\n", Updated: updated,
		Images: []string{"/1/a.png", "/1/b.svg"}}
}

var t0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func (e *testEnv) importItem(t *testing.T, project, url, op string, status int, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"opId": op, "url": url}
	for k, v := range extra {
		body[k] = v
	}
	return e.expect(t, "POST", "/api/dispatch/projects/"+project+"/import", body, status, "")
}

// imagesOf is a reply's images by id.
func imagesOf(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	l, _ := out["images"].([]any)
	for _, x := range l {
		im := x.(map[string]any)
		m[im["id"].(string)] = im
	}
	return m
}

const (
	pngID = "https://" + imageHost + "/1/a.png"
	svgID = "https://" + imageHost + "/1/b.svg"
)

// An import makes the ticket GitHub owns the title and the description
// of, its steps to reproduce and its labels in the snapshot (never
// mapped), the PNG an attachment and the SVG skipped; the preview shows
// it beforehand and writes nothing, then names the ticket that has it. A
// resend answers the receipt and reads nothing (nothing is pending); a
// second import is 409 external_linked naming the ticket; a reference the
// adapter does not read is 400, quoting nothing; a host with no token 412.
func TestImportRoute(t *testing.T) {
	e, g := externalEnv(t)
	g.set(12, issue("I_kwDOitem12", "The list crashes", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "IMP", map[string]any{"trackerUrl": "https://github.com/octo/app", "tracker": "external"})

	pv := entityOf(t, e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "#12", "project": "IMP"}, http.StatusOK, ""), "preview")
	if pv["title"] != "The list crashes" || pv["linked"] != nil || !strings.Contains(pv["repro"].(string), "See it break") || len(pv["images"].([]any)) != 2 {
		t.Fatalf("the preview: %v", pv)
	}
	op := opID()
	out := e.importItem(t, "IMP", "https://github.com/octo/app/issues/12", op, http.StatusCreated, nil)
	tk := entityOf(t, out, "ticket")
	key := tk["key"].(string)
	owners, _ := tk["owners"].(map[string]any)
	var sn dispatch.ExternalSnapshot
	_ = json.Unmarshal([]byte(snapText(t, tk["externalSnapshot"])), &sn)
	if tk["source"] != "github" || tk["title"] != "The list crashes" || owners["title"] != "github" || owners["description"] != "github" ||
		!strings.Contains(sn.Repro, "See it break") || !slices.Equal(sn.Labels, []string{"bug"}) || len(tk["labels"].([]any)) != 0 || tk["externalUrl"] != "https://github.com/octo/app/issues/12" {
		t.Fatalf("the ticket: %v", tk)
	}
	ims := imagesOf(t, out)
	if ims[pngID]["attachmentId"] == nil || ims[svgID]["skipped"] != "type" || len(tk["attachments"].([]any)) != 1 {
		t.Fatalf("the images: %v, attachments %v", out["images"], tk["attachments"])
	}
	reads, downloads := g.count("api.github.com", itemsRepo+"/issues/12"), g.count(imageHost, "/1/")
	again := e.importItem(t, "IMP", "https://github.com/octo/app/issues/12", op, http.StatusOK, nil)
	if rcOf(again)["repeat"] != true || g.count("api.github.com", itemsRepo+"/issues/12") != reads || g.count(imageHost, "/1/") != downloads {
		t.Fatalf("the resend read again: %v", again)
	}
	pv = entityOf(t, e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "octo/app#12"}, http.StatusOK, ""), "preview")
	if pv["linked"] != key {
		t.Fatalf("the preview after: %v", pv)
	}
	twice := e.importItem(t, "IMP", "https://github.com/octo/app/issues/12", opID(), http.StatusConflict, nil)
	if errorCode(twice) != "external_linked" || rcOf(twice)["key"] != key {
		t.Fatalf("a second import: %v", twice)
	}
	for _, u := range []string{"https://evil.example.com/octo/app/issues/1", "https://github.com/octo/app/issues/1?" + plantedJWT, "not a url " + plantedJWT} {
		_, data, out := e.call(t, "POST", "/api/dispatch/projects/IMP/import", map[string]any{"opId": opID(), "url": u})
		if errorCode(out) != "not_a_tracker_url" || bytes.Contains(data, []byte(plantedJWT)) || bytes.Contains(data, []byte("evil")) {
			t.Fatalf("%q: %s", u, data)
		}
	}
	// An attempt's read of its ticket (ticket_read) shows the linked
	// issue's page, GitHub's state and when it was read, and nothing of the
	// token.
	run, sid, tok := e.memberSession(t, "Imported")
	doneOf(t)(e.srv.dispatch.AddLink(t.Context(), adminOp("Ada"), key, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}, nil))
	status, raw, sess := e.callAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil)
	st := entityOf(t, sess, "ticket")
	read, _ := st["externalRead"].(map[string]any)
	snap, _ := st["externalSnapshot"].(map[string]any)
	if status != http.StatusOK || st["externalUrl"] != "https://github.com/octo/app/issues/12" || snap["state"] != "open" || read["readAt"] == nil || len(leaks(raw)) > 0 {
		t.Fatalf("the session's read: %d %.400s", status, raw)
	}
	e.expect(t, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "octo/app#12"}, http.StatusPreconditionFailed, "no_token")
}

func rcOf(out map[string]any) map[string]any {
	rc, _ := out["receipt"].(map[string]any)
	return rc
}

func snapText(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// At most 8 images an import; a PNG, an image past its bound (skipped for
// its size) and an SVG (skipped for its type) recorded so that a refresh
// fetches none again; a signed address's query is stored nowhere: not in
// the ticket's read, not in any file of the data directory.
func TestImportImages(t *testing.T) {
	e, g := externalEnv(t)
	var paths []string
	for i := range 10 {
		p := fmt.Sprintf("/2/i%d.png", i)
		paths = append(paths, p)
		g.image(p, []byte(pngBytes+strconv.Itoa(i)))
	}
	g.image(paths[1], bytes.Repeat([]byte("a"), dispatch.MaxAttachmentSize+1))
	g.image(paths[2], []byte(svgBytes))
	it := issue("I_kwDOitem20", "Many images", t0)
	it.Images = paths
	g.set(20, it)
	e.newDispatchProject(t, "IMG", nil)
	out := e.importItem(t, "IMG", "https://github.com/octo/app/issues/20", opID(), http.StatusCreated, nil)
	ims := imagesOf(t, out)
	id := func(i int) string { return "https://" + imageHost + paths[i] }
	if len(ims) != dispatch.MaxImportImages || ims[id(8)] != nil || ims[id(0)]["attachmentId"] == nil || ims[id(1)]["skipped"] != "size" || ims[id(2)]["skipped"] != "type" {
		t.Fatalf("the images: %v", out["images"])
	}
	key := entityOf(t, out, "ticket")["key"].(string)
	downloads := g.count(imageHost, "/2/")
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": opID()}, http.StatusOK, "")
	if g.count(imageHost, "/2/") != downloads {
		t.Fatal("a refresh fetched an image the manifest has done")
	}
	_, data, _ := e.call(t, "GET", "/api/dispatch/tickets/"+key, nil)
	if bytes.Contains(data, []byte(plantedJWT)) || bytes.Contains(data, []byte("jwt=")) {
		t.Fatalf("the ticket holds a signed address's query: %.300s", data)
	}
	if hit := filesHolding(t, e.srv.store.Dir(), plantedJWT); len(hit) > 0 {
		t.Fatalf("files hold the signed query: %v", hit)
	}
}

// filesHolding is the files under dir holding s.
func filesHolding(t *testing.T, dir, s string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(s)) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// An import whose images' budget ends answers within its deadline with the
// image held pending (the fake holds its download at a gate); a resend,
// the gate open, finishes it.
func TestImportTimeout(t *testing.T) {
	e, g := externalEnv(t)
	caps := trackerCaps{busy: time.Second, reads: testWait, images: 300 * time.Millisecond, merged: time.Second, write: testWait}
	e.srv.external.caps = &caps
	g.set(30, issue("I_kwDOitem30", "Held", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	gate := g.f.Hold("GET", imageHost, "/1/a.png")
	e.newDispatchProject(t, "TMO", nil)
	op := opID()
	out := e.importItem(t, "TMO", "https://github.com/octo/app/issues/30", op, http.StatusCreated, nil)
	if ims := imagesOf(t, out); ims[pngID]["pending"] != true {
		t.Fatalf("the held image: %v", out["images"])
	}
	gate.Open()
	// The resend's own budget is not what is tested: it is the product's.
	caps.images = testWait
	again := e.importItem(t, "TMO", "https://github.com/octo/app/issues/30", op, http.StatusOK, nil)
	if ims := imagesOf(t, again); ims[pngID]["attachmentId"] == nil || ims[svgID]["skipped"] != "type" {
		t.Fatalf("the resend: %v", again["images"])
	}
}

// An import cut through the test's seam after its ticket, and after its
// first image's commit, is completed by its resend from the manifest: a
// committed image is never fetched again, though GitHub no longer serves
// it or serves other bytes under its address; with GitHub unavailable the
// resend answers its receipt with the rest pending, then completes them.
func TestImportResendCompletes(t *testing.T) {
	e, g := externalEnv(t)
	it := issue("I_kwDOitem40", "Cut", t0)
	it.Images = []string{"/4/a.png", "/4/b.png", "/4/c.png"}
	g.set(40, it)
	for _, p := range it.Images {
		g.image(p, []byte(pngBytes+p))
	}
	e.newDispatchProject(t, "CUT", nil)
	var cutAt string
	e.srv.external.cut = func(at string) bool { return at == cutAt }
	img := func(p string) string { return "https://" + imageHost + p }

	// After the ticket: no image yet.
	cutAt = "ticket"
	op := opID()
	e.call(t, "POST", "/api/dispatch/projects/CUT/import", map[string]any{"opId": op, "url": "https://github.com/octo/app/issues/40"})
	if g.count(imageHost, "/4/") != 0 {
		t.Fatal("images fetched past the cut")
	}
	// After the first image's commit.
	cutAt = "image"
	out := e.importItem(t, "CUT", "https://github.com/octo/app/issues/40", op, http.StatusOK, nil)
	ims := imagesOf(t, out)
	first := ims[img("/4/a.png")]["attachmentId"]
	if first == nil || ims[img("/4/b.png")]["pending"] != true {
		t.Fatalf("cut after the first image: %v", out["images"])
	}
	// The first image gone from GitHub, the second's bytes changed, and
	// GitHub unavailable for the item: the rest stay pending.
	cutAt = ""
	g.gone("/4/a.png")
	g.image("/4/b.png", []byte(pngBytes+"changed"))
	g.edit(40, func(i *ghItem) { i.Fail = http.StatusServiceUnavailable })
	out = e.importItem(t, "CUT", "https://github.com/octo/app/issues/40", op, http.StatusOK, nil)
	if ims := imagesOf(t, out); ims[img("/4/a.png")]["attachmentId"] != first || ims[img("/4/b.png")]["pending"] != true {
		t.Fatalf("with GitHub unavailable: %v", out["images"])
	}
	g.edit(40, func(i *ghItem) { i.Fail = 0 })
	downloadsA := g.count(imageHost, "/4/a.png")
	out = e.importItem(t, "CUT", "https://github.com/octo/app/issues/40", op, http.StatusOK, nil)
	ims = imagesOf(t, out)
	if ims[img("/4/a.png")]["attachmentId"] != first || ims[img("/4/b.png")]["attachmentId"] == nil || ims[img("/4/c.png")]["attachmentId"] == nil ||
		g.count(imageHost, "/4/a.png") != downloadsA {
		t.Fatalf("the last resend: %v", out["images"])
	}

	// An image's file written and its row not committed (the store's seam
	// between the two, its lease dropped there), then every receipt pruned:
	// the resend completes the image once, with no op_conflict.
	it = issue("I_kwDOitem41", "File first", t0)
	it.Images = []string{"/41/a.png", "/41/b.png"}
	g.set(41, it)
	for _, p := range it.Images {
		g.image(p, []byte(pngBytes+p))
	}
	raw := openRaw(t, filepath.Join(e.srv.dispatch.Dir(), dispatch.File))
	var once sync.Once
	e.srv.dispatch.SetBeforeCommitForTest(func() {
		once.Do(func() {
			if _, err := raw.Exec("DELETE FROM uploads"); err != nil {
				t.Error(err)
			}
		})
	})
	op = opID()
	out = e.importItem(t, "CUT", "https://github.com/octo/app/issues/41", op, http.StatusCreated, nil)
	e.srv.dispatch.SetBeforeCommitForTest(nil)
	if ims := imagesOf(t, out); ims[img("/41/a.png")]["pending"] != true || ims[img("/41/b.png")]["attachmentId"] == nil {
		t.Fatalf("the image whose row was not committed: %v", out["images"])
	}
	if _, err := raw.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	out = e.importItem(t, "CUT", "https://github.com/octo/app/issues/41", op, http.StatusOK, nil)
	key := entityOf(t, out, "ticket")["key"].(string)
	attached := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")["attachments"].([]any)
	if ims := imagesOf(t, out); ims[img("/41/a.png")]["attachmentId"] == nil || ims[img("/41/b.png")]["attachmentId"] == nil || len(attached) != 2 || rcOf(out)["status"] != "done" {
		t.Fatalf("the resend with its receipts pruned: %v, %d attachments", out, len(attached))
	}
}

// A resend completes only what its own operation left: an import, and a
// refresh that changed the ticket, each leaving an image pending, then the
// ticket unlinked and linked to another item with its own image pending,
// sent again under their first operation ids, answer their receipts and
// neither read that item nor attach its image.
func TestResendAfterRelink(t *testing.T) {
	e, g := externalEnv(t)
	e.newDispatchProject(t, "RLK", nil)
	for _, n := range []int{70, 71, 72, 73} {
		it := issue("I_kwDOrelink"+strconv.Itoa(n), "Relinked", t0)
		it.Images = []string{fmt.Sprintf("/%d/a.png", n)}
		g.set(n, it)
		g.image(it.Images[0], []byte(pngBytes))
	}
	relink := func(key string, n int) {
		t.Helper()
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external", map[string]any{"opId": opID(), "url": "https://github.com/octo/app/issues/" + strconv.Itoa(n)}, http.StatusOK, "")
	}
	untouched := func(what string, out map[string]any, n int, reads int) {
		t.Helper()
		p := fmt.Sprintf("https://%s/%d/a.png", imageHost, n)
		if rcOf(out)["repeat"] != true || g.count("api.github.com", itemsRepo+"/issues/"+strconv.Itoa(n)) != reads || g.count(imageHost, fmt.Sprintf("/%d/", n)) != 0 || imagesOf(t, out)[p]["pending"] != true {
			t.Fatalf("%s: %v, item %d read %d more times", what, out, n, g.count("api.github.com", itemsRepo+"/issues/"+strconv.Itoa(n))-reads)
		}
	}
	op := opID()
	key := entityOf(t, e.importItem(t, "RLK", "https://github.com/octo/app/issues/70", op, http.StatusCreated, map[string]any{"images": false}), "ticket")["key"].(string)
	relink(key, 71)
	reads := g.count("api.github.com", itemsRepo+"/issues/71")
	untouched("the import sent again", e.importItem(t, "RLK", "https://github.com/octo/app/issues/70", op, http.StatusOK, nil), 71, reads)

	key = entityOf(t, e.importItem(t, "RLK", "https://github.com/octo/app/issues/72", opID(), http.StatusCreated, map[string]any{"images": false}), "ticket")["key"].(string)
	g.edit(72, func(i *ghItem) { i.Title, i.Updated = "Changed", t0.Add(time.Hour) })
	op = opID()
	if out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": op, "images": false}, http.StatusOK, ""); out["changed"] != true {
		t.Fatalf("the refresh: %v", out)
	}
	relink(key, 73)
	reads = g.count("api.github.com", itemsRepo+"/issues/73")
	untouched("the refresh sent again", e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": op}, http.StatusOK, ""), 73, reads)
}

// An import's first send completes its images only while the ticket is
// still linked to the item it imported: unlinked and linked to another item
// between its commit and its images (the cut seam after the commit, the
// routes run from inside it), the other item's text naming the same image
// address, it attaches nothing and downloads nothing.
func TestImportImagesAfterRelink(t *testing.T) {
	e, g := externalEnv(t)
	e.newDispatchProject(t, "IRL", nil)
	first := issue("I_kwDOfirst90", "First", t0)
	first.Images = []string{"/90/shared.png"}
	other := issue("I_kwDOfirst91", "Other", t0)
	other.Images = []string{"/90/shared.png", "/91/own.png"}
	g.set(90, first)
	g.set(91, other)
	g.image("/90/shared.png", []byte(pngBytes))
	g.image("/91/own.png", []byte(pngBytes))
	const key = "IRL-1"
	var unlink, link int
	e.srv.external.cut = func(at string) bool {
		if at == "ticket" {
			unlink, _, _ = e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/delete", map[string]any{"opId": opID()})
			link, _, _ = e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external", map[string]any{"opId": opID(), "url": "https://github.com/octo/app/issues/91"})
		}
		return false
	}
	out := e.importItem(t, "IRL", "https://github.com/octo/app/issues/90", opID(), http.StatusCreated, nil)
	e.srv.external.cut = nil
	tv, err := e.srv.dispatch.Ticket(t.Context(), key)
	if err != nil || unlink != http.StatusOK || link != http.StatusOK || entityOf(t, out, "ticket")["key"] != key {
		t.Fatalf("the relink: unlink %d, link %d, %v, %v", unlink, link, err, out)
	}
	ims := imagesOf(t, out)
	if n := g.count(imageHost, "/90/") + g.count(imageHost, "/91/"); n != 0 || len(tv.Attachments) != 0 || ims["https://"+imageHost+"/90/shared.png"]["pending"] != true || ims["https://"+imageHost+"/91/own.png"]["pending"] != true {
		t.Fatalf("the import of 90, its ticket linked to 91 since: %d downloads, attachments %v, images %v", n, tv.Attachments, out["images"])
	}
}

func (e *testEnv) refresh(t *testing.T, key string, status int, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"opId": opID()}
	for k, v := range extra {
		body[k] = v
	}
	return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", body, status, "")
}

// A refresh writes what GitHub changed of the fields it owns (the title
// follows, an observe row), records an unchanged item's read alone (no
// receipt), keeps the ticket's status whatever GitHub's state (a closed
// issue is shown closed); a field taken over is never written by a
// refresh, and given back it follows GitHub again; a linked ticket keeps
// its own fields; an unlinked one has no item to refresh.
func TestRefreshRoute(t *testing.T) {
	e, g := externalEnv(t)
	g.set(50, issue("I_kwDOitem50", "First", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "REF", nil)
	key := entityOf(t, e.importItem(t, "REF", "https://github.com/octo/app/issues/50", opID(), http.StatusCreated, nil), "ticket")["key"].(string)
	if out := e.refresh(t, key, http.StatusOK, nil); out["changed"] != false || out["receipt"] != nil {
		t.Fatalf("an unchanged item: %v", out)
	}
	g.edit(50, func(i *ghItem) {
		i.Title, i.State, i.Reason, i.Updated = "Second", "closed", "completed", t0.Add(time.Hour)
	})
	out := e.refresh(t, key, http.StatusOK, nil)
	tk := entityOf(t, out, "ticket")
	var sn dispatch.ExternalSnapshot
	_ = json.Unmarshal([]byte(snapText(t, tk["externalSnapshot"])), &sn)
	if out["changed"] != true || rcOf(out)["status"] != "done" || tk["title"] != "Second" || tk["status"] == "done" || sn.State != "closed" {
		t.Fatalf("a changed item: %v", out)
	}
	rev := tk["rev"]
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "expectedRev": rev, "field": "title", "owner": "dispatch"}, http.StatusOK, "")
	g.edit(50, func(i *ghItem) { i.Title, i.Updated = "Third", t0.Add(2*time.Hour) })
	if tk := entityOf(t, e.refresh(t, key, http.StatusOK, nil), "ticket"); tk["title"] != "Second" {
		t.Fatalf("a refresh wrote a field Dispatch owns: %v", tk["title"])
	}
	rev = entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")["rev"]
	if tk := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "expectedRev": rev, "field": "title", "owner": "github"}, http.StatusOK, ""), "ticket"); tk["title"] != "Third" {
		t.Fatalf("given back: %v", tk["title"])
	}

	// A ticket linked keeps its own fields; unlinked, nothing to refresh.
	g.set(51, issue("I_kwDOitem51", "Linked", t0))
	own := e.newDispatchTicket(t, "REF", "My own", nil)
	lk := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/"+own["key"].(string)+"/external", map[string]any{"opId": opID(), "expectedRev": own["rev"], "url": "octo/app#51"}, http.StatusOK, ""), "ticket")
	if lk["title"] != "My own" || lk["externalUrl"] != "https://github.com/octo/app/issues/51" {
		t.Fatalf("linked: %v", lk)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+lk["key"].(string)+"/external/delete", map[string]any{"opId": opID(), "expectedRev": lk["rev"]}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/tickets/"+lk["key"].(string)+"/external/refresh", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")
}

// An item whose node id is not the one the ticket holds is 409 moved,
// naming the item found; nothing of the ticket changes, and the read's
// words are kept.
func TestRefreshMovedRefused(t *testing.T) {
	e, g := externalEnv(t)
	g.set(60, issue("I_kwDOitem60", "Moved", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "MOV", nil)
	tk := entityOf(t, e.importItem(t, "MOV", "https://github.com/octo/app/issues/60", opID(), http.StatusCreated, nil), "ticket")
	g.edit(60, func(i *ghItem) { i.Node, i.Title, i.Updated = "I_kwDOother", "Another item", t0.Add(time.Hour) })
	out := e.refresh(t, tk["key"].(string), http.StatusConflict, nil)
	after := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+tk["key"].(string), nil, http.StatusOK, ""), "ticket")
	read, _ := after["externalRead"].(map[string]any)
	if errorCode(out) != "moved" || !strings.Contains(apiMessage(out), "octo/app#60") || after["title"] != "Moved" || after["rev"] != tk["rev"] || read["error"] == nil {
		t.Fatalf("moved: %v; after %v", out, after)
	}
}

// R1 for a refresh: an import with its images left pending (images false)
// is completed by the next refresh, which, answered 304, reads once more
// without validators for the addresses and downloads the image; one
// answered with an unchanged 200 downloads from that read's addresses; and
// with nothing pending, a 304 makes no second read.
func TestRefreshResumesImages(t *testing.T) {
	e, g := externalEnv(t)
	g.set(70, issue("I_kwDOitem70", "Resume", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "RES", nil)
	out := e.importItem(t, "RES", "https://github.com/octo/app/issues/70", opID(), http.StatusCreated, map[string]any{"images": false})
	key := entityOf(t, out, "ticket")["key"].(string)
	if ims := imagesOf(t, out); ims[pngID]["pending"] != true || g.count(imageHost, "/1/") != 0 {
		t.Fatalf("images false: %v", out["images"])
	}
	// The first refresh records its read, the images left (images false).
	e.refresh(t, key, http.StatusOK, map[string]any{"images": false})
	reads := g.count("api.github.com", itemsRepo+"/issues/70")
	out = e.refresh(t, key, http.StatusOK, nil)
	if ims := imagesOf(t, out); ims[pngID]["attachmentId"] == nil || ims[svgID]["skipped"] != "type" || g.count("api.github.com", itemsRepo+"/issues/70") != reads+2 {
		t.Fatalf("after a 304: %v, %d reads", out["images"], g.count("api.github.com", itemsRepo+"/issues/70")-reads)
	}
	reads = g.count("api.github.com", itemsRepo+"/issues/70")
	e.refresh(t, key, http.StatusOK, nil)
	if g.count("api.github.com", itemsRepo+"/issues/70") != reads+1 {
		t.Fatal("nothing pending, and a 304 read twice")
	}

	// An unchanged 200 (the validator dropped): its own addresses.
	g.set(71, issue("I_kwDOitem71", "Resume 200", t0))
	key = entityOf(t, e.importItem(t, "RES", "https://github.com/octo/app/issues/71", opID(), http.StatusCreated, map[string]any{"images": false}), "ticket")["key"].(string)
	if _, err := e.srv.dispatch.Ticket(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	g.edit(71, func(i *ghItem) { i.Body += " " }) // a new ETag, the same description
	reads = g.count("api.github.com", itemsRepo+"/issues/71")
	out = e.refresh(t, key, http.StatusOK, nil)
	if ims := imagesOf(t, out); ims[pngID]["attachmentId"] == nil || g.count("api.github.com", itemsRepo+"/issues/71") != reads+1 {
		t.Fatalf("after a 200: %v", out["images"])
	}
}

// Two refreshes of one ticket are made one at a time: one held at the
// fake's gate holds the other behind the ticket's lock, and each reads
// what GitHub says when it reads; a read older by GitHub's clock than the
// one kept (the fake serving an older answer) is 409 stale_read, the newer
// snapshot kept.
func TestRefreshOrderGuarded(t *testing.T) {
	e, g := externalEnv(t)
	g.set(80, issue("I_kwDOitem80", "One", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "ORD", nil)
	key := entityOf(t, e.importItem(t, "ORD", "https://github.com/octo/app/issues/80", opID(), http.StatusCreated, nil), "ticket")["key"].(string)
	g.edit(80, func(i *ghItem) { i.Title, i.Updated = "Two", t0.Add(time.Hour) })
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/80")
	first := make(chan map[string]any, 1)
	go func() {
		_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": opID()})
		first <- out
	}()
	<-gate.Reached()
	second := make(chan map[string]any, 1)
	go func() {
		_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": opID()})
		second <- out
	}()
	// The first holds the ticket's lock while GitHub holds it, so the second
	// reads only after the first committed, and reads what GitHub says then.
	if _, held := e.srv.external.heldKey("ticket:" + key); !held {
		t.Fatal("the first refresh holds no lock")
	}
	g.edit(80, func(i *ghItem) { i.Title, i.Updated = "Three", t0.Add(2*time.Hour) })
	gate.Open()
	a, b := <-first, <-second
	if entityOf(t, a, "ticket")["title"] == nil || entityOf(t, b, "ticket")["title"] != "Three" {
		t.Fatalf("in turn: %v, then %v", a["ticket"], b["ticket"])
	}
	// GitHub answering an older update: refused, the newer kept.
	g.edit(80, func(i *ghItem) { i.Title, i.Updated = "Old", t0.Add(30*time.Minute) })
	out := e.refresh(t, key, http.StatusConflict, nil)
	if errorCode(out) != "stale_read" || entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")["title"] != "Three" {
		t.Fatalf("an older read: %v", out)
	}
}

// heldKey reports whether a lock is held now (for a test that waits on
// it).
func (x *externalState) heldKey(key string) (chan struct{}, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	ch, ok := x.held[key]
	return ch, ok
}

// A delivery link to a GitHub pull request, for a host with a token, is
// read once after its add, in the background, and the read reaches the
// pages as a dispatch_read event; one to a host with no token is not read
// at all. A link's read reads its checks at the head commit; its full
// read is served whole.
func TestLinkReadOnAdd(t *testing.T) {
	e, g := externalEnv(t)
	g.set(90, ghItem{Node: "PR_kwDOitem90", Title: "A change", Pull: true, Updated: t0})
	events := e.sse(t)
	e.newDispatchProject(t, "LNK", nil)
	tk := e.newDispatchTicket(t, "LNK", "Deliver it", nil)
	key := tk["key"].(string)
	add := map[string]any{"opId": opID(), "kind": "delivery", "url": "https://github.com/octo/app/pull/90"}
	out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", add, http.StatusCreated, "")
	link := rcOf(out)["id"].(string)
	if !waitNamed(t, events, "dispatch_read", link) {
		t.Fatal("no dispatch_read for the link's read")
	}
	// The add sent again is its receipt: no read is started (the token is
	// asked for, on the request, only for a read to start).
	reached := e.srv.trackers.reached.Load()
	if again := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", add, http.StatusOK, ""); rcOf(again)["repeat"] != true || e.srv.trackers.reached.Load() != reached {
		t.Fatalf("the add's resend: %v, the token asked for %d times", again, e.srv.trackers.reached.Load()-reached)
	}
	read, found, err := e.srv.dispatch.LinkReadOf(t.Context(), link)
	if err != nil || !found || read.State != "open" || read.Kind != "change" || read.HeadSha != headSHA || len(read.Checks) != 1 || read.Checks[0].Name != "lint" {
		t.Fatalf("the read after the add: %+v %v %v", read, found, err)
	}
	g.edit(90, func(i *ghItem) { i.Merged, i.State, i.Updated = true, "closed", t0.Add(time.Hour) })
	r := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{}, http.StatusOK, ""), "read")
	if r["state"] != "merged" || r["mergeSha"] != mergeSHA {
		t.Fatalf("read again: %v", r)
	}
	if got := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", nil, http.StatusOK, ""), "read"); got["state"] != "merged" || len(got["checks"].([]any)) != 1 {
		t.Fatalf("the full read: %v", got)
	}
	// A host with no token: nothing read (the token is asked for on the
	// request, and with none no read is started; nor could one ask GitHub).
	e.expect(t, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}, http.StatusOK, "")
	before := len(g.f.Requests())
	out = e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "external", "url": "https://github.com/octo/app/pull/90"}, http.StatusCreated, "")
	other := rcOf(out)["id"].(string)
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+other+"/read", map[string]any{}, http.StatusPreconditionFailed, "no_token")
	if _, found, _ := e.srv.dispatch.LinkReadOf(t.Context(), other); found || len(g.f.Requests()) != before {
		t.Fatal("a link to a host with no token was read")
	}
	if got := e.expect(t, "GET", "/api/dispatch/tickets/"+key+"/links/"+other+"/read", nil, http.StatusOK, ""); got["read"] != nil {
		t.Fatalf("a read of no read: %v", got)
	}
}

// A validator GitHub answers out of the store's form (past 200 bytes, or
// holding a query or an address) is dropped: the import keeps none, and
// the next refresh asks without If-None-Match.
func TestOutOfFormValidatorDropped(t *testing.T) {
	e, g := externalEnv(t)
	e.newDispatchProject(t, "VAL", nil)
	for i, tag := range []string{`"` + strings.Repeat("v", dispatch.MaxValidator-1) + `"`, `"a?b"`, `"https://example.com/x"`} {
		n := 155 + i
		it := issue("I_kwDOvalid"+strconv.Itoa(n), "Validator", t0)
		it.Images, it.ETag = nil, tag
		g.set(n, it)
		key := entityOf(t, e.importItem(t, "VAL", "https://github.com/octo/app/issues/"+strconv.Itoa(n), opID(), http.StatusCreated, nil), "ticket")["key"].(string)
		before := len(g.f.Requests())
		e.refresh(t, key, http.StatusOK, nil)
		for _, r := range g.f.Requests()[before:] {
			if r.Path == itemsRepo+"/issues/"+strconv.Itoa(n) && r.Header.Get("If-None-Match") != "" {
				t.Errorf("%.20s: the refresh sent If-None-Match %.20s", tag, r.Header.Get("If-None-Match"))
			}
		}
	}
}

// waitNamed waits for an event of the name (sse's "<name> <data>") whose
// data holds s.
func waitNamed(t *testing.T, events <-chan string, name, s string) bool {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case ev := <-events:
			if strings.HasPrefix(ev, name+" ") && strings.Contains(ev, s) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// A project whose policy requires merged: the close reads the delivery
// link first, so a pull request still open is refused (close_policy
// naming merged) and, merged on GitHub since, met at the next close, with
// the read it rested on in closed_on.
func TestCloseReadsMerged(t *testing.T) {
	e, g := externalEnv(t)
	g.set(91, ghItem{Node: "PR_kwDOitem91", Title: "Ship", Pull: true, Updated: t0})
	e.newDispatchProject(t, "MRG", map[string]any{"closeRequires": []string{"merged"}})
	key := e.newDispatchTicket(t, "MRG", "Ship it", nil)["key"].(string)
	link := rcOf(e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "delivery", "url": "https://github.com/octo/app/pull/91"}, http.StatusCreated, ""))["id"].(string)
	if !waitFor(testWait, func() bool { _, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link); return ok }) {
		t.Fatal("the delivery was not read after its add")
	}
	done := func(status int) map[string]any {
		t.Helper()
		return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}, status, "")
	}
	if out := done(http.StatusConflict); errorCode(out) != "close_policy" || !slices.Contains(rcOf(out)["missing"].([]any), any("merged")) {
		t.Fatalf("open: %v", out)
	}
	g.edit(91, func(i *ghItem) { i.Merged, i.State, i.Updated = true, "closed", t0.Add(time.Hour) })
	reads := g.count("api.github.com", itemsRepo+"/pulls/91")
	tk := entityOf(t, done(http.StatusOK), "ticket")
	if tk["status"] != "done" || tk["closedOn"] == nil || g.count("api.github.com", itemsRepo+"/pulls/91") != reads+1 {
		t.Fatalf("merged: %v", tk)
	}
}

// Each failure of GitHub's is one answer, in fixed words that carry
// nothing GitHub sent (its refusal echoes the token it was sent): 404
// external_not_found, 422 token_refused (never 401), 403
// token_permission, 429 rate_limited with Retry-After, 502
// tracker_unavailable for a 5xx, 502 tracker_bound past an answer's
// bound; the store as it was.
func TestTrackerErrorsMapped(t *testing.T) {
	e, g := externalEnv(t)
	for _, c := range []struct {
		fail   int
		huge   bool
		status int
		code   string
	}{
		{http.StatusNotFound, false, http.StatusNotFound, "external_not_found"},
		{http.StatusGone, false, http.StatusNotFound, "external_not_found"},
		{http.StatusUnauthorized, false, http.StatusUnprocessableEntity, "token_refused"},
		{http.StatusForbidden, false, http.StatusForbidden, "token_permission"},
		{http.StatusTooManyRequests, false, http.StatusTooManyRequests, "rate_limited"},
		{http.StatusBadGateway, false, http.StatusBadGateway, "tracker_unavailable"},
		{0, true, http.StatusBadGateway, "tracker_bound"},
	} {
		g.set(100, ghItem{Node: "I_kwDOitem100", Title: "Fails", Updated: t0, Fail: c.fail, Huge: c.huge})
		status, data, out := e.call(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "octo/app#100"})
		if status != c.status || errorCode(out) != c.code || len(leaks(data)) > 0 || bytes.Contains(data, []byte("refused for")) {
			t.Errorf("%d: %d %s", c.fail, status, data)
		}
	}
	if status, _, out := e.call(t, "POST", "/api/dispatch/projects/NONE/import", map[string]any{"opId": opID(), "url": "octo/app#100"}); status != http.StatusNotFound {
		t.Errorf("no project: %d %v", status, out)
	}
}

// A ticket's edit, then three refreshes that find nothing new: three
// dispatch_read events, none with a seq, and no dispatch event among
// them.
func TestDispatchReadEvent(t *testing.T) {
	e, g := externalEnv(t)
	g.set(110, issue("I_kwDOitem110", "Read", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "EVT", nil)
	tk := entityOf(t, e.importItem(t, "EVT", "https://github.com/octo/app/issues/110", opID(), http.StatusCreated, nil), "ticket")
	key := tk["key"].(string)
	events := e.sse(t)
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"priority": 2}}, http.StatusOK, "")
	if !waitNamed(t, events, "dispatch", tk["id"].(string)) {
		t.Fatal("no dispatch event for the edit")
	}
	for range 3 {
		e.refresh(t, key, http.StatusOK, nil)
	}
	reads := 0
	deadline := time.After(testWait)
	for reads < 3 {
		select {
		case ev := <-events:
			name, data, _ := strings.Cut(ev, " ")
			switch name {
			case "dispatch":
				t.Fatalf("a dispatch event for a read: %s", ev)
			case "dispatch_read":
				var m map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &m); err != nil || m["seq"] != nil || m["ticket"] != tk["id"] || m["project"] != "EVT" || len(data) > maxDispatchReadEvent {
					t.Fatalf("a dispatch_read: %s", ev)
				}
				reads++
			}
		case <-deadline:
			t.Fatalf("%d dispatch_read events of 3", reads)
		}
	}
}

// Each deadline is the sum of its steps' caps. With the caps scaled down,
// a request held behind a ticket's lock past the wait answers 503
// tracker_busy with Retry-After, nothing read or written, and the route
// holding the lock answers within its own deadline.
func TestTrackerRouteDeadlines(t *testing.T) {
	c := defaultTrackerCaps
	for _, d := range []struct {
		got, want time.Duration
	}{
		{TrackerReadTimeout, c.busy + c.reads + c.write},
		{ImportTimeout, c.busy + c.reads + c.write + c.images},
		{MergedCloseTimeout, c.busy + c.merged + c.write + c.write},
		{TrackerReadTimeout, 90 * time.Second}, {ImportTimeout, 150 * time.Second}, {MergedCloseTimeout, 105 * time.Second},
	} {
		if d.got != d.want {
			t.Errorf("a deadline of %s, not %s", d.got, d.want)
		}
	}
	e, g := externalEnv(t)
	caps := trackerCaps{busy: 200 * time.Millisecond, reads: testWait, images: time.Second, merged: time.Second, write: testWait}
	e.srv.external.caps = &caps
	g.set(120, issue("I_kwDOitem120", "Busy", t0))
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "BSY", nil)
	tk := entityOf(t, e.importItem(t, "BSY", "https://github.com/octo/app/issues/120", opID(), http.StatusCreated, nil), "ticket")
	key := tk["key"].(string)
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/120")
	held := make(chan int, 1)
	go func() {
		status, _, _ := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": opID()})
		held <- status
	}()
	<-gate.Reached()
	reads := g.count("api.github.com", itemsRepo+"/issues/120")
	status, data, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/delete", map[string]any{"opId": opID(), "expectedRev": tk["rev"]})
	if status != http.StatusServiceUnavailable || errorCode(out) != "tracker_busy" || g.count("api.github.com", itemsRepo+"/issues/120") != reads {
		t.Fatalf("behind the lock: %d %s", status, data)
	}
	if v, _ := e.srv.dispatch.Ticket(t.Context(), key); v.ExternalID == "" {
		t.Fatal("the busy unlink unlinked")
	}
	gate.Open()
	if s := <-held; s != http.StatusOK {
		t.Fatalf("the held refresh: %d", s)
	}
}

// Every route of this row that calls GitHub gets its credential from
// credentialFor (the keep's seam counts the token leaving the value that
// holds it while the route is served), and the fake sees that token on
// every request to the API host during the route, in Authorization alone,
// and on no image's fetch.
func TestEveryTrackerRouteUsesCredentialFor(t *testing.T) {
	e, g := externalEnv(t)
	served := map[string]bool{}
	do := func(pattern, path, body string, status int, code string) map[string]any {
		t.Helper()
		method, _, _ := strings.Cut(pattern, " ")
		before, n := e.srv.trackers.reached.Load(), len(g.f.Requests())
		got, data, out := e.callAs(t, adminToken, method, path, []byte(body))
		if got != status || code != "" && errorCode(out) != code {
			t.Fatalf("%s: %d %s", path, got, data)
		}
		// A reference out of form is refused before any credential.
		if status != http.StatusBadRequest && e.srv.trackers.reached.Load() == before {
			t.Errorf("%s reached no credential", pattern)
		}
		for _, r := range g.f.Requests()[n:] {
			auth := r.Header.Get("Authorization")
			if r.Host == "api.github.com" && auth != "Bearer "+plantedToken || r.Host != "api.github.com" && auth != "" {
				t.Errorf("%s: a request to %s%s, Authorization given %v", pattern, r.Host, r.Path, auth != "")
			}
		}
		served[pattern] = true
		return out
	}
	driveExternal(t, e, g, do)
	for _, p := range externalTouching {
		if !served[p] {
			t.Errorf("%s was not driven", p)
		}
	}
}

// The guide's rows of a ticket's GitHub item: each the admin's, with no
// MCP tool (a person reads GitHub; no agent's tool does), its body bound
// the route's, and its route one the mux serves; the read after a link's
// add and the close's reads ride on ticket_link and ticket_edit.
func TestGuideRowsTrackers(t *testing.T) {
	want := map[string]string{
		"ticket_import_preview": "POST /api/dispatch/external/preview",
		"ticket_import":         "POST /api/dispatch/projects/{key}/import",
		"ticket_link_issue":     "POST /api/dispatch/tickets/{key}/external",
		"ticket_unlink_issue":   "POST /api/dispatch/tickets/{key}/external/delete",
		"ticket_refresh":        "POST /api/dispatch/tickets/{key}/external/refresh",
		"ticket_authority":      "POST /api/dispatch/tickets/{key}/external/authority",
		"link_read":             "POST /api/dispatch/tickets/{key}/links/{id}/read",
		"tracker_status":        "GET /api/trackers",
		"tracker_set":           "POST /api/trackers/github",
	}
	seen := map[string]bool{}
	for _, op := range Operations() {
		route, ok := want[op.Name]
		if !ok {
			continue
		}
		seen[op.Name] = true
		if op.Method+" "+op.Path != route || op.Scope != "admin" || op.MCP != "" {
			t.Errorf("%s: %s %s, scope %s, tool %q", op.Name, op.Method, op.Path, op.Scope, op.MCP)
		}
		if op.Method == http.MethodPost && op.Name != "tracker_set" && op.Bounds["body"] != maxBody {
			t.Errorf("%s: body bound %d", op.Name, op.Bounds["body"])
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("rows %v of %v", seen, want)
	}
	if b := Operations(); !slices.ContainsFunc(b, func(o Operation) bool {
		return o.Name == "ticket_import" && o.Bounds["images"] == dispatch.MaxImportImages && o.Bounds["image"] == dispatch.MaxAttachmentSize
	}) {
		t.Error("ticket_import does not name its images' bounds")
	}
}

// externalTouching is the routes of this row that call GitHub.
var externalTouching = []string{
	"POST /api/dispatch/external/preview", "POST /api/dispatch/projects/{key}/import", "POST /api/dispatch/tickets/{key}/external",
	"POST /api/dispatch/tickets/{key}/external/refresh", "POST /api/dispatch/tickets/{key}/external/authority",
	"POST /api/dispatch/tickets/{key}/links/{id}/read", "POST /api/dispatch/tickets/{key}/links", "POST /api/dispatch/tickets/{key}/edit",
}

// driveExternal drives every operation of this row that calls GitHub,
// through do (a fixture: the pattern, the path, the body, the answer it
// must give): a preview, an import with its images, a link, refreshes
// (unchanged, changed), a field taken over and given back, a link's add
// and its read, a close whose read of its delivery fails and one that
// reads it, an import's resend with its images left pending and one past
// its bound, a refresh answered 304 with an image pending (its second
// read), the failures of GitHub's (each refusal, a 5xx, an answer past its
// bound), a request held at the fake and cancelled by its client, a
// reference out of form. The fake's items are octo/app's 140 to 145; the
// project DRV requires merged.
func driveExternal(t *testing.T, e *testEnv, g *ghItems, do func(pattern, path, body string, status int, code string) map[string]any) {
	t.Helper()
	g.set(140, issue("I_kwDOdrive140", "Drive", t0))
	g.set(141, issue("I_kwDOdrive141", "Drive link", t0))
	g.set(142, ghItem{Node: "PR_kwDOdrive142", Title: "Drive change", Pull: true, Merged: true, State: "closed", Updated: t0})
	g.set(143, ghItem{Node: "I_kwDOdrive143", Title: "Drive fails", Updated: t0})
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "DRV", map[string]any{"closeRequires": []string{"merged"}})
	do("POST /api/dispatch/external/preview", "/api/dispatch/external/preview", `{"url":"https://github.com/octo/app/issues/140"}`, http.StatusOK, "")
	op := opID()
	out := do("POST /api/dispatch/projects/{key}/import", "/api/dispatch/projects/DRV/import", `{"opId":"`+op+`","url":"octo/app#140"}`, http.StatusCreated, "")
	key := entityOf(t, out, "ticket")["key"].(string)
	g.edit(140, func(i *ghItem) { i.Images = append(i.Images, "/1/c.png") })
	g.image("/1/c.png", []byte(pngBytes+"c"))
	do("POST /api/dispatch/tickets/{key}/external/refresh", "/api/dispatch/tickets/"+key+"/external/refresh", `{"opId":"`+opID()+`"}`, http.StatusOK, "")
	g.edit(140, func(i *ghItem) { i.Title, i.Updated = "Drive again", t0.Add(time.Hour) })
	do("POST /api/dispatch/tickets/{key}/external/refresh", "/api/dispatch/tickets/"+key+"/external/refresh", `{"opId":"`+opID()+`"}`, http.StatusOK, "")
	rev := func(k string) string {
		v, err := e.srv.dispatch.Ticket(context.Background(), k)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(v.Rev, 10)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "field": "description", "owner": "dispatch"}, http.StatusOK, "")
	do("POST /api/dispatch/tickets/{key}/external/authority", "/api/dispatch/tickets/"+key+"/external/authority",
		`{"opId":"`+opID()+`","expectedRev":`+rev(key)+`,"field":"description","owner":"github"}`, http.StatusOK, "")
	own := e.newDispatchTicket(t, "DRV", "Own", nil)["key"].(string)
	do("POST /api/dispatch/tickets/{key}/external", "/api/dispatch/tickets/"+own+"/external", `{"opId":"`+opID()+`","url":"#141"}`, http.StatusBadRequest, "not_a_tracker_url")
	do("POST /api/dispatch/tickets/{key}/external", "/api/dispatch/tickets/"+own+"/external", `{"opId":"`+opID()+`","url":"octo/app#141"}`, http.StatusOK, "")
	out = do("POST /api/dispatch/tickets/{key}/links", "/api/dispatch/tickets/"+key+"/links", `{"opId":"`+opID()+`","kind":"delivery","url":"https://github.com/octo/app/pull/142"}`, http.StatusCreated, "")
	link := rcOf(out)["id"].(string)
	if !waitFor(testWait, func() bool { _, ok, _ := e.srv.dispatch.LinkReadOf(context.Background(), link); return ok }) {
		t.Fatal("the added link was not read")
	}
	do("POST /api/dispatch/tickets/{key}/links/{id}/read", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", `{}`, http.StatusOK, "")
	// The close's read of its delivery failing (a 5xx), then read.
	g.edit(142, func(i *ghItem) { i.Fail = http.StatusServiceUnavailable })
	do("POST /api/dispatch/tickets/{key}/edit", "/api/dispatch/tickets/"+key+"/edit", `{"opId":"`+opID()+`","set":{"status":"done"}}`, http.StatusConflict, "close_policy")
	g.edit(142, func(i *ghItem) { i.Fail = 0 })
	do("POST /api/dispatch/tickets/{key}/edit", "/api/dispatch/tickets/"+key+"/edit", `{"opId":"`+opID()+`","set":{"status":"done"}}`, http.StatusOK, "")
	// The import's resend with its images left pending, one of them past
	// its bound; a refresh answered 304 with an image pending, which reads
	// once more without validators.
	big := issue("I_kwDOdrive144", "Drive resend", t0)
	big.Images = []string{"/1/a.png", "/7/big.png"}
	g.set(144, big)
	g.image("/7/big.png", bytes.Repeat([]byte("a"), dispatch.MaxAttachmentSize+1))
	iop := opID()
	do("POST /api/dispatch/projects/{key}/import", "/api/dispatch/projects/DRV/import", `{"opId":"`+iop+`","url":"octo/app#144","images":false}`, http.StatusCreated, "")
	out = do("POST /api/dispatch/projects/{key}/import", "/api/dispatch/projects/DRV/import", `{"opId":"`+iop+`","url":"octo/app#144"}`, http.StatusOK, "")
	if ims := imagesOf(t, out); ims["https://"+imageHost+"/7/big.png"]["skipped"] != "size" || ims[pngID]["attachmentId"] == nil {
		t.Errorf("the import's resend: %v", out["images"])
	}
	g.set(145, issue("I_kwDOdrive145", "Drive 304", t0))
	k145 := entityOf(t, do("POST /api/dispatch/projects/{key}/import", "/api/dispatch/projects/DRV/import", `{"opId":"`+opID()+`","url":"octo/app#145","images":false}`, http.StatusCreated, ""), "ticket")["key"].(string)
	do("POST /api/dispatch/tickets/{key}/external/refresh", "/api/dispatch/tickets/"+k145+"/external/refresh", `{"opId":"`+opID()+`","images":false}`, http.StatusOK, "")
	reads := g.count("api.github.com", itemsRepo+"/issues/145")
	do("POST /api/dispatch/tickets/{key}/external/refresh", "/api/dispatch/tickets/"+k145+"/external/refresh", `{"opId":"`+opID()+`"}`, http.StatusOK, "")
	if g.count("api.github.com", itemsRepo+"/issues/145") != reads+2 {
		t.Errorf("a 304 with an image pending: %d reads", g.count("api.github.com", itemsRepo+"/issues/145")-reads)
	}
	// GitHub's failures, each answered in its fixed words.
	for _, c := range []struct {
		fail   int
		huge   bool
		status int
		code   string
	}{
		{http.StatusNotFound, false, http.StatusNotFound, "external_not_found"}, {http.StatusUnauthorized, false, http.StatusUnprocessableEntity, "token_refused"},
		{http.StatusForbidden, false, http.StatusForbidden, "token_permission"}, {http.StatusTooManyRequests, false, http.StatusTooManyRequests, "rate_limited"},
		{http.StatusServiceUnavailable, false, http.StatusBadGateway, "tracker_unavailable"}, {0, true, http.StatusBadGateway, "tracker_bound"},
	} {
		g.edit(143, func(i *ghItem) { i.Fail, i.Huge = c.fail, c.huge })
		do("POST /api/dispatch/projects/{key}/import", "/api/dispatch/projects/DRV/import", `{"opId":"`+opID()+`","url":"octo/app#143"}`, c.status, c.code)
	}
	// A request its client cancels while GitHub holds it: the route goes on
	// to its own end, and answers no one.
	g.edit(143, func(i *ghItem) { i.Fail, i.Huge = 0, false })
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/143")
	ctx, cancel := context.WithCancel(context.Background())
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		req, _ := http.NewRequestWithContext(ctx, "POST", e.http.URL+"/api/dispatch/external/preview", strings.NewReader(`{"url":"octo/app#143"}`))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		if resp, err := e.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-gate.Reached()
	cancel()
	<-sent
	gate.Open()
}

// waitLock waits for a request found waiting on the lock named key
// (externalState.waiting), testWait only bounding a hang.
func waitLock(t *testing.T, waits <-chan string, key string) {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case k := <-waits:
			if k == key {
				return
			}
		case <-deadline:
			t.Fatalf("nothing waits on %s", key)
		}
	}
}

// heldWaits is a channel told of every ticket's or link's lock found held.
func heldWaits(e *testEnv) <-chan string {
	waits := make(chan string, 64)
	e.srv.external.waiting = func(key string) {
		select {
		case waits <- key:
		default:
		}
	}
	return waits
}

// A resend that waits behind its first send, which holds the ticket's lock
// while the fake holds its read of GitHub, answers the first's receipt and
// asks GitHub nothing: a refresh that changed the ticket, a link, and a
// field given back to GitHub alike.
func TestResendBehindItsFirst(t *testing.T) {
	e, g := externalEnv(t)
	waits := heldWaits(e)
	e.newDispatchProject(t, "RSD", nil)
	// twice sends body to path twice, the second once the first is held at
	// the fake's read of item n and the second waits on key's lock: both
	// answers, and the fake's reads of the item made meanwhile.
	twice := func(path string, n int, key string, body map[string]any) (a, b map[string]any, reads int) {
		t.Helper()
		item := itemsRepo + "/issues/" + strconv.Itoa(n)
		before := g.count("api.github.com", item)
		gate := g.f.Hold("GET", "api.github.com", item)
		first, second := make(chan map[string]any, 1), make(chan map[string]any, 1)
		go func() { _, _, out := e.call(t, "POST", path, body); first <- out }()
		<-gate.Reached()
		go func() { _, _, out := e.call(t, "POST", path, body); second <- out }()
		waitLock(t, waits, "ticket:"+key)
		gate.Open()
		a, b = <-first, <-second
		return a, b, g.count("api.github.com", item) - before
	}
	same := func(what string, a, b map[string]any, reads int) {
		t.Helper()
		ra, rb := rcOf(a), rcOf(b)
		if reads != 1 || ra["status"] != "done" || rb["repeat"] != true || ra["opId"] != rb["opId"] || ra["rev"] != rb["rev"] || ra["key"] != rb["key"] || a["changed"] != b["changed"] {
			t.Fatalf("%s: %d reads; %v, then %v", what, reads, a, b)
		}
	}
	it := issue("I_kwDOresend88", "Before", t0)
	it.Images = nil
	g.set(88, it)
	key := entityOf(t, e.importItem(t, "RSD", "https://github.com/octo/app/issues/88", opID(), http.StatusCreated, nil), "ticket")["key"].(string)
	g.edit(88, func(i *ghItem) { i.Title, i.Updated = "After", t0.Add(time.Hour) })
	a, b, reads := twice("/api/dispatch/tickets/"+key+"/external/refresh", 88, key, map[string]any{"opId": opID()})
	same("a refresh", a, b, reads)
	if a["changed"] != true {
		t.Fatalf("the first refresh: %v", a)
	}

	g.set(89, ghItem{Node: "I_kwDOresend89", Title: "Linked", Updated: t0})
	own := e.newDispatchTicket(t, "RSD", "Own", nil)["key"].(string)
	a, b, reads = twice("/api/dispatch/tickets/"+own+"/external", 89, own, map[string]any{"opId": opID(), "url": "octo/app#89"})
	same("a link", a, b, reads)

	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "field": "title", "owner": "dispatch"}, http.StatusOK, "")
	a, b, reads = twice("/api/dispatch/tickets/"+key+"/external/authority", 88, key, map[string]any{"opId": opID(), "field": "title", "owner": "github"})
	same("a field given back", a, b, reads)
}

// A refresh that changed the ticket and stopped before its images (the
// test's cut after its ticket's commit, or images false) is completed by
// the same operation sent again: the images done, the answer the
// receipt's.
func TestRefreshResendCompletesImages(t *testing.T) {
	e, g := externalEnv(t)
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "RIM", nil)
	for _, c := range []struct {
		n     int
		cut   bool
		first map[string]any
	}{{86, true, nil}, {87, false, map[string]any{"images": false}}} {
		g.set(c.n, issue("I_kwDOresend"+strconv.Itoa(c.n), "Before", t0))
		key := entityOf(t, e.importItem(t, "RIM", "https://github.com/octo/app/issues/"+strconv.Itoa(c.n), opID(), http.StatusCreated, map[string]any{"images": false}), "ticket")["key"].(string)
		g.edit(c.n, func(i *ghItem) { i.Title, i.Updated = "After", t0.Add(time.Hour) })
		op := opID()
		body := map[string]any{"opId": op}
		for k, v := range c.first {
			body[k] = v
		}
		if c.cut {
			e.srv.external.cut = func(at string) bool { return at == "ticket" }
		}
		downloads := g.count(imageHost, "/1/")
		e.call(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", body)
		e.srv.external.cut = nil
		if v, _ := e.srv.dispatch.Ticket(t.Context(), key); v.Title != "After" || g.count(imageHost, "/1/") != downloads {
			t.Fatalf("%d: the first send: %q, %d downloads", c.n, v.Title, g.count(imageHost, "/1/")-downloads)
		}
		out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/refresh", map[string]any{"opId": op}, http.StatusOK, "")
		ims := imagesOf(t, out)
		if out["changed"] != true || rcOf(out)["opId"] != op || rcOf(out)["repeat"] != true || ims[pngID]["attachmentId"] == nil || ims[svgID]["skipped"] != "type" {
			t.Fatalf("%d: the resend: %v", c.n, out)
		}
	}
}

// pullEnv is externalEnv with a project STP, a ticket of it, and pull
// request n on the fake.
func pullEnv(t *testing.T, n int) (*testEnv, *ghItems, string) {
	t.Helper()
	e, g := externalEnv(t)
	g.set(n, ghItem{Node: "PR_kwDOstop" + strconv.Itoa(n), Title: "Stop", Pull: true, Updated: t0})
	e.newDispatchProject(t, "STP", nil)
	return e, g, e.newDispatchTicket(t, "STP", "Stop it", nil)["key"].(string)
}

// causeOf is a transport to the fake that tells, for a request to path
// that fails, what ended its context (context.Cause): the stop's cancel,
// or the adapter's own deadline.
type causeOf struct {
	base  http.RoundTripper
	path  string
	cause chan<- error
}

func (c causeOf) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if err != nil && r.URL.Path == c.path {
		select {
		case c.cause <- context.Cause(r.Context()):
		default:
		}
	}
	return resp, err
}

// addLink adds a delivery link to pull request n of octo/app: its id.
func (e *testEnv) addPullLink(t *testing.T, key string, n int) string {
	t.Helper()
	out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "delivery", "url": "https://github.com/octo/app/pull/" + strconv.Itoa(n)}, http.StatusCreated, "")
	return rcOf(out)["id"].(string)
}

// The reads of GitHub end with the server. The read after a link's add,
// held at the fake when the server begins to stop, asks nothing more (the
// pull request's half), records nothing, and the stop does not wait for
// it; a link added once the stop began asks GitHub nothing; a route's read
// held the same way, with the server's caps far above the test's wait,
// ends with the stop (its request ended by the stop's cancel, not by a
// deadline), answered as a busy route is. Once
// the stop has begun, the one door to GitHub asks nothing, and 2,000
// previews and 500 imports on one processor make no request, each held to
// its own route's answer once the stop ended it (stopAnswer): a preview
// busy, an import the store's failure; routes begun inside the server's
// real shutdown, held at its seam after the runs' stops, are answered so
// and ask nothing.
func TestTrackerReadsEndWithServer(t *testing.T) {
	// stopAnswer is the answer of a route the stop ended, by its path: a
	// preview's is the busy answer, an import's the store's failure for its
	// call to the store, which the stop ended.
	stopAnswer := func(path string, out map[string]any) bool {
		if strings.HasSuffix(path, "/import") {
			return errorCode(out) == "store_failed"
		}
		return errorCode(out) == "tracker_busy"
	}
	t.Run("the read after a link's add", func(t *testing.T) {
		e, g, key := pullEnv(t, 92)
		gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/92")
		link := e.addPullLink(t, key, 92)
		<-gate.Reached()
		wctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		e.srv.WaitBackground(wctx)
		stoppedFirst := wctx.Err() == nil
		gate.Open()
		e.srv.bg.Wait()
		ds, err := dispatch.OpenOptions(filepath.Join(e.srv.store.Dir(), "dispatch"), dispatch.Options{Systems: []string{"github"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ds.Close()
		_, found, err := ds.LinkReadOf(t.Context(), link)
		if !stoppedFirst || g.count("api.github.com", itemsRepo+"/pulls/92") != 0 || found || err != nil {
			t.Fatalf("after the stop: it ended first %v, the pull request asked %d times, a read recorded %v %v", stoppedFirst, g.count("api.github.com", itemsRepo+"/pulls/92"), found, err)
		}
	})
	t.Run("a link added once the stop began", func(t *testing.T) {
		e, g, key := pullEnv(t, 93)
		e.srv.external.end()
		before := len(g.f.Requests())
		e.addPullLink(t, key, 93)
		// What the add started is waited for: the stop's own wait.
		wctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		e.srv.WaitBackground(wctx)
		if n := len(g.f.Requests()) - before; n != 0 || wctx.Err() != nil {
			t.Fatalf("%d requests after the stop began (the wait ended on its own: %v)", n, wctx.Err() != nil)
		}
	})
	t.Run("a route's read", func(t *testing.T) {
		e, g, key := pullEnv(t, 94)
		// The server's own caps far above the test's wait, and the held
		// request's end told by its cause (the adapter's own deadline is
		// below the test's wait): only the stop ends the route here.
		far := 10 * testWait
		e.srv.external.caps = &trackerCaps{busy: far, reads: far, images: far, merged: far, write: far}
		held := make(chan error, 1)
		e.srv.trackers.gh = e.srv.trackers.adapter(causeOf{g.f.Transport(), itemsRepo + "/issues/94", held})
		link := e.addPullLink(t, key, 94)
		if !waitFor(testWait, func() bool { _, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link); return ok }) {
			t.Fatal("the added link was not read")
		}
		gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/94")
		answer := make(chan map[string]any, 1)
		go func() {
			_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
			answer <- out
		}()
		<-gate.Reached()
		stopped := make(chan struct{})
		go func() { e.srv.WaitBackground(context.Background()); close(stopped) }()
		var out map[string]any
		select {
		case out = <-answer:
		case <-time.After(testWait):
			gate.Open()
			t.Fatal("the held route did not end with the stop")
		}
		gate.Open()
		<-stopped
		var cause error
		select {
		case cause = <-held:
		default:
		}
		if cause != context.Canceled || errorCode(out) != "tracker_busy" || g.count("api.github.com", itemsRepo+"/pulls/94") != 1 {
			t.Fatalf("the held route: %v, its request ended by %v, the pull request asked %d times", out, cause, g.count("api.github.com", itemsRepo+"/pulls/94"))
		}
	})
	t.Run("routes begun once the stop began", func(t *testing.T) {
		e, g, _ := pullEnv(t, 95)
		e.newDispatchProject(t, "BGN", nil)
		preview := map[string]any{"url": "https://github.com/octo/app/issues/95"}
		// The connection to the fake made before the stop, so a request
		// after it would be quick to go.
		e.expect(t, "POST", "/api/dispatch/external/preview", preview, http.StatusOK, "")
		e.srv.external.end()
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
		before := len(g.f.Requests())
		asked := false
		if err := e.srv.atGitHub(context.Background(), tracker.Ref{System: tracker.GitHub, Host: tracker.GitHubHost, Space: "octo/app", Kind: tracker.KindIssue, ID: "95"},
			func(tracker.Adapter, tracker.Credential) error { asked = true; return nil }); err != errItemBusy || asked {
			t.Fatalf("the one door after the stop: %v, asked %v", err, asked)
		}
		ended := 0
		for i := range 2500 {
			path, body := "/api/dispatch/external/preview", preview
			if i >= 2000 {
				path, body = "/api/dispatch/projects/BGN/import", map[string]any{"opId": opID(), "url": preview["url"]}
			}
			if _, _, out := e.call(t, "POST", path, body); stopAnswer(path, out) {
				ended++
			}
		}
		if n := len(g.f.Requests()) - before; n != 0 || ended != 2500 {
			t.Fatalf("2,000 previews and 500 imports once the stop began: %d requests, %d answered as ended by the stop", n, ended)
		}
	})
	t.Run("a route begun inside the shutdown", func(t *testing.T) {
		e, g, _ := pullEnv(t, 99)
		e.newDispatchProject(t, "SHD", nil)
		var answers []map[string]any
		var asked int
		routes := []struct {
			path string
			body map[string]any
		}{
			{"/api/dispatch/external/preview", map[string]any{"url": "https://github.com/octo/app/issues/99"}},
			{"/api/dispatch/projects/SHD/import", map[string]any{"opId": opID(), "url": "https://github.com/octo/app/issues/99"}},
		}
		e.srv.launches.setSeams(func(gate *launchGate) {
			gate.afterStops = func() {
				before := len(g.f.Requests())
				for _, c := range routes {
					_, _, out := e.call(t, "POST", c.path, c.body)
					answers = append(answers, out)
				}
				asked = len(g.f.Requests()) - before
			}
		})
		e.srv.Shutdown(context.Background())
		if len(answers) != 2 || !stopAnswer(routes[0].path, answers[0]) || !stopAnswer(routes[1].path, answers[1]) || asked != 0 {
			t.Fatalf("routes begun inside the shutdown: %v, %d requests", answers, asked)
		}
	})
}

// Once the server has begun to stop, the stop reaches a close under merged
// and nothing else of an edit: an edit to done in a project without merged
// is made as any edit is; one in a project with merged is a busy route's
// 503, nothing closed, nothing asked.
func TestCloseOnceTheStopBegan(t *testing.T) {
	e, g, merged := mergedEnv(t, nil)
	e.newDispatchProject(t, "NMG", map[string]any{"closeRequires": []string{}})
	plain := e.newDispatchTicket(t, "NMG", "Close it", nil)["key"].(string)
	key, _ := merged(99)
	e.srv.external.end()
	before := len(g.f.Requests())
	status, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+plain+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}})
	if status != http.StatusOK || entityOf(t, out, "ticket")["status"] != "done" {
		t.Fatalf("an edit to done without merged once the stop began: %d %v", status, out)
	}
	status, _, out = e.call(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}})
	tv, _ := e.srv.dispatch.Ticket(t.Context(), key)
	if status != http.StatusServiceUnavailable || errorCode(out) != "tracker_busy" || tv.Status == dispatch.TicketDone || len(g.f.Requests()) != before {
		t.Fatalf("an edit to done under merged once the stop began: %d %v, the ticket %s, %d requests", status, out, tv.Status, len(g.f.Requests())-before)
	}
}

// The route's look at the project before a close does not depend on the
// client staying: an edit to done under merged whose request has ended
// before the handler runs reads the delivery link again, and that read
// failing refuses the close naming the link, as for a client that stays.
func TestCloseLookWithoutTheClient(t *testing.T) {
	e, g, merged := mergedEnv(t, nil)
	key, link := merged(88)
	g.edit(88, func(i *ghItem) { i.Fail = http.StatusTooManyRequests })
	reads := g.count("api.github.com", itemsRepo+"/issues/88")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := fmt.Sprintf(`{"opId":%q,"set":{"status":"done"}}`, opID())
	r := httptest.NewRequestWithContext(ctx, "POST", "/api/dispatch/tickets/"+key+"/edit", strings.NewReader(body))
	r.SetPathValue("key", key)
	w := httptest.NewRecorder()
	e.srv.handleDispatchEditTicket(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusConflict {
		t.Fatalf("the close with its client gone: %d %s", w.Code, w.Body.String())
	}
	closeRefused(t, e, "the close with its client gone", key, out, "octo/app#88")
	if rd, _, err := e.srv.dispatch.LinkReadOf(t.Context(), link); err != nil || rd.Error == "" || g.count("api.github.com", itemsRepo+"/issues/88") != reads+1 {
		t.Fatalf("the close's read: %+v %v, the item asked %d times", rd, err, g.count("api.github.com", itemsRepo+"/issues/88")-reads)
	}
}

// A route's context ends with the stop itself, in the stop's own cancel,
// and keeps the request's values: one made before the stop has ended once
// the stop has returned, and one made after it has ended already.
func TestTrackerContextEndsWithTheStop(t *testing.T) {
	e := newTestEnv(t, nil)
	type key struct{}
	r := httptest.NewRequestWithContext(context.WithValue(context.Background(), key{}, "the request's"), "POST", "/", nil)
	before, cancel := e.srv.trackerContext(r, time.Hour)
	defer cancel()
	e.srv.external.end()
	after, cancelAfter := e.srv.trackerContext(r, time.Hour)
	defer cancelAfter()
	for what, ctx := range map[string]context.Context{"made before the stop": before, "made after it": after} {
		if ctx.Err() != context.Canceled || ctx.Value(key{}) != "the request's" {
			t.Errorf("a route's context %s, once the stop returned: %v, the request's value %v", what, ctx.Err(), ctx.Value(key{}))
		}
	}
}

// The shared error mapper answers a store call ended by a canceled request
// as it does on main whether or not the server has begun to stop: an
// ordinary Dispatch read whose request is canceled answers the same before
// and after the stop.
func TestDispatchReadCanceledAtTheStop(t *testing.T) {
	e, _ := externalEnv(t)
	e.newDispatchProject(t, "CNL", nil)
	key := e.newDispatchTicket(t, "CNL", "Read it", nil)["key"].(string)
	read := func() (int, string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := httptest.NewRequestWithContext(ctx, "GET", "/api/dispatch/tickets/"+key, nil)
		r.SetPathValue("key", key)
		w := httptest.NewRecorder()
		e.srv.handleDispatchTicket(w, r)
		return w.Code, w.Body.String()
	}
	status, body := read()
	e.srv.external.end()
	if after, abody := read(); after != status || abody != body || status != http.StatusInternalServerError || !strings.Contains(body, "store_failed") {
		t.Fatalf("a canceled read: %d %s before the stop, %d %s after", status, body, after, abody)
	}
}

// mergedEnv is a server whose project MLR requires merged, with caps
// when given; merged makes a ticket of it with a delivery link to pull
// request n, read merged after the add (its key and its link's id).
func mergedEnv(t *testing.T, caps *trackerCaps) (e *testEnv, g *ghItems, merged func(n int) (key, link string)) {
	t.Helper()
	e, g = externalEnv(t)
	e.srv.external.caps = caps
	e.newDispatchProject(t, "MLR", map[string]any{"closeRequires": []string{"merged"}})
	return e, g, func(n int) (key, link string) {
		t.Helper()
		g.set(n, ghItem{Node: "PR_kwDOlatest" + strconv.Itoa(n), Title: "Merged", Pull: true, Merged: true, State: "closed", Updated: t0})
		key = e.newDispatchTicket(t, "MLR", "Close it", nil)["key"].(string)
		link = e.addPullLink(t, key, n)
		if !waitFor(testWait, func() bool {
			r, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link)
			return ok && r.Merged && r.Error == ""
		}) {
			t.Fatalf("%d was not read merged", n)
		}
		return key, link
	}
}

// closeIt sends key's edit to done, expecting status.
func (e *testEnv) closeIt(t *testing.T, key string, status int) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}, status, "")
}

// closeRefused fails unless out refuses a close in words holding names
// and the ticket is not done.
func closeRefused(t *testing.T, e *testEnv, what, key string, out map[string]any, names ...string) {
	t.Helper()
	tv, _ := e.srv.dispatch.Ticket(t.Context(), key)
	if errorCode(out) != "close_policy" || tv.Status == dispatch.TicketDone {
		t.Fatalf("%s: %v, the ticket %s", what, out, tv.Status)
	}
	for _, n := range names {
		if !strings.Contains(apiMessage(out), n) {
			t.Fatalf("%s: %v does not name %s", what, out, n)
		}
	}
}

// The close under merged counts a delivery's read only while it is the
// link's latest, and the route reads each delivery link again before a
// close it sees: a link read merged,
// then read as another item (moved), or not read for a rate limit, is
// refused at the close inside the five minutes, naming the link, until a
// read succeeds again; with the token removed after a merged read, the
// close is refused naming the link, nothing closed, and closes once a
// token is kept again; a read the store's check refuses is recorded as
// failed and refuses the close; once the server has begun to stop, the
// close is a busy route's answer, nothing closed.
func TestCloseMergedLatestRead(t *testing.T) {
	e, g, merged := mergedEnv(t, nil)
	key, link := merged(96)
	g.edit(96, func(i *ghItem) { i.Node, i.Updated = "PR_kwDOanother96", t0.Add(time.Hour) })
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{}, http.StatusConflict, "moved")
	closeRefused(t, e, "read as another item", key, e.closeIt(t, key, http.StatusConflict), "octo/app#96")

	key, link = merged(97)
	g.edit(97, func(i *ghItem) { i.Fail = http.StatusTooManyRequests })
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{}, http.StatusTooManyRequests, "rate_limited")
	closeRefused(t, e, "not read for a rate limit", key, e.closeIt(t, key, http.StatusConflict), "octo/app#97")
	g.edit(97, func(i *ghItem) { i.Fail = 0 })
	if tk := entityOf(t, e.closeIt(t, key, http.StatusOK), "ticket"); tk["status"] != "done" {
		t.Fatalf("read again: %v", tk)
	}

	key, _ = merged(98)
	e.expect(t, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}, http.StatusOK, "")
	closeRefused(t, e, "the token removed", key, e.closeIt(t, key, http.StatusConflict), "octo/app#98")
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token again: %d %s", status, data)
	}
	if tk := entityOf(t, e.closeIt(t, key, http.StatusOK), "ticket"); tk["status"] != "done" {
		t.Fatalf("with a token again: %v", tk)
	}

	key, link = merged(90)
	g.edit(90, func(i *ghItem) { i.Updated = time.Time{} })
	closeRefused(t, e, "a read the store's check refuses", key, e.closeIt(t, key, http.StatusConflict), "octo/app#90")
	if r, _, err := e.srv.dispatch.LinkReadOf(t.Context(), link); err != nil || !strings.Contains(r.Error, "could not be kept") {
		t.Fatalf("the refused read was not recorded as failed: %+v %v", r, err)
	}

	key, _ = merged(89)
	e.srv.external.end()
	out := e.closeIt(t, key, http.StatusServiceUnavailable)
	if tv, _ := e.srv.dispatch.Ticket(t.Context(), key); errorCode(out) != "tracker_busy" || tv.Status == dispatch.TicketDone {
		t.Fatalf("a close once the stop began: %v, the ticket %s", out, tv.Status)
	}
}

// With the close's caps short, and only here: the close's read held to its
// cap at the fake has its failure recorded and refuses the close; the
// link's lock held by a link's read (held at the fake) past the close's
// wait refuses the close naming the link, nothing closed, the read kept
// from before not counted.
func TestCloseMergedCapped(t *testing.T) {
	e, g, merged := mergedEnv(t, &trackerCaps{busy: 300 * time.Millisecond, reads: testWait, images: testWait, merged: 300 * time.Millisecond, write: testWait})
	key, link := merged(98)
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/98")
	closeRefused(t, e, "the close's read held to its cap", key, e.closeIt(t, key, http.StatusConflict), "octo/app#98")
	gate.Open()
	if r, _, err := e.srv.dispatch.LinkReadOf(t.Context(), link); err != nil || r.Error == "" {
		t.Fatalf("the held read's failure was not recorded: %+v %v", r, err)
	}

	key, link = merged(99)
	gate = g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/99")
	held := make(chan struct{})
	go func() {
		defer close(held)
		e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
	}()
	<-gate.Reached()
	closeRefused(t, e, "the link's lock held past the wait", key, e.closeIt(t, key, http.StatusConflict), "octo/app#99")
	gate.Open()
	<-held
}

// The close's reads are made only for an edit the store runs as a close:
// a resend of a committed edit to done, an edit to done of a ticket done
// already and one with a stale revision ask GitHub nothing, each answered
// as the store answers it.
func TestCloseReadsOnlyForAClose(t *testing.T) {
	e, g, merged := mergedEnv(t, nil)
	key, _ := merged(91)
	pulls := func() int { return g.count("api.github.com", itemsRepo+"/pulls/91") }
	op := opID()
	body := map[string]any{"opId": op, "set": map[string]any{"status": "done"}}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", body, http.StatusOK, "")
	before := pulls()
	if out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", body, http.StatusOK, ""); rcOf(out)["repeat"] != true || pulls() != before {
		t.Fatalf("a resend: %v, %d reads", out, pulls()-before)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done", "priority": 2}}, http.StatusOK, "")
	if pulls() != before {
		t.Fatalf("an edit of a ticket done already: %d reads", pulls()-before)
	}
	key, _ = merged(92)
	before = g.count("api.github.com", itemsRepo+"/pulls/92")
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "expectedRev": 99, "set": map[string]any{"status": "done"}}, http.StatusConflict, "stale_revision")
	if n := g.count("api.github.com", itemsRepo+"/pulls/92") - before; n != 0 {
		t.Fatalf("a stale edit: %d reads", n)
	}
}

// A deleted ticket is asked nothing and records nothing: a link's read, a
// refresh, a field given back to GitHub, a link and an edit to done under
// merged each answer the one 404 and make no request; a link's read held
// at the fake while its ticket is deleted records nothing.
func TestDeletedTicketAskedNothing(t *testing.T) {
	e, g, merged := mergedEnv(t, nil)
	g.set(93, issue("I_kwDOdeleted93", "Deleted", t0))
	key := entityOf(t, e.importItem(t, "MLR", "https://github.com/octo/app/issues/93", opID(), http.StatusCreated, map[string]any{"images": false}), "ticket")["key"].(string)
	g.set(94, ghItem{Node: "PR_kwDOdeleted94", Title: "Merged", Pull: true, Merged: true, State: "closed", Updated: t0})
	link := e.addPullLink(t, key, 94)
	if !waitFor(testWait, func() bool { _, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link); return ok }) {
		t.Fatal("the link was not read")
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	before := len(g.f.Requests())
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/links/" + link + "/read", map[string]any{}},
		{"/external/refresh", map[string]any{"opId": opID()}},
		{"/external/authority", map[string]any{"opId": opID(), "field": "title", "owner": "github"}},
		{"/external", map[string]any{"opId": opID(), "url": "https://github.com/octo/app/issues/95"}},
		{"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}},
	} {
		if status, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+c.path, c.body); status != http.StatusNotFound || errorCode(out) != "not_found" || !strings.Contains(apiMessage(out), key+" is deleted") {
			t.Errorf("%s of a deleted ticket: %d %v", c.path, status, out)
		}
	}
	if n := len(g.f.Requests()) - before; n != 0 {
		t.Fatalf("a deleted ticket's routes made %d requests", n)
	}

	key, link = merged(96)
	prior, _, _ := e.srv.dispatch.LinkReadOf(t.Context(), link)
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/96")
	answer := make(chan int, 1)
	go func() {
		status, _, _ := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
		answer <- status
	}()
	<-gate.Reached()
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	gate.Open()
	status := <-answer
	if r, _, err := e.srv.dispatch.LinkReadOf(t.Context(), link); err != nil || !r.ReadAt.Equal(prior.ReadAt) || r.Error != prior.Error || status != http.StatusNotFound {
		t.Fatalf("a read held across its ticket's delete: %d, %+v, kept %+v", status, r, prior)
	}
}

// A read that waited for its link's lock while its ticket was deleted asks
// nothing: it loads the ticket under the lock (ticketFor) before any
// request. A link's read behind the read after a link's add answers the
// deleted ticket's 404, the read after a second link's add, queued behind
// the first, asks nothing, and a close under merged behind a link's read
// asks nothing and refuses, nothing closed.
func TestDeletedWhileTheReadWaits(t *testing.T) {
	e, g, key := pullEnv(t, 92)
	g.set(97, ghItem{Node: "PR_kwDOstop97", Title: "Stop", Pull: true, Updated: t0})
	waits := heldWaits(e)
	// heldOn waits until a lock named want is found held by a waiter.
	heldOn := func(want string) {
		t.Helper()
		for {
			select {
			case got := <-waits:
				if got == want {
					return
				}
			case <-time.After(testWait):
				t.Fatalf("no wait on %s", want)
			}
		}
	}
	gate := g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/92")
	link := e.addPullLink(t, key, 92)
	<-gate.Reached()
	answer := make(chan map[string]any, 1)
	go func() {
		_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
		answer <- out
	}()
	heldOn("link:" + link)
	link2 := e.addPullLink(t, key, 97)
	heldOn("added:" + key)
	// The second link's own lock held here, so its read, once it holds the
	// ticket's "added:" lock, is seen waiting.
	release2, ok := e.srv.external.lock(t.Context(), "link:"+link2, testWait)
	if !ok {
		t.Fatal("the second link's lock")
	}
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	gate.Open()
	out := <-answer
	heldOn("link:" + link2)
	release2()
	// The "added:" lock is the second read's until it has ended.
	ended, ok := e.srv.external.lock(t.Context(), "added:"+key, testWait)
	if !ok {
		t.Fatal("the second link's read did not end")
	}
	ended()
	_, kept, _ := e.srv.dispatch.LinkReadOf(t.Context(), link2)
	if n, n2 := g.count("api.github.com", itemsRepo+"/issues/92"), g.count("api.github.com", itemsRepo+"/issues/97"); errorCode(out) != "not_found" || !strings.Contains(apiMessage(out), key+" is deleted") || n != 1 || n2 != 0 || kept {
		t.Fatalf("deleted while the reads waited: the link's read %v, 92 asked %d times, 97 asked %d, the second link's read kept %v", out, n, n2, kept)
	}

	e, g, merged := mergedEnv(t, nil)
	waits = heldWaits(e)
	key, link = merged(98)
	gate = g.f.Hold("GET", "api.github.com", itemsRepo+"/issues/98")
	go func() {
		_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
		answer <- out
	}()
	<-gate.Reached()
	// The waits seen before the link's read held its lock are not the
	// close's.
	for len(waits) > 0 {
		<-waits
	}
	closed := make(chan map[string]any, 1)
	go func() {
		_, _, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}})
		closed <- out
	}()
	heldOn("link:" + link)
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	asked := g.count("api.github.com", itemsRepo+"/issues/98")
	gate.Open()
	<-answer
	out = <-closed
	tv, _ := e.srv.dispatch.Ticket(t.Context(), key)
	if n := g.count("api.github.com", itemsRepo+"/issues/98"); errorCode(out) != "close_policy" || tv.Status == dispatch.TicketDone || n != asked {
		t.Fatalf("a close behind a link's read, its ticket deleted: %v, the ticket %s, 98 asked %d more times", out, tv.Status, n-asked)
	}
}

// keptBounds is the bound of each kind of text a ticket's GitHub item
// keeps, by its field's name in the answers.
var keptBounds = map[string]int{"title": dispatch.MaxSnapshotTitle, "labels": dispatch.MaxSnapshotLabel, "assignees": dispatch.MaxSnapshotLogin,
	"author": dispatch.MaxSnapshotLogin, "milestone": dispatch.MaxSnapshotName, "baseRef": dispatch.MaxSnapshotName, "name": dispatch.MaxCheckName,
	"app": dispatch.MaxCheckName, "status": 40, "conclusion": 40, "words": dispatch.MaxIncompleteWord}

// keptTexts checks every text of an answer: no control character (a
// description's and steps' lines and tabs aside) and no tag character, none of one line with a
// character that reorders a line, none past its field's bound; the
// snapshot's JSON text is read as its own.
func keptTexts(t *testing.T, what string, v any, key string) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			keptTexts(t, what, e, k)
		}
	case []any:
		for _, e := range x {
			keptTexts(t, what, e, key)
		}
	case string:
		if key == "externalSnapshot" {
			var sn any
			if json.Unmarshal([]byte(x), &sn) == nil {
				keptTexts(t, what, sn, "")
			}
			return
		}
		lines := key == "description" || key == "repro"
		hidden := func(r rune) bool {
			return unicode.IsControl(r) && !(lines && (r == '\n' || r == '\t')) || r >= 0xe0000 && r <= 0xe007f
		}
		if strings.ContainsFunc(x, hidden) ||
			!lines && strings.ContainsFunc(x, func(r rune) bool { return r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 }) {
			t.Errorf("%s: %s holds %.40q", what, key, x)
		}
		if n, ok := keptBounds[key]; ok && len(x) > n {
			t.Errorf("%s: %s is %d bytes, past %d", what, key, len(x), n)
		}
	}
}

// Each text GitHub writes is made storable where a read becomes the
// store's record: with each hostile filler (poisons: a control character,
// a reordering character, quotes, backslashes, short scheme:// tokens, a
// tag character, a value past every one-line bound), alone and all
// together, after every text of the fake's answers (a walk over them, so a
// field added later is in it; the fields of a fixed form aside), and every
// listing the adapter reads served (eight workflow runs whose job listings
// are cut among them), the preview answers, every route that records a
// read records it (the import, a refresh, a field given back, a link, the
// read after a link's add and a link's read, its reasons cut to the
// store's bounds), and nothing kept or shown holds a control or tag
// character, nothing of one line a reordering character, nothing is past
// its bound.
func TestOutsideTextStorable(t *testing.T) {
	e, g := externalEnv(t)
	g.image("/1/a.png", []byte(pngBytes))
	g.image("/1/b.svg", []byte(svgBytes))
	e.newDispatchProject(t, "PSN", nil)
	n := 200
	for _, what := range slices.Sorted(maps.Keys(poisons)) {
		g.mu.Lock()
		g.poison = poisons[what]
		g.mu.Unlock()
		n += 2
		issueN, pullN := strconv.Itoa(n), strconv.Itoa(n+1)
		g.set(n, issue("I_kwDOpoison"+issueN, "Poisoned", t0))
		g.set(n+1, ghItem{Node: "PR_kwDOpoison" + pullN, Title: "Poisoned change", Pull: true, Updated: t0})
		pv := e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "https://github.com/octo/app/issues/" + issueN}, http.StatusOK, "")
		keptTexts(t, what+": the preview", pv, "")
		key := entityOf(t, e.importItem(t, "PSN", "https://github.com/octo/app/issues/"+issueN, opID(), http.StatusCreated, nil), "ticket")["key"].(string)
		g.edit(n, func(i *ghItem) { i.Title, i.Updated = "Poisoned again", t0.Add(time.Hour) })
		if out := e.refresh(t, key, http.StatusOK, nil); out["changed"] != true {
			t.Fatalf("%s: the refresh: %v", what, out)
		}
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "field": "title", "owner": "dispatch"}, http.StatusOK, "")
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/external/authority", map[string]any{"opId": opID(), "field": "title", "owner": "github"}, http.StatusOK, "")
		own := e.newDispatchTicket(t, "PSN", "Own", nil)["key"].(string)
		e.expect(t, "POST", "/api/dispatch/tickets/"+own+"/external", map[string]any{"opId": opID(), "url": "octo/app#" + pullN}, http.StatusOK, "")
		link := e.addPullLink(t, key, n+1)
		if !waitFor(testWait, func() bool { r, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link); return ok && r.Error == "" }) {
			r, _, _ := e.srv.dispatch.LinkReadOf(t.Context(), link)
			t.Fatalf("%s: the read after the link's add was not recorded: %+v", what, r)
		}
		read := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{}, http.StatusOK, ""), "read")
		if checks, _ := read["checks"].([]any); len(checks) != 10 || read["incomplete"] == nil {
			t.Fatalf("%s: the link's read: %v", what, read)
		}
		for _, k := range []string{key, own} {
			tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+k, nil, http.StatusOK, ""), "ticket")
			keptTexts(t, what+": "+k, tk, "")
			var sn dispatch.ExternalSnapshot
			_ = json.Unmarshal([]byte(snapText(t, tk["externalSnapshot"])), &sn)
			if len(sn.Labels) != 1 || !strings.HasPrefix(sn.Labels[0], "bug") || !strings.HasPrefix(sn.Author, "mona") {
				t.Fatalf("%s: %s: the snapshot does not hold the texts read: %+v", what, k, sn)
			}
		}
		keptTexts(t, what+": the link's read", e.expect(t, "GET", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", nil, http.StatusOK, ""), "")
	}
}

// What the import keeps is what is counted and shown: an issue of 51
// labels keeps 20 and counts 51; the preview of an issue of nine images,
// one of them past an image id's bound, lists what the import then keeps;
// and the preview names the ticket that has the item as its linked issue,
// though eight tickets before it hold its address as an ordinary link.
func TestImportKeepsWhatIsShown(t *testing.T) {
	e, g := externalEnv(t)
	e.newDispatchProject(t, "KPS", nil)
	labels := []string{}
	for i := range 51 {
		labels = append(labels, fmt.Sprintf("label-%02d", i))
	}
	it := issue("I_kwDOkeeps152", "Labels", t0)
	it.Labels, it.Images = labels, nil
	g.set(152, it)
	tk := entityOf(t, e.importItem(t, "KPS", "https://github.com/octo/app/issues/152", opID(), http.StatusCreated, nil), "ticket")
	var sn dispatch.ExternalSnapshot
	_ = json.Unmarshal([]byte(snapText(t, tk["externalSnapshot"])), &sn)
	if len(sn.Labels) != dispatch.MaxSnapshotLabels || sn.LabelsTotal != 51 {
		t.Fatalf("51 labels: %d kept, %d counted", len(sn.Labels), sn.LabelsTotal)
	}

	it = issue("I_kwDOkeeps153", "Images", t0)
	it.Images = []string{"/9/" + strings.Repeat("l", dispatch.MaxImageID) + ".png"}
	for i := range 8 {
		it.Images = append(it.Images, fmt.Sprintf("/9/i%d.png", i))
	}
	g.set(153, it)
	pv := entityOf(t, e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": "https://github.com/octo/app/issues/153"}, http.StatusOK, ""), "preview")
	out := e.importItem(t, "KPS", "https://github.com/octo/app/issues/153", opID(), http.StatusCreated, map[string]any{"images": false})
	var shown, kept []string
	for _, id := range pv["images"].([]any) {
		shown = append(shown, id.(string))
	}
	for _, im := range out["images"].([]any) {
		kept = append(kept, im.(map[string]any)["id"].(string))
	}
	if !slices.Equal(shown, kept) || len(kept) != dispatch.MaxImportImages {
		t.Fatalf("the preview shows %d images, the import keeps %d: %v, %v", len(shown), len(kept), shown, kept)
	}

	g.set(154, ghItem{Node: "I_kwDOkeeps154", Title: "Linked", Updated: t0})
	page := "https://github.com/octo/app/issues/154"
	for range 8 {
		k := e.newDispatchTicket(t, "KPS", "Mentions it", nil)["key"].(string)
		e.expect(t, "POST", "/api/dispatch/tickets/"+k+"/links", map[string]any{"opId": opID(), "kind": "external", "url": page}, http.StatusCreated, "")
	}
	linked := entityOf(t, e.importItem(t, "KPS", page, opID(), http.StatusCreated, nil), "ticket")["key"].(string)
	if pv := entityOf(t, e.expect(t, "POST", "/api/dispatch/external/preview", map[string]any{"url": page}, http.StatusOK, ""), "preview"); pv["linked"] != linked {
		t.Fatalf("the preview names %v, not %s", pv["linked"], linked)
	}
}

// A link's checks are fitted as the store measures them, where the read
// becomes the store's record. A hundred statuses at a pull request's head
// whose names each hold eighteen short scheme tokens (each kept as words,
// so a name is longer kept than read), with a 72-byte page: the adapter
// cuts them to 32 KiB by its measure, and the store's measure of what it
// kept is past the bound; the read is recorded, cut again to the store's
// bound, failures first, both cuts said and every check left out counted.
// A hundred with plain names fit both measures and are recorded whole. A
// hundred failures past the bound are recorded as the failures that fit,
// the cut said and counted.
func TestLinkChecksFitAsKept(t *testing.T) {
	scheme := strings.Repeat("x:// ", 18)
	for _, c := range []struct {
		what   string
		name   string
		failed func(i int) bool
		whole  bool
	}{
		{"names longer as kept", scheme, func(i int) bool { return i%5 == 0 }, false},
		{"within both measures", "lint ", func(i int) bool { return i%5 == 0 }, true},
		{"failures alone past the bound", scheme, func(int) bool { return true }, false},
	} {
		t.Run(c.what, func(t *testing.T) {
			e, g, key := pullEnv(t, 87)
			failures := 0
			var statuses []any
			for i := range 100 {
				state := "success"
				if c.failed(i) {
					state, failures = "failure", failures+1
				}
				statuses = append(statuses, map[string]any{"id": 7000 + i, "context": fmt.Sprintf("%ss%02d", c.name, i), "state": state,
					"target_url": fmt.Sprintf("https://ci.example.com/octo/app/commit-statuses/%024d", i), "created_at": itemsLayout, "updated_at": "2026-11-01T10:00:00Z"})
			}
			g.f.Reply("GET", "api.github.com", itemsRepo+"/commits/"+headSHA+"/status", http.StatusOK, map[string]any{"state": "failure", "total_count": 100, "statuses": statuses})
			link := e.addPullLink(t, key, 87)
			if !waitFor(testWait, func() bool { _, ok, _ := e.srv.dispatch.LinkReadOf(t.Context(), link); return ok }) {
				t.Fatal("the added link was not read")
			}
			status, data, out := e.call(t, "POST", "/api/dispatch/tickets/"+key+"/links/"+link+"/read", map[string]any{})
			if status != http.StatusOK {
				t.Fatalf("the link's read: %d %s", status, data)
			}
			read, _, err := e.srv.dispatch.LinkReadOf(t.Context(), link)
			if err != nil || read.Error != "" || len(entityOf(t, out, "read")["checks"].([]any)) != len(read.Checks) {
				t.Fatalf("the read kept: %+v %v", read, err)
			}
			if c.whole {
				if len(read.Checks) != 100 || read.Incomplete != nil {
					t.Fatalf("within both measures: %d checks kept, incomplete %+v", len(read.Checks), read.Incomplete)
				}
				return
			}
			if read.Incomplete == nil || read.Incomplete.Omitted != 100-len(read.Checks) {
				t.Fatalf("cut: %d checks kept, incomplete %+v", len(read.Checks), read.Incomplete)
			}
			// The adapter's cut, by its own measure, and the store's after it.
			adapter, stored := 0, false
			for _, w := range read.Incomplete.Words {
				if rest, ok := strings.CutPrefix(w, "the checks' list is past 32 KiB; "); ok {
					_, _ = fmt.Sscan(rest, &adapter)
				}
				stored = stored || strings.HasSuffix(w, " checks left out to keep the list within 100 checks and 32 KiB, failures kept first")
			}
			if adapter <= 0 || !stored || len(read.Checks) >= 100-adapter {
				t.Fatalf("the cuts said: %q, %d kept", read.Incomplete.Words, len(read.Checks))
			}
			kept := 0
			for i, ch := range read.Checks {
				if ch.Conclusion == "failure" {
					kept++
					if kept != i+1 {
						t.Fatalf("check %d, a failure, after one that is not: %+v", i, read.Checks)
					}
				}
			}
			if kept != min(failures, len(read.Checks)) {
				t.Fatalf("%d failures of %d kept in %d checks", kept, failures, len(read.Checks))
			}
		})
	}
}
