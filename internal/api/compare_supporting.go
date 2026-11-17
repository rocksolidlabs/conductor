package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// What was said of a comparison beside what was measured
// (docs/plans/compare-crew.md, PR-5, invariant I6): each candidate's own
// summary, its review's verdict and findings, and Brakeman's draft. They
// are read from the ledger and Brakeman's records, never into the
// measured part (candidates, checks), and nothing counts agreement: the
// page shows them in a panel of their own.

// The supporting part's bounds, each string as encoding/json writes it:
// four claims of a 4 KiB summary, four reviews of twenty findings' titles
// of 300 bytes, Brakeman's state; at most maxSupporting in all.
const (
	maxSupporting      = 48 << 10
	maxClaimSummary    = 4 << 10
	maxFindingTitle    = 300
	maxReviewFindings  = 20
	maxSupportVerdict  = 40
	maxSupportedLanes  = 4
	maxSupportedChains = 256
)

// The words the comparison gives for a draft there is none of.
const (
	draftNotSetUp    = "Brakeman is not set up"
	draftOverBudget  = "over today's budget"
	draftNoRunsClass = "Brakeman does not see runs"
)

// comparisonSupporting is what was said of a comparison beside what was
// measured: the candidates' claims, their reviews and Brakeman's draft.
type comparisonSupporting struct {
	Claims   []supportClaim  `json:"claims"`
	Reviews  []supportReview `json:"reviews"`
	Brakeman supportBrakeman `json:"brakeman"`
}

// supportClaim is a candidate's own summary of its result; it favours
// its own candidate (PR-6b).
type supportClaim struct {
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Summary string `json:"summary"`
	// SummaryCut says the summary is longer than it is given here.
	SummaryCut bool   `json:"summaryCut,omitempty"`
	Favours    string `json:"favours"`
}

// supportReview is the verdict of a review of a candidate's commit, and
// its findings' titles: task and attempt are the review's own; it
// favours the candidate it approved, none for another verdict (PR-6b).
type supportReview struct {
	Task     string   `json:"task"`
	Attempt  int      `json:"attempt"`
	Verdict  string   `json:"verdict"`
	Findings []string `json:"findings"`
	// FindingsTotal is how many findings the review gave, of which
	// Findings holds the first maxReviewFindings.
	FindingsTotal int    `json:"findingsTotal"`
	Favours       string `json:"favours,omitempty"`
}

// supportBrakeman is the newest compare request about the comparison:
// its id, its state and its brief's (the request's id once it has one);
// or why there is none.
type supportBrakeman struct {
	Request string `json:"request,omitempty"`
	State   string `json:"state,omitempty"`
	Why     string `json:"why,omitempty"`
	Brief   string `json:"brief,omitempty"`
}

// comparisonSupporting reads the supporting part of the comparison c from
// the ledger and Brakeman's records; nil without a ledger.
func (s *Server) comparisonSupporting(ctx context.Context, c crew.Comparison) *comparisonSupporting {
	if s.ledger == nil {
		return nil
	}
	out := &comparisonSupporting{Claims: []supportClaim{}, Reviews: []supportReview{}}
	chain, launched, err := s.compareChain(ctx, c.Run)
	if err != nil {
		s.log.Warn("a comparison's supporting part could not be read", "run", c.Run, "err", err)
		return out
	}
	attempts := map[string][]ledger.AttemptRow{}
	attemptsOf := func(run string) []ledger.AttemptRow {
		if a, ok := attempts[run]; ok {
			return a
		}
		a, err := s.ledger.Attempts(ctx, run)
		if err != nil {
			s.log.Warn("a comparison's attempts could not be read", "run", run, "err", err)
		}
		attempts[run] = a
		return a
	}
	for _, cand := range c.Candidates[:min(len(c.Candidates), maxSupportedLanes)] {
		if cand.Attempt == 0 || cand.Run == "" {
			continue
		}
		i := slices.IndexFunc(attemptsOf(cand.Run), func(a ledger.AttemptRow) bool { return a.TaskID == cand.Task && a.N == cand.Attempt })
		if i < 0 {
			continue
		}
		res, ok := resultOf(attemptsOf(cand.Run)[i])
		if !ok {
			continue
		}
		out.Claims = append(out.Claims, supportClaim{Task: cand.Task, Attempt: cand.Attempt, Summary: cutEncoded(res.Summary, maxClaimSummary), Favours: cand.Task})
		out.Claims[len(out.Claims)-1].SummaryCut = len(out.Claims[len(out.Claims)-1].Summary) < len(res.Summary)
		if r, ok := s.reviewOf(launched, chain, attemptsOf, cand); ok {
			out.Reviews = append(out.Reviews, r)
		}
	}
	out.Brakeman = s.draftOf(chain)
	return out
}

// compareChain is the runs of the comparison's chain, from its launched
// run on along its continuations (at most maxSupportedChains), and the
// crew as launched.
func (s *Server) compareChain(ctx context.Context, launched string) ([]string, launchedTasks, error) {
	var c launchedTasks
	var chain []string
	for id := launched; id != "" && len(chain) < maxSupportedChains; {
		row, ok, err := s.ledger.Run(ctx, id)
		if err != nil {
			return nil, c, err
		}
		if !ok {
			break
		}
		if len(chain) == 0 {
			if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
				return nil, c, fmt.Errorf("the crew of %s: %w", id, err)
			}
		}
		chain = append(chain, id)
		id = row.ContinuedBy
	}
	return chain, c, nil
}

// launchedTasks is what the supporting part reads of the crew as
// launched: its tasks alone, never its members.
type launchedTasks struct {
	Tasks []crew.Task `json:"tasks"`
}

// resultOf is the attempt's submitted result, ok false for none.
func resultOf(a ledger.AttemptRow) (crew.Result, bool) {
	var r crew.Result
	if a.Result == "" || json.Unmarshal([]byte(a.Result), &r) != nil {
		return r, false
	}
	return r, true
}

// reviewOf is the review of the candidate's commit: the newest attempt
// with a result, along the chain from its newest run, of a review task
// of the launched crew whose one need is the candidate's task and that
// started at the candidate's commit.
func (s *Server) reviewOf(launched launchedTasks, chain []string, attemptsOf func(string) []ledger.AttemptRow, cand crew.ComparedCandidate) (supportReview, bool) {
	var reviews []string
	for _, t := range launched.Tasks {
		if t.Kind == crew.TaskReview && slices.Equal(t.Needs, []string{cand.Task}) {
			reviews = append(reviews, t.ID)
		}
	}
	if len(reviews) == 0 || cand.Commit == "" {
		return supportReview{}, false
	}
	for i := len(chain) - 1; i >= 0; i-- {
		var best *ledger.AttemptRow
		for _, a := range attemptsOf(chain[i]) {
			if slices.Contains(reviews, a.TaskID) && a.StartCommit == cand.Commit && a.Result != "" && (best == nil || a.N > best.N) {
				best = &a
			}
		}
		if best == nil {
			continue
		}
		res, ok := resultOf(*best)
		if !ok {
			continue
		}
		r := supportReview{Task: best.TaskID, Attempt: best.N, Verdict: cutEncoded(res.Verdict, maxSupportVerdict), Findings: []string{}, FindingsTotal: len(res.Findings)}
		if res.Verdict == crew.VerdictApproved {
			r.Favours = cand.Task
		}
		for _, f := range res.Findings[:min(len(res.Findings), maxReviewFindings)] {
			r.Findings = append(r.Findings, cutEncoded(findingTitle(f), maxFindingTitle))
		}
		return r, true
	}
	return supportReview{}, false
}

// findingTitle is a finding in one line: its severity, its place and the
// first line of its message.
func findingTitle(f crew.Finding) string {
	msg, _, _ := strings.Cut(f.Message, "\n")
	place := ""
	switch {
	case f.Path != "" && f.Line > 0:
		place = fmt.Sprintf(" %s:%d", f.Path, f.Line)
	case f.Path != "":
		place = " " + f.Path
	}
	return f.Severity + place + ": " + msg
}

// draftOf is Brakeman's part: the newest compare request about a run of
// the chain, or why there is none.
func (s *Server) draftOf(chain []string) supportBrakeman {
	desk := s.brakeman
	if desk == nil {
		return supportBrakeman{Why: draftNotSetUp}
	}
	req, ok, err := s.drafts.newestAbout(desk, chain)
	if err != nil {
		s.log.Warn("a comparison's Brakeman request could not be read", "err", err)
	}
	if ok {
		out := supportBrakeman{Request: req.ID, State: req.State}
		if req.HasBrief {
			out.Brief = req.ID
		}
		if req.State == brakeman.StateOverBudget {
			out.Why = draftOverBudget
		}
		return out
	}
	cfg, err := desk.st.Config()
	switch {
	case err != nil || !cfg.Enabled || cfg.AgentID == "":
		return supportBrakeman{Why: draftNotSetUp}
	case !cfg.Visibility.Runs:
		return supportBrakeman{Why: draftNoRunsClass}
	}
	return supportBrakeman{}
}

// cutEncoded is s cut where a character ends so that encoding/json writes
// it in at most n bytes, quotes left out: '<', '>' and '&' count six, as
// a control character, U+2028, U+2029 and a byte that is no UTF-8 do.
func cutEncoded(s string, n int) string {
	used := 0
	for i, r := range s {
		w := encodedRune(s[i:], r)
		if used+w > n {
			return s[:i]
		}
		used += w
	}
	return s
}

// encodedRune is how many bytes encoding/json writes for r, the rune at
// the start of rest.
func encodedRune(rest string, r rune) int {
	switch {
	case r == utf8.RuneError:
		if _, size := utf8.DecodeRuneInString(rest); size == 1 {
			return 6
		}
		return 3
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == ' ' || r == ' ':
		return 6
	}
	return utf8.RuneLen(r)
}
