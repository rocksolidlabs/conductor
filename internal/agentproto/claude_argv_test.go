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

// For Codex, StaysInTerminal says yes exactly where CodexArgs refuses the
// flag (with a value it takes, where it takes one).
func TestStaysInTerminalAgreesWithCodexArgs(t *testing.T) {
	refused := func(args ...string) bool {
		_, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args})
		var e *CodexArgError
		return errors.Is(err, ErrAddedFolder) || errors.As(err, &e)
	}
	value := map[string]string{"-c": "k=v", "--config": "k=v", "--enable": "x", "--disable": "x", "-m": "o3", "--model": "o3", "-s": "read-only", "--sandbox": "read-only", "-a": "never", "--ask-for-approval": "never"}
	for _, f := range codexTakes {
		args := []string{f}
		if v, ok := value[f]; ok {
			args = append(args, v)
		}
		if StaysInTerminal(CodexAppServer, f) || refused(args...) {
			t.Errorf("%s: marked %v, refused %v", f, StaysInTerminal(CodexAppServer, f), refused(args...))
		}
	}
	for _, f := range []string{"--add-dir", "--add-dir=/x", "--cd", "-C", "--image", "--search", "--oss", "-i"} {
		if !StaysInTerminal(CodexAppServer, f) {
			t.Errorf("%s: not marked", f)
		}
		if !refused(f, "x") {
			t.Errorf("%s: marked, and CodexArgs takes it", f)
		}
	}
	if StaysInTerminal(CodexAppServer, "-mo3") || StaysInTerminal(CodexAppServer, "--model=o3") {
		t.Error("a taken flag with its value attached was marked")
	}
}
