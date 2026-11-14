package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// The tests of a ticket's publications (dispatch_publish.go). Every token
// is a made-up one and every GitHub the tests' own fake: nothing leaves
// the machine.

// The fake's accounts, by the token they answer for.
var (
	alicePub = pubAccount{9000001, "mona-example"}
	bobPub   = pubAccount{9000002, "bob-example"}
	carolPub = pubAccount{9000003, "carol-example"}
)

type pubAccount struct {
	id    int64
	login string
}

// pubNode is an issue or a pull request as the fake's GraphQL answers it,
// on github.com unless host says otherwise.
type pubNode struct {
	typ, name, state, host string
	number                 int
	repo                   int64
}

// page is the node's page on its host.
func (n *pubNode) page() string {
	host, seg := cmp.Or(n.host, "github.com"), "issues"
	if n.typ == "PullRequest" {
		seg = "pull"
	}
	return fmt.Sprintf("https://%s/%s/%s/%d", host, n.name, seg, n.number)
}

// pubComment is a comment the fake holds.
type pubComment struct {
	id           int64
	repo         int64
	number       int
	body         string
	author       pubAccount
	created      time.Time
	subject, url string
}

// Post modes: how the fake treats the next AddComment. ok commits and
// answers; drop commits nothing and answers 502; lost commits and answers
// 502; hold commits and holds the answer until released or the client
// leaves; late commits nothing, answers 502, and commits it when the test
// says (commitLate); refused commits nothing and answers 403, which proves
// nothing was posted; madeRefused commits and answers 403 all the same.
const (
	postOK          = "ok"
	postDrop        = "drop"
	postLost        = "lost"
	postHold        = "hold"
	postLate        = "late"
	postRefused     = "refused"
	postMadeRefused = "made-refused"
)

// pubGH is the fake's GitHub for publications on top of ghItems: the
// three GraphQL documents over its nodes, and the comments' listing, with
// the accounts each token answers as.
type pubGH struct {
	g        *ghItems
	mu       sync.Mutex
	nodes    map[string]*pubNode
	comments []*pubComment
	nextID   int64
	accounts map[string]pubAccount
	mode     string
	skew     time.Duration
	closeTo  int    // the close's answer: 0 a close, 403 a refusal, 502 none
	lostShut bool   // with closeTo 502, the close is made all the same
	listTo   int    // a listing's answer: 0 the comments, else that status
	urlTo    string // a post's answer's address, when set, for the comment's own
	held     chan struct{}
	reached  chan struct{}
	late     *pubComment
	// onUser, when set, is called as an account is read (GET /user),
	// before the answer.
	onUser func()
	user   http.HandlerFunc
	ghe    bool
}

// gheAPI is the Enterprise Server's API path, before a REST path.
const gheAPI = "/api/v3"

func newPubGH(t *testing.T, g *ghItems) *pubGH {
	p := &pubGH{g: g, nodes: map[string]*pubNode{}, nextID: 7000000001, mode: postOK,
		accounts: map[string]pubAccount{plantedToken: alicePub, plantedToken2: bobPub, plantedEnv: carolPub}}
	user := func(w http.ResponseWriter, r *http.Request) {
		a, ok := p.account(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		on := p.onUser
		p.mu.Unlock()
		if on != nil {
			on()
		}
		fmt.Fprintf(w, `{"login":%q,"id":%d}`, a.login, a.id)
	}
	p.user = user
	g.f.Handle("POST", "api.github.com", "/graphql", p.graphql)
	g.f.Handle("GET", "api.github.com", "/user", user)
	t.Cleanup(p.release)
	return p
}

// onGHE serves the publications' side on the Enterprise Server's host as
// well (its GraphQL, its account and, for each node put after, its
// comments' listing).
func (p *pubGH) onGHE() {
	p.mu.Lock()
	p.ghe = true
	p.mu.Unlock()
	p.g.f.Handle("POST", gheHost, "/api/graphql", p.graphql)
	p.g.f.Handle("GET", gheHost, gheAPI+"/user", p.user)
}

func (p *pubGH) account(r *http.Request) (pubAccount, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.accounts[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	return a, ok
}

// node puts a node on the fake and serves its comments' listing.
func (p *pubGH) node(id, typ, name string, number int, repo int64) {
	p.mu.Lock()
	p.nodes[id] = &pubNode{typ: typ, name: name, number: number, repo: repo, state: "OPEN"}
	ghe := p.ghe
	p.mu.Unlock()
	path := fmt.Sprintf("/repositories/%d/issues/%d/comments", repo, number)
	list := func(w http.ResponseWriter, r *http.Request) { p.list(w, r, repo, number) }
	p.g.f.Handle("GET", "api.github.com", path, list)
	if ghe {
		p.g.f.Handle("GET", gheHost, gheAPI+path, list)
	}
}

// listings is the fake's listings of comments asked, on any host.
func (p *pubGH) listings() int {
	n := 0
	for _, r := range p.g.f.Requests() {
		if strings.HasSuffix(r.Path, "/comments") {
			n++
		}
	}
	return n
}

// edit changes a node.
func (p *pubGH) edit(id string, fn func(*pubNode)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p.nodes[id])
}

func (p *pubGH) set(fn func(*pubGH)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
}

// add puts a comment on the node as if its author posted it.
func (p *pubGH) add(node, body string, by pubAccount, created time.Time) *pubComment {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addLocked(node, body, by, created)
}

func (p *pubGH) addLocked(node, body string, by pubAccount, created time.Time) *pubComment {
	n := p.nodes[node]
	c := &pubComment{id: p.nextID, repo: n.repo, number: n.number, body: body, author: by, created: created.UTC().Truncate(time.Second), subject: node}
	c.url = fmt.Sprintf("https://%s/%s/issues/%d#issuecomment-%d", cmp.Or(n.host, "github.com"), n.name, n.number, c.id)
	p.nextID++
	p.comments = append(p.comments, c)
	return c
}

// commented is the comments on the node, in their order.
func (p *pubGH) commented(node string) []*pubComment {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*pubComment
	for _, c := range p.comments {
		if c.subject == node {
			out = append(out, c)
		}
	}
	return out
}

// release lets a held answer go.
func (p *pubGH) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held != nil {
		close(p.held)
		p.held = nil
	}
}

// commitLate commits the comment a late post left.
func (p *pubGH) commitLate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.late != nil {
		p.comments = append(p.comments, p.late)
		p.late = nil
	}
}

func (p *pubGH) graphql(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	q, err := githubtest.ParseGraphQL(b)
	a, ok := p.account(r)
	if err != nil || !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	answer := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p.mu.Lock()
	switch q.Query {
	case github.ResolveDocument:
		n := p.nodes[q.Variables["id"].(string)]
		if n == nil {
			p.mu.Unlock()
			answer(map[string]any{"data": map[string]any{"node": nil}})
			return
		}
		v := map[string]any{"__typename": n.typ, "id": q.Variables["id"], "number": n.number, "state": n.state,
			"url": n.page(), "repository": map[string]any{"fullDatabaseId": strconv.FormatInt(n.repo, 10), "nameWithOwner": n.name}}
		p.mu.Unlock()
		answer(map[string]any{"data": map[string]any{"node": v}})
	case github.AddCommentDocument:
		node := q.Variables["subjectId"].(string)
		c := p.addLocked(node, q.Variables["body"].(string), a, time.Now().Add(p.skew))
		mode, url := p.mode, cmp.Or(p.urlTo, c.url)
		var held, reached chan struct{}
		switch mode {
		case postDrop, postLate, postRefused:
			p.comments = p.comments[:len(p.comments)-1]
			if mode == postLate {
				p.late = c
			}
		case postHold:
			held, reached = p.held, p.reached
		}
		p.mu.Unlock()
		switch mode {
		case postDrop, postLost, postLate:
			w.WriteHeader(http.StatusBadGateway)
			return
		case postRefused, postMadeRefused:
			w.WriteHeader(http.StatusForbidden)
			return
		case postHold:
			close(reached)
			select {
			case <-held:
			case <-r.Context().Done():
				panic(http.ErrAbortHandler)
			}
		}
		answer(map[string]any{"data": map[string]any{"addComment": map[string]any{"subject": map[string]any{"id": node}, "commentEdge": map[string]any{"node": map[string]any{
			"fullDatabaseId": strconv.FormatInt(c.id, 10), "url": url, "createdAt": c.created.Format(time.RFC3339),
			"repository": map[string]any{"fullDatabaseId": strconv.FormatInt(c.repo, 10)}, "author": map[string]any{"login": a.login, "databaseId": a.id}}}}}})
	case github.CloseIssueDocument:
		id := q.Variables["issueId"].(string)
		n, status := p.nodes[id], p.closeTo
		if status == 0 || p.lostShut {
			n.state = "CLOSED"
		}
		p.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		answer(map[string]any{"data": map[string]any{"closeIssue": map[string]any{"issue": map[string]any{"id": id, "state": "CLOSED",
			"stateReason": q.Variables["stateReason"], "repository": map[string]any{"fullDatabaseId": strconv.FormatInt(n.repo, 10)}}}}})
	default:
		p.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}
}

// list answers a node's comments since the query's time, a page of 100.
func (p *pubGH) list(w http.ResponseWriter, r *http.Request, repo int64, number int) {
	if _, ok := p.account(r); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	p.mu.Lock()
	if p.listTo != 0 {
		status := p.listTo
		p.mu.Unlock()
		w.WriteHeader(status)
		return
	}
	var all []map[string]any
	for _, c := range p.comments {
		if c.repo == repo && c.number == number && !c.created.Before(since) {
			all = append(all, map[string]any{"id": c.id, "html_url": c.url, "body": c.body, "created_at": c.created.Format(time.RFC3339),
				"user": map[string]any{"login": c.author.login, "id": c.author.id}})
		}
	}
	p.mu.Unlock()
	from := min(len(all), (max(page, 1)-1)*100)
	out := all[from:min(len(all), from+100)]
	if out == nil {
		out = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// adds is the comments the fake was asked to post.
func (p *pubGH) adds() int { return p.g.f.Writes("graphql AddComment") }

// closes is the closes the fake was asked to make.
func (p *pubGH) closes() int { return p.g.f.Writes("graphql CloseIssue") }

const pubNodeID = "I_kwDOitem12"

// drivePublications drives every publication route that reaches the
// token through do (a fixture: the pattern, the path, the body, the answer
// it must give): a prepare, a send, a send whose post GitHub dropped and
// its Check, and a send whose close did not answer and its Close now. The
// fake's issue is octo/app#160 on github.com, imported into the project
// PBW; on another host (an Enterprise Server's, gheHost), octo/app#170
// there, imported into PBG, whose token the fake takes is plantedToken2.
func drivePublications(t *testing.T, e *testEnv, g *ghItems, host string, do func(pattern, path, body string, status int, code string) map[string]any) {
	t.Helper()
	p := newPubGH(t, g)
	n, node, project := 160, "I_kwDOitem160", "PBW"
	if host == "github.com" {
		g.set(n, ghItem{Node: node, Title: "Walked", Body: "b", Updated: t0, Labels: []string{}})
		p.node(node, "Issue", "octo/app", n, 4000001)
	} else {
		n, node, project = 170, "I_kwDOghe170", "PBG"
		p.onGHE()
		gheIssue(g, host, n, node)
		p.node(node, "Issue", "octo/app", n, 4000001)
		p.edit(node, func(x *pubNode) { x.host = host })
	}
	e.newDispatchProject(t, project, nil)
	e.importItem(t, project, fmt.Sprintf("https://%s/octo/app/issues/%d", host, n), opID(), http.StatusCreated, map[string]any{"images": false})
	key := project + "-1"
	prepare := func(extra string) (id, digest string) {
		out := do("POST /api/dispatch/tickets/{key}/publications", "/api/dispatch/tickets/"+key+"/publications",
			`{"opId":"`+opID()+`","target":"linked","body":"Walked."`+extra+`}`, http.StatusCreated, "")
		return out["publication"].(map[string]any)["id"].(string), out["postSha256"].(string)
	}
	send := func(id, digest string) {
		do("POST /api/dispatch/publications/{id}/send", "/api/dispatch/publications/"+id+"/send", `{"opId":"`+opID()+`","postSha256":"`+digest+`"}`, http.StatusOK, "")
	}
	send(prepare(""))
	p.set(func(p *pubGH) { p.mode = postDrop })
	id, digest := prepare("")
	send(id, digest)
	p.set(func(p *pubGH) { p.mode, p.closeTo = postOK, http.StatusBadGateway })
	do("POST /api/dispatch/publications/{id}/check", "/api/dispatch/publications/"+id+"/check", `{}`, http.StatusOK, "")
	id, digest = prepare(`,"closeIssue":"completed"`)
	send(id, digest)
	p.set(func(p *pubGH) { p.closeTo = 0 })
	do("POST /api/dispatch/publications/{id}/close", "/api/dispatch/publications/"+id+"/close", `{"opId":"`+opID()+`"}`, http.StatusOK, "")
}

// gheIssue serves issue n of octo/app (repository 4000001) on the
// Enterprise Server's host, its node node, to plantedToken2 alone.
func gheIssue(g *ghItems, host string, n int, node string) {
	g.f.Handle("GET", host, gheAPI+"/repos/octo/app", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":4000001,"full_name":"octo/app"}`)
	})
	g.f.Handle("GET", host, fmt.Sprintf("%s%s/issues/%d", gheAPI, itemsRepo, n), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+plantedToken2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := json.Marshal(map[string]any{"id": 5000000 + n, "node_id": node, "number": n, "html_url": fmt.Sprintf("https://%s/octo/app/issues/%d", host, n),
			"title": "Walked there", "body": "b", "body_html": "", "state": "open", "labels": []any{}, "assignees": []any{},
			"user": map[string]any{"login": "mona", "id": 9000001}, "created_at": itemsLayout, "updated_at": t0.Format(time.RFC3339)})
		_, _ = w.Write(b)
	})
}

// pubEnv is a server with a ticket (PUB-1) imported from the fake's issue
// octo/app#12, the token planted for github.com (Alice's), and the fake's
// publications side.
func pubEnv(t *testing.T) (*testEnv, *pubGH) {
	t.Helper()
	e, g := externalEnv(t)
	p := newPubGH(t, g)
	g.set(12, ghItem{Node: pubNodeID, Title: "The list crashes", Body: "It breaks.", Updated: t0, Labels: []string{}})
	p.node(pubNodeID, "Issue", "octo/app", 12, 4000001)
	e.newDispatchProject(t, "PUB", nil)
	e.importItem(t, "PUB", "https://github.com/octo/app/issues/12", opID(), http.StatusCreated, map[string]any{"images": false})
	return e, p
}

// prepare prepares a publication of the ticket and gives the reply.
func (e *testEnv) prepare(t *testing.T, key string, body map[string]any, status int, code string) map[string]any {
	t.Helper()
	b := map[string]any{"opId": opID(), "target": "linked", "body": "Fixed in v2."}
	for k, v := range body {
		b[k] = v
	}
	return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/publications", b, status, code)
}

// sendPub sends the publication with the op id and the digest given.
func (e *testEnv) sendPub(t *testing.T, id, op, digest string, status int, code string) map[string]any {
	t.Helper()
	return e.expect(t, "POST", "/api/dispatch/publications/"+id+"/send", map[string]any{"opId": op, "postSha256": digest}, status, code)
}

// pubOf is the publication a reply names.
func pubOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	return entityOf(t, out, "publication")
}

// stored is the publication as the server's store holds it.
func (e *testEnv) stored(t *testing.T, id string) dispatch.Publication {
	t.Helper()
	p, err := e.srv.dispatch.Publication(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// onlyToTheAPI fails the test unless every request the fake received
// since the n-th carried the token in Authorization to the API host
// alone.
func onlyToTheAPI(t *testing.T, g *ghItems, n int, tok string) {
	t.Helper()
	for _, r := range g.f.Requests()[n:] {
		auth := r.Header.Get("Authorization")
		if r.Host == "api.github.com" && auth != "Bearer "+tok || r.Host != "api.github.com" && auth != "" {
			t.Errorf("a request to %s%s, Authorization given %v", r.Host, r.Path, auth != "")
		}
	}
}

// A publication is posted once: its preview is the body, a blank line and
// GitHub's marker, named by its digest; a send posts it to the issue's
// node with the token (to the API host alone) and answers it sent; the
// same send again, a lost reply's resend, posts nothing; concurrent sends
// of one opId and of two post one comment; and the ticket's read carries
// it, without its body.
func TestPublishOnce(t *testing.T) {
	e, p := pubEnv(t)
	n := len(p.g.f.Requests())
	reached := e.srv.trackers.reached.Load()
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	if e.srv.trackers.reached.Load() == reached {
		t.Fatal("the prepare reached no credential through credentialFor")
	}
	reached = e.srv.trackers.reached.Load()
	pub := pubOf(t, out)
	id, digest := pub["id"].(string), out["postSha256"].(string)
	marker := "<!-- conductor-publication:" + id + " -->"
	if out["preview"] != "Fixed in v2.\n\n"+marker || pub["state"] != "draft" || pub["targetKind"] != "issue" || pub["targetNodeId"] != pubNodeID {
		t.Fatalf("the prepare: %v", out)
	}
	op := opID()
	sent := e.sendPub(t, id, op, digest, http.StatusOK, "")
	if got := pubOf(t, sent); got["state"] != "sent" || got["sentAsId"] != "9000001" || p.adds() != 1 || e.srv.trackers.reached.Load() == reached {
		t.Fatalf("the send: %v (%d posts)", sent, p.adds())
	}
	if c := p.commented(pubNodeID); len(c) != 1 || c[0].body != out["preview"] {
		t.Fatalf("the comment: %+v", c)
	}
	// The reply's address is the one the store kept: the comment's page with
	// its anchor.
	if url, _ := pubOf(t, sent)["remoteUrl"].(string); url != e.stored(t, id).RemoteURL || url != p.commented(pubNodeID)[0].url || !strings.Contains(url, "#issuecomment-") {
		t.Fatalf("the comment's address: %q, the store's %q", url, e.stored(t, id).RemoteURL)
	}
	again := e.sendPub(t, id, op, digest, http.StatusOK, "")
	if rc := entityOf(t, again, "receipt"); rc["repeat"] != true || p.adds() != 1 {
		t.Fatalf("the resend: %v (%d posts)", again, p.adds())
	}
	e.sendPub(t, id, opID(), digest, http.StatusConflict, dispatch.RefusalInvalid)
	onlyToTheAPI(t, p.g, n, plantedToken)
	// Concurrent sends of a second publication: two of one opId, two of
	// others; one post.
	second := pubOf(t, e.prepare(t, "PUB-1", map[string]any{"body": "Second."}, http.StatusCreated, ""))
	sid, sdigest := second["id"].(string), e.stored(t, second["id"].(string)).PostSha256
	shared := opID()
	var wg sync.WaitGroup
	for _, o := range []string{shared, shared, opID(), opID()} {
		wg.Go(func() {
			_, _, _ = e.call(t, "POST", "/api/dispatch/publications/"+sid+"/send", map[string]any{"opId": o, "postSha256": sdigest})
		})
	}
	wg.Wait()
	if got := e.stored(t, sid); got.State != dispatch.PublicationSent || p.adds() != 2 || got.Tries != 1 {
		t.Fatalf("concurrent sends: %+v, %d posts", got, p.adds())
	}
	tv, err := e.srv.dispatch.Ticket(t.Context(), "PUB-1")
	if err != nil || len(tv.Publications) != 2 {
		t.Fatalf("the ticket's read: %+v %v", tv.Publications, err)
	}
	if b, _ := json.Marshal(tv); strings.Contains(string(b), "Fixed in v2.") {
		t.Fatal("the ticket's read carries a publication's body")
	}
}

// A post held at GitHub while the server stops: the comment made, its
// answer never read; the row stays sending, and the send answers the
// routes' busy answer. A server started again over the data directory
// settles it in the background by its marker: sent, one comment, no
// second post; the same operation id again answers it as it stands.
func TestPublishAcrossCrash(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	reached := make(chan struct{})
	p.set(func(p *pubGH) { p.mode, p.held, p.reached = postHold, make(chan struct{}), reached })
	type reply struct {
		status int
		data   []byte
		out    map[string]any
	}
	answered := make(chan reply, 1)
	op := opID()
	go func() {
		status, data, out := e.call(t, "POST", "/api/dispatch/publications/"+id+"/send", map[string]any{"opId": op, "postSha256": digest})
		answered <- reply{status, data, out}
	}()
	<-reached
	stop, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(stop)
	if got := <-answered; got.status != http.StatusServiceUnavailable || errorCode(got.out) != "tracker_busy" || apiMessage(got.out) != errRouteEnded.Message {
		t.Fatalf("the send held across the stop: %d %s", got.status, got.data)
	}
	p.set(func(p *pubGH) { p.mode = postOK })
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	if got := e2.stored(t, id); got.State != dispatch.PublicationSending || len(p.commented(pubNodeID)) != 1 {
		t.Fatalf("after the stop: %+v, %d comments", got, len(p.commented(pubNodeID)))
	}
	e2.srv.SettlePublications()
	if !waitFor(testWait, func() bool { return e2.stored(t, id).State == dispatch.PublicationSent }) {
		t.Fatalf("the settle at the start: %+v", e2.stored(t, id))
	}
	if p.adds() != 1 || len(p.commented(pubNodeID)) != 1 || e2.stored(t, id).RemoteID != strconv.FormatInt(p.commented(pubNodeID)[0].id, 10) {
		t.Fatalf("%d posts, %d comments", p.adds(), len(p.commented(pubNodeID)))
	}
	if got := pubOf(t, e2.sendPub(t, id, op, digest, http.StatusOK, "")); got["state"] != "sent" || p.adds() != 1 {
		t.Fatalf("the same send again: %v, %d posts", got, p.adds())
	}
}

// A post GitHub dropped, its answer not one that proves it: the listing
// finds nothing, and within the quiet time the row stays sending with its
// words; past it (the server's clock seam, moved on from the time the send
// began as the store holds it) Check makes it unconfirmed. Send is
// refused; Publish as new posts one comment of its own.
func TestPublishUnconfirmedNeverResent(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	p.set(func(p *pubGH) { p.mode = postDrop })
	got := pubOf(t, e.sendPub(t, id, opID(), digest, http.StatusOK, ""))
	if got["state"] != "sending" || got["error"] == nil {
		t.Fatalf("a dropped post: %v", got)
	}
	ahead := time.Until(e.stored(t, id).SendingAt.Add(dispatch.PublicationQuiet + time.Second))
	e.srv.external.now = func() time.Time { return time.Now().Add(ahead) }
	if got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, "")); got["state"] != "unconfirmed" {
		t.Fatalf("the check: %v", got)
	}
	e.sendPub(t, id, opID(), digest, http.StatusConflict, dispatch.RefusalInvalid)
	p.set(func(p *pubGH) { p.mode = postOK })
	again := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	e.sendPub(t, pubOf(t, again)["id"].(string), opID(), again["postSha256"].(string), http.StatusOK, "")
	if p.adds() != 2 || len(p.commented(pubNodeID)) != 1 {
		t.Fatalf("%d posts, %d comments", p.adds(), len(p.commented(pubNodeID)))
	}
}

// A post GitHub commits after the negative listing: unconfirmed, no second
// post; a later Check finds it, sent.
func TestPublishLateCommit(t *testing.T) {
	e, p := pubEnv(t)
	e.srv.external.now = func() time.Time { return time.Now().Add(time.Hour) }
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	p.set(func(p *pubGH) { p.mode = postLate })
	if got := pubOf(t, e.sendPub(t, id, opID(), digest, http.StatusOK, "")); got["state"] != "unconfirmed" {
		t.Fatalf("the send: %v", got)
	}
	p.commitLate()
	if got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, "")); got["state"] != "sent" || p.adds() != 1 {
		t.Fatalf("the check: %v, %d posts", got, p.adds())
	}
}

// GitHub's clock three hours behind the server's, and three hours ahead:
// the settle's window (find_since, a day before the send) finds the
// comment either way.
func TestPublishSettleClockSkew(t *testing.T) {
	for _, skew := range []time.Duration{-3 * time.Hour, 3 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			e, p := pubEnv(t)
			out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
			id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
			p.set(func(p *pubGH) { p.mode, p.skew = postLost, skew })
			if got := pubOf(t, e.sendPub(t, id, opID(), digest, http.StatusOK, "")); got["state"] != "sent" || p.adds() != 1 {
				t.Fatalf("a lost answer: %v, %d posts", got, p.adds())
			}
		})
	}
}

// Alice's post's answer lost, Bob's token then saved: the settle finds
// Alice's comment by the account that sent it, and Bob's comment with the
// same bytes copied into it is not a finding.
func TestPublishSettleAfterTokenReplaced(t *testing.T) {
	e, p := pubEnv(t)
	e.srv.external.now = func() time.Time { return time.Now().Add(time.Hour) }
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	p.set(func(p *pubGH) { p.mode = postDrop })
	e.sendPub(t, id, opID(), digest, http.StatusOK, "")
	copied := p.add(pubNodeID, out["preview"].(string), bobPub, time.Now())
	if status, data, _ := e.saveToken(t, "", plantedToken2); status != http.StatusOK {
		t.Fatalf("Bob's token: %d %s", status, data)
	}
	got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, ""))
	if got["state"] != "unconfirmed" {
		t.Fatalf("Bob's copy found: %v (%d)", got, copied.id)
	}
	alice := p.add(pubNodeID, out["preview"].(string), alicePub, time.Now())
	got = pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, ""))
	if got["state"] != "sent" || got["remoteId"] != strconv.FormatInt(alice.id, 10) || got["sentAsId"] != "9000001" {
		t.Fatalf("Alice's comment: %v", got)
	}
}

// A server that starts with GitHub unreachable answers at once; its settle
// of a publication being sent ends within its bound, the row sending with
// the listing's words, nothing posted.
func TestPublishSettleInBackground(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), id, out["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
		t.Fatalf("begun: %v", err)
	}
	e2 := e.restart()
	e2.srv.trackers.gh = e2.srv.trackers.adapter(unreachable{})
	rev := e.stored(t, id).Rev
	e2.srv.SettlePublications()
	e2.expect(t, "GET", "/api/dispatch/publications/"+id, nil, http.StatusOK, "")
	// The settle's record, waited for on the store, within its bound.
	if !waitFor(settleStartTimeout+testWait, func() bool { return e.stored(t, id).Rev != rev }) {
		t.Fatal("the settle at the start recorded nothing")
	}
	if got := e.stored(t, id); got.State != dispatch.PublicationSending || got.Error == "" || p.adds() != 0 {
		t.Fatalf("the settle: %+v", got)
	}
}

// unreachable is a base transport that reaches nothing.
type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial tcp: connect: network is unreachable")
}

// sentWithClose sends a publication that asks the issue be closed, the
// close's answer as the fake gives it, and returns its id.
func sentWithClose(t *testing.T, e *testEnv, p *pubGH) string {
	t.Helper()
	out := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	e.sendPub(t, id, opID(), out["postSha256"].(string), http.StatusOK, "")
	return id
}

// A close GitHub refuses keeps the comment: sent, the close refused with
// its words; Close now closes it once GitHub takes it.
func TestPublishCloseRefusedKeepsComment(t *testing.T) {
	e, p := pubEnv(t)
	p.set(func(p *pubGH) { p.closeTo = http.StatusForbidden })
	id := sentWithClose(t, e, p)
	if got := e.stored(t, id); got.State != dispatch.PublicationSent || got.CloseState != dispatch.CloseRefused || got.CloseError == "" || len(p.commented(pubNodeID)) != 1 {
		t.Fatalf("a refused close: %+v", got)
	}
	p.set(func(p *pubGH) { p.closeTo = 0 })
	got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
	if got["closeState"] != "closed" || p.closes() != 2 || p.adds() != 1 {
		t.Fatalf("Close now: %v, %d closes", got, p.closes())
	}
}

// A close cut at each point is finished by Close now and never by the
// settle at a start: before the close (pending), it closes; after its
// begin (closing), it begins again and closes; after GitHub closed it, it
// sends nothing and settles closed from where it is, no try spent.
func TestPublishCloseRecovers(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		at         string
		sent, want int // closes sent by Close now; close_tries after it
	}{{"pending", 1, 1}, {"closing", 1, 2}, {"closed on GitHub", 0, 1}} {
		t.Run(c.at, func(t *testing.T) {
			e, p := pubEnv(t)
			var id string
			if c.at == "pending" {
				// Cut after the comment's settle, before the close was begun.
				out := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
				id = pubOf(t, out)["id"].(string)
				if _, _, err := e.srv.dispatch.BeginSend(ctx, adminOp("Alice"), id, out["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); err != nil {
					t.Fatal(err)
				}
				cm := p.add(pubNodeID, out["preview"].(string), alicePub, time.Now())
				if _, _, err := e.srv.dispatch.SettlePublication(ctx, id, 1, dispatch.Outcome{State: dispatch.PublicationSent,
					Comment: &dispatch.SentComment{ID: strconv.FormatInt(cm.id, 10), AuthorID: "9000001", Author: "mona-example"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				// The close the send makes answers nothing that says: closing.
				p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
				id = sentWithClose(t, e, p)
			}
			if c.at == "closed on GitHub" {
				p.edit(pubNodeID, func(n *pubNode) { n.state = "CLOSED" })
			}
			if got := e.stored(t, id); got.State != dispatch.PublicationSent || got.CloseState != map[bool]string{true: dispatch.ClosePending, false: dispatch.CloseClosing}[c.at == "pending"] {
				t.Fatalf("before Close now: %+v", got)
			}
			// A start's settle never closes.
			closes := p.closes()
			wctx, cancel := context.WithTimeout(ctx, testWait)
			defer cancel()
			e2 := e.restart()
			useFake(e2.srv, p.g.f)
			e2.srv.SettlePublications()
			e2.srv.WaitBackground(wctx)
			if p.closes() != closes {
				t.Fatal("a settle at a start closed")
			}
			p.set(func(p *pubGH) { p.closeTo = 0 })
			got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
			if got["closeState"] != "closed" || p.closes()-closes != c.sent || got["closeTries"] != float64(c.want) {
				t.Fatalf("Close now: %v, %d closes sent", got, p.closes()-closes)
			}
		})
	}
}

// A restored data directory sends none of its publications: a draft the
// original sent (Bob's comment there) and a failed one the original sent
// later are refused Send, nothing posted; Check finds each comment by any
// author and records it as the sender; a comment of another account with
// the marker and other text is not a finding; Publish as new posts one
// comment of its own.
func TestPublishAfterRestore(t *testing.T) {
	e, p := pubEnv(t)
	draft := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	failed := e.prepare(t, "PUB-1", map[string]any{"body": "Second."}, http.StatusCreated, "")
	p.set(func(p *pubGH) { p.mode = postDrop })
	fid := pubOf(t, failed)["id"].(string)
	if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), fid, failed["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.srv.dispatch.SettlePublication(t.Context(), fid, 1, dispatch.Outcome{State: dispatch.PublicationFailed}); err != nil {
		t.Fatal(err)
	}
	// What the original did after the export: each sent by Bob; and a
	// comment with the first's marker and other text.
	p.add(pubNodeID, draft["preview"].(string)+" edited", carolPub, time.Now())
	p.add(pubNodeID, draft["preview"].(string), bobPub, time.Now())
	p.add(pubNodeID, failed["preview"].(string), bobPub, time.Now())
	// The restore: the store stopped, made a new instance, served again
	// with Carol's token.
	wctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(wctx)
	if err := dispatch.Reissue(t.Context(), filepath.Join(e.srv.store.Dir(), "dispatch", dispatch.File)); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	if status, data, _ := e2.saveToken(t, "", plantedEnv); status != http.StatusOK {
		t.Fatalf("Carol's token: %d %s", status, data)
	}
	p.set(func(p *pubGH) { p.mode = postOK })
	adds := p.adds()
	for _, out := range []map[string]any{draft, failed} {
		id := pubOf(t, out)["id"].(string)
		e2.sendPub(t, id, opID(), out["postSha256"].(string), http.StatusConflict, dispatch.RefusalInvalid)
		got := pubOf(t, e2.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, ""))
		if got["state"] != "sent" || got["sentAsId"] != "9000002" {
			t.Fatalf("the check of %s: %v", id, got)
		}
	}
	again := e2.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	e2.sendPub(t, pubOf(t, again)["id"].(string), opID(), again["postSha256"].(string), http.StatusOK, "")
	if p.adds() != adds+1 {
		t.Fatalf("%d posts after the restore", p.adds()-adds)
	}
}

// The destination is read from GitHub at prepare, its kind GitHub's: a
// link to an /issues/N address GitHub answers as a pull request is kind
// change, and its close is refused; a link added with no token is read at
// prepare; with no token, 412 no_token, nothing kept.
func TestPrepareVerifiesTarget(t *testing.T) {
	e, p := pubEnv(t)
	p.g.set(13, ghItem{Node: "PR_kwDOitem13", Title: "A pull request", Body: "b", Updated: t0, Pull: true, Labels: []string{}})
	p.node("PR_kwDOitem13", "PullRequest", "octo/app", 13, 4000001)
	if status, data, _ := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK {
		t.Fatalf("the remove: %d %s", status, data)
	}
	linkID := rcOf(e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/links", map[string]any{"opId": opID(), "kind": "external",
		"url": "https://github.com/octo/app/issues/13"}, http.StatusCreated, ""))["id"].(string)
	e.prepare(t, "PUB-1", map[string]any{"target": "link:" + linkID}, http.StatusPreconditionFailed, "no_token")
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token: %d %s", status, data)
	}
	e.prepare(t, "PUB-1", map[string]any{"target": "link:" + linkID, "closeIssue": "completed"}, http.StatusConflict, dispatch.RefusalInvalid)
	got := pubOf(t, e.prepare(t, "PUB-1", map[string]any{"target": "link:" + linkID}, http.StatusCreated, ""))
	if got["targetKind"] != "change" || got["targetNodeId"] != "PR_kwDOitem13" {
		t.Fatalf("the link's target: %v", got)
	}
}

// Every write goes to the node fixed at prepare: a repository renamed
// keeps its id, and the comment goes to the node where it is now; a new
// repository then made with the old name, with an issue of that number,
// gets no comment and no listing, from a send or from a Check of a post
// GitHub dropped; the address kept for a comment posted, and for one a
// listing found, is under the repository's name now; an issue transferred
// to another repository is 409 target_changed, nothing posted or
// committed.
func TestSendRefusesChangedTarget(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	p.edit(pubNodeID, func(n *pubNode) { n.name = "octo/renamed" })
	const other = "I_kwDOnewrepo12"
	p.node(other, "Issue", "octo/app", 12, 4000009)
	sent := pubOf(t, e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, ""))
	c := p.commented(pubNodeID)
	if len(c) != 1 || p.adds() != 1 {
		t.Fatalf("the renamed repository: %d comments", len(c))
	}
	if want := "https://github.com/octo/renamed/issues/12#issuecomment-" + strconv.FormatInt(c[0].id, 10); sent["remoteUrl"] != want || e.stored(t, sent["id"].(string)).RemoteURL != want {
		t.Fatalf("the comment's address: %v, want %s", sent["remoteUrl"], want)
	}
	p.set(func(p *pubGH) { p.mode = postLost })
	lost := e.prepare(t, "PUB-1", map[string]any{"body": "Its answer lost."}, http.StatusCreated, "")
	found := pubOf(t, e.sendPub(t, pubOf(t, lost)["id"].(string), opID(), lost["postSha256"].(string), http.StatusOK, ""))
	if c := p.commented(pubNodeID); found["state"] != "sent" || len(c) != 2 || found["remoteUrl"] != "https://github.com/octo/renamed/issues/12#issuecomment-"+strconv.FormatInt(c[1].id, 10) {
		t.Fatalf("a comment a listing found: %v", found)
	}
	p.set(func(p *pubGH) { p.mode = postDrop })
	dropped := e.prepare(t, "PUB-1", map[string]any{"body": "Dropped."}, http.StatusCreated, "")
	did := pubOf(t, dropped)["id"].(string)
	e.sendPub(t, did, opID(), dropped["postSha256"].(string), http.StatusOK, "")
	p.set(func(p *pubGH) { p.mode = postOK })
	e.expect(t, "POST", "/api/dispatch/publications/"+did+"/check", map[string]any{}, http.StatusOK, "")
	if len(p.commented(other)) != 0 || p.g.count("api.github.com", "/repositories/4000009/") != 0 || p.g.count("api.github.com", "/repositories/4000001/") == 0 {
		t.Fatalf("the new repository of the old name: %d comments, %d listings", len(p.commented(other)), p.g.count("api.github.com", "/repositories/4000009/"))
	}
	moved := e.prepare(t, "PUB-1", map[string]any{"body": "Again."}, http.StatusCreated, "")
	p.edit(pubNodeID, func(n *pubNode) { n.repo, n.name = 4000002, "other/app" })
	id := pubOf(t, moved)["id"].(string)
	e.sendPub(t, id, opID(), moved["postSha256"].(string), http.StatusConflict, "target_changed")
	if got := e.stored(t, id); got.State != dispatch.PublicationDraft || p.adds() != 3 {
		t.Fatalf("a transferred issue: %+v, %d posts", got, p.adds())
	}
}

// The ticket's older publications that need a person are listed by
// needYou=1 past the newest ten, and Check and Close now act on them; the
// pages are fifty at most; a cursor out of form is 400.
func TestPublicationsRoute(t *testing.T) {
	e, p := pubEnv(t)
	e.srv.external.now = func() time.Time { return time.Now().Add(time.Hour) }
	out := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
	old := pubOf(t, out)["id"].(string)
	p.set(func(p *pubGH) { p.mode = postDrop })
	e.sendPub(t, old, opID(), out["postSha256"].(string), http.StatusOK, "")
	p.set(func(p *pubGH) { p.mode = postOK })
	for i := range dispatch.MaxTicketPublications {
		e.prepare(t, "PUB-1", map[string]any{"body": fmt.Sprint("n", i)}, http.StatusCreated, "")
	}
	tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/PUB-1", nil, http.StatusOK, ""), "ticket")
	if tk["publicationsNeedYou"] != float64(1) || len(tk["publications"].([]any)) != dispatch.MaxTicketPublications {
		t.Fatalf("the ticket: %v %v", tk["publicationsNeedYou"], len(tk["publications"].([]any)))
	}
	page := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications?needYou=1", nil, http.StatusOK, "")
	if l := page["publications"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != old || l[0].(map[string]any)["body"] != nil {
		t.Fatalf("need you: %v", page)
	}
	p.add(pubNodeID, out["preview"].(string), alicePub, time.Now())
	e.expect(t, "POST", "/api/dispatch/publications/"+old+"/check", map[string]any{}, http.StatusOK, "")
	if got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+old+"/close", map[string]any{"opId": opID()}, http.StatusOK, "")); got["closeState"] != "closed" {
		t.Fatalf("Close now: %v", got)
	}
	e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications?cursor=x", nil, http.StatusBadRequest, "invalid_request")
	all := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications", nil, http.StatusOK, "")
	if len(all["publications"].([]any)) != dispatch.MaxTicketPublications+1 || all["next"] != "" {
		t.Fatalf("every page: %v", all["next"])
	}
}

// Nothing is posted or closed by a side effect: with a draft, a failed
// and a sent publication asking a close on the ticket, its edits, a
// comment, a link's add and read, a refresh, its close, a Check of a draft
// (refused) and a server's start make no write to GitHub.
func TestNothingPublishedBySideEffect(t *testing.T) {
	e, p := pubEnv(t)
	e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	failed := e.prepare(t, "PUB-1", map[string]any{"body": "f"}, http.StatusCreated, "")
	p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
	sentWithClose(t, e, p)
	fid := pubOf(t, failed)["id"].(string)
	// One being sent that asked a close, as a crash after its post leaves
	// it (its comment on GitHub), for the start's settle.
	sending := e.prepare(t, "PUB-1", map[string]any{"body": "s", "closeIssue": "completed"}, http.StatusCreated, "")
	sid := pubOf(t, sending)["id"].(string)
	if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), sid, sending["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
		t.Fatal(err)
	}
	p.add(pubNodeID, e.stored(t, sid).Posted(), alicePub, time.Now())
	writes, closes := p.g.f.AllWrites(), p.closes()
	tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/PUB-1", nil, http.StatusOK, ""), "ticket")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/edit", map[string]any{"opId": opID(), "expectedRev": tk["rev"], "set": map[string]any{"priority": 1}}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/comments", map[string]any{"opId": opID(), "body": "private"}, http.StatusCreated, "")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/external/refresh", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/publications/"+fid+"/check", map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid)
	e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil, http.StatusOK, "")
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	e2.srv.SettlePublications()
	// The start's settle finds the comment: sent, its close pending; and
	// once it lets the publication's lock go (it settles under it), it
	// has closed nothing.
	if !waitFor(testWait, func() bool {
		got := e.stored(t, sid)
		return got.State == dispatch.PublicationSent && got.CloseState == dispatch.ClosePending
	}) {
		t.Fatalf("the start's settle: %+v", e.stored(t, sid))
	}
	release, ok := e2.srv.external.lock(t.Context(), "publication:"+sid, testWait)
	if !ok {
		t.Fatal("the start's settle kept the publication's lock")
	}
	release()
	if n := p.g.f.AllWrites() - writes; n != 0 || p.closes() != closes || e.stored(t, sid).CloseState != dispatch.ClosePending {
		t.Fatalf("%d writes to GitHub by a side effect, %d closes", n, p.closes()-closes)
	}
}

// What is posted is the person's text and the parts they ticked, each the
// exact Markdown the offer showed, and the marker: never the ticket's
// private comment, its attachment's name, its description or its plan's
// constraints.
func TestPublicationCarriesOnlyChosen(t *testing.T) {
	e, p := pubEnv(t)
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/comments", map[string]any{"opId": opID(), "body": "PRIVATE-COMMENT"}, http.StatusCreated, "")
	tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/PUB-1", nil, http.StatusOK, ""), "ticket")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/edit", map[string]any{"opId": opID(), "expectedRev": tk["rev"], "set": map[string]any{"acceptance": "PRIVATE-ACCEPTANCE"}},
		http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/links", map[string]any{"opId": opID(), "kind": "delivery", "url": "https://github.com/octo/app/pull/99",
		"note": "the fix"}, http.StatusCreated, "")
	offer := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil, http.StatusOK, "")
	var parts []string
	body := "The outcome."
	for _, x := range offer["parts"].([]any) {
		part := x.(map[string]any)
		parts = append(parts, part["name"].(string))
		body += "\n\n" + part["markdown"].(string)
	}
	if !slices.Contains(parts, "deliveries") || !slices.Contains(parts, "key") {
		t.Fatalf("the offer: %v", offer)
	}
	out := e.prepare(t, "PUB-1", map[string]any{"body": body, "parts": parts}, http.StatusCreated, "")
	e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, "")
	posted := p.commented(pubNodeID)[0].body
	if posted != out["preview"] || !strings.Contains(posted, "https://github.com/octo/app/pull/99 the fix") {
		t.Fatalf("posted %q", posted)
	}
	for _, private := range []string{"PRIVATE-COMMENT", "PRIVATE-ACCEPTANCE", "It breaks."} {
		if strings.Contains(posted, private) || strings.Contains(fmt.Sprint(offer), private) {
			t.Errorf("%q was offered or posted", private)
		}
	}
}

// No Brakeman grant and no shared read names a publication's route, so a
// turn's principal reaches none (actable and readable leave them the
// admin's); and every route is the admin's (TestDispatchRoutesAreTheAdminsAlone
// walks them with every other token).
func TestPublishRefusesBrakemanAndSessions(t *testing.T) {
	e, _ := pubEnv(t)
	for _, r := range e.srv.dispatchRoutes() {
		if !strings.Contains(r.pattern, "publication") && !strings.Contains(r.pattern, "/publish/") {
			continue
		}
		if _, ok := actRoutes[r.pattern]; ok {
			t.Errorf("%s is a Brakeman grant's route", r.pattern)
		}
		if _, ok := sharedReadSpec(r.pattern); ok {
			t.Errorf("%s is a shared read", r.pattern)
		}
	}
	for _, op := range Operations() {
		if strings.HasPrefix(op.Name, "publication_") && (op.Scope != "admin" || op.MCP != "") {
			t.Errorf("%s: scope %s, tool %q", op.Name, op.Scope, op.MCP)
		}
	}
	e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil, http.StatusOK, "")
	status, _, _ := e.callAs(t, "test-host-token", "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("a host token: %d", status)
	}
}

// The deadlines of the publications' routes are the sums of their steps'
// caps; a send waiting behind its publication's lock past the wait is 503
// tracker_busy, nothing posted.
func TestPublishDeadlines(t *testing.T) {
	c := defaultTrackerCaps
	for _, d := range []struct{ got, want time.Duration }{
		{PublishTimeout, c.sending()}, {CheckTimeout, c.checking()}, {CloseNowTimeout, c.closeNow()}, {closeSteps, c.closing()},
		{c.closing(), c.reads + c.write + c.post + c.write},
		{c.sending(), c.busy + c.reads + c.write + c.post + c.post + c.write + c.closing()},
		{c.checking(), c.busy + c.post + c.write}, {c.closeNow(), c.busy + c.closing()},
		{PrepareTimeout, c.preparing()}, {c.preparing(), c.busy + c.reads + c.write + c.reads + c.write},
		{PublishTimeout, 315 * time.Second}, {PrepareTimeout, 165 * time.Second}, {CheckTimeout, 75 * time.Second},
		{CloseNowTimeout, 150 * time.Second},
	} {
		if d.got != d.want {
			t.Errorf("a deadline of %s, not %s", d.got, d.want)
		}
	}
	e, p := pubEnv(t)
	caps := c
	caps.busy = 100 * time.Millisecond
	e.srv.external.caps = &caps
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	release, ok := e.srv.external.lock(t.Context(), "publication:"+id, time.Second)
	if !ok {
		t.Fatal("the lock")
	}
	defer release()
	e.sendPub(t, id, opID(), out["postSha256"].(string), http.StatusServiceUnavailable, "tracker_busy")
	if p.adds() != 0 || e.stored(t, id).State != dispatch.PublicationDraft {
		t.Fatal("a busy send went on")
	}
}

// settleErrs is an error for every code GitHub's answer can carry
// (trackerAnswer), the ones credentialFor and the routes answer with
// among them, none, and one whose code is out of form.
var settleErrs = []error{nil, tracker.ErrNotFound, tracker.ErrUnauthorized, tracker.ErrForbidden, tracker.ErrBound, tracker.ErrTargetChanged,
	&tracker.ErrRateLimited{}, &tracker.ErrMoved{To: tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: "octo/other", Kind: "issue", ID: "9"}},
	errors.New("dial tcp: GitHub said https://x.example.com/p?SECRET"), context.DeadlineExceeded, errItemBusy, errTargetChanged, errNoToken("github.com"),
	newAPIError(http.StatusBadRequest, "invalid_host", trackerHostWords), newAPIError(http.StatusBadRequest, "not_a_tracker_url", "x"),
	newAPIError(http.StatusInternalServerError, "credentials_unusable", "x"), newAPIError(http.StatusBadRequest, "Bad Code/x.example.com?SECRET", "x")}

// Every sentence a publication's settle is handed (settleWords), with every
// code GitHub's answer can carry, holds nothing GitHub wrote and is kept
// whole by the store: as the words of a publication being sent, and as a
// close's, each read back from the row.
func TestPublicationSettleWordsKept(t *testing.T) {
	e, _ := pubEnv(t)
	ctx := t.Context()
	ds := e.srv.dispatch
	target := dispatch.PublicationTarget{NodeID: pubNodeID, SpaceID: "4000001", Kind: dispatch.ExternalIssue, URL: "https://github.com/octo/app/issues/12", Ref: "octo/app#12"}
	begun := func(closeIssue string) string {
		t.Helper()
		id := doneOf(t)(ds.PreparePublication(ctx, adminOp("Alice"), dispatch.PublicationArgs{Key: "PUB-1", Target: dispatch.TargetLinked, Body: "Words.",
			CloseIssue: closeIssue}, target, e.srv.trackers.gh.Marker)).ID
		if _, owned, err := ds.BeginSend(ctx, adminOp("Alice"), id, e.stored(t, id).PostSha256, dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
			t.Fatalf("begun: %v", err)
		}
		return id
	}
	var said []string
	for k := range pubSaids {
		for _, err := range settleErrs {
			w := settleWords(k, err)
			if strings.Contains(w, "SECRET") || strings.Contains(w, "example.com") || strings.Contains(w, "Bad Code") || strings.Contains(w, "octo/") {
				t.Errorf("%q holds what GitHub or the error said", w)
			}
			if !slices.Contains(said, w) {
				said = append(said, w)
			}
		}
	}
	if len(said) < int(pubSaids)+4*12 {
		t.Fatalf("%d sentences: %q", len(said), said)
	}
	sending := begun("")
	for _, w := range said {
		if _, _, err := ds.SettlePublication(ctx, sending, 1, dispatch.Outcome{Words: w}); err != nil {
			t.Fatal(err)
		}
		if got := e.stored(t, sending).Error; got != w {
			t.Errorf("a send's words %q kept as %q", w, got)
		}
	}
	var closing string
	for i, w := range said {
		if i%dispatch.MaxPublicationCloseTries == 0 {
			closing = begun("completed")
			if _, _, err := ds.SettlePublication(ctx, closing, 1, dispatch.Outcome{State: dispatch.PublicationSent,
				Comment: &dispatch.SentComment{ID: strconv.Itoa(8000000000 + i), AuthorID: "9000001", Author: "mona-example"}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := ds.BeginClose(ctx, closing, 0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ds.SettleClose(ctx, closing, dispatch.CloseOutcome{State: dispatch.CloseRefused, Words: w}); err != nil {
			t.Fatal(err)
		}
		if got := e.stored(t, closing).CloseError; got != w {
			t.Errorf("a close's words %q kept as %q", w, got)
		}
	}
}

// A person's Check, Send, Close now and Discard of a publication are
// refused for a deleted ticket and for an archived project in a comment's
// write's words and answer, before any credential is read or anything is
// asked of GitHub (Send and Discard keep their receipt). A send whose
// comment was posted answers with the publication when its close is then
// refused; and the server's own settle at its start records a comment
// posted for a ticket deleted meanwhile.
func TestPublicationNeedsALiveTicket(t *testing.T) {
	e, p := pubEnv(t)
	draft := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	did := pubOf(t, draft)["id"].(string)
	p.set(func(p *pubGH) { p.closeTo = http.StatusForbidden })
	closing := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.mode, p.closeTo = postDrop, 0 })
	sending := e.prepare(t, "PUB-1", map[string]any{"body": "Sending."}, http.StatusCreated, "")
	sid := pubOf(t, sending)["id"].(string)
	e.sendPub(t, sid, opID(), sending["postSha256"].(string), http.StatusOK, "")
	p.set(func(p *pubGH) { p.mode = postOK })
	if got, closed := e.stored(t, sid), e.stored(t, closing); got.State != dispatch.PublicationSending || closed.CloseState != dispatch.CloseRefused {
		t.Fatalf("the publications: %+v, %+v", got, closed)
	}
	for _, c := range []struct {
		name, act, undo string
		status          int
		code            string
	}{
		{"a deleted ticket", "/api/dispatch/tickets/PUB-1/delete", "/api/dispatch/tickets/PUB-1/undelete", http.StatusNotFound, dispatch.RefusalNotFound},
		{"an archived project", "/api/dispatch/projects/PUB/archive", "/api/dispatch/projects/PUB/unarchive", http.StatusConflict, dispatch.RefusalArchived},
	} {
		e.expect(t, "POST", c.act, map[string]any{"opId": opID()}, http.StatusOK, "")
		want := apiMessage(e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/comments", map[string]any{"opId": opID(), "body": "b"}, c.status, c.code))
		n, reached, adds, closes := len(p.g.f.Requests()), e.srv.trackers.reached.Load(), p.adds(), p.closes()
		for _, a := range []struct {
			route   string
			path    string
			body    map[string]any
			receipt bool
		}{
			{"Check", "/api/dispatch/publications/" + sid + "/check", map[string]any{}, false},
			{"Close now", "/api/dispatch/publications/" + closing + "/close", map[string]any{"opId": opID()}, false},
			{"Send", "/api/dispatch/publications/" + did + "/send", map[string]any{"opId": opID(), "postSha256": draft["postSha256"]}, true},
			{"Discard", "/api/dispatch/publications/" + did + "/discard", map[string]any{"opId": opID()}, true},
		} {
			out := e.expect(t, "POST", a.path, a.body, c.status, c.code)
			if apiMessage(out) != want || (rcOf(out) != nil) != a.receipt {
				t.Errorf("%s, %s: %v, want the words %q", c.name, a.route, out, want)
			}
		}
		if len(p.g.f.Requests()) != n || e.srv.trackers.reached.Load() != reached || p.adds() != adds || p.closes() != closes {
			t.Fatalf("%s: %d requests to GitHub, %d credentials read", c.name, len(p.g.f.Requests())-n, e.srv.trackers.reached.Load()-reached)
		}
		e.expect(t, "POST", c.undo, map[string]any{"opId": opID()}, http.StatusOK, "")
	}
	// The project archived while a send's post is held at GitHub: the
	// comment is recorded, the close is not begun, and the send answers
	// with the publication as it stands.
	held := e.prepare(t, "PUB-1", map[string]any{"body": "Held.", "closeIssue": "completed"}, http.StatusCreated, "")
	hid := pubOf(t, held)["id"].(string)
	reached := make(chan struct{})
	p.set(func(p *pubGH) { p.mode, p.held, p.reached = postHold, make(chan struct{}), reached })
	type reply struct {
		status int
		out    map[string]any
	}
	answered := make(chan reply, 1)
	go func() {
		status, _, out := e.call(t, "POST", "/api/dispatch/publications/"+hid+"/send", map[string]any{"opId": opID(), "postSha256": held["postSha256"]})
		answered <- reply{status, out}
	}()
	<-reached
	e.expect(t, "POST", "/api/dispatch/projects/PUB/archive", map[string]any{"opId": opID()}, http.StatusOK, "")
	closes := p.closes()
	p.release()
	got := <-answered
	if pub, _ := got.out["publication"].(map[string]any); got.status != http.StatusOK || pub["state"] != "sent" || pub["closeState"] != "pending" || p.closes() != closes {
		t.Fatalf("a send whose close was refused: %d %v", got.status, got.out)
	}
	e.expect(t, "POST", "/api/dispatch/projects/PUB/unarchive", map[string]any{"opId": opID()}, http.StatusOK, "")
	// The server's own settle at its start: the post was made (Alice's
	// comment), and the ticket was deleted meanwhile.
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	cm := p.add(pubNodeID, e.stored(t, sid).Posted(), alicePub, time.Now())
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	e2.srv.SettlePublications()
	if !waitFor(testWait, func() bool { return e.stored(t, sid).State == dispatch.PublicationSent }) {
		t.Fatalf("the settle at the start: %+v", e.stored(t, sid))
	}
	if got := e.stored(t, sid); got.RemoteID != strconv.FormatInt(cm.id, 10) {
		t.Fatalf("settled with %+v", got)
	}
}

// Every store's answer a publication's route can meet has one answer of
// the route's, made in one place (publicationFail): a refusal the store
// kept with its receipt, one it returned and kept nowhere (a settle that
// another change at the same revision came before, a close begun on a
// deleted ticket or in an archived project), an error of the store's, and
// GitHub's failures.
func TestPublicationFailAnswers(t *testing.T) {
	e, _ := pubEnv(t)
	kept := func(code string) dispatch.Receipt {
		return dispatch.Receipt{OpID: "o1", Intent: dispatch.IntentPublicationSend, Status: dispatch.StatusFailed, Refusal: code, Error: "words of " + code}
	}
	for _, c := range []struct {
		name    string
		rc      dispatch.Receipt
		err     error
		status  int
		code    string
		receipt bool
	}{
		{"a send on a deleted ticket", kept(dispatch.RefusalNotFound), nil, http.StatusNotFound, dispatch.RefusalNotFound, true},
		{"a send in an archived project", kept(dispatch.RefusalArchived), nil, http.StatusConflict, dispatch.RefusalArchived, true},
		{"a send of another digest", kept(dispatch.RefusalStaleRevision), nil, http.StatusConflict, dispatch.RefusalStaleRevision, true},
		{"a send of one sent", kept(dispatch.RefusalInvalid), nil, http.StatusConflict, dispatch.RefusalInvalid, true},
		{"a send past its tries", kept(dispatch.RefusalBound), nil, http.StatusConflict, dispatch.RefusalBound, true},
		{"a store that takes no writes", kept(dispatch.RefusalReadOnly), nil, http.StatusServiceUnavailable, "dispatch_unavailable", false},
		{"a settle another came before at its revision", dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalStaleRevision, Words: "w"},
			http.StatusConflict, dispatch.RefusalStaleRevision, false},
		{"a close begun on a deleted ticket", dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalNotFound, Words: "PUB-1 is deleted"},
			http.StatusNotFound, dispatch.RefusalNotFound, false},
		{"a close begun in an archived project", dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalArchived, Words: "PUB is archived"},
			http.StatusConflict, dispatch.RefusalArchived, false},
		{"a close past its tries", dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalBound, Words: "w"}, http.StatusConflict, dispatch.RefusalBound, false},
		{"a settle the row refuses", dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalInvalid, Words: "w"}, http.StatusConflict, dispatch.RefusalInvalid, false},
		{"no such publication, as an older store answers", dispatch.Receipt{}, dispatch.ErrNotFound, http.StatusNotFound, "not_found", false},
		{"a store that cannot serve", dispatch.Receipt{}, dispatch.ErrUnavailable, http.StatusServiceUnavailable, "dispatch_unavailable", false},
		{"a display name out of form", dispatch.Receipt{}, dispatch.ErrBadBy, http.StatusBadRequest, "invalid_request", false},
		{"the target changed", dispatch.Receipt{}, errTargetChanged, http.StatusConflict, "target_changed", false},
		{"GitHub has no such item", dispatch.Receipt{}, tracker.ErrNotFound, http.StatusNotFound, "external_not_found", false},
		{"no token", dispatch.Receipt{}, errNoToken("github.com"), http.StatusPreconditionFailed, "no_token", false},
		{"GitHub out of reach", dispatch.Receipt{}, context.DeadlineExceeded, http.StatusBadGateway, "tracker_unavailable", false},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/dispatch/publications/0123456789abcdef/send", nil)
		e.srv.publicationFail(w, r, tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: "octo/app", Kind: "issue", ID: "12"}, c.rc, c.err, e.srv.dispatch,
			t.Context(), "0123456789abcdef")
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != c.status || errorCode(out) != c.code || (rcOf(out) != nil) != c.receipt {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
}

// Every route of a publication the store does not hold answers 404
// not_found, as a store from before its publications answers every one,
// and asks GitHub nothing.
func TestPublicationNotThere(t *testing.T) {
	e, p := pubEnv(t)
	n := len(p.g.f.Requests())
	at := "/api/dispatch/publications/0123456789abcdef"
	e.expect(t, "GET", at, nil, http.StatusNotFound, "not_found")
	e.sendPub(t, "0123456789abcdef", opID(), strings.Repeat("0", 64), http.StatusNotFound, "not_found")
	e.expect(t, "POST", at+"/check", map[string]any{}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", at+"/close", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", at+"/discard", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	if len(p.g.f.Requests()) != n {
		t.Fatalf("%d requests to GitHub", len(p.g.f.Requests())-n)
	}
}

// What the server can refuse from what it holds is refused before any
// credential is read, any request is made to GitHub or any link's read is
// recorded: a prepare of a deleted ticket or in an archived project (for
// the linked issue and for a link), with a body empty, of spaces, past its
// bound or with a control character, a close or a part out of form (both
// targets), of a link of another ticket, past the bound on a ticket's
// publications; a send, the repeat of a send being sent, a Check, a Close
// now and a discard on a deleted ticket and in an archived project; a
// publication not there, sent or discarded; a digest not the preview's;
// an operation id out of form.
func TestPublicationRefusedBeforeGitHub(t *testing.T) {
	e, p := pubEnv(t)
	ctx := t.Context()
	// The links are added with no token kept, so none is read on its add.
	if status, data, _ := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK {
		t.Fatalf("the remove: %d %s", status, data)
	}
	link := func(key string) string {
		return rcOf(e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "external",
			"url": "https://github.com/octo/app/issues/13"}, http.StatusCreated, ""))["id"].(string)
	}
	own := link("PUB-1")
	other := link(e.newDispatchTicket(t, "PUB", "Other", nil)["key"].(string))
	if status, data, _ := e.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token: %d %s", status, data)
	}
	draft := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	did, digest := pubOf(t, draft)["id"].(string), draft["postSha256"].(string)
	sent := e.prepare(t, "PUB-1", map[string]any{"body": "Sent."}, http.StatusCreated, "")
	tid := pubOf(t, sent)["id"].(string)
	e.sendPub(t, tid, opID(), sent["postSha256"].(string), http.StatusOK, "")
	gone := e.prepare(t, "PUB-1", map[string]any{"body": "Gone."}, http.StatusCreated, "")
	xid := pubOf(t, gone)["id"].(string)
	e.expect(t, "POST", "/api/dispatch/publications/"+xid+"/discard", map[string]any{"opId": opID()}, http.StatusOK, "")
	p.set(func(p *pubGH) { p.closeTo = http.StatusForbidden })
	cid := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.mode, p.closeTo = postDrop, 0 })
	sending := e.prepare(t, "PUB-1", map[string]any{"body": "Sending."}, http.StatusCreated, "")
	sid, sop := pubOf(t, sending)["id"].(string), opID()
	e.sendPub(t, sid, sop, sending["postSha256"].(string), http.StatusOK, "")
	p.set(func(p *pubGH) { p.mode = postOK })
	if e.stored(t, sid).State != dispatch.PublicationSending || e.stored(t, cid).CloseState != dispatch.CloseRefused {
		t.Fatal("the publications are not as the test needs them")
	}
	type act struct {
		name, path string
		body       map[string]any
		status     int
		code       string
	}
	prep := func(name, target string, extra map[string]any, status int, code string) act {
		b := map[string]any{"opId": opID(), "target": target, "body": "Fixed in v2."}
		for k, v := range extra {
			b[k] = v
		}
		return act{"a prepare, " + name, "/api/dispatch/tickets/PUB-1/publications", b, status, code}
	}
	at := func(id, verb string) string { return "/api/dispatch/publications/" + id + "/" + verb }
	run := func(acts ...act) {
		t.Helper()
		for _, a := range acts {
			read, _, _ := e.srv.dispatch.LinkReadOf(ctx, own)
			n, reached := len(p.g.f.Requests()), e.srv.trackers.reached.Load()
			status, data, out := e.call(t, "POST", a.path, a.body)
			if status != a.status || a.code != "" && errorCode(out) != a.code {
				t.Errorf("%s: %d %s, want %d %s", a.name, status, data, a.status, a.code)
			}
			if after, _, _ := e.srv.dispatch.LinkReadOf(ctx, own); len(p.g.f.Requests()) != n || e.srv.trackers.reached.Load() != reached || !reflect.DeepEqual(after, read) {
				t.Errorf("%s: %d requests to GitHub, %d credentials read, a link's read recorded %v", a.name, len(p.g.f.Requests())-n,
					e.srv.trackers.reached.Load()-reached, !reflect.DeepEqual(after, read))
			}
		}
	}
	var acts []act
	for _, target := range []string{dispatch.TargetLinked, "link:" + own} {
		acts = append(acts,
			prep("an empty body, "+target, target, map[string]any{"body": ""}, http.StatusConflict, dispatch.RefusalInvalid),
			prep("a body of spaces, "+target, target, map[string]any{"body": " \r\n\t "}, http.StatusConflict, dispatch.RefusalInvalid),
			prep("a body past its bound, "+target, target, map[string]any{"body": strings.Repeat("a", dispatch.MaxPublicationBody+1)}, http.StatusConflict, dispatch.RefusalBound),
			prep("a control character, "+target, target, map[string]any{"body": "a\x1b[2Jb"}, http.StatusConflict, dispatch.RefusalInvalid),
			prep("a close out of form, "+target, target, map[string]any{"closeIssue": "bogus"}, http.StatusConflict, dispatch.RefusalInvalid),
			prep("a part out of form, "+target, target, map[string]any{"parts": []string{"x"}}, http.StatusConflict, dispatch.RefusalInvalid))
	}
	acts = append(acts, prep("a link of another ticket", "link:"+other, nil, http.StatusConflict, dispatch.RefusalInvalid),
		act{"a send, no such publication", at("0123456789abcdef", "send"), map[string]any{"opId": opID(), "postSha256": digest}, http.StatusNotFound, "not_found"},
		act{"a send, sent", at(tid, "send"), map[string]any{"opId": opID(), "postSha256": sent["postSha256"]}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a send, discarded", at(xid, "send"), map[string]any{"opId": opID(), "postSha256": gone["postSha256"]}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a send, a digest not the preview's", at(did, "send"), map[string]any{"opId": opID(), "postSha256": strings.Repeat("0", 64)}, http.StatusConflict, dispatch.RefusalStaleRevision},
		act{"a send, an operation id out of form", at(did, "send"), map[string]any{"opId": "not an id", "postSha256": digest}, http.StatusBadRequest, "invalid_request"},
		act{"a Check, no such publication", at("0123456789abcdef", "check"), map[string]any{}, http.StatusNotFound, "not_found"},
		act{"a Check, sent", at(tid, "check"), map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a Check, discarded", at(xid, "check"), map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a Close now, no such publication", at("0123456789abcdef", "close"), map[string]any{"opId": opID()}, http.StatusNotFound, "not_found"},
		act{"a Close now, discarded", at(xid, "close"), map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a Close now, a draft", at(did, "close"), map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid},
		act{"a discard, no such publication", at("0123456789abcdef", "discard"), map[string]any{"opId": opID()}, http.StatusNotFound, "not_found"},
		act{"a discard, sent", at(tid, "discard"), map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid})
	run(acts...)
	gones := func(name string, status int, code string) []act {
		return []act{
			prep(name+", linked", dispatch.TargetLinked, nil, status, code),
			prep(name+", a link", "link:"+own, nil, status, code),
			{"a send, " + name, at(did, "send"), map[string]any{"opId": opID(), "postSha256": digest}, status, code},
			{"a send's repeat, " + name, at(sid, "send"), map[string]any{"opId": sop, "postSha256": sending["postSha256"]}, http.StatusOK, ""},
			{"a Check, " + name, at(sid, "check"), map[string]any{}, status, code},
			{"a Close now, " + name, at(cid, "close"), map[string]any{"opId": opID()}, status, code},
			{"a discard, " + name, at(did, "discard"), map[string]any{"opId": opID()}, status, code},
		}
	}
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	run(gones("a deleted ticket", http.StatusNotFound, dispatch.RefusalNotFound)...)
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/undelete", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/projects/PUB/archive", map[string]any{"opId": opID()}, http.StatusOK, "")
	run(gones("an archived project", http.StatusConflict, dispatch.RefusalArchived)...)
	e.expect(t, "POST", "/api/dispatch/projects/PUB/unarchive", map[string]any{"opId": opID()}, http.StatusOK, "")
	if got := e.stored(t, sid); got.State != dispatch.PublicationSending {
		t.Errorf("the publication being sent: %+v", got)
	}
	// The bound on a ticket's publications, reached through the store.
	target := dispatch.PublicationTarget{NodeID: pubNodeID, SpaceID: "4000001", Kind: dispatch.ExternalIssue, URL: "https://github.com/octo/app/issues/12", Ref: "octo/app#12"}
	page, _, err := e.srv.dispatch.Publications(ctx, "PUB-1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(page); i < dispatch.MaxPublications; i++ {
		doneOf(t)(e.srv.dispatch.PreparePublication(ctx, adminOp("Alice"), dispatch.PublicationArgs{Key: "PUB-1", Target: dispatch.TargetLinked,
			Body: fmt.Sprint("n", i)}, target, e.srv.trackers.gh.Marker))
	}
	run(prep("past the bound, linked", dispatch.TargetLinked, nil, http.StatusConflict, dispatch.RefusalBound),
		prep("past the bound, a link", "link:"+own, nil, http.StatusConflict, dispatch.RefusalBound))
}

// The send's look before it asks GitHub (sendable) gives the store's
// answer for its begin (BeginSend) for every state a publication can be
// in, with the preview's digest or another, and no revision expected, the
// publication's or an older one: a draft, a failed one, one being sent,
// sent, not confirmed, discarded, failed at its bound of tries, and a
// restored draft and failed one. The store's clock moves the quiet time on
// at each of its operations, so every try is past it (the quiet time is
// TestSendLooksBeforeAgain's).
func TestSendableIsTheStores(t *testing.T) {
	e, _ := pubEnv(t)
	ctx := t.Context()
	base := time.Now()
	var ticks atomic.Int64
	e.srv.dispatch.SetClockForTest(func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * dispatch.PublicationQuiet) })
	n := 0
	target := dispatch.PublicationTarget{NodeID: pubNodeID, SpaceID: "4000001", Kind: dispatch.ExternalIssue, URL: "https://github.com/octo/app/issues/12", Ref: "octo/app#12"}
	alice := dispatch.Sender{ID: "9000001", Login: "mona-example"}
	begin := func(ds *dispatch.Store, id string) {
		if _, owned, err := ds.BeginSend(ctx, adminOp("Alice"), id, e.stored(t, id).PostSha256, alice); !owned || err != nil {
			t.Fatalf("begun: %v", err)
		}
	}
	settle := func(ds *dispatch.Store, id string, o dispatch.Outcome) {
		if _, _, err := ds.SettlePublication(ctx, id, e.stored(t, id).Attempt, o); err != nil {
			t.Fatal(err)
		}
	}
	// in makes a new publication in the state.
	in := func(ds *dispatch.Store, state string) string {
		n++
		id := doneOf(t)(ds.PreparePublication(ctx, adminOp("Alice"), dispatch.PublicationArgs{Key: "PUB-1", Target: dispatch.TargetLinked, Body: fmt.Sprint("n", n)},
			target, e.srv.trackers.gh.Marker)).ID
		switch state {
		case "discarded":
			doneOf(t)(ds.DiscardPublication(ctx, adminOp("Alice"), id))
		case "failed at its bound":
			for range dispatch.MaxPublicationTries {
				begin(ds, id)
				settle(ds, id, dispatch.Outcome{State: dispatch.PublicationFailed})
			}
		case "draft":
		default:
			begin(ds, id)
			switch state {
			case "failed", "unconfirmed":
				settle(ds, id, dispatch.Outcome{State: state})
			case "sent":
				settle(ds, id, dispatch.Outcome{State: dispatch.PublicationSent, Comment: &dispatch.SentComment{ID: fmt.Sprint(8000000000 + n), AuthorID: "9000001"}})
			}
		}
		return id
	}
	type ask struct {
		name     string
		digest   func(dispatch.Publication) string
		expected func(dispatch.Publication) int64
	}
	asks := []ask{
		{"the preview's digest", func(p dispatch.Publication) string { return p.PostSha256 }, func(dispatch.Publication) int64 { return 0 }},
		{"another digest", func(dispatch.Publication) string { return strings.Repeat("0", 64) }, func(dispatch.Publication) int64 { return 0 }},
		{"its revision", func(p dispatch.Publication) string { return p.PostSha256 }, func(p dispatch.Publication) int64 { return p.Rev }},
		{"an older revision", func(p dispatch.Publication) string { return p.PostSha256 }, func(p dispatch.Publication) int64 { return p.Rev - 1 }},
	}
	judge := func(e *testEnv, state string, id string, a ask) {
		t.Helper()
		p := e.stored(t, id)
		want := sendable(p, a.digest(p), a.expected(p))
		op := adminOp("Alice")
		op.ExpectedRev = a.expected(p)
		rc, owned, err := e.srv.dispatch.BeginSend(ctx, op, id, a.digest(p), alice)
		if err != nil || owned != want || owned != (rc.Status == dispatch.StatusDone) {
			t.Errorf("%s, %s: the look says %v, the store %v (%+v %v)", state, a.name, want, owned, rc, err)
		}
	}
	states := []string{"draft", "failed", "sending", "sent", "unconfirmed", "discarded", "failed at its bound"}
	restored := map[string][]string{}
	for _, state := range []string{"draft", "failed"} {
		for range asks {
			restored[state] = append(restored[state], in(e.srv.dispatch, state))
		}
	}
	for _, state := range states {
		for _, a := range asks {
			judge(e, state, in(e.srv.dispatch, state), a)
		}
	}
	wctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(wctx)
	if err := dispatch.Reissue(ctx, filepath.Join(e.srv.store.Dir(), "dispatch", dispatch.File)); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	for _, state := range []string{"draft", "failed"} {
		for i, a := range asks {
			judge(e2, "restored "+state, restored[state][i], a)
		}
	}
}

// The post is made only by the request whose begin of the send the store
// says it owns: a publication discarded, or its ticket deleted, after the
// send's look passed and before its begin (while the account is read on
// GitHub) is refused by the store with its receipt, nothing posted, the
// row as the store left it.
func TestSendOwnsItsPost(t *testing.T) {
	e, p := pubEnv(t)
	ctx := t.Context()
	for _, c := range []struct {
		name   string
		status int
		code   string
		act    func(id string)
		state  string
		undo   func()
	}{
		{"discarded", http.StatusConflict, dispatch.RefusalInvalid, func(id string) { doneOf(t)(e.srv.dispatch.DiscardPublication(ctx, adminOp("Bob"), id)) },
			dispatch.PublicationDiscarded, func() {}},
		{"its ticket deleted", http.StatusNotFound, dispatch.RefusalNotFound, func(string) { doneOf(t)(e.srv.dispatch.DeleteTicket(ctx, adminOp("Bob"), "PUB-1")) },
			dispatch.PublicationDraft, func() { doneOf(t)(e.srv.dispatch.UndeleteTicket(ctx, adminOp("Bob"), "PUB-1")) }},
	} {
		out := e.prepare(t, "PUB-1", map[string]any{"body": c.name}, http.StatusCreated, "")
		id := pubOf(t, out)["id"].(string)
		p.set(func(p *pubGH) {
			p.onUser = func() {
				p.mu.Lock()
				p.onUser = nil
				p.mu.Unlock()
				c.act(id)
			}
		})
		adds := p.adds()
		got := e.sendPub(t, id, opID(), out["postSha256"].(string), c.status, c.code)
		if rcOf(got) == nil || p.adds() != adds || e.stored(t, id).State != c.state || e.stored(t, id).Tries != 0 {
			t.Errorf("%s: %v, %d posts, %+v", c.name, got, p.adds()-adds, e.stored(t, id))
		}
		c.undo()
	}
}

// A publication of another instance (a restored copy) is never closed
// here: Check finds the comment the original sent and records it sent,
// with no close pending and nothing for a person to act on; Close now is
// refused before any credential is read or anything is asked of GitHub.
func TestRestoredPublicationNeverClosed(t *testing.T) {
	e, p := pubEnv(t)
	draft := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
	id := pubOf(t, draft)["id"].(string)
	p.add(pubNodeID, draft["preview"].(string), bobPub, time.Now())
	wctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(wctx)
	if err := dispatch.Reissue(t.Context(), filepath.Join(e.srv.store.Dir(), "dispatch", dispatch.File)); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	got := pubOf(t, e2.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, ""))
	if got["state"] != "sent" || got["closeState"] != nil || got["sentAsId"] != "9000002" || got["restored"] != true {
		t.Fatalf("the check: %v", got)
	}
	if page := e2.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications?needYou=1", nil, http.StatusOK, ""); len(page["publications"].([]any)) != 0 {
		t.Errorf("needing a person: %v", page)
	}
	n, reached, closes := len(p.g.f.Requests()), e2.srv.trackers.reached.Load(), p.closes()
	e2.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid)
	if len(p.g.f.Requests()) != n || e2.srv.trackers.reached.Load() != reached || p.closes() != closes {
		t.Fatalf("Close now of a restored publication: %d requests to GitHub, %d credentials read", len(p.g.f.Requests())-n, e2.srv.trackers.reached.Load()-reached)
	}
}

// Two Close now requests that both looked at the open issue (the
// publication's lock held by neither, as two servers over one store would
// be; the seam holds each after its look until both have looked): the
// store's begin of the close is one of theirs, and only that one closes:
// one close on GitHub, one more try.
func TestCloseNowOnceAtOneRevision(t *testing.T) {
	e, p := pubEnv(t)
	p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
	id := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = 0 })
	pub, closes := e.stored(t, id), p.closes()
	if pub.CloseState != dispatch.CloseClosing || pub.CloseTries != 1 {
		t.Fatalf("before: %+v", pub)
	}
	looked, both := make(chan struct{}, 2), make(chan struct{})
	e.srv.external.publishStep = func(_ context.Context, at, _ string) {
		if at == "looked" {
			looked <- struct{}{}
			<-both
		}
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- e.srv.closeNow(t.Context(), e.srv.dispatch, pub) }()
	}
	<-looked
	<-looked
	close(both)
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("a Close now: %v", err)
		}
	}
	e.srv.external.publishStep = nil
	if got := e.stored(t, id); p.closes()-closes != 1 || got.CloseTries != 2 || got.CloseState != dispatch.CloseClosed {
		t.Fatalf("two Close now at one revision: %d closes, %+v", p.closes()-closes, got)
	}
}

// Close now looks at the item first. An issue found closed whose close
// was begun and never answered is settled closed from where it is, its
// tries as they were, with the words that it was found closed: after the
// fifth close (GitHub made it, its answer lost) and after the first. An
// issue closed on GitHub before any close of Conductor's was begun is
// closed with the words that say so. An open issue at five tries: the
// look, then the bound's refusal.
func TestCloseNowLooksFirst(t *testing.T) {
	// closing is a sent publication whose close was begun tries times, the
	// last close made on GitHub with its answer lost when made.
	closing := func(t *testing.T, tries int, made bool) (*testEnv, *pubGH, string) {
		e, p := pubEnv(t)
		p.set(func(p *pubGH) { p.closeTo, p.lostShut = http.StatusBadGateway, made && tries == 1 })
		id := sentWithClose(t, e, p)
		for i := 2; i <= tries; i++ {
			p.set(func(p *pubGH) { p.lostShut = made && i == tries })
			e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, "")
		}
		p.set(func(p *pubGH) { p.closeTo, p.lostShut = 0, false })
		if got := e.stored(t, id); got.CloseState != dispatch.CloseClosing || got.CloseTries != tries {
			t.Fatalf("closing at %d: %+v", tries, got)
		}
		return e, p, id
	}
	for _, tries := range []int{dispatch.MaxPublicationCloseTries, 1} {
		t.Run(fmt.Sprint("found closed at ", tries), func(t *testing.T) {
			e, p, id := closing(t, tries, true)
			closes := p.closes()
			got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
			if got["closeState"] != "closed" || got["closeError"] != settleWords(saidClosedFound, nil) || got["closeTries"] != float64(tries) || p.closes() != closes {
				t.Fatalf("Close now: %v, %d closes sent", got, p.closes()-closes)
			}
		})
	}
	t.Run("closed before any begin", func(t *testing.T) {
		e, p := pubEnv(t)
		out := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
		id := pubOf(t, out)["id"].(string)
		if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), id, out["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
			t.Fatal(err)
		}
		cm := p.add(pubNodeID, out["preview"].(string), alicePub, time.Now())
		if _, _, err := e.srv.dispatch.SettlePublication(t.Context(), id, 1, dispatch.Outcome{State: dispatch.PublicationSent,
			Comment: &dispatch.SentComment{ID: strconv.FormatInt(cm.id, 10), AuthorID: "9000001", Author: "mona-example"}}); err != nil {
			t.Fatal(err)
		}
		p.edit(pubNodeID, func(n *pubNode) { n.state = "CLOSED" })
		got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
		if got["closeState"] != "closed" || got["closeError"] != settleWords(saidClosedBefore, nil) || p.closes() != 0 {
			t.Fatalf("Close now: %v, %d closes sent", got, p.closes())
		}
	})
	t.Run("open at the bound", func(t *testing.T) {
		e, p, id := closing(t, dispatch.MaxPublicationCloseTries, false)
		closes, looks := p.closes(), p.g.count("api.github.com", "/graphql")
		e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalBound)
		if got := e.stored(t, id); p.closes() != closes || p.g.count("api.github.com", "/graphql") != looks+1 || got.CloseTries != dispatch.MaxPublicationCloseTries {
			t.Fatalf("open at the bound: %d closes, %d requests, %+v", p.closes()-closes, p.g.count("api.github.com", "/graphql")-looks, got)
		}
	})
}

// A send whose try comes after an earlier one looks before it posts, and
// decides by the publication's marker: a comment the earlier try made
// though GitHub refused it, by this try's account with the exact bytes, is
// recorded as sent, its id and its address, nothing posted; an earlier try
// that made nothing: posted once. Any other comment holding the marker
// (the earlier try's, the token then another account's; the earlier try's
// edited on the item; another account's that quotes it): nothing posted,
// one comment holding it on the item, the try failed in the fixed words.
// A listing not whole, a listing that fails, an item that moved: nothing
// posted, failed with their words, to be sent again. Within the quiet time
// after the earlier try began, the send is not begun: 503 tracker_busy with
// the seconds left, the row as it was, and the same operation id after the
// quiet time posts once. A first send lists nothing before its post.
func TestSendLooksBeforeAgain(t *testing.T) {
	failedOnce := func(t *testing.T, mode string) (*testEnv, *pubGH, map[string]any) {
		e, p := pubEnv(t)
		out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
		p.set(func(p *pubGH) { p.mode = mode })
		if got := pubOf(t, e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, "")); got["state"] != "failed" {
			t.Fatalf("the first try: %v", got)
		}
		p.set(func(p *pubGH) { p.mode = postOK })
		return e, p, out
	}
	// pastQuiet moves the publications' clock and the store's past the
	// quiet time after the earlier try began, as the store holds it.
	pastQuiet := func(t *testing.T, e *testEnv, out map[string]any) {
		ahead := time.Until(e.stored(t, pubOf(t, out)["id"].(string)).SendingAt.Add(dispatch.PublicationQuiet + time.Second))
		bothClocks(e, func() time.Time { return time.Now().Add(ahead) })
	}
	again := func(t *testing.T, e *testEnv, out map[string]any) map[string]any {
		t.Helper()
		pastQuiet(t, e, out)
		return pubOf(t, e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, ""))
	}
	t.Run("made, though refused", func(t *testing.T) {
		e, p, out := failedOnce(t, postMadeRefused)
		first, adds := p.commented(pubNodeID)[0], p.adds()
		got := again(t, e, out)
		if got["state"] != "sent" || got["remoteId"] != strconv.FormatInt(first.id, 10) || got["remoteUrl"] != first.url || p.adds() != adds || len(p.commented(pubNodeID)) != 1 {
			t.Fatalf("the second try: %v, %d posts, %d comments", got, p.adds()-adds, len(p.commented(pubNodeID)))
		}
	})
	t.Run("nothing made", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		adds := p.adds()
		got := again(t, e, out)
		if got["state"] != "sent" || p.adds() != adds+1 || len(p.commented(pubNodeID)) != 1 {
			t.Fatalf("the second try: %v, %d posts", got, p.adds()-adds)
		}
	})
	marked := func(t *testing.T, e *testEnv, p *pubGH, out map[string]any) {
		t.Helper()
		adds := p.adds()
		got := again(t, e, out)
		var holding int
		for _, c := range p.commented(pubNodeID) {
			if strings.Contains(c.body, e.stored(t, pubOf(t, out)["id"].(string)).Marker) {
				holding++
			}
		}
		if got["state"] != "failed" || got["error"] != settleWords(saidMarkedOther, nil) || got["tries"] != float64(2) || p.adds() != adds || holding != 1 {
			t.Fatalf("the second try: %v, %d posts, %d comments holding the marker", got, p.adds()-adds, holding)
		}
	}
	t.Run("made, though refused, then another account's token", func(t *testing.T) {
		e, p, out := failedOnce(t, postMadeRefused)
		if status, data, _ := e.saveToken(t, "", plantedToken2); status != http.StatusOK {
			t.Fatalf("Bob's token: %d %s", status, data)
		}
		marked(t, e, p, out)
	})
	t.Run("made, though refused, then edited on the item", func(t *testing.T) {
		e, p, out := failedOnce(t, postMadeRefused)
		marker := e.stored(t, pubOf(t, out)["id"].(string)).Marker
		p.set(func(p *pubGH) { p.comments[0].body = "Edited on GitHub.\n\n" + marker })
		marked(t, e, p, out)
	})
	t.Run("another account's comment quoting the marker", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		p.add(pubNodeID, "Is "+e.stored(t, pubOf(t, out)["id"].(string)).Marker+" this one?", bobPub, time.Now())
		marked(t, e, p, out)
	})
	nothing := func(t *testing.T, e *testEnv, p *pubGH, out map[string]any, words string) {
		t.Helper()
		adds, comments := p.adds(), len(p.commented(pubNodeID))
		got := again(t, e, out)
		if got["state"] != "failed" || got["error"] != words || got["tries"] != float64(2) || p.adds() != adds || len(p.commented(pubNodeID)) != comments {
			t.Fatalf("the second try: %v, %d posts", got, p.adds()-adds)
		}
	}
	t.Run("more comments than the look lists", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		for i := range github.MaxCommentPages*100 + 1 {
			p.add(pubNodeID, fmt.Sprint("another comment ", i), bobPub, time.Now())
		}
		nothing(t, e, p, out, settleWords(saidNotLookedWhole, nil))
	})
	t.Run("a listing that fails", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		p.set(func(p *pubGH) { p.listTo = http.StatusBadGateway })
		nothing(t, e, p, out, settleWords(saidNotLookedFor, errors.New("x")))
	})
	t.Run("the item moved before the look", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		// Moved after the send's resolve, as its account is read.
		p.set(func(p *pubGH) {
			p.onUser = func() { p.edit(pubNodeID, func(n *pubNode) { n.repo, n.name = 4000002, "other/app" }) }
		})
		nothing(t, e, p, out, settleWords(saidTargetChanged, nil))
	})
	t.Run("within the quiet time", func(t *testing.T) {
		e, p, out := failedOnce(t, postRefused)
		id, op := pubOf(t, out)["id"].(string), opID()
		before, adds := e.stored(t, id), p.adds()
		for _, c := range []struct {
			name  string
			at    time.Duration
			retry string
		}{
			// 10.5 s after the earlier try began: 20 s left, rounded up.
			{"10.5 s after", 10*time.Second + 500*time.Millisecond, "20"},
			// A clock 5 s behind the earlier try's start: 35 s left.
			{"a clock behind", -5 * time.Second, "35"},
		} {
			at := before.SendingAt.Add(c.at)
			bothClocks(e, func() time.Time { return at })
			n, reached := len(p.g.f.Requests()), e.srv.trackers.reached.Load()
			resp, got := e.do("POST", "/api/dispatch/publications/"+id+"/send", adminToken, map[string]any{"opId": op, "postSha256": out["postSha256"]})
			if resp.StatusCode != http.StatusServiceUnavailable || errorCode(got) != "tracker_busy" || apiMessage(got) != errTryTooSoon.Message || resp.Header.Get("Retry-After") != c.retry {
				t.Fatalf("%s: %d %v, Retry-After %q", c.name, resp.StatusCode, got, resp.Header.Get("Retry-After"))
			}
			if after := e.stored(t, id); after.Rev != before.Rev || after.Tries != 1 || p.adds() != adds || len(p.g.f.Requests()) != n || e.srv.trackers.reached.Load() != reached {
				t.Fatalf("%s: %+v, %d posts, %d requests, %d credentials read", c.name, after, p.adds()-adds, len(p.g.f.Requests())-n, e.srv.trackers.reached.Load()-reached)
			}
		}
		// At exactly the quiet time, the same operation id posts once.
		at := before.SendingAt.Add(dispatch.PublicationQuiet)
		bothClocks(e, func() time.Time { return at })
		if got := pubOf(t, e.sendPub(t, id, op, out["postSha256"].(string), http.StatusOK, "")); got["state"] != "sent" || got["tries"] != float64(2) || p.adds() != adds+1 {
			t.Fatalf("the same operation at the quiet time: %v, %d posts", got, p.adds()-adds)
		}
	})
	t.Run("a try another server began and settled meanwhile", func(t *testing.T) {
		e, p := pubEnv(t)
		out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
		id, digest, op := pubOf(t, out)["id"].(string), out["postSha256"].(string), opID()
		ds2, err := dispatch.OpenOptions(filepath.Join(e.srv.store.Dir(), "dispatch"), dispatch.Options{Systems: []string{"github"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ds2.Close()
		// The send has read the publication as a draft and reads its
		// account; meanwhile the second server begins a try and settles it
		// failed, as one whose post GitHub refused.
		begun := make(chan error, 1)
		p.set(func(p *pubGH) {
			p.onUser = func() {
				p.mu.Lock()
				p.onUser = nil
				p.mu.Unlock()
				_, owned, err := ds2.BeginSend(context.Background(), adminOp("Bob"), id, digest, dispatch.Sender{ID: strconv.FormatInt(alicePub.id, 10), Login: alicePub.login})
				if err == nil && !owned {
					err = errors.New("not owned")
				}
				if err == nil {
					_, _, err = ds2.SettlePublication(context.Background(), id, 1, dispatch.Outcome{State: dispatch.PublicationFailed, Words: "refused"})
				}
				begun <- err
			}
		})
		adds := p.adds()
		resp, got := e.do("POST", "/api/dispatch/publications/"+id+"/send", adminToken, map[string]any{"opId": op, "postSha256": digest})
		select {
		case err := <-begun:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("the account was not read")
		}
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if resp.StatusCode != http.StatusServiceUnavailable || errorCode(got) != "tracker_busy" || apiMessage(got) != errTryTooSoon.Message || retry < 1 || retry > 30 {
			t.Fatalf("the send after another server's try: %d %v, Retry-After %q", resp.StatusCode, got, resp.Header.Get("Retry-After"))
		}
		before := e.stored(t, id)
		if _, found, err := e.srv.dispatch.Receipt(t.Context(), adminActor, op); before.State != dispatch.PublicationFailed || before.Tries != 1 || p.adds() != adds || found || err != nil {
			t.Fatalf("the send after another server's try: %+v, %d posts, a receipt %v %v", before, p.adds()-adds, found, err)
		}
		ahead := time.Until(before.SendingAt.Add(dispatch.PublicationQuiet + time.Second))
		bothClocks(e, func() time.Time { return time.Now().Add(ahead) })
		if got := pubOf(t, e.sendPub(t, id, op, digest, http.StatusOK, "")); got["state"] != "sent" || got["tries"] != float64(2) || p.adds() != adds+1 {
			t.Fatalf("the same operation after the quiet time: %v, %d posts", got, p.adds()-adds)
		}
	})
	t.Run("a first send", func(t *testing.T) {
		e, p := pubEnv(t)
		out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
		listings := p.listings()
		if got := pubOf(t, e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, "")); got["state"] != "sent" || p.listings() != listings {
			t.Fatalf("a first send: %v, %d listings", got, p.listings()-listings)
		}
	})
}

// bothClocks sets the publications' clock and the store's to now.
func bothClocks(e *testEnv, now func() time.Time) {
	e.srv.external.now = now
	e.srv.dispatch.SetClockForTest(now)
}

// frozenClock makes the publications' clock one that moves only when the
// test moves it (the func given), from the moment it is set.
func frozenClock(e *testEnv) func(time.Duration) {
	base := time.Now()
	var ahead atomic.Int64
	e.srv.external.now = func() time.Time { return base.Add(time.Duration(ahead.Load())) }
	return func(d time.Duration) { ahead.Add(int64(d)) }
}

// Each step of a close has its own cap from the moment it starts: a step
// of the store's that used most of its cap before the close (the
// publications' clock, which only the test moves, moved on at the seam
// after the close's begin) leaves the close its whole cap, its deadline
// exactly the cap after the clock's reading that made it.
func TestCloseHasItsOwnCap(t *testing.T) {
	e, p := pubEnv(t)
	var left atomic.Int64
	p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
	id := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = 0 })
	caps := e.srv.trackerCaps()
	move := frozenClock(e)
	left.Store(-1)
	e.srv.external.publishStep = func(ctx context.Context, at, _ string) {
		switch at {
		case "begun":
			move(caps.write - time.Millisecond)
		case "closing":
			if d, ok := ctx.Deadline(); ok {
				left.Store(int64(d.Sub(e.srv.publishNow())))
			}
		}
	}
	got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
	e.srv.external.publishStep = nil
	if l := time.Duration(left.Load()); got["closeState"] != "closed" || l != caps.post {
		t.Fatalf("the close's time left as it was sent: %s of %s (%v)", l, caps.post, got)
	}
}

// Each step of a send has its own cap from the moment it starts: the post
// that used most of its cap (the clock moved at the seam before it) leaves
// the listing after a lost answer its whole cap; each deadline is exactly
// the cap after the clock's reading that made it.
func TestSendStepsHaveTheirOwnCaps(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	caps := e.srv.trackerCaps()
	p.set(func(p *pubGH) { p.mode = postLost })
	move := frozenClock(e)
	var mu sync.Mutex
	left := map[string]time.Duration{}
	e.srv.external.publishStep = func(ctx context.Context, at, _ string) {
		if at != "posting" && at != "listing" {
			return
		}
		if d, ok := ctx.Deadline(); ok {
			mu.Lock()
			left[at] = d.Sub(e.srv.publishNow())
			mu.Unlock()
		}
		if at == "posting" {
			move(caps.post - time.Millisecond)
		}
	}
	got := pubOf(t, e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, ""))
	e.srv.external.publishStep = nil
	mu.Lock()
	defer mu.Unlock()
	if got["state"] != "sent" || left["posting"] != caps.post || left["listing"] != caps.post || p.adds() != 1 {
		t.Fatalf("a send's steps: %v, time left %v of %s", got, left, caps.post)
	}
}

// Each step of a prepare has its own cap from the moment it starts: a
// link's read and its record that together used more than the resolve's
// whole cap (the clock moved at the seam after the record) leave the
// resolve its own, its deadline exactly the cap after the clock's reading
// that made it.
func TestPrepareStepsHaveTheirOwnCaps(t *testing.T) {
	e, p := pubEnv(t)
	p.g.set(13, ghItem{Node: "I_kwDOitem13", Title: "Another issue", Body: "b", Updated: t0, Labels: []string{}})
	p.node("I_kwDOitem13", "Issue", "octo/app", 13, 4000001)
	link := rcOf(e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/links", map[string]any{"opId": opID(), "kind": "external",
		"url": "https://github.com/octo/app/issues/13"}, http.StatusCreated, ""))["id"].(string)
	caps := e.srv.trackerCaps()
	move := frozenClock(e)
	var left atomic.Int64
	left.Store(-1)
	e.srv.external.publishStep = func(ctx context.Context, at, _ string) {
		switch at {
		case "recorded":
			move(caps.reads + caps.write)
		case "resolving":
			if d, ok := ctx.Deadline(); ok {
				left.Store(int64(d.Sub(e.srv.publishNow())))
			}
		}
	}
	got := pubOf(t, e.prepare(t, "PUB-1", map[string]any{"target": "link:" + link}, http.StatusCreated, ""))
	e.srv.external.publishStep = nil
	if l := time.Duration(left.Load()); got["targetNodeId"] != "I_kwDOitem13" || l != caps.reads {
		t.Fatalf("the resolve's time left as it began: %s of %s (%v)", l, caps.reads, got)
	}
}

// A listing past the look's bound (more comments than its pages hold) is
// one not whole, never one without the comment: past the quiet time a
// Check leaves the publication being sent, with a listing not whole's
// words.
func TestCheckPastTheLooksBound(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	p.set(func(p *pubGH) { p.mode = postDrop })
	e.sendPub(t, id, opID(), out["postSha256"].(string), http.StatusOK, "")
	p.set(func(p *pubGH) { p.mode = postOK })
	for i := range github.MaxCommentPages*100 + 1 {
		p.add(pubNodeID, fmt.Sprint("another comment ", i), bobPub, time.Now())
	}
	ahead := time.Until(e.stored(t, id).SendingAt.Add(dispatch.PublicationQuiet + time.Second))
	e.srv.external.now = func() time.Time { return time.Now().Add(ahead) }
	got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/check", map[string]any{}, http.StatusOK, ""))
	if got["state"] != "sending" || got["error"] != settleWords(saidNotWhole, nil) {
		t.Fatalf("a Check past the look's bound: %v", got)
	}
}

// pendingClose is a sent publication whose close was asked for and never
// begun (a send cut after its comment's settle), Alice's comment on the
// fake.
func pendingClose(t *testing.T, e *testEnv, p *pubGH) string {
	t.Helper()
	out := e.prepare(t, "PUB-1", map[string]any{"closeIssue": "completed"}, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), id, out["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
		t.Fatal(err)
	}
	cm := p.add(pubNodeID, out["preview"].(string), alicePub, time.Now())
	if _, _, err := e.srv.dispatch.SettlePublication(t.Context(), id, 1, dispatch.Outcome{State: dispatch.PublicationSent,
		Comment: &dispatch.SentComment{ID: strconv.FormatInt(cm.id, 10), AuthorID: "9000001", Author: "mona-example"}}); err != nil {
		t.Fatal(err)
	}
	return id
}

// An issue found closed is recorded closed whatever its close's state and
// count of tries, by one look and no close sent, no try spent: a close
// GitHub refused five times, then closed on the item, after which the
// publication needs no person; a pending close (no close begun: the words
// that it was closed before Conductor's); a close begun once and refused
// (the words that it was found closed).
func TestCloseFoundClosedWhateverItsState(t *testing.T) {
	for _, c := range []struct {
		name  string
		tries int
		words string
	}{
		{"refused at every try", dispatch.MaxPublicationCloseTries, settleWords(saidClosedFound, nil)},
		{"pending", 0, settleWords(saidClosedBefore, nil)},
		{"refused once", 1, settleWords(saidClosedFound, nil)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, p := pubEnv(t)
			var id string
			if c.tries == 0 {
				id = pendingClose(t, e, p)
			} else {
				p.set(func(p *pubGH) { p.closeTo = http.StatusForbidden })
				id = sentWithClose(t, e, p)
				for range c.tries - 1 {
					e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, "")
				}
				p.set(func(p *pubGH) { p.closeTo = 0 })
			}
			want := map[bool]string{true: dispatch.ClosePending, false: dispatch.CloseRefused}[c.tries == 0]
			if got := e.stored(t, id); got.CloseState != want || got.CloseTries != c.tries {
				t.Fatalf("before: %+v", got)
			}
			p.edit(pubNodeID, func(n *pubNode) { n.state = "CLOSED" })
			closes, looks := p.closes(), p.g.count("api.github.com", "/graphql")
			got := pubOf(t, e.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusOK, ""))
			if got["closeState"] != "closed" || got["closeError"] != c.words || got["closeTries"] != float64(c.tries) || p.closes() != closes ||
				p.g.count("api.github.com", "/graphql") != looks+1 {
				t.Fatalf("Close now: %v, %d closes sent, %d requests", got, p.closes()-closes, p.g.count("api.github.com", "/graphql")-looks)
			}
			if page := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications?needYou=1", nil, http.StatusOK, ""); len(page["publications"].([]any)) != 0 {
				t.Errorf("needing a person: %v", page)
			}
		})
	}
}

// Two closes of one publication from two stores over one data directory
// (as two servers would be, the publication's lock ordering neither): the
// second begun after the first's begin and before its close was sent (the
// seam holding the first there). GitHub is asked twice to close the one
// issue, the row ends closed with both tries counted, and both answer
// without an error (the route's 200): the first's answer, for a close the
// second recorded closed, changes nothing.
func TestCloseFromTwoServersOneDirectory(t *testing.T) {
	e, p := pubEnv(t)
	p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
	id := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = 0 })
	ds2, err := dispatch.OpenOptions(filepath.Join(e.srv.store.Dir(), "dispatch"), dispatch.Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ds2.Close()
	closes := p.closes()
	var at atomic.Int32
	held, release := make(chan struct{}), make(chan struct{})
	e.srv.external.publishStep = func(_ context.Context, step, _ string) {
		if step == "closing" && at.Add(1) == 1 {
			close(held)
			<-release
		}
	}
	first := make(chan int, 1)
	go func() {
		status, _, _ := e.call(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()})
		first <- status
	}()
	<-held
	if got := e.stored(t, id); got.CloseState != dispatch.CloseClosing || got.CloseTries != 2 {
		t.Fatalf("the first begun: %+v", got)
	}
	pub, err := ds2.Publication(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	second := e.srv.closeNow(t.Context(), ds2, pub)
	close(release)
	status := <-first
	e.srv.external.publishStep = nil
	if got := e.stored(t, id); status != http.StatusOK || second != nil || p.closes()-closes != 2 || got.CloseState != dispatch.CloseClosed || got.CloseTries != 3 {
		t.Fatalf("two servers' closes: %d, %v, %d closes, %+v", status, second, p.closes()-closes, got)
	}
}

// What the server holds refuses a prepare before any credential is read,
// any request is made to GitHub or any link's read is recorded, the
// refusal kept with its receipt and a repeat of the operation answering it:
// a revision expected that is not the ticket's (linked, and a link); a
// close of an item held as a pull request (a link by its address, and one
// by its last read); a linked target of a ticket with no linked item.
func TestPrepareRefusedFromWhatIsHeld(t *testing.T) {
	e, p := pubEnv(t)
	ctx := t.Context()
	p.g.set(13, ghItem{Node: "PR_kwDOitem13", Title: "A pull request", Body: "b", Updated: t0, Pull: true, Labels: []string{}})
	p.node("PR_kwDOitem13", "PullRequest", "octo/app", 13, 4000001)
	link := func(url string) string {
		return rcOf(e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/links", map[string]any{"opId": opID(), "kind": "external", "url": url}, http.StatusCreated, ""))["id"].(string)
	}
	issue, byAddress, byRead := link("https://github.com/octo/app/issues/14"), link("https://github.com/octo/app/pull/15"), link("https://github.com/octo/app/issues/13")
	// The link to #13 read, as a prepare reads it: GitHub's pull request.
	e.prepare(t, "PUB-1", map[string]any{"target": "link:" + byRead}, http.StatusCreated, "")
	if lr, _, err := e.srv.dispatch.LinkReadOf(ctx, byRead); err != nil || lr.Kind != dispatch.ExternalChange {
		t.Fatalf("the link's read: %+v %v", lr, err)
	}
	native := e.newDispatchTicket(t, "PUB", "Native", nil)["key"].(string)
	tv, err := e.srv.dispatch.Ticket(ctx, "PUB-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, key string
		body      map[string]any
		code      string
	}{
		{"an older revision, linked", "PUB-1", map[string]any{"expectedRev": tv.Rev - 1}, dispatch.RefusalStaleRevision},
		{"an older revision, a link", "PUB-1", map[string]any{"expectedRev": tv.Rev - 1, "target": "link:" + issue}, dispatch.RefusalStaleRevision},
		{"a close of a pull request's address", "PUB-1", map[string]any{"target": "link:" + byAddress, "closeIssue": "completed"}, dispatch.RefusalInvalid},
		{"a close of a link read as a pull request", "PUB-1", map[string]any{"target": "link:" + byRead, "closeIssue": "completed"}, dispatch.RefusalInvalid},
		{"linked, with no linked item", native, nil, dispatch.RefusalInvalid},
	} {
		body := map[string]any{"opId": opID(), "target": "linked", "body": "Fixed in v2."}
		for k, v := range c.body {
			body[k] = v
		}
		reads := map[string]dispatch.LinkRead{}
		for _, l := range []string{issue, byAddress, byRead} {
			reads[l], _, _ = e.srv.dispatch.LinkReadOf(ctx, l)
		}
		n, reached := len(p.g.f.Requests()), e.srv.trackers.reached.Load()
		first := e.expect(t, "POST", "/api/dispatch/tickets/"+c.key+"/publications", body, http.StatusConflict, c.code)
		repeat := e.expect(t, "POST", "/api/dispatch/tickets/"+c.key+"/publications", body, http.StatusConflict, c.code)
		if rc := rcOf(first); rc == nil || rcOf(repeat)["opId"] != rc["opId"] || apiMessage(repeat) != apiMessage(first) {
			t.Errorf("%s: %v, then %v", c.name, first, repeat)
		}
		if len(p.g.f.Requests()) != n || e.srv.trackers.reached.Load() != reached {
			t.Errorf("%s: %d requests to GitHub, %d credentials read", c.name, len(p.g.f.Requests())-n, e.srv.trackers.reached.Load()-reached)
		}
		for l, before := range reads {
			if after, _, _ := e.srv.dispatch.LinkReadOf(ctx, l); !reflect.DeepEqual(after, before) {
				t.Errorf("%s: a link's read recorded", c.name)
			}
		}
	}
}

// Two prepares of one operation id at once: the second, past its first
// replay and its judgement, waits at the target's lock while the first,
// holding it, has read its credential; the token is then removed, and the
// first commits. The second, once it holds the lock, answers the first's
// receipt, asking nothing of the credentials or GitHub.
func TestPrepareReplaysUnderItsLock(t *testing.T) {
	e, p := pubEnv(t)
	body := map[string]any{"opId": opID(), "target": "linked", "body": "Fixed in v2."}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.external.publishStep = func(_ context.Context, at, _ string) {
		if at == "resolving" {
			once.Do(func() { close(held); <-release })
		}
	}
	waiting := make(chan struct{})
	var w sync.Once
	e.srv.external.waiting = func(key string) {
		if key == "ticket:PUB-1" {
			w.Do(func() { close(waiting) })
		}
	}
	first := preparing(t, e, body)
	<-held
	if status, data, _ := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK {
		t.Fatalf("the remove: %d %s", status, data)
	}
	second := preparing(t, e, body)
	<-waiting
	reached, n := e.srv.trackers.reached.Load(), len(p.g.f.Requests())
	close(release)
	// Each reply by its own request, whichever arrives first.
	a, b := <-first, <-second
	e.srv.external.publishStep, e.srv.external.waiting = nil, nil
	if a.status != http.StatusCreated || b.status != http.StatusOK || pubOf(t, a.out)["id"] != pubOf(t, b.out)["id"] || rcOf(b.out)["repeat"] != true {
		t.Fatalf("two prepares of one operation: %d %v, then %d %v", a.status, a.out, b.status, b.out)
	}
	// The first's resolve and nothing of the second's.
	if e.srv.trackers.reached.Load() != reached || len(p.g.f.Requests()) != n+1 {
		t.Fatalf("%d credentials read, %d requests to GitHub after the second waited", e.srv.trackers.reached.Load()-reached, len(p.g.f.Requests())-n)
	}
}

// pubReply is a request's reply: its status and body.
type pubReply struct {
	status int
	out    map[string]any
}

// preparing posts a prepare of PUB-1 with the body, and gives the channel
// its own reply arrives on.
func preparing(t *testing.T, e *testEnv, body map[string]any) chan pubReply {
	reply := make(chan pubReply, 1)
	go func() {
		status, _, out := e.call(t, "POST", "/api/dispatch/tickets/PUB-1/publications", body)
		reply <- pubReply{status, out}
	}()
	return reply
}

// A prepare that waited at the target's lock is judged again once it holds
// it, before any credential is read: two different prepares, the first
// holding the lock with its credential read, the second waiting, the token
// then removed. When the first takes the ticket's publications to their
// bound, when the ticket's revision moves past the one the second expects,
// and when the project is archived meanwhile, the second is refused as the
// store judges it, kept with its receipt (a repeat answers the same), with
// no credential read and nothing asked of GitHub.
func TestPrepareJudgedAgainUnderItsLock(t *testing.T) {
	for _, c := range []struct {
		name, code string
		meanwhile  func(t *testing.T, e *testEnv)
	}{
		{"the bound", dispatch.RefusalBound, nil},
		{"the revision moved", dispatch.RefusalStaleRevision, func(t *testing.T, e *testEnv) {
			acceptance := "Moved on."
			doneOf(t)(e.srv.dispatch.EditTicket(t.Context(), adminOp("Bob"), "PUB-1", dispatch.TicketEdit{Set: dispatch.TicketSet{Acceptance: &acceptance}}))
		}},
		{"the project archived", dispatch.RefusalArchived, func(t *testing.T, e *testEnv) {
			doneOf(t)(e.srv.dispatch.ArchiveProject(t.Context(), adminOp("Bob"), "PUB"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, p := pubEnv(t)
			if c.meanwhile == nil {
				// The ticket one publication short of its bound before the
				// first.
				kept := e.stored(t, pubOf(t, e.prepare(t, "PUB-1", nil, http.StatusCreated, ""))["id"].(string))
				target := dispatch.PublicationTarget{NodeID: kept.TargetNodeID, SpaceID: kept.TargetSpaceID, Kind: kept.TargetKind, URL: kept.TargetURL, Ref: kept.TargetRef}
				for i := 1; i < dispatch.MaxPublications-1; i++ {
					doneOf(t)(e.srv.dispatch.PreparePublication(t.Context(), adminOp("Alice"), dispatch.PublicationArgs{Key: "PUB-1", Target: dispatch.TargetLinked, Body: fmt.Sprint("n", i)},
						target, e.srv.trackers.gh.Marker))
				}
			}
			tv, err := e.srv.dispatch.Ticket(t.Context(), "PUB-1")
			if err != nil {
				t.Fatal(err)
			}
			held, release := make(chan struct{}), make(chan struct{})
			free := sync.OnceFunc(func() { close(release) })
			t.Cleanup(free) // a failed check before the release lets the first go
			var once sync.Once
			e.srv.external.publishStep = func(_ context.Context, at, _ string) {
				if at == "resolving" {
					once.Do(func() { close(held); <-release })
				}
			}
			waiting := make(chan struct{})
			var w sync.Once
			e.srv.external.waiting = func(key string) {
				if key == "ticket:PUB-1" {
					w.Do(func() { close(waiting) })
				}
			}
			first := preparing(t, e, map[string]any{"opId": opID(), "target": "linked", "body": "The first."})
			<-held
			if status, data, _ := e.callAs(t, adminToken, "POST", "/api/trackers/github/remove", map[string]any{"host": "github.com"}); status != http.StatusOK {
				t.Fatalf("the remove: %d %s", status, data)
			}
			body := map[string]any{"opId": opID(), "target": "linked", "body": "The second."}
			if c.code == dispatch.RefusalStaleRevision {
				body["expectedRev"] = tv.Rev
			}
			second := preparing(t, e, body)
			select {
			case <-waiting:
			case b := <-second:
				t.Fatalf("the second answered before the lock: %d %v", b.status, b.out)
			}
			// The second judged before the lock and waiting at it.
			if c.meanwhile != nil {
				c.meanwhile(t, e)
			}
			reached, n := e.srv.trackers.reached.Load(), len(p.g.f.Requests())
			free()
			a, b := <-first, <-second
			e.srv.external.publishStep, e.srv.external.waiting = nil, nil
			if c.meanwhile == nil && a.status != http.StatusCreated {
				t.Fatalf("the first: %d %v", a.status, a.out)
			}
			if b.status != http.StatusConflict || errorCode(b.out) != c.code || rcOf(b.out) == nil {
				t.Fatalf("the second: %d %v", b.status, b.out)
			}
			// The first's resolve and nothing of the second's.
			if e.srv.trackers.reached.Load() != reached || len(p.g.f.Requests()) != n+1 {
				t.Fatalf("%d credentials read, %d requests to GitHub after the second waited", e.srv.trackers.reached.Load()-reached, len(p.g.f.Requests())-n)
			}
			repeat := e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/publications", body, http.StatusConflict, c.code)
			if rcOf(repeat)["opId"] != rcOf(b.out)["opId"] || apiMessage(repeat) != apiMessage(b.out) {
				t.Fatalf("the second sent again: %v, first %v", repeat, b.out)
			}
		})
	}
}

// A comment's address is its place on the target's item: GitHub's answer
// naming another item on the same host, the row, the send's reply, the
// publication's read, the ticket's read and the list all show the
// target's page with the comment's anchor.
func TestSentCommentAddressIsTheTargets(t *testing.T) {
	e, p := pubEnv(t)
	p.set(func(p *pubGH) { p.urlTo = "https://github.com/octo/app/issues/99#issuecomment-1" })
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id := pubOf(t, out)["id"].(string)
	sent := pubOf(t, e.sendPub(t, id, opID(), out["postSha256"].(string), http.StatusOK, ""))
	want := "https://github.com/octo/app/issues/12#issuecomment-" + strconv.FormatInt(p.commented(pubNodeID)[0].id, 10)
	read := pubOf(t, e.expect(t, "GET", "/api/dispatch/publications/"+id, nil, http.StatusOK, ""))
	listed := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publications", nil, http.StatusOK, "")["publications"].([]any)[0].(map[string]any)
	tk := e.expect(t, "GET", "/api/dispatch/tickets/PUB-1", nil, http.StatusOK, "")["ticket"].(map[string]any)["publications"].([]any)[0].(map[string]any)
	for name, got := range map[string]any{"the row": e.stored(t, id).RemoteURL, "the send's reply": sent["remoteUrl"], "the read": read["remoteUrl"],
		"the list": listed["remoteUrl"], "the ticket's read": tk["remoteUrl"]} {
		if got != want {
			t.Errorf("%s: %v, want %s", name, got, want)
		}
	}
}

// Close now for a publication restored from an export, sent with its close
// pending, closing or refused, is refused before any credential is read or
// anything is asked of GitHub.
func TestRestoredCloseRefusedBeforeGitHub(t *testing.T) {
	e, p := pubEnv(t)
	pending := pendingClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = http.StatusBadGateway })
	closing := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = http.StatusForbidden })
	refused := sentWithClose(t, e, p)
	p.set(func(p *pubGH) { p.closeTo = 0 })
	wctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(wctx)
	if err := dispatch.Reissue(t.Context(), filepath.Join(e.srv.store.Dir(), "dispatch", dispatch.File)); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	for name, id := range map[string]string{"pending": pending, "closing": closing, "refused": refused} {
		if got := e2.stored(t, id); !got.Restored || got.CloseState != name {
			t.Fatalf("%s: %+v", name, got)
		}
		n, reached := len(p.g.f.Requests()), e2.srv.trackers.reached.Load()
		e2.expect(t, "POST", "/api/dispatch/publications/"+id+"/close", map[string]any{"opId": opID()}, http.StatusConflict, dispatch.RefusalInvalid)
		if len(p.g.f.Requests()) != n || e2.srv.trackers.reached.Load() != reached {
			t.Errorf("%s: %d requests to GitHub, %d credentials read", name, len(p.g.f.Requests())-n, e2.srv.trackers.reached.Load()-reached)
		}
	}
}

// The one place a publication's route answers an error met once its
// context had ended (routeEnded), whichever call met it: a replay's, a
// begin's or a read's failure, a step at GitHub's, the lock's, each 503
// tracker_busy with Retry-After in its own words, through publicationFail,
// the reply's read and the lock; the store closed is the store's 503
// dispatch_unavailable; a refusal, kept or returned, is its answer. With
// the context live the same errors answer as before, whatever the server's
// stop. Once the stop has begun, the store still open, a route whose
// context the stop ended (a send's operation id sent again, a prepare's
// replay) answers the busy answer in its own words, and a read with a live
// context of a publication that is not there is 404.
func TestPublicationRouteEnded(t *testing.T) {
	e, _ := pubEnv(t)
	prep := map[string]any{"opId": opID(), "target": "linked", "body": "Fixed in v2."}
	out := e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/publications", prep, http.StatusCreated, "")
	send := map[string]any{"opId": opID(), "postSha256": out["postSha256"]}
	sendPath := "/api/dispatch/publications/" + pubOf(t, out)["id"].(string) + "/send"
	e.expect(t, "POST", sendPath, send, http.StatusOK, "")
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	ref := tracker.Ref{System: tracker.GitHub, Host: "github.com", Space: "octo/app", Kind: "issue", ID: "12"}
	fail := func(ctx context.Context, rc dispatch.Receipt, err error) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/dispatch/publications/0123456789abcdef/send", nil)
		e.srv.publicationFail(w, r, ref, rc, err, e.srv.dispatch, ctx, "0123456789abcdef")
		return w
	}
	answer := func(w *httptest.ResponseRecorder) (int, string, string) {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		code, _ := errorCode(out).(string)
		return w.Code, code, apiMessage(out)
	}
	busy := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if status, code, words := answer(w); status != http.StatusServiceUnavailable || code != "tracker_busy" || words != errRouteEnded.Message || w.Header().Get("Retry-After") != "5" {
			t.Errorf("%s: %d %s %q", name, status, code, words)
		}
	}
	met := map[string]error{"a replay's": context.Canceled, "a begin's": dispatch.ErrUnavailable, "a read's": fmt.Errorf("dispatch: %w", context.DeadlineExceeded),
		"a step at GitHub's": &tracker.RequestError{System: tracker.GitHub, Method: http.MethodPost, Path: "/graphql", Err: context.Canceled}, "the stop's door": errItemBusy}
	for name, err := range met {
		busy(name, fail(ended, dispatch.Receipt{}, err))
	}
	if status, code, _ := answer(fail(ended, dispatch.Receipt{}, dispatch.ErrClosed)); status != http.StatusServiceUnavailable || code != "dispatch_unavailable" {
		t.Errorf("the store closed: %d %s", status, code)
	}
	if status, code, _ := answer(fail(ended, dispatch.Receipt{}, &dispatch.Refusal{Code: dispatch.RefusalArchived, Words: "PUB is archived"})); status != http.StatusConflict || code != dispatch.RefusalArchived {
		t.Errorf("a refusal returned: %d %s", status, code)
	}
	kept := dispatch.Receipt{OpID: "o1", Intent: dispatch.IntentPublicationSend, Status: dispatch.StatusFailed, Refusal: dispatch.RefusalBound, Error: "w"}
	if status, code, _ := answer(fail(ended, kept, nil)); status != http.StatusConflict || code != dispatch.RefusalBound {
		t.Errorf("a refusal kept: %d %s", status, code)
	}
	if status, code, _ := answer(fail(t.Context(), dispatch.Receipt{}, context.Canceled)); status == http.StatusServiceUnavailable || code == "tracker_busy" {
		t.Errorf("a live route's failure: %d %s", status, code)
	}
	// The reply's read, and the lock held past the route's end.
	w := httptest.NewRecorder()
	e.srv.publicationReply(ended, w, e.srv.dispatch, "0123456789abcdef", http.StatusOK, nil)
	busy("the reply's read", w)
	release, ok := e.srv.external.lock(t.Context(), "publication:held", time.Second)
	if !ok {
		t.Fatal("the lock")
	}
	defer release()
	w = httptest.NewRecorder()
	if _, ok := e.srv.publicationLock(ended, w, "publication:held"); ok {
		t.Fatal("a held lock taken")
	}
	busy("the lock", w)
	// Once the server has begun to stop, the store still open.
	e.srv.external.end()
	for name, err := range met {
		if _, _, words := answer(fail(t.Context(), dispatch.Receipt{}, err)); words == errRouteEnded.Message {
			t.Errorf("stopping, %s, the route's context live: the busy answer", name)
		}
	}
	for name, c := range map[string]struct {
		path string
		body map[string]any
	}{"the send sent again": {sendPath, send}, "the prepare's replay": {"/api/dispatch/tickets/PUB-1/publications", prep}} {
		resp, got := e.do("POST", c.path, adminToken, c.body)
		if resp.StatusCode != http.StatusServiceUnavailable || errorCode(got) != "tracker_busy" || apiMessage(got) != errRouteEnded.Message || resp.Header.Get("Retry-After") != "5" {
			t.Errorf("stopping, %s: %d %v", name, resp.StatusCode, got)
		}
	}
	e.expect(t, "GET", "/api/dispatch/publications/0123456789abcdef", nil, http.StatusNotFound, "")
}
