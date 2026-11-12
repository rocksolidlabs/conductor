#!/usr/bin/env bash
# stub-agent.sh stands in for Claude Code, Codex and Goose in the e2e suite
# (web/e2e/conductor.e2e.json gives those ids this script, with the real
# agents' recipes). It never runs what it reads. Each line typed into it (a
# role prompt, a handoff, a broadcast, a person's words) is echoed; in it,
# only the merges the example crews ask for are recognised, by fixed
# patterns, for branches of this run alone (crew/<run>/<member>, both ids
# checked), and run as git's argv. Then it commits a file of its own on its
# branch and reports done the way its agent does. It turns bracketed paste
# on, as the agents do, and takes the paste markers off what it reads.
#
# What the environment chooses:
#   STUB_IDENTITY   claude | codex | pressly-goose | stub: what --version prints
#   STUB_REPORT     plain | claude | codex: notify's words, or the agent's
#                   hook payloads (Claude Code's UserPromptSubmit and Stop,
#                   Codex's agent-turn-complete with the thread id)
#   STUB_CODEX_TITLE=1   a Codex title-thread turn is reported before the
#                   answer, with a higher thread id, as Codex 0.159 does
#   STUB_TRUST_DIALOG=1  "Trust this folder?" is drawn first, as Codex
#                   0.161 draws it (1. Trust and continue, 2. Quit), and
#                   answered by Enter alone, which writes the trust into
#                   $HOME/.codex/config.toml; Down then Enter, or Esc,
#                   quits; a launch with -c projects=… (the yolo trust
#                   override) asks nothing
#   STUB_CHOICES    a|b|c: the stub asks STUB_ASK (else "Which one?") with
#                   these as choices (notify --choices) before its prompt;
#                   the next line typed answers it
#   STUB_BRAKEMAN=1 the stub is Brakeman's agent: on the line a turn types
#                   ("Brakeman request br-n (<kind>): …") it reads its request
#                   and the changes, waits for the file STUB_BRAKEMAN_WAIT
#                   names when one is named (a spec's gate; none if it never
#                   appears), and submits one
#                   brief whose fact cites its own session and run, and the
#                   check receipt STUB_BRAKEMAN_CHECK names (or, once the gate
#                   is open, the brief in the file STUB_BRAKEMAN_BRIEF names,
#                   when it holds one: a spec's own items, after reading the
#                   ticket STUB_BRAKEMAN_TICKET names, as a turn reads what it
#                   proposes to change); through the CLI, or the MCP tools
#                   with STUB_WORKER=mcp. With STUB_BRAKEMAN_ASK, a wake
#                   request's turn (the background role's) first asks it as
#                   its question (notify --state needs_input), before the gate.
#                   Before the brief, once the gate is open, it runs the act
#                   STUB_BRAKEMAN_ACT's file names when it holds one
#                   ("OPERATION KEY [PARAM]", its body the JSON file beside
#                   it, <file>.body; a run id in <file>.run is read first,
#                   and @REV in the body and the brief is the rev that read
#                   lists for the decision KEY names):
#                   conductor brakeman act, or the act tool;
#                   what the server answered, taken or refused, is written to
#                   <file>.out and the brief is submitted either way
# $HOME/.codex/stub-hooks-review present: "Hooks need review" is drawn at
# start as Codex 0.161 draws it when its hooks are new or changed (round 13,
# G2c): 1. Review hooks highlighted; a digit moves the highlight, Enter
# picks it; 2 trusts ("Hooks trusted."), 3 goes on without ("Hooks off."),
# 1 opens the review ("Reviewing hooks."), which a prompt's Enter would;
# the choice is written to $HOME/.codex/stub-hooks-answer.
# $HOME/.codex/stub-background and $HOME/.codex/stub-update present: Codex
# 0.161's "Background server has incompatible feature settings" (a digit
# picks at once: 1 runs without the daemon, 3 cancels and exits) and then
# its "Update available · 0.161.0 → 0.162.0" (Update now highlighted;
# Enter or 1 would install, Esc or 2 skips, 3 skips until the next version)
# are drawn at start, as seen beside a newer background server; the answers
# are written to $HOME/.codex/stub-background-answer and stub-update-answer
# (update, skip or skip-version).
# A line that is exactly `stub tools codex`, `stub tools copilot`,
# `stub tools agy` or `stub tools goose` makes the stub write stub-tools.txt and report, through
# that agent's hook flag, the payloads a live run of it sends (round 13,
# G2b): a shell `cat README.md` and the write of stub-tools.txt, in the
# shapes captured in internal/notify/testdata.
# A line `stub approve <command>` (letters, digits, spaces and ./:_-) makes
# the stub ask, as Codex 0.161 does before a command outside its sandbox,
# "Would you like to run the following command?" with its three choices
# (y, p, Esc; the digits too; Enter picks Yes) before it answers the line;
# the answer goes into the transcript as "approval: …". It is never run.
# That turn ends as Codex's does while its terminal is not focused: a bell
# after its done ("bell: after done" in the transcript). A line
# `stub approvals <command>|<command>…` asks for each command in turn, the
# next drawn as soon as the one before is answered, as Codex asks the
# approvals it has queued.
# A line `stub plan` makes the stub send the hooks Claude Code sends for its
# plan's approval and draw that dialog as Claude Code 2.1.304 draws it
# (#288): its three choices, "1. Yes, and use auto mode" highlighted; 1 or
# 2 picks at once and goes into the transcript as "plan: 1" or "plan: 2".
# A line `stub open <url>` runs conductor open on the link, and `stub
# browser <url>` runs $BROWSER on it as an agent's own opener does (#436);
# each notes the command's one line in the transcript (open: …, browser: …).
# A line `stub permission` sends the PermissionRequest hook of a Bash
# command and draws no dialog, a permission whose choices the screen does
# not show; the next key goes into the transcript as "permission: <key>".
# A line `stub spin N` (N seconds, 1 to 99) makes the stub draw a dot every
# 100 ms for about N seconds and answer nothing, as an agent draws while it
# works: what is typed meanwhile waits (the terminal echoes it, or the stub
# in its TUI mode), and a submission's Enter waits for a quiet screen as
# long as Conductor's look does. It reports nothing and goes into no
# transcript; $HOME/.stub-sessions/<id>.spin says "spinning" from its first
# dot and "done" after its last, and anything else written into it (a
# test's "stop") ends the spin at the next dot: a test ends it once what
# it does meanwhile is done, rather than guessing how long that takes on a
# slow machine.
# A line `stub tui on` makes the stub read as Claude Code's and Codex's
# TUIs do from then on: any-event mouse tracking on (SGR reports: the
# pointer over the terminal sends INPUT), and the terminal's own line
# editing off, the stub reading key by key, also while it spins. It echoes
# what is typed and keeps the line not yet entered in
# $HOME/.stub-sessions/<id>.line; Ctrl+U clears it, Backspace erases, the
# terminal's reports, the mouse's and the paste markers are dropped (the
# mouse reports read counted in <id>.mouse), and Enter answers the line
# (one entered while it spins once it is done), each Enter taken appended
# to <id>.entered as a line "<when, in microseconds since the epoch>
# <the line>". The files let a test read what the stub holds whatever the
# browser draws the terminal with: with WebGL the screen is a canvas and
# holds no text. .line and .mouse are appended a record a line, the last
# whole one what is now; .spin is written whole (a new file renamed over
# it): a reader never sees one half written.
# Launched with -c tui.terminal_title=["activity","run-state"] (Conductor's
# Codex launch), the stub writes its window title as Codex 0.161 does
# (ESC ] 0 ; title BEL, seen on 2026-10-14): "Ready" at rest, a braille
# frame and "Working" while it answers a line or spins, the frame changing
# every 100 ms, and "[ ! ] Action Required", blinking with "[ . ] Action
# Required" every second, while a dialog waits on a person (`stub approve`)
# or an async question is pending. With STUB_TITLE_STILL=1, or -c
# tui.animations=false, it writes "Working" once a turn and no frames, as
# Codex with animations off. Two lines are Codex's questions while it
# works: `stub ask <question>` asks it as the agent's own (notify --state
# needs_input) and spins (as `stub spin 30`, ended by a test's "stop");
# `stub async <question>` draws Codex's "? 1 question" under its status row
# (request_user_input_async), rings the bell only when the launch's
# tui.notifications names async-question, says Action Required in its title
# while the question is pending (STUB_ASYNC_S seconds, 5 by default; Codex's
# resolves by itself after 30 s), writes "resolved <microseconds since the
# epoch>" to $HOME/.stub-sessions/<id>.async as it resolves, says Working
# again and spins; `stub async-ask <question>` also asks it as the agent's
# own while it is pending. `stub ask-wait <question>` asks it marked as the
# agent's own question (notify --ask, #193), spins as `stub ask` does, and
# then ends its turn as its agent's hooks do (STUB_REPORT): the question
# outlasts that done. `stub bell <seconds>` rings the bell and draws nothing
# for that many seconds (a wait with no words), then prints four lines (the
# agent moving on: the wait is withdrawn by the screen), pauses two seconds
# and spins as `stub spin 30` does.
# The agent's session: --session-id ID names it, --resume ID (or a leading
# `resume ID`) continues it; without either a fresh id is made. The
# transcript lives in $HOME/.stub-sessions/<id>.txt, so a resumed session
# prints what was typed into it before; with CLAUDE_CONFIG_DIR set it begins
# with "env: CLAUDE_CONFIG_DIR=<dir>" (a copy of the agent, #291).
set -euo pipefail

identity=${STUB_IDENTITY:-stub}
report_mode=${STUB_REPORT:-plain}
member=${CONDUCTOR_MEMBER:-agent}
run=${CONDUCTOR_RUN:-}
conductor=${CONDUCTOR_BIN:-conductor}
wait_s=${STUB_MERGE_WAIT_S:-60}

# Usage (Program item AC1, docs/plans/capacity.md, PR-5): what Claude Code's
# `auth status --json` and `-p /usage …` and Codex's `app-server` answer, as
# the two tools do, from web/e2e/fixtures/usage/ (synthetic: the tools'
# shapes, invented values). The state is read from the file stub-usage in the
# account's folder (CLAUDE_CONFIG_DIR for the claude identity, CODEX_HOME for
# codex), one line "<state> [<version>]", so one spec holds several states at
# once over the suite's one server: ok, signed_out, key_paid, turn (/usage
# answered with a model turn), slow (/usage answered once the file
# stub-usage-gate is in the same folder, a spec's gate, waited for at most
# 80 s, under the reader's 90 s) and error (a result line that says the run
# failed; Codex's limits refused). With that file the stub says it is the
# version the readers were checked on (Claude Code 2.1.322, Codex 0.163.0),
# or the one the line names; without it, the older one it always said, which
# the readers read nothing of past --version. Resets are counted from now.
usage_dir=
case $identity in
claude) usage_dir=${CLAUDE_CONFIG_DIR:-} ;;
codex) usage_dir=${CODEX_HOME:-} ;;
esac
usage_state= usage_version=
if [[ -n $usage_dir && -f $usage_dir/stub-usage ]]; then
	read -r usage_state usage_version _ <"$usage_dir/stub-usage" || true
fi
claude_version=2.1.287 codex_version=0.159.0
if [[ -n $usage_state ]]; then
	claude_version=${usage_version:-2.1.322}
	codex_version=${usage_version:-0.163.0}
fi
usage_fixtures=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fixtures/usage

# usage_iso prints a Unix time as RFC 3339 in UTC (GNU date, else BSD's).
usage_iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ; }

# usage_fill prints a fixture with its resets made: the five-hour window's two
# hours and ten minutes from now, the weekly one's five days and an hour.
usage_fill() {
	local now five week text
	now=$(date +%s)
	five=$((now + 7800))
	week=$((now + 435600))
	text=$(<"$usage_fixtures/$1")
	text=${text//@FIVE_HOUR_RESET_S@/$five}
	text=${text//@WEEKLY_RESET_S@/$week}
	text=${text//@FIVE_HOUR_RESET@/$(usage_iso "$five")}
	text=${text//@WEEKLY_RESET@/$(usage_iso "$week")}
	printf '%s\n' "$text"
}

# claude_usage answers `auth status --json` and `-p /usage …` for the state.
claude_usage() {
	case $1/$usage_state in
	auth/signed_out) usage_fill claude-auth-signed-out.json ;;
	auth/key_paid) usage_fill claude-auth-key.json ;;
	auth/*) usage_fill claude-auth.json ;;
	usage/turn) usage_fill claude-usage-turn.jsonl ;;
	usage/error) usage_fill claude-usage-error.jsonl ;;
	usage/slow)
		local i
		for ((i = 0; i < 800; i++)); do
			[[ -e $usage_dir/stub-usage-gate ]] && break
			sleep 0.1
		done
		usage_fill claude-usage.jsonl
		;;
	usage/*) usage_fill claude-usage.jsonl ;;
	esac
}

# codex_app_server answers app-server's JSON-RPC on stdio, a reply by id to
# each request, notifications (no id) unanswered, until stdin ends.
codex_app_server() {
	local line id result
	while IFS= read -r line; do
		[[ $line =~ \"id\":([0-9]+) ]] || continue
		id=${BASH_REMATCH[1]}
		result=
		case $line in
		*'"method":"initialize"'*) result=$(usage_fill codex-initialize.json) ;;
		*'"method":"account/read"'*)
			case $usage_state in
			signed_out) result=$(usage_fill codex-account-signed-out.json) ;;
			key_paid) result=$(usage_fill codex-account-key.json) ;;
			*) result=$(usage_fill codex-account.json) ;;
			esac
			;;
		*'"method":"account/rateLimits/read"'*) [[ $usage_state == error ]] || result=$(usage_fill codex-limits.json) ;;
		esac
		if [[ -n $result ]]; then
			printf '{"id":%s,"result":%s}\n' "$id" "$result"
		else
			printf '{"id":%s,"error":{"code":-32603,"message":"e2e stub refusal"}}\n' "$id"
		fi
	done
}

case "${1:-}" in
--version | -V)
	case $identity in
	claude) echo "$claude_version (Claude Code)" ;;
	codex) echo "codex-cli $codex_version" ;;
	pressly-goose) echo "goose version: v3.22.1" ;;
	*) echo "conductor e2e stub 2.0" ;;
	esac
	exit 0
	;;
--help | -h)
	cat <<'HELP'
Usage: stub-agent.sh [options] [resume ID]

Options:
  --session-id ID    name the agent's own session
  --resume ID        continue a session
  -c KEY=VALUE       a configuration override (projects=... trusts the folder)
  --settings FILE    extra settings (ignored)
  --dangerously-skip-permissions
  --dangerously-bypass-approvals-and-sandbox
  --version          print the version
HELP
	exit 0
	;;
auth)
	# Claude Code's auth status, for a usage read; nothing is answered without a state.
	[[ $identity == claude && -n $usage_state && ${2:-} == status ]] || exit 1
	claude_usage auth
	exit 0
	;;
-p)
	if [[ $identity == claude && ${2:-} == /usage ]]; then
		[[ -n $usage_state ]] || exit 1
		claude_usage usage
		exit 0
	fi
	;;
app-server)
	[[ $identity == codex && -n $usage_state ]] || exit 1
	codex_app_server
	exit 0
	;;
esac

id_re='^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
run_re='^[a-z0-9][a-z0-9-]{0,40}-[0-9a-f]{8}$'
member_re='^[a-z0-9][a-z0-9._-]{0,39}$'
target_re='(git merge --no-edit|wait until) crew/(\$CONDUCTOR_RUN|[a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9._-]*)'
handoff_re='--event handoff --to ([a-z0-9][a-z0-9._-]{0,39})'
paste_start=$'\e[200~'
paste_end=$'\e[201~'
git_id=(-c user.name=conductor-e2e -c user.email=e2e@conductor.invalid -c commit.gpgsign=false)

[[ $member =~ $member_re ]] || member=agent
[[ $run =~ $run_re ]] || run=
handed_off=
# ring_at_end: the turn's approval was drawn as Codex draws it while its
# terminal is not focused, and the turn ends the same way: a bell after the
# done.
ring_at_end=

# The arguments: the session, the trust override, the yolo flags, the
# title and the notifications Codex is launched with. Anything else is
# taken and ignored, as a flag of the real agent would be.
session_id= resume_id= trusted= yolo= titles= still=${STUB_TITLE_STILL:-} async_bell=
[[ $still == 1 ]] || still=
args=("$@")
i=0
while ((i < ${#args[@]})); do
	a=${args[i]}
	case $a in
	--session-id)
		session_id=${args[i + 1]:-}
		i=$((i + 2))
		continue
		;;
	--resume)
		resume_id=${args[i + 1]:-}
		i=$((i + 2))
		continue
		;;
	resume)
		if ((i == 0)); then
			resume_id=${args[1]:-}
			i=2
			continue
		fi
		;;
	-c)
		case ${args[i + 1]:-} in
		projects=*) trusted=1 ;;
		'tui.terminal_title=["activity","run-state"]') titles=1 ;;
		tui.animations=false) still=1 ;;
		tui.notifications=*'"async-question"'*) async_bell=1 ;;
		esac
		i=$((i + 2))
		continue
		;;
	--settings)
		i=$((i + 2))
		continue
		;;
	--dangerously-skip-permissions | --dangerously-bypass-approvals-and-sandbox | --yolo) yolo=1 ;;
	esac
	i=$((i + 1))
done
[[ -n $session_id && ! $session_id =~ $id_re ]] && session_id=
[[ -n $resume_id && ! $resume_id =~ $id_re ]] && resume_id=

newid() {
	if [[ -r /proc/sys/kernel/random/uuid ]]; then
		tr 'A-F' 'a-f' </proc/sys/kernel/random/uuid
	elif command -v uuidgen >/dev/null 2>&1; then
		uuidgen | tr 'A-F' 'a-f'
	else
		printf '%08x-%04x-4%03x-8%03x-%012x\n' "$RANDOM$RANDOM" "$RANDOM" "$((RANDOM % 4096))" "$((RANDOM % 4096))" "$(date +%s)$RANDOM"
	fi
}
id=${resume_id:-${session_id:-$(newid)}}
# A thread id above the session's for Codex's title thread (lowest wins).
title_id="ffffffff-${id:9}"
[[ $title_id =~ $id_re ]] || title_id=ffffffff-ffff-ffff-ffff-ffffffffffff

transcripts=${HOME:-/tmp}/.stub-sessions
mkdir -p "$transcripts"
transcript=$transcripts/$id.txt
# A configuration directory of the agent's own (a copy of Claude Code under
# its CLAUDE_CONFIG_DIR, #291) is noted in the transcript, so a spec can see
# that the copy's environment reached the agent.
if [[ -n ${CLAUDE_CONFIG_DIR:-} ]]; then
	printf 'env: CLAUDE_CONFIG_DIR=%s\n' "$CLAUDE_CONFIG_DIR" >>"$transcript"
fi

# json_str prints $1 as a JSON string.
json_str() {
	local s=$1
	s=$(printf '%s' "$s" | tr -d '\000-\010\013\014\016-\037')
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\n'/\\n}
	s=${s//$'\t'/\\t}
	printf '"%s"' "$s"
}

# report sends a plain report to Conductor; outside a session it does nothing.
report() { "$conductor" notify "$@" >/dev/null 2>&1 || true; }
# claude_hook sends a Claude Code hook payload.
claude_hook() { printf '%s' "$1" | "$conductor" notify --claude-hook >/dev/null 2>&1 || true; }
# codex_notify sends a Codex notify payload for thread $1 with the answer $2 and the input $3.
codex_notify() { "$conductor" notify --codex "{\"type\":\"agent-turn-complete\",\"thread-id\":\"$1\",\"last-assistant-message\":$(json_str "$2"),\"input-messages\":[$(json_str "$3")]}" >/dev/null 2>&1 || true; }

report_working() {
	case $report_mode in
	claude) claude_hook "{\"hook_event_name\":\"UserPromptSubmit\",\"session_id\":\"$id\"}" ;;
	codex) : ;;
	*) report --state working ;;
	esac
}

# tool_replay writes stub-tools.txt and reports agent $1's hook payloads
# for a shell read of README.md and that write, as a live run sends them.
tool_replay() {
	local dir write read
	dir=$(json_str "$PWD")
	printf 'from the stub\n' >stub-tools.txt
	case $1 in
	codex)
		read="{\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"cat README.md\"},\"cwd\":$dir}"
		write="{\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"apply_patch\",\"tool_input\":{\"command\":$(json_str "*** Begin Patch
*** Add File: $PWD/stub-tools.txt
+from the stub
*** End Patch")},\"cwd\":$dir}"
		;;
	copilot)
		read="{\"toolName\":\"bash\",\"toolArgs\":{\"command\":\"cat README.md\",\"description\":\"Show README\"},\"toolResult\":{\"resultType\":\"success\",\"textResultForLlm\":\"\"},\"cwd\":$dir}"
		write="{\"toolName\":\"create\",\"toolArgs\":{\"path\":$(json_str "$PWD/stub-tools.txt"),\"file_text\":\"from the stub\"},\"toolResult\":{\"resultType\":\"success\",\"textResultForLlm\":\"\"},\"cwd\":$dir}"
		;;
	goose)
		read="{\"event\":\"PostToolUse\",\"tool_name\":\"shell\",\"tool_input\":{\"command\":\"cat README.md\"},\"working_dir\":$dir}"
		write="{\"event\":\"PostToolUse\",\"tool_name\":\"write\",\"tool_input\":{\"path\":\"stub-tools.txt\",\"content\":\"from the stub\"},\"working_dir\":$dir}"
		;;
	agy)
		read="{\"toolCall\":{\"name\":\"run_command\",\"args\":{\"CommandLine\":\"cat README.md\",\"Cwd\":$dir}},\"error\":\"\"}"
		write="{\"toolCall\":{\"name\":\"write_to_file\",\"args\":{\"TargetFile\":$(json_str "$PWD/stub-tools.txt"),\"CodeContent\":\"from the stub\"}},\"error\":\"\"}"
		;;
	esac
	printf '%s' "$read" | "$conductor" notify "--$1-hook" >/dev/null 2>&1 || true
	printf '%s' "$write" | "$conductor" notify "--$1-hook" >/dev/null 2>&1 || true
}

# approval draws Codex 0.161's command approval for the command $1, as the
# owner saw it in the rc.7 app (2026-10-11), with the bell first, as Codex
# rings it while its terminal is not focused, on a cleared screen (the
# overlay takes Codex's view, and goes with the answer), and waits for one
# key: y or 1 approves, p or 2 approves and stops asking for commands that
# start with it, 3 or Esc declines, Enter picks the highlighted Yes;
# anything else (an arrow, a paste, whose letters are not keys) is
# ignored. The answer goes into the
# transcript as "approval: approved", "approval: approved-prefix" or
# "approval: declined".
approval() {
	local cmd=$1 key seq answer= pasted
	# The dialog takes the screen, as Codex's overlay takes its view, and
	# goes with the answer; the title says Action Required meanwhile.
	title_stop
	printf '\a\e[2J\e[H  Would you like to run the following command?\n\n  Environment: local\n\n  Reason: May I run this with network access? The sandboxed attempt failed.\n\n  $ %s\n\n› 1. Yes, proceed (y)\n  2. Yes, and don'"'"'t ask again for commands that start with `%s` (p)\n  3. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel\n' "$cmd" "$cmd"
	title_action
	status_set blocked permission
	while IFS= read -rsn1 key; do
		case $key in
		y | 1 | '' | $'\r') answer=approved ;;
		p | 2) answer=approved-prefix ;;
		3) answer=declined ;;
		$'\e')
			seq=''
			IFS= read -rsn2 -t 0.2 seq || true
			if [[ $seq == '[2' ]]; then
				pasted=''
				while IFS= read -rsn1 key; do
					pasted+=$key
					[[ $pasted == *$'\e[201~' ]] && break
				done
				continue
			fi
			[[ -n $seq ]] && continue
			answer=declined
			;;
		*) continue ;;
		esac
		break
	done
	title_working
	printf 'approval: %s\n' "$answer" >>"$transcript"
	ring_at_end=1
	printf '\e[2J\e[H'
	if [[ $answer == declined ]]; then
		printf '✗ You did not approve %s\n' "$cmd"
	else
		printf '✔ You approved codex to run %s\n' "$cmd"
	fi
}

# claude_plan writes the plan where Claude Code 2.1.304 writes it, under
# the home's .claude/plans (#289), with the PostToolUse hook of that Write,
# then sends the hooks Claude Code sends for its plan's approval
# (PreToolUse, then PermissionRequest, of ExitPlanMode, the plan's path as
# planFilePath) and draws the dialog as it drew it in a PTY on 2026-10-19
# (#288), its rules cut to fit and its question on two rows: "❯ 1. Yes,
# and use auto mode", "2. Yes, manually approve edits", "3. Tell Claude
# what to change", its hint, then "ctrl+g to edit in Vim · <plan>" as the
# last row. It waits for one key: 1 or 2 picks at once, Escape declines;
# any other key, a paste included, is ignored (3 only takes Claude Code's
# highlight into its feedback field). The key goes into the transcript as
# "plan: 1", "plan: 2" or "plan: esc".
claude_plan() {
	local key seq rule dash pasted plan
	plan="$HOME/.claude/plans/plan.md"
	mkdir -p "$HOME/.claude/plans"
	printf '# Plan\n\nCreate hello.txt containing hi.\n' >"$plan"
	claude_hook "{\"hook_event_name\":\"PostToolUse\",\"tool_name\":\"Write\",\"session_id\":\"$id\",\"tool_input\":{\"file_path\":$(json_str "$plan"),\"content\":\"# Plan\\n\\nCreate hello.txt containing hi.\\n\"},\"cwd\":$(json_str "$PWD")}"
	claude_hook "{\"hook_event_name\":\"PreToolUse\",\"tool_name\":\"ExitPlanMode\",\"session_id\":\"$id\"}"
	claude_hook "{\"hook_event_name\":\"PermissionRequest\",\"tool_name\":\"ExitPlanMode\",\"session_id\":\"$id\",\"tool_input\":{\"plan\":\"Create hello.txt containing hi.\",\"planFilePath\":$(json_str "$plan")}}"
	rule=$(printf '─%.0s' {1..40})
	dash=$(printf '╌%.0s' {1..40})
	printf '\e[2J\e[H  %s\n   Ready to code?\n\n   Here is Claude'"'"'s plan:\n  %s\n   Create hello.txt containing hi.\n  %s\n\n  %s\n   Claude has written up a plan and is ready to execute.\n   Would you like to proceed?\n\n   ❯ 1. Yes, and use auto mode\n     2. Yes, manually approve edits\n     3. Tell Claude what to change\n        shift+tab to approve with this feedback\n\n   ctrl+g to edit in Vim · ~/.claude/plans/plan.md' "$rule" "$dash" "$dash" "$rule"
	while IFS= read -rsn1 key; do
		case $key in
		1 | 2) break ;;
		$'\e')
			seq=''
			IFS= read -rsn2 -t 0.2 seq || true
			if [[ $seq == '[2' ]]; then
				pasted=''
				while IFS= read -rsn1 key; do
					pasted+=$key
					[[ $pasted == *$'\e[201~' ]] && break
				done
				continue
			fi
			[[ -n $seq ]] && continue
			key=esc
			break
			;;
		*) continue ;;
		esac
	done
	printf 'plan: %s\n' "$key" >>"$transcript"
	printf '\e[2J\e[H● User answered Claude'"'"'s plan: %s\n' "$key"
}

# claude_permission sends the PermissionRequest hook Claude Code sends for
# a Bash command and draws no dialog: a permission whose choices the screen
# does not show (#288). The next key goes into the transcript as
# "permission: <key>".
claude_permission() {
	local key
	claude_hook "{\"hook_event_name\":\"PermissionRequest\",\"tool_name\":\"Bash\",\"session_id\":\"$id\",\"tool_input\":{\"command\":\"touch probe.txt\"}}"
	printf 'Bash wants to run touch probe.txt\n'
	IFS= read -rsn1 key
	printf 'permission: %s\n' "$key" >>"$transcript"
}

# The window title, as Codex 0.161 writes it with the launch's
# tui.terminal_title (titles set): title_pid is the loop that animates it,
# title_runs counts the loops started, and <id>.title names the one that
# may write (put), "stop" none.
title_pid= title_runs=0

# title_loop writes the title of mode $1 while <id>.title names loop $2,
# checked before each write: working, a braille frame and "Working" every
# 100 ms; action, "[ ! ] Action Required" and "[ . ] Action Required" in
# turn, every second.
title_loop() {
	local -a frames=(⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏ ⠋)
	local i=0 st
	while :; do
		st=
		IFS= read -r st <"$transcripts/$id.title" || true
		[[ $st == "$2" ]] || return 0
		if [[ $1 == working ]]; then
			printf '\e]0;%s Working\a' "${frames[i % 10]}"
		elif ((i % 10 == 0)); then
			if ((i % 20 == 0)); then
				printf '\e]0;[ ! ] Action Required\a'
			else
				printf '\e]0;[ . ] Action Required\a'
			fi
		fi
		i=$((i + 1))
		sleep 0.1
	done
}

# title_start starts a loop of mode $1 in the background.
title_start() {
	title_runs=$((title_runs + 1))
	put "$transcripts/$id.title" "$title_runs"$'\n'
	title_loop "$1" "$title_runs" &
	title_pid=$!
}

# title_stop ends the loop and returns once it has written its last.
title_stop() {
	if [[ -n $title_pid ]]; then
		put "$transcripts/$id.title" $'stop\n'
		wait "$title_pid" 2>/dev/null || true
		title_pid=
	fi
}

# title_working says the stub works: frames, or "Working" once (still), and
# the status working.
title_working() {
	status_set working
	[[ -n $titles ]] || return 0
	title_stop
	if [[ -n $still ]]; then
		printf '\e]0;Working\a'
		return 0
	fi
	title_start working
}

# title_action says a person is needed: Action Required, blinking (once,
# still).
title_action() {
	[[ -n $titles ]] || return 0
	title_stop
	if [[ -n $still ]]; then
		printf '\e]0;[ ! ] Action Required\a'
		return 0
	fi
	title_start action
}

# title_ready says the stub is at rest: idle, or done ($1) at a turn's end.
title_ready() {
	status_set "${1:-idle}"
	[[ -n $titles ]] || return 0
	title_stop
	printf '\e]0;Ready\a'
}

# The program status, as Codex writes it (OSC 7501, ST-terminated, #197):
# "state=S:app=codex" and, from 0.164, ":kind=K" with blocked. Written with
# the launch's titles (a Codex launch), once per change, as Codex's cache
# has it. status_mode is 0.164 (the default: working through an async
# question, blocked with its kind for a form), 0.163 (no kind, and an async
# question pending reads blocked, as that version has it) or none; `stub
# status MODE` sets it for the session (<id>.status), STUB_STATUS the
# default.
status_mode=${STUB_STATUS:-0.164} status_last=
status_set() {
	[[ -n $titles ]] || return 0
	local mode=$status_mode seq
	if [[ -s $transcripts/$id.status ]]; then
		IFS= read -r mode <"$transcripts/$id.status" || true
	fi
	[[ $mode == none ]] && return 0
	seq="state=$1:app=codex"
	if [[ $1 == blocked && -n ${2:-} && $mode != 0.163 ]]; then
		seq+=":kind=$2"
	fi
	[[ $seq == "$status_last" ]] && return 0
	status_last=$seq
	printf '\e]7501;%s\e\\' "$seq"
}

# status_async_pending is the status while an async question is pending:
# 0.163 reads blocked (no kind), 0.164 goes on working.
status_async_pending() {
	local mode=$status_mode
	if [[ -s $transcripts/$id.status ]]; then
		IFS= read -r mode <"$transcripts/$id.status" || true
	fi
	[[ $mode == 0.163 ]] && status_set blocked
	return 0
}

# blocking_form draws a request_user_input form for the question $1, as
# Codex 0.164 shows one, which no catalog pattern knows: its status alone
# says blocked:question until Enter answers it (other keys are ignored).
blocking_form() {
	title_stop
	printf '\e[2J\e[H  %s\n\n  Type your answer and press enter to continue\n' "$1"
	title_action
	status_set blocked question
	local key
	while IFS= read -rsn1 key; do
		[[ -z $key || $key == $'\r' ]] && break
	done
	printf 'form: answered\n' >>"$transcript"
	title_working
	printf '\e[2J\e[H'
}

# sign_in draws a sign-in prompt no catalog pattern knows: blocked:auth
# until any key.
sign_in() {
	title_stop
	printf '\e[2J\e[H  Sign in to continue\n\n  Press any key to open the browser\n'
	title_action
	status_set blocked auth
	local key
	IFS= read -rsn1 key || true
	printf 'auth: signed in\n' >>"$transcript"
	title_working
	printf '\e[2J\e[H'
}

# async_question draws the question $1 Codex asks while it works
# (request_user_input_async) as Codex 0.161 shows it, under its status row,
# with the bell only when the launch's notifications name async-question;
# its title says Action Required while the question is pending (STUB_ASYNC_S
# seconds), its status row redrawn meanwhile. With $2 = ask the agent asks
# its own question then too. The question resolves by itself: noted with
# its time in <id>.async, the title says Working again, and the stub spins.
async_question() {
	local q=$1 n=${STUB_ASYNC_S:-5} k
	[[ $n =~ ^[1-9][0-9]?$ ]] || n=5
	title_stop
	if [[ -n $async_bell ]]; then
		printf '\a'
	fi
	printf '\n  ? 1 question \xc2\xb7 0:%02d  %s\n' "$n" "$q"
	title_action
	status_async_pending
	if [[ ${2:-} == ask ]]; then
		report --state needs_input --message "$q"
	fi
	for ((k = 0; k < n * 10; k++)); do
		printf .
		sleep 0.1
	done
	now_us
	printf 'resolved %s\n' "$us" >>"$transcripts/$id.async"
	printf '\n  question resolved\n'
	title_working
	spin 30
}

# report_done reports the answer $1 to the line $2.
report_done() {
	case $report_mode in
	claude) claude_hook "{\"hook_event_name\":\"Stop\",\"session_id\":\"$id\",\"last_assistant_message\":$(json_str "$1")}" ;;
	codex)
		if [[ ${STUB_CODEX_TITLE:-} == 1 ]]; then
			codex_notify "$title_id" "Fix the todo app" "Generate a concise, single-line task title for the following conversation: $2"
		fi
		codex_notify "$id" "$1" "$2"
		;;
	*) report --state done --message "$1" ;;
	esac
}

# holds reports whether branch $1 has stub-$2.txt, the file member $2 commits.
holds() { git cat-file -e "$1:stub-$2.txt" 2>/dev/null; }

# The stub as a worker of a managed run (STUB_WORKER=cli or mcp): the task
# line Conductor types names the task and the attempt; the stub reads the
# task (conductor task context, or the task_context tool of conductor mcp
# on stdio), does it, and submits the result (conductor task result, or
# task_result). A work task commits stub-<member>.txt (with the first line
# of .conductor/ticket.md in it when the run was launched from a ticket),
# and stub-fixed.txt too once a finding or an attempt before asks for a
# change, a person's request among them (a repair whose task file says
# who "asked for changes to the commit", RV1), as a review task's findings
# do, and then names each change asked for (by its id) as addressed in its
# result. Under STUB_REVIEW_EDITS=1 its edits are fixed for the located
# states a person's review reads: the first attempt also writes
# stub-review.txt, eight numbered lines, and an attempt that takes up a
# person's request inserts two lines at the top of stub-review.txt and
# rewrites the first line of stub-<member>.txt, nothing else of either; a
# review says
# needs_changes with one finding on its first attempt and approved after.
# What it reads and what the server answered go into the transcript.
# With STUB_DECISION=approval or clarification, a work task's first
# attempt asks the operator one durable decision before it commits
# (decision_request through conductor mcp, or conductor task decide under
# STUB_WORKER=cli), writes its id into stub-decision.txt and ends its turn;
# on the line Conductor types once it is answered ("Decision dr-N (TYPE)
# answered by …") it writes that line into stub-answer.txt and goes on:
# both files are committed with stub-<member>.txt and the result submitted.
# STUB_DECISION_EXIT=1 makes it exit right after asking, so its attempt
# ends while it waits; an attempt after it (a continuation's) whose task
# carries the decision's answer as applied asks nothing: it writes that
# line of its task into stub-answer.txt and goes on as above.
# STUB_DECISION_EVIDENCE is the request's evidence refs, space-separated,
# each `{run}` in them the run's id and `{session}` the attempt's session's.
# STUB_DECISION_FROM=N asks it only from attempt N on: the attempts before
# commit and submit as a worker does, so a check of theirs can be cited.

# mcp_call runs one tool through conductor mcp and prints its reply line.
mcp_call() {
	printf '%s\n%s\n%s\n' \
		'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stub","version":"1"}}}' \
		'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
		"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" |
		"$conductor" mcp 2>/dev/null | grep -F '"id":3' | head -1
}

# task_read prints the task as the stub reads it (the CLI's text, or the
# tool's reply line, JSON-escaped: the words looked for survive both).
task_read() {
	case ${STUB_WORKER:-} in
	mcp) mcp_call task_context '{}' ;;
	*) "$conductor" task context 2>&1 ;;
	esac
}

# task_submit submits a result: $1 is the JSON body for the tool, the rest
# the flags for the CLI. Prints the reply and records it.
task_submit() {
	local body=$1 reply
	shift
	case ${STUB_WORKER:-} in
	mcp) reply=$(mcp_call task_result "$body") ;;
	*) reply=$("$conductor" task result "$@" 2>&1) ;;
	esac
	printf 'submit: %s\n' "${reply:0:300}" >>"$transcript"
	printf 'stub: submit -> %s\n' "${reply:0:200}"
}

# task_attempt does the attempt the line names: $1 task or review, $2 the
# task id, $3 the attempt number.
task_attempt() {
	local kind=$1 task=$2 n=$3 ctx
	ctx=$(task_read)
	printf 'task: %s %s attempt %s: read %s bytes\n' "$kind" "$task" "$n" "${#ctx}" >>"$transcript"
	if [[ $kind == task && -n ${STUB_DECISION:-} && -z $decided && $n -ge ${STUB_DECISION_FROM:-1} ]]; then
		local applied
		applied=$(decision_in_task "$task" "$ctx")
		if [[ -n $applied ]]; then
			# An attempt after the one that asked (a continuation's): the answer is in its task, as read through the task route; it is
			# kept, and the attempt goes on to commit and submit.
			printf '%s\n' "$applied" >stub-answer.txt
			printf '%s\n' "${applied%% *}" >stub-decision.txt
			printf 'decision: in the task: %s\n' "${applied:0:300}" >>"$transcript"
			decided=1
		else
			# STUB_DECISION: ask the operator first, end the turn, and go on once the answer is typed (answer, below); with
			# STUB_DECISION_EXIT=1 the stub exits instead, so its attempt ends while it waits.
			stub_decision "$task" "$n"
			if [[ ${STUB_DECISION_EXIT:-} == 1 ]]; then
				printf 'decision: asked; exiting while it waits\n' >>"$transcript"
				exit 0
			fi
			return
		fi
	fi
	if [[ $kind == task && $ctx == *"STUB DECIDE"* ]]; then
		# Its instructions ask the operator first: a durable decision, and nothing submitted while it waits.
		stub_decide "$task"
		return
	fi
	if [[ $kind == task ]]; then
		printf 'attempt %s of %s by %s\n' "$n" "$task" "$member" >>"stub-$member.txt"
		if [[ -f .conductor/ticket.md ]]; then
			# A run launched from a ticket: the package's first line (the ticket's key and title as launched) goes into the commit, so a
			# spec sees that the package reached the attempt.
			printf 'ticket: %s\n' "$(head -n 1 .conductor/ticket.md)" >>"stub-$member.txt"
		fi
		git add -- "stub-$member.txt" >/dev/null 2>&1 || true
		if [[ -n $decided ]]; then
			git add -- stub-decision.txt stub-answer.txt >/dev/null 2>&1 || true
		fi
		if [[ -n ${STUB_REVIEW_EDITS:-} && $n == 1 && ! -f stub-review.txt ]]; then
			# The fixed edits a person's review reads (STUB_REVIEW_EDITS=1, RV1): eight numbered lines to put notes on.
			printf 'review line %s\n' 1 2 3 4 5 6 7 8 >stub-review.txt
			git add -- stub-review.txt >/dev/null 2>&1 || true
		fi
		local addressed=() changes=() id
		if [[ $ctx == *"asked for changes to the commit"* ]]; then
			printf 'repair: %s attempt %s takes up a request for changes\n' "$task" "$n" >>"$transcript"
			if [[ -n ${STUB_REVIEW_EDITS:-} ]]; then
				# Two lines at the top of stub-review.txt (a note on its lines 3-4 is then "moved: now 5-6") and the first line of
				# stub-<member>.txt rewritten (a note on it is then "the lines changed"), and nothing else of either file.
				if [[ -f stub-review.txt ]]; then
					{ printf 'inserted by attempt %s\n' "$n" "$n"; cat stub-review.txt; } >stub-review.txt.new && mv stub-review.txt.new stub-review.txt
					git add -- stub-review.txt >/dev/null 2>&1 || true
				fi
				{ printf 'the first line, rewritten by attempt %s\n' "$n"; tail -n +2 "stub-$member.txt"; } >"stub-$member.txt.new" && mv "stub-$member.txt.new" "stub-$member.txt"
				git add -- "stub-$member.txt" >/dev/null 2>&1 || true
			fi
			# Each change asked for, by its id (a change has a severity; a question or a note has none), named as addressed.
			local re='`([0-9a-f]{16})` [(](blocker|major|minor|nit)[)]' rest=$ctx
			while [[ $rest =~ $re ]]; do
				id=${BASH_REMATCH[1]}
				[[ " ${changes[*]} " == *" $id "* ]] || changes+=("$id")
				rest=${rest#*"${BASH_REMATCH[0]}"}
			done
			for id in ${changes[@]+"${changes[@]}"}; do
				addressed+=(--addressed "$id=done in attempt $n")
			done
		fi
		if [[ $ctx == *"Findings to address"* || $ctx == *"asked for changes to the commit"* || $ctx == *"The attempt before"* || $n -ge 2 ]]; then
			printf 'fixed in attempt %s\n' "$n" >stub-fixed.txt
			git add -- stub-fixed.txt >/dev/null 2>&1 || true
		fi
		git "${git_id[@]}" commit -q -m "stub: $member does $task, attempt $n" >/dev/null 2>&1 || true
		if [[ -n ${STUB_CHECKPOINT:-} ]]; then
			# Where it got to, tied to the commit just made (STUB_CHECKPOINT=1).
			"$conductor" task checkpoint --done "stub-$member.txt written" --remaining "nothing" --notes "attempt $n" >/dev/null 2>&1 || true
		fi
		local addressedJSON='' sep=''
		for id in ${changes[@]+"${changes[@]}"}; do
			addressedJSON+="$sep{\"id\":\"$id\",\"note\":\"done in attempt $n\"}"
			sep=,
		done
		task_submit "{\"summary\":\"stub $member did $task, attempt $n\",\"issues\":[\"none\"]${addressedJSON:+,\"addressed\":[$addressedJSON]}}" --summary "stub $member did $task, attempt $n" --issue none ${addressed[@]+"${addressed[@]}"}
		if [[ -n ${STUB_MESSAGE_TO:-} ]]; then
			# A message to the member named (STUB_MESSAGE_TO), as a worker tells the next what it needs to know.
			"$conductor" task send --to "$STUB_MESSAGE_TO" "stub $member says: $task is submitted, attempt $n" >/dev/null 2>&1 || true
		fi
	elif ((n >= 2)); then
		task_submit '{"verdict":"approved","summary":"fine now"}' --verdict approved --summary "fine now"
	else
		task_submit "{\"verdict\":\"needs_changes\",\"summary\":\"one thing\",\"findings\":[{\"severity\":\"major\",\"path\":\"stub-$member.txt\",\"line\":1,\"message\":\"add stub-fixed.txt\"}]}" --verdict needs_changes --summary "one thing" --finding "major:stub-$member.txt:1 add stub-fixed.txt"
	fi
}

# decision_in_task prints the line of task $1's read task $2 (the CLI's
# text, or the tool's reply, JSON-escaped) that carries the answer of the
# decision the stub asks (STUB_DECISION) as applied: "dr-N (TYPE), applied:
# QUESTION NAME answered: ANSWER." (the task file's Decisions); nothing
# when it carries none.
decision_in_task() {
	local re='(dr-[1-9][0-9]* [(][a-z]+[)], applied: [^\\]*)' line
	[[ $2 =~ $re ]] || return 0
	line=${BASH_REMATCH[1]}
	line=${line%%$'\n'*}
	if [[ $line == *" $1 "* && $line == *" answered: "* ]]; then
		printf '%s\n' "$line"
	fi
}

# stub_decide asks the operator a durable decision for the task $1 (its
# instructions say STUB DECIDE): a clarification, POSTed over the session's
# own token to the decisions route beside its attention route, as an
# agent's tool asks one; the token goes in a header read from a pipe, never
# in curl's argv. What the server answered goes into the transcript.
stub_decide() {
	local url=${CONDUCTOR_NOTIFY_URL%/attention}/decisions reply
	reply=$(printf 'Authorization: Bearer %s\n' "${CONDUCTOR_NOTIFY_TOKEN:-}" | curl -sS -X POST -H @- -H 'Content-Type: application/json' \
		--data "{\"requestId\":\"stub-$1\",\"type\":\"clarification\",\"question\":\"Which name should $1 take?\"}" "$url" 2>&1) || true
	printf 'decision: %s\n' "$reply" >>"$transcript"
}

# stub_decision asks the operator the durable decision STUB_DECISION names
# for task $1, attempt $2, before anything is committed (so the commit it is
# about, HEAD, stays where it was asked), through decision_request
# (STUB_WORKER=mcp) or conductor task decide --file - (cli); its evidence is
# STUB_DECISION_EVIDENCE's refs, `{run}` the run's id and `{session}` the
# attempt's session's. The decision's id goes
# into stub-decision.txt, and the task and attempt are kept for the answer's
# line (answer). What the server answered goes into the transcript.
decision_task=''
decision_n=''
decided=''
stub_decision() {
	local task=$1 n=$2 body reply ref evidence='' options
	for ref in ${STUB_DECISION_EVIDENCE:-}; do
		ref=${ref//\{run\}/$run}
		ref=${ref//\{session\}/${CONDUCTOR_SESSION_ID:-}}
		evidence+="${evidence:+,}$(json_str "$ref")"
	done
	case $STUB_DECISION in
	approval)
		options='[{"label":"Approve","tradeoffs":"the change goes in as it is"},{"label":"Reject","tradeoffs":"nothing is committed"}]'
		body="{\"requestId\":\"stub-$task-$n\",\"type\":\"approval\",\"question\":$(json_str "May $task commit its change?"),\"context\":$(json_str "The stub is about to commit stub-$member.txt for **$task**.")"
		body+=",\"options\":$options,\"recommendation\":0,\"uncertainty\":\"none: it is a stub\",\"authorizes\":$(json_str "$task commits stub-$member.txt and submits it")"
		;;
	*)
		options='[{"label":"stub-a","tradeoffs":"the short name"},{"label":"stub-b"}]'
		body="{\"requestId\":\"stub-$task-$n\",\"type\":\"clarification\",\"question\":$(json_str "Which name should $task take?"),\"options\":$options,\"recommendation\":0"
		;;
	esac
	body+=",\"evidence\":[$evidence]}"
	case ${STUB_WORKER:-} in
	mcp) reply=$(mcp_call decision_request "$body") ;;
	*) reply=$("$conductor" task decide --file - <<<"$body" 2>&1) ;;
	esac
	printf 'decision: %s\n' "${reply:0:300}" >>"$transcript"
	if [[ $reply =~ (dr-[1-9][0-9]*) ]]; then
		printf '%s\n' "${BASH_REMATCH[1]}" >stub-decision.txt
		decision_task=$task
		decision_n=$n
	fi
}

# brakeman_turn does the Brakeman request $1 (STUB_BRAKEMAN=1): it reads the
# request and what changed (conductor brakeman request and observe changes,
# or the brakeman_request and observe tools), waits for STUB_BRAKEMAN_WAIT's
# file to exist when one is named (at most 15 min, the turn's own default
# deadline: the spec's steps before it opens the gate are its bound, not the
# stub's), then submits a brief whose
# one fact cites the session and the run it runs as (and the check receipt
# STUB_BRAKEMAN_CHECK names) and whose interpretation reads that fact. Only
# the gate's file releases the brief: a gate that never opens ends the turn
# with none (status 1), so a brief never arrives before the spec opened it.
# Both reads must succeed, the request's naming $1, before anything is
# submitted, and the submission must be taken: a read or a submission that
# fails ends the turn with status 1 and its words in brakeman_why. What it
# read and what the server answered go into the transcript.
brakeman_turn() {
	local req=$1 got changes brief reply evidence n=0 ok=1 actrev=
	case ${STUB_WORKER:-} in
	mcp)
		got=$(mcp_call brakeman_request '{}')
		changes=$(mcp_call observe '{"what":"changes"}')
		mcp_ok "$got" || ok=0
		mcp_ok "$changes" || ok=0
		;;
	*)
		got=$("$conductor" brakeman request --json 2>&1) || ok=0
		changes=$("$conductor" brakeman observe changes 2>&1) || ok=0
		;;
	esac
	[[ $got == *"$req"* ]] || ok=0
	printf 'brakeman: %s: read %s bytes of its request and %s of the changes\n' "$req" "${#got}" "${#changes}" >>"$transcript"
	if ((!ok)); then
		printf 'brakeman: %s: a read failed: request %s; changes %s\n' "$req" "${got:0:200}" "${changes:0:200}" >>"$transcript"
		printf 'stub: brief -> none, a read failed\n'
		brakeman_why='a read failed'
		return 1
	fi
	if [[ -n ${STUB_BRAKEMAN_ASK:-} && $got == *'"kind":"wake"'* ]]; then
		# A background turn's question, which pages no one: Needs you lists it.
		report --state needs_input --message "$STUB_BRAKEMAN_ASK"
		printf 'brakeman: %s: asked %s\n' "$req" "$STUB_BRAKEMAN_ASK" >>"$transcript"
	fi
	if [[ -n ${STUB_BRAKEMAN_WAIT:-} ]]; then
		while [[ ! -e $STUB_BRAKEMAN_WAIT ]] && ((n < 9000)); do
			sleep 0.1
			n=$((n + 1))
		done
		if [[ ! -e $STUB_BRAKEMAN_WAIT ]]; then
			printf 'brakeman: %s: the gate %s never opened: no brief\n' "$req" "$STUB_BRAKEMAN_WAIT" >>"$transcript"
			printf 'stub: brief -> none, the gate never opened\n'
			brakeman_why='the gate never opened'
			return 1
		fi
	fi
	if [[ -n ${STUB_BRAKEMAN_TICKET:-} ]]; then
		# The ticket a spec's proposal edits, read with its rev, as the turn's read of it.
		case ${STUB_WORKER:-} in
		mcp) mcp_call observe "{\"what\":\"ticket\",\"key\":$(json_str "$STUB_BRAKEMAN_TICKET")}" >/dev/null ;;
		*) "$conductor" brakeman observe ticket --key "$STUB_BRAKEMAN_TICKET" >/dev/null 2>&1 ;;
		esac
		printf 'brakeman: %s: read the ticket %s\n' "$req" "$STUB_BRAKEMAN_TICKET" >>"$transcript"
	fi
	if [[ -n ${STUB_BRAKEMAN_ACT:-} && -s $STUB_BRAKEMAN_ACT.run ]]; then
		# The run a spec's act changes (a decision's route), read before the
		# act as the turn's read of what it proposes: its decisions, each
		# with its ref and rev, give the rev the act and the proposal name
		# (@REV) for the decision the act's KEY names.
		local actrun actop actkey runread re
		actrun=$(<"$STUB_BRAKEMAN_ACT.run")
		read -r actop actkey _ <"$STUB_BRAKEMAN_ACT"
		runread=$("$conductor" brakeman observe run --run "$actrun" 2>&1)
		re="\"ref\":\"decision:${actkey}\",\"rev\":([0-9]+)"
		[[ $runread =~ $re ]] && actrev=${BASH_REMATCH[1]}
		printf 'brakeman: %s: read the run %s: %s at rev %s\n' "$req" "$actrun" "$actkey" "${actrev:-none}" >>"$transcript"
	fi
	if [[ -n ${STUB_BRAKEMAN_ACT:-} && -s $STUB_BRAKEMAN_ACT ]]; then
		brakeman_act "$req"
	fi
	evidence=$(json_str "session:${CONDUCTOR_SESSION_ID:-}")
	[[ -n $run ]] && evidence+=",$(json_str "run:$run")"
	[[ -n ${STUB_BRAKEMAN_CHECK:-} ]] && evidence+=",$(json_str "$STUB_BRAKEMAN_CHECK")"
	if [[ -n ${STUB_BRAKEMAN_BRIEF:-} && -s $STUB_BRAKEMAN_BRIEF ]]; then
		brief=$(<"$STUB_BRAKEMAN_BRIEF")
		[[ -n $actrev ]] && brief=${brief//@REV/$actrev}
	else
		brief="{\"schema\":\"brakeman.brief/1\",\"title\":\"Stub brief for $req\",\"summary\":\"The stub read its request and what changed, and found its own turn.\",\"items\":[{\"kind\":\"fact/1\",\"text\":\"This turn runs as its own session and run.\",\"evidence\":[$evidence]},{\"kind\":\"interpretation/1\",\"text\":\"Nothing else happened that the stub was asked about.\",\"facts\":[0]}]}"
	fi
	case ${STUB_WORKER:-} in
	mcp)
		reply=$(mcp_call brief_submit "{\"brief\":$brief}")
		mcp_ok "$reply" || ok=0
		;;
	*)
		printf '%s\n' "$brief" >"$transcripts/$id.brief.json"
		reply=$("$conductor" brakeman brief --file "$transcripts/$id.brief.json" 2>&1) || ok=0
		;;
	esac
	printf 'brief: %s\n' "${reply:0:300}" >>"$transcript"
	printf 'stub: brief -> %s\n' "${reply:0:200}"
	if ((!ok)); then
		brakeman_why='the brief was refused'
		return 1
	fi
}

# brakeman_act runs the act STUB_BRAKEMAN_ACT's file names for request $1:
# its one line is "OPERATION KEY [PARAM]" (PARAM the route's path parameter
# KEY fills, key unless named: did for a decision's route), its body the
# JSON in <file>.body. The
# answer (the command's words, or the tool's reply line), taken or refused,
# goes to <file>.out, written whole by a rename, and into the transcript; a
# refusal does not end the turn: a spec's brief says what happened.
brakeman_act() {
	local req=$1 op key param body out status=taken
	read -r op key param <"$STUB_BRAKEMAN_ACT"
	param=${param:-key}
	body='{}'
	[[ -s $STUB_BRAKEMAN_ACT.body ]] && body=$(<"$STUB_BRAKEMAN_ACT.body")
	[[ -n ${actrev:-} ]] && body=${body//@REV/$actrev}
	case ${STUB_WORKER:-} in
	mcp)
		out=$(mcp_call act "{\"operation\":$(json_str "$op"),\"params\":{$(json_str "$param"):$(json_str "$key")},\"body\":$body}")
		mcp_ok "$out" || status=refused
		;;
	*)
		printf '%s\n' "$body" >"$transcripts/$id.act.json"
		out=$("$conductor" brakeman act "$op" "--$param" "$key" --body-file "$transcripts/$id.act.json" 2>&1) || status=refused
		;;
	esac
	printf '%s\n%s\n' "$status" "$out" >"$STUB_BRAKEMAN_ACT.out.new"
	mv -f "$STUB_BRAKEMAN_ACT.out.new" "$STUB_BRAKEMAN_ACT.out"
	printf 'brakeman: %s: act %s on %s: %s %s\n' "$req" "$op" "$key" "$status" "${out:0:300}" >>"$transcript"
}

# mcp_ok reports whether $1, a tools/call reply line from mcp_call, is a
# result the tool answered without error: present, a result, and neither a
# JSON-RPC error nor a tool result with isError set.
mcp_ok() {
	[[ -n $1 && $1 == *'"result"'* && $1 != *'"error"'* && $1 != *'"isError":true'* ]]
}

# trust_dialog draws the folder-trust question as Codex 0.161 does (1. Trust
# and continue, highlighted; 2. Quit) and waits for Enter; arrows move the
# selection and redraw, Esc quits, anything else is ignored. Enter on the
# first writes the trust the way Codex keeps it; on Quit the stub exits, as
# Codex does.
trust_dialog() {
	local sel=0 key seq
	draw() {
		local y n
		if ((sel == 0)); then y='›' n=' '; else y=' ' n='›'; fi
		printf '\r\e[2K\e[1A\e[2K\e[1A\e[2K\e[1A\e[2K\e[1A\e[2KTrust this folder?\n%s 1. Trust and continue\n%s 2. Quit\nenter continue · esc quit\n' "$y" "$n"
	}
	printf 'Trust this folder?\n› 1. Trust and continue\n  2. Quit\nenter continue · esc quit\n'
	while IFS= read -rsn1 key; do
		if [[ -z $key || $key == $'\r' ]]; then
			break
		fi
		if [[ $key == $'\e' ]]; then
			seq=''
			IFS= read -rsn2 -t 0.2 seq || true
			case $seq in
			'[A' | '[B') sel=$((1 - sel)) ;;
			'') sel=1 && break ;;
			esac
			draw
		fi
	done
	if ((sel == 0)); then
		mkdir -p "${HOME:-/tmp}/.codex"
		printf '[projects.%s]\ntrust_level = "trusted"\n' "$(json_str "$PWD")" >>"${HOME:-/tmp}/.codex/config.toml"
		# The dialog's lines go as Codex takes them away (its TUI redraws
		# without them), the screen not erased whole.
		printf '\r\e[2K\e[1A\e[2K\e[1A\e[2K\e[1A\e[2K\e[1A\e[2KTrusted.\n'
	else
		printf 'Quit.\n'
		exit 0
	fi
}

# erase_dialog erases a startup dialog's $1 lines above the cursor's, and
# the cursor's, as Codex takes a dialog away once it is answered (its TUI
# redraws without it), the screen not erased whole.
erase_dialog() {
	local n=$1 up=''
	while ((n-- > 0)); do up+='\e[1A\e[2K'; done
	printf '\r\e[2K%b' "$up"
}

# hooks_review draws Codex's "Hooks need review" and waits for Enter; a
# digit moves the highlight.
hooks_review() {
	local sel=1 key
	printf 'Hooks need review\n7 hooks are new or changed.\nHooks can run outside the sandbox after you trust them.\n> 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won'"'"'t run)\n'
	while IFS= read -rsn1 key; do
		if [[ -z $key || $key == $'\r' ]]; then
			break
		fi
		case $key in
		1 | 2 | 3) sel=$key ;;
		esac
	done
	erase_dialog 6
	printf '%s\n' "$sel" >"${HOME:-/tmp}/.codex/stub-hooks-answer"
	case $sel in
	2) printf 'Hooks trusted.\n' ;;
	3) printf 'Hooks off.\n' ;;
	*) printf 'Reviewing hooks.\n' ;;
	esac
}

# background_question draws Codex's background-server question; a digit picks at once.
background_question() {
	local key
	printf 'Background server has incompatible feature settings\nThis session requires api_key_model_discovery to be enabled\n  1. Run without daemon this time\n  2. Restart with these settings\n> 3. Cancel\n'
	while IFS= read -rsn1 key; do
		case $key in
		1 | 2 | 3) break ;;
		esac
	done
	erase_dialog 5
	printf '%s\n' "$key" >"${HOME:-/tmp}/.codex/stub-background-answer"
	if [[ $key == 3 ]]; then
		exit 0
	fi
}

# update_question draws Codex's update question as Codex 0.161 does: Enter
# installs (what must never happen from a prompt), 1 too; Esc and 2 skip;
# 3 skips until the next version (it picks at once, as in Codex 0.161,
# verified in a PTY on 2026-10-11).
update_question() {
	local key answer=
	printf 'Update available · 0.161.0 → 0.162.0\nRelease notes: https://github.com/openai/codex/releases/latest\n\n› 1. Update now (runs `npm install -g @openai/codex`)\n  2. Skip\n  3. Skip until next version\n\nenter continue · esc skip\n'
	while IFS= read -rsn1 key; do
		case $key in
		'' | $'\r' | 1) answer=update ;;
		$'\e')
			# ESC and a key at once (Alt+z, an arrow) is one key, as
			# Codex's crossterm reads it, and the question ignores it; an
			# ESC alone skips.
			if IFS= read -rsn1 -t 0.05 _; then
				continue
			fi
			answer=skip
			;;
		2) answer=skip ;;
		3) answer=skip-version ;;
		*) continue ;;
		esac
		break
	done
	erase_dialog 8
	printf '%s\n' "$answer" >"${HOME:-/tmp}/.codex/stub-update-answer"
	if [[ $answer == update ]]; then
		printf 'Updating Codex...\n'
	fi
}

# The TUI mode (`stub tui on`): tui is set once it is on, tui_line is the
# line typed and not yet entered, tui_queue the lines entered while the stub
# spins, mice the mouse reports read; spinning is set while it spins; us
# is now_us's.
tui= tui_line= mice= spinning= us=
tui_queue=()

# put writes $2 into the file $1 whole, for a test to read (the spin's
# state): a new file renamed over it, so a reader sees the old or the new,
# never a part.
put() {
	printf '%s' "$2" >"$1.new"
	mv -f -- "$1.new" "$1"
}

# tui_save appends the line not yet entered to <id>.line, tui_save_mice the
# mouse reports read to <id>.mouse: a record a line, the last whole one
# what is now. Appended in one write, never rewritten: a reader never sees
# one half written, and no file is made for each key.
tui_save() { printf '%s\n' "$tui_line" >>"$transcripts/$id.line"; }
tui_save_mice() { printf '%s\n' "${mice:-0}" >>"$transcripts/$id.mouse"; }

# tui_read reads one byte (a character) of input into byte, within $1
# seconds if given; it fails when none came. A read whose timer runs out as
# it takes a byte (a busy machine) says it timed out and keeps the byte:
# that byte is read all the same.
tui_read() {
	byte=
	if [[ -n ${1:-} ]]; then
		IFS= read -rsn1 -d '' -t "$1" byte || [[ -n $byte ]]
	else
		IFS= read -rsn1 -d '' byte
	fi
}

# tui_next reads the next byte of an escape sequence begun into byte: at
# once when it is there (read -t 0 asks without a timer, which a busy
# machine can run out before the read is made), else within half a second
# (a lone ESC is the Escape key); it fails when none came.
tui_next() {
	if read -t 0; then
		tui_read
	else
		tui_read 0.5
	fi
}

# tui_keys_now takes the keys whose bytes are there now, waiting for none.
tui_keys_now() {
	while read -t 0; do
		tui_read
		tui_key "$byte"
	done
}

# now_us sets us to the time in microseconds since the epoch: bash 5's
# EPOCHREALTIME, or GNU date's on an older bash (the stub needs bash 4).
now_us() {
	if [[ -n ${EPOCHREALTIME:-} ]]; then
		us=${EPOCHREALTIME/[.,]/}
	else
		us=$(date +%s%6N)
	fi
}

# tui_key takes the key whose first byte is $1, reading the rest of an
# escape sequence: a CSI (a report, the mouse's, a paste marker, a key with
# a sequence) to its final byte, an SS3 key, an OSC to its BEL or ST; all
# dropped, a mouse report counted. Enter enters the line, Ctrl+U clears
# it, Backspace erases, other control characters are dropped, the rest is
# typed into the line and echoed.
tui_key() {
	local k=$1 seq= code
	case $k in
	'') ;;
	$'\e')
		tui_next || return 0
		case $byte in
		'[')
			while tui_next; do
				seq+=$byte
				printf -v code '%d' "'$byte"
				((code >= 64)) && break
			done
			if [[ $seq == M ]]; then
				# A legacy mouse report: three bytes follow.
				tui_next || true
				tui_next || true
				tui_next || true
				mice=$((${mice:-0} + 1))
				tui_save_mice
			elif [[ $seq == '<'*[Mm] ]]; then
				mice=$((${mice:-0} + 1))
				tui_save_mice
			fi
			;;
		O) tui_next || true ;;
		']')
			while tui_next; do
				[[ $byte == $'\a' ]] && break
				if [[ $byte == $'\e' ]]; then
					tui_next || true
					break
				fi
			done
			;;
		esac
		;;
	$'\r' | $'\n')
		local l=$tui_line
		now_us
		printf '%s %s\n' "$us" "$l" >>"$transcripts/$id.entered"
		tui_line=
		tui_save
		printf '\n'
		if [[ -n $spinning ]]; then
			tui_queue+=("$l")
		else
			handle_line "$l"
			printf '> %s' "$tui_line"
		fi
		;;
	$'\x15')
		tui_line=
		tui_save
		printf '\r\e[2K> '
		;;
	$'\x7f' | $'\b')
		if [[ -n $tui_line ]]; then
			tui_line=${tui_line%?}
			printf '\b \b'
		fi
		tui_save
		;;
	[[:cntrl:]]) ;;
	*)
		tui_line+=$k
		printf '%s' "$k"
		tui_save
		;;
	esac
}

# spin draws a dot every 100 ms for about $1 seconds, or until its file
# says other than "spinning", answering nothing: in the TUI mode it takes
# keys between the dots (a line entered is answered once it is done),
# otherwise it reads nothing.
spin() {
	local end=$((SECONDS + $1)) l st
	local -a queued
	spinning=1
	put "$transcripts/$id.spin" $'spinning\n'
	while ((SECONDS < end)); do
		st=
		IFS= read -r st <"$transcripts/$id.spin" || true
		[[ $st == spinning ]] || break
		printf .
		if [[ -z $tui ]]; then
			sleep 0.1
			continue
		fi
		# The keys typed meanwhile: those there now, then any within the
		# 100 ms to the next dot.
		tui_keys_now
		if tui_read 0.1; then
			tui_key "$byte"
			tui_keys_now
		fi
	done
	printf '\n'
	put "$transcripts/$id.spin" $'done\n'
	spinning=
	queued=(${tui_queue[@]+"${tui_queue[@]}"})
	tui_queue=()
	for l in ${queued[@]+"${queued[@]}"}; do
		handle_line "$l"
	done
}

# answer answers one line.
answer() {
	local line=$1 rest=$1 kind target_run target branch deadline top to msg
	local -a merged=() failed=()
	printf 'got: %s\n' "$line"
	printf '%s\n' "${line:0:200}" >>"$transcript"
	title_working
	report_working
	if [[ -n ${STUB_BRAKEMAN:-} && -n ${CONDUCTOR_BRAKEMAN:-} && $line =~ ^Brakeman\ request\ (br-[1-9][0-9]{0,17})\ \( ]]; then
		local req=${BASH_REMATCH[1]} brakeman_why=''
		if brakeman_turn "$req"; then
			title_ready done
			report_done "brakeman $req: brief submitted" "$line"
		else
			title_ready done
			report_done "brakeman $req: no brief, $brakeman_why" "$line"
		fi
		return
	fi
	if [[ -n ${STUB_WORKER:-} && -n $decision_task && $line =~ ^Decision\ dr-[1-9][0-9]*\ \([a-z]+\)\ answered\  ]]; then
		# The answer of the decision it asked (STUB_DECISION), typed by Yardmaster: kept, and the attempt goes on to commit and submit.
		printf '%s\n' "$line" >stub-answer.txt
		local dtask=$decision_task dn=$decision_n
		decision_task=''
		decided=1
		task_attempt task "$dtask" "$dn"
		title_ready done
		report_done "task $dtask attempt $dn: submitted after its decision" "$line"
		return
	fi
	if [[ -n ${STUB_WORKER:-} && -n ${CONDUCTOR_TASK:-} && $line =~ ^Conductor\ (task|review)\ \"([a-z0-9_-]+)\",\ attempt\ ([0-9]+): ]]; then
		task_attempt "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
		title_ready done
		report_done "task $CONDUCTOR_TASK attempt ${CONDUCTOR_ATTEMPT:-?}: submitted" "$line"
		return
	fi
	if [[ $line =~ ^stub\ tools\ (codex|copilot|agy|goose)$ ]]; then
		tool_replay "${BASH_REMATCH[1]}"
	fi
	if [[ $line =~ ^stub\ approve\ ([A-Za-z0-9\ ./:_-]{1,100})$ ]]; then
		approval "${BASH_REMATCH[1]}"
	fi
	if [[ $line =~ ^stub\ approvals\ ([A-Za-z0-9\ ./:_|-]{1,200})$ ]]; then
		local -a queued=()
		local cmd
		IFS='|' read -ra queued <<<"${BASH_REMATCH[1]}"
		for cmd in "${queued[@]}"; do
			if [[ -n ${cmd// /} ]]; then
				approval "$cmd"
			fi
		done
	fi
	if [[ $line =~ ^stub\ ask\ (.{1,200})$ ]]; then
		report --state needs_input --message "${BASH_REMATCH[1]}"
		spin 30
	fi
	if [[ $line =~ ^stub\ form\ (.{1,200})$ ]]; then
		blocking_form "${BASH_REMATCH[1]}"
	fi
	if [[ $line == 'stub auth' ]]; then
		sign_in
	fi
	if [[ $line == 'stub plan' ]]; then
		claude_plan
	fi
	if [[ $line == 'stub permission' ]]; then
		claude_permission
	fi
	if [[ $line =~ ^stub\ open\ (https?://[^[:space:]]{1,2000})$ ]]; then
		# A link opened for the person (#436): conductor open, and its
		# one line in the transcript, which a spec waits on.
		out=$("$conductor" open -- "${BASH_REMATCH[1]}" 2>&1) || true
		printf '%s\n' "$out"
		printf 'open: %s\n' "$out" >>"$transcript"
	fi
	if [[ $line =~ ^stub\ browser\ (https?://[^[:space:]]{1,2000})$ ]]; then
		# The agent's own opener: $BROWSER, Conductor's for an agent with
		# a browser set (#436), run as an agent runs it, the link its one
		# argument.
		if [[ -n ${BROWSER:-} ]]; then
			out=$("$BROWSER" "${BASH_REMATCH[1]}" 2>&1) || true
		else
			out='no BROWSER'
		fi
		printf '%s\n' "$out"
		printf 'browser: %s\n' "$out" >>"$transcript"
	fi
	if [[ $line =~ ^stub\ ask-wait\ (.{1,200})$ ]]; then
		report --ask --message "${BASH_REMATCH[1]}"
		spin 30
	fi
	if [[ $line =~ ^stub\ bell\ ([1-9][0-9]?)$ ]]; then
		# A bell with nothing drawn after it, as an agent with no hooks
		# rings for a prompt it does not name; then, the pause over, the
		# agent moves on: output, a pause for the screen to be looked at,
		# and the spin a test ends.
		printf '\a'
		sleep "${BASH_REMATCH[1]}"
		printf '\nmoving on: one\nmoving on: two\nmoving on: three\nmoving on: four\n'
		sleep 2
		spin 30
	fi
	if [[ $line =~ ^stub\ async\ (.{1,200})$ ]]; then
		async_question "${BASH_REMATCH[1]}"
	fi
	if [[ $line =~ ^stub\ async-ask\ (.{1,200})$ ]]; then
		async_question "${BASH_REMATCH[1]}" ask
	fi
	while [[ $rest =~ $target_re ]]; do
		kind=${BASH_REMATCH[1]}
		target_run=${BASH_REMATCH[2]}
		target=${BASH_REMATCH[3]}
		rest=${rest#*"${BASH_REMATCH[0]}"}
		while [[ $target == *. ]]; do target=${target%.}; done
		[[ $target_run == '$CONDUCTOR_RUN' ]] && target_run=$run
		if [[ -z $run || $target_run != "$run" || ! $target =~ $member_re ]]; then
			printf 'stub: crew/%s/%s is not a branch of this run: left alone\n' "$target_run" "$target"
			continue
		fi
		branch=crew/$run/$target
		if [[ $kind == 'wait until' ]]; then
			deadline=$((SECONDS + wait_s))
			until holds "$branch" "$target"; do
				if ((SECONDS >= deadline)); then
					printf 'stub: %s did not hold stub-%s.txt within %ss\n' "$branch" "$target" "$wait_s"
					break
				fi
				sleep 0.5
			done
			continue
		fi
		if git "${git_id[@]}" merge -q --no-edit -- "$branch" >/dev/null 2>&1; then
			merged+=("$target")
		else
			git merge --abort >/dev/null 2>&1 || true
			failed+=("$target")
		fi
	done
	top=$(git rev-parse --show-toplevel 2>/dev/null) || top=.
	printf '%s\n' "${line:0:200}" >>"$top/stub-$member.txt"
	git -C "$top" add -- "stub-$member.txt" >/dev/null 2>&1 || true
	git -C "$top" "${git_id[@]}" commit -q -m "stub: $member answers" >/dev/null 2>&1 || true
	if [[ -z $handed_off && $line =~ $handoff_re ]]; then
		to=${BASH_REMATCH[1]}
		if [[ $to != "$member" ]]; then
			handed_off=1
			report --event handoff --to "$to" --message "stub $member: over to $to"
		fi
	fi
	printf 'stub: merged [%s] failed [%s]\n' "${merged[*]-}" "${failed[*]-}"
	msg="got: ${line:0:100}; merged: ${merged[*]-none}; failed: ${failed[*]-none}"
	title_ready done
	report_done "$msg" "$line"
	if [[ -n $ring_at_end ]]; then
		# Codex rings the bell it is asked for (tui.notification_method="bel")
		# as its turn ends, after notify has reported done (rc.7 app,
		# 2026-10-11).
		ring_at_end=
		printf '\a'
		printf 'bell: after done\n' >>"$transcript"
	fi
}

printf 'conductor e2e stub: %s in run %s (%s, session %s%s%s)\n' "$member" "${run:-none}" "$identity" "$id" "${yolo:+, yolo}" "${trusted:+, trusted}"
if [[ -n $resume_id ]]; then
	if [[ -s $transcript ]]; then
		printf 'stub: resumed %s: %s lines\n' "$id" "$(wc -l <"$transcript" | tr -d ' ')"
		cat "$transcript"
	else
		printf 'stub: resumed %s: 0 lines\n' "$id"
	fi
else
	: >>"$transcript"
fi
if [[ ${STUB_TRUST_DIALOG:-} == 1 && -z $trusted ]]; then
	trust_dialog
fi
if [[ -e ${HOME:-/nonexistent}/.codex/stub-hooks-review ]]; then
	hooks_review
fi
if [[ -e ${HOME:-/nonexistent}/.codex/stub-background ]]; then
	background_question
fi
if [[ -e ${HOME:-/nonexistent}/.codex/stub-update ]]; then
	update_question
fi
title_ready
if [[ -n ${STUB_CHOICES:-} ]]; then
	printf '%s [%s]\n' "${STUB_ASK:-Which one?}" "$STUB_CHOICES"
	report --state needs_input --message "${STUB_ASK:-Which one?}" --choices "$STUB_CHOICES"
fi
# handle_line takes one line entered: the stub's own lines, or a line to
# answer.
handle_line() {
	local line=$1
	if [[ $line =~ ^stub\ spin\ ([1-9][0-9]?)$ ]]; then
		title_working
		spin "${BASH_REMATCH[1]}"
		title_ready
	elif [[ $line =~ ^stub\ status\ (0\.164|0\.163|none)$ ]]; then
		# The program status's shape for this session (status_set).
		put "$transcripts/$id.status" "${BASH_REMATCH[1]}"$'\n'
		status_last=
	elif [[ $line == 'stub tui on' ]]; then
		# Any-event mouse tracking in SGR, and keys read one by one with the
		# terminal's line editing and echo off, as Claude Code and Codex do.
		printf '\e[?1003h\e[?1006h'
		stty -icanon -echo min 1 time 0
		tui=1
		tui_save_mice
		tui_save
	elif [[ -n ${line//[[:space:]]/} ]]; then
		answer "$line"
	fi
}

printf '\e[?2004h> '
while :; do
	if [[ -n $tui ]]; then
		tui_read || break
		tui_key "$byte"
		continue
	fi
	IFS= read -r line || break
	line=${line//"$paste_start"/}
	line=${line//"$paste_end"/}
	line=${line%$'\r'}
	handle_line "$line"
	if [[ -n $tui ]]; then
		printf '> %s' "$tui_line"
	else
		printf '> '
	fi
done
status_set clear
