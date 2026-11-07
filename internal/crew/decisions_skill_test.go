package crew

import (
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The skill's Decisions section quotes each line Conductor types to a
// decision's asking agent, which an agent acts on with nothing else to go
// on: built here by the engine's own words, with the skill's placeholders
// for their parts, so a change to a line that the skill does not follow
// fails (#689, review round 1).
func TestTheSkillQuotesTheEngine(t *testing.T) {
	skill := strings.Join(strings.Fields(agents.Skill), " ")
	zero := 0
	d := &decision{n: 7, typ: ledger.DecisionApproval, options: []string{"ANSWER", "Keep it"}}
	for _, line := range []string{
		answerWords(d, &accepted{k: 1, choice: &zero, byName: "NAME", rationale: "WHY"}),
		moreInfoWords("dr-7", "TEXT"),
		cancelledWords("dr-7", "cancelled by the operator, not approved: WHY"),
		supersededWords("dr-7", "WHAT MOVED"),
		notAppliedWords("dr-7", "WHAT MOVED"),
	} {
		if !strings.Contains(skill, `"`+line+`"`) {
			t.Errorf("the skill does not quote %q", line)
		}
	}
}
