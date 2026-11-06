package api

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
)

// The history's sources (docs/plans/history.md, "The sources", PR-3):
// each a function from what its store holds to the index's entries and
// links, every field taken from the record itself. A session record is
// one source; a run (its record and, for a managed run, the ledger's rows,
// read without any log) is another; each finished check receipt is a
// source of its own, read once. Every ref and link end is written in the
// grammar of docs/plans/brakeman.md (Evidence references); an entry's key
// is its ref when it has one of its own, else its anchor's ref and its
// place in it. A link is written by the source of its later end.

// The relations of the links (history.Link.Rel).
const (
	relSessionOf   = "session_of"
	relContinues   = "continues"
	relLaunched    = "launched_from"
	relHanded      = "handed"
	relPassedAfter = "passed_after"
	relReviews     = "reviews"
	relReopened    = "reopened"
	relResumedFrom = "resumed_from"
)

// The refs, as Brakeman's grammar spells them.
func hRunRef(run string) string        { return "run:" + run }
func hTaskRef(run, task string) string { return "task:" + run + "/" + task }
func hSessionRef(id string) string     { return "session:" + id }
func hCheckRef(run, task string, n int, id string, k int) string {
	return fmt.Sprintf("check:%s/%s/%d/%s/%d", run, task, n, id, k)
}

// attemptKey is an attempt's ref, attempt:<run>/<task>/<n>.
func attemptKey(run, task string, n int) string {
	return fmt.Sprintf("attempt:%s/%s/%d", run, task, n)
}

// hPlanRef is a plan revision's ref, as Brakeman's grammar spells it
// (brakeman.RefPlan: plan:<KEY-n>/<revision>, the revision's number alone,
// plan:CND-1/2), so ParseRef reads it and a brief's citation joins it.
func hPlanRef(ticket string, n int) string { return fmt.Sprintf("plan:%s/%d", ticket, n) }

// field is a metadata field as the index bounds it (history.MaxField); an
// identity that does not fit is no identity, so repo and tree are left
// empty rather than cut (fitID).
func hField(s string) string { return history.Clip(s, history.MaxField) }

func hFitID(s string) string {
	if len(s) > history.MaxField {
		return ""
	}
	return s
}

func hTitle(s string) string   { return history.Clip(hFirstLine(s), history.MaxTitle) }
func hSummary(s string) string { return history.Clip(s, history.MaxSummary) }
func hShortBody(s string) string {
	return history.Clip(s, history.MaxShortBody)
}

func hFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// short is a commit's short id.
func hShort(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// at is a time as an entry holds it: UTC, and the fallback when t is zero.
func hAt(t, fallback time.Time) time.Time {
	if t.IsZero() {
		t = fallback
	}
	return t.UTC()
}

// sessionPlace is what a session record's entries take from elsewhere:
// the project its repository is in, and, for a crew member's attempt, the
// commit it started from (the ledger's).
type sessionPlace struct {
	project, commit string
}

// sessionEntries is a session record's entries: its record entry, one said
// entry per kept entry (a status line is not one: the record entry says
// how it ended) and one per file row, each anchored to the session, and
// its resumed_from link.
func sessionEntries(rec journal.Record, p sessionPlace) ([]history.Entry, []history.Link) {
	ref := hSessionRef(rec.ID)
	base := history.Entry{
		Anchor: ref, Host: hField(rec.Host.Name), HostUser: hField(rec.Host.User), Hosted: rec.Host.Hosted, Where: hField(rec.Where),
		Project: p.project, Agent: hField(rec.Agent.ID), AgentVersion: hField(rec.Agent.Version),
	}
	if !rec.Host.Hosted {
		base.Repo, base.Tree = hFitID(rec.RepoCommon), hFitID(rec.Repo)
	}
	if rec.Agent.Session != nil {
		base.AgentSession = hField(rec.Agent.Session.ID)
	}
	if rec.Crew != nil {
		base.Crew, base.Run = hField(rec.Crew.CrewID), hField(rec.Crew.RunID)
	}
	commit := rec.Start
	if rec.StartFrom == journal.StartFromAttempt {
		commit = p.commit
	}
	agent := cmp.Or(rec.Agent.Name, rec.Agent.ID)
	ending := rec.Ending
	if ending == "" && rec.EndedAt.IsZero() && rec.LostAt.IsZero() {
		ending = "running"
	}
	e := base
	e.Key, e.Ref, e.Kind, e.At = ref, ref, "session", hAt(rec.StartedAt, time.Unix(0, 0))
	e.Revision, e.Commit, e.Outcome = hField(rec.Branch), commit, hField(ending)
	e.Title = hTitle(cmp.Or(rec.Name, agent, rec.ID))
	e.Summary = hSummary(strings.Join(hNonEmpty(agent, rec.Cwd, ending), " · "))
	out := []history.Entry{e}
	questions := map[string]string{}
	for _, je := range rec.Entries {
		if je.Kind == journal.KindQuestion && je.ID != "" {
			questions[je.ID] = je.Message
		}
	}
	for _, je := range rec.Entries {
		x := base
		x.Key, x.Ref, x.Seq, x.At, x.Actor = fmt.Sprintf("%s#e%d", ref, je.Seq), ref, je.Seq, hAt(je.At, rec.StartedAt), hField(je.ByName)
		switch je.Kind {
		case journal.KindTyped:
			x.Kind, x.Title, x.Body = "session.typed", hTitle(je.Message), hShortBody(je.Message)
		case journal.KindQuestion:
			x.Kind, x.Title, x.Body = "session.question", hTitle(je.Message), hShortBody(je.Message)
			if je.ID != "" {
				x.Ref = "question:" + rec.ID + "/" + je.ID
			}
			x.Summary = hSummary(strings.Join(je.Choices, " · "))
			x.Actor = hField(cmp.Or(je.ByName, agent))
		case journal.KindAnswer:
			x.Kind, x.Outcome = "session.answer", hField(je.Outcome)
			if je.Ref != "" {
				x.Ref = "question:" + rec.ID + "/" + je.Ref
			}
			x.Title = hTitle(answerWords(je))
			x.Summary = hSummary(questions[je.Ref])
			x.Body = hShortBody(je.Message)
		case journal.KindChat:
			x.Kind, x.Title, x.Body = "session.chat", hTitle(je.Message), hShortBody(je.Message)
			if je.Event != "" {
				x.Summary = hSummary(je.Event)
			}
		case journal.KindReport:
			x.Kind, x.Title = "session.report", hTitle(cmp.Or(je.Message, je.URL, je.To, je.Tool))
			x.Summary = hSummary(strings.Join(hNonEmpty(je.Event, je.URL, je.To, je.Tool), " · "))
			x.Body = hShortBody(je.Message)
			x.Actor = hField(cmp.Or(je.ByName, agent))
		default:
			continue // a status line: the record entry says how it ended
		}
		if x.Title == "" {
			x.Title = je.Kind
		}
		out = append(out, x)
	}
	for _, f := range rec.Files {
		x := base
		sum := sha256.Sum256([]byte(f.Path))
		x.Key, x.Ref, x.Kind, x.At = fmt.Sprintf("%s#f%s/%s", ref, f.Op, hex.EncodeToString(sum[:8])), ref, "session.file", hAt(f.Last, rec.StartedAt)
		x.Title = hTitle(f.Path)
		x.Summary = hSummary(fmt.Sprintf("%s %d× · %s", f.Op, f.Count, f.Path))
		x.Actor = hField(agent)
		out = append(out, x)
	}
	var links []history.Link
	if rec.ResumedFrom != "" {
		links = append(links, history.Link{From: ref, Rel: relResumedFrom, To: hSessionRef(rec.ResumedFrom)})
	}
	return out, links
}

// answerWords is an answer's title: the choice picked, or how the question
// ended when no words of it are kept.
func answerWords(je journal.Entry) string {
	switch {
	case je.Message != "":
		return "answered: " + je.Message
	case je.Outcome == journal.OutcomeAnsweredInTerminal:
		return "answered in the terminal"
	case je.Outcome == journal.OutcomeWithdrawn:
		return "withdrawn: the agent moved on"
	}
	return "answered"
}

func hNonEmpty(ss ...string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// runRows is what the ledger holds of one managed run, read without any
// log (Checks(..., false)).
type runRows struct {
	row      ledger.RunRow
	tasks    []ledger.TaskRow
	attempts []ledger.AttemptRow
	commands []ledger.CommandRow
	messages []ledger.MessageRow
	checks   []ledger.CheckRow
}

// runPlace is what a run's entries take from beyond its rows: its crew,
// its project and repository, the machine it ran on, each task's kind and
// each member's agent.
type runPlace struct {
	run, crew, project, repo, tree, host, ticket string
	kinds, agents                                map[string]string
	// needs are each task's dependencies as its crew declares them: a
	// review's subject is its first (the engine's reopen).
	needs map[string][]string
	// agentSessions are the members' agents' own sessions, as the run's
	// record keeps them, by the attempt each is the member's latest of
	// ("<task>/<n>").
	agentSessions map[string]string
	fallback      time.Time
}

// base is an entry of the run, its fields of the six filled.
func (p runPlace) base() history.Entry {
	return history.Entry{Host: p.host, Project: p.project, Repo: p.repo, Tree: p.tree, Crew: hField(p.crew), Run: p.run, Ticket: hField(p.ticket)}
}

// historyOutcome is a finished receipt's outcome by the engine's own
// predicate (crew.FailedOutcome), never a display's; an interrupted one is
// interrupted, never passed or failed.
func historyOutcome(r ledger.CheckRow) string {
	switch {
	case r.State == ledger.CheckInterrupted:
		return ledger.CheckInterrupted
	case crew.FailedOutcome(r):
		return "failed"
	}
	return "passed"
}

// isReceipt says a row is a check's or a verify step's receipt.
func hIsReceipt(r ledger.CheckRow) bool {
	return r.Phase == "" || r.Phase == ledger.PhaseCheck || r.Phase == ledger.PhaseVerify
}

// receiptFinished says a receipt is one the index holds as its own
// source: finished or interrupted, which never changes once recorded.
func receiptFinished(r ledger.CheckRow) bool {
	return hIsReceipt(r) && (r.State == ledger.CheckFinished || r.State == ledger.CheckInterrupted)
}

// argvKey is a receipt's command, the SHA-256 of its argv: the same
// command elsewhere.
func hArgvKey(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	b, _ := json.Marshal(argv)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkEntry is a finished receipt as its own source's one entry: its
// summary the argv, its body the log as bounded (a failed one's ends, a
// passed one's ledger tail), its commit what it checked.
func checkEntry(p runPlace, r ledger.CheckRow, branch string) history.Entry {
	e := p.base()
	e.Key = hCheckRef(p.run, r.TaskID, r.N, r.ID, r.K)
	e.Ref, e.Anchor = e.Key, hTaskRef(p.run, r.TaskID)
	e.Kind = "check"
	if r.Phase == ledger.PhaseVerify {
		e.Kind = "verify"
	}
	e.At = hAt(r.FinishedAt, hAt(r.StartedAt, p.fallback))
	e.Where, e.Commit, e.Revision, e.Agent = hField(r.Where), r.Subject, hField(branch), hField(p.agents[r.TaskID])
	e.Outcome, e.Subject = historyOutcome(r), hArgvKey(r.Argv)
	e.Title = hTitle(fmt.Sprintf("%s on %s #%d", r.ID, r.TaskID, r.N))
	e.Summary = history.CheckSummary(r.Argv)
	switch {
	case e.Outcome == "passed" || r.LogDropped || r.Log == nil:
		e.Body = history.Clip(r.Tail, history.MaxCheckBody)
	default:
		e.Body = history.CheckLog(string(r.Log))
	}
	return e
}

// runSource is a run's own source: its record entry, its tasks, its
// attempts with what they submitted, its commands of the admin's actor,
// its messages, its verified candidates and failed steps, and every link
// whose later end is in it. chain is the runs it continues, nearest first,
// each with its rows (none for a legacy run). rec is its record, nil for a
// run the ledger alone holds. kept are the handed links into attempts a
// later withdrawal took back, as the index held them (keptHanded).
func runSource(p runPlace, rec *crew.Run, rows *runRows, chain []runRows, kept []history.Link) ([]history.Entry, []history.Link) {
	ref := hRunRef(p.run)
	var out []history.Entry
	var links []history.Link
	e := p.base()
	e.Key, e.Ref, e.Anchor, e.Kind, e.At = ref, ref, ref, "run", hAt(p.fallback, time.Unix(0, 0))
	var label, goal, outcome, base, from string
	if rec != nil {
		label, goal, outcome, from = cmp.Or(rec.Label, rec.Name, rec.CrewID), rec.Goal, cmp.Or(rec.Outcome, rec.State), rec.ResumedFrom
	}
	if rows != nil {
		label = cmp.Or(label, rows.row.CrewID)
		outcome, base, from = cmp.Or(rows.row.Outcome, outcome, rows.row.State), rows.row.Base, cmp.Or(rows.row.ContinuedFrom, from)
	}
	e.Title, e.Outcome, e.Commit = hTitle(cmp.Or(label, p.run)), hField(outcome), base
	e.Summary = hSummary(strings.Join(hNonEmpty(p.crew, goal, outcome), " · "))
	out = append(out, e)
	if from != "" {
		links = append(links, history.Link{From: ref, Rel: relContinues, To: hRunRef(from)})
	}
	if rec != nil && rec.Ticket != nil && rec.Ticket.Key != "" {
		links = append(links, history.Link{From: ref, Rel: relLaunched, To: "ticket:" + rec.Ticket.Key})
		if rec.Ticket.PlanN > 0 {
			links = append(links, history.Link{From: ref, Rel: relLaunched, To: hPlanRef(rec.Ticket.Key, rec.Ticket.PlanN)})
		}
		if rec.Ticket.Launch != "" {
			links = append(links, history.Link{From: ref, Rel: relLaunched, To: hLaunchRef(rec.Ticket.Launch)})
		}
	}
	if rows == nil {
		// A legacy run: its members' sessions.
		if rec != nil {
			for _, m := range rec.Members {
				if m.SessionID != "" {
					links = append(links, history.Link{From: ref, Rel: relSessionOf, To: hSessionRef(m.SessionID)})
				}
			}
		}
		return out, dedupLinks(links)
	}
	// Tasks.
	for _, t := range rows.tasks {
		x := p.base()
		x.Key, x.Kind, x.Outcome = hTaskRef(p.run, t.ID), "task", hField(t.State)
		x.Ref, x.Anchor, x.At = x.Key, x.Key, e.At
		x.Agent = hField(p.agents[t.ID])
		x.Title = hTitle(t.ID + " · " + cmp.Or(label, p.run))
		x.Summary = hSummary(strings.Join(hNonEmpty(t.Kind, t.State, t.Reason), " · "))
		x.Commit = t.ResultCommit
		out = append(out, x)
	}
	byTask := map[string][]ledger.AttemptRow{}
	for _, a := range rows.attempts {
		byTask[a.TaskID] = append(byTask[a.TaskID], a)
	}
	for _, as := range byTask {
		slices.SortFunc(as, func(a, b ledger.AttemptRow) int { return a.N - b.N })
	}
	taskRow := func(id string) ledger.TaskRow {
		if i := slices.IndexFunc(rows.tasks, func(t ledger.TaskRow) bool { return t.ID == id }); i >= 0 {
			return rows.tasks[i]
		}
		return ledger.TaskRow{}
	}
	branchOf := map[string]string{}
	for _, a := range rows.attempts {
		branchOf[fmt.Sprintf("%s/%d", a.TaskID, a.N)] = a.Branch
		out = append(out, attemptEntries(p, a, p.kinds[a.TaskID])...)
		if a.SessionID != "" {
			links = append(links, history.Link{From: attemptKey(p.run, a.TaskID, a.N), Rel: relSessionOf, To: hSessionRef(a.SessionID)})
		}
		kind := p.kinds[a.TaskID]
		if kind == crew.TaskReview {
			continue
		}
		// A work attempt's result, Checked only by the delivery's own
		// predicate and with no receipt the engine fails.
		cand := candidateOf(kind, taskRow(a.TaskID), a, rows.checks, nil)
		if cand.Commit == "" || !verifiedOf(a, cand.Receipts) || slices.ContainsFunc(rows.checks, func(r ledger.CheckRow) bool {
			return r.TaskID == a.TaskID && r.N == a.N && r.Subject == cand.Commit && hIsReceipt(r) && crew.FailedOutcome(r)
		}) {
			continue
		}
		v := p.base()
		v.Key = fmt.Sprintf("candidate:%s/%s/%d", p.run, a.TaskID, a.N)
		v.Ref, v.Anchor, v.Kind, v.Outcome = v.Key, hTaskRef(p.run, a.TaskID), "candidate.verified", "passed"
		v.At, v.Where, v.Commit, v.Revision, v.Agent = hAt(a.ClosedAt, p.fallback), hField(a.Where), cand.Commit, hField(a.Branch), hField(p.agents[a.TaskID])
		v.Title = hTitle(fmt.Sprintf("%s #%d verified at %s", a.TaskID, a.N, hShort(cand.Commit)))
		v.Summary = hSummary(fmt.Sprintf("%d receipts passed", len(cand.Receipts)))
		out = append(out, v)
	}
	out = append(out, blockerEntries(p, rec)...)
	// Failed setup and service steps: records of the run.
	for _, r := range rows.checks {
		if hIsReceipt(r) || r.State != ledger.CheckFinished || !crew.FailedOutcome(r) || (r.Phase != ledger.PhaseSetup && r.Phase != ledger.PhaseService && r.Phase != ledger.PhaseReady) {
			continue
		}
		x := p.base()
		x.Key = hCheckRef(p.run, r.TaskID, r.N, r.ID, r.K)
		x.Ref, x.Anchor, x.Kind, x.Outcome = x.Key, hTaskRef(p.run, r.TaskID), "step", "failed"
		x.At, x.Where, x.Commit, x.Revision = hAt(r.FinishedAt, p.fallback), hField(r.Where), r.Subject, hField(branchOf[fmt.Sprintf("%s/%d", r.TaskID, r.N)])
		x.Title = hTitle(fmt.Sprintf("%s step %s on %s #%d", r.Phase, r.ID, r.TaskID, r.N))
		x.Summary = history.CheckSummary(r.Argv)
		// Its words of failure alone: a step's output (a service's or a
		// setup's log) is terminal output the history does not index.
		x.Body = history.Clip(r.Error, history.MaxCheckBody)
		out = append(out, x)
	}
	// Commands: the admin's receipts alone, keyed by actor and id.
	for _, c := range rows.commands {
		if c.Actor != adminActor {
			continue
		}
		x := p.base()
		x.Key = fmt.Sprintf("%s#command/%s/%s", ref, c.Actor, c.ID)
		x.Ref, x.Anchor, x.Kind, x.At, x.Actor, x.Outcome = "command:"+p.run+"/"+c.ID, ref, "command", hAt(c.CreatedAt, p.fallback), hField(c.Actor), hField(c.Status)
		x.Title = hTitle(c.Intent + " by " + c.Actor)
		x.Summary = hSummary(c.Intent + " · " + c.Status)
		out = append(out, x)
	}
	for _, m := range rows.messages {
		x := p.base()
		x.Key = fmt.Sprintf("message:%s/%d", p.run, m.Seq)
		x.Ref, x.Anchor, x.Kind, x.At, x.Actor = x.Key, ref, "message", hAt(m.CreatedAt, p.fallback), hField(m.From)
		x.Title = hTitle(fmt.Sprintf("%s to %s", m.From, m.To))
		x.Body = hShortBody(m.Text)
		out = append(out, x)
	}
	links = append(links, handedLinks(p.run, rows, chain)...)
	links = append(links, kept...)
	links = append(links, passedAfterLinks(p.run, rows, chain)...)
	links = append(links, reviewLinks(p, rows, chain)...)
	return out, dedupLinks(links)
}

// attemptEntries is an attempt's record entry and what it submitted: a
// work attempt's result, a review's verdict and findings, its checkpoint.
func attemptEntries(p runPlace, a ledger.AttemptRow, kind string) []history.Entry {
	ref := attemptKey(p.run, a.TaskID, a.N)
	b := p.base()
	b.Anchor, b.Where, b.Revision, b.Agent = hTaskRef(p.run, a.TaskID), hField(a.Where), hField(a.Branch), hField(p.agents[a.TaskID])
	when := hAt(cmp.Or(a.LaunchedAt, a.ReservedAt), p.fallback)
	x := b
	x.Key, x.Ref, x.Kind, x.At, x.Outcome = ref, ref, "attempt", when, hField(a.State)
	x.AgentSession = hField(p.agentSessions[a.TaskID+"/"+strconv.Itoa(a.N)])
	x.Commit = cmp.Or(a.ResultCommit, a.StartCommit)
	x.Title = hTitle(fmt.Sprintf("%s #%d", a.TaskID, a.N))
	x.Summary = hSummary(strings.Join(hNonEmpty(a.State, a.Stage, hStartToResult(a), hSessionWords(a.SessionID), a.Worktree), " · "))
	x.Body = hShortBody(a.Error)
	out := []history.Entry{x}
	var res crew.Result
	if a.Result != "" && json.Unmarshal([]byte(a.Result), &res) == nil {
		said := b
		said.Ref, said.At, said.Actor = ref, hAt(a.ClosedAt, when), hField(a.Member)
		if kind == crew.TaskReview {
			v := said
			v.Key, v.Kind, v.Outcome, v.Commit = ref+"#verdict", "review.verdict", hField(res.Verdict), a.StartCommit
			v.Title = hTitle(fmt.Sprintf("review %s #%d: %s", a.TaskID, a.N, res.Verdict))
			v.Summary = hSummary(fmt.Sprintf("%d findings", len(res.Findings)))
			v.Body = hShortBody(res.Summary)
			out = append(out, v)
			for i, f := range res.Findings {
				fe := said
				fe.Key, fe.Kind, fe.Outcome, fe.Commit = fmt.Sprintf("%s#finding/%d", ref, i+1), "review.finding", hField(f.Severity), a.StartCommit
				fe.Title = hTitle(f.Severity + ": " + f.Message)
				if f.Path != "" {
					fe.Summary = hSummary(f.Path + ":" + strconv.Itoa(f.Line))
				}
				fe.Body = hShortBody(f.Message)
				out = append(out, fe)
			}
		} else {
			r := said
			r.Key, r.Kind, r.Commit = ref+"#result", "attempt.result", cmp.Or(a.ResultCommit, res.Commit)
			r.Title = hTitle(fmt.Sprintf("%s #%d's result", a.TaskID, a.N))
			r.Summary = hSummary(res.Summary)
			r.Body = history.Clip(strings.Join(append([]string{res.Summary}, res.Issues...), "\n"), history.MaxLongBody)
			out = append(out, r)
		}
	}
	var cp crew.Checkpoint
	if a.Checkpoint != "" && json.Unmarshal([]byte(a.Checkpoint), &cp) == nil {
		c := b
		c.Key, c.Ref, c.Kind, c.At, c.Actor, c.Commit = ref+"#checkpoint", ref, "attempt.checkpoint", hAt(cp.At, when), hField(a.Member), cp.Commit
		c.Title = hTitle(fmt.Sprintf("%s #%d's checkpoint", a.TaskID, a.N))
		c.Summary = hSummary(cp.Done)
		c.Body = hShortBody(strings.Join(hNonEmpty("Done: "+cp.Done, "Remaining: "+cp.Remaining, cp.Notes), "\n"))
		out = append(out, c)
	}
	return out
}

func hStartToResult(a ledger.AttemptRow) string {
	switch {
	case a.ResultCommit != "":
		return hShort(a.StartCommit) + " → " + hShort(a.ResultCommit)
	case a.StartCommit != "":
		return "from " + hShort(a.StartCommit)
	}
	return ""
}

func hSessionWords(id string) string {
	if id == "" {
		return ""
	}
	return "session " + id
}

// blockerEntry is what a run's record says stops a task's attempt in
// flight (its Blocker), as that attempt's.
func blockerEntries(p runPlace, rec *crew.Run) []history.Entry {
	if rec == nil {
		return nil
	}
	var out []history.Entry
	for _, t := range rec.Tasks {
		if t.Blocker == nil || t.Attempt == nil {
			continue
		}
		x := p.base()
		ref := attemptKey(p.run, t.ID, t.Attempt.N)
		x.Key, x.Ref, x.Anchor, x.Kind = ref+"#blocker", ref, hTaskRef(p.run, t.ID), "attempt.blocker"
		x.At, x.Actor, x.Agent = hAt(t.Blocker.At, p.fallback), hField(t.Blocker.By), hField(p.agents[t.ID])
		x.Title = hTitle(t.ID + " blocked: " + t.Blocker.Reason)
		x.Body = hShortBody(t.Blocker.Message)
		out = append(out, x)
	}
	return out
}

// chainAttempt is an attempt with the run whose rows hold it.
type chainAttempt struct {
	run string
	a   ledger.AttemptRow
}

// withdrawnOf is each task's Withdrawn in rows.
func withdrawnOf(rows *runRows) map[string]ledger.TaskRow {
	out := map[string]ledger.TaskRow{}
	for _, t := range rows.tasks {
		out[t.ID] = t
	}
	return out
}

// handedLinks are the handed links whose attempt is the run's, as the
// engine hands a receipt (previousOf, readHistory): attempt b is handed
// the receipt that failed (crew.FailingReceipt) the last attempt of its
// task before it that is counted (crew.UncountedAttempt) and kept (above
// the task's withdrawal), in this run or, when this run holds none, back
// along the runs it continues, walking past a run whose attempts at the
// task are all uncounted (the engine carries the receipt through them),
// with the attempts each ancestor holds bounded as the engine bounds them
// (above the continued run's withdrawal, within its count). An attempt a
// later withdrawal took back (its number at or below its task's Withdrawn
// now) is left out: the withdrawal before its launch is no longer in the
// rows, so its handed link is the one the index wrote while it was
// (keptHanded), never one rebuilt from today's boundary.
func handedLinks(run string, rows *runRows, chain []runRows) []history.Link {
	cur := withdrawnOf(rows)
	var from map[string]ledger.TaskRow // the run continued: its rows bound every ancestor's
	if len(chain) > 0 {
		from = withdrawnOf(&chain[0])
	}
	var links []history.Link
	for _, b := range rows.attempts {
		w := cur[b.TaskID].Withdrawn
		if b.N <= w {
			continue
		}
		pick := func(as []ledger.AttemptRow, usable func(ledger.AttemptRow) bool) (ledger.AttemptRow, bool) {
			var out ledger.AttemptRow
			found := false
			for _, a := range as {
				if a.TaskID == b.TaskID && a.N < b.N && a.N > w && usable(a) && !crew.UncountedAttempt(a) && (!found || a.N > out.N) {
					out, found = a, true
				}
			}
			return out, found
		}
		src, checks := run, rows.checks
		p, ok := pick(rows.attempts, func(ledger.AttemptRow) bool { return true })
		for i := 0; !ok && i < len(chain); i++ {
			row := from[b.TaskID]
			if row.Attempts <= row.Withdrawn {
				break // the continued run kept no attempt of the task
			}
			p, ok = pick(chain[i].attempts, func(a ledger.AttemptRow) bool { return a.N > row.Withdrawn && a.N <= row.Attempts })
			src, checks = chain[i].row.ID, chain[i].checks
		}
		if !ok {
			continue
		}
		fr, ok := crew.FailingReceipt(p, checks)
		if !ok {
			continue
		}
		links = append(links, history.Link{From: hCheckRef(src, fr.TaskID, fr.N, fr.ID, fr.K), Rel: relHanded, To: attemptKey(run, b.TaskID, b.N)})
	}
	return links
}

// passedAfterLinks are the passed_after links whose passing receipt is the
// run's: from each failed receipt of the same check on the same task, in
// this run or along the runs it continues, that it is the first pass
// after.
func passedAfterLinks(run string, rows *runRows, chain []runRows) []history.Link {
	type receipt struct {
		run    string
		r      ledger.CheckRow
		passed bool
	}
	seq := map[string][]receipt{} // by task and check id, oldest first
	add := func(runID string, checks []ledger.CheckRow) {
		sorted := slices.Clone(checks)
		slices.SortFunc(sorted, func(a, b ledger.CheckRow) int { return cmp.Or(a.N-b.N, a.K-b.K) })
		for _, r := range sorted {
			if !hIsReceipt(r) || r.State != ledger.CheckFinished {
				continue
			}
			k := r.TaskID + "\x00" + r.ID
			seq[k] = append(seq[k], receipt{run: runID, r: r, passed: !crew.FailedOutcome(r)})
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		add(chain[i].row.ID, chain[i].checks)
	}
	add(run, rows.checks)
	var links []history.Link
	for _, rs := range seq {
		for i, p := range rs {
			if !p.passed || p.run != run {
				continue
			}
			for j := i - 1; j >= 0 && !rs[j].passed; j-- {
				f := rs[j]
				links = append(links, history.Link{From: hCheckRef(f.run, f.r.TaskID, f.r.N, f.r.ID, f.r.K), Rel: relPassedAfter, To: hCheckRef(run, p.r.TaskID, p.r.N, p.r.ID, p.r.K)})
			}
		}
	}
	return links
}

// reviewedBy is the work attempt a review attempt v reviewed, found as
// the engine names it: the review's subject is the first task it needs
// (its crew's Needs[0], reopen), and its start commit that task's result
// when it was dispatched, the result of the subject's last attempt to
// succeed with that commit before the review was reserved, in the review's
// run or back along the runs it continues (a result imported by a
// continuation). runs are the review's run first, then its ancestors.
func reviewedBy(p runPlace, v ledger.AttemptRow, runs []runRows) (chainAttempt, bool) {
	needs := p.needs[v.TaskID]
	if len(needs) == 0 || v.StartCommit == "" {
		return chainAttempt{}, false
	}
	subject := needs[0]
	for _, rr := range runs {
		var out ledger.AttemptRow
		found := false
		for _, w := range rr.attempts {
			if w.TaskID != subject || w.State != ledger.AttemptSucceeded || w.ResultCommit != v.StartCommit {
				continue
			}
			if rr.row.ID == runs[0].row.ID && !v.ReservedAt.IsZero() && !w.ClosedAt.IsZero() && w.ClosedAt.After(v.ReservedAt) {
				continue // closed after the review was reserved: not its subject
			}
			if !found || w.N > out.N {
				out, found = w, true
			}
		}
		if found {
			return chainAttempt{rr.row.ID, out}, true
		}
	}
	return chainAttempt{}, false
}

// reviewLinks are the review links whose later end is the run's: reviews
// from each of its review attempts to the work attempt it reviewed
// (reviewedBy), and reopened from a needs_changes verdict to the reviewed
// task's next attempt, the repair, which is the run's either because the
// review is (its next attempt in the run) or because the review is an
// ancestor's whose run ended before the repair began, carried into this
// continuation (the task's first attempt here, when no run between holds
// one).
func reviewLinks(p runPlace, rows *runRows, chain []runRows) []history.Link {
	runs := append([]runRows{*rows}, chain...)
	needsChanges := func(v ledger.AttemptRow) bool {
		var res crew.Result
		return v.Result != "" && json.Unmarshal([]byte(v.Result), &res) == nil && res.Verdict == crew.VerdictNeedsChanges
	}
	next := func(as []ledger.AttemptRow, task string, after int) int {
		n := 0
		for _, a := range as {
			if a.TaskID == task && a.N > after && (n == 0 || a.N < n) {
				n = a.N
			}
		}
		return n
	}
	var links []history.Link
	for i, rr := range runs {
		for _, v := range rr.attempts {
			if p.kinds[v.TaskID] != crew.TaskReview || v.Result == "" {
				continue
			}
			if i > 0 && !needsChanges(v) {
				continue
			}
			w, ok := reviewedBy(p, v, runs[i:])
			if !ok {
				continue
			}
			from := attemptKey(rr.row.ID, v.TaskID, v.N)
			if i == 0 {
				links = append(links, history.Link{From: from, Rel: relReviews, To: attemptKey(w.run, w.a.TaskID, w.a.N)})
				if !needsChanges(v) {
					continue
				}
				if n := next(rows.attempts, w.a.TaskID, w.a.N); n > 0 {
					links = append(links, history.Link{From: from, Rel: relReopened, To: attemptKey(p.run, w.a.TaskID, n)})
				}
				continue
			}
			// An ancestor's review: the repair is this run's only when
			// neither its run nor any run between holds an attempt of the
			// task after the one reviewed.
			if slices.ContainsFunc(runs[1:i+1], func(c runRows) bool { return next(c.attempts, w.a.TaskID, w.a.N) > 0 }) {
				continue
			}
			if n := next(rows.attempts, w.a.TaskID, w.a.N); n > 0 {
				links = append(links, history.Link{From: from, Rel: relReopened, To: attemptKey(p.run, w.a.TaskID, n)})
			}
		}
	}
	return links
}

// dedupLinks keeps each link once, in its first place.
func dedupLinks(links []history.Link) []history.Link {
	seen := map[history.Link]bool{}
	out := links[:0:0]
	for _, l := range links {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}
