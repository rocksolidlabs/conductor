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
		// Codex's chat refuses any flag it takes no counterpart of, -- among
		// them; an ACP agent's refuses every word of the terminal's.
		var cx *CodexArgError
		if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args}); !errors.As(err, &cx) || cx.Flag != "--" && args[0] == "--" {
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

// What Claude Code's driver adds comes before the person's words, each flag
// in one word, whatever the person's words are: a last flag that wants a
// value, a list flag and plain words after it, a tool server of their own.
// Before them nothing of the person's, after them nothing of the driver's.
func TestTheDriversOwnFlagsComeFirstEachInOneWord(t *testing.T) {
	hooks := filepath.Join(t.TempDir(), "hooks")
	for _, c := range []struct {
		yolo, noMCP bool
		own         []string
	}{
		{false, false, []string{"--mcp-config=" + filepath.Join(hooks, ClaudeProtocolMCP), "--strict-mcp-config", "--permission-prompt-tool=" + ClaudePermissionTool, "--settings=" + filepath.Join(hooks, ClaudeProtocolSettings)}},
		{true, false, []string{"--mcp-config=" + filepath.Join(hooks, ClaudeProtocolMCP), "--strict-mcp-config", "--permission-prompt-tool=" + ClaudePermissionTool, "--settings=" + filepath.Join(hooks, ClaudeProtocolYoloSettings)}},
		{false, true, []string{"--strict-mcp-config", "--settings=" + filepath.Join(hooks, ClaudeProtocolSettings)}},
		{true, true, []string{"--strict-mcp-config", "--settings=" + filepath.Join(hooks, ClaudeProtocolYoloSettings)}},
	} {
		for _, words := range [][]string{
			nil,
			{"--effort", "high"},
			{"--append-system-prompt"},
			{"--allowedTools", "Bash", "Edit"},
			{"--allowedTools", "Bash", "Edit", "fix the tests"},
			{"--mcp-config", "/elsewhere.json", "--append-system-prompt"},
			{"--add-dirs"},
		} {
			l := ClaudeLaunch{Command: []string{"claude"}, Conversation: "c0ffee-1", HooksDir: hooks, LaunchArgs: words, PermissionHost: true, NoMCP: c.noMCP, Yolo: c.yolo, YoloArgs: []string{"--dangerously-skip-permissions"}}
			argv, err := ClaudeArgs(l)
			if err != nil {
				t.Fatalf("%q: %v", words, err)
			}
			at := len(argv) - len(words)
			if !slices.Equal(argv[at:], words) {
				t.Fatalf("%q: the person's words are not the argv's end: %q", words, argv)
			}
			head := argv[:at]
			if !slices.Equal(head[len(head)-len(c.own):], c.own) {
				t.Fatalf("yolo %v, no MCP %v, %q: the driver's own flags just before the person's words: %q", c.yolo, c.noMCP, words, argv)
			}
		}
	}
}

// Codex's chat takes the person's words only as the overrides' pairs, and
// a value that begins with a dash is refused, so no value of theirs can be
// read as the next option, or as the end of them.
func TestCodexOverrideValuesNeverStartWithADash(t *testing.T) {
	for _, args := range [][]string{{"-c", "--"}, {"-c", "-x"}, {"--config", "--mcp-config"}, {"--enable", "--"}, {"--disable=-x"}, {"-c=--"}} {
		var cx *CodexArgError
		if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args}); !errors.As(err, &cx) {
			t.Errorf("codex %q: %v", args, err)
		}
	}
	if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: []string{"-c", "model_reasoning_effort=high", "--enable", "web"}}); err != nil {
		t.Errorf("codex's overrides: %v", err)
	}
}

// An ACP agent's chat takes no word of the terminal's: StaysInTerminal says
// so for every one, a flag or a plain word.
func TestAnACPChatTakesNoWordOfTheTerminals(t *testing.T) {
	for _, w := range []string{"--x", "word", "--"} {
		var acp *ACPArgError
		if _, err := ACPArgs(ACPLaunch{Command: []string{"agent"}, Protocol: []string{"acp"}, Conversation: "ses_1", LaunchArgs: []string{w}}); !errors.As(err, &acp) {
			t.Errorf("acp %q: %v", w, err)
		}
		if !StaysInTerminal(ACPv1, w) {
			t.Errorf("acp %q: not marked", w)
		}
	}
}

// A setting Codex's chat asks by, fences by or reaches Conductor's tool
// server by is the driver's: an override of one in the terminal's
// arguments keeps the session in its terminal; any other is carried over.
func TestCodexKeepsItsOwnSettings(t *testing.T) {
	for _, kv := range []string{
		`approval_policy="never"`, `approval_policy.granular.rules=false`, ` "approval_policy" = "never"`,
		`sandbox_mode="danger-full-access"`, `sandbox_workspace_write.network_access=true`,
		`profile="fast"`, `profiles.fast.approval_policy="never"`,
		`mcp_servers.conductor.enabled=false`, `mcp_servers.conductor.command="x"`, `mcp_servers={}`,
		`mcp_servers.mine.default_tools_approval_mode="approve"`, `mcp_servers."mine".default_tools_approval_mode="approve"`,
	} {
		for _, args := range [][]string{{"-c", kv}, {"--config", kv}, {"-c" + kv}, {"--config=" + kv}} {
			var cx *CodexArgError
			if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: args}); !errors.As(err, &cx) {
				t.Errorf("%q: %v", args, err)
			}
		}
	}
	for _, kv := range []string{`model_reasoning_effort="high"`, `mcp_servers.mine.command="x"`, `features.web_search=true`, `sandbox_extra=1`} {
		if _, _, err := CodexArgs(CodexLaunch{Command: []string{"codex"}, Conversation: "c0ffee-1", LaunchArgs: []string{"-c", kv}}); err != nil {
			t.Errorf("%q: %v", kv, err)
		}
	}
}
