package agents

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The skill tells an agent when and how to report to Conductor: its
// frontmatter names it and says when it applies, its body gives the commands.
func TestSkillText(t *testing.T) {
	rest, ok := strings.CutPrefix(Skill, "---\n")
	front, body, closed := strings.Cut(rest, "\n---\n")
	if !ok || !closed {
		t.Fatalf("no frontmatter:\n%s", Skill)
	}
	if want := "name: conductor\ndescription: Report progress, artifacts, blockers and handoffs to Conductor, and form a crew around your session, while working in a Conductor session"; front != want {
		t.Fatalf("frontmatter:\n%s\nwant\n%s", front, want)
	}
	// The marker that makes the file Conductor's comes first after it.
	if !strings.HasPrefix(body, "\n<!-- "+skillMarker+" ") || strings.Count(Skill, skillMarker) != 1 {
		t.Fatalf("no marker line after the frontmatter:\n%s", body)
	}
	// The commands run the binary the session names in CONDUCTOR_BIN, quoted
	// for a path with spaces, or the conductor on PATH.
	for _, want := range []string{
		`"${CONDUCTOR_BIN:-conductor}" notify --event progress --message "4/7 handlers"`,
		`"${CONDUCTOR_BIN:-conductor}" notify --event artifact --url <url>`,
		`"${CONDUCTOR_BIN:-conductor}" notify --event handoff --to <member> --message "…"`,
		// The agent's own question, marked (#193): it shows until someone
		// replies, also once the turn has ended; and its clear.
		`"${CONDUCTOR_BIN:-conductor}" notify --ask --message "…"`,
		`"${CONDUCTOR_BIN:-conductor}" notify --ask --message "Which database?" --choices "Postgres|SQLite|Keep both"`,
		`"${CONDUCTOR_BIN:-conductor}" notify --state clear`,
		"The session shows as needing input until someone replies, also\n  once your turn has ended.",
		`"${CONDUCTOR_BIN:-conductor}" notify --event file --op edit --path internal/api/users.go`,
		`"${CONDUCTOR_BIN:-conductor}" crew create crew.json --self lead --open`,
		`"${CONDUCTOR_BIN:-conductor}" crew status`,
		`"${CONDUCTOR_BIN:-conductor}" crew add member.json`,
		`"${CONDUCTOR_BIN:-conductor}" crew link --label "for the PR"`,
		// The managed run's task (#334, row 6): read, submit a work result,
		// submit a review; a done finishes nothing.
		`"${CONDUCTOR_BIN:-conductor}" task context`,
		`"${CONDUCTOR_BIN:-conductor}" task result --summary "what you did" --issue "what is left"`,
		`"${CONDUCTOR_BIN:-conductor}" task result --verdict needs_changes --finding "major internal/api/users.go:42 the handler ignores the error"`,
		"A done, a handoff or\nthe end of your turn finishes nothing; only a submitted result does.",
		// A project manifest's tree (EN1 PR-3): its ports, its preview and
		// its state directory, in the environment.
		"CONDUCTOR_PORT_<NAME> holds each of its ports", "CONDUCTOR_PREVIEW_URL its preview", "CONDUCTOR_STATE_DIR (.conductor/state)",
		`"${CONDUCTOR_BIN:-conductor}" task send --to docs "the API is under internal/api/users.go; the handler names are final"`,
		`"${CONDUCTOR_BIN:-conductor}" task inbox`,
		`"${CONDUCTOR_BIN:-conductor}" task ack 3`,
		`"${CONDUCTOR_BIN:-conductor}" task blocked --reason "no token for the registry" --message "Set NPM_TOKEN in the worktree's .env, then answer here"`,
		`"${CONDUCTOR_BIN:-conductor}" task checkpoint --done "handlers and tests" --remaining "the docs" --notes "the docs page is docs/api.md"`,
		"most 50 not acknowledged",
		// Decisions (DR1 PR-6): when a question is one rather than ask or
		// task blocked, that the request answers at once, how the answer
		// arrives, and what each id, receipt and ref it names is.
		`"${CONDUCTOR_BIN:-conductor}" task decide --type approval --question "Drop the legacy table?" --option "Drop it: frees 2 GB" --option "Keep it" --recommend 0 --authorizes "running migration 42" --evidence "task:$CONDUCTOR_RUN/$CONDUCTOR_TASK"`,
		"is a\ndecision: ask it with decision_request or task decide, not with ask or task\nblocked, whose question lasts only as long as this session.",
		"does not count an attempt that ends while it waits",
		// What each kind does before it asks (#689, review round 3): a
		// work task commits and saves a checkpoint; a review, which can do
		// neither, asks without them, as its own example shows.
		"a review is a task whose task.md says\n\"Do not commit\" under How to finish; any other is a work task.",
		"- A work task commits its work and saves a checkpoint first (task\n  checkpoint, below)",
		"- A review commits nothing and saves no checkpoint (task checkpoint\n  refuses a review): it asks as it is, about the candidate it reviews,\n  which is its HEAD. No checkpoint is needed for a decision:",
		`"${CONDUCTOR_BIN:-conductor}" task decide --type clarification --question "Must the v1 routes stay as they are?" --evidence "task:$CONDUCTOR_RUN/$CONDUCTOR_TASK"`,
		"(a work\n  task's at your checkpoint, a review's at its candidate)",
		// Only the task's own decisions answer, and a withdrawal refused
		// not_held is the attempt's end (#637, review round 3).
		"These commands take your\n  own task's decisions only: another task's id is answered exactly as an\n  id no decision has.",
		"A withdrawal refused not_held means your run was\n  continued: the decision stays open in the run that continues this one,\n  which waits on it. Stop there and end your turn, as this attempt's work\n  is over; submit nothing and ask nothing more.",
		// Every id and ref a command prints, named; the go-on rule (applied
		// alone); and, for each state a decision reaches, what the agent
		// sees and does (the lines themselves are tied to the engine's
		// words by crew's TestTheSkillQuotesTheEngine).
		"\"dr-7 open.\n  Pending. ...\". dr-7 is the decision's id", `"${CONDUCTOR_BIN:-conductor}" task decision dr-7`, `"${CONDUCTOR_BIN:-conductor}" task withdraw dr-7`,
		"rev counts the\n  decision's changes and is nothing you send",
		"never\n  submit your result until its answer is applied", "Go on only once a decision is applied:\n  open and answered (the response recorded, not yet applied: it may still\n  be superseded or cancelled instead) still wait; superseded, cancelled\n  and expired mean no answer will come.",
		// A list's answer is without its rationale: an applied decision is
		// read whole before acting; a failed decide is looked for before
		// asking again (#689, review round 2).
		"read an applied decision whole (task decision dr-7) before you act\n  on it", "list them with task decision before you ask again, or you wait on two",
		"give a requestId of your own: the same request with it\n  again replies with the same decision, never a second",
		"dr-7/1 is the receipt of\n  the first response to dr-7, nothing you cite", "it overrides your recommendation",
		"Ask again\n  with what was asked for in --context and --supersedes dr-7", "Take it as a no", "Never act\n  on its answer",
		"Nothing is typed for the operator's comment or a re-route: keep\n  waiting.", "If the run stops or your session ends while you wait", "Decisions section says each decision's state",
		"Options are\n  numbered from 0", "answered Done or Not done", "what a yes --authorizes",
		"Only refs to your own\n  work are taken", "task:RUN/TASK", "attempt:RUN/TASK/N", "check:RUN/TASK/N/CHECK/K", "commit:RUN/SHA (the full id", "message:RUN/SEQ", "plan:KEY-n/REV (REV the number of the plan\n  revision that launched it, from the launch package, not an attempt's)",
		// Each number is the cited record's own (#689, review round 2): N
		// the attempt the record belongs to, a check's an earlier one's.
		"N is the number of the attempt\n  the record belongs to: CONDUCTOR_ATTEMPT for your own, an earlier one's", "so a\n  check's N is an earlier attempt's, CHECK the check's id and K which run\n  of it on that attempt, from 1.",
		"comment:KEY-n/ID and launch:ID", "ticket:KEY-n", "CONDUCTOR_SESSION_ID", "\"evidence[0]: REF resolves to nothing\"",
		"artifact:PATH", "candidate:RUN/TASK/N@COMMIT",
		// The ticket the run is linked to (DS1 PR-12): read it, comment on
		// it, nothing more.
		`"${CONDUCTOR_BIN:-conductor}" task ticket`,
		`"${CONDUCTOR_BIN:-conductor}" task ticket comment "the cause is the cache key, which ignores the locale; the fix is in the handler"`,
		"with the ticket_read\ntool or the command below; a run with no ticket says so (no_ticket)", "with the ticket_comment tool",
		"You read the ticket and comment on it, nothing more: you cannot change\n  it, close it or make another.",
		// A run launched from a ticket (DS2 PR-13): where the package is,
		// that the run follows it, and that a change is a person's to decide.
		".conductor/ticket.md", ".conductor/package.json", ".conductor/attachments/",
		"Your\n  run follows that package.", "a change that matters to your work is a\n  person's to decide: tell them, with a comment or task blocked, and never\n  act on it alone.",
		"ever call notify or crew outside a Conductor session; the commands exit silently there",
		"CONDUCTOR_AGENT", "most 2 an hour", "at most 12\n  members",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if n := strings.Count(body, " notify --"); n != 7 {
		t.Errorf("%d notify commands, want the 7 above", n)
	}
	// The ticket's section comes after the task's and before the rules,
	// once, and the marker stays the first line after the frontmatter
	// (above).
	task, ticket, rules := strings.Index(body, "\n## Your task\n"), strings.Index(body, "\n## Your ticket\n"), strings.Index(body, "\n## Rules\n")
	if strings.Count(body, "\n## Your ticket\n") != 1 || task < 0 || !(task < ticket && ticket < rules) {
		t.Errorf("the ticket's section is not between the task's and the rules: %d %d %d", task, ticket, rules)
	}
	// The decisions' section follows the task's, once, before the messages'.
	decisions, messages := strings.Index(body, "\n## Decisions\n"), strings.Index(body, "\n## Messages, blockers and checkpoints\n")
	if strings.Count(body, "\n## Decisions\n") != 1 || !(task < decisions && decisions < messages) {
		t.Errorf("the decisions' section is not between the task's and the messages': %d %d %d", task, decisions, messages)
	}
	// No number is said to be CONDUCTOR_ATTEMPT for every ref (#689,
	// review round 2): a plan's revision and a check's attempt are not.
	if strings.Contains(body, "N and SESSION are") {
		t.Error("the skill says every ref's N is CONDUCTOR_ATTEMPT")
	}
	// A file: ref is outside an asker's grant (#683): never offered.
	if strings.Contains(body, "file:SESSION") {
		t.Error("the skill offers a file: ref, which a decision refuses")
	}
	// No checkpoint asked of every asker (#689, review round 3): a review
	// cannot save one, and the Decisions section asks it of a work task.
	if section := body[decisions:messages]; strings.Contains(section, "Commit and save a\ncheckpoint first") || strings.Count(section, "saves a checkpoint first") != 1 {
		t.Error("the Decisions section asks every task for a checkpoint before a decision")
	}
	if strings.Contains(body, "--state needs_input") {
		t.Error("the skill asks without the mark (--state needs_input)")
	}
	if strings.Contains(Skill, binPlaceholder) || !strings.HasSuffix(Skill, "\n") {
		t.Fatalf("skill:\n%s", Skill)
	}
}

// WriteAssets puts the skill in the hooks dir next to the adapters' assets,
// held to the same modes.
func TestWriteAssetsWritesTheSkill(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "skills", "conductor", "SKILL.md")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != Skill {
		t.Fatalf("%s: %v\n%s", p, err, b)
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", p, fi.Mode(), err)
	}
	for _, d := range []string{filepath.Join(dir, "skills"), filepath.Join(dir, "skills", "conductor")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s: %v %v", d, fi.Mode(), err)
		}
	}
}

// skillFor is where Install puts the skill for each agent that reads skills:
// Claude Code and Codex have their own directory, pi and Goose read the
// shared ~/.agents/skills.
var skillFor = map[string]string{
	"claude":   ".claude/skills/conductor/SKILL.md",
	"codex":    ".codex/skills/conductor/SKILL.md",
	"agy":      ".gemini/antigravity-cli/skills/conductor/SKILL.md",
	"copilot":  ".agents/skills/conductor/SKILL.md",
	"cursor":   ".agents/skills/conductor/SKILL.md",
	"opencode": ".agents/skills/conductor/SKILL.md",
	"pi":       ".agents/skills/conductor/SKILL.md",
	"omp":      ".agents/skills/conductor/SKILL.md",
	"goose":    ".agents/skills/conductor/SKILL.md",
	"amp":      ".agents/skills/conductor/SKILL.md",
	"dsh":      ".agents/skills/conductor/SKILL.md",
}

// Install copies the skill for the agents that read skills, and for no other,
// and InstallsSkill says which those are. It is part of the install: without
// it Status reads not installed, and Install puts it back, from the hooks dir
// when a server wrote it there.
func TestInstallCopiesTheSkill(t *testing.T) {
	useBin(t, "/opt/conductor")
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	for _, a := range All() {
		if rel, reads := skillFor[a.ID]; a.InstallsSkill() != reads || a.SkillPath != rel {
			t.Errorf("%s: InstallsSkill %v, SkillPath %q", a.ID, a.InstallsSkill(), a.SkillPath)
		}
		if a.Install == nil || a.ID == "dsh" {
			continue
		}
		t.Run(a.ID, func(t *testing.T) {
			home := t.TempDir()
			touched, err := a.Install(home, hooks)
			if err != nil {
				t.Fatal(err)
			}
			var skills []string
			for _, p := range touched {
				if filepath.Base(p) == "SKILL.md" {
					skills = append(skills, p)
				}
			}
			rel, reads := skillFor[a.ID]
			if !reads {
				if len(skills) != 0 {
					t.Fatalf("%s does not read skills, and got %q", a.ID, skills)
				}
				return
			}
			p := filepath.Join(home, filepath.FromSlash(rel))
			if !slices.Equal(skills, []string{p}) {
				t.Fatalf("skills written %q, want %q", skills, p)
			}
			if b, err := os.ReadFile(p); err != nil || string(b) != Skill {
				t.Fatalf("%s: %v\n%s", p, err, b)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if ok, _ := a.Status(home); ok {
				t.Fatal("status reads installed without the skill")
			}
			if again, err := a.Install(home, hooks); err != nil || !slices.Equal(again, []string{p}) {
				t.Fatalf("install after the skill was removed: %q %v", again, err)
			}
			if ok, _ := a.Status(home); !ok {
				t.Fatal("status after the skill came back")
			}
		})
	}
	// pi and Goose share the skill: the second install finds it in place.
	home := t.TempDir()
	pi, _ := Get("pi")
	goose, _ := Get("goose")
	if _, err := pi.Install(home, hooks); err != nil {
		t.Fatal(err)
	}
	touched, err := goose.Install(home, hooks)
	if err != nil || !slices.Equal(touched, under(home, installed["goose"].files[0])) {
		t.Fatalf("goose after pi: %q %v", touched, err)
	}
}

// A SKILL.md of the user's own where Conductor's would go stays theirs:
// Install leaves it by hand and does the rest, and Status reads not
// installed. A skill Conductor wrote, which carries its marker, is replaced,
// and an empty file counts as none.
func TestInstallLeavesTheUsersOwnSkillAlone(t *testing.T) {
	useBin(t, "/opt/conductor")
	a, _ := Get("claude")
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	skill := filepath.Join(home, ".claude", "skills", "conductor", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "---\nname: conductor\ndescription: my notes on the orchestra\n---\n\nMine.\n"
	os.WriteFile(skill, []byte(mine), 0o644)
	touched, err := a.Install(home, t.TempDir())
	if !errors.Is(err, ErrByHand) || !strings.Contains(err.Error(), skill) || !strings.Contains(err.Error(), "conductor skill") || !slices.Equal(touched, []string{settings}) {
		t.Fatalf("install: %q %v", touched, err)
	}
	if b, _ := os.ReadFile(skill); string(b) != mine {
		t.Fatalf("the user's skill changed:\n%s", b)
	}
	if ok, _ := a.Status(home); ok {
		t.Fatal("status reads installed with the user's skill in place")
	}

	for name, content := range map[string]string{
		"an older Conductor skill": strings.Replace(Skill, "4/7 handlers", "3/7 handlers", 1),
		"an empty file":            "",
	} {
		os.WriteFile(skill, []byte(content), 0o600)
		touched, err := a.Install(home, t.TempDir())
		if err != nil || !slices.Equal(touched, []string{skill}) {
			t.Fatalf("%s: %q %v", name, touched, err)
		}
		if b, _ := os.ReadFile(skill); string(b) != Skill {
			t.Fatalf("%s: not replaced:\n%s", name, b)
		}
		if ok, _ := a.Status(home); !ok {
			t.Fatalf("%s: status after the install", name)
		}
	}
}

// InstallSkill, what a launch runs, puts the skill where the agent reads it
// (SkillPath) and nowhere else, changes nothing the second time, keeps a
// SKILL.md of the user's own (ErrByHand) and does nothing for an agent
// without a skills directory or an unknown one. DeepSeek Harness included:
// its hooks are by hand, its skill is not.
func TestInstallSkillForEveryAdapterThatReadsSkills(t *testing.T) {
	useBin(t, "/opt/conductor")
	hooks := t.TempDir()
	for _, a := range All() {
		rel, reads := skillFor[a.ID]
		t.Run(a.ID, func(t *testing.T) {
			home := t.TempDir()
			path, changed, err := InstallSkill(a.ID, home, hooks)
			if !reads {
				if path != "" || changed || err != nil {
					t.Fatalf("%s reads no skills: %q %v %v", a.ID, path, changed, err)
				}
				if entries, _ := os.ReadDir(home); len(entries) != 0 {
					t.Fatalf("home has %v", entries)
				}
				return
			}
			want := filepath.Join(home, filepath.FromSlash(rel))
			if path != want || !changed || err != nil {
				t.Fatalf("install: %q %v %v, want %q", path, changed, err, want)
			}
			if b, err := os.ReadFile(want); err != nil || string(b) != Skill {
				t.Fatalf("%s: %v\n%s", want, err, b)
			}
			if path, changed, err := InstallSkill(a.ID, home, hooks); path != want || changed || err != nil {
				t.Fatalf("second install: %q %v %v", path, changed, err)
			}
			mine := "---\nname: conductor\ndescription: mine\n---\n\nMine.\n"
			os.WriteFile(want, []byte(mine), 0o600)
			if path, changed, err := InstallSkill(a.ID, home, hooks); path != want || changed || !errors.Is(err, ErrByHand) || !strings.Contains(err.Error(), want) {
				t.Fatalf("the user's own: %q %v %v", path, changed, err)
			}
			if b, _ := os.ReadFile(want); string(b) != mine {
				t.Fatalf("the user's skill changed:\n%s", b)
			}
		})
	}
	if path, changed, err := InstallSkill("", t.TempDir(), hooks); path != "" || changed || err != nil {
		t.Fatalf("no adapter: %q %v %v", path, changed, err)
	}
	if path, changed, err := InstallSkill("nope", t.TempDir(), hooks); path != "" || changed || err != nil {
		t.Fatalf("unknown adapter: %q %v %v", path, changed, err)
	}
}

// An agent launched with its configuration directory moved (Claude Code's
// CLAUDE_CONFIG_DIR, Codex's CODEX_HOME) reads its skills there: the skill
// goes under that directory, which is made when missing, at the path it has
// under the agent's directory in the home; a SKILL.md of the user's own
// there stays. An adapter without such a directory, an unknown one, a
// relative directory and an unset variable get nothing (#291).
func TestInstallSkillInAConfigurationDirectoryOfTheAgentsOwn(t *testing.T) {
	useBin(t, "/opt/conductor")
	hooks := t.TempDir()
	for _, tc := range []struct{ adapter, env, rel string }{
		{"claude", "CLAUDE_CONFIG_DIR", "skills/conductor/SKILL.md"},
		{"codex", "CODEX_HOME", "skills/conductor/SKILL.md"},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "own")
			getenv := func(k string) string {
				if k == tc.env {
					return dir
				}
				return ""
			}
			if got := ConfigDirOf(tc.adapter, getenv); got != dir {
				t.Fatalf("ConfigDirOf: %q, want %q", got, dir)
			}
			want := filepath.Join(dir, filepath.FromSlash(tc.rel))
			path, changed, err := InstallSkillIn(tc.adapter, dir, hooks)
			if path != want || !changed || err != nil {
				t.Fatalf("install: %q %v %v, want %q", path, changed, err, want)
			}
			if b, err := os.ReadFile(want); err != nil || string(b) != Skill {
				t.Fatalf("%s: %v\n%s", want, err, b)
			}
			if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
				t.Fatalf("the directory made: %v %v", fi, err)
			}
			if path, changed, err := InstallSkillIn(tc.adapter, dir, hooks); path != want || changed || err != nil {
				t.Fatalf("second install: %q %v %v", path, changed, err)
			}
			mine := "---\nname: conductor\ndescription: mine\n---\n\nMine.\n"
			os.WriteFile(want, []byte(mine), 0o600)
			if path, changed, err := InstallSkillIn(tc.adapter, dir, hooks); path != want || changed || !errors.Is(err, ErrByHand) {
				t.Fatalf("the user's own: %q %v %v", path, changed, err)
			}
			if b, _ := os.ReadFile(want); string(b) != mine {
				t.Fatalf("the user's skill changed:\n%s", b)
			}
			// Unset, or relative: the launch leaves it to the agent.
			if got := ConfigDirOf(tc.adapter, func(string) string { return "" }); got != "" {
				t.Fatalf("unset: %q", got)
			}
			if got := ConfigDirOf(tc.adapter, func(string) string { return "own" }); got != "" {
				t.Fatalf("relative: %q", got)
			}
			if path, changed, err := InstallSkillIn(tc.adapter, "own", hooks); path != "" || changed || err != nil {
				t.Fatalf("relative install: %q %v %v", path, changed, err)
			}
		})
	}
	always := func(string) string { return "/abs/own" }
	for _, id := range []string{"", "nope", "aider", "goose", "pi"} {
		if got := ConfigDirOf(id, always); got != "" {
			t.Fatalf("%q has a configuration directory: %q", id, got)
		}
		if path, changed, err := InstallSkillIn(id, t.TempDir(), hooks); path != "" || changed || err != nil {
			t.Fatalf("%q: %q %v %v", id, path, changed, err)
		}
	}
	if got := ConfigDirOf("claude", nil); got != "" {
		t.Fatalf("no environment: %q", got)
	}
}

// SkillFile names the skill under an absolute hooks dir, and nothing under
// a relative one or none.
func TestSkillFile(t *testing.T) {
	if got := SkillFile("/data/hooks"); got != filepath.Join("/data/hooks", "skills", "conductor", "SKILL.md") {
		t.Fatalf("got %q", got)
	}
	for _, dir := range []string{"", "hooks", "./hooks"} {
		if got := SkillFile(dir); got != "" {
			t.Fatalf("%q: got %q", dir, got)
		}
	}
}

// MCPFor registers Conductor's MCP server with the agents that take one at
// launch, Claude Code by a config file and Codex by config overrides, and
// with no other; a relative hooks dir gives nothing.
func TestMCPForEachAdapter(t *testing.T) {
	useBin(t, "/opt/conductor")
	dir := t.TempDir()
	want := map[string][]string{
		"claude": {"--mcp-config", filepath.Join(dir, "claude-mcp.json")},
		"codex":  {"-c", `mcp_servers.conductor.command="/opt/conductor"`, "-c", `mcp_servers.conductor.args=["mcp"]`, "-c", "mcp_servers.conductor.env_vars=" + codexMCPEnv, "-c", "mcp_servers.conductor.tool_timeout_sec=720"},
	}
	for _, a := range All() {
		got := MCPFor(a.ID, dir)
		if !slices.Equal(got, want[a.ID]) {
			t.Errorf("%s: %q, want %q", a.ID, got, want[a.ID])
		}
	}
	if got := MCPFor("claude", "hooks"); got != nil {
		t.Fatalf("relative hooks dir: %q", got)
	}
	if got := MCPFor("nope", dir); got != nil {
		t.Fatalf("unknown adapter: %q", got)
	}
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(hooks, "claude-mcp.json"))
	if err != nil || !strings.Contains(string(b), `"command": "/opt/conductor"`) || !strings.Contains(string(b), `"mcp"`) {
		t.Fatalf("claude-mcp.json: %v\n%s", err, b)
	}
}

// Codex starts its MCP server with an environment of its own, so the
// adapter names every variable the server needs to find its session: the
// notify URL, token and session id among them (#354).
func TestCodexPassesTheSessionsVariablesToItsMCPServer(t *testing.T) {
	for _, v := range []string{"CONDUCTOR_SESSION_ID", "CONDUCTOR_NOTIFY_URL", "CONDUCTOR_NOTIFY_TOKEN", "CONDUCTOR_RUN", "CONDUCTOR_MEMBER", "CONDUCTOR_TASK", "CONDUCTOR_ATTEMPT"} {
		if !strings.Contains(codexMCPEnv, `"`+v+`"`) {
			t.Errorf("Codex's MCP server is not given %s", v)
		}
	}
	if strings.Contains(codexMCPEnv, "=") {
		t.Error("a value went into the override: names only")
	}
}

// The skill's Brakeman section is one short section, between the
// ticket's and the rules, of at most six lines: CONDUCTOR_BRAKEMAN names
// a turn, guide comes first, the request, observe and one brief, and
// nothing written becomes work; the skill carries nothing else of
// Brakeman's, so no agent carries a large static prompt.
func TestTheSkillsBrakemanSectionIsShort(t *testing.T) {
	const head = "\n## If you are Brakeman\n"
	if strings.Count(Skill, head) != 1 {
		t.Fatal("no Brakeman section, or more than one")
	}
	start := strings.Index(Skill, head) + len(head)
	end := strings.Index(Skill[start:], "\n## ")
	if end < 0 || strings.Index(Skill, "\n## Your ticket\n") > start || !strings.HasPrefix(Skill[start+end:], "\n## Rules\n") {
		t.Fatal("the Brakeman section is not between the ticket's and the rules")
	}
	section := Skill[start : start+end]
	var lines []string
	for _, l := range strings.Split(section, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 6 {
		t.Fatalf("%d lines, at most 6:\n%s", len(lines), section)
	}
	for _, want := range []string{"CONDUCTOR_BRAKEMAN is set", "Call guide first", "brakeman_request", "observe", "brief_submit", "evidence", "Nothing you write becomes work", "act", "granted", "one brief", "your turn ends"} {
		if !strings.Contains(section, want) {
			t.Errorf("the section does not say %q", want)
		}
	}
	if n := strings.Count(Skill, "Brakeman"); n != strings.Count(section, "Brakeman")+1 {
		t.Errorf("Brakeman is named outside its section (%d times in the skill)", n)
	}
}

// The skill's "Your task" section says the findings to address may come
// from a person, with their ids, lines and the resolution asked for; that
// addressed says which changes were done, by id, as a claim; and that a
// question is answered in the summary (RV1 PR-5).
func TestSkillSaysFindingsMayComeFromAPerson(t *testing.T) {
	start := strings.Index(Skill, "\n## Your task\n")
	end := strings.Index(Skill, "\n## Decisions\n")
	if start < 0 || end < start {
		t.Fatal("no Your task section ahead of Decisions")
	}
	section := strings.Join(strings.Fields(Skill[start:end]), " ")
	for _, want := range []string{
		"The findings to address may come from a person",
		"each has its id, its lines as they saw them and the resolution asked for",
		"answer each question in your summary",
		"say which changes you addressed, each by its id with a note (addressed in task_result, or --addressed)",
		"a claim a person checks against your result, never the change's resolution",
		`"${CONDUCTOR_BIN:-conductor}" task result --summary "what you did; the answers" --addressed "<id>=renamed the field"`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the section does not say %q", want)
		}
	}
}

// The skill's decisions paragraph says whom a decision is for: the task
// file's "Who answers for this work" names the holders, and --for (for,
// in decision_request) names the one the decision is for, the operator
// answering for them (docs/plans/decisions.md, HM1, PR-12).
func TestSkillSaysWhomADecisionIsFor(t *testing.T) {
	start := strings.Index(Skill, "\n## Decisions\n")
	if start < 0 {
		t.Fatal("no Decisions section")
	}
	section := strings.Join(strings.Fields(Skill[start:]), " ")
	want := `Your task file's "Who answers for this work" names who holds each responsibility; --for (for, in decision_request) names the one the decision is for, role:architecture or person:nate, and the operator answers it for them.`
	if !strings.Contains(section, want) {
		t.Errorf("the decisions section does not say %q", want)
	}
}
