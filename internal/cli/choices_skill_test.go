package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/api"
)

// A choice's bound is in bytes (#703, review round 1): parseChoices, which
// notify --choices and the ask tool both go through, measures len, so the
// skill and the guide's ask say bytes, with this package's numbers, and a
// choice of 40 characters past 40 bytes is refused.
func TestTheSkillStatesTheChoiceBoundInBytes(t *testing.T) {
	say := fmt.Sprintf("at most %d, each at most %d bytes", maxChoices, maxChoiceSize)
	if words := strings.Join(strings.Fields(agents.Skill), " "); !strings.Contains(words, say) {
		t.Errorf("the skill does not say %q, the bound parseChoices enforces", say)
	}
	ops := api.Operations()
	i := slices.IndexFunc(ops, func(o api.Operation) bool { return o.MCP == "ask" })
	if i < 0 {
		t.Fatal("the guide has no ask tool")
	}
	if !strings.Contains(ops[i].Description, say) {
		t.Errorf("the ask tool's description does not say %q: %s", say, ops[i].Description)
	}

	fits := strings.Repeat("é", maxChoiceSize/2) // 40 bytes
	if _, err := parseChoices("a|" + fits); err != nil {
		t.Errorf("a choice of %d bytes is refused: %v", len(fits), err)
	}
	over := strings.Repeat("é", maxChoiceSize/2+1) // 21 characters, 42 bytes
	if _, err := parseChoices("a|" + over); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("longer than %d bytes", maxChoiceSize)) {
		t.Errorf("a choice of %d characters and %d bytes: %v, want refused in bytes", len([]rune(over)), len(over), err)
	}
}
