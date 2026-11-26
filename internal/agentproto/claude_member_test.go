package agentproto

import (
	"errors"
	"slices"
	"testing"
)

// A crew member's chat (design 14) starts a new conversation by the
// session recipe's start on a new id, never --resume, with Conductor's
// tool server alone; one that is not yolo gets no --permission-mode of
// Conductor's, a yolo one the catalog's yolo arguments, and a person's
// chat keeps its mode.
func TestMemberChatArgvStartsANewConversation(t *testing.T) {
	hooks := "/data/hooks"
	id := "99999999-8888-4777-8666-555555555555"
	fresh := []string{"--session-id", id}
	member := ClaudeLaunch{Command: []string{"claude"}, Conversation: id, Fresh: fresh, Member: true, Model: []string{"--model=opus"}, LaunchArgs: []string{"--fallback-model=sonnet"}, HooksDir: hooks, PermissionHost: true}
	got, err := ClaudeArgs(member)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Concat([]string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--replay-user-messages"},
		fresh, []string{"--model=opus", "--fallback-model=sonnet", "--mcp-config", hooks + "/claude-protocol-mcp.json", "--strict-mcp-config", "--permission-prompt-tool", "mcp__conductor__permission", "--settings", hooks + "/claude-protocol.json"})
	if !slices.Equal(got, want) {
		t.Fatalf("a member's chat:\n got %q\nwant %q", got, want)
	}
	if slices.Contains(got, "--resume") || slices.Contains(got, "--permission-mode") {
		t.Fatalf("a member's chat resumes or takes a mode: %q", got)
	}
	yolo := member
	yolo.Yolo, yolo.YoloArgs = true, []string{"--dangerously-skip-permissions"}
	got, err = ClaudeArgs(yolo)
	if err != nil || !slices.Contains(got, "--dangerously-skip-permissions") || slices.Contains(got, "--permission-mode") || !slices.Contains(got, hooks+"/claude-protocol-yolo.json") {
		t.Fatalf("a yolo member's chat: %q %v", got, err)
	}
	person := member
	person.Member, person.Fresh = false, nil
	got, err = ClaudeArgs(person)
	if err != nil || !slices.Contains(got, "--resume") || got[slices.Index(got, "--permission-mode")+1] != "default" {
		t.Fatalf("a person's chat: %q %v", got, err)
	}
	// The recipe's start is the chat's own --session-id, which a launch's
	// arguments may not hold; an added folder in it is refused as any.
	bad := member
	bad.LaunchArgs = []string{"--session-id", id}
	var own *OwnFlagError
	if _, err := ClaudeArgs(bad); !errors.As(err, &own) || own.Flag != "--session-id" {
		t.Fatalf("a launch's own --session-id: %v", err)
	}
	bad = member
	bad.Fresh = []string{"--session-id", id, "--add-dir", "/x"}
	if _, err := ClaudeArgs(bad); !errors.Is(err, ErrAddedFolder) {
		t.Fatalf("an added folder in the start: %v", err)
	}
}
