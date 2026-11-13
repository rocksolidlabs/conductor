package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// pubNode is an issue or a pull request as the fake's GraphQL answers it.
type pubNode struct {
	typ, name, state string
	number           int
	repo             int64
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
// says (commitLate).
const (
	postOK   = "ok"
	postDrop = "drop"
	postLost = "lost"
	postHold = "hold"
	postLate = "late"
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
	closeTo  int // the close's answer: 0 a close, 403 a refusal
	held     chan struct{}
	reached  chan struct{}
	late     *pubComment
}

func newPubGH(t *testing.T, g *ghItems) *pubGH {
	p := &pubGH{g: g, nodes: map[string]*pubNode{}, nextID: 7000000001, mode: postOK,
		accounts: map[string]pubAccount{plantedToken: alicePub, plantedToken2: bobPub, plantedEnv: carolPub}}
	g.f.Handle("POST", "api.github.com", "/graphql", p.graphql)
	g.f.Handle("GET", "api.github.com", "/user", func(w http.ResponseWriter, r *http.Request) {
		a, ok := p.account(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"login":%q,"id":%d}`, a.login, a.id)
	})
	t.Cleanup(p.release)
	return p
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
	p.mu.Unlock()
	p.g.f.Handle("GET", "api.github.com", fmt.Sprintf("/repositories/%d/issues/%d/comments", repo, number), func(w http.ResponseWriter, r *http.Request) {
		p.list(w, r, repo, number)
	})
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
	c.url = fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", n.name, n.number, c.id)
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
		seg := "issues"
		if n.typ == "PullRequest" {
			seg = "pull"
		}
		v := map[string]any{"__typename": n.typ, "id": q.Variables["id"], "number": n.number, "state": n.state,
			"url": fmt.Sprintf("https://github.com/%s/%s/%d", n.name, seg, n.number), "repository": map[string]any{"fullDatabaseId": strconv.FormatInt(n.repo, 10), "nameWithOwner": n.name}}
		p.mu.Unlock()
		answer(map[string]any{"data": map[string]any{"node": v}})
	case github.AddCommentDocument:
		node := q.Variables["subjectId"].(string)
		c := p.addLocked(node, q.Variables["body"].(string), a, time.Now().Add(p.skew))
		mode := p.mode
		var held, reached chan struct{}
		switch mode {
		case postDrop, postLate:
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
		case postHold:
			close(reached)
			select {
			case <-held:
			case <-r.Context().Done():
				panic(http.ErrAbortHandler)
			}
		}
		answer(map[string]any{"data": map[string]any{"addComment": map[string]any{"subject": map[string]any{"id": node}, "commentEdge": map[string]any{"node": map[string]any{
			"fullDatabaseId": strconv.FormatInt(c.id, 10), "url": c.url, "createdAt": c.created.Format(time.RFC3339),
			"repository": map[string]any{"fullDatabaseId": strconv.FormatInt(c.repo, 10)}, "author": map[string]any{"login": a.login, "databaseId": a.id}}}}}})
	case github.CloseIssueDocument:
		id := q.Variables["issueId"].(string)
		n, status := p.nodes[id], p.closeTo
		if status == 0 {
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
// fake's issue is octo/app#160, imported into the project PBW.
func drivePublications(t *testing.T, e *testEnv, g *ghItems, do func(pattern, path, body string, status int, code string) map[string]any) {
	t.Helper()
	p := newPubGH(t, g)
	g.set(160, ghItem{Node: "I_kwDOitem160", Title: "Walked", Body: "b", Updated: t0, Labels: []string{}})
	p.node("I_kwDOitem160", "Issue", "octo/app", 160, 4000001)
	e.newDispatchProject(t, "PBW", nil)
	e.importItem(t, "PBW", "https://github.com/octo/app/issues/160", opID(), http.StatusCreated, map[string]any{"images": false})
	prepare := func(extra string) (id, digest string) {
		out := do("POST /api/dispatch/tickets/{key}/publications", "/api/dispatch/tickets/PBW-1/publications",
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
// answer never read; the row stays sending. A server started again over
// the data directory settles it in the background by its marker: sent,
// one comment, no second post.
func TestPublishAcrossCrash(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	reached := make(chan struct{})
	p.set(func(p *pubGH) { p.mode, p.held, p.reached = postHold, make(chan struct{}), reached })
	answered := make(chan int, 1)
	go func() {
		status, _, _ := e.call(t, "POST", "/api/dispatch/publications/"+id+"/send", map[string]any{"opId": opID(), "postSha256": digest})
		answered <- status
	}()
	<-reached
	stop, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(stop)
	<-answered
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
}

// A post GitHub dropped, its answer not one that proves it: the listing
// finds nothing, and within settleQuiet the row stays sending with its
// words; past it (the server's clock seam) Check makes it unconfirmed.
// Send is refused; Publish as new posts one comment of its own.
func TestPublishUnconfirmedNeverResent(t *testing.T) {
	e, p := pubEnv(t)
	now := time.Now()
	clock := func() time.Time { return now }
	e.srv.external.now = func() time.Time { return clock() }
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	id, digest := pubOf(t, out)["id"].(string), out["postSha256"].(string)
	p.set(func(p *pubGH) { p.mode = postDrop })
	got := pubOf(t, e.sendPub(t, id, opID(), digest, http.StatusOK, ""))
	if got["state"] != "sending" || got["error"] == nil {
		t.Fatalf("a dropped post: %v", got)
	}
	clock = func() time.Time { return now.Add(settleQuiet + time.Second) }
	e.srv.external.now = func() time.Time { return clock() }
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
// sends nothing and settles closed.
func TestPublishCloseRecovers(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		at         string
		sent, want int // closes sent by Close now; close_tries after it
	}{{"pending", 1, 1}, {"closing", 1, 2}, {"closed on GitHub", 0, 2}} {
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
// keeps its id, and the comment goes to the node where it is now; an
// issue transferred to another repository is 409 target_changed, nothing
// posted or committed.
func TestSendRefusesChangedTarget(t *testing.T) {
	e, p := pubEnv(t)
	out := e.prepare(t, "PUB-1", nil, http.StatusCreated, "")
	p.edit(pubNodeID, func(n *pubNode) { n.name = "octo/renamed" })
	e.sendPub(t, pubOf(t, out)["id"].(string), opID(), out["postSha256"].(string), http.StatusOK, "")
	if c := p.commented(pubNodeID); len(c) != 1 || p.adds() != 1 {
		t.Fatalf("the renamed repository: %d comments", len(c))
	}
	moved := e.prepare(t, "PUB-1", map[string]any{"body": "Again."}, http.StatusCreated, "")
	p.edit(pubNodeID, func(n *pubNode) { n.repo, n.name = 4000002, "other/app" })
	id := pubOf(t, moved)["id"].(string)
	e.sendPub(t, id, opID(), moved["postSha256"].(string), http.StatusConflict, "target_changed")
	if got := e.stored(t, id); got.State != dispatch.PublicationDraft || p.adds() != 1 {
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
	// One being sent, as a crash leaves it, for the start's settle.
	sending := e.prepare(t, "PUB-1", map[string]any{"body": "s"}, http.StatusCreated, "")
	sid := pubOf(t, sending)["id"].(string)
	if _, owned, err := e.srv.dispatch.BeginSend(t.Context(), adminOp("Alice"), sid, sending["postSha256"].(string), dispatch.Sender{ID: "9000001", Login: "mona-example"}); !owned || err != nil {
		t.Fatal(err)
	}
	writes := p.g.f.AllWrites()
	tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/PUB-1", nil, http.StatusOK, ""), "ticket")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/edit", map[string]any{"opId": opID(), "expectedRev": tk["rev"], "set": map[string]any{"priority": 1}}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/comments", map[string]any{"opId": opID(), "body": "private"}, http.StatusCreated, "")
	e.expect(t, "POST", "/api/dispatch/tickets/PUB-1/external/refresh", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/publications/"+fid+"/check", map[string]any{}, http.StatusConflict, dispatch.RefusalInvalid)
	e.expect(t, "GET", "/api/dispatch/tickets/PUB-1/publish/offer", nil, http.StatusOK, "")
	rev := e.stored(t, sid).Rev
	e2 := e.restart()
	useFake(e2.srv, p.g.f)
	e2.srv.SettlePublications()
	if !waitFor(testWait, func() bool { return e.stored(t, sid).Rev != rev }) {
		t.Fatal("the start's settle recorded nothing")
	}
	if n := p.g.f.AllWrites() - writes; n != 0 {
		t.Fatalf("%d writes to GitHub by a side effect", n)
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
		{PublishTimeout, c.busy + c.reads + c.write + c.post + c.write + c.post + 2*c.write},
		{PrepareTimeout, c.busy + c.reads + 2*c.write}, {CloseNowTimeout, c.busy + c.post + 2*c.write},
		{PublishTimeout, 240 * time.Second}, {PrepareTimeout, 120 * time.Second}, {CloseNowTimeout, 105 * time.Second},
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
	if len(said) < int(pubSaids)+3*12 {
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
		if _, _, err := ds.BeginClose(ctx, closing); err != nil {
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
