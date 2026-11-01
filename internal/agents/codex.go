package agents

import (
	"regexp"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Codex CLI takes config overrides with -c, so a session launched by Conductor
// gets a notify program, the terminal bell, rung for the notifications that
// wait for a person, and a window title that says what Codex is doing,
// without anything written to ~/.codex. Install puts the
// same notify line in a marked block of
// ~/.codex/config.toml and writes ~/.codex/hooks.json, Codex's experimental
// hooks, when there is none; Codex runs those only with features.hooks on.
// Install also puts the Conductor skill in ~/.codex/skills.

const (
	codexConfig = ".codex/config.toml"
	codexHooks  = ".codex/hooks.json"
	codexBegin  = "# >>> conductor"
	codexEnd    = "# <<< conductor"
	codexMarker = "notify --codex-hook"
	// codexHooksHint is what to do about a hooks.json Conductor did not write.
	codexHooksHint = "add Conductor's hooks from the snippet to it by hand (Codex runs the hooks in hooks.json only with features.hooks enabled)"
	// codexNotifications is the -c override naming the notifications Codex
	// rings the bell for in a session Conductor launches: every one Codex
	// 0.161 has (Notification::type_name in tui/src/chatwidget/notifications.rs)
	// but "async-question". That one is a question Codex asks without
	// stopping: its turn goes on, and the bell would show the session as
	// waiting until the turn's end (#186). An approval or an MCP form
	// ("approval-requested"), a request_user_input question or a plan's
	// prompt ("plan-mode-prompt") and the turn's end ("agent-turn-complete")
	// still ring. Codex takes any list of names and warns about none, so a
	// name it does not know only rings nothing.
	codexNotifications = `tui.notifications=["approval-requested","plan-mode-prompt","agent-turn-complete"]`
	// codexTerminalTitle is the -c override naming what Codex writes in its
	// window title (ESC ] 0 ; title BEL, tui/src/terminal_title.rs): its
	// activity (a spinner's frame while it works) and its run state, so the
	// title reads "⠋ Working" while a turn runs, "Ready" at rest (Codex
	// 0.161, seen on 2026-10-14). The session reads it as Codex at work
	// (catalog.CodexWorkingTitle): Codex reports nothing else during a turn,
	// and a wait raised while it goes on (a question it does not wait on)
	// would show until the turn's end (#186). Codex's own default names no
	// state, and a person's setting may name any items. The workbench shows
	// no program's title, so the override costs nothing seen.
	codexTerminalTitle = `tui.terminal_title=["activity","run-state"]`
)

var codexAssets = map[string]string{
	"codex-hooks.json": jsonAsset(`{"hooks":{` + hookLists(`{"hooks":[{"type":"command","command":"{{BIN}} notify --codex-hook"}]}`,
		"PreToolUse", "PostToolUse", "Stop", "PermissionRequest") + `}}`),
}

// codexNotify is the notify setting of config.toml, a TOML array: Codex runs
// the program with the event's JSON as the last argument.
func codexNotify(bin string) string {
	return "notify = [" + tomlString(bin) + `, "notify", "--codex"]`
}

func codexSteps(hooksDir string) []step {
	return []step{
		{codexConfig, func(h *homeDir) (bool, error) {
			bin, err := binPath()
			if err != nil {
				return false, err
			}
			cur, _, err := h.read(codexConfig)
			if err != nil {
				return false, err
			}
			line := codexNotify(bin)
			doc, err := readTOML(string(cur))
			if err != nil {
				return false, byHand("%s cannot be read line by line (%v); put %s at its top by hand", h.path(codexConfig), err, line)
			}
			// A second notify at the root would make config.toml invalid.
			if doc.setsRootKey("notify", codexBegin, codexEnd) {
				return false, byHand("%s sets notify already; make it %s by hand", h.path(codexConfig), line)
			}
			next, err := doc.withBlock(codexBegin, codexEnd, line)
			if err != nil {
				return false, byHand("%s: %v", h.path(codexConfig), err)
			}
			return h.write(codexConfig, []byte(next))
		}},
		{codexHooks, func(h *homeDir) (bool, error) {
			asset, err := assetFor(codexAssets, hooksDir, "codex-hooks.json")
			if err != nil {
				return false, err
			}
			return createJSON(h, codexHooks, asset, codexMarker, codexHooksHint)
		}},
		skillStep(hooksDir, codexSkill),
	}
}

func codexAdapter() Adapter {
	return Adapter{
		ID:   "codex",
		Name: "Codex CLI",
		// Verified live (0.159.0): `codex --version` prints "codex-cli 0.159.0".
		Probe:  &Probe{Args: []string{"--version"}, Match: regexp.MustCompile(`(?m)^\s*codex-cli\s+(\d+\.\d+\.\d+)`), Verified: true},
		Assets: codexAssets,
		// The per-launch trust override Codex takes in place of the trust
		// saved in config.toml: -c projects={"<dir>"={trust_level="trusted"}}
		// (the dotted -c form does not skip the question). Applied only with
		// the yolo recipe.
		TrustArgs: func(dirs []string) []string {
			parts := make([]string, 0, len(dirs))
			for _, d := range dirs {
				parts = append(parts, tomlString(d)+`={trust_level="trusted"}`)
			}
			return []string{"-c", "projects={" + strings.Join(parts, ",") + "}"}
		},
		Inject: func(hooksDir string, sig catalog.Signal) ([]string, map[string]string) {
			bin, err := binPath()
			if err != nil {
				return nil, nil
			}
			return []string{"-c", "notify=[" + tomlString(bin) + `,"notify","--codex"]`, "-c", `tui.notification_method="bel"`, "-c", codexNotifications, "-c", codexTerminalTitle}, nil
		},
		Install: func(home, hooksDir string) ([]string, error) {
			return install(home, codexSteps(hooksDir)...)
		},
		SkillPath:    codexSkill,
		ConfigDirEnv: "CODEX_HOME",
		// Codex takes an MCP server as config overrides: [mcp_servers.<name>]
		// command and args (its config.toml; verified against codex mcp add).
		// It starts a stdio MCP server with a minimal environment of its own
		// (HOME, PATH, TERM and a few more), not the session's, so the
		// server would not know its session and every Conductor tool failed
		// (#354): env_vars names the variables Codex copies from its own
		// environment into the server's. Names only: the token's value
		// never goes on argv or into a file.
		MCP: func(hooksDir string) []string {
			bin, err := binPath()
			if err != nil {
				return nil
			}
			return []string{"-c", "mcp_servers.conductor.command=" + tomlString(bin), "-c", `mcp_servers.conductor.args=["mcp"]`, "-c", "mcp_servers.conductor.env_vars=" + codexMCPEnv}
		},
		// Checked against `codex --help` (0.163.0) and by the recipes tier.
		// Conductor's MCP server still reaches the server under the
		// read-only sandbox (tried by hand with 0.163.0). Unlike Claude
		// Code's, this mode does not hold what the person's own Codex
		// configuration adds (codexReadOnlyApprovals says what and why).
		ReadOnlyArgs: func() []string { return []string{"--sandbox", "read-only", "-c", codexReadOnlyApprovals} },
		ModelArgs: func(model string) []string {
			if model == "" {
				return nil
			}
			return []string{"--model=" + model}
		},
		// Codex asks before every tool of an MCP server ("Allow the
		// conductor MCP server to run tool …?" in 0.163.0), whatever the
		// sandbox, unless the server's default_tools_approval_mode is
		// "approve" (one of auto, prompt, writes and approve; the recipes
		// tier reads the list from Codex's refusal of another value).
		AllowTools: func(server string) []string {
			if !mcpServerName.MatchString(server) {
				return nil
			}
			return []string{"-c", "mcp_servers." + server + `.default_tools_approval_mode="approve"`}
		},
		Status: func(home string) (bool, string) {
			return statusOf(home, codexConfig, codexSteps("")...)
		},
		Snippet: func(hooksDir string) string {
			bin, err := binPath()
			if err != nil {
				bin = "conductor"
			}
			return "# ~/.codex/config.toml, at the top:\n" + codexBegin + "\n" + codexNotify(bin) + "\n" + codexEnd + "\n\n" +
				"# ~/.codex/hooks.json (Codex runs these hooks only with features.hooks enabled):\n" +
				snippetOf(codexAssets, hooksDir, "codex-hooks.json")
		},
		Events: []string{"needs_input", "working", "done", "tool_use"},
	}
}

// codexReadOnlyApprovals is the approval policy of a read-only launch.
// Under the read-only sandbox alone Codex asks the person to run a write the
// sandbox blocked ("Would you like to run the following command?", 0.163.0,
// on-request), and a yes would write. The granular policy turns every such
// request down without asking (sandbox_approval, a prompt the person's
// command rules raise, a request for more permissions) and still asks an MCP
// server's questions, an MCP tool's approval among them (tried by hand with
// 0.163.0). A Codex too old to know it refuses to start, so a launch never
// runs without it.
//
// What the mode does not hold, in 0.163.0, and no flag of it turns off:
//   - the person's own MCP servers (mcp_servers in their configuration):
//     they stay beside Conductor's, run outside the sandbox and are asked
//     about like Conductor's, so one yes, or a server whose
//     default_tools_approval_mode is "approve", lets a tool of theirs
//     write. Codex turns a server off only by its name
//     (mcp_servers.<name>.enabled=false); an override of the whole
//     mcp_servers table leaves the configured ones (tried with a scratch
//     CODEX_HOME).
//   - the person's exec-policy allow rules: a command one matches runs
//     outside the sandbox without asking ("exec-policy allow behavior
//     (including any sandbox bypass) takes precedence", Codex's own
//     instructions in 0.163.0); rules=false refuses only the prompts a
//     prompt rule raises.
//
// So under Codex a Brakeman turn is read-only only for an account with no
// MCP server and no allow rule of its own; whether Codex stays Brakeman's
// agent on those terms is the owner's decision (PR-3c's Validate, Settings).
const codexReadOnlyApprovals = `approval_policy={granular={sandbox_approval=false,rules=false,request_permissions=false,mcp_elicitations=true}}`

// codexMCPEnv is the TOML list of the variables Codex passes on to
// Conductor's MCP server (#354): every CONDUCTOR_* one a session sets.
const codexMCPEnv = `["CONDUCTOR_SESSION_ID","CONDUCTOR_NOTIFY_URL","CONDUCTOR_NOTIFY_TOKEN","CONDUCTOR_BIN","CONDUCTOR_CREW","CONDUCTOR_RUN","CONDUCTOR_MEMBER","CONDUCTOR_TASK","CONDUCTOR_ATTEMPT","CONDUCTOR_TASK_FILE","CONDUCTOR_AGENT","CONDUCTOR_SKILL"]`
