package api

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The Conductor skill (internal/agents, Skill) and the guide say the same
// (#703): an agent reads both, and where they differed it could not tell
// which was true.

// skillWords is the skill's text with its line breaks and indents as
// single spaces, in lower case, so a phrase is found wherever it wraps.
func skillWords() string {
	return strings.ToLower(strings.Join(strings.Fields(agents.Skill), " "))
}

// Every limit the skill states is the server's, read from the constant
// that enforces it, and the guide gives the same bound; and the skill
// states no number past these, so a limit it gains is one this test
// holds to the server too.
func TestTheSkillStatesTheServersLimits(t *testing.T) {
	b := crew.TheBounds()
	limit := func(name string) int {
		ls := guideLimits()
		i := slices.IndexFunc(ls, func(l guideLimit) bool { return l.Name == name })
		if i < 0 {
			t.Fatalf("the guide has no limit %s", name)
		}
		return ls[i].Max
	}
	op := func(name string) Operation {
		ops := operationsTable()
		i := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == name })
		if i < 0 {
			t.Fatalf("the guide has no operation %s", name)
		}
		return ops[i]
	}
	maxItems := func(op Operation, prop string) int {
		n, _ := op.Input["properties"].(map[string]any)[prop].(map[string]any)["maxItems"].(int)
		return n
	}
	words := skillWords()
	limits := []struct {
		// say is how the skill says it, in skillWords' form; server is
		// the constant that enforces it; guide is the guide's bound.
		say           string
		server, guide int
	}{
		{"at most %d a minute", selfOpensPerMinute, limit("self_opens")},
		{"a session forms at most %d an hour", selfCrewsPerHour, limit("self_crews")},
		{"a crew has at most %d members", b.Members, limit("crew_members")},
		{"a member keeps at most %d not acknowledged", b.Inbox, limit("inbox")},
		{"one short line, at most %d bytes", session.MaxAttentionMessage, limit("report_message")},
		{"a url is at most %d bytes", MaxOpenURL, op("open_url").Bounds["url"]},
		{"a url is at most %d bytes", session.MaxEventURL, op("report").Bounds["url"]},
		{"a member name at most %d characters", b.MemberName, limit("member_name")},
		{"(at most %d, each at most 40 characters)", session.MaxAttentionOptions, maxItems(op("ask"), "choices")},
		{"approval (2 to %d options", ledger.MaxDecisionOptions, maxItems(op("decision_request"), "options")},
	}
	said := []string{}
	for _, l := range limits {
		said = append(said, strings.ReplaceAll(regexp.QuoteMeta(l.say), "%d", `\d+`))
		say := fmt.Sprintf(l.say, l.server)
		if !strings.Contains(words, say) {
			t.Errorf("the skill does not say %q, the server's bound", say)
		}
		if l.guide != l.server {
			t.Errorf("%q: the guide says %d, the server enforces %d", say, l.guide, l.server)
		}
	}
	// A choice's 40 is the command's and the tool's (internal/cli,
	// maxChoiceSize): the guide's ask says it as the skill does.
	if say := fmt.Sprintf("at most %d, each at most 40 characters", session.MaxAttentionOptions); !strings.Contains(strings.ToLower(op("ask").Description), say) {
		t.Errorf("the guide's ask does not say %q as the skill does", say)
	}
	// Every number the skill bounds something by is one of the above.
	known := regexp.MustCompile(strings.Join(said, "|"))
	covered := known.FindAllStringIndex(words, -1)
	for _, m := range regexp.MustCompile(`at most \d+|\d+ to \d+`).FindAllStringIndex(words, -1) {
		if !slices.ContainsFunc(covered, func(c []int) bool { return c[0] <= m[0] && m[1] <= c[1] }) {
			t.Errorf("the skill states %q, which this test does not hold to the server's bound", words[max(0, m[0]-40):m[1]])
		}
	}
}

// skillToolLeads are the conditions the skill lists its tools under, each
// as the guide lists an operation: its scope and what it needs of the
// session, past a launching server (not a switchyard) and a turn that is
// not Brakeman's, which the skill says apart. A Brakeman turn's own tools
// (the observe and act scopes) are its Brakeman section's.
var skillToolLeads = map[string]struct {
	scope string
	needs opNeeds
}{
	"In every session": {"session", 0},
	"In a session the server runs (not a hosted one), while it lets agents serve themselves (its agents.selfService setting, on by default)": {"session", needSelfService},
	"There, while the session is in no crew":                         {"session", needSelfService | needNoRun},
	"There, once the session is in a crew":                           {"session", needSelfService | needRun},
	"There, once the session is in a crew that is not a managed run": {"session", needSelfService | needRun | needUnmanaged},
	"In an attempt of a managed run (CONDUCTOR_TASK is set)":         {"task", 0},
	"In a run a person linked to a Dispatch ticket":                  {"ticket", 0},
}

// The skill names every tool the guide has, each under the condition the
// guide lists it under, and no other: none it does not know, none as
// always there that the guide gives only to a session in a crew (#703:
// add_member and run_status were listed for every session).
func TestTheSkillListsTheGuidesTools(t *testing.T) {
	type cond struct {
		scope string
		needs opNeeds
	}
	_, brakemans, _ := strings.Cut(agents.Skill, "\n## If you are Brakeman\n")
	brakemans, _, _ = strings.Cut(brakemans, "\n## ")
	want := map[cond][]string{}
	for _, op := range operationsTable() {
		if op.MCP != "" && (op.Scope == "observe" || op.Scope == "act") {
			if !regexp.MustCompile(`\b` + op.MCP + `\b`).MatchString(brakemans) {
				t.Errorf("the skill's Brakeman section does not name %s", op.MCP)
			}
		} else if op.MCP != "" {
			c := cond{op.Scope, op.needs &^ (needLauncher | needNotBrakeman)}
			want[c] = append(want[c], op.MCP)
		}
	}
	// The skill's list: the bullets after its MCP paragraph, up to the
	// first blank line, each "- CONDITION: a, b and c."
	_, rest, ok := strings.Cut(agents.Skill, "Call those you have; the commands below do the\nsame.\n\n")
	block, _, _ := strings.Cut(rest, "\n\n")
	if !ok || !strings.HasPrefix(block, "- ") {
		t.Fatalf("no list of tools in the skill:\n%s", block)
	}
	got := map[cond][]string{}
	for item := range strings.SplitSeq(strings.TrimPrefix(block, "- "), "\n- ") {
		item = strings.Join(strings.Fields(item), " ")
		lead, list, ok := strings.Cut(item, ": ")
		l, known := skillToolLeads[lead]
		if !ok || !known || !strings.HasSuffix(list, ".") {
			t.Errorf("the skill lists tools under %q, a condition this test does not know", item)
			continue
		}
		c := cond(l)
		if _, dup := got[c]; dup {
			t.Errorf("the skill lists %q twice", lead)
		}
		got[c] = strings.Split(strings.ReplaceAll(strings.TrimSuffix(list, "."), " and ", ", "), ", ")
	}
	for _, c := range slices.Collect(maps.Keys(want)) {
		slices.Sort(want[c])
		slices.Sort(got[c])
		if !slices.Equal(got[c], want[c]) {
			t.Errorf("for a caller of scope %s needing %b, the skill lists %v and the guide %v", c.scope, c.needs, got[c], want[c])
		}
	}
	for c, tools := range got {
		if _, ok := want[c]; !ok {
			t.Errorf("the skill lists %v under scope %s needing %b, where the guide lists nothing", tools, c.scope, c.needs)
		}
	}
}
