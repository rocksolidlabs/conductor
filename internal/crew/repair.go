package crew

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A person's repair (docs/plans/review-repair.md, RV1 PR-2b): a person's
// Request changes of a task's standing candidate, the batch of a review
// Dispatch keeps, applied by Yardmaster as the same repair transition a
// review task's needs_changes makes (reopenFor). The repair attempt's task
// file carries the person's changes, each with its id, its lines and the
// resolution asked for, and its result may say which it addressed. Nothing
// is typed into a session but the task line every attempt gets.

// Errors of a person's change request.
var (
	// ErrBadChangeRequest refuses a request whose shape is not one a
	// review's batch makes.
	ErrBadChangeRequest = errors.New("bad change request")
	// ErrChangeRequestTooLarge refuses a request the run could not keep, or
	// whose repair attempt's task file could not hold it (FitChangeRequest):
	// refused whole, before anything is recorded.
	ErrChangeRequestTooLarge = errors.New("the change request is too large")
)

// Bounds of a person's change request, beside a review task's (maxFindings,
// maxFindingText, maxFindingPath, maxSummary).
const (
	maxRequestRef   = 64  // a review's id, a change's id, a via
	maxRequestBy    = 200 // who asked, in characters
	maxAnchorLines  = 12  // the lines a change keeps as the person saw them
	maxAnchorLine   = 200 // bytes, each
	maxCheckReceipt = 512 // a check finding's receipt identity, in bytes
	maxCheckWords   = 500 // a check finding's failure words, in bytes
	maxAddressed    = 50  // what a result says it addressed
	maxAddressNote  = 1000
	// maxCutMessage is how much of a change's words the task file keeps
	// once they are cut (cutChanges); task-more.json has them whole.
	maxCutMessage = 200
)

// The kinds and intents of a requested change, as Dispatch's findings have
// them.
var (
	changeKinds   = []string{"line", "file", "general", "check"}
	changeIntents = []string{"change", "question", "note"}
	severities    = []string{"blocker", "major", "minor", "nit"}
)

// ChangeRequest is a person's Request changes, as the repair keeps it: the
// Dispatch review's id, who asked (a display name, else the actor) and
// through what (web, cli), the base the candidate was read against, the
// review's summary, and its findings. The repair keeps the summary as its
// own (TaskRepair.Summary), the request without it.
type ChangeRequest struct {
	Review  string            `json:"review"`
	By      string            `json:"by"`
	Via     string            `json:"via,omitempty"`
	Base    string            `json:"base,omitempty"`
	Summary string            `json:"summary,omitempty"`
	Changes []RequestedChange `json:"changes"`
}

// RequestedChange is one finding of a person's request: its id (a result's
// addressed names it), its kind (line, file, general, check) and intent (a
// change asked for, a question, a note), a change's severity, the path and
// the side it is on (candidate, base), the lines from and to and as the
// person saw them, the words, the resolution asked for, and a check
// finding's receipt.
type RequestedChange struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Intent     string          `json:"intent"`
	Severity   string          `json:"severity,omitempty"`
	Path       string          `json:"path,omitempty"`
	Side       string          `json:"side,omitempty"`
	From       int             `json:"from,omitempty"`
	To         int             `json:"to,omitempty"`
	Lines      []string        `json:"lines,omitempty"`
	Message    string          `json:"message,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	Check      *RequestedCheck `json:"check,omitempty"`
	// What fitTaskFile cut of it (cutChanges), for task.md's words.
	linesCut, resolutionCut, messageCut bool
}

// RequestedCheck is a check finding's receipt as Dispatch keeps it: the
// ledger's identity (<run>/<task>/<n>/<phase>/<id>/<k>) and its parts, the
// commit it tested, how it ended, its log's hash and size, the last of its
// output, the class the server worked out (ClassifyFailure) and the one
// the person chose, and what they quote of the output.
type RequestedCheck struct {
	Receipt   string `json:"receipt"`
	Run       string `json:"run,omitempty"`
	Task      string `json:"task,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Phase     string `json:"phase,omitempty"`
	ID        string `json:"id,omitempty"`
	K         int    `json:"k,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Exit      *int   `json:"exit,omitempty"`
	Signal    string `json:"signal,omitempty"`
	TimedOut  bool   `json:"timedOut,omitempty"`
	Stable    bool   `json:"stable"`
	Error     string `json:"error,omitempty"`
	LogSHA256 string `json:"logSha256,omitempty"`
	LogBytes  int64  `json:"logBytes,omitempty"`
	Tail      string `json:"tail,omitempty"`
	Class     string `json:"class,omitempty"`
	Chosen    string `json:"chosen,omitempty"`
	Quote     string `json:"quote,omitempty"`
}

// Addressed is one change a repair attempt's result says it addressed:
// the change's id and a note.
type Addressed struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

// RepairRequest names the candidate a person's Request changes repairs:
// the task, the attempt that made it and its commit, and, in a
// continuation, Imported, the candidate's reference in the run that made
// it ("<run>/<task>/<n>"); "" names an attempt of the run itself. Request
// is the person's batch.
type RepairRequest struct {
	Task     string        `json:"task"`
	Attempt  int           `json:"attempt"`
	Commit   string        `json:"commit"`
	Imported string        `json:"imported,omitempty"`
	Request  ChangeRequest `json:"request"`
}

// intentRequestChanges is a person's Request changes' intent, as its
// command and receipt name it.
const intentRequestChanges = "request_changes"

// clone is a deep copy of cr, nil for nil.
func (cr *ChangeRequest) clone() *ChangeRequest {
	if cr == nil {
		return nil
	}
	out := *cr
	out.Changes = slices.Clone(cr.Changes)
	for i := range out.Changes {
		c := &out.Changes[i]
		c.Lines = slices.Clone(c.Lines)
		if c.Check != nil {
			ch := *c.Check
			if ch.Exit != nil {
				x := *ch.Exit
				ch.Exit = &x
			}
			c.Check = &ch
		}
	}
	return &out
}

// validate holds cr to the shape a review's batch has: who asked, the
// review, at least one change asked for among at most maxFindings, each
// with an id of its own, a kind and an intent, a change's severity, and
// its parts within their bounds and agreeing with its kind.
func (cr ChangeRequest) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrBadChangeRequest}, args...)...)
	}
	if cr.Review == "" || len(cr.Review) > maxRequestRef || cr.By == "" || utf8.RuneCountInString(cr.By) > maxRequestBy || len(cr.Via) > maxRequestRef {
		return bad("a request names its review in at most %d bytes and who asked in at most %d characters", maxRequestRef, maxRequestBy)
	}
	if !oneLineName(cr.Review, cr.By, cr.Via) {
		return bad("the review, who asked and through what are each one line of text, with no control character")
	}
	if cr.Base != "" && !fullCommit(cr.Base) {
		return bad("the base is not a full commit id")
	}
	if utf8.RuneCountInString(cr.Summary) > maxSummary {
		return bad("the summary is more than %d characters", maxSummary)
	}
	if len(cr.Changes) == 0 || len(cr.Changes) > maxFindings {
		return bad("a request has 1 to %d changes", maxFindings)
	}
	ids := map[string]bool{}
	asked := false
	for _, c := range cr.Changes {
		if c.ID == "" || len(c.ID) > maxRequestRef || ids[c.ID] {
			return bad("each change has an id of its own of at most %d bytes", maxRequestRef)
		}
		ids[c.ID] = true
		if !slices.Contains(changeKinds, c.Kind) || !slices.Contains(changeIntents, c.Intent) {
			return bad("change %s: its kind is line, file, general or check, its intent change, question or note", c.ID)
		}
		asked = asked || c.Intent == "change"
		if c.Intent == "change" && !slices.Contains(severities, c.Severity) || c.Intent != "change" && c.Severity != "" {
			return bad("change %s: a change asked for has a severity, blocker, major, minor or nit, and a question or a note none", c.ID)
		}
		if len(c.Path) > maxFindingPath || c.From < 0 || c.To < c.From || len(c.Lines) > maxAnchorLines || slices.ContainsFunc(c.Lines, func(l string) bool { return len(l) > maxAnchorLine }) {
			return bad("change %s: a path of at most %d bytes, lines from 1 to at or after it, at most %d lines of %d bytes", c.ID, maxFindingPath, maxAnchorLines, maxAnchorLine)
		}
		if c.Side != "" && c.Side != "candidate" && c.Side != "base" {
			return bad("change %s: its side is candidate or base", c.ID)
		}
		if !oneLineName(c.ID, c.Path) {
			return bad("change %q: its id and its path are each one line of text, with no control character", c.ID)
		}
		if utf8.RuneCountInString(c.Message) > maxFindingText || utf8.RuneCountInString(c.Resolution) > maxFindingText {
			return bad("change %s: its words and the resolution asked are at most %d characters each", c.ID, maxFindingText)
		}
		switch {
		case c.Kind == "line" && (c.Path == "" || c.From < 1):
			return bad("change %s: a line's change names its path and its lines", c.ID)
		case c.Kind == "file" && c.Path == "":
			return bad("change %s: a file's change names its path", c.ID)
		case c.Kind == "general" && c.Path != "":
			return bad("change %s: a general change names no path", c.ID)
		case c.Kind == "check" && c.Check == nil, c.Kind != "check" && c.Check != nil:
			return bad("change %s: a check's change, and only it, names its receipt", c.ID)
		}
		if ch := c.Check; ch != nil && (ch.Receipt == "" || len(ch.Receipt) > maxCheckReceipt || len(ch.Tail) > checkTail || len(ch.Quote) > checkTail || len(ch.Error) > maxCheckWords) {
			return bad("change %s: its receipt is named in at most %d bytes, its tail and quote at most %d bytes each, its words %d", c.ID, maxCheckReceipt, checkTail, maxCheckWords)
		}
		if ch := c.Check; ch != nil && !oneLineName(ch.Receipt, ch.Run, ch.Task, ch.Phase, ch.ID, ch.Subject, ch.Signal, ch.LogSHA256, ch.Class, ch.Chosen) {
			return bad("change %s: its check's receipt, identity, subject, signal, hash and classes are each one line of text, with no control character", c.ID)
		}
	}
	if !asked {
		return bad("a request asks for at least one change: questions and notes alone repair nothing")
	}
	return nil
}

// oneLineName says each of names is valid UTF-8 with no control character
// (C0, DEL, C1) and no line or paragraph separator: a name task.md writes
// in a line of its own words (who asked, an id, a path) can then never end
// that line and start one that reads as the file's own.
func oneLineName(names ...string) bool {
	for _, s := range names {
		if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool {
			return r < 0x20 || r >= 0x7f && r <= 0x9f || r == '\u2028' || r == '\u2029'
		}) {
			return false
		}
	}
	return true
}

// changeWords is a request's changes as a task's reason says them: "2
// changes, 1 question, 1 note".
func changeWords(cr *ChangeRequest) string {
	var parts []string
	for _, w := range []struct{ intent, one, many string }{{"change", "change", "changes"}, {"question", "question", "questions"}, {"note", "note", "notes"}} {
		n := 0
		for _, c := range cr.Changes {
			if c.Intent == w.intent {
				n++
			}
		}
		switch {
		case n == 1:
			parts = append(parts, "1 "+w.one)
		case n > 1:
			parts = append(parts, strconv.Itoa(n)+" "+w.many)
		}
	}
	return strings.Join(parts, ", ")
}

// validAddressed holds a result's addressed to its bounds: none for a
// review task, at most maxAddressed, each a change's id once with a note
// of at most maxAddressNote characters. Whether the request names each id
// is the attempt's repair's to say (names).
func validAddressed(kind string, addressed []Addressed) error {
	if len(addressed) == 0 {
		return nil
	}
	if kind == TaskReview {
		return fmt.Errorf("%w: a review's result addresses nothing: its findings say what it found", ErrBadResult)
	}
	if len(addressed) > maxAddressed {
		return fmt.Errorf("%w: addressed names more than %d changes", ErrBadResult, maxAddressed)
	}
	seen := map[string]bool{}
	for _, a := range addressed {
		if a.ID == "" || len(a.ID) > maxRequestRef || seen[a.ID] || utf8.RuneCountInString(a.Note) > maxAddressNote {
			return fmt.Errorf("%w: addressed names each change once by its id, with a note of at most %d characters", ErrBadResult, maxAddressNote)
		}
		seen[a.ID] = true
	}
	return nil
}

// names refuses an addressed that names a change the repair's request does
// not have (or any, with no person's request). rp may be nil.
func (rp *repair) names(addressed []Addressed) error {
	for _, a := range addressed {
		if rp == nil || rp.request == nil || !slices.ContainsFunc(rp.request.Changes, func(c RequestedChange) bool { return c.ID == a.ID }) {
			return fmt.Errorf("%w: addressed names a change the request does not have: %q", ErrBadResult, a.ID)
		}
	}
	return nil
}

// FitChangeRequest measures a person's change request for task of the crew
// c, as the run was launched (its row's crew, and pkg its launch package,
// nil for a run no ticket launched), before anything is recorded anywhere:
// the repair as the task's row keeps it, with the largest attempt and
// commits, within ledger.MaxPayload; and the repair attempt's task.md and
// task.json, each within maxTaskFile once fitTaskFile has cut what it may,
// with the crew's goal, the member's prompt and the task's instructions,
// the crew's name and people, the ticket and the package's files, the
// request after its cuts and its summary (never cut), beside the largest
// parts an attempt's file carries that are never cut (the first input's
// references, the attempt before with its failed check's identity and its
// error as cut, an attempt that could not be made usable, a project
// manifest at its bounds, a review task's findings the request keeps as
// cut), each text at its widest: once at four bytes a character, task.md's
// worst, and once at six, task.json's (Go's JSON escapes a control
// character, U+2028 and U+2029 as six bytes). A request past either is
// refused whole (ErrChangeRequestTooLarge), so one accepted never fails at
// its attempt's start for its size; one not of a batch's shape is
// ErrBadChangeRequest.
func FitChangeRequest(c Crew, pkg *Package, task string, cr ChangeRequest) error {
	if err := cr.validate(); err != nil {
		return err
	}
	i := slices.IndexFunc(c.Tasks, func(t Task) bool { return t.ID == task })
	if i < 0 {
		return ErrTaskNotFound
	}
	def := c.Tasks[i]
	prompt := ""
	if j := slices.IndexFunc(c.Members, func(m Member) bool { return m.Name == def.Member }); j >= 0 {
		prompt = c.Members[j].Prompt
	}
	rp := measuredRepair(cr)
	n, err := repairBytes(cr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadChangeRequest, err)
	}
	if n > ledger.MaxPayload {
		return fmt.Errorf("%w: the notes are %d KiB as the run keeps them, more than %d KiB: shorten them", ErrChangeRequestTooLarge, kib(n), ledger.MaxPayload>>10)
	}
	for _, wide := range []bool{false, true} {
		if _, _, _, err := fitTaskFile(worstRepairFile(c, pkg, def, prompt, rp, wide)); err != nil {
			var tl *taskFileTooLarge
			if errors.As(err, &tl) {
				return fmt.Errorf("%w: with %s's own instructions the task file would be %d KiB, more than %d KiB: shorten the notes or their paths", ErrChangeRequestTooLarge, def.ID, kib(max(tl.md, tl.js)), maxTaskFile>>10)
			}
			return err
		}
	}
	return nil
}

// measuredRepair is the repair cr makes at its largest: its own review's
// id (as JSON escapes it), the largest attempt, and a commit and a start
// of 64 hex digits (a SHA-256 repository's).
func measuredRepair(cr ChangeRequest) *repair {
	return requestRepair(cr, cr.Review, 1<<20, strings.Repeat("0", 64), strings.Repeat("0", 64))
}

// widest is a text of at most n characters (or, bytes true, n bytes) at
// its widest in the file wide names: in task.md (wide false) four bytes a
// character, in task.json (wide true) six, as JSON escapes U+2028; a text
// of bytes, at most n of them, is at its widest in task.json as U+2028s,
// six bytes for three, and in task.md as its n bytes.
func widest(n int, bytes, wide bool) string {
	switch {
	case bytes && wide:
		return strings.Repeat("\u2028", (n+2)/3)
	case bytes:
		return strings.Repeat("e", n)
	case wide:
		return strings.Repeat("\u2028", n)
	}
	return strings.Repeat("\U0001D11E", n)
}

// repairBytes is FitChangeRequest's first measure: cr's repair at its
// largest (measuredRepair) as the task's row keeps it, Go's JSON with its
// escapes ("<", ">", "&", U+2028 and U+2029 six bytes each). The web's
// count of a draft follows the same arithmetic, against the shared fixture
// testdata/change-request-payload.json.
func repairBytes(cr ChangeRequest) (int, error) {
	b, err := json.Marshal(measuredRepair(cr).taskRepair())
	return len(b), err
}

// kib is n bytes in KiB, rounded up.
func kib(n int) int { return (n + 1<<10 - 1) >> 10 }

// requestRepair is the repair a person's request makes of the candidate
// at commit made by attempt n: the summary the repair's, the request
// without it; start where the repair attempt starts, "" for the commit.
func requestRepair(cr ChangeRequest, review string, n int, commit, start string) *repair {
	req := cr.clone()
	req.Summary = ""
	return &repair{review: review, attempt: n, commit: commit, start: start, summary: cr.Summary, request: req}
}

// worstRepairFile is the repair attempt's task file for FitChangeRequest:
// the crew's own text, people and ticket, rp, and every part that is never
// cut at its largest, its texts at their widest in task.json (wide) or in
// task.md (widest).
func worstRepairFile(c Crew, pkg *Package, def Task, prompt string, rp *repair, wide bool) TaskFile {
	runID := c.ID + "-ffffffff"
	n := 1 << 20
	name := fmt.Sprintf("%s.%d", def.ID, n)
	commit := strings.Repeat("0", 64)
	f := TaskFile{Run: runID, Crew: c.ID, CrewName: c.Name, Goal: c.Goal, Task: def.ID, Attempt: n, MaxAttempts: n, Member: def.Member, Role: prompt, Kind: def.Kind,
		Instructions: def.Instructions, Worktree: filepath.Join(c.Cwd, worktreesDir, runID, name), Branch: "crew/" + runID + "/" + name, Prefix: c.Cwd, Start: commit,
		StartFrom: fmt.Sprintf("where attempt %d started: its result, which %s asked changes of, was refused before its checks", n, rp.request.By),
		Checks:    slices.Clone(def.Checks), Repair: rp.taskRepair(), WrittenAt: time.Now().UTC()}
	// A review task's findings the request keeps (keepReview), each cut
	// whole before the person's changes are: their review named, and the
	// cut said, at their largest.
	f.Repair.FindingsFrom = &FindingsFrom{Review: strings.Repeat("v", 40), Attempt: n, Commit: commit, Summary: "s"}
	f.Repair.Findings = slices.Repeat([]Finding{{Severity: "nit", Message: "m"}}, maxFindings)
	f.Owner, f.OwnerFrom, f.People = taskPeople(c, def)
	if pkg != nil {
		f.Ticket = &TaskTicket{Key: pkg.Ticket.Key, Rev: 1<<63 - 1, Plan: pkg.Plan.ID, PlanN: pkg.Plan.N, Digest: strings.Repeat("0", 64), Files: packageFilePaths(*pkg)}
		f.ticketTitle = cutTitle(pkg.Ticket.Title)
	}
	if len(def.Needs) > 0 {
		need := def.Needs[0]
		f.Inputs = []TaskInput{{Task: need, Attempt: n, Commit: commit, Branch: "crew/" + runID + "/" + fmt.Sprintf("%s.%d", need, n), Verdict: VerdictNeedsChanges}}
	}
	const idLen = 40 // a check's or a step's id, at its bound as a task's is
	id := strings.Repeat("v", idLen)
	check := &TaskCheck{Run: runID, Task: def.ID, Attempt: n, Phase: ledger.PhaseVerify, ID: id, K: 1 << 30, Subject: commit, Exit: -1, Signal: "killed", TimedOut: true,
		LogSHA256: commit, LogBytes: ledger.MaxLog, Log: taskDir + "/" + previousDir + "/check-" + id + ".log"}
	check.Receipt = fmt.Sprintf("%s/%s/%d/%s/%s/%d", check.Run, check.Task, check.Attempt, check.Phase, check.ID, check.K)
	// The attempt before's error is cut to maxPreviousError characters and
	// a mark; an unusable attempt's is never cut, and failUnusable has it
	// through session.CleanMessage, at most MaxAttentionMessage bytes.
	f.Previous = &TaskPrevious{Attempt: n, State: ledger.AttemptFailed, Stage: "verify " + id, Error: widest(maxPreviousError+1, false, wide), Result: commit, Branch: f.Branch, Check: check}
	f.Unusable = &TaskUnusable{Attempt: n, Stage: "setup " + id, Error: widest(session.MaxAttentionMessage, true, wide)}
	ports := map[string]int{}
	var services []string
	for i := range maxManifestPorts {
		ports[fmt.Sprintf("port%012d", i)] = maxPort
	}
	for i := range maxManifestServices {
		services = append(services, fmt.Sprintf("%s%d", id[:idLen-1], i))
	}
	f.Manifest = &TaskManifest{Source: sourceFile, Revision: 1<<63 - 1, Digest: commit, Ports: ports, Preview: widest(maxPreview, true, wide), Services: services}
	return f
}

// RequestChanges is a person's Request changes of a task's standing
// candidate, a command with a receipt (docs/plans/review-repair.md, RV1):
// the candidate rq names is repaired by the same transition a review
// task's needs_changes makes (reopenFor): its result dropped, the repair
// set (rq's request in its next attempt's task file, that attempt starting
// at the candidate, or where its attempt started when it was refused
// before its checks), one more attempt granted when the task's are spent,
// what rested on the result withdrawn, the decisions its attempts asked
// cancelled, and a finished run open again (reopenRun). A task a review
// task's findings reopened keeps them beside the request (keepReview).
// The reopen re-applies the task's gates as every reopen does (reopenRun's
// regate): a gate may ask a decision, and a task a decision holds stays
// held. No decision is answered; nothing is typed into a session but the
// task line of the repair attempt, a new attempt like any other.
//
// Refused before anything is recorded: a request not of a batch's shape,
// or one too large for the run to keep or for its attempt's task file
// (FitChangeRequest, with the run's crew and package as launched). Refused
// with a receipt, nothing changed: a review task (task_state, "request
// changes of the candidate it reviewed"); a candidate that does not stand
// (stale_candidate, in words that say what stands now): a succeeded task
// whose result is that attempt and commit stands, in a continuation by
// its reference (Imported), and so does a failed task whose standing
// failed candidate (standingFailed) is that attempt, having submitted that
// commit; a stopped run (run_stopped) and a continued one (run_continued),
// whose candidate a continuation carries; a reopen whose gates would ask
// again in a run with no room for them (decision_limit, retryNeedsRoom,
// as for a Retry). A run the engine does not hold
// is ErrRunNotFound. The same command sent again is its receipt again,
// never a second repair, and the same id naming another candidate or
// review a conflict, as for every command (act). Refused with a receipt
// too (ErrChangeRequestTooLarge, task_state): a request whose repair,
// with the review's findings it keeps, the run could not keep.
func (e *Engine) RequestChanges(ctx context.Context, runID, actor string, cmd Command, rq RepairRequest) (Receipt, error) {
	if err := rq.Request.validate(); err != nil {
		return Receipt{}, err
	}
	if e.ledger != nil {
		e.mu.Lock()
		r, ok := e.runs[runID]
		var crewJSON string
		var pkg *Package
		if ok {
			crewJSON = r.crewJSON
			if r.pkg != nil {
				p := r.pkg.p
				pkg = &p
			}
		}
		e.mu.Unlock()
		if ok {
			var c Crew
			if err := json.Unmarshal([]byte(crewJSON), &c); err != nil {
				return Receipt{}, fmt.Errorf("the run's crew as launched cannot be read: %w", err)
			}
			if err := FitChangeRequest(c, pkg, rq.Task, rq.Request); err != nil && !errors.Is(err, ErrTaskNotFound) {
				return Receipt{}, err
			}
		}
	}
	return e.act(ctx, runID, actor, cmd, intentRequestChanges, requestArgs(rq), func(r *run) (*task, []event, error) {
		t := r.task(rq.Task)
		if t == nil {
			return nil, nil, ErrTaskNotFound
		}
		if r.stopping || r.stoppedAt != nil {
			return t, nil, fmt.Errorf("%w: continue it, then request changes in its continuation", ErrRunStopped)
		}
		events, err := e.repairCandidate(r, t, rq)
		return t, events, err
	}, nil)
}

// repairCandidate is a person's repair of t's standing candidate, the
// change RequestChanges makes in its command's transaction and a
// continuation that carries the repair in its first (ContinueRunWith):
// the task a work task, the candidate rq names standing
// (standingCandidate), room for the gates its reopen asks again, the
// repair within what the run keeps, then the repair transition
// (reopenFor), one more attempt granted when the task's are spent. An
// error changes nothing. The caller holds e.mu, in a transaction's change.
func (e *Engine) repairCandidate(r *run, t *task, rq RepairRequest) ([]event, error) {
	if t.def.Kind != TaskWork {
		return nil, fmt.Errorf("%w: a review task's verdict is not repaired: request changes of the candidate it reviewed", ErrTaskState)
	}
	start, err := r.standingCandidate(t, rq)
	if err != nil {
		return nil, err
	}
	// The gates its reopen would ask again need their room, as a Retry's
	// do: refused before anything is recorded.
	if err := r.retryNeedsRoom(t, "request for changes"); err != nil {
		return nil, err
	}
	cr := rq.Request
	rp := requestRepair(cr, cr.Review, rq.Attempt, rq.Commit, start)
	rp.keepReview(t.repair)
	if b, err := json.Marshal(rp.taskRepair()); err != nil || len(b) > ledger.MaxPayload {
		kept := ""
		if rp.from != nil {
			kept = fmt.Sprintf(" with the %d findings of review %s it keeps,", len(rp.findings), rp.from.Review)
		}
		return nil, fmt.Errorf("%w:%s the repair is %d KiB as the run keeps it, more than %d KiB: shorten the notes", ErrChangeRequestTooLarge, kept, kib(len(b)), ledger.MaxPayload>>10)
	}
	who := cr.By
	ev := map[string]any{"review": cr.Review, "by": cr.By, "attempt": rq.Attempt}
	if rq.Imported != "" {
		ev["imported"] = rq.Imported
	}
	return e.reopenFor(r, t, rp, who, "changes requested by "+who+": "+changeWords(&cr), true, ev), nil
}

// requestArgs are a person's Request changes' arguments as its command
// keeps them, in RequestChanges and in a continuation that carries it
// alike: the same command sent to either is the same content.
func requestArgs(rq RepairRequest) []string {
	return []string{rq.Task, strconv.Itoa(rq.Attempt), rq.Commit, rq.Request.Review}
}

// keepReview carries into rp, a person's repair, the findings of a review
// task that old, the task's repair until now, holds: old's own (a review
// task's needs_changes reopened the task, and no attempt has made it
// succeed since) or those old kept (a person's request before this one
// kept them). A person's request never silently drops a review's
// findings: the repair attempt's task file has both, the person's changes
// first and the review's findings after them, and its cut takes the
// review's findings before any part of the person's changes.
func (rp *repair) keepReview(old *repair) {
	switch {
	case old == nil:
	case old.request == nil:
		rp.findings = slices.Clone(old.findings)
		rp.from = &FindingsFrom{Review: old.review, Attempt: old.attempt, Commit: old.commit, Summary: old.summary}
	case old.from != nil:
		from := *old.from
		rp.findings, rp.from = slices.Clone(old.findings), &from
	}
}

// standingCandidate checks that rq names t's standing candidate: its
// result (succeeded), or its standing failed candidate, having submitted
// that commit (failed); ErrStaleCandidate otherwise, in words that say
// what stands now. start is where the repair attempt starts when the
// candidate was refused before its checks (its attempt's start), "" for
// the candidate's commit. The caller holds e.mu, in the command's change.
func (r *run) standingCandidate(t *task, rq RepairRequest) (start string, err error) {
	named := rq.Imported
	if named == "" {
		named = fmt.Sprintf("%s/%s/%d", r.id, rq.Task, rq.Attempt)
	}
	switch t.state {
	case ledger.TaskSucceeded:
		if t.result == nil {
			break
		}
		standing := cmp.Or(t.imported, fmt.Sprintf("%s/%s/%d", r.id, t.def.ID, t.result.attempt))
		if standing != named || t.result.attempt != rq.Attempt || t.result.commit != rq.Commit {
			return "", fmt.Errorf("%w: the result is now attempt %d's %s: request changes of that candidate", ErrStaleCandidate, t.result.attempt, short(t.result.commit))
		}
		return "", nil
	case ledger.TaskFailed:
		fc, ok := r.standingFailed(t)
		if !ok {
			return "", fmt.Errorf("%w: the task has no failed candidate since its withdrawal: retry it", ErrStaleCandidate)
		}
		if standing := fmt.Sprintf("%s/%s/%d", fc.RunID, fc.TaskID, fc.N); standing != named || fc.N != rq.Attempt || fc.ResultCommit == "" || fc.ResultCommit != rq.Commit {
			if fc.ResultCommit == "" {
				return "", fmt.Errorf("%w: attempt %d failed since, at %s, without a result: retry the task", ErrStaleCandidate, fc.N, cmp.Or(fc.Stage, fc.State))
			}
			return "", fmt.Errorf("%w: attempt %d failed since, at %s: request changes of its result %s", ErrStaleCandidate, fc.N, cmp.Or(fc.Stage, fc.State), short(fc.ResultCommit))
		}
		if start := RepairStart(fc); start != fc.ResultCommit {
			return start, nil
		}
		return "", nil
	}
	if a := t.active(); a != nil {
		return "", fmt.Errorf("%w: attempt %d is %s: request changes of its result when it ends, or cancel it", ErrStaleCandidate, a.n, a.state)
	}
	return "", fmt.Errorf("%w: the task is %s, with no result to request changes of", ErrStaleCandidate, t.state)
}

// RepairStart is where a person's repair of the failed candidate a (its
// attempt's row, the task's standing failed candidate) starts, the rule
// standingCandidate applies: the attempt's own start when it was refused
// before its checks (refusedBeforeChecks: its result was never built on,
// so the repair starts where the attempt did), else its result, the
// candidate's commit. A succeeded candidate's repair starts at its result.
func RepairStart(a ledger.AttemptRow) string {
	if refusedBeforeChecks(a.Stage) && a.StartCommit != "" {
		return a.StartCommit
	}
	return a.ResultCommit
}

// The parts of a person's requested changes fitTaskFile cuts, in order.
const (
	cutLines = iota
	cutResolution
	cutMessage
)

// cutWords is what task.json's cut says of each part, by its count.
var cutWords = map[int]string{
	cutLines:      "the lines of %d of the requested changes",
	cutResolution: "the resolutions asked of %d of the requested changes",
	cutMessage:    "the words past " + strconv.Itoa(maxCutMessage) + " characters of %d of the requested changes",
}

// cutChanges cuts one part (cutLines: a change's lines and a check's
// output, its tail and quote; cutResolution; cutMessage: its words past
// maxCutMessage characters) of the last of f's requested changes that has
// it, into m's changes by its id, and says so in f's cut, once per part:
// false when none has it left. A change's id, kind, intent, severity,
// path, side and lines' numbers are never cut.
func cutChanges(f *TaskFile, m *TaskMore, part int) bool {
	if f.Repair == nil || f.Repair.Request == nil {
		return false
	}
	cs := f.Repair.Request.Changes
	has := func(c RequestedChange) bool {
		switch part {
		case cutLines:
			return len(c.Lines) > 0 || c.Check != nil && (c.Check.Tail != "" || c.Check.Quote != "")
		case cutResolution:
			return c.Resolution != ""
		}
		return !c.messageCut && utf8.RuneCountInString(c.Message) > maxCutMessage
	}
	at := -1
	for j := len(cs) - 1; j >= 0; j-- {
		if has(cs[j]) {
			at = j
			break
		}
	}
	if at < 0 {
		return false
	}
	c := &cs[at]
	mc := moreChange(m, cs, c.ID)
	n := 0
	switch part {
	case cutLines:
		mc.Lines, c.Lines, c.linesCut = c.Lines, nil, true
		if ch := c.Check; ch != nil {
			mc.Check = &RequestedCheck{Receipt: ch.Receipt, Tail: ch.Tail, Quote: ch.Quote}
			ch.Tail, ch.Quote = "", ""
		}
		for _, x := range cs {
			if x.linesCut {
				n++
			}
		}
	case cutResolution:
		mc.Resolution, c.Resolution, c.resolutionCut = c.Resolution, "", true
		for _, x := range cs {
			if x.resolutionCut {
				n++
			}
		}
	default:
		mc.Message, c.Message, c.messageCut = c.Message, string([]rune(c.Message)[:maxCutMessage])+"…", true
		for _, x := range cs {
			if x.messageCut {
				n++
			}
		}
	}
	prefix := strings.SplitN(cutWords[part], "%d", 2)[0]
	f.Cut = slices.DeleteFunc(f.Cut, func(s string) bool {
		return strings.HasPrefix(s, prefix) && strings.HasSuffix(s, "of the requested changes")
	})
	f.Cut = append(f.Cut, fmt.Sprintf(cutWords[part], n))
	return true
}

// moreChange is m's entry for the change id, made in the request's order
// when there is none yet.
func moreChange(m *TaskMore, cs []RequestedChange, id string) *RequestedChange {
	if i := slices.IndexFunc(m.Changes, func(c RequestedChange) bool { return c.ID == id }); i >= 0 {
		return &m.Changes[i]
	}
	order := func(id string) int { return slices.IndexFunc(cs, func(c RequestedChange) bool { return c.ID == id }) }
	at := len(m.Changes)
	for i, c := range m.Changes {
		if order(c.ID) > order(id) {
			at = i
			break
		}
	}
	m.Changes = slices.Insert(m.Changes, at, RequestedChange{ID: id})
	return &m.Changes[at]
}

// writeChangeRequest writes task.md's "Findings to address" for a
// person's request: who asked for changes to which commit, in which
// review, against which base, and where this attempt starts; their
// summary; then the changes asked for, the questions to answer and the
// notes, each with its id, its severity, its location, the lines as the
// person saw them, the words and the resolution asked; the findings of a
// review task the request keeps, after them; and how to say what was
// addressed. The person's words of more than one line (a summary, a
// change's words, a resolution) are fenced under their item, so no line
// of theirs reads as one of the file's own sections.
func writeChangeRequest(b *strings.Builder, f TaskFile) {
	rp := f.Repair
	cr := rp.Request
	fmt.Fprintf(b, "## Findings to address\n\n%s asked for changes to the commit %s (review %s", cr.By, rp.Commit, rp.Review)
	if cr.Base != "" {
		fmt.Fprintf(b, ", against the base %s", cr.Base)
	}
	b.WriteString(")")
	switch {
	case rp.Start != "" && rp.Start == f.Start:
		fmt.Fprintf(b, ". That commit was refused before its checks, so this attempt starts where its attempt %d started: carry its work over and mend what the refusal names (The attempt before)", rp.Attempt)
	case rp.Commit == f.Start:
		b.WriteString(", where this attempt starts")
	}
	b.WriteString(".")
	if s := strings.TrimSpace(rp.Summary); s != "" && singleLine(s) {
		fmt.Fprintf(b, " Their summary: %s\n", s)
	} else if s != "" {
		b.WriteString(" Their summary:\n")
		writeIndentedFence(b, s)
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	for _, g := range []struct{ intent, head string }{
		{"change", "Changes asked for"},
		{"question", "Questions to answer in your summary"},
		{"note", "Notes, for context"},
	} {
		first := true
		for _, c := range cr.Changes {
			if c.Intent != g.intent {
				continue
			}
			if first {
				fmt.Fprintf(b, "### %s\n\n", g.head)
				first = false
			}
			writeChange(b, f, c)
		}
	}
	if from := rp.FindingsFrom; from != nil && (len(rp.Findings) > 0 || slices.ContainsFunc(f.Cut, func(s string) bool { return strings.HasSuffix(s, "of the review's findings") })) {
		fmt.Fprintf(b, "### The findings of review %q (attempt %d), which this request keeps\n\nReview %q asked for changes to the commit %s before this request, and they stand: address each of these too, or say in your summary why not; the review runs again on your result.", from.Review, from.Attempt, from.Review, from.Commit)
		if s := strings.TrimSpace(from.Summary); s != "" {
			fmt.Fprintf(b, " Its summary: %s", s)
		}
		b.WriteString("\n\n")
		writeFindings(b, rp.Findings)
		if len(rp.Findings) > 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("Say which you addressed with `addressed` in your result (each change's id, and a note); a person checks each one on your result.\n\n")
}

// writeChange writes one requested change as a list item.
func writeChange(b *strings.Builder, f TaskFile, c RequestedChange) {
	fmt.Fprintf(b, "- %s", inlineCode(c.ID))
	if c.Severity != "" {
		fmt.Fprintf(b, " (%s)", c.Severity)
	}
	fmt.Fprintf(b, ", %s:", changeLocation(c))
	cut := ""
	if c.messageCut {
		cut = "(the rest is in the file named under \"Cut to fit\")"
	}
	if msg := strings.TrimSpace(c.Message); singleLine(msg) {
		if msg != "" {
			b.WriteString(" " + msg)
		}
		if cut != "" {
			b.WriteString(" " + cut)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
		writeIndentedFence(b, msg)
		if cut != "" {
			b.WriteString("\n  " + cut + "\n")
		}
	}
	if len(c.Lines) > 0 {
		writeIndentedFence(b, strings.Join(c.Lines, "\n"))
	}
	if ch := c.Check; ch != nil {
		writeRequestedCheck(b, f, ch)
	}
	if c.linesCut {
		b.WriteString("\n  Its lines are in the file named under \"Cut to fit\".\n")
	}
	if r := strings.TrimSpace(c.Resolution); r != "" && singleLine(r) {
		fmt.Fprintf(b, "\n  Asked: %s\n", r)
	} else if r != "" {
		b.WriteString("\n  Asked:\n")
		writeIndentedFence(b, r)
	} else if c.resolutionCut {
		b.WriteString("\n  The resolution asked is in the file named under \"Cut to fit\".\n")
	}
	b.WriteString("\n")
}

// changeLocation says where a change is: a path's lines or the whole file
// on one side, a check's receipt, or the candidate as a whole.
func changeLocation(c RequestedChange) string {
	side := ""
	switch c.Side {
	case "candidate":
		side = ", the candidate's side"
	case "base":
		side = ", the base's side (lines the candidate removed)"
	}
	switch c.Kind {
	case "line":
		at := strconv.Itoa(c.From)
		if c.To > c.From {
			at += "–" + strconv.Itoa(c.To)
		}
		return inlineCode(c.Path+":"+at) + side
	case "file":
		return inlineCode(c.Path) + ", the whole file" + side
	case "check":
		if c.Check != nil && c.Check.ID != "" {
			return "the check " + inlineCode(c.Check.ID)
		}
		return "a check"
	}
	return "the candidate as a whole"
}

// writeRequestedCheck says a check finding's receipt: its identity, the
// commit it failed and how, the class the server gave it and the one the
// person chose, where its log is, and its output as the person quoted it
// and the ledger kept its last.
func writeRequestedCheck(b *strings.Builder, f TaskFile, ch *RequestedCheck) {
	fmt.Fprintf(b, "\n  Its receipt in the ledger is %s", ch.Receipt)
	if ch.Subject != "" {
		fmt.Fprintf(b, ", on the commit %s", ch.Subject)
	}
	var how []string
	switch {
	case ch.TimedOut:
		how = append(how, "timed out")
	case ch.Signal != "":
		how = append(how, "ended by "+ch.Signal)
	case ch.Exit != nil:
		how = append(how, "exit "+strconv.Itoa(*ch.Exit))
	}
	if words := strings.Join(strings.Fields(ch.Error), " "); words != "" {
		how = append(how, words)
	}
	if len(how) > 0 {
		fmt.Fprintf(b, " (%s)", strings.Join(how, ": "))
	}
	if ch.Class != "" {
		fmt.Fprintf(b, "; Conductor classed it %s", ch.Class)
	}
	if ch.Chosen != "" && ch.Chosen != ch.Class {
		fmt.Fprintf(b, ", and the person chose %s", ch.Chosen)
	} else if ch.Chosen != "" {
		b.WriteString(", as the person did")
	}
	if p := f.Previous; p != nil && p.Check != nil && p.Check.Receipt == ch.Receipt && p.Check.Log != "" {
		fmt.Fprintf(b, ". Its whole log is `%s`", p.Check.Log)
	} else {
		b.WriteString(". Its whole log is the ledger's, by that receipt")
	}
	b.WriteString(".\n")
	if ch.Quote != "" {
		b.WriteString("\n  They quote:\n")
		writeIndentedFence(b, ch.Quote)
	}
	if ch.Tail != "" {
		b.WriteString("\n  Its last output:\n")
		writeIndentedFence(b, ch.Tail)
	}
}

// singleLine says s has no line break a Markdown reader would take as one
// (a line feed, a carriage return, a vertical tab, a form feed, U+0085,
// U+2028 or U+2029).
func singleLine(s string) bool {
	return !strings.ContainsAny(s, "\n\r\v\f\u0085\u2028\u2029")
}

// writeIndentedFence writes s in a fence longer than any run of backticks
// in it, indented to sit under a list item. Its line breaks are each made
// a line feed first, so none of its lines can end the fence.
func writeIndentedFence(b *strings.Builder, s string) {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\v", "\n", "\f", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n").Replace(s)
	fence := strings.Repeat("`", max(3, longestBacktickRun(s)+1))
	b.WriteString("\n  " + fence + "\n")
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("  " + fence + "\n")
}

// inlineCode is s as inline code, its delimiter longer than any run of
// backticks in it.
func inlineCode(s string) string {
	n := longestBacktickRun(s)
	if n == 0 {
		return "`" + s + "`"
	}
	d := strings.Repeat("`", n+1)
	return d + " " + s + " " + d
}
