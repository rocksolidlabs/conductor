package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// Skill is the Conductor skill, a SKILL.md: it tells an agent working in a
// Conductor session when and how to report progress, artifacts, blockers
// and handoffs with conductor notify, and how to form a crew around its
// session with conductor crew. `conductor skill` prints it, WriteAssets
// writes it to the hooks dir as skills/conductor/SKILL.md (every session
// names that copy in CONDUCTOR_SKILL), and Install and a launch copy it
// for the agents that read skills (Adapter.SkillPath): Claude Code and
// Codex into their own skills directory, the Antigravity CLI into its, the
// others into the shared ~/.agents/skills. It names no binary path: its
// commands run the binary the session names in CONDUCTOR_BIN, else the
// conductor on PATH. The line after the frontmatter marks the file as
// Conductor's (skillMarker).
const Skill = `---
name: conductor
description: Report progress, artifacts, blockers and handoffs to Conductor, and form a crew around your session, while working in a Conductor session
---

<!-- conductor:skill (written by Conductor, which replaces this file when it installs its hooks; delete this line to keep your own changes) -->

# Conductor

Conductor runs coding agents in terminals that people watch and share from a
browser. In a Conductor session, conductor notify tells them what you are
doing without anyone reading the terminal: each report appears in the
session's activity and on the Events page. You are in a Conductor session
when CONDUCTOR_NOTIFY_URL and CONDUCTOR_NOTIFY_TOKEN are set.

With Conductor's MCP server registered (Claude Code and Codex get it at
launch), the same reports and actions are tools, each where the server
lets this session call it; the guide tool says which it may call now and
the limits that bind it. Call those you have; the commands below do the
same.

- In every session: guide, report, set_state and ask.
- In a session the server runs (not a hosted one), while it lets agents
  serve themselves (its agents.selfService setting, on by default): link
  and open_url.
- There, while the session is in no crew: form_crew.
- There, once the session is in a crew: run_status.
- There, once the session is in a crew that is not a managed run:
  add_member.
- In an attempt of a managed run (CONDUCTOR_TASK is set): task_context,
  task_result, message_send, inbox_read, inbox_ack, task_blocked,
  task_checkpoint, decision_request, decision_list, decision_read and
  decision_withdraw.
- In a run a person linked to a Dispatch ticket: ticket_read and
  ticket_comment.

## When to report

- Progress on a long task, at milestones rather than at every step:

      "${CONDUCTOR_BIN:-conductor}" notify --event progress --message "4/7 handlers"

- An artifact someone should look at, such as a pull request, a preview
  deployment or a report (add --message to say what it is):

      "${CONDUCTOR_BIN:-conductor}" notify --event artifact --url <url>

- A link meant for the person, such as an artifact, a pull request, a
  preview or a sign-in page: open it with open_url, or this command, rather
  than your own opener. It opens in the browser your agent's settings name,
  where the right account is signed in; it prints what happened. At most
  10 a minute.

      "${CONDUCTOR_BIN:-conductor}" open <url>

- A handoff, when the next part of the work belongs to another member of the
  crew:

      "${CONDUCTOR_BIN:-conductor}" notify --event handoff --to <member> --message "…"

- A file you read, edited, wrote or deleted, when your tools do not report
  it through hooks (Claude Code's, Codex's and Cursor's do): the Files tab
  lists what you touched.

      "${CONDUCTOR_BIN:-conductor}" notify --event file --op edit --path internal/api/users.go

- A decision you cannot make yourself, such as a choice between designs, a
  missing credential or an approval: ask, then wait for the answer in the
  terminal. The session shows as needing input until someone replies, also
  once your turn has ended.

      "${CONDUCTOR_BIN:-conductor}" notify --ask --message "…"

- A question with a few answers: name them in the terminal on their own
  line, then ask with the same words as choices, so one click types the
  answer as a line (at most 6, each at most 40 bytes):

      "${CONDUCTOR_BIN:-conductor}" notify --ask --message "Which database?" --choices "Postgres|SQLite|Keep both"

- A question you no longer need answered (you found the answer yourself):
  clear it, so nobody answers it for nothing.

      "${CONDUCTOR_BIN:-conductor}" notify --state clear

## Form a crew

When the work splits into parts that can run side by side, such as a feature
and its tests or a migration and its docs, form a crew around this session:
every other member gets a session of its own, and you stay in yours as the
lead. Write the crew as JSON, then create it; --open offers the run to the
people watching.

      "${CONDUCTOR_BIN:-conductor}" crew create crew.json --self lead --open

    {
      "name": "users api",
      "goal": "ship /v1/users with tests and docs",
      "members": [
        {"name": "lead", "agentId": "", "prompt": "", "start": {"when": "manual"}},
        {"name": "tests", "agentId": "claude", "prompt": "Write the tests for $GOAL; lead has the handler", "start": {"when": "after", "member": "lead"}},
        {"name": "docs", "agentId": "codex", "prompt": "Document $GOAL", "start": {"when": "immediately"}}
      ]
    }

- The lead is you: its agentId and prompt stay empty and its start is manual.
  Every other member names an agent of Conductor's catalog (your own id is in
  CONDUCTOR_AGENT; use it unless told otherwise) and gets a prompt, in which
  $GOAL is replaced by the goal.
- A member that starts "after" you begins when you next finish a turn, so do
  your part first; one that starts "immediately" begins at once; "manual"
  waits for a person. Hand details to a member with a handoff (above).
- Never form a crew for work you can finish alone; a session forms at
  most 2 an hour, and none while it is in a crew. A crew has at most 12
  members and works in this session's directory.
- Once the crew is formed, crew status lists the members and their
  states, crew add member.json adds one member (the shape of one entry
  above), and crew link prints a view-only link to this session for a
  pull request or a message:

      "${CONDUCTOR_BIN:-conductor}" crew status
      "${CONDUCTOR_BIN:-conductor}" crew add member.json
      "${CONDUCTOR_BIN:-conductor}" crew link --label "for the PR"

## Your task

In a managed run (CONDUCTOR_TASK is set), this session is one attempt at
one task: read it, do it, and submit the result once. A done, a handoff or
the end of your turn finishes nothing; only a submitted result does.

- Read the task: its goal, where it starts, its inputs, findings to
  address and how it is checked, in .conductor/task.md of your working
  directory (CONDUCTOR_TASK_FILE), with the task_context tool, or:

      "${CONDUCTOR_BIN:-conductor}" task context

- When the crew has a project manifest, it made your tree usable before
  your session started: CONDUCTOR_PORT_<NAME> holds each of its ports,
  this tree's alone, CONDUCTOR_PREVIEW_URL its preview when it has one,
  and CONDUCTOR_STATE_DIR (.conductor/state) the directory its tools and
  services keep their state in, as task.md says. CONDUCTOR_TREE_SECRET is
  a credential of this tree alone, for a server the tree runs: never print
  it, and never dump the environment (env, printenv, set) in a step, a
  check, a verify step or a service: Conductor keeps their output and
  quotes a failing command's last lines in its failure.

- A work task: commit your work on the branch you are on (the worktree
  must be clean apart from .conductor/), then submit the commit with a
  summary and the issues you know of, with the task_result tool or:

      "${CONDUCTOR_BIN:-conductor}" task result --summary "what you did" --issue "what is left"

- A review: do not commit. Submit a verdict, approved or needs_changes,
  with findings (severity blocker, major, minor or nit; path and line
  when it is about a place):

      "${CONDUCTOR_BIN:-conductor}" task result --verdict needs_changes --finding "major internal/api/users.go:42 the handler ignores the error"

- The findings to address may come from a person, who reviewed your
  task's candidate: each has its id, its lines as they saw them and the
  resolution asked for. Do the changes asked for, answer each question in
  your summary, and say which changes you addressed, each by its id with
  a note (addressed in task_result, or --addressed). It is a claim a
  person checks against your result, never the change's resolution:

      "${CONDUCTOR_BIN:-conductor}" task result --summary "what you did; the answers" --addressed "<id>=renamed the field"

- Conductor runs the task's checks against your commit; a check that
  fails starts a fresh attempt at your commit with the log in its task
  file. A refused submit says why (uncommitted changes, the wrong branch,
  a commit not from the inputs): fix it and submit again. The same commit
  submitted twice is taken once.

## Decisions

In a managed run, a question your work must wait for (a choice between
designs, an approval before a step, something only a person can do) is a
decision: ask it with decision_request or task decide, not with ask or task
blocked, whose question lasts only as long as this session. Conductor keeps
a decision through restarts, holds your task until the operator answers,
and does not count an attempt that ends while it waits. What you do before
you ask depends on your task's kind: a review is a task whose task.md says
"Do not commit" under How to finish; any other is a work task.

- A work task commits its work and saves a checkpoint first (task
  checkpoint, below), so an attempt that ends while it waits loses
  nothing, then asks:

      "${CONDUCTOR_BIN:-conductor}" task decide --type approval --question "Drop the legacy table?" --option "Drop it: frees 2 GB" --option "Keep it" --recommend 0 --authorizes "running migration 42" --evidence "task:$CONDUCTOR_RUN/$CONDUCTOR_TASK"

- A review commits nothing and saves no checkpoint (task checkpoint
  refuses a review): it asks as it is, about the candidate it reviews,
  which is its HEAD. No checkpoint is needed for a decision:

      "${CONDUCTOR_BIN:-conductor}" task decide --type clarification --question "Must the v1 routes stay as they are?" --evidence "task:$CONDUCTOR_RUN/$CONDUCTOR_TASK"

- --type is clarification (options optional, answered in words too),
  approval (2 to 6 options and what a yes --authorizes) or assignment (no
  options, answered Done or Not done, for work a person does). Options are
  numbered from 0 in the order given; --recommend names one by its number.
  Your task file's "Who answers for this work" names who holds each
  responsibility; --for (for, in decision_request) names the one the
  decision is for, role:architecture or person:nate, and the operator
  answers it for them.
- It replies at once with the decision's id and its state: "dr-7 open.
  Pending. ...". dr-7 is the decision's id, which task decision, task
  withdraw and --supersedes take. Without an id, task decision lists your
  task's decisions (id, type, state, rev, question, answer); rev counts the
  decision's changes and is nothing you send. These commands take your
  own task's decisions only: another task's id is answered exactly as an
  id no decision has. Withdraw an open decision this attempt asked once you
  no longer need it. A withdrawal refused not_held means your run was
  continued: the decision stays open in the run that continues this one,
  which waits on it. Stop there and end your turn, as this attempt's work
  is over; submit nothing and ask nothing more.

      "${CONDUCTOR_BIN:-conductor}" task decision dr-7
      "${CONDUCTOR_BIN:-conductor}" task withdraw dr-7

- Then end your turn, or go on with work that does not depend on it; never
  submit your result until its answer is applied (a result is refused,
  decision_open, while one waits). Go on only once a decision is applied:
  open and answered (the response recorded, not yet applied: it may still
  be superseded or cancelled instead) still wait; superseded, cancelled
  and expired mean no answer will come. task decision says which of these
  each is; its list shows an answer without the rationale it was given,
  so read an applied decision whole (task decision dr-7) before you act
  on it. A task decide that failed may still have opened its decision:
  list them with task decision before you ask again, or you wait on two.
  With --file, give a requestId of your own: the same request with it
  again replies with the same decision, never a second.
- --evidence cites a record by its ref, repeatable. Only refs to your own
  work are taken: run:RUN, task:RUN/TASK, attempt:RUN/TASK/N and
  check:RUN/TASK/N/CHECK/K (your task's), commit:RUN/SHA (the full id, git
  rev-parse HEAD, of a commit in your run's repository), session:SESSION
  and question:SESSION/ID (this session's), message:RUN/SEQ (a message to
  you, #SEQ as task inbox numbers it), and, in a run launched from a
  ticket, ticket:KEY-n, plan:KEY-n/REV (REV the number of the plan
  revision that launched it, from the launch package, not an attempt's),
  comment:KEY-n/ID and launch:ID (as the launch package in your task
  folder names them). RUN, TASK and SESSION are CONDUCTOR_RUN,
  CONDUCTOR_TASK and CONDUCTOR_SESSION_ID. N is the number of the attempt
  the record belongs to: CONDUCTOR_ATTEMPT for your own, an earlier one's
  as your task file names it; checks run after an attempt submits, so a
  check's N is an earlier attempt's, CHECK the check's id and K which run
  of it on that attempt, from 1. Any other ref (a file:, another task's
  or run's) and one that names no record refuse the whole request as
  "evidence[0]: REF resolves to nothing": drop it and ask again. --subject is what it is about: commit (your
  HEAD, the default), artifact:PATH (a file committed in your worktree) or
  candidate:RUN/TASK/N@COMMIT (a result attempt N submitted).

What Conductor types into this session about a decision, and what you do:

- "Decision dr-7 (approval) answered by NAME: ANSWER. Why: WHY. (receipt
  dr-7/1)" (Why only when the operator gave one; dr-7/1 is the receipt of
  the first response to dr-7, nothing you cite): the answer, applied. Do
  what it says, even when it is not the option you recommended or adds
  words of its own; it overrides your recommendation. Then go on and
  submit.
- "The operator asks about dr-7: TEXT": dr-7 is not answered. Ask again
  with what was asked for in --context and --supersedes dr-7; dr-7 closes
  and you wait for the new decision's answer.
- "Decision dr-7 was cancelled by the operator, not approved: WHY": it is
  never answered. Take it as a no: do not do what it asked about, finish
  the rest and submit, naming what was not done with --issue.
- "dr-7 was superseded: WHAT MOVED; ask again if you still need it", or
  "Decision dr-7's answer was not applied: WHAT MOVED": what it was about
  (your HEAD, the file, the candidate) changed since you asked. Never act
  on its answer; ask again about what is there now if you still need it.
- Nothing is typed for the operator's comment or a re-route: keep
  waiting. A decision you superseded or withdrew is closed: nothing comes.
- If the run stops or your session ends while you wait, the decision
  waits with your task. Your task's next attempt starts once it is
  applied or closed, in this run or the one that continues it (a work
  task's at your checkpoint, a review's at its candidate), and its task
  file's Decisions section says each decision's state: applied with the
  answer (do it), superseded or cancelled with why (never act on an
  answer it shows). A run that ends cancels the decisions it holds.

## Messages, blockers and checkpoints

- What the next task needs to know beyond your result, send its member as
  a message: it is kept with the run and typed into that member's session
  once it has its task (a member not started yet gets it then). Read your
  own with inbox, and acknowledge what you have read; a member keeps at
  most 50 not acknowledged. In a managed run a handoff is a message too.

      "${CONDUCTOR_BIN:-conductor}" task send --to docs "the API is under internal/api/users.go; the handler names are final"
      "${CONDUCTOR_BIN:-conductor}" task inbox
      "${CONDUCTOR_BIN:-conductor}" task ack 3

- Something stops you that only a person can give (a credential, a
  step by hand) and that you wait for in this session: say so. The task shows as blocked with the
  reason, and your session asks the message as your question; report
  working, or submit, once you go on.

      "${CONDUCTOR_BIN:-conductor}" task blocked --reason "no token for the registry" --message "Set NPM_TOKEN in the worktree's .env, then answer here"

- Before a long step, and before you stop, commit and save a checkpoint:
  what is done, what remains, notes. The next attempt at the task reads
  it, and one after an attempt that ends without a result starts at that
  commit, so the work is not lost.

      "${CONDUCTOR_BIN:-conductor}" task checkpoint --done "handlers and tests" --remaining "the docs" --notes "the docs page is docs/api.md"

## Your ticket

A person may link your run to a Dispatch ticket, Conductor's own record of
the work: what was asked, the criteria its owner accepts it against, and
the discussion around it. Read it before you start, with the ticket_read
tool or the command below; a run with no ticket says so (no_ticket), and
there is nothing more to do about it.

      "${CONDUCTOR_BIN:-conductor}" task ticket

- A run launched from a ticket's approved plan revision carries the ticket
  as it stood at the launch, in your worktree: .conductor/ticket.md (the
  ticket, the plan revision, the references, the constraints, the decisions
  a person makes, the comments handed over), .conductor/package.json (the
  same as launched) and the attachments in .conductor/attachments/. Your
  run follows that package. The tool and the command say first what changed
  of the ticket since the launch; a change that matters to your work is a
  person's to decide: tell them, with a comment or task blocked, and never
  act on it alone.
- Its acceptance criteria are what the work is accepted against: meet them,
  and say in your result which you could not.
- Comment on it for the people who own it: what you found, a question about
  what was asked, what you did and what is left. The comment is your run
  member's, with the ticket_comment tool or:

      "${CONDUCTOR_BIN:-conductor}" task ticket comment "the cause is the cache key, which ignores the locale; the fix is in the handler"

- You read the ticket and comment on it, nothing more: you cannot change
  it, close it or make another.

## If you are Brakeman

- CONDUCTOR_BRAKEMAN is set: this session is one Brakeman turn, answering one request. It forms no crew and adds no member: form_crew, add_member, crew create and crew add refuse it.
- Call guide first (its briefs section gives a brief's kinds, fields and refs), then read the request: brakeman_request, or "${CONDUCTOR_BIN:-conductor}" brakeman request.
- Read before you write: observe (or brakeman observe WHAT) reads what you may see.
- Every fact cites the evidence refs it rests on; your reading stays apart from the facts.
- Nothing you write becomes work; a person decides, or act (brakeman act) runs only what they granted you. A tool may ask a person first: wait.
- Submit one brief with brief_submit (or brakeman brief --file F), then your turn ends.

## Rules

- Never call notify or crew outside a Conductor session; the commands exit silently there.
- task context and task result work in an attempt of a managed run; elsewhere
  they say so and exit 1.
- Keep a message to one short line, at most 500 bytes. A URL is at most
  2048 bytes, a member name at most 40 characters.
- Do not report every tool call or file edit: Conductor's hooks report those
  when the session asks for them.
`

// skillAsset is where WriteAssets puts the skill under the hooks dir.
const skillAsset = "skills/conductor/SKILL.md"

// skillMarker marks a SKILL.md as Conductor's own, which Install replaces; a
// file without it is the user's.
const skillMarker = "conductor:skill"

var skillAssets = map[string]string{skillAsset: Skill}

// Where the agents that read skills find the Conductor skill, under home.
const (
	claudeSkill = ".claude/skills/conductor/SKILL.md"
	codexSkill  = ".codex/skills/conductor/SKILL.md"
	// agentsSkill is in the skills directory agents share (the Agent Skills
	// convention): pi, Goose, Cursor, Copilot, OpenCode, oh-my-pi, Amp and
	// DeepSeek Harness read it.
	agentsSkill = ".agents/skills/conductor/SKILL.md"
	// agySkill is the Antigravity CLI's global skills directory.
	agySkill = ".gemini/antigravity-cli/skills/conductor/SKILL.md"
)

// SkillFile is the skill's path under the hooks dir, which every session
// gets as CONDUCTOR_SKILL; empty unless hooksDir is absolute (a relative
// path would be read from the session's working directory).
func SkillFile(hooksDir string) string {
	if !filepath.IsAbs(hooksDir) {
		return ""
	}
	return filepath.Join(hooksDir, filepath.FromSlash(skillAsset))
}

// InstallSkill puts the Conductor skill where the agent of adapter agentID
// reads skills under home (Adapter.SkillPath): the file's path, whether it
// was written (false when it was Conductor's already), and ErrByHand for a
// SKILL.md of the user's own there, which is left to them. An adapter that
// reads no skills, or none, gives "", false, nil. A launch runs it before
// the agent starts; conductor hooks install does the same within Install.
func InstallSkill(agentID, home, hooksDir string) (path string, changed bool, err error) {
	a, ok := Get(agentID)
	if !ok || a.SkillPath == "" {
		return "", false, nil
	}
	path = filepath.Join(home, filepath.FromSlash(a.SkillPath))
	touched, err := install(home, skillStep(hooksDir, a.SkillPath))
	return path, len(touched) > 0, err
}

// ConfigDirOf is the configuration directory of its own the agent of adapter
// agentID runs with, given the environment of its process (getenv): the
// absolute path its Adapter.ConfigDirEnv names, else "" (no such adapter or
// variable, not set, or relative, which the agent takes from its working
// directory and a launch leaves alone).
func ConfigDirOf(agentID string, getenv func(string) string) string {
	a, ok := Get(agentID)
	if !ok || a.ConfigDirEnv == "" || getenv == nil {
		return ""
	}
	dir := getenv(a.ConfigDirEnv)
	if !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Clean(dir)
}

// InstallSkillIn puts the Conductor skill where the agent of adapter agentID
// reads skills under dir, a configuration directory of its own in place of
// its directory under home (ConfigDirOf; #291): the skill's path there is its
// path under home without that first directory (.claude/skills/conductor/
// SKILL.md becomes skills/conductor/SKILL.md). dir is made when it is
// missing, 0700, as the agent makes it on its first run. The results are
// InstallSkill's; "", false, nil for an adapter that reads no skills or has
// no such directory, and for a dir that is not absolute.
func InstallSkillIn(agentID, dir, hooksDir string) (path string, changed bool, err error) {
	a, ok := Get(agentID)
	if !ok || a.SkillPath == "" || a.ConfigDirEnv == "" || !filepath.IsAbs(dir) {
		return "", false, nil
	}
	rel := skillTail(a.SkillPath)
	path = filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return path, false, err
	}
	touched, err := install(dir, skillStep(hooksDir, rel))
	return path, len(touched) > 0, err
}

// skillTail is rel without its first element, the agent's directory under
// home: what lies under the configuration directory that stands in for it.
func skillTail(rel string) string {
	if _, tail, ok := strings.Cut(rel, "/"); ok {
		return tail
	}
	return rel
}

// skillStep is the step that makes the file rel under home a copy of the
// skill. A file there that does not carry skillMarker is the user's own and
// is left to them (ErrByHand); an empty one counts as none.
func skillStep(hooksDir, rel string) step {
	return step{rel, func(h *homeDir) (bool, error) {
		cur, ok, err := h.read(rel)
		if err != nil {
			return false, err
		}
		if ok && len(bytes.TrimSpace(cur)) > 0 && !bytes.Contains(cur, []byte(skillMarker)) {
			return false, byHand("%s is not Conductor's skill (it has no %s line), and Conductor does not overwrite it; to use Conductor's, replace it with what conductor skill prints", h.path(rel), skillMarker)
		}
		b, err := assetFor(skillAssets, hooksDir, skillAsset)
		if err != nil {
			return false, err
		}
		return h.write(rel, b)
	}}
}
