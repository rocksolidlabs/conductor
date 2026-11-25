package api

import (
	"errors"
	"slices"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// A crew member's chat's child (design 14) from its launch: a fresh one
// starts a new conversation by the session recipe's start and a resumed
// one resumes its conversation, neither with a permission mode of
// Conductor's for a member that is not yolo; a person's chat keeps its
// mode. What the move's checks refuse, before anything is stopped, is told
// in the run's log's words.
func TestAMembersChatArgvAndItsChecks(t *testing.T) {
	a, ok := catalog.Default().Get("claude")
	if !ok {
		t.Fatal("no built-in Claude Code")
	}
	l := &chatLaunch{agent: a, args: []string{"--fallback-model=sonnet"}, gate: &chatGate{}}
	id := "99999999-8888-4777-8666-555555555555"
	fresh, err := memberChatArgv(true)(l, id, "/data/hooks", true)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.Index(fresh, "--session-id")
	if at < 0 || fresh[at+1] != id || slices.Contains(fresh, "--resume") || slices.Contains(fresh, "--permission-mode") || !slices.Contains(fresh, "--strict-mcp-config") {
		t.Fatalf("a fresh member's chat: %q", fresh)
	}
	resumed, err := memberChatArgv(false)(l, id, "/data/hooks", true)
	if err != nil || resumed[slices.Index(resumed, "--resume")+1] != id || slices.Contains(resumed, "--session-id") || slices.Contains(resumed, "--permission-mode") {
		t.Fatalf("a resumed member's chat: %q %v", resumed, err)
	}
	person, err := chatArgv(l, id, "/data/hooks", true)
	if err != nil || person[slices.Index(person, "--permission-mode")+1] != "default" {
		t.Fatalf("a person's chat: %q %v", person, err)
	}
	// The checks the move asks with the terminal running.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--resume", "x"}, "its arguments hold --resume, which a chat sets itself."},
		{[]string{"--add-dir", "/other"}, "it was launched with an added folder."},
	} {
		bad := *l
		bad.args = c.args
		_, err := memberChatArgv(true)(&bad, id, "/data/hooks", true)
		var r *crew.TerminalReason
		if err == nil || !errors.As(memberReason(err), &r) || r.Words != c.want {
			t.Fatalf("%q: %v", c.args, err)
		}
	}
	for err, want := range map[error]string{
		agentproto.ErrNotQualified: "this version of Claude Code can only run in its terminal.",
		errNoPipes:                 "the run's Runtime has no pipes.",
		errNoChat:                  "its agent has no chat.",
	} {
		if got := memberReason(err).Error(); got != want {
			t.Fatalf("%v: %q, want %q", err, got, want)
		}
	}
}
