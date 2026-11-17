"""Tests for live_setup.py: python3 -m unittest discover -s scripts -p '*_test.py'"""
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest

import live_setup

# Shaped like a key, and nobody's: the tests look for it, whole, in everything the script prints and writes.
KEY = "sk-test-" + "A1b2C3d4" * 6 + "-LASTTWENTYCHARACTERS"
OTHER = "sk-other-" + "Z9y8X7w6" * 5


def git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True,
                   env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
                        "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"})


class Case(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        top = Path(self.tmp.name).resolve()
        self.home = top / "home"
        self.home.mkdir()
        self.repo = top / "live-repo"
        self.repo.mkdir()
        git(self.repo, "init", "-q")
        (self.repo / "README.md").write_text("# live\n")
        git(self.repo, "add", "README.md")
        git(self.repo, "commit", "-q", "-m", "init")
        # A stand-in for the Codex program: it records how it was called and what it read, and answers as told.
        self.bin = top / "bin"
        self.bin.mkdir()
        self.calls = top / "calls.jsonl"
        self.program = self.bin / "codex"
        self.program.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            f"calls = {str(self.calls)!r}\n"
            "read = sys.stdin.read() if sys.argv[1:] == ['login', '--with-api-key'] else ''\n"
            "open(calls, 'a').write(json.dumps({'argv': sys.argv[1:], 'stdin': read, 'home': os.environ.get('HOME'),\n"
            "    'key_in_env': 'OPENAI_API_KEY' in os.environ}) + '\\n')\n"
            "sys.exit(int(os.environ.get('STUB_EXIT_' + '_'.join(a.strip('-').replace('-', '_') for a in sys.argv[1:]).upper(), '0')))\n")
        self.program.chmod(0o755)

    def setup(self, env, repo=None):
        out = io.StringIO()
        code = live_setup.run(["--repo", str(repo or self.repo), "--home", str(self.home), "--codex", str(self.program)], env, out)
        return code, out.getvalue()

    def called(self):
        return [json.loads(l) for l in self.calls.read_text().splitlines()] if self.calls.exists() else []

    def everything_written(self):
        return "".join(p.read_text() for p in self.home.rglob("*") if p.is_file())


class ClaudeCode(Case):
    def test_a_fresh_home_gets_the_trust_the_finished_first_run_and_the_keys_approval(self):
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY})
        self.assertEqual(code, 0, said)
        state = json.loads((self.home / ".claude.json").read_text())
        self.assertIs(state["projects"][str(self.repo)]["hasTrustDialogAccepted"], True)
        self.assertIs(state["hasCompletedOnboarding"], True)
        self.assertEqual(state["customApiKeyResponses"], {"approved": [KEY[-20:]], "rejected": []})
        self.assertEqual(stat.S_IMODE((self.home / ".claude.json").stat().st_mode), 0o600)
        # Only the one directory is trusted: any other still asks.
        self.assertEqual(list(state["projects"]), [str(self.repo)])

    def test_what_a_person_already_has_there_is_kept(self):
        (self.home / ".claude.json").write_text(json.dumps({
            "theme": "light", "customApiKeyResponses": {"approved": [OTHER[-20:]], "rejected": ["x"]},
            "projects": {"/somewhere/else": {"hasTrustDialogAccepted": False, "history": [1]}}}))
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY})
        self.assertEqual(code, 0, said)
        state = json.loads((self.home / ".claude.json").read_text())
        self.assertEqual(state["theme"], "light")
        self.assertEqual(state["customApiKeyResponses"], {"approved": [OTHER[-20:], KEY[-20:]], "rejected": ["x"]})
        self.assertEqual(state["projects"]["/somewhere/else"], {"hasTrustDialogAccepted": False, "history": [1]})
        self.assertIs(state["projects"][str(self.repo)]["hasTrustDialogAccepted"], True)
        # A second run changes nothing.
        before = (self.home / ".claude.json").read_text()
        self.assertEqual(self.setup({"ANTHROPIC_API_KEY": KEY})[0], 0)
        self.assertEqual((self.home / ".claude.json").read_text(), before)

    def test_a_worktrees_trust_is_its_main_checkouts(self):
        tree = Path(self.tmp.name).resolve() / "tree"
        git(self.repo, "worktree", "add", "-q", str(tree))
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY}, repo=tree)
        self.assertEqual(code, 0, said)
        self.assertEqual(list(json.loads((self.home / ".claude.json").read_text())["projects"]), [str(self.repo)])

    def test_a_file_that_is_no_object_is_refused_and_left(self):
        (self.home / ".claude.json").write_text("[1, 2]")
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY})
        self.assertEqual(code, 1)
        self.assertIn("is not a JSON object", said)
        self.assertEqual((self.home / ".claude.json").read_text(), "[1, 2]")


class Codex(Case):
    def test_a_fresh_home_gets_its_config_and_is_signed_in_with_the_key_on_standard_input(self):
        code, said = self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})
        self.assertEqual(code, 0, said)
        text = (self.home / ".codex" / "config.toml").read_text()
        self.assertEqual(text, "check_for_update_on_startup = false\n\n[tui]\nshow_tooltips = false\n"
                               "screen_reader_detection_done = true\n\n"
                               f'[projects."{self.repo}"]\ntrust_level = "trusted"\n')
        try:
            import tomllib
        except ImportError:
            tomllib = None
        if tomllib:
            self.assertEqual(tomllib.loads(text), {"check_for_update_on_startup": False,
                                                   "tui": {"show_tooltips": False, "screen_reader_detection_done": True},
                                                   "projects": {str(self.repo): {"trust_level": "trusted"}}})
        calls = self.called()
        self.assertEqual([c["argv"] for c in calls], [["login", "--with-api-key"], ["login", "status"]])
        self.assertEqual(calls[0]["stdin"], KEY + "\n")
        # The key reaches the program on its standard input alone, and it reads the home it was told.
        self.assertEqual([c["key_in_env"] for c in calls], [False, False])
        self.assertEqual([c["home"] for c in calls], [str(self.home)] * 2)

    def test_a_config_a_person_wrote_is_refused_and_left_and_nothing_is_signed_in(self):
        (self.home / ".codex").mkdir()
        mine = 'model = "o"\n'
        (self.home / ".codex" / "config.toml").write_text(mine)
        code, said = self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})
        self.assertEqual(code, 1)
        self.assertIn("rewrites none", said)
        self.assertEqual((self.home / ".codex" / "config.toml").read_text(), mine)
        self.assertEqual(self.called(), [])

    def test_its_own_config_from_an_earlier_run_is_taken_as_it_is(self):
        self.assertEqual(self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})[0], 0)
        before = (self.home / ".codex" / "config.toml").read_text()
        self.assertEqual(self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})[0], 0)
        self.assertEqual((self.home / ".codex" / "config.toml").read_text(), before)

    def test_a_login_the_program_refuses_fails_the_setup(self):
        for variable, words in (("STUB_EXIT_LOGIN_WITH_API_KEY", "login --with-api-key` exited 3"), ("STUB_EXIT_LOGIN_STATUS", "login status` exited 3")):
            with self.subTest(variable):
                os.environ[variable] = "3"
                self.addCleanup(os.environ.pop, variable, None)
                code, said = self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})
                os.environ.pop(variable)
                self.assertEqual(code, 1)
                self.assertIn(words, said)

    def test_a_program_that_is_not_there_fails_the_setup_in_one_line(self):
        self.program.unlink()
        code, said = self.setup({"OPENAI_API_KEY": KEY, "PATH": os.environ["PATH"]})
        self.assertEqual(code, 1)
        errors = [l for l in said.splitlines() if l.startswith("live_setup: ")]
        self.assertEqual(len(errors), 1, said)
        self.assertEqual(said.strip().splitlines()[-1], errors[0])


class Both(Case):
    def test_a_tool_with_no_key_is_left_alone_and_said(self):
        code, said = self.setup({"PATH": os.environ["PATH"]})
        self.assertEqual(code, 0, said)
        self.assertEqual([p.name for p in self.home.iterdir()], [])
        self.assertIn("claude: no ANTHROPIC_API_KEY", said)
        self.assertIn("codex: no OPENAI_API_KEY", said)
        self.assertEqual(self.called(), [])
        # An empty secret, as a workflow passes one that is not set, is no key.
        code, said = self.setup({"ANTHROPIC_API_KEY": "", "OPENAI_API_KEY": "  ", "PATH": os.environ["PATH"]})
        self.assertEqual(code, 0, said)
        self.assertEqual([p.name for p in self.home.iterdir()], [])

    def test_no_key_is_printed_and_no_whole_key_is_written(self):
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY, "OPENAI_API_KEY": OTHER, "PATH": os.environ["PATH"]})
        self.assertEqual(code, 0, said)
        for key in (KEY, OTHER):
            self.assertNotIn(key, said)
            self.assertNotIn(key[-20:], said)
            self.assertNotIn(key, self.everything_written())
        self.assertEqual(said.count("\n"), 2, said)

    def test_a_folder_that_is_no_checkout_is_refused(self):
        code, said = self.setup({"ANTHROPIC_API_KEY": KEY}, repo=self.home)
        self.assertEqual(code, 1)
        self.assertEqual([p.name for p in self.home.iterdir()], [])


if __name__ == "__main__":
    unittest.main()
