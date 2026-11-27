package agentproto

import (
	"errors"
	"path/filepath"
	"testing"
)

// StaysInTerminal is what the flags list marks Terminal only: it must say
// yes exactly where ClaudeArgs refuses the same argument, so the list and
// the move cannot disagree.
func TestStaysInTerminalAgreesWithClaudeArgs(t *testing.T) {
	refused := func(arg string) bool {
		_, err := ClaudeArgs(ClaudeLaunch{
			Command:      []string{"claude"},
			Conversation: "c0ffee-1",
			HooksDir:     filepath.Join(t.TempDir(), "hooks"),
			LaunchArgs:   []string{arg},
		})
		var own *OwnFlagError
		return errors.Is(err, ErrAddedFolder) || errors.As(err, &own)
	}
	var yes []string
	for _, f := range claudeOwnFlags {
		yes = append(yes, f, f+"=x")
	}
	yes = append(yes, "--add-dir", "--add-dir=/tmp", "-cp")
	for _, arg := range yes {
		if !StaysInTerminal(ClaudeStreamJSON, arg) {
			t.Errorf("%s: not marked", arg)
		}
		if !refused(arg) {
			t.Errorf("%s: marked, and ClaudeArgs takes it", arg)
		}
	}
	for _, arg := range []string{"--effort", "--effort=high", "--model", "--allowedTools", "-n", "--add-dirs"} {
		if StaysInTerminal(ClaudeStreamJSON, arg) {
			t.Errorf("%s: marked", arg)
		}
		if refused(arg) {
			t.Errorf("%s: not marked, and ClaudeArgs refuses it", arg)
		}
	}
	for _, name := range []string{"", "another-protocol"} {
		if StaysInTerminal(name, "--add-dir") || StaysInTerminal(name, "--verbose") {
			t.Errorf("protocol %q: marked", name)
		}
	}
}
