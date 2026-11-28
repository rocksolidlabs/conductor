package agentproto

import (
	"errors"
	"path/filepath"
	"slices"
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

// A lone "--" in a launch's arguments: every chat's child would read what
// the driver adds after it as plain words, so each driver refuses it, and
// StaysInTerminal says so for each.
func TestALoneDoubleDashKeepsATerminal(t *testing.T) {
	hooks := filepath.Join(t.TempDir(), "hooks")
	for _, args := range [][]string{{"--"}, {"--", "fix the tests"}, {"--effort", "high", "--"}} {
		if _, err := ClaudeArgs(ClaudeLaunch{Command: []string{"claude"}, Conversation: "c0ffee-1", HooksDir: hooks, LaunchArgs: args, PermissionHost: true}); !errors.Is(err, ErrOptionsEnd) {
			t.Errorf("claude %q: %v", args, err)
		}
		var cx *CodexArgError
		if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args}); !errors.As(err, &cx) {
			t.Errorf("codex %q: %v", args, err)
		}
		var acp *ACPArgError
		if _, err := ACPArgs(ACPLaunch{Command: []string{"agent"}, Protocol: []string{"acp"}, Conversation: "ses_1", LaunchArgs: args}); !errors.As(err, &acp) {
			t.Errorf("acp %q: %v", args, err)
		}
	}
	// The catalog's own sets are judged the same way.
	for _, l := range []ClaudeLaunch{
		{Command: []string{"claude", "--"}, Conversation: "c0ffee-1", HooksDir: hooks},
		{Command: []string{"claude"}, Protocol: []string{"--"}, Conversation: "c0ffee-1", HooksDir: hooks},
		{Command: []string{"claude"}, Yolo: true, YoloArgs: []string{"--"}, Conversation: "c0ffee-1", HooksDir: hooks},
	} {
		if _, err := ClaudeArgs(l); !errors.Is(err, ErrOptionsEnd) {
			t.Errorf("%+v: %v", l, err)
		}
	}
	// A double dash inside a word is no lone one.
	if _, err := ClaudeArgs(ClaudeLaunch{Command: []string{"claude"}, Conversation: "c0ffee-1", HooksDir: hooks, LaunchArgs: []string{"--append-system-prompt", "a -- b"}}); err != nil {
		t.Errorf("a -- inside a value: %v", err)
	}
	for _, name := range []string{ClaudeStreamJSON, CodexAppServer, ACPv1} {
		if !StaysInTerminal(name, "--") {
			t.Errorf("%s: a lone -- not marked", name)
		}
	}
}

// What the driver adds after the person's words stays what it is: for
// Claude Code, every driver flag comes after them and a lone "--" is
// refused; for Codex, the person's words reach the argv only as the
// overrides' pairs, whose values never start with a dash; for an ACP
// agent, none of them reaches it.
func TestThePersonsWordsLeaveTheDriversOwn(t *testing.T) {
	hooks := filepath.Join(t.TempDir(), "hooks")
	argv, err := ClaudeArgs(ClaudeLaunch{Command: []string{"claude"}, Conversation: "c0ffee-1", HooksDir: hooks, LaunchArgs: []string{"--effort", "high"}, PermissionHost: true})
	if err != nil {
		t.Fatal(err)
	}
	tail := argv[len(argv)-7:]
	want := []string{"--mcp-config", filepath.Join(hooks, ClaudeProtocolMCP), "--strict-mcp-config", "--permission-prompt-tool", ClaudePermissionTool, "--settings", filepath.Join(hooks, ClaudeProtocolSettings)}
	if !slices.Equal(tail, want) || slices.Index(argv, "high") > slices.Index(argv, "--mcp-config") {
		t.Fatalf("%q", argv)
	}
	for _, args := range [][]string{{"-c", "--"}, {"-c", "-x"}, {"--config", "--mcp-config"}, {"--enable", "--"}, {"--disable=-x"}, {"-c=--"}} {
		var cx *CodexArgError
		if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args}); !errors.As(err, &cx) {
			t.Errorf("codex %q: %v", args, err)
		}
	}
	if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: []string{"-c", "model_reasoning_effort=high", "--enable", "web"}}); err != nil {
		t.Errorf("codex's overrides: %v", err)
	}
	var acp *ACPArgError
	if _, err := ACPArgs(ACPLaunch{Command: []string{"agent"}, Protocol: []string{"acp"}, Conversation: "ses_1", LaunchArgs: []string{"--x"}}); !errors.As(err, &acp) {
		t.Errorf("acp: %v", err)
	}
	if !StaysInTerminal(ACPv1, "--model") || StaysInTerminal(ACPv1, "word") {
		t.Error("acp: a flag not marked, or a plain word marked")
	}
}
