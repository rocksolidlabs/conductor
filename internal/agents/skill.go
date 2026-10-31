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
launch), the same reports and crew actions are tools: report, set_state,
ask, form_crew, add_member, run_status, link, task_context, task_result,
message_send, inbox_read, inbox_ack, task_blocked, task_checkpoint,
ticket_read and ticket_comment. Call those when you have them; the
commands below do the same.

## When to report

- Progress on a long task, at milestones rather than at every step:

      "${CONDUCTOR_BIN:-conductor}" notify --event progress --message "4/7 handlers"

- An artifact someone should look at, such as a pull request, a preview
  deployment or a report (add --message to say what it is):

      "${CONDUCTOR_BIN:-conductor}" notify --event artifact --url <url>

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
  answer as a line (at most 6, each at most 40 characters):

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
- Never form a crew for work you can finish alone, and at most 2 a session.
  A crew has at most 12 members and works in this session's directory.
- Later, crew status lists the members and their states, crew add member.json
  adds one member (the shape of one entry above), and crew link prints a
  view-only link to this session for a pull request or a message:

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
  services keep their state in, as task.md says.

- A work task: commit your work on the branch you are on (the worktree
  must be clean apart from .conductor/), then submit the commit with a
  summary and the issues you know of, with the task_result tool or:

      "${CONDUCTOR_BIN:-conductor}" task result --summary "what you did" --issue "what is left"

- A review: do not commit. Submit a verdict, approved or needs_changes,
  with findings (severity blocker, major, minor or nit; path and line
  when it is about a place):

      "${CONDUCTOR_BIN:-conductor}" task result --verdict needs_changes --finding "major internal/api/users.go:42 the handler ignores the error"

- Conductor runs the task's checks against your commit; a check that
  fails starts a fresh attempt at your commit with the log in its task
  file. A refused submit says why (uncommitted changes, the wrong branch,
  a commit not from the inputs): fix it and submit again. The same commit
  submitted twice is taken once.

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
  decision, a step by hand): say so. The task shows as blocked with the
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

- Its acceptance criteria are what the work is accepted against: meet them,
  and say in your result which you could not.
- Comment on it for the people who own it: what you found, a question about
  what was asked, what you did and what is left. The comment is your run
  member's, with the ticket_comment tool or:

      "${CONDUCTOR_BIN:-conductor}" task ticket comment "the cause is the cache key, which ignores the locale; the fix is in the handler"

- You read the ticket and comment on it, nothing more: you cannot change
  it, close it or make another.

## Rules

- Never call notify or crew outside a Conductor session; the commands exit silently there.
- task context and task result work in an attempt of a managed run; elsewhere
  they say so and exit 1.
- Keep a message to one short line, at most 500 characters. A URL is at most
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
