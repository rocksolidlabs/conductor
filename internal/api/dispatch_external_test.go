package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
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
	Huge                             bool // an answer past the adapter's bound
}

// ghItems scripts the fake's items and images.
type ghItems struct {
	f      *fakeGH
	mu     sync.Mutex
	items  map[int]*ghItem
	images map[string][]byte
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
	g.f.Reply("GET", "api.github.com", itemsRepo+"/commits/"+headSHA+"/status", http.StatusOK, map[string]any{"state": "success", "total_count": 1,
		"statuses": []any{map[string]any{"id": 31, "context": "lint", "state": "success", "target_url": "https://ci.example.com/31", "created_at": itemsLayout, "updated_at": "2026-11-01T10:00:00Z"}}})
	g.f.Reply("GET", "api.github.com", itemsRepo+"/actions/runs", http.StatusOK, map[string]any{"total_count": 0, "workflow_runs": []any{}})
	g.f.Reply("GET", "api.github.com", itemsRepo+"/commits/"+headSHA+"/check-runs", http.StatusOK, map[string]any{"total_count": 0, "check_runs": []any{}})
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
		v = map[string]any{"id": 5000000 + n, "node_id": it.Node, "number": n, "html_url": "https://github.com/octo/app/" + kind + "/" + num, "title": it.Title,
			"body": it.Body, "body_html": html.String(), "state": it.State, "state_reason": it.Reason, "labels": []any{map[string]any{"name": "bug"}},
			"assignees": []any{map[string]any{"login": "hubot"}}, "user": map[string]any{"login": "mona", "id": 9000001},
			"created_at": itemsLayout, "updated_at": it.Updated.UTC().Format(time.RFC3339)}
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
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
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
	// The second waits for the ticket's lock: GitHub saw one read of it.
	if !waitFor(testWait, func() bool { _, held := e.srv.external.heldKey("ticket:" + key); return held }) {
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
	out := e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "delivery", "url": "https://github.com/octo/app/pull/90"}, http.StatusCreated, "")
	link := rcOf(out)["id"].(string)
	if !waitNamed(t, events, "dispatch_read", link) {
		t.Fatal("no dispatch_read for the link's read")
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
		{MergedCloseTimeout, c.busy + c.merged + c.write},
		{TrackerReadTimeout, 90 * time.Second}, {ImportTimeout, 150 * time.Second}, {MergedCloseTimeout, 75 * time.Second},
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
// must give): a preview, an import with its images and its resend, a link,
// refreshes (unchanged, changed), a field taken over and given back, a
// link's add and its read, a close that reads its delivery, and the
// failures of GitHub's (each refusal, a 5xx, an answer past its bound), a
// request held at the fake and cancelled by its client, a reference out of
// form. The fake's items are octo/app's 140 to 143; the project DRV
// requires merged.
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
	do("POST /api/dispatch/tickets/{key}/edit", "/api/dispatch/tickets/"+key+"/edit", `{"opId":"`+opID()+`","set":{"status":"done"}}`, http.StatusOK, "")
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
