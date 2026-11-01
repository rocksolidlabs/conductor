package agents

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Claude Code reads extra settings from --settings, so a session launched by
// Conductor gets its hooks without anything written to ~/.claude. Install
// merges the same hooks into ~/.claude/settings.json for Claude Code started
// anywhere else; Claude Code runs a hook command it finds in both only once.
// Install also puts the Conductor skill in ~/.claude/skills.

const (
	claudeMarker   = "notify --claude-hook"
	claudeSettings = ".claude/settings.json"
)

func claudeHooks(events ...string) string {
	return jsonAsset(`{` + claudeHookList(events...) + `}`)
}

// claudeHookList is the "hooks" member of a settings file for events.
func claudeHookList(events ...string) string {
	entry := `{"hooks":[{"type":"command","command":"{{BIN}} notify --claude-hook"}]}`
	return `"hooks":{` + hookLists(entry, events...) + `}`
}

// claudeFileTools are the tools whose calls name a file (design 4e). The
// base settings hook their PostToolUse alone, reporting the files and not
// the calls (`--files`), so the Touched section fills with tool events off;
// the tools settings hook every call, which reports its files too.
const claudeFileTools = "Read|Edit|MultiEdit|NotebookEdit|Write"

// claudeBaseHookList is the "hooks" member of the base settings: the quiet
// events and the file tools' hook.
func claudeBaseHookList() string {
	entry := `{"hooks":[{"type":"command","command":"{{BIN}} notify --claude-hook"}]}`
	files := `{"matcher":"` + claudeFileTools + `","hooks":[{"type":"command","command":"{{BIN}} notify --claude-hook --files"}]}`
	return `"hooks":{` + hookLists(entry, claudeEvents...) + `,"PostToolUse":[` + files + `]}`
}

// claudeYoloKey skips the warning Claude Code shows before its first launch
// with --dangerously-skip-permissions (the yolo recipe). Claude Code honours
// only the last --settings, so it goes in the file that carries the hooks.
const claudeYoloKey = `"skipDangerousModePermissionPrompt":true`

// Claude Code's read-only mode is three rules. --tools names the built-in
// tools a session has, and these are its file reads alone: Conductor's MCP
// tools are not built in and stay. Denying only the tools that write or run
// a command would leave others that act (Task's subagents, EnterWorktree,
// CronCreate, RemoteTrigger, seen in 2.1.314's tool list), so the reads are
// named instead. --disallowedTools denies the writers by name as well: a
// deny rule holds over any allow rule in the person's own settings.
// --strict-mcp-config keeps only the MCP servers of --mcp-config, the one
// Conductor registers (MCP): --tools leaves every other MCP server the
// account has (its own, its plugins', its claude.ai connectors), and their
// tools can write. With it 2.1.314's tools were Read, Grep, Glob and
// Conductor's alone, a connector's create, update and delete tools gone.
var (
	claudeReadTools  = []string{"Read", "Grep", "Glob"}
	claudeWriteTools = []string{"Bash", "Edit", "Write", "NotebookEdit"}
)

var (
	claudeEvents      = []string{"Notification", "Stop", "UserPromptSubmit", "PermissionRequest", "PermissionDenied"}
	claudeToolsEvents = append(slices.Clone(claudeEvents), "PostToolUse", "PostToolUseFailure", "SubagentStop")
)

var claudeAssets = map[string]string{
	"claude.json": jsonAsset(`{` + claudeBaseHookList() + `}`),
	// Tool events are chatty: a signal asks for them with toolEvents.
	"claude-tools.json": claudeHooks(claudeToolsEvents...),
	// The same with the yolo key, for a launch with the yolo recipe, and the
	// key alone for one whose hooks are not wired.
	"claude-yolo.json":       jsonAsset(`{` + claudeYoloKey + `,` + claudeBaseHookList() + `}`),
	"claude-tools-yolo.json": jsonAsset(`{` + claudeYoloKey + `,` + claudeHookList(claudeToolsEvents...) + `}`),
	"claude-yolo-only.json":  jsonAsset(`{` + claudeYoloKey + `}`),
	// Conductor's MCP server, for --mcp-config (Claude Code 2.1.288 takes
	// JSON files or strings; verified with --help).
	"claude-mcp.json": jsonAsset(`{"mcpServers":{"conductor":{"command":"{{BIN}}","args":["mcp"]}}}`),
}

func claudeSteps(hooksDir string) []step {
	return []step{mergeHooksStep(claudeAssets, hooksDir, "claude.json", claudeSettings, claudeMarker), skillStep(hooksDir, claudeSkill)}
}

func claudeAdapter() Adapter {
	return Adapter{
		ID:   "claude",
		Name: "Claude Code",
		// Verified live (2.1.287): `claude --version` prints "2.1.287 (Claude Code)".
		Probe:  &Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`(?m)^\s*(\d+\.\d+\.\d+)\s*\(Claude Code\)`), Verified: true},
		Assets: claudeAssets,
		Inject: func(hooksDir string, sig catalog.Signal) ([]string, map[string]string) {
			settings := "claude.json"
			if sig.ToolEvents {
				settings = "claude-tools.json"
			}
			return []string{"--settings", filepath.Join(hooksDir, settings)}, nil
		},
		YoloInject: func(hooksDir string, sig catalog.Signal) ([]string, map[string]string) {
			settings := "claude-yolo-only.json"
			switch {
			case sig.Kind == catalog.SignalHook && sig.ToolEvents:
				settings = "claude-tools-yolo.json"
			case sig.Kind == catalog.SignalHook:
				settings = "claude-yolo.json"
			}
			return []string{"--settings", filepath.Join(hooksDir, settings)}, nil
		},
		ConfirmsSubmit: true,
		Install: func(home, hooksDir string) ([]string, error) {
			return install(home, claudeSteps(hooksDir)...)
		},
		SkillPath:    claudeSkill,
		ConfigDirEnv: "CLAUDE_CONFIG_DIR",
		MCP: func(hooksDir string) []string {
			return []string{"--mcp-config", filepath.Join(hooksDir, "claude-mcp.json")}
		},
		PlansDir: claudePlansDir,
		// Checked against `claude --help` (2.1.314) and by the recipes tier;
		// the = form, as --tools, --disallowedTools and --allowedTools take
		// a list and would read a following word as one of its items.
		// Conductor's tools come with MCP's --mcp-config, the only MCP
		// configuration --strict-mcp-config keeps.
		ReadOnlyArgs: func() []string {
			return []string{"--tools=" + strings.Join(claudeReadTools, ","), "--disallowedTools=" + strings.Join(claudeWriteTools, ","), "--strict-mcp-config"}
		},
		ModelArgs: func(model string) []string {
			if model == "" {
				return nil
			}
			return []string{"--model=" + model}
		},
		AllowTools: func(server string) []string {
			if !mcpServerName.MatchString(server) {
				return nil
			}
			return []string{"--allowedTools=mcp__" + server}
		},
		Status: func(home string) (bool, string) {
			return statusOf(home, claudeSettings, claudeSteps("")...)
		},
		Snippet: func(hooksDir string) string {
			return snippetOf(claudeAssets, hooksDir, "claude.json")
		},
		Events: []string{"needs_input", "working", "done", "tool_use", "tool_denied", "error", "progress"},
	}
}
