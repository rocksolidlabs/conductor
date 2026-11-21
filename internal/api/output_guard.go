package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The copied-output check (docs/plans/sync-seams.md, OS-1c): whether a
// text quotes what the server holds captured, by the index (output_index.go).
// It knows nothing of Dispatch or decisions: each caller keeps the verdict
// beside its own text, so a later route or door calls it unchanged.
//
// What counts as terminal output is the bytes Conductor captured, as the
// owner decided on 2026-11-20 (docs/plans/sync-seams.md, What counts as
// terminal output): an agent's text is judged by what it quotes, as a
// person's is. agentTextIsOutput is that reading in one place.
const agentTextIsOutput = false

// outputGuard judges texts against the index. A nil guard, or one with no
// index, answers pending: no verdict is clean that the check could not
// make.
type outputGuard struct {
	index *outputIndex
}

// Check is the verdict of one text: clean, output (it quotes captured
// output by the rule), or pending (the index cannot answer now).
func (g *outputGuard) Check(ctx context.Context, text string) string {
	if len(text) < indexLine {
		// Below every threshold of the rule: nothing it holds can quote.
		return verdictClean
	}
	if g == nil || g.index == nil || !g.index.whole(ctx) {
		return verdictPending
	}
	q, err := g.index.quotes(text)
	switch {
	case err != nil:
		return verdictPending
	case q:
		return verdictOutput
	}
	return verdictClean
}

// Judge is the verdict of a text written by an agent (agent) or a person:
// the rule's reading in one place (agentTextIsOutput), then Check.
func (g *outputGuard) Judge(ctx context.Context, text string, agent bool) string {
	if agentTextIsOutput && agent && text != "" {
		return verdictOutput
	}
	return g.Check(ctx, text)
}

// JudgeNow is Judge in the form the ledger calls inside a transaction,
// under its lock: it never waits and never reads a kept source. While the
// index is behind, or a hit could only be confirmed by reading a kept
// source (a check's log, which the ledger itself holds), the verdict is
// pending, and the settle loop judges the part again outside the lock.
func (g *outputGuard) JudgeNow(_ context.Context, text string, agent bool) string {
	if agentTextIsOutput && agent && text != "" {
		return verdictOutput
	}
	if len(text) < indexLine {
		return verdictClean
	}
	if g == nil || g.index == nil || !g.index.wholeNow() {
		return verdictPending
	}
	hit, deferred, err := g.index.quotesIf(text, false)
	switch {
	case err != nil, !hit && deferred:
		return verdictPending
	case hit:
		return verdictOutput
	}
	return verdictClean
}

// JudgeValue is the verdict of a change row's value or prior: the value
// itself, or, when it is JSON (a create's fields, a list's members), each
// string it holds, the worst of them (output, then pending, then clean).
func (g *outputGuard) JudgeValue(ctx context.Context, value string, agent bool) string {
	return g.judgeValue(ctx, value, agent, g.Judge)
}

// JudgeValueNow is JudgeValue in the form a write's transaction calls, as
// JudgeNow: it never waits and never reads a kept source; what it cannot
// judge now is pending, settled outside the transaction.
func (g *outputGuard) JudgeValueNow(ctx context.Context, value string, agent bool) string {
	return g.judgeValue(ctx, value, agent, g.JudgeNow)
}

// judgeValue is JudgeValue by judge.
func (g *outputGuard) judgeValue(ctx context.Context, value string, agent bool, judge func(context.Context, string, bool) string) string {
	var v any
	if len(value) < indexLine || json.Unmarshal([]byte(value), &v) != nil {
		return judge(ctx, value, agent)
	}
	worst := verdictClean
	if _, ok := v.(string); !ok && agentTextIsOutput && agent {
		return verdictOutput
	}
	var walk func(any)
	walk = func(v any) {
		if worst == verdictOutput {
			return
		}
		switch t := v.(type) {
		case string:
			if s := judge(ctx, t, agent); s == verdictOutput || s == verdictPending && worst == verdictClean {
				worst = s
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return worst
}

// outputArrived is a local session's output as it arrives: the index takes
// it as the session's ring, holding what the ring holds.
func (s *Server) outputArrived(id string, chunk []byte) {
	if s.outputs != nil {
		s.outputs.feed("ring:"+id, chunk, s.cfg.ScrollbackBytes)
		s.outputs.shotDue("screen:"+id, func() []string { return s.screenOf(id) }, s.cfg.ScrollbackBytes)
	}
}

// screenRows bounds the rows a screen is read with.
const screenRows = 10_000

// screenOf is a local session's screen as drawn, nil for none.
func (s *Server) screenOf(id string) []string {
	d, ok := s.registry.Get(id)
	if !ok {
		return nil
	}
	l, ok := d.(*session.Local)
	if !ok {
		return nil
	}
	rows, _ := l.ScreenRead(screenRows)
	return rows
}

// liveScreens is every local session's screen as drawn now, by source.
func (s *Server) liveScreens() map[string][]string {
	out := map[string][]string{}
	s.registry.Each(func(d session.Driver) {
		if l, ok := d.(*session.Local); ok {
			if rows, _ := l.ScreenRead(screenRows); len(rows) > 0 {
				out["screen:"+l.Info().ID] = rows
			}
		}
	})
	return out
}

// handedVerdicts is a row's two verdicts as it is handed out: its own,
// and for a row written before the check existed (none kept) the check's
// now, an agent's row output whatever that finds, since what it could
// have quoted may be gone. A row from another machine is not judged here.
func (s *Server) handedVerdicts(ctx context.Context, c dispatch.ChangeRow) (string, string) {
	if c.Remote {
		return c.Output, c.PriorOutput
	}
	value, prior := c.Output, c.PriorOutput
	agent := dispatch.AgentActor(c.Actor)
	judge := func(text string) string {
		if agent {
			return verdictOutput
		}
		return s.copyCheck.JudgeValue(ctx, text, false)
	}
	if value == "" {
		value = judge(c.Value)
	}
	if prior == "" {
		prior = verdictClean
		if c.Prior != "" {
			prior = judge(c.Prior)
		}
	}
	return value, prior
}

// settleEvery is how often the pending verdicts are judged again.
const settleEvery = 2 * time.Second

// settlePending judges every pending verdict again once the index is
// whole: output for a row this process did not write (another process's,
// beside this one over the same folder or one that ended: what it may
// have quoted lived in that process's memory), and when a source was
// pruned after the row was written (what it quoted may be gone), else what
// the check finds now. It answers how many rows it settled.
func (s *Server) settlePending(ctx context.Context) int {
	if s.dispatch == nil || s.copyCheck == nil || !s.outputs.whole(ctx) {
		return 0
	}
	rows, err := s.dispatch.PendingRows(ctx, dispatch.MaxPendingPage)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		pruned, err := s.outputs.prunedSince(r.At)
		if err != nil {
			return n
		}
		agent := dispatch.AgentActor(r.Actor)
		judge := func(verdict, text string) string {
			if verdict != verdictPending {
				return verdict
			}
			if pruned || !r.Mine {
				return verdictOutput
			}
			return s.copyCheck.JudgeValue(ctx, text, agent)
		}
		output, prior := judge(r.Output, r.Value), judge(r.PriorOutput, r.Prior)
		if output == verdictPending || prior == verdictPending {
			continue
		}
		if s.dispatch.SettleVerdicts(ctx, r.Seq, output, prior) == nil {
			n++
		}
	}
	return n
}

// settleLoop judges the pending verdicts again when a write left one
// pending (rowsWake) and every settleEvery besides, until ctx ends.
func (s *Server) settleLoop(ctx context.Context) {
	t := time.NewTicker(settleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.rowsWake:
		}
		s.settlePending(ctx)
	}
}

// observeLogs has the ledger tell the index of every check's and
// prerequisite's log it keeps, after its commit, as a kept source.
func (s *Server) observeLogs(ld *ledger.Ledger) {
	ld.OnLog(func(source string, log []byte) {
		if s.outputs != nil {
			s.outputs.keepText(source, log)
		}
	})
	// A decision's free-text parts are judged as they are written
	// (decision_output, OS-3), when a sync store is registered: with none,
	// nothing of a decision leaves and the ledger writes as before.
	if s.copyCheck != nil && s.syncName() != "" {
		// Inside the ledger's transaction: the form that never waits and
		// never reads the ledger (JudgeNow); a part it leaves pending has
		// the settle loop woken once the write's commit is told
		// (pokeSettle).
		ld.SetOutputCheck(func(ctx context.Context, text string, agent bool) string {
			v := s.copyCheck.JudgeNow(ctx, text, agent)
			if v == verdictPending {
				s.settleDue.Store(true)
			}
			return v
		})
	}
}

// logsPruned lets a pruned run's logs go from the index.
func (s *Server) logsPruned(runID string, _ int64) {
	if s.outputs != nil {
		s.outputs.prunePrefix("check:" + runID + "/")
		s.outputs.prunePrefix("prerequisite:" + runID + "/")
	}
}

// readLogs gives the index the ledger's logs, for a hit's confirmation:
// a check's by run, task, attempt, id and k, a prerequisite's by run, id
// and k.
func (s *Server) readLogs(x *outputIndex) {
	x.reader("check", func(id string) ([]byte, bool) {
		p := strings.Split(id, "/")
		if len(p) != 5 || s.ledger == nil {
			return nil, false
		}
		n, err1 := strconv.Atoi(p[2])
		k, err2 := strconv.Atoi(p[4])
		if err1 != nil || err2 != nil {
			return nil, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), indexCatchUp)
		defer cancel()
		r, ok, err := s.ledger.Check(ctx, p[0], p[1], n, p[3], k)
		return r.Log, ok && err == nil && len(r.Log) > 0
	})
	x.reader("prerequisite", func(id string) ([]byte, bool) {
		p := strings.Split(id, "/")
		if len(p) != 3 || s.ledger == nil {
			return nil, false
		}
		k, err := strconv.Atoi(p[2])
		if err != nil {
			return nil, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), indexCatchUp)
		defer cancel()
		r, ok, err := s.ledger.Prerequisite(ctx, p[0], p[1], k)
		return r.Log, ok && err == nil && len(r.Log) > 0
	})
}

// rebuildIndex makes a fresh index again from what the server keeps
// captured: every run's checks' and prerequisites' logs in the ledger. A
// session's ring and screen went with the process before this one.
func (s *Server) rebuildIndex() {
	if s.outputs == nil || !s.outputs.fresh {
		return
	}
	ld := s.ledger
	s.outputs.rebuild(func(keep func(string, []byte)) {
		s.historySources(keep)
		if ld == nil {
			return
		}
		ctx := context.Background()
		runs, err := ld.Runs(ctx)
		if err != nil {
			s.outputs.fail(err)
			return
		}
		for _, r := range runs {
			checks, err := ld.Checks(ctx, r.ID, "", 0, true)
			if err != nil {
				s.outputs.fail(err)
				return
			}
			for _, c := range checks {
				if len(c.Log) > 0 {
					keep(ledger.CheckLogSource(r.ID, c.TaskID, c.N, c.ID, c.K), c.Log)
				}
			}
			pre, err := ld.Prerequisites(ctx, r.ID, true)
			if err != nil {
				s.outputs.fail(err)
				return
			}
			for _, p := range pre {
				if len(p.Log) > 0 {
					keep(ledger.PrerequisiteLogSource(r.ID, p.ID, p.K), p.Log)
				}
			}
		}
	})
}

// outputNotHeld marks a write whose agent runs where the server does not
// hold its output: a hosted session's. The host link carries a session's
// signalling and, only to a viewer that asked for the server's relay, its
// terminal; with no such viewer none of its output passes the server, so
// the index cannot know what it printed.
type outputNotHeld struct{}

// withOutputNotHeld is ctx marking its write's agent as one whose output
// the server does not hold.
func withOutputNotHeld(ctx context.Context) context.Context {
	return context.WithValue(ctx, outputNotHeld{}, true)
}

// judgeRow is the check Dispatch's writes are judged by, inside their
// transaction: an agent's text whose output the server does not hold is
// output (it stays on this machine), failing closed where the check cannot
// look; every other text goes through the guard without waiting or reading
// a kept source, and one it cannot judge now is pending, which wakes the
// settle once the write's commit is told.
func (s *Server) judgeRow(ctx context.Context, text string, agent bool) string {
	if agent && text != "" && ctx.Value(outputNotHeld{}) != nil {
		// Counted for the sync status's words (sync_status.go).
		s.hostedKept.Add(1)
		return verdictOutput
	}
	v := s.copyCheck.JudgeValueNow(ctx, text, agent)
	if v == verdictPending {
		s.rowsDue.Store(true)
	}
	return v
}

// historySource is a history record's entry as the index names it:
// history:<session>/<seq>.
func historySource(id string, seq int64) string { return fmt.Sprintf("history:%s/%d", id, seq) }

// entryWords is the words a record's entry keeps: its message and its
// choices, a line each.
func entryWords(e journal.Entry) string {
	return strings.Join(append([]string{e.Message}, e.Choices...), "\n")
}

// historyKept indexes an entry a session's record keeps, as a kept source
// read again through the record (readHistory).
func (s *Server) historyKept(id string, e journal.Entry) {
	if s.outputs == nil {
		return
	}
	if words := entryWords(e); len(words) >= indexLine {
		s.outputs.keepText(historySource(id, e.Seq), []byte(words))
	}
}

// readHistory gives the index the records' entries, for a hit's
// confirmation: history:<session>/<seq>.
func (s *Server) readHistory(x *outputIndex) {
	x.reader("history", func(id string) ([]byte, bool) {
		sid, seqs, ok := strings.Cut(id, "/")
		seq, err := strconv.ParseInt(seqs, 10, 64)
		j := s.sessionRecords
		if !ok || err != nil || j == nil {
			return nil, false
		}
		r, _, err := j.Get(sid)
		if err != nil {
			return nil, false
		}
		for _, e := range r.Entries {
			if e.Seq == seq {
				return []byte(entryWords(e)), true
			}
		}
		return nil, false
	})
}

// historySources hands every record's entries to keep, for a rebuild.
func (s *Server) historySources(keep func(string, []byte)) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	for after := ""; ; {
		page, err := j.List(after, journal.MaxPage)
		if err != nil {
			s.outputs.fail(err)
			return
		}
		for _, sum := range page.Records {
			r, _, err := j.Get(sum.ID)
			if err != nil {
				continue
			}
			for _, e := range r.Entries {
				if words := entryWords(e); len(words) >= indexLine {
					keep(historySource(r.ID, e.Seq), []byte(words))
				}
			}
		}
		if page.Next == "" {
			return
		}
		after = page.Next
	}
}
