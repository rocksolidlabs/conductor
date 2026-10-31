package crew

import (
	"slices"
	"time"
)

// Examples returns the example crews: data like any saved crew, which
// conductor serve --examples and POST /api/crews/examples seed once
// (Store.Seed) and which are edited or deleted like any other. Their members
// use the claude and codex built-ins; every crew works in cwd, the server's
// default working directory, with a worktree per member, and opens its view
// after launch. now stamps them.
//
// A member's worktree starts at the checkout's HEAD, so what an earlier
// member committed is on its branch only, crew/<run>/<member>: each member
// that starts after another begins by merging that member's branch, naming
// the run as $CONDUCTOR_RUN, which the launch puts in the member's
// environment and the agent's shell expands (ExpandPrompt expands only
// $GOAL). The notify command goes through $CONDUCTOR_BIN, as the Conductor
// skill's does, for an agent whose PATH has no conductor.
//
// The todo app with tasks is the same app run by Yardmaster (Crew.Tasks):
// each task's attempt gets a worktree made at the results it needs and a
// task file with the goal, the member's prompt as its role and the task's
// instructions, and the checks decide a result. Its checks are Python's
// unittest, a fixed command with nothing to install. Its project manifest
// has one prerequisite, python3 --version, run once before the first
// attempt, so a host without Python pauses the run with the step named
// instead of spending every task's attempts, and the run page shows the
// receipt on the first real run (EN1).
func Examples(cwd string, now time.Time) []Crew {
	crew := func(id, name, goal string, members ...Member) Crew {
		return Crew{ID: id, Name: name, Goal: goal, Cwd: cwd, Where: WhereServer, Isolation: IsolationWorktree, OpenAfterLaunch: true, Members: members, CreatedAt: now, UpdatedAt: now}
	}
	member := func(name, agent, prompt string, start Start) Member {
		return Member{Name: name, AgentID: agent, Prompt: prompt, Start: start}
	}
	first := Start{When: StartImmediately}
	after := func(m string) Start { return Start{When: StartAfter, Member: m} }
	byTask := Start{When: StartTask}
	task := func(id, member, kind, instructions string, needs []string, checks ...Check) Task {
		return Task{ID: id, Member: member, Kind: kind, Instructions: instructions, Needs: needs, Checks: checks}
	}
	// The suite as every task after the plan runs it: the tests under tests/, imported from the top of the repository.
	unittest := []string{"python3", "-m", "unittest", "discover", "-s", "tests", "-t", "."}
	tasks := crew("example-todo-app-tasks", "Example: todo app, with tasks",
		"Build a small command-line todo app in this repository (meant for a fresh repository with one commit: point the crew's working directory at one before launching, not at a real project): add, list and done commands, items kept in a JSON file in the user's home directory (the path in TODO_FILE when it is set), with tests, in Python with its standard library only",
		member("lead", "claude", "You lead the crew: you plan the todo app and write no code of it.", byTask),
		member("core", "claude", "You build the todo app's core, storage and the data model, then put it together with the cli and make the whole suite pass, production code and tests alike.", byTask),
		member("cli", "codex", "You build the todo app's command-line interface against the interface the plan gives core.", byTask),
		member("reviewer", "codex", "You review the finished todo app against its plan and the goal, and change nothing.", byTask),
	)
	tasks.Tasks = []Task{
		task("plan", "lead", TaskWork, "Write PLAN.md at the top of the repository: the data model, the command-line interface (add, list and done, their output and exit codes), the file layout and the split into two parts. core owns todo/store.py: a Store class over the JSON file and the item type, with the file's path from TODO_FILE, else ~/.todo.json. cli owns todo/cli.py, whose main(argv, store) takes the store as an argument so that cli is built and tested without core, and todo/__main__.py, which makes the Store and calls main. Give core's interface exactly (names, arguments, return values, the error for an unknown id): cli writes against it while core is written. Tests go in tests/test_*.py, run from the top of the repository by python3 -m unittest discover -s tests -t . (core's in tests/test_store.py, cli's in tests/test_cli.py with a fake store, the end-to-end ones in tests/test_end_to_end.py); a test never touches the real home: it sets TODO_FILE to a file in a temporary directory. Commit PLAN.md with the empty todo/__init__.py and tests/__init__.py and a .gitignore for __pycache__/ and *.pyc, so that both parts start from them and running the tests leaves the worktree clean.", nil,
			Check{ID: "plan", Argv: []string{"test", "-s", "PLAN.md"}}),
		task("core", "core", TaskWork, "Build core as PLAN.md gives it: todo/store.py with exactly the interface PLAN.md names, and its unit tests in tests/test_store.py against a TODO_FILE in a temporary directory. Touch no file PLAN.md gives to cli.", []string{"plan"},
			Check{ID: "tests", Argv: unittest}),
		task("cli", "cli", TaskWork, "Build cli as PLAN.md gives it: todo/cli.py and todo/__main__.py, against the interface PLAN.md gives core, which is being written at the same time and is not in your worktree. Test main(argv, store) in tests/test_cli.py with a fake store written in the test file; import nothing from todo/store.py there or in todo/cli.py. Touch no file PLAN.md gives to core.", []string{"plan"},
			Check{ID: "tests", Argv: unittest}),
		task("tests", "core", TaskWork, "Put the two parts together: your worktree starts at core's result; merge cli's branch (named under Inputs) with git merge --no-edit and resolve any conflict. Add tests/test_end_to_end.py, which runs python3 -m todo in a subprocess with TODO_FILE in a temporary directory: add, list, done, done of an unknown id, and list with nothing in it. Fix whatever the merged app gets wrong, in either part, with a test for each fix. When the review asks for changes, this task is done again: address each finding, in whichever file it names.", []string{"core", "cli"},
			Check{ID: "suite", Argv: unittest}),
		task("review", "reviewer", TaskReview, "Review the app at HEAD against PLAN.md and the goal: run python3 -m unittest discover -s tests -t . and each command by hand with TODO_FILE set to a file in a temporary directory, and read the code. Approve when the app does what PLAN.md and the goal say and the tests cover it; otherwise ask for changes, with a finding for each problem naming its file and line.", []string{"tests"}),
	}
	tasks.Manifest = &Manifest{Prerequisites: []Check{{ID: "python", Argv: []string{"python3", "--version"}}}}
	list := []Crew{
		crew("example-todo-app", "Example: todo app",
			"Build a small command-line todo app in this repository (meant for a fresh repository with one commit: point the crew's working directory at one before launching, not at a real project): add, list and done commands, items kept in a JSON file in the user's home directory, with tests",
			member("lead", "claude", "You lead a crew of four on this goal: $GOAL. Plan, do not build. Write PLAN.md at the top of the repository with the data model, the command-line interface and the file layout, then split the work into two parts, core (storage and the data model) and cli (the commands), naming the files each part owns and the interface between them. Commit PLAN.md on your branch and stop: the builders merge your branch and start when you report done.", first),
			member("core", "claude", "Start by merging the lead's branch, which holds PLAN.md: git merge --no-edit crew/$CONDUCTOR_RUN/lead (your run is in $CONDUCTOR_RUN). Then build the core part of PLAN.md for this goal: $GOAL. Work only in the files PLAN.md gives to core, with unit tests, and commit as you go. Report done when the storage and the data model are complete and their tests pass.", after("lead")),
			member("cli", "codex", "Start by merging the lead's branch, which holds PLAN.md: git merge --no-edit crew/$CONDUCTOR_RUN/lead (your run is in $CONDUCTOR_RUN). Then build the cli part of PLAN.md for this goal: $GOAL. Work only in the files PLAN.md gives to cli, against the interface PLAN.md says core exposes, and commit as you go. Report done when every command works end to end.", after("lead")),
			// A start condition names one member: the tester starts after cli
			// and waits for core's branch itself.
			member("tester", "codex", "Start by merging the lead's and cli's branches: git merge --no-edit crew/$CONDUCTOR_RUN/lead, then git merge --no-edit crew/$CONDUCTOR_RUN/cli (your run is in $CONDUCTOR_RUN). You test the todo app built for this goal: $GOAL. You start once cli reports done; core may still be at work: wait until crew/$CONDUCTOR_RUN/core holds the core part PLAN.md describes, then git merge --no-edit crew/$CONDUCTOR_RUN/core, run the whole test suite, add the tests PLAN.md asks for that are missing, and change test code only. Hand a bug in the app to its owner with: \"${CONDUCTOR_BIN:-conductor}\" notify --event handoff --to core --message \"what and where\" (or --to cli). Report done when the suite is green.", after("cli")),
		),
		crew("example-test-fixer", "Example: test fixer",
			"Make this repository's test suite pass without weakening a test",
			member("triage", "claude", "Run this repository's test suite for this goal: $GOAL. Write TRIAGE.md at the top of the repository: every failing test, its cause as far as you can tell, grouped by the production file to change. Commit it on your branch and report done. Fix nothing yourself.", first),
			member("fixer", "codex", "Start by merging the triage branch, which holds TRIAGE.md: git merge --no-edit crew/$CONDUCTOR_RUN/triage (your run is in $CONDUCTOR_RUN). Then fix the failures TRIAGE.md lists, for this goal: $GOAL. Change production code before tests; never delete, skip or loosen a test. Commit once per group in TRIAGE.md and report done when the whole suite passes.", after("triage")),
		),
		crew("example-docs-writer", "Example: docs writer",
			"Write a README for this repository that a new contributor can build, run and configure from",
			member("reader", "claude", "Read this repository for this goal: $GOAL: the build files, the entry points, the configuration and the tests. Write NOTES.md at the top of the repository with what the project does, how it is built and run, and how it is configured, every fact with the file it comes from. Commit it on your branch and report done.", first),
			member("writer", "codex", "Start by merging the reader's branch, which holds NOTES.md: git merge --no-edit crew/$CONDUCTOR_RUN/reader (your run is in $CONDUCTOR_RUN). Then write README.md from NOTES.md, for this goal: $GOAL: what it is, a quick start, configuration, development. Keep every command exactly as it is run; mark anything NOTES.md does not establish as an open question rather than guessing. Commit it and report done.", after("reader")),
		),
		crew("example-dependency-upgrade", "Example: dependency upgrade",
			"Upgrade this repository's direct dependencies to their latest compatible versions, one at a time, with the test suite passing after each",
			member("scout", "codex", "Survey this repository's direct dependencies for this goal: $GOAL. Write UPGRADES.md at the top of the repository: each dependency with its current and latest versions, the ones furthest behind first, and a note on any whose release notes announce a breaking change. Commit it on your branch and report done. Upgrade nothing yourself.", first),
			member("upgrader", "claude", "Start by merging the scout's branch, which holds UPGRADES.md: git merge --no-edit crew/$CONDUCTOR_RUN/scout (your run is in $CONDUCTOR_RUN). Then upgrade the dependencies UPGRADES.md lists, for this goal: $GOAL, one dependency per commit, running the test suite after each and fixing what the upgrade breaks. Skip, and note in UPGRADES.md, any that cannot be made to pass. Report done with the list of what moved.", after("scout")),
		),
	}
	// Beside the todo app, so the two are compared.
	return slices.Insert(list, 1, tasks)
}
