package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The copied-output check (docs/plans/sync-seams.md, OS-1c): whether a
// text quotes what the server holds captured, by the index (output_index.go).
// It knows nothing of Dispatch or decisions: each caller keeps the verdict
// beside its own text, so a later route or door calls it unchanged.
//
// What counts as terminal output is the owner's decision (the plan's open
// question, decision 25), taken at the plan's default until the owner
// answers: captured bytes. agentTextIsOutput is the whole of the other
// reading, "agent text too": set, every text an agent wrote is output
// whatever it quotes, and a person's is judged as it is now. Nothing else
// changes with it.
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

// JudgeValue is the verdict of a change row's value or prior: the value
// itself, or, when it is JSON (a create's fields, a list's members), each
// string it holds, the worst of them (output, then pending, then clean).
func (g *outputGuard) JudgeValue(ctx context.Context, value string, agent bool) string {
	var v any
	if len(value) < indexLine || json.Unmarshal([]byte(value), &v) != nil {
		return g.Judge(ctx, value, agent)
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
			if s := g.Judge(ctx, t, agent); s == verdictOutput || s == verdictPending && worst == verdictClean {
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
// whole: output when a source was pruned after the row was written (what
// it quoted may be gone), else what the check finds now. It answers how
// many rows it settled.
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
			if pruned {
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

// settleLoop judges the pending verdicts again every settleEvery until ctx
// ends.
func (s *Server) settleLoop(ctx context.Context) {
	t := time.NewTicker(settleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.settlePending(ctx)
		}
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

// judgeRow is the check Dispatch's writes are judged by: an agent's text
// whose output the server does not hold is output (it stays on this
// machine), failing closed where the check cannot look; every other text
// goes through the guard.
func (s *Server) judgeRow(ctx context.Context, text string, agent bool) string {
	if agent && text != "" && ctx.Value(outputNotHeld{}) != nil {
		return verdictOutput
	}
	return s.copyCheck.JudgeValue(ctx, text, agent)
}
