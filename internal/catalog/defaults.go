package catalog

// uuidPattern is the shape of a UUID as the agents print it.
const uuidPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

// codexApprovalWords picks the words of Codex's approval dialog out of the
// screen (its rows joined by line breaks): its heading ("Would you like to
// run the following command?", "Do you want to approve network access to
// …?") and, when it shows one, the command, from the first row after the
// heading that begins with "$ " up to the row of the first choice, every
// row of it ("$ curl -sI https://example.com"); a "$ " inside the command
// is part of it.
const codexApprovalWords = `(?ism)((?:would\s+you\s+like|do\s+you\s+want)\s+to\s+[^?]{1,120}\?)(?:.*?^[ \t]*(\$ .*?)\s*^\s*›?\s*1\.\s+yes,\s+proceed)?`

// CodexWorkingTitle matches Codex's window title while a turn runs, with the
// items Conductor's launch asks for (activity, run-state): "⠋ Working",
// "⠋ Thinking" or "⠋ Waiting" (a background terminal), the braille frame
// changing every 100 ms, or the word alone with animations off. Not
// "Starting" (its MCP servers starting), "Ready" (at rest), nor "[ ! ]
// Action Required" and its blink "[ . ] Action Required", which Codex
// writes in place of the spinner and the run state while a view waits on a
// person (tui/src/chatwidget/status_surfaces.rs and
// bottom_pane/action_required_title.rs, Codex 0.161).
const CodexWorkingTitle = `^(?:\S+\s+)?(?:Working|Thinking|Waiting)$`

// claudeDialog finds a Claude Code dialog asked during the work: a row
// whose number the highlight ❯ stands before, then, within its dialog, one
// of the footers Claude Code ends its dialogs with as the last thing the
// screen shows (a dialog is the bottom of what Claude Code draws, its
// prompt box and status line gone while it shows), so that a numbered list
// or a whole dialog quoted in the transcript, or typed into the prompt
// box, is not taken for one. A narrow screen wraps a footer over the rows
// below it ("ctrl+g to edit in Vim · ~/.claude/pla", "ns/plan….md" at 44
// columns), never a rule's or the prompt's.
const claudeDialog = `(?m)^[ \t]*❯[ \t]*[1-9]\.[ \t].*(?:\n.*){0,40}\n[ \t]*(?:Esc to cancel|Enter to select|ctrl\+g to edit)[^\n]*(?:\n[ \t]*[^\s─❯][^\n]*){0,3}\s*\z`

// claudeAnswersReview finds AskUserQuestion's last page, "Ready to submit
// your answers?" with its two choices the last rows the screen shows.
const claudeAnswersReview = `(?m)^[ \t]*Ready to submit your answers\?\n(?:[ \t]*\n){0,2}[ \t]*❯?[ \t]*1\.[ \t][^\n]*\n[ \t]*❯?[ \t]*2\.[ \t][^\n]*\s*\z`

// codexApprovalFooter ends a pattern of Codex's approval dialog: its footer,
// on one of the last rows the screen shows (the dialog is the bottom of
// what Codex draws), so that a dialog's words in a file or a log on the
// screen are not taken for one.
const codexApprovalFooter = `.{0,200}?press\s+enter\s+to\s+confirm\s+or\s+esc\s+to\s+cancel[^\n]*(?:\n[^\n]*){0,3}\z`

// defaults lists the built-in agents: one for each agent Conductor has a hook
// adapter for (internal/agents), with the signal its adapter reports
// through and its website as best known (to be checked in a browser: the
// open verification of round 3 in docs/features.md), then a login shell.
// Operators can override any entry by ID or disable the whole set with
// disableDefaults. Each built-in carries the yolo recipe the research found
// (docs/features.md, the adapter matrix: what each disables, verified live or
// from docs) and, for Claude Code and Codex, the words of its trust question
// as drawn by Claude Code 2.1.287 and Codex 0.159.0.
func defaults() []Agent {
	return []Agent{
		{
			ID:          "claude",
			Name:        "Claude Code",
			Description: "Anthropic's terminal coding agent.",
			Command:     []string{"claude"},
			AllowArgs:   true,
			Icon:        "i-lucide-sparkles",
			Site:        "https://claude.com/claude-code",
			Adapter:     "claude",
			Signal:      &Signal{Kind: SignalHook},
			Yolo:        &Yolo{Args: []string{"--dangerously-skip-permissions"}},
			TrustPrompt: `Is\s*this\s*a\s*project\s*you\s*created\s*or\s*one\s*you\s*trust\?`,
			// Claude Code highlights "No, exit" first: the trusting answer is Down, then Enter (verified in a PTY on 2026-10-03, 2.1.288).
			TrustAnswers: []Answer{{Label: "Yes, I trust this folder", Input: "\x1b[B\r"}, {Label: "No, exit", Input: "\r"}},
			// Its prompt at rest, by its shape (2.1.319 in a PTY, #714): a
			// ❯ row, its text not a numbered choice, right under a rule,
			// a rule under it, and only rows that are no choice below that
			// to the screen's end. The ❯ row's words change (eight "Try …"
			// placeholders in turn, the typed line, none), so they are not
			// matched; a dialog's ❯ row is not between two rules.
			PromptShape: claudePromptShape,
			// Verified live (2.1.287).
			Session: &SessionRecipe{StartArgs: []string{"--session-id", IDArg}, IDFrom: "hook", ResumeArgs: []string{"--resume", IDArg}, IDPattern: uuidPattern},
			// Claude Code's dialogs during the work, as 2.1.304 drew them in
			// a PTY on 2026-10-19 (#288): a tool's permission ("Do you want
			// to proceed?", "Do you want to create c288.txt?"), the plan's
			// approval ("Claude has written up a plan and is ready to
			// execute. Would you like to proceed?") and AskUserQuestion's
			// choices, each a numbered list with ❯ on the highlighted row,
			// ending in "Esc to cancel · Tab to amend", "Enter to select · …
			// · Esc to cancel" or "ctrl+g to edit in <editor> · <plan>". Their
			// choices change with the tool, the permission mode and the
			// release (Bash's 3 was "Yes, and switch to auto mode", Write's 2
			// "Yes, and switch to accept edits"), so they are read off the
			// dialog. A digit picks at once (4 denied the Bash command, 2
			// answered AskUserQuestion's Blue); the plan's "Tell Claude what
			// to change" only takes the highlight into its feedback field.
			// Its last page of AskUserQuestion's ("Ready to submit your
			// answers?", 1. Submit answers, 2. Cancel) has no footer: a
			// question of its own, its list the bottom of the screen. Two
			// choices are fields, whose keys type into them while the
			// highlight is there (seen with 2.1.304): the plan's "Tell
			// Claude what to change" ("❯ 3. abc", its hint "shift+tab to
			// approve with this feedback" below; its digit only takes the
			// highlight there) and AskUserQuestion's "Type something."
			// ("❯ 3. xy", a rule between it and "4. Chat about this",
			// arrows taking the highlight there and "ctrl+g to edit" in
			// the footer then).
			Questions: []Question{{
				Prompt:            claudeDialog,
				Anytime:           true,
				AnswersFromScreen: true,
				ModeAnswers:       `(?i)auto mode|accept edits|auto-accept|bypass permissions`,
				TypedAnswers:      `shift\+tab to approve with this| ─$`,
			}, {
				Prompt:            claudeAnswersReview,
				Anytime:           true,
				AnswersFromScreen: true,
			}, {
				// Signed out, Claude Code does not exit: it opens its
				// sign-in screen and waits there for a browser's sign-in
				// or a code pasted ("Browser didn't open? Use the url below
				// to sign in (c to copy)", the URL, then "Paste code here
				// if prompted >"; 2.1.319 under an empty CLAUDE_CONFIG_DIR,
				// seen by #659's live check on 2026-11-06). A startup
				// screen with no answers: no key of Conductor's passes it,
				// so it holds the session's first line as every startup
				// question does, shows under Needs you with its words, and
				// a Brakeman turn that meets it ends (heldAtStartup).
				Prompt: `(?i)paste\s*code\s*here\s*if\s*prompted`,
				Words:  `(?i)browser\s*didn.t\s*open\?\s*use\s*the\s*url\s*below\s*to\s*sign\s*in`,
			}, {
				// On an account folder never used (2.1.319 under an empty
				// CLAUDE_CONFIG_DIR, seen by #659's third live check on
				// 2026-11-07), Claude Code first asks for a theme, its
				// highlight on one ("Choose the text style that looks best
				// with your terminal", "❯ ✔ Dark mode"), then for a login
				// method ("Select login method:", "❯ 1. Claude account with
				// subscription"), then opens a browser to sign in ("Opening
				// browser to sign in…") before the sign-in screen above.
				// An Enter picks a theme and a login method, and the browser
				// opens: these are startup screens with no answers, as the
				// sign-in screen is: no key of Conductor's is typed, the
				// session shows under Needs you with the screen's words, and
				// a Brakeman turn that meets one ends (heldAtStartup).
				Prompt: `(?i)choose\s*the\s*text\s*style\s*that\s*looks\s*best\s*with\s*your\s*terminal`,
			}, {
				Prompt: `(?i)select\s*login\s*method:`,
			}, {
				Prompt: `(?i)opening\s*browser\s*to\s*sign\s*in`,
			}},
		},
		{
			ID:          "codex",
			Name:        "Codex CLI",
			Description: "OpenAI's terminal coding agent.",
			Command:     []string{"codex"},
			AllowArgs:   true,
			Icon:        "i-lucide-code-xml",
			Site:        "https://learn.chatgpt.com/docs/codex/cli",
			Adapter:     "codex",
			// Its window title says what it does (the launch asks for
			// tui.terminal_title=["activity","run-state"]): a spinner's
			// frame and "Working" while a turn runs, Ready at rest (seen
			// with 0.161 on 2026-10-14; #186).
			Signal:      &Signal{Kind: SignalHook, WorkingTitle: CodexWorkingTitle},
			Yolo:        &Yolo{Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
			TrustPrompt: `Trust\s*this\s*folder\?`,
			// Codex highlights the trusting answer: Enter alone. Codex 0.161's
			// other answer is "2. Quit" (Down, then Enter), which ends Codex:
			// the label says so, as Claude Code's "No, exit" does (seen in the
			// rc.7 app on 2026-10-11).
			TrustAnswers: []Answer{{Label: "Yes, trust this folder", Input: "\r"}, {Label: "No, quit", Input: "\x1b[B\r"}},
			// Its composer at rest, by its shape (0.163.0 in a PTY, #714):
			// a › row whose text is no numbered choice (its dialogs'
			// highlighted row, "› 1. Update now") and not "Waiting for
			// startup" (the composer before its session is configured: RE2
			// has no lookahead, so a text that starts "Wa" is refused),
			// then only rows that are no choice (its status line, "? for
			// shortcuts") to the screen's end.
			PromptShape: codexPromptShape,
			// Codex 0.161 opens on "Hooks need review" whenever its hooks are
			// new or changed (1. Review hooks, highlighted; 2. Trust all and
			// continue; 3. Continue without trusting): a digit moves the
			// highlight and Enter picks it, so a prompt's Enter would open the
			// review. Seen live on 2026-10-09 (round 13, G2c).
			//
			// Seen live with Codex 0.161 on 2026-10-09 beside a 0.162 background
			// server (the desktop's): "Background server has incompatible
			// feature settings" (1. Run without daemon this time, 2. Restart
			// with these settings, 3. Cancel highlighted), where a digit picks
			// at once, so an Enter would cancel and Codex exits; then "Update
			// available" with "1. Update now (runs `npm install -g
			// @openai/codex`)" highlighted, where an Enter installs: both held
			// so a prompt's Enter never lands on them. The update question's
			// answers are its own footer's "esc skip" and "3. Skip until next
			// version": `3` alone picks that at once (verified in a PTY on
			// 2026-10-11 with 0.161.0 and an isolated CODEX_HOME: Codex left the
			// screen and wrote dismissed_version to its version.json; no Enter
			// was ever sent). Its words are its heading, "Update available ·
			// 0.161.0 → 0.162.1", not the highlighted line, which read as if
			// Update had been the answer.
			Questions: []Question{{
				Prompt:  `(?i)hooks\s*n?eed\s*review`,
				Answers: []Answer{{Label: "Trust all and continue", Input: "2\r"}, {Label: "Continue without trusting", Input: "3\r"}},
			}, {
				Prompt:  `(?i)background\s*server\s*has\s*incompatible\s*feature\s*settings`,
				Answers: []Answer{{Label: "Run without the daemon this time", Input: "1"}, {Label: "Cancel (Codex exits)", Input: "3"}},
			}, {
				Prompt:  `(?i)update\s*now\s*\(\s*runs`,
				Words:   `(?i)(update\s*available)\W*?(\d+(?:\.\d+)+\s*(?:→|->)\s*\d+(?:\.\d+)+)`,
				Answers: []Answer{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}},
			}, {
				// Codex 0.161 asks before a command it would run outside its
				// sandbox, at any time in a session (seen in the rc.7 app on
				// 2026-10-11, a crew member running curl): "Would you like to
				// run the following command?", its reason, "$ <command>",
				// then "› 1. Yes, proceed (y)", "2. Yes, and don't ask again
				// for commands that start with `<prefix>` (p)", "3. No, and
				// tell Codex what to do differently (esc)" and "Press enter
				// to confirm or esc to cancel". Each choice's own key picks
				// it at once (a `y` typed there approved the command); an
				// Enter picks the highlighted Yes, so no prompt's Enter may
				// land on it. Codex 0.161's overlay (approval_overlay.rs)
				// also takes a digit, d (deny) where shown, and Esc or
				// Ctrl+C as cancel; the choices here are the keys the dialog
				// shows. Like every anytime question it is read off the
				// screen as drawn; it is found by its choices' keys and its
				// footer on the last rows, its words are the heading and the
				// command.
				Prompt:  `(?is)proceed\s*\(\s*y\s*\).{0,400}?\(\s*p\s*\).{0,400}?\(\s*esc\s*\)` + codexApprovalFooter,
				Words:   codexApprovalWords,
				Anytime: true,
				Answers: []Answer{{Label: "Yes, proceed", Input: "y"}, {Label: "Yes, and don't ask again", Input: "p"}, {Label: "No, tell Codex what to do differently", Input: "\x1b"}},
			}, {
				// The same dialog whose No is "No, continue without running
				// it (d)" (an accept-or-decline request: the deny shortcut
				// of approval_overlay.rs, its test pressing d); Escape, in
				// its footer, cancels it.
				Prompt:  `(?is)proceed\s*\(\s*y\s*\).{0,600}?\(\s*d\s*\)` + codexApprovalFooter,
				Words:   codexApprovalWords,
				Anytime: true,
				Answers: []Answer{{Label: "Yes, proceed", Input: "y"}, {Label: "No, continue without running it", Input: "d"}, {Label: "Cancel", Input: "\x1b"}},
			}, {
				// The same dialog without the "don't ask again" of a prefix
				// (Codex offers it only when it has a prefix to propose):
				// Yes and No alone, whose keys are the same.
				Prompt:  `(?is)proceed\s*\(\s*y\s*\).{0,800}?\(\s*esc\s*\)` + codexApprovalFooter,
				Words:   codexApprovalWords,
				Anytime: true,
				Answers: []Answer{{Label: "Yes, proceed", Input: "y"}, {Label: "No, tell Codex what to do differently", Input: "\x1b"}},
			}, {
				// Any other of Codex's approvals (network access: "Do you
				// want to approve network access to …?", "Yes, just this
				// once (y)"): its y and the footer's Escape, which cancels.
				Prompt:  `(?is)\(\s*y\s*\).{0,800}?` + codexApprovalFooter,
				Words:   codexApprovalWords,
				Anytime: true,
				Answers: []Answer{{Label: "Yes", Input: "y"}, {Label: "Cancel", Input: "\x1b"}},
			}},
			// Verified live (0.159.0); the id is captured, never chosen.
			Session: &SessionRecipe{IDFrom: "hook", IDPolicy: "lowest", ResumeArgs: []string{"resume", IDArg, "-c", `tui.resume_cwd="session"`}, IDPattern: uuidPattern, ResumeNeedsCwd: true},
		},
		{
			ID:          "agy",
			Name:        "Antigravity",
			Description: "Google's Antigravity (Gemini) CLI.",
			Command:     []string{"agy"},
			AllowArgs:   true,
			Icon:        "i-lucide-rocket",
			Site:        "https://antigravity.google",
			Adapter:     "agy",
			// Antigravity fires no event while it waits for the user.
			Signal: &Signal{Kind: SignalBell},
			Yolo:   &Yolo{Args: []string{"--dangerously-skip-permissions"}},
			// Verified live in print mode (1.2.14).
			Session: &SessionRecipe{IDFrom: "hook", ResumeArgs: []string{"--conversation", IDArg}, IDPattern: uuidPattern, ResumeNeedsCwd: true},
		},
		{
			ID:          "copilot",
			Name:        "Copilot CLI",
			Description: "GitHub Copilot's coding agent in the terminal.",
			Command:     []string{"copilot"},
			AllowArgs:   true,
			Icon:        "i-lucide-github",
			Site:        "https://github.com/features/copilot/cli",
			Adapter:     "copilot",
			Signal:      &Signal{Kind: SignalHook},
			Yolo:        &Yolo{Args: []string{"--yolo"}, Env: map[string]string{"COPILOT_ALLOW_ALL": "true"}},
			// Verified live (1.0.59).
			Session: &SessionRecipe{StartArgs: []string{"--session-id", IDArg}, IDFrom: "hook", ResumeArgs: []string{"--session-id", IDArg}, IDPattern: uuidPattern},
		},
		{
			ID:          "cursor",
			Name:        "Cursor CLI",
			Description: "Cursor's coding agent in the terminal.",
			Command:     []string{"cursor-agent"},
			AllowArgs:   true,
			Icon:        "i-lucide-mouse-pointer-2",
			Site:        "https://cursor.com/cli",
			Adapter:     "cursor",
			// The CLI fires no event while it waits: its prompt on the last
			// line says so.
			Signal: &Signal{Kind: SignalPattern, Pattern: `^› $`},
			Yolo:   &Yolo{Args: []string{"--yolo", "--trust"}},
			// From docs.
			Session: &SessionRecipe{IDFrom: "hook", ResumeArgs: []string{"--resume", IDArg}, IDPattern: uuidPattern, ResumeNeedsCwd: true},
		},
		{
			ID:          "opencode",
			Name:        "OpenCode",
			Description: "Open source terminal coding agent.",
			Command:     []string{"opencode"},
			AllowArgs:   true,
			Icon:        "i-lucide-braces",
			Site:        "https://opencode.ai",
			Adapter:     "opencode",
			Signal:      &Signal{Kind: SignalHook},
			Yolo:        &Yolo{Args: []string{"--auto"}},
		},
		{
			ID:          "pi",
			Name:        "pi",
			Description: "Minimal, extensible terminal coding agent.",
			Command:     []string{"pi"},
			AllowArgs:   true,
			Icon:        "i-lucide-pi",
			Site:        "https://github.com/earendil-works/pi",
			Adapter:     "pi",
			Signal:      &Signal{Kind: SignalHook},
			// From source and docs.
			Session: &SessionRecipe{StartArgs: []string{"--session-id", IDArg}, ResumeArgs: []string{"--session-id", IDArg}, IDPattern: `^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`, ResumeNeedsCwd: true},
		},
		{
			ID:          "omp",
			Name:        "oh-my-pi",
			Description: "Coding agent built on pi.",
			Command:     []string{"omp"},
			AllowArgs:   true,
			Icon:        "i-lucide-pi-square",
			Site:        "https://github.com/can1357/oh-my-pi",
			Adapter:     "omp",
			Signal:      &Signal{Kind: SignalHook},
			Yolo:        &Yolo{Args: []string{"--yolo"}},
		},
		{
			ID:          "aider",
			Name:        "aider",
			Description: "AI pair programming in the terminal.",
			Command:     []string{"aider"},
			AllowArgs:   true,
			Icon:        "i-lucide-git-commit",
			Site:        "https://aider.chat",
			Adapter:     "aider",
			Signal:      &Signal{Kind: SignalHook},
			Yolo:        &Yolo{Args: []string{"--yes-always"}},
		},
		{
			ID:          "goose",
			Name:        "Goose",
			Description: "Open source AI agent from Block.",
			Command:     []string{"goose"},
			AllowArgs:   true,
			Icon:        "i-lucide-feather",
			Site:        "https://goose-docs.ai",
			Adapter:     "goose",
			// Goose fires no event while it waits for the user.
			Signal: &Signal{Kind: SignalBell},
			Yolo:   &Yolo{Env: map[string]string{"GOOSE_MODE": "auto"}},
			// From source and docs.
			Session: &SessionRecipe{StartArgs: []string{"session", "--name", IDArg}, NewID: "name", ResumeArgs: []string{"session", "--resume", "--name", IDArg}, IDPattern: `^cdr-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, ResumeNeedsCwd: true},
		},
		{
			ID:          "amp",
			Name:        "Amp",
			Description: "Amp's coding agent in the terminal.",
			Command:     []string{"amp"},
			AllowArgs:   true,
			Icon:        "i-lucide-zap",
			Site:        "https://ampcode.com",
			Adapter:     "amp",
			Signal:      &Signal{Kind: SignalHook},
			// Amp leaves the flag out of its --help but takes it; its own
			// refusals say "Rerun with --dangerously-allow-all".
			Yolo: &Yolo{Args: []string{"--dangerously-allow-all"}},
		},
		{
			ID:          "dsh",
			Name:        "DeepSeek Harness",
			Description: "DeepSeek's agent harness (developer preview).",
			Command:     []string{"dsh"},
			AllowArgs:   true,
			Icon:        "i-lucide-cpu",
			Site:        "https://github.com/deepseek-ai/deepseek-harness",
			Adapter:     "dsh",
			// Its plugin is installed by hand, if at all: nothing is assumed.
			Signal: &Signal{Kind: SignalNone},
			Yolo:   &Yolo{Env: map[string]string{"DSH_PERMISSION_MODE": "danger-full-access"}},
		},
		{
			ID:          "shell",
			Name:        "Shell",
			Description: "Interactive login shell.",
			Command:     []string{"/bin/bash", "-l"},
			AllowArgs:   false,
			Icon:        "i-lucide-terminal",
			// Readline rings the bell on a failed completion or a backspace
			// at the start of the line: no call for attention while a person
			// types.
			Signal: &Signal{Kind: SignalNone},
		},
	}
}

// The built-in agents' prompt shapes (Agent.PromptShape). A text or a row
// that is no choice begins with neither a highlight's marker nor a number
// and a dot ("2. Skip"); an empty row is none either. Every row a shape
// reads, the ones inside Claude Code's box as the ones under it, is such a
// row, and a screen with a numbered choice anywhere on it is still not the
// prompt (the session's atPrompt asks both). The session matches a shape
// on the screen with every Unicode space a plain one (Claude Code's no-break
// space after its marker included), so a shape names spaces as " " and \s.
const (
	claudePromptShape = `(?m)^─{8,}\n❯ *(?:(?:[^\d\s]|\d+[^\d.\n])[^\n]*)?(?:\n *(?:(?:[^\d\s─❯›>]|\d+[^\d.\n])[^\n]*)?){0,8}\n─{8,}(?:\n *(?:(?:[^\d\s❯›>]|\d+[^\d.\n])[^\n]*)?){0,8}\s*\z`
	codexPromptShape  = `(?m)^ {0,3}› +(?:[^\d\sW]|W[^a\n]|\d+[^\d.\n])[^\n]*(?:\n *(?:(?:[^\d\s❯›>]|\d+[^\d.\n])[^\n]*)?){1,6}\s*\z`
)
