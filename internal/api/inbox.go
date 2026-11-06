package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The decision inbox (Program item MB0, docs/plans/decision-inbox.md,
// PR-1): the read model of the page "Needs your decision", built on each
// read from what the server holds (the sessions' questions, the engine's
// failed tasks, Dispatch's open plan revisions, Brakeman's decision cards)
// and from nothing of its own but the routes a router gave (router.go,
// keepRoute) and the hosted sessions' resumptions (hostedResumes). It
// writes nothing: every action names its operation's own route and the
// revision that operation checks (Expect), and the page acts through it.
// An item is named by Brakeman's ref of what it is about.

// The inbox's bounds, encoded.
const (
	// inboxPage is how many summaries a page of GET /api/inbox holds.
	inboxPage = 150
	// maxInboxSummary, maxInboxItem, maxInboxReply and maxInboxTrack bound
	// a summary, an item, the list's reply and a track's reply.
	maxInboxSummary = 4 << 10
	maxInboxItem    = 96 << 10
	maxInboxReply   = 640 << 10
	maxInboxTrack   = 8 << 10
	// maxInboxRef bounds a ref, encoded (Brakeman's MaxRef raw).
	maxInboxRef = brakeman.MaxRef
	// maxInboxCursor bounds the after parameter.
	maxInboxCursor = 600
	// inboxRefs bounds an item's evidence and its blocking refs.
	inboxRefs = 20
	// inboxActions bounds an item's actions.
	inboxActions = 10
	// inboxBriefs is how many of Brakeman's newest requests are read for
	// their decision cards.
	inboxBriefs = 20
)

// The kinds of an item.
const (
	inboxQuestion = "question"
	inboxTask     = "task"
	inboxPlan     = "plan"
)

// The answerer of every item in the open-source build: the operator
// (decision D4).
const inboxAnswerer = "you, the operator"

// inboxSummary is one item as the list shows it, folded: at most 4 KiB
// encoded. It carries no action's input or scope: the page reads the item
// before it acts.
type inboxSummary struct {
	Ref      string    `json:"ref"`
	Kind     string    `json:"kind"`
	Since    time.Time `json:"since"`
	Title    string    `json:"title"`    // 200
	Brief    string    `json:"brief"`    // 280
	Question string    `json:"question"` // 280: the question, cut
	Answerer string    `json:"answerer"` // 120
	Routed   int       `json:"routed,omitempty"`
	Blocks   int       `json:"blocks,omitempty"`
	Actions  []string  `json:"actions"` // at most 10 labels of 80
	Card     bool      `json:"card,omitempty"`
}

// inboxItem is one thing a person must decide, whole: at most 96 KiB
// encoded. Exact fields (Question, the refs, an action's Input and Expect,
// receipts) are never cut; display fields are cut on a rune boundary until
// their encoded form fits.
type inboxItem struct {
	Ref         string             `json:"ref"`
	Subject     string             `json:"subject"`
	Kind        string             `json:"kind"`
	Since       time.Time          `json:"since"`
	Title       string             `json:"title"`   // 200
	Brief       string             `json:"brief"`   // 2 KiB
	BriefBy     string             `json:"briefBy"` // conductor | brakeman
	Card        string             `json:"card,omitempty"`
	Question    string             `json:"question"` // exact: at most 500 raw bytes
	Answerer    string             `json:"answerer"`
	RoutedTo    []inboxDestination `json:"routedTo,omitempty"`
	RoutedRule  string             `json:"routedRule,omitempty"` // 200
	Revision    string             `json:"revision"`             // 64
	Now         string             `json:"now,omitempty"`        // 500
	Blocking    inboxBlocking      `json:"blocking"`
	Actions     []inboxAction      `json:"actions"`
	WhyHuman    string             `json:"whyHuman,omitempty"`   // 1 KiB
	Considered  []cardAlternative  `json:"considered,omitempty"` // at most 6
	Recommended int                `json:"recommended"`
	Uncertainty string             `json:"uncertainty,omitempty"` // 1 KiB
	Authorizes  string             `json:"authorizes,omitempty"`  // 1 KiB, Brakeman's
	CardRev     string             `json:"cardRev,omitempty"`     // 64
	CardNote    string             `json:"cardNote,omitempty"`    // 200: the card read at another revision
	Evidence    []inboxRef         `json:"evidence,omitempty"`
	Delivery    []deliveryStep     `json:"delivery,omitempty"`
}

// inboxAction is one thing a person may do with an item, through its own
// operation's route, at the revision that operation checks (Expect).
type inboxAction struct {
	Name      string `json:"name"`            // answer | retry | accept | cancel | approve | withdraw
	Label     string `json:"label"`           // 80
	Input     string `json:"input,omitempty"` // exact: a choice's keys
	Text      bool   `json:"text,omitempty"`  // a line is typed
	Expect    string `json:"expect"`          // exact
	TicketRev int64  `json:"ticketRev,omitempty"`
	Scope     string `json:"scope"` // 1 KiB
	Confirm   bool   `json:"confirm,omitempty"`
	Route     string `json:"route"` // 200
	// Track is the ref the action's steps are read under
	// (GET /api/inbox/track), when it is not the item's own: a task's
	// command carried by a blocker's question is tracked as the task's.
	Track string `json:"track,omitempty"`
}

// deliveryStep is one of an action's four steps.
type deliveryStep struct {
	Step    string     `json:"step"`  // available | recorded | validated | resumed
	State   string     `json:"state"` // done | waiting | refused | not_applicable | unknown
	At      *time.Time `json:"at,omitempty"`
	Words   string     `json:"words"` // 500
	Receipt string     `json:"receipt,omitempty"`
}

// inboxBlocking is what waits for an item: words, at most 20 refs, and how
// many more.
type inboxBlocking struct {
	Words string   `json:"words"` // 500
	Refs  []string `json:"refs"`
	More  int      `json:"more,omitempty"`
}

// inboxRef is an evidence entry: a ref and its label.
type inboxRef struct {
	Ref   string `json:"ref"`
	Label string `json:"label"` // 200
}

// inboxDestination is whom a router said a question went to.
type inboxDestination struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// cardAlternative is one of a Brakeman card's alternatives, as text.
type cardAlternative struct {
	Label     string `json:"label"`     // 320
	Tradeoffs string `json:"tradeoffs"` // 1 KiB
}

// The step names and states.
const (
	stepAvailable = "available"
	stepRecorded  = "recorded"
	stepValidated = "validated"
	stepResumed   = "resumed"

	stepDone          = "done"
	stepWaiting       = "waiting"
	stepRefused       = "refused"
	stepNotApplicable = "not_applicable"
	stepUnknown       = "unknown"
)

// jsonLen is how many bytes s takes in JSON, its quotes left out:
// encoding/json escapes a control character, a byte of invalid UTF-8,
// '<', '>' and '&' in six bytes, and U+2028 and U+2029 too.
func jsonLen(s string) int {
	b, _ := json.Marshal(s)
	return len(b) - 2
}

// fitText is s as a display field of at most max encoded bytes: whole when it
// fits, else cut on a rune boundary with "…" until its encoded form fits.
func fitText(s string, max int) string {
	if jsonLen(s) <= max {
		return s
	}
	rs := []rune(s)
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if jsonLen(string(rs[:mid])+"…") <= max {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(rs[:lo]) + "…"
}

// refFits reports whether ref is short enough to carry whole: an
// evidence or blocking ref past it is left out, never cut.
func refFits(ref string) bool { return ref != "" && jsonLen(ref) <= maxInboxRef }

// addRef adds an evidence entry, at most inboxRefs, none twice.
func (it *inboxItem) addRef(ref, label string) {
	if !refFits(ref) || len(it.Evidence) >= inboxRefs || slices.ContainsFunc(it.Evidence, func(e inboxRef) bool { return e.Ref == ref }) {
		return
	}
	it.Evidence = append(it.Evidence, inboxRef{Ref: ref, Label: fitText(label, 200)})
}

// block adds a ref to what waits for the item: past inboxRefs, counted.
func (it *inboxItem) block(ref string) {
	if slices.Contains(it.Blocking.Refs, ref) {
		return
	}
	if !refFits(ref) || len(it.Blocking.Refs) >= inboxRefs {
		it.Blocking.More++
		return
	}
	it.Blocking.Refs = append(it.Blocking.Refs, ref)
}

// addAction adds an action, at most inboxActions, its display fields cut.
func (it *inboxItem) addAction(a inboxAction) {
	if len(it.Actions) >= inboxActions {
		return
	}
	a.Label, a.Scope, a.Route = fitText(a.Label, 80), fitText(a.Scope, 1<<10), fitText(a.Route, 200)
	it.Actions = append(it.Actions, a)
}

// summary is the item as the list shows it.
func (it *inboxItem) summary() inboxSummary {
	s := inboxSummary{Ref: it.Ref, Kind: it.Kind, Since: it.Since, Title: fitText(it.Title, 200), Brief: fitText(it.Brief, 280),
		Question: fitText(it.Question, 280), Answerer: fitText(it.Answerer, 120), Routed: len(it.RoutedTo),
		Blocks: len(it.Blocking.Refs) + it.Blocking.More, Actions: []string{}, Card: it.Card != ""}
	for _, a := range it.Actions {
		s.Actions = append(s.Actions, fitText(a.Label, 80))
	}
	return s
}

// inboxCursor is where a page begins: after the item (since, ref).
type inboxCursor struct {
	since time.Time
	ref   string
}

// before orders items oldest first, by (since, ref).
func (it *inboxItem) before(c inboxCursor) int {
	if n := it.Since.Compare(c.since); n != 0 {
		return n
	}
	return strings.Compare(it.Ref, c.ref)
}

// cursorOf is the cursor after the item.
func cursorOf(it *inboxItem) string { return it.Since.UTC().Format(time.RFC3339Nano) + " " + it.Ref }

// parseCursor reads an after parameter: "<since> <ref>".
func parseCursor(v string) (inboxCursor, error) {
	if v == "" {
		return inboxCursor{}, nil
	}
	at, ref, ok := strings.Cut(v, " ")
	if !ok || len(v) > maxInboxCursor || ref == "" {
		return inboxCursor{}, errors.New("after is the next a page gave")
	}
	since, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return inboxCursor{}, errors.New("after is the next a page gave")
	}
	return inboxCursor{since: since, ref: ref}, nil
}

// inboxRead is what one read of the inbox holds of the sources it does
// not build items from on its own: Brakeman's cards by subject, read once.
type inboxRead struct {
	s     *Server
	ctx   context.Context
	cards map[string]inboxCard
	// bodies and plans are what the read took of Dispatch once: the names
	// of a revision's body by its digest, a ticket's revisions by its key.
	bodies map[string]planBodyRead
	plans  map[string][]dispatch.PlanSummary
	// planRefs, when a test sets it, says whether Brakeman's resolver
	// takes the plan kind (brakeman.ParseRef); nil asks it.
	planRefs func() bool
}

// inboxCard is a Brakeman decision card and where it is kept.
type inboxCard struct {
	request string
	index   int
	d       brakeman.Decision
}

// planRefsParsed, when a test sets it, stands for brakeman.ParseRef's
// taking the plan kind; nil outside tests.
var planRefsParsed func() bool

// newInboxRead begins a read: the newest decision card about each subject
// among Brakeman's newest inboxBriefs requests with a kept brief, read-only
// from its store; none without Brakeman.
func (s *Server) newInboxRead(ctx context.Context) *inboxRead {
	rd := &inboxRead{s: s, ctx: ctx, cards: map[string]inboxCard{}, bodies: map[string]planBodyRead{}, plans: map[string][]dispatch.PlanSummary{}, planRefs: planRefsParsed}
	if s.brakeman == nil || s.brakeman.st == nil {
		return rd
	}
	page, _, err := s.brakeman.st.List("", inboxBriefs)
	if err != nil {
		return rd
	}
	for _, req := range page { // newest first
		if !req.HasBrief {
			continue
		}
		b, _, err := s.brakeman.st.Brief(req.ID)
		if err != nil {
			continue
		}
		for i := len(b.Items) - 1; i >= 0; i-- {
			it := b.Items[i]
			if it.Kind != brakeman.ItemDecision || it.Decision == nil {
				continue
			}
			about := it.Decision.About.String()
			if _, seen := rd.cards[about]; !seen {
				rd.cards[about] = inboxCard{request: req.ID, index: i, d: *it.Decision}
			}
		}
	}
	return rd
}

// withCard fills it from Brakeman's card about its subject, when there is
// one: the brief, why a person, the alternatives as text, the
// recommendation, the uncertainty, what answering authorizes (Brakeman's,
// beside each action's scope), the revision it was written at, and its
// evidence and blocked refs after Conductor's own.
func (rd *inboxRead) withCard(it *inboxItem) {
	it.Recommended = -1
	c, ok := rd.cards[it.Subject]
	if !ok {
		return
	}
	d := c.d
	it.Brief, it.BriefBy, it.Card = fitText(d.Happened, 2<<10), "brakeman", fmt.Sprintf("brief:%s/%d", c.request, c.index)
	it.WhyHuman, it.Uncertainty, it.Authorizes = fitText(d.WhyHuman, 1<<10), fitText(d.Uncertainty, 1<<10), fitText(d.Authorizes, 1<<10)
	for i, a := range d.Alternatives {
		if i == 6 {
			break
		}
		it.Considered = append(it.Considered, cardAlternative{Label: fitText(a.Label, 320), Tradeoffs: fitText(a.Tradeoffs, 1<<10)})
	}
	if d.Recommendation != nil && *d.Recommendation >= 0 && *d.Recommendation < len(it.Considered) {
		it.Recommended = *d.Recommendation
	}
	it.CardRev = fitText(d.SubjectRevision, 64)
	if it.CardRev != "" && it.CardRev != it.Revision {
		it.CardNote = fitText(fmt.Sprintf("Brakeman read this at revision %s; it is at %s now", it.CardRev, it.Revision), 200)
	}
	for _, ref := range d.Evidence {
		it.addRef(ref.String(), "Brakeman's evidence")
	}
	for _, ref := range d.Blocked {
		it.block(ref.String())
	}
}

// agentName is the name the catalog gives the session's agent.
func (s *Server) agentName(info session.Info) string {
	if a, ok := s.Catalog().Get(info.AgentID); ok && a.Name != "" {
		return a.Name
	}
	if info.AgentID != "" {
		return info.AgentID
	}
	return "The agent"
}

// questionRef is a session's question's ref.
func questionRef(sess, id string) string { return "question:" + sess + "/" + id }

// keysWords says a choice's keys as they are typed: the text, and Enter
// for a carriage return.
func keysWords(input string) string {
	enter := strings.HasSuffix(input, "\r")
	text := strings.TrimSuffix(input, "\r")
	words := strconv.Quote(text)
	if text == "" {
		words = ""
	}
	switch {
	case enter && words != "":
		return words + " and Enter"
	case enter:
		return "Enter"
	}
	return words
}

// questionItem is the item of the question info shows, false when it
// shows none an item is made of: not listed, no needs_input with an id,
// or answered in the terminal. A turn held for the terminal is an item
// with no answer: the session refuses every reply naming it.
func (rd *inboxRead) questionItem(info session.Info) (*inboxItem, bool) {
	a := info.Attention
	if a.State != session.AttentionNeedsInput || a.ID == "" || a.Answered == session.AnsweredTerminal || info.Status.Ended() {
		return nil, false
	}
	s := rd.s
	agent := s.agentName(info)
	ref := questionRef(info.ID, a.ID)
	it := &inboxItem{Ref: ref, Subject: ref, Kind: inboxQuestion, Question: a.Message, Answerer: inboxAnswerer, Revision: a.ID, BriefBy: "conductor", Actions: []inboxAction{}}
	if a.Since != nil {
		it.Since = *a.Since
	} else {
		it.Since = info.CreatedAt
	}
	it.Title = fitText(agent+" in "+info.Name+" asks", 200)
	brief := agent + " in " + info.Name + " asks"
	var run crew.Run
	var task *crew.TaskState
	if c := info.Crew; c != nil && c.Task != "" && s.runs != nil {
		if r, ok := s.runs.Get(c.RunID); ok {
			run = r
			for i := range r.Tasks {
				if r.Tasks[i].ID == c.Task {
					task = &r.Tasks[i]
				}
			}
		}
		brief += fmt.Sprintf("; it is attempt %s.%d of run %s", c.Task, c.Attempt, c.RunID)
		if run.Ticket != nil {
			brief += " for " + run.Ticket.Key
		}
	}
	held := a.Source == session.SourceSubmit
	it.addRef("session:"+info.ID, "the session "+info.Name+"'s terminal")
	if held {
		words := agent + "'s turn is held for keys typed in its terminal: open the terminal to go on"
		it.Brief, it.Blocking.Words = fitText(words, 2<<10), fitText(words, 500)
	} else {
		it.Brief = fitText(brief, 2<<10)
		it.Blocking.Words = fitText(agent+" does nothing until it is answered", 500)
		// The choices, a mode choice last as the reply bar orders them,
		// then a line for a question that takes one.
		opts := slices.Clone(a.Options)
		slices.SortStableFunc(opts, func(x, y session.Option) int {
			return cmp.Compare(btoi(x.Mode), btoi(y.Mode))
		})
		plan := ""
		if a.Plan != "" {
			plan = "; it approves the plan at " + a.Plan
		}
		route := "/ws/sessions/" + info.ID
		for _, o := range opts {
			it.addAction(inboxAction{Name: "answer", Label: o.Label, Input: o.Input, Expect: a.ID, Route: route,
				Scope: fmt.Sprintf("Types %s into the terminal of %s: the choice '%s'%s", keysWords(o.Input), info.Name, o.Label, plan)})
		}
		if a.Kind == session.KindPrompt || len(a.Options) == 0 {
			it.addAction(inboxAction{Name: "answer", Label: "Reply with a line", Text: true, Expect: a.ID, Route: route,
				Scope: fmt.Sprintf("Types the line you write into the terminal of %s and enters it%s", info.Name, plan)})
		}
		if rt, ok := s.router.routeOf(info.ID, a.ID); ok {
			for _, d := range rt.To {
				it.RoutedTo = append(it.RoutedTo, inboxDestination{Kind: d.Kind, Name: d.Name})
			}
			it.RoutedRule = fitText(rt.Rule, 200)
		}
	}
	if c := info.Crew; c != nil && c.Task != "" {
		it.addRef("run:"+c.RunID, "the run")
		it.addRef("task:"+c.RunID+"/"+c.Task, "the task "+c.Task)
		if c.Attempt > 0 {
			it.addRef(fmt.Sprintf("attempt:%s/%s/%d", c.RunID, c.Task, c.Attempt), fmt.Sprintf("attempt %d", c.Attempt))
		}
		it.block("task:" + c.RunID + "/" + c.Task)
		for _, d := range dependents(run, c.Task) {
			it.block("task:" + c.RunID + "/" + d)
		}
		if run.Ticket != nil {
			it.addRef("ticket:"+run.Ticket.Key, run.Ticket.Key)
		}
		// A blocker the attempt reported is this question: it names the
		// task and carries the task's commands, each at the run's revision.
		if task != nil && task.Blocker != nil && task.Attempt != nil && task.Attempt.SessionID == info.ID && !held {
			it.Brief = fitText(brief+"; "+c.Task+" is blocked: "+task.Blocker.Reason, 2<<10)
			if set, ok := rd.commandsOf(run.ID, c.Task); ok {
				rd.taskActions(it, run, set, " task "+c.Task)
			}
		}
	}
	rd.withCard(it)
	return it, true
}

// btoi is 1 for true.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// dependents is the tasks of the run that need task, transitively, each
// once, in the run's order.
func dependents(run crew.Run, task string) []string {
	need := map[string]bool{task: true}
	var out []string
	for changed := true; changed; {
		changed = false
		for _, t := range run.Tasks {
			if need[t.ID] {
				continue
			}
			if slices.ContainsFunc(t.Needs, func(n string) bool { return need[n] }) {
				need[t.ID], changed = true, true
				out = append(out, t.ID)
			}
		}
	}
	return out
}

// commandsOf is what the engine allows of the run's task's commands now,
// with the task as it stood when they were judged (one read, under the
// engine's lock): false when the engine does not hold the run or the task.
func (rd *inboxRead) commandsOf(runID, taskID string) (crew.TaskCommandSet, bool) {
	if rd.s.runs == nil {
		return crew.TaskCommandSet{}, false
	}
	set, err := rd.s.runs.TaskCommands(runID, taskID)
	return set, err == nil
}

// taskActions adds the commands set allows to it, each with the run's
// revision as judged as its Expect, described from the task the set was
// judged on; labelled with suffix, and tracked as the task's when the item
// is not the task's own.
func (rd *inboxRead) taskActions(it *inboxItem, run crew.Run, set crew.TaskCommandSet, suffix string) {
	task := set.Task
	rev := strconv.FormatInt(set.Revision, 10)
	deps := dependents(run, task.ID)
	depWords := "nothing else waits for it"
	if len(deps) > 0 {
		depWords = strings.Join(deps, ", ") + " wait for it"
	}
	ref := "task:" + run.ID + "/" + task.ID
	track := ""
	if it.Ref != ref {
		track = ref
	}
	base := "/api/runs/" + run.ID + "/tasks/" + task.ID + "/"
	for _, name := range set.Commands {
		a := inboxAction{Name: name, Expect: rev, Route: http.MethodPost + " " + base + name, Track: track}
		switch name {
		case crew.TaskRetry:
			start := "the commit its inputs give it"
			if set.RetryStart != "" {
				start = "commit " + short(set.RetryStart)
			}
			a.Label = "Retry" + suffix
			a.Scope = fmt.Sprintf("Allows %s one more attempt (attempt %d), started at %s with what its last attempt found; %s; at run revision %s",
				task.ID, task.Attempts+1, start, depWords, rev)
		case crew.TaskAccept:
			a.Label, a.Confirm = "Accept anyway"+suffix, true
			a.Scope = fmt.Sprintf("Takes attempt %d's commit %s as %s's result though it failed its checks; %s and start from it; at run revision %s",
				set.AcceptAttempt, short(set.AcceptCommit), task.ID, depWords, rev)
		case crew.TaskCancel:
			a.Label, a.Confirm = "Cancel"+suffix, true
			blocked := "nothing else is blocked by it"
			if len(deps) > 0 {
				blocked = strings.Join(deps, ", ") + " are blocked for good"
			}
			a.Scope = fmt.Sprintf("Cancels %s: its attempt in flight is stopped and %s; at run revision %s", task.ID, blocked, rev)
		}
		it.addAction(a)
	}
}

// short is a commit's first 12 hex digits.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// taskItem is the item of the run's task, and whether it is open: failed,
// with a command its state allows. A task blocked only by a failed input
// is no item of its own: the input's names it. The task and its commands
// are described as the engine judged the commands, in one read; a run the
// engine does not hold (its record, a run stopped and let go) offers none.
func (rd *inboxRead) taskItem(run crew.Run, task crew.TaskState) (*inboxItem, bool) {
	set, held := rd.commandsOf(run.ID, task.ID)
	if held {
		task = set.Task
	}
	ref := "task:" + run.ID + "/" + task.ID
	it := &inboxItem{Ref: ref, Subject: ref, Kind: inboxTask, Answerer: inboxAnswerer, BriefBy: "conductor", Actions: []inboxAction{}, Since: run.StartedAt}
	if run.Managed != nil {
		it.Revision = strconv.FormatInt(run.Managed.Revision, 10)
	}
	if a := task.Attempt; a != nil && a.EndedAt != nil {
		it.Since = *a.EndedAt
	}
	it.Title = fitText(fmt.Sprintf("%s %s in run %s", task.ID, task.State, run.Name), 200)
	words := fmt.Sprintf("Task %s of run %s is %s after %d attempts", task.ID, run.Name, task.State, task.Attempts)
	if task.Reason != "" {
		words += ": " + task.Reason
	}
	if run.Ticket != nil {
		words += "; the run is for " + run.Ticket.Key
	}
	it.Brief = fitText(words, 2<<10)
	open := false
	if held {
		it.Revision = strconv.FormatInt(set.Revision, 10)
		if task.State == "failed" {
			rd.taskActions(it, run, set, "")
			open = len(set.Commands) > 0
		}
	}
	switch {
	case !held:
		it.Now = fitText(fmt.Sprintf("run %s is no longer held by the engine (it stopped and was let go): %s is %s, and a command on it needs the run continued", run.Name, task.ID, task.State), 500)
	case !open:
		it.Now = fitText(fmt.Sprintf("%s is %s now", task.ID, task.State), 500)
	}
	deps := dependents(run, task.ID)
	for _, d := range deps {
		it.block("task:" + run.ID + "/" + d)
	}
	it.block("run:" + run.ID)
	it.Blocking.Words = fitText(fmt.Sprintf("%d tasks wait for %s, and the run does not end until it is decided", len(deps), task.ID), 500)
	it.addRef("run:"+run.ID, "the run "+run.Name)
	it.addRef(ref, "the task "+task.ID)
	if a := task.Attempt; a != nil {
		it.addRef(fmt.Sprintf("attempt:%s/%s/%d", run.ID, task.ID, a.N), fmt.Sprintf("attempt %d", a.N))
		if rd.s.ledger != nil {
			if rows, err := rd.s.ledger.Checks(rd.ctx, run.ID, task.ID, a.N, false); err == nil {
				for _, c := range rows {
					if c.Phase != "check" || (c.Exit == 0 && !c.TimedOut) {
						continue
					}
					it.addRef(fmt.Sprintf("check:%s/%s/%d/%s/%d", run.ID, task.ID, a.N, c.ID, c.K),
						fmt.Sprintf("check %s failed (exit %d, ran %s, log sha256 %s)", c.ID, c.Exit, cmp.Or(a.Where, "natively"), short(c.LogSHA256)))
				}
			}
		}
		if a.Result != "" {
			it.addRef("commit:"+run.ID+"/"+a.Result, "the commit "+short(a.Result))
		}
	}
	if run.Ticket != nil {
		it.addRef("ticket:"+run.Ticket.Key, run.Ticket.Key)
	}
	rd.withCard(it)
	return it, open
}

// yoloWords is the yolo choice in the words the ticket page's approval
// shows (web/app/utils/dispatchPlans.ts, yoloWords).
func yoloWords(yolo *bool) string {
	if yolo != nil && *yolo {
		return "yolo on: the agents run without their permission prompts, nothing they do asks first"
	}
	return "yolo off: the agents ask before they act, as they do on their own"
}

// planBodyNames is what an approval's scope names of a revision's body.
type planBodyNames struct {
	Crew struct {
		Name    string `json:"name"`
		Members []struct {
			AgentID string `json:"agentId"`
		} `json:"members"`
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	} `json:"crew"`
}

// planBodyRead is a revision's body as an approval's scope reads it: its
// names and its yolo choice, ok false when it could not be read.
type planBodyRead struct {
	names planBodyNames
	yolo  *bool
	ok    bool
}

// planItem is the item of the plan revision p, open as OpenPlans reads it
// (open) or as it is now.
func (rd *inboxRead) planItem(p dispatch.OpenPlan, open bool) *inboxItem {
	ref := p.Ref()
	subject := "ticket:" + p.Key
	if rd.planRefsTaken() {
		subject = ref
	}
	rev := strconv.FormatInt(p.Rev, 10)
	it := &inboxItem{Ref: ref, Subject: subject, Kind: inboxPlan, Since: p.CreatedAt, Answerer: inboxAnswerer, Revision: rev, BriefBy: "conductor", Actions: []inboxAction{}}
	it.Title = fitText(fmt.Sprintf("%s revision %d: %s", p.Key, p.N, p.Title), 200)
	route := http.MethodPost + " /api/dispatch/plans/" + p.ID + "/"
	approvable := p.State == dispatch.PlanProposed || (p.Stale != nil && p.Stale.Remedy == dispatch.RemedyApprove)
	switch {
	case p.State == dispatch.PlanProposed:
		it.Brief = fitText(fmt.Sprintf("Revision %d of %s (%s) waits for the operator's approval: crew %s at base %s", p.N, p.Key, p.Title, p.CrewName, short(p.Base)), 2<<10)
	case p.Stale != nil:
		it.Brief = fitText(fmt.Sprintf("Revision %d of %s (%s) was approved; the approval no longer holds: %s", p.N, p.Key, p.Title, p.Stale.Words), 2<<10)
		now := "The approval no longer holds: " + p.Stale.Words
		if p.Stale.Remedy == dispatch.RemedyRevise {
			now += "; a new revision is needed"
		} else {
			now += "; approve again"
		}
		it.Now = fitText(now, 500)
	default:
		it.Brief = fitText(fmt.Sprintf("Revision %d of %s (%s) is %s", p.N, p.Key, p.Title, p.State), 2<<10)
		it.Now = fitText(fmt.Sprintf("revision %d is %s", p.N, p.State), 500)
	}
	if open && approvable {
		if scope, why := rd.approveScope(p); why != "" {
			it.Now = fitText(strings.TrimPrefix(it.Now+"; ", "; ")+why, 500)
		} else {
			it.addAction(inboxAction{Name: "approve", Label: "Approve", Expect: rev, TicketRev: p.TicketRev, Scope: scope, Route: route + "approve"})
		}
	}
	if open {
		it.addAction(inboxAction{Name: "withdraw", Label: "Withdraw", Expect: rev, Confirm: true, Route: route + "withdraw",
			Scope: fmt.Sprintf("Withdraws revision %d of %s at its revision %s: it can no longer be approved or launched", p.N, p.Key, rev)})
	}
	it.block("ticket:" + p.Key)
	it.Blocking.Words = fitText(fmt.Sprintf("Nothing runs for %s until a revision is approved and launched", p.Key), 500)
	it.addRef("ticket:"+p.Key, p.Key+": "+p.Title)
	it.addRef(ref, fmt.Sprintf("revision %d", p.N))
	rd.withCard(it)
	return it
}

// planRefsTaken reports whether Brakeman's resolver takes a plan ref
// (Brakeman's PR-10): a plan item's subject is then its own ref, its
// ticket's before.
func (rd *inboxRead) planRefsTaken() bool {
	if rd.planRefs != nil {
		return rd.planRefs()
	}
	_, err := brakeman.ParseRef("plan:CND-1/1")
	return err == nil
}

// approveScope is what an Approve of p authorizes, read from its body: the
// crew, its tasks and agents, the base, the ticket's revision, the yolo
// choice and the approval it supersedes. why, set, says the body was not
// read, so no Approve is offered.
func (rd *inboxRead) approveScope(p dispatch.OpenPlan) (scope, why string) {
	s := rd.s
	notRead := fmt.Sprintf("revision %d could not be read, so its yolo choice is not known", p.N)
	b, ok := rd.bodies[p.Digest]
	if !ok || p.Digest == "" {
		b = planBodyRead{}
		if v, err := s.dispatch.Plan(rd.ctx, p.ID); err == nil {
			f, ferr := s.planFacts.get(v.Digest, func() (json.RawMessage, error) { return v.Body, nil })
			b.ok = ferr == nil && json.Unmarshal(v.Body, &b.names) == nil
			b.yolo = f.Yolo
		}
		if p.Digest != "" {
			rd.bodies[p.Digest] = b
		}
	}
	if !b.ok {
		return "", notRead
	}
	names := b.names
	var tasks, agents []string
	for _, t := range names.Crew.Tasks {
		tasks = append(tasks, t.ID)
	}
	for _, m := range names.Crew.Members {
		if !slices.Contains(agents, m.AgentID) {
			agents = append(agents, m.AgentID)
		}
	}
	list, ok := rd.plans[p.Key]
	if !ok {
		list, _ = s.dispatch.Plans(rd.ctx, p.Key)
		rd.plans[p.Key] = list
	}
	var superseded []int
	for _, o := range list {
		if o.ID != p.ID && o.State == dispatch.PlanApproved {
			superseded = append(superseded, o.N)
		}
	}
	return approvalScope(p, cmp.Or(names.Crew.Name, p.CrewName), tasks, agents, b.yolo, superseded), ""
}

// maxScope bounds an action's scope, encoded (addAction).
const maxScope = 1 << 10

// approvalScope is an Approve's scope within maxScope: what it must
// disclose whole (the revision, the base, the ticket's revision, the yolo
// choice, the approvals it supersedes, that launching is a separate step)
// kept, the crew's name, its agents and its tasks shortened first, a list
// past its room ending with how many more.
func approvalScope(p dispatch.OpenPlan, crewName string, tasks, agents []string, yolo *bool, superseded []int) string {
	head := fmt.Sprintf("Approves revision %d of %s: crew ", p.N, p.Key)
	tail := fmt.Sprintf(", at base %s, against the ticket at revision %d; %s", short(p.Base), p.TicketRev, yoloWords(yolo))
	for _, n := range superseded {
		tail += fmt.Sprintf("; sets revision %d's approval superseded", n)
	}
	tail += "; launching is a separate step on the ticket page"
	const withTasks, andAgents = " with the tasks ", " and the agents "
	room := maxScope - jsonLen(head) - jsonLen(tail) - jsonLen(withTasks) - jsonLen(andAgents)
	crewName = fitText(crewName, min(200, max(room/4, 16)))
	room -= jsonLen(crewName)
	agentList := fitList(agents, max(room/3, 0))
	taskList := fitList(tasks, max(room-jsonLen(agentList), 0))
	return fitText(head+crewName+withTasks+taskList+andAgents+agentList+tail, maxScope)
}

// fitList is items joined with ", " within room encoded bytes: whole when
// they fit, else as many as fit followed by "and N more", or "N" alone.
func fitList(items []string, room int) string {
	whole := strings.Join(items, ", ")
	if jsonLen(whole) <= room {
		return whole
	}
	for k := len(items) - 1; k > 0; k-- {
		if s := strings.Join(items[:k], ", ") + fmt.Sprintf(" and %d more", len(items)-k); jsonLen(s) <= room {
			return s
		}
	}
	return strconv.Itoa(len(items))
}

// openQuestions and openTasks are the open items of the sessions and of
// the engine's managed runs, every one.
func (rd *inboxRead) openQuestions() []*inboxItem {
	var out []*inboxItem
	rd.s.registry.Each(func(d session.Driver) {
		if it, ok := rd.questionItem(d.Info()); ok {
			out = append(out, it)
		}
	})
	return out
}

func (rd *inboxRead) openTasks() []*inboxItem {
	if rd.s.runs == nil {
		return nil
	}
	var out []*inboxItem
	for _, run := range rd.s.runs.List() {
		if run.Managed == nil {
			continue
		}
		for _, t := range run.Tasks {
			if t.State != "failed" {
				continue
			}
			if it, ok := rd.taskItem(run, t); ok {
				out = append(out, it)
			}
		}
	}
	return out
}

// inboxOpen is a page of the open items after after, oldest first: at
// most limit, whether more follow, and how many are open in all. Each
// source gives its items after the cursor (Dispatch a page and one), they
// are merged by (since, ref), and the sources' totals added.
func (s *Server) inboxOpen(ctx context.Context, after inboxCursor, limit int) ([]*inboxItem, bool, int, error) {
	rd := s.newInboxRead(ctx)
	var all []*inboxItem
	all = append(all, rd.openQuestions()...)
	all = append(all, rd.openTasks()...)
	total := len(all)
	all = slices.DeleteFunc(all, func(it *inboxItem) bool { return !after.since.IsZero() && it.before(after) <= 0 })
	if s.dispatch != nil {
		page, _, n, err := s.dispatch.OpenPlans(ctx, dispatch.PlanCursor{Since: after.since, Ref: after.ref}, limit+1)
		if err != nil {
			return nil, false, 0, err
		}
		total += n
		for _, p := range page {
			all = append(all, rd.planItem(p, true))
		}
	}
	slices.SortFunc(all, func(a, b *inboxItem) int { return a.before(inboxCursor{b.Since, b.Ref}) })
	if len(all) > limit {
		return all[:limit], true, total, nil
	}
	return all, false, total, nil
}

// inboxQuery reads the parameters of an inbox route, each at most once:
// 400 invalid_request for one the route does not take or one given twice,
// as Dispatch's reads refuse them. When it reports false it has answered.
func inboxQuery(w http.ResponseWriter, r *http.Request, names ...string) (map[string]string, bool) {
	q, err := readQuery(r, names...)
	out := map[string]string{}
	for _, name := range names {
		if err != nil {
			break
		}
		out[name], err = q.one(name)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	return out, true
}

// inboxOnly wraps an inbox route: the admin's alone, and on a switchyard
// 403 switchyard (decisions are answered where the work runs).
func (s *Server) inboxOnly(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Switchyard.Enabled {
			writeError(w, http.StatusForbidden, "switchyard", "decisions are answered where the work runs, or through a decision link")
			return
		}
		next(w, r)
	})
}

// handleInbox answers a page of the open items: {items, next?, total,
// at}, the summaries oldest first by (since, ref), at most inboxPage,
// next the cursor after the last when more follow.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	q, ok := inboxQuery(w, r, "after")
	if !ok {
		return
	}
	after, err := parseCursor(q["after"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items, more, total, err := s.inboxOpen(r.Context(), after, inboxPage)
	if err != nil {
		s.log.Warn("inbox: the plan revisions could not be read", "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the inbox could not be read")
		return
	}
	out := struct {
		Items []inboxSummary `json:"items"`
		Next  string         `json:"next,omitempty"`
		Total int            `json:"total"`
		At    time.Time      `json:"at"`
	}{Items: []inboxSummary{}, Total: total, At: time.Now().UTC()}
	for _, it := range items {
		out.Items = append(out.Items, it.summary())
	}
	if more && len(items) > 0 {
		out.Next = cursorOf(items[len(items)-1])
	}
	writeJSON(w, http.StatusOK, out)
}

// handleInboxItem answers one item with its detail: {item}; for a ref of
// something no longer open, the item as it is now with its delivery; 404
// not_found for a ref that names nothing the server holds.
func (s *Server) handleInboxItem(w http.ResponseWriter, r *http.Request) {
	q, ok := inboxQuery(w, r, "ref")
	if !ok {
		return
	}
	ref, err := brakeman.ParseRef(q["ref"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "ref is a question:, task: or plan: ref")
		return
	}
	it, err := s.inboxItemOf(r.Context(), ref)
	switch {
	case errors.Is(err, errNoItem):
		writeError(w, http.StatusNotFound, "not_found", "no such item")
		return
	case err != nil:
		s.log.Warn("inbox: an item could not be read", "ref", ref.String(), "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the item could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

// runOrRecord is the run as the engine holds it, else as its record keeps
// it (a run stopped and let go: nothing changes it until it is
// continued); false for one found in neither.
func (s *Server) runOrRecord(id string) (crew.Run, bool) {
	if s.runs != nil {
		if r, ok := s.runs.Get(id); ok {
			return r, true
		}
	}
	if s.records == nil {
		return crew.Run{}, false
	}
	r, ok, err := s.records.Get(id)
	if err != nil {
		s.log.Warn("inbox: a run's record could not be read", "run", id, "err", err)
	}
	return r, ok && err == nil
}

// errNoItem says a ref names nothing the server holds.
var errNoItem = errors.New("no such item")

// inboxItemOf is the item ref names, open or as it is now.
func (s *Server) inboxItemOf(ctx context.Context, ref brakeman.Ref) (*inboxItem, error) {
	rd := s.newInboxRead(ctx)
	switch ref.Kind {
	case brakeman.RefQuestion:
		d, ok := s.registry.Get(ref.Session)
		if !ok {
			return nil, errNoItem
		}
		info := d.Info()
		if it, ok := rd.questionItem(info); ok && info.Attention.ID == ref.Question {
			return it, nil
		}
		steps, known := s.questionSteps(d, ref.Question, "")
		if !known {
			return nil, errNoItem
		}
		r := questionRef(ref.Session, ref.Question)
		it := &inboxItem{Ref: r, Subject: r, Kind: inboxQuestion, Answerer: inboxAnswerer, Revision: ref.Question, BriefBy: "conductor", Actions: []inboxAction{}, Recommended: -1, Delivery: steps}
		it.Title = fitText(s.agentName(info)+" in "+info.Name+" asked", 200)
		// Kept to show again (set aside, held over), it is not open now
		// and not gone; else it is gone, and its recorded step says how.
		gone := "no longer open: "
		if steps[1].State == stepWaiting {
			gone = "not open now: "
		}
		it.Now = fitText(gone+steps[1].Words, 500)
		it.Brief = it.Now
		it.addRef("session:"+info.ID, "the session "+info.Name+"'s terminal")
		return it, nil
	case brakeman.RefTask:
		if s.runs == nil {
			return nil, errNoItem
		}
		run, ok := s.runOrRecord(ref.Run)
		if !ok || run.Managed == nil {
			return nil, errNoItem
		}
		for _, t := range run.Tasks {
			if t.ID == ref.Task {
				it, _ := rd.taskItem(run, t)
				return it, nil
			}
		}
		return nil, errNoItem
	case brakeman.RefPlan:
		if s.dispatch == nil {
			return nil, errNoItem
		}
		list, err := s.dispatch.Plans(ctx, ref.Ticket)
		if errors.Is(err, dispatch.ErrNotFound) {
			return nil, errNoItem
		}
		if err != nil {
			return nil, err
		}
		tk, err := s.dispatch.Ticket(ctx, ref.Ticket)
		if err != nil {
			return nil, errNoItem
		}
		for _, p := range list {
			if p.N == ref.N {
				op := dispatch.OpenPlan{PlanSummary: p, Key: tk.Key, Title: tk.Title, TicketRev: tk.Rev}
				open := tk.DeletedAt.IsZero() && (p.State == dispatch.PlanProposed || (p.State == dispatch.PlanApproved && p.Stale != nil && !dispatch.TicketClosed(tk.Status)))
				return rd.planItem(op, open), nil
			}
		}
	}
	return nil, errNoItem
}

// hostedResumes keeps, for each hosted session, how the server saw its
// questions leave as its host synced its attention, for its last 64
// questions, in memory. A question leaves when the host's session holds
// it no longer: neither shown nor kept to show again (Info.Kept, the
// agent's own set aside, the one a held turn holds over). Its resumption
// is the first change from then that says how the work went on (the
// agent working, a next question, the end), set once: a question shown
// again with its id is held again, and leaves anew. A question whose
// leaving the server did not see has none.
type hostedResumes struct {
	mu       sync.Mutex
	sessions map[string]*hostedSeen
	// seam, when a test sets it, is called with the session's id before a
	// change is noted and once it is (noted), outside the lock: the
	// moments a page may read the track at, beside the change's event.
	seam atomic.Pointer[func(sess string, noted bool)]
}

// hostedSeen is what hostedResumes keeps of one session.
type hostedSeen struct {
	held    []string // the questions shown or kept at the last change
	pending []string // the questions that left, their resumption not seen yet
	seen    map[string]bool
	kept    map[string]session.Resumption
	order   []string
}

// change notes the hosted session's attention as its host synced it.
func (h *hostedResumes) change(info session.Info) {
	f := h.seam.Load()
	if f != nil {
		(*f)(info.ID, false)
	}
	h.note(info)
	if f != nil {
		(*f)(info.ID, true)
	}
}

// note is change under the lock.
func (h *hostedResumes) note(info session.Info) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions == nil {
		h.sessions = map[string]*hostedSeen{}
	}
	st := h.sessions[info.ID]
	if st == nil {
		st = &hostedSeen{seen: map[string]bool{}, kept: map[string]session.Resumption{}}
		h.sessions[info.ID] = st
	}
	q := question(info)
	held := slices.DeleteFunc(append([]string{q}, info.Kept...), func(id string) bool { return id == "" })
	for _, id := range st.held {
		if !slices.Contains(held, id) && st.seen[id] && !slices.Contains(st.pending, id) {
			st.pending = append(st.pending, id)
		}
	}
	for _, id := range held {
		// Held again (shown again with its id): it has not left, and what
		// was kept of an earlier leaving is not its resumption.
		st.pending = slices.DeleteFunc(st.pending, func(x string) bool { return x == id })
		delete(st.kept, id)
	}
	st.held = held
	if q != "" && !st.seen[q] {
		st.seen[q] = true
		st.order = append(st.order, q)
		if len(st.order) > 64 {
			old := st.order[0]
			st.order = st.order[1:]
			delete(st.seen, old)
			delete(st.kept, old)
			st.pending = slices.DeleteFunc(st.pending, func(x string) bool { return x == old })
		}
	}
	if len(st.pending) == 0 {
		return
	}
	at := time.Now().UTC()
	how := ""
	switch a := info.Attention; {
	case info.Status.Ended():
		how = session.ResumedEnded
	case a.State == session.AttentionWorking:
		how = session.ResumedWorking
		if a.Since != nil {
			at = *a.Since
		}
	case q != "":
		// A next question: q is held, so it is none of those pending.
		how = session.ResumedAsked
		if a.Since != nil {
			at = *a.Since
		}
	default:
		return
	}
	for _, id := range st.pending {
		if _, set := st.kept[id]; !set {
			st.kept[id] = session.Resumption{At: at, How: how}
		}
	}
	st.pending = st.pending[:0]
}

// gone forgets the session id.
func (h *hostedResumes) gone(id string) {
	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()
}

// of is what the server saw of the hosted session's question id: its
// resumption; whether it saw the question at all; and whether the work
// is yet to go on as far as the server knows: it saw the question leave
// and waits for the change that says how, or holds it still from the
// last change it noted (a change the host made, not noted yet).
func (h *hostedResumes) of(sess, id string) (res *session.Resumption, seen, pending bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.sessions[sess]
	if st == nil {
		return nil, false, false
	}
	if r, ok := st.kept[id]; ok {
		return &r, true, false
	}
	return nil, st.seen[id], slices.Contains(st.pending, id) || slices.Contains(st.held, id)
}
