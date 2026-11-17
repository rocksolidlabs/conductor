#!/usr/bin/env python3
# Copyright the Conductor Authors and RockSolid Labs, Inc.
# SPDX-License-Identifier: Apache-2.0
"""Brings freshly installed agent tools to where a person's machine has them, for the live tier.

The live tier (web/e2e/live.spec.ts) starts the real Claude Code and Codex in their full-screen
interfaces and was written for a machine whose owner has used both: each is signed in, past its
first-run screens, and trusts the live repository. A fresh runner has none of that, so every row
met a first-run, sign-in or trust screen (#988). This script puts that state in place before the
suite runs, for the tools whose key the environment holds:

    ANTHROPIC_API_KEY=... OPENAI_API_KEY=... python3 scripts/live_setup.py --repo DIR [--home DIR]

Claude Code, in <home>/.claude.json (merged into what is there):
  - projects[<the repository's main checkout>].hasTrustDialogAccepted = true. Documented
    (Claude Code's permissions page: the trust is keyed on the repository root; a worktree uses
    the main checkout's root, so the suite's worktrees are trusted with it).
  - hasCompletedOnboarding = true and customApiKeyResponses.approved holding the key's last
    twenty characters. NOT documented: no documented setting approves a key for an interactive
    session, and these are what the tool itself writes when a person answers. If a release
    stops honouring them the suite says so in one line (the first-run screen's words) and this
    is the place to look.

Codex, in <home>/.codex/config.toml (written only when there is none, or an empty one):
  - [projects."<the repository's root>"] trust_level = "trusted", check_for_update_on_startup
    = false and [tui] show_tooltips = false. Documented (Codex's config reference).
  - [tui] screen_reader_detection_done = true. NOT documented: without it the first start
    writes that key into the file, which the suite's row about a config left alone would see.
  - then `codex login --with-api-key`, the key on standard input, and `codex login status`.
    Documented (Codex's authentication page): an interactive session does not sign in from
    the environment's key by itself.

It prints what it did, never a key and never a file's content. A tool with no key in the
environment is left alone, and so is a Codex config a person wrote: this is for a fresh
machine, and it refuses rather than rewrite one that is not.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

CODEX_TOP = {"check_for_update_on_startup": False}
CODEX_TUI = {"show_tooltips": False, "screen_reader_detection_done": True}


def main_checkout(repo: Path) -> str:
    """The root both tools key a repository's trust on: the main checkout's, also for a linked worktree."""
    common = subprocess.run(["git", "-C", str(repo), "rev-parse", "--path-format=absolute", "--git-common-dir"],
                            capture_output=True, text=True, check=True).stdout.strip()
    root = Path(common)
    # The common directory of an ordinary repository is <root>/.git; a bare one has no checkout to trust.
    if root.name != ".git":
        raise ValueError(f"{repo} is not a checkout of a git repository")
    return str(root.parent.resolve())


def claude(home: Path, root: str, key: str) -> str:
    """Merges the trust, the finished first run and the key's approval into <home>/.claude.json."""
    path = home / ".claude.json"
    state = {}
    if path.exists() and path.read_text().strip():
        state = json.loads(path.read_text())
        if not isinstance(state, dict):
            raise ValueError(f"{path} is not a JSON object")
    state["hasCompletedOnboarding"] = True
    answers = state.setdefault("customApiKeyResponses", {})
    approved = answers.setdefault("approved", [])
    answers.setdefault("rejected", [])
    tail = key[-20:]
    if tail not in approved:
        approved.append(tail)
    state.setdefault("projects", {}).setdefault(root, {})["hasTrustDialogAccepted"] = True
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(json.dumps(state, indent=2) + "\n")
    os.chmod(path, 0o600)
    return f"claude: first run finished, the key approved, {root} trusted"


def codex_config(root: str) -> str:
    """The config a fresh Codex gets: no update check, no tooltips, the screen-reader look done, the root trusted."""
    def value(v):
        return "true" if v is True else "false" if v is False else json.dumps(v)
    lines = [f"{k} = {value(v)}" for k, v in CODEX_TOP.items()]
    lines += ["", "[tui]"] + [f"{k} = {value(v)}" for k, v in CODEX_TUI.items()]
    # A TOML basic string takes JSON's escapes for a path.
    lines += ["", f"[projects.{json.dumps(root)}]", 'trust_level = "trusted"', ""]
    return "\n".join(lines)


def codex(home: Path, root: str, key: str, program: str = "codex") -> str:
    """Writes the config of a fresh machine and signs in with the key, read from standard input by the tool."""
    path = home / ".codex" / "config.toml"
    want = codex_config(root)
    if path.exists() and path.read_text().strip():
        # Compared as text, not parsed: the runner's Python may be older than the standard library's TOML reader.
        if path.read_text().strip() != want.strip():
            raise ValueError(f"{path} holds a configuration this script did not write: it is for a fresh machine and rewrites none")
    else:
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        path.write_text(want)
    env = {k: v for k, v in os.environ.items() if k != "OPENAI_API_KEY"}
    env["HOME"] = str(home)
    done = subprocess.run([program, "login", "--with-api-key"], input=key + "\n", text=True, capture_output=True, env=env)
    if done.returncode != 0:
        raise ValueError(f"`{program} login --with-api-key` exited {done.returncode}")
    status = subprocess.run([program, "login", "status"], capture_output=True, text=True, env=env)
    if status.returncode != 0:
        raise ValueError(f"`{program} login status` exited {status.returncode} after the login")
    return f"codex: signed in with the key, {root} trusted, no update check"


def run(argv, environ, out) -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n")[0])
    ap.add_argument("--repo", required=True, help="the live repository (CONDUCTOR_E2E_LIVE_REPO)")
    ap.add_argument("--home", default=environ.get("HOME", ""), help="the home directory the tools read (default: HOME)")
    ap.add_argument("--codex", default="codex", help=argparse.SUPPRESS)
    a = ap.parse_args(argv)
    if not a.home:
        print("live_setup: no home directory", file=out)
        return 2
    home = Path(a.home)
    try:
        root = main_checkout(Path(a.repo))
        for name, variable, setup in (("claude", "ANTHROPIC_API_KEY", lambda k: claude(home, root, k)),
                                      ("codex", "OPENAI_API_KEY", lambda k: codex(home, root, k, a.codex))):
            key = environ.get(variable, "").strip()
            if not key:
                print(f"{name}: no {variable} in the environment, left alone (its rows are skipped)", file=out)
                continue
            print(setup(key), file=out)
    except (ValueError, OSError, subprocess.CalledProcessError) as e:
        print(f"live_setup: {e}", file=out)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1:], os.environ, sys.stdout))
