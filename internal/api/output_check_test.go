package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/journal"
)

// checkEnv is a server whose sync store is the fake, joined, with the
// project CND shared, a ticket CND-1, and captured output: a session's
// ring and a check's log, each words of its own.
type checkEnv struct {
	*testEnv
	host      *extHost
	ring, log string
	key       string // the ticket's
}

func newCheckEnv(t *testing.T, seed uint64) *checkEnv {
	t.Helper()
	ctx := context.Background()
	e, own := syncDoorsEnv(t, newFakeSync())
	if err := own.DispatchJoin(ctx, "a1", "w1", "dev-a", 1_000_001, 1_100_000); err != nil {
		t.Fatal(err)
	}
	e.newDispatchProject(t, "CND", nil)
	if err := e.srv.dispatch.ShareForTest(ctx, "CND", "w1"); err != nil {
		t.Fatal(err)
	}
	key := e.newDispatchTicket(t, "CND", "The ticket", nil)["key"].(string)
	r := rand.New(rand.NewPCG(seed, seed+1))
	c := &checkEnv{testEnv: e, host: own, ring: words(r, 3000), log: words(r, 3000), key: key}
	e.srv.outputs.feed("ring:s-1", []byte(c.ring), 1<<20)
	e.srv.outputs.keepText("check:r1", []byte(c.log))
	e.srv.outputs.reader("check", func(id string) ([]byte, bool) { return []byte(c.log), id == "r1" })
	return c
}

// handed is every row the sync store is handed, by the sync door.
func (c *checkEnv) handed(t *testing.T) []ext.DispatchChange {
	t.Helper()
	rows, _, err := c.host.DispatchChanges(context.Background(), 0, ext.MaxDispatchBatch)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// lastRow is the newest change row of the field, as the store keeps it.
func (c *checkEnv) lastRow(t *testing.T, entity, field string) dispatch.ChangeRow {
	t.Helper()
	rows, _, err := c.srv.dispatch.ChangesFrom(context.Background(), 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Entity == entity && (field == "" || rows[i].Field == field) {
			return rows[i]
		}
	}
	t.Fatalf("no %s row of %q", entity, field)
	return dispatch.ChangeRow{}
}

// comment adds a comment to the ticket as actor.
func (c *checkEnv) comment(t *testing.T, actor, body string) {
	t.Helper()
	rc, err := c.srv.dispatch.AddComment(context.Background(), dispatch.Op{Actor: actor, ID: opID()}, c.key, body)
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the comment: %+v %v", rc, err)
	}
}

// handedRow is the handed row with the value.
func handedRow(t *testing.T, rows []ext.DispatchChange, has string) ext.DispatchChange {
	t.Helper()
	i := slices.IndexFunc(rows, func(d ext.DispatchChange) bool { return strings.Contains(d.Value, has) })
	if i < 0 {
		t.Fatalf("no handed row holds %q", has)
	}
	return rows[i]
}

// Every write's rows carry the check's verdicts of the value and, apart,
// of the prior, in the row's own transaction: a comment of a person's own
// words clean, one quoting a session's output output, a create's prior
// clean.
func TestOutputVerdictWrittenWithTheRow(t *testing.T) {
	c := newCheckEnv(t, 21)
	c.comment(t, adminActor, "my own words on this ticket, long enough to be judged")
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != verdictClean || r.PriorOutput != verdictClean {
		t.Fatalf("a clean comment's verdicts %q %q", r.Output, r.PriorOutput)
	}
	c.comment(t, adminActor, "the session printed:\n"+c.ring[1000:1100])
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != verdictOutput {
		t.Fatalf("a quoting comment's verdict %q", r.Output)
	}
}

// An agent's row quoting a session's output, and a person's paste of a
// check's log, are handed out marked output-like; a clean row is not.
func TestAgentRowQuotingOutputIsMarked(t *testing.T) {
	c := newCheckEnv(t, 23)
	c.comment(t, "agent:builder", "Here is what I saw: "+c.ring[200:330])
	c.comment(t, "agent:builder", "I fixed the widget count and ran the tests again.")
	rows := c.handed(t)
	if d := handedRow(t, rows, "Here is what I saw"); !d.OutputLike {
		t.Fatal("an agent's quote handed out clean")
	}
	if d := handedRow(t, rows, "I fixed the widget"); d.OutputLike {
		t.Fatal("an agent's own words handed out output-like")
	}
}

func TestPersonsPasteOfCapturedOutputWithheld(t *testing.T) {
	c := newCheckEnv(t, 25)
	c.comment(t, adminActor, "pasted from the check: "+strings.ReplaceAll(c.log[400:520], " ", "\n"))
	if d := handedRow(t, c.handed(t), "pasted from the check"); !d.OutputLike {
		t.Fatal("a person's paste of a check's log handed out clean")
	}
}

// Output-like text replaced by exactly one clean edit, the source pruned,
// then handed out: the edit's value clean and its prior output, so the
// row goes with PriorOutputLike alone.
func TestOutputLikePriorLeftOutOfACleanCorrection(t *testing.T) {
	c := newCheckEnv(t, 27)
	ctx := context.Background()
	bad := "From the ring: " + c.ring[600:700]
	good := "A clean description of the ticket, in words of its own."
	for _, d := range []string{bad, good} {
		rc, err := c.srv.dispatch.EditTicket(ctx, adminOp(""), c.key, dispatch.TicketEdit{Set: dispatch.TicketSet{Description: &d}})
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("the edit: %+v %v", rc, err)
		}
	}
	c.srv.outputs.prune("ring:s-1")
	d := handedRow(t, c.handed(t), "A clean description")
	if d.OutputLike || !d.PriorOutputLike {
		t.Fatalf("the correction: output-like %v, prior output-like %v", d.OutputLike, d.PriorOutputLike)
	}
}

// A row is judged by what the server held when it was written: a comment
// quoting a check's log, the log pruned, then handed out: withheld.
func TestVerdictKeptAfterTheSourceIsGone(t *testing.T) {
	c := newCheckEnv(t, 29)
	c.comment(t, adminActor, "the check said: "+c.log[100:220])
	c.srv.outputs.prune("check:r1")
	if !c.srv.outputs.whole(context.Background()) {
		t.Fatal("not whole")
	}
	if q, _ := c.srv.outputs.quotes(c.log[100:220]); q {
		t.Fatal("the pruned log still caught")
	}
	if d := handedRow(t, c.handed(t), "the check said"); !d.OutputLike {
		t.Fatal("a row whose source is gone handed out clean")
	}
}

// A row written before the check existed keeps no verdict: at hand-out an
// agent's is output-like whatever the check finds now, and a person's is
// judged then.
func TestHistoricalAgentRowWithheld(t *testing.T) {
	c := newCheckEnv(t, 31)
	c.srv.dispatch.SetOutputCheck(nil)
	c.comment(t, "agent:builder", "An agent's note written before the check existed.")
	c.comment(t, adminActor, "A person's note written before the check existed.")
	c.comment(t, adminActor, "A person's paste before the check: "+c.ring[50:160])
	c.srv.dispatch.SetOutputCheck(c.srv.copyCheck.JudgeValue)
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != "" {
		t.Fatalf("a row with no check has a verdict %q", r.Output)
	}
	rows := c.handed(t)
	if d := handedRow(t, rows, "An agent's note"); !d.OutputLike {
		t.Fatal("an agent's row from before the check handed out clean")
	}
	if d := handedRow(t, rows, "A person's note"); d.OutputLike {
		t.Fatal("a person's clean row from before the check withheld")
	}
	if d := handedRow(t, rows, "A person's paste"); !d.OutputLike {
		t.Fatal("a person's paste from before the check handed out clean")
	}
}

// While the index is being rebuilt a write's verdict is pending: the
// door hands out the rows before it and stops there, the cursor short of
// it; once the index is whole the verdict is settled and the row goes.
func TestPendingVerdictNotHandedOutDuringRebuild(t *testing.T) {
	c := newCheckEnv(t, 33)
	ctx := context.Background()
	c.comment(t, adminActor, "a clean comment before the rebuild, long enough")
	c.srv.outputs.mu.Lock()
	c.srv.outputs.rebuilding = true
	c.srv.outputs.mu.Unlock()
	c.comment(t, adminActor, "written while the index is rebuilt: "+c.ring[300:400])
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != verdictPending {
		t.Fatalf("a verdict made while rebuilding: %q", r.Output)
	}
	// The operation's first row whose verdict waits: the door stops there.
	waiting, err := c.srv.dispatch.PendingRows(ctx, 10)
	if err != nil || len(waiting) == 0 {
		t.Fatalf("pending rows: %d %v", len(waiting), err)
	}
	pending := waiting[0]
	rows, next, err := c.host.DispatchChanges(ctx, 0, ext.MaxDispatchBatch)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(rows, func(d ext.DispatchChange) bool { return d.Seq >= pending.Seq }) || next != pending.Seq-1 {
		t.Fatalf("handed past the pending row %d: next %d, %d rows", pending.Seq, next, len(rows))
	}
	handedRow(t, rows, "a clean comment before")
	if n := c.srv.settlePending(ctx); n != 0 {
		t.Fatalf("%d settled while rebuilding", n)
	}
	c.srv.outputs.mu.Lock()
	c.srv.outputs.rebuilding = false
	c.srv.outputs.mu.Unlock()
	if n := c.srv.settlePending(ctx); n != len(waiting) {
		t.Fatalf("%d settled once whole, of %d", n, len(waiting))
	}
	if d := handedRow(t, c.handed(t), "written while the index"); !d.OutputLike {
		t.Fatal("the settled row handed out clean")
	}
}

// A verdict pending when the server ends (its index behind, the ring it
// may have quoted in that process's memory) is output at the next start,
// before the check answers anything: the row is withheld, never clean.
func TestPendingVerdictOfAnEndedProcessWithheld(t *testing.T) {
	dir := syncDataDir(t)
	e := syncEnv(t, dir, newFakeSync())
	ctx := context.Background()
	e.newDispatchProject(t, "CND", nil)
	e.newDispatchTicket(t, "CND", "The ticket", nil)
	ring := words(rand.New(rand.NewPCG(35, 36)), 3000)
	e.srv.outputs.feed("ring:s-1", []byte(ring), 1<<20)
	e.srv.outputs.mu.Lock()
	e.srv.outputs.rebuilding = true
	e.srv.outputs.mu.Unlock()
	rc, err := e.srv.dispatch.AddComment(ctx, adminOp(""), "CND-1", "quoting the ring: "+ring[700:800])
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the comment: %+v %v", rc, err)
	}
	stopEnv(t, e)
	again := syncEnv(t, dir, newFakeSync())
	rows, _, err := again.srv.dispatch.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(c dispatch.ChangeRow) bool { return strings.Contains(c.Value, "quoting the ring") })
	if i < 0 || rows[i].Output != verdictOutput {
		t.Fatalf("the row after the restart: %+v", rows[i])
	}
	if pr, err := again.srv.dispatch.PendingRows(ctx, 10); err != nil || len(pr) != 0 {
		t.Fatalf("pending rows after the start: %d %v", len(pr), err)
	}
	if code, _, _ := again.call(t, http.MethodGet, "/api/dispatch/tickets/CND-1", nil); code != http.StatusOK {
		t.Fatalf("the ticket after the restart: %d", code)
	}
}

// A check's log the ledger keeps is a source from its commit: a comment
// quoting it is output, confirmed through the ledger's own row; once the
// run is pruned the log goes with it, and a new comment quoting it is
// clean, while the first keeps its verdict.
func TestCheckLogFromTheLedgerIsASource(t *testing.T) {
	c := newCheckEnv(t, 37)
	ctx := context.Background()
	ld := c.srv.ledger
	logText := words(rand.New(rand.NewPCG(1037, 1038)), 3000)
	sum := sha256.Sum256([]byte(logText))
	if _, err := ld.Transition(ctx, "team-0000c0de", func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutRun(ledger.RunRow{ID: "team-0000c0de", CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: ledger.RunFinished, CreatedAt: now, ClosedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptSucceeded, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, State: ledger.CheckFinished,
				Log: []byte(logText), LogSHA256: hex.EncodeToString(sum[:]), StartedAt: now, FinishedAt: now}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.comment(t, adminActor, "the test's log says "+logText[800:900])
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != verdictOutput {
		t.Fatalf("a quote of a check's log: %q", r.Output)
	}
	if err := ld.Prune(ctx, "team-0000c0de"); err != nil {
		t.Fatal(err)
	}
	if !c.srv.outputs.whole(ctx) {
		t.Fatal("not whole")
	}
	c.comment(t, adminActor, "after the prune, "+logText[800:900])
	if r := c.lastRow(t, dispatch.EntityComment, ""); r.Output != verdictClean {
		t.Fatalf("a quote of a pruned run's log: %q", r.Output)
	}
	if d := handedRow(t, c.handed(t), "the test's log says"); !d.OutputLike {
		t.Fatal("the first quote lost its verdict with the prune")
	}
}

// A passage a program draws one cell at a time by cursor moves, in a
// shuffled order, is never contiguous in its output: it is output-like
// while it is on the screen (the screens are read whole at the write) and
// after the screen has redrawn over it (a snapshot of the screen was kept
// as the output arrived).
func TestPassageAssembledByCursorMovesCaught(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(39, 40))
	passage := words(r, 64)
	var draw strings.Builder
	draw.WriteString("\x1b[2J")
	for _, i := range r.Perm(len(passage)) {
		fmt.Fprintf(&draw, "\x1b[3;%dH%c", i+1, passage[i])
	}
	draw.WriteString("\x1b[10;1H")
	if err := os.WriteFile(filepath.Join(dir, "draw"), []byte(draw.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "over"), []byte("\x1b[2J\x1b[Hsomething else entirely\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(draw.String(), passage[:8]) {
		t.Fatal("the drawing holds the passage's bytes in a row")
	}
	off := false
	painter := catalog.Agent{ID: "painter", Name: "painter", Command: []string{"/bin/sh", "-c",
		`cat "$1/draw"; until [ -e "$1/go" ]; do sleep 0.05; done; cat "$1/over"; exec sleep 600`, "painter", dir}, Probe: &off}
	e := newTestEnvAgents(t, nil, nil, []catalog.Agent{painter})
	e.newDispatchProject(t, "CND", nil)
	key := e.newDispatchTicket(t, "CND", "The ticket", nil)["key"].(string)
	id := e.createSession("painter")
	screen := func() string { return strings.Join(e.srv.screenOf(id), "\n") }
	if !waitFor(testWait, func() bool { return strings.Contains(screen(), passage) }) {
		t.Fatalf("the passage never drawn: %q", screen())
	}
	comment := func(body string) string {
		t.Helper()
		rc, err := e.srv.dispatch.AddComment(context.Background(), dispatch.Op{Actor: "agent:painter", ID: opID()}, key, body)
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("the comment: %+v %v", rc, err)
		}
		rows, _, err := e.srv.dispatch.ChangesFrom(context.Background(), 0, dispatch.MaxChangesPage)
		if err != nil {
			t.Fatal(err)
		}
		return rows[len(rows)-1].Output
	}
	if v := comment("on my screen: " + passage); v != verdictOutput {
		t.Fatalf("a passage on the screen: %s", v)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(testWait, func() bool {
		return strings.Contains(screen(), "something else") && !strings.Contains(screen(), passage)
	}) {
		t.Fatalf("the screen never redrawn over the passage: %q", screen())
	}
	if v := comment("it was on the screen: " + passage); v != verdictOutput {
		t.Fatalf("a passage the screen redrew over: %s", v)
	}
}

// A pin's name is judged by the copied-output check when the pin is kept:
// its verdict kept beside it (nameVerdict, which a put may not carry), and
// only a clean name leaves the machine; one quoting output, or one the
// check could not judge, is delivered empty.
func TestPinNameUnderTheOutputCheck(t *testing.T) {
	c := newCheckEnv(t, 41)
	pin := func(session, name string) map[string]any {
		return map[string]any{"session": session, "name": name, "agent": "claude", "at": 1_700_000_000_000}
	}
	quote := c.ring[1500:1580]
	putUIDoc(t, c.testEnv, "pins", map[string]any{"v": 1, "pins": []any{pin("s-clean", "fix the widget tests"), pin("s-quote", quote)}}, http.StatusOK)
	kept := uiFile(t, c.testEnv, "pins")["pins"].([]any)
	if v := kept[0].(map[string]any)["nameVerdict"]; v != verdictClean {
		t.Fatalf("a clean name's verdict: %v", v)
	}
	if v := kept[1].(map[string]any)["nameVerdict"]; v != verdictOutput {
		t.Fatalf("a quoting name's verdict: %v", v)
	}
	raw, err := os.ReadFile(filepath.Join(c.srv.store.Dir(), "ui", "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out uiPinsDoc
	if err := json.Unmarshal(deliveredUIDoc("ui/pins.json", raw), &out); err != nil || len(out.Pins) != 2 {
		t.Fatalf("the pins delivered: %+v %v", out, err)
	}
	if out.Pins[0].Name != "fix the widget tests" || out.Pins[1].Name != "" || out.Pins[1].Session != "s-quote" || out.Pins[0].NameVerdict != "" {
		t.Fatalf("the pins delivered: %+v", out.Pins)
	}
	// A verdict the check could not make: delivered empty, as a quote is.
	c.srv.outputs.mu.Lock()
	c.srv.outputs.rebuilding = true
	c.srv.outputs.mu.Unlock()
	putUIDoc(t, c.testEnv, "pins", map[string]any{"v": 1, "pins": []any{pin("s-new", "a name the check cannot judge now")}}, http.StatusOK)
	if v := uiFile(t, c.testEnv, "pins")["pins"].([]any)[0].(map[string]any)["nameVerdict"]; v != verdictPending {
		t.Fatalf("a name judged while rebuilding: %v", v)
	}
	raw, _ = os.ReadFile(filepath.Join(c.srv.store.Dir(), "ui", "pins.json"))
	if err := json.Unmarshal(deliveredUIDoc("ui/pins.json", raw), &out); err != nil || out.Pins[0].Name != "" {
		t.Fatalf("a pending name delivered: %+v %v", out.Pins, err)
	}
	withVerdict := pin("s-x", "a name")
	withVerdict["nameVerdict"] = verdictClean
	if out := putUIDoc(t, c.testEnv, "pins", map[string]any{"v": 1, "pins": []any{withVerdict}}, http.StatusBadRequest); !strings.Contains(apiMessage(out), "nameVerdict") {
		t.Fatalf("a put carrying a verdict: %v", out)
	}
}

// An index missing at the start (a data directory restored from an
// export, which leaves it out, or its file removed) is made again from
// the ledger's logs before it answers: a quote of a check's log kept
// before is caught after it.
func TestAMissingIndexIsRebuiltFromTheLedger(t *testing.T) {
	dir := syncDataDir(t)
	e := syncEnv(t, dir, newFakeSync())
	ctx := context.Background()
	logText := words(rand.New(rand.NewPCG(43, 44)), 3000)
	sum := sha256.Sum256([]byte(logText))
	if _, err := e.srv.ledger.Transition(ctx, "team-0000beef", func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutRun(ledger.RunRow{ID: "team-0000beef", CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: ledger.RunFinished, CreatedAt: now, ClosedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptSucceeded, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, State: ledger.CheckFinished,
				Log: []byte(logText), LogSHA256: hex.EncodeToString(sum[:]), StartedAt: now, FinishedAt: now}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stopEnv(t, e)
	for _, p := range []string{indexFile, indexFile + "-wal", indexFile + "-shm"} {
		if err := os.Remove(filepath.Join(dir, p)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	again := syncEnv(t, dir, newFakeSync())
	if !again.srv.outputs.fresh {
		t.Fatal("the index was not taken as fresh")
	}
	g := again.srv.copyCheck
	if v := g.Check(ctx, "the log said "+logText[1200:1300]); v != verdictOutput {
		t.Fatalf("a quote of a log kept before the index was made: %s", v)
	}
}

// A hosted session's output never passes this server whole (the host link
// carries its signalling, and its terminal only to a viewer the server
// relays), so the check fails closed for it: its agent's text is judged
// output whatever it says, and stays on this machine. A person's text,
// and a local session's agent's, are judged by the index. Today a hosted
// session's agent has no ticket here to write to at all: it is no member
// of this server's runs, and its comment writes nothing.
func TestAHostedSessionsAgentTextIsOutput(t *testing.T) {
	c := newCheckEnv(t, 45)
	ctx := context.Background()
	own := "An agent's sentence of its own, long enough to be judged."
	if v := c.srv.judgeRow(withOutputNotHeld(ctx), own, true); v != verdictOutput {
		t.Fatalf("a hosted agent's own words: %s", v)
	}
	if v := c.srv.judgeRow(withOutputNotHeld(ctx), own, false); v != verdictClean {
		t.Fatalf("a person's words beside a hosted session: %s", v)
	}
	if v := c.srv.judgeRow(ctx, own, true); v != verdictClean {
		t.Fatalf("a local agent's own words: %s", v)
	}

	host := dialFakeHost(t, c.testEnv, "hosted-agent-token-0123456789abcdef")
	before, _, err := c.srv.dispatch.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
	if err != nil {
		t.Fatal(err)
	}
	c.expectAs(t, "hosted-agent-token-0123456789abcdef", "POST", "/api/sessions/"+host.sessionID+"/ticket/comments",
		map[string]any{"opId": opID(), "body": "from the host: " + own}, http.StatusNotFound, "no_ticket")
	after, _, err := c.srv.dispatch.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
	if err != nil || len(after) != len(before) {
		t.Fatalf("a hosted agent's comment wrote %d rows %v", len(after)-len(before), err)
	}
}

// A history record's words are a source: an entry the record keeps (an
// agent's progress) is indexed as it is kept, a quote of it is output; an
// index missing at a start is made again from the records; and a record
// forgotten takes its words with it.
func TestHistoryRecordsAreSources(t *testing.T) {
	dir := syncDataDir(t)
	e := syncEnv(t, dir, newFakeSync())
	ctx := context.Background()
	e.newDispatchProject(t, "CND", nil)
	key := e.newDispatchTicket(t, "CND", "The ticket", nil)["key"].(string)
	words := words(rand.New(rand.NewPCG(47, 48)), 300)
	id := e.createSession("cat")
	e.expect(t, "POST", "/api/sessions/"+id+"/events", map[string]any{"type": "progress", "message": words}, http.StatusAccepted, "")
	kept := func(j *testEnv) bool {
		r, _, err := j.srv.sessionRecords.Get(id)
		return err == nil && slices.ContainsFunc(r.Entries, func(en journal.Entry) bool { return en.Message == words })
	}
	if !waitFor(testWait, func() bool { return kept(e) }) {
		t.Fatal("the record never kept the progress")
	}
	comment := func(j *testEnv, body string) string {
		t.Helper()
		rc, err := j.srv.dispatch.AddComment(ctx, adminOp(""), key, body)
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("the comment: %+v %v", rc, err)
		}
		rows, _, err := j.srv.dispatch.ChangesFrom(ctx, 0, dispatch.MaxChangesPage)
		if err != nil {
			t.Fatal(err)
		}
		return rows[len(rows)-1].Output
	}
	if v := comment(e, "the history says "+words[50:170]); v != verdictOutput {
		t.Fatalf("a quote of a record's words: %s", v)
	}
	if d, ok := e.srv.registry.Get(id); ok {
		_ = d.Stop(ctx)
	}
	stopEnv(t, e)
	for _, p := range []string{indexFile, indexFile + "-wal", indexFile + "-shm"} {
		if err := os.Remove(filepath.Join(dir, p)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	again := syncEnv(t, dir, newFakeSync())
	if !kept(again) {
		t.Fatal("the record is not kept across the restart")
	}
	if v := comment(again, "after the rebuild, "+words[50:170]); v != verdictOutput {
		t.Fatalf("a quote of a record's words after the rebuild: %s", v)
	}
	if !waitFor(testWait, func() bool { return again.srv.sessionRecords.Forget(id) == nil }) {
		t.Fatal("the record was never forgotten")
	}
	if !again.srv.outputs.whole(ctx) {
		t.Fatal("not whole")
	}
	if v := comment(again, "after the forget, "+words[50:170]); v != verdictClean {
		t.Fatalf("a quote of a forgotten record's words: %s", v)
	}
}
