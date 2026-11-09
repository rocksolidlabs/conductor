# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 the Conductor Authors and RockSolid Labs, Inc.
"""Every check CI runs, run locally: `make preflight`.

`python3 scripts/preflight.py` runs, from a clean committed worktree, every
check `ci.yml` runs, on this machine (linux) and, when asked with
`--machines`, on the Mac over SSH (mac) and on the Windows host through WSL
interop (windows). It writes one receipt per machine against the head SHA
under `<git common dir>/preflight/`: the record of what ran where, with a log
per step. CI stays the gate (AGENTS.md); this is how to see everything it
would see before a push, without a pull request.

The remote machines get the revision pushed to them directly (a `preflight`
branch in their checkout, checked out detached), never through GitHub. No
machine address is written here: the Mac is the `mac` host of ~/.ssh/config,
the Windows host is the one this WSL runs on.

A step that fails, a machine that cannot be reached, or a run of a subset of
the steps (--only) is a failed or partial receipt: nothing is skipped
silently. A run of a subset still checks the revision out on each remote
machine: the steps named test the head, never the machine's last checkout.

`origin/main` is fetched before anything runs, a subset too. The `dco` step
judges `origin/main..HEAD`, and a ref behind GitHub's main puts main's own
commits in that range: red for commits that are not the branch's, with a fix
that would replay them as the branch's. So the base the receipt names is
GitHub's main at the run's start, and a fetch that fails stops the run.
"""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import time

MACHINES = ("linux", "mac", "windows")
RECEIPT_VERSION = 1

# The checks of ci.yml, by job, as this machine runs them; `covers` names the
# CI job, so that a job added to ci.yml without a step here is noticed
# (preflight_test.py). GATE_JOBS run nothing of the repository (the one
# check branch protection requires, green when every other job passed).
# The order is CI's: the web build first, since the Go binary embeds it.
GATE_JOBS = ("required-checks",)
# The whole module's race tests, as ci.yml, make test and docs/checks.md run
# them (preflight_test.py holds them to one bound). The bound catches a hang:
# go test's default, ten minutes a package, is less than internal/api takes
# on a machine shared with other runs.
GO_TEST_RACE = ["go", "test", "-race", "-count=1", "-timeout", "60m", "./..."]
LINUX_STEPS = [
    # First: it takes a second, the rest an hour, so an unsigned commit is the
    # first line of the receipt (CONTRIBUTING.md, Sign-off). origin/main is
    # fetched before the run starts (main).
    {"name": "dco", "covers": "dco", "argv": ["python3", "scripts/dco_check.py", "--base", "origin/main", "--head", "HEAD"]},
    {"name": "web-typecheck", "covers": "web", "argv": ["make", "web-typecheck"]},
    {"name": "web-test", "covers": "web", "argv": ["npm", "--prefix", "web", "test"]},
    {"name": "web-build", "covers": "web", "argv": ["make", "web-build"]},
    {"name": "lint", "covers": "go", "argv": ["make", "lint"]},
    {"name": "lint-static", "covers": "go", "argv": ["make", "lint-static"]},
    {"name": "go-test-race", "covers": "go", "argv": [*GO_TEST_RACE]},
    {
        "name": "build-linux-arm64",
        "covers": "build-matrix",
        "argv": ["make", "build-go"],
        "env": {"GOOS": "linux", "GOARCH": "arm64"},
    },
    {"name": "build-go", "covers": "go", "argv": ["make", "build-go"]},
    {"name": "check-version", "covers": "go", "argv": ["make", "check-version"]},
    {"name": "brand-assets", "covers": "go", "argv": ["python3", "scripts/brand_assets.py", "--check"]},
    {
        "name": "scripts-tests",
        "covers": "go",
        "argv": ["python3", "-m", "unittest", "discover", "-s", "scripts", "-p", "*_test.py"],
    },
    # CI installs the desktop shell's packages (npm ci) before the notices check
    # and the desktop tests; a worktree starts without them.
    {"name": "desktop-install", "covers": "desktop-test", "argv": ["make", "desktop-install"]},
    {"name": "notices", "covers": "go", "argv": ["python3", "scripts/notices.py", "--check"]},
    {"name": "vuln", "covers": "vuln", "argv": ["make", "vuln"]},
    {"name": "e2e", "covers": "e2e", "argv": ["make", "test-e2e"]},
    {"name": "pebble", "covers": "pebble", "argv": ["make", "test-pebble"]},
    {"name": "desktop-test", "covers": "desktop-test", "argv": ["make", "desktop-test"]},
    {"name": "desktop-e2e", "covers": "desktop-smoke", "argv": ["make", "desktop-e2e"], "display": True},
]

# On the Mac: the Go tests on Darwin (its PTYs are not Linux's), the binary,
# and CI's desktop-test row for macOS. On Windows: CI's desktop-test row.
MAC_STEPS = [
    {"name": "lint", "argv": ["make", "lint"]},
    {"name": "go-test-race", "argv": [*GO_TEST_RACE]},
    {"name": "build-go", "argv": ["make", "build-go"]},
    {"name": "check-version", "argv": ["make", "check-version"]},
    {"name": "desktop-install", "argv": ["npm", "--prefix", "desktop", "ci"]},
    {"name": "desktop-test", "argv": ["make", "desktop-test"]},
]
WINDOWS_STEPS = [
    {"name": "desktop-install", "argv": ["npm", "ci"], "cwd": "desktop"},
    {"name": "desktop-typecheck", "argv": ["npm", "run", "typecheck"], "cwd": "desktop"},
    {"name": "desktop-test", "argv": ["npm", "test"], "cwd": "desktop"},
]

MAC_HOST = os.environ.get("PREFLIGHT_MAC_HOST", "mac")
MAC_REPO = os.environ.get("PREFLIGHT_MAC_REPO", "code/conductor")  # under $HOME on the Mac
WINDOWS_REPO = os.environ.get("PREFLIGHT_WINDOWS_REPO", "conductor-preflight")  # under %USERPROFILE%


class Error(Exception):
    pass


def git(*args, cwd=None):
    p = subprocess.run(["git", *args], capture_output=True, text=True, cwd=cwd)
    if p.returncode:
        raise Error(p.stderr.strip() or "git failed")
    return p.stdout.strip()


def receipt_dir():
    return Path(git("rev-parse", "--path-format=absolute", "--git-common-dir")) / "preflight"


def receipt_path(directory, head, machine):
    return Path(directory) / f"{head}.{machine}.json"


def load_receipt(directory, head, machine):
    path = receipt_path(directory, head, machine)
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return None


def receipt_passes(receipt, head, machine):
    """Whether a receipt is a complete passing run of machine at head."""
    return (
        isinstance(receipt, dict)
        and receipt.get("version") == RECEIPT_VERSION
        and receipt.get("head") == head
        and receipt.get("machine") == machine
        and receipt.get("ok") is True
        and receipt.get("partial") is False
        and isinstance(receipt.get("steps"), list)
        and len(receipt["steps"]) > 0
        and all(s.get("exit") == 0 for s in receipt["steps"])
    )


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds")


class Run:
    def __init__(self, root, head, base, machine, directory, only=None, log=print):
        self.root, self.head, self.base, self.machine = root, head, base, machine
        self.directory = Path(directory)
        self.only = set(only or [])
        self.logdir = self.directory / f"{head}.{machine}"
        self.logdir.mkdir(parents=True, exist_ok=True)
        self.steps = []
        self.started = now()
        self.say = log

    def step(self, name, argv, runner, cwd=None, env=None, always=False):
        """Run one step through runner(argv, cwd, env, logfile) -> exit code.
        A step not named by --only is skipped, unless always: a remote
        machine's checkout, which is no check but getting the revision there
        (a partial run tests the head it names, never the machine's last
        checkout)."""
        if self.only and name not in self.only and not always:
            return True
        logfile = self.logdir / f"{name}.log"
        self.say(f"[{self.machine}] {name}: {' '.join(argv)}")
        t0 = time.monotonic()
        try:
            code = runner(argv, cwd, env, logfile)
        except Error as e:
            logfile.write_text(str(e) + "\n")
            code = -1
        seconds = round(time.monotonic() - t0, 1)
        self.steps.append(
            {"name": name, "command": " ".join(argv), "cwd": cwd, "exit": code, "seconds": seconds, "log": str(logfile)}
        )
        self.say(f"[{self.machine}] {name}: {'ok' if code == 0 else 'FAILED (' + str(code) + ')'} in {seconds}s")
        return code == 0

    def write(self, extra=None):
        ok = bool(self.steps) and all(s["exit"] == 0 for s in self.steps)
        receipt = {
            "version": RECEIPT_VERSION,
            "head": self.head,
            "base": self.base,
            "machine": self.machine,
            "started": self.started,
            "finished": now(),
            "ok": ok and not self.only,
            "partial": bool(self.only),
            "steps": self.steps,
        }
        receipt.update(extra or {})
        path = receipt_path(self.directory, self.head, self.machine)
        path.write_text(json.dumps(receipt, indent=2) + "\n")
        return receipt


WSLG_X_SOCKET = "/tmp/.X11-unix/X0"


def display_env(environ=None, socket=WSLG_X_SOCKET):
    """The environment a step that opens a window needs: under WSLg, DISPLAY
    pointing at its X server (a shell's DISPLAY often names an external X
    server that is not there); elsewhere whatever DISPLAY or WAYLAND_DISPLAY
    the environment has. None when there is no display at all (CI uses
    xvfb-run)."""
    environ = os.environ if environ is None else environ
    if os.path.exists(socket):
        return {"DISPLAY": ":0"}
    if environ.get("DISPLAY") or environ.get("WAYLAND_DISPLAY"):
        return {}
    return None


def local_runner(root):
    def run(argv, cwd, env, logfile):
        e = dict(os.environ)
        e.update(env or {})
        with open(logfile, "w") as f:
            p = subprocess.run(argv, cwd=root / cwd if cwd else root, env=e, stdout=f, stderr=subprocess.STDOUT)
        return p.returncode

    return run


def run_linux(root, head, base, directory, only, say):
    r = Run(root, head, base, "linux", directory, only, say)
    runner = local_runner(root)
    for s in LINUX_STEPS:
        env = s.get("env")
        if s.get("display"):
            display = display_env()
            if display is None:
                r.steps.append(
                    {"name": s["name"], "command": " ".join(s["argv"]), "cwd": None, "exit": -1, "seconds": 0, "log": "no display: the Electron smoke needs WSLg's X server, DISPLAY or WAYLAND_DISPLAY (CI uses xvfb-run)"}
                )
                say(f"[linux] {s['name']}: FAILED (no display)")
                continue
            env = {**(env or {}), **display}
        r.step(s["name"], s["argv"], runner, s.get("cwd"), env)
    versions = {}
    for tool, argv in (("go", ["go", "version"]), ("node", ["node", "-v"])):
        try:
            versions[tool] = subprocess.run(argv, capture_output=True, text=True).stdout.strip()
        except OSError:
            versions[tool] = "missing"
    return r.write({"tools": versions})


def ssh_runner(host, repo):
    """Run argv on host, in $HOME/repo, through a login zsh (the Mac's PATH)."""

    def run(argv, cwd, env, logfile):
        where = repo + ("/" + cwd if cwd else "")
        inner = " ".join(shlex.quote(a) for a in argv)
        if env:
            inner = " ".join(f"{k}={shlex.quote(v)}" for k, v in env.items()) + " " + inner
        script = f"cd ~/{shlex.quote(where)} && {inner}"
        with open(logfile, "w") as f:
            p = subprocess.run(
                ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, "zsh", "-lc", shlex.quote(script)],
                stdout=f,
                stderr=subprocess.STDOUT,
            )
        return p.returncode

    return run


def push_revision(dest, head):
    """Put head on the preflight branch of the repository at dest."""
    git("push", "--quiet", "--force", dest, f"{head}:refs/heads/preflight")


def run_mac(root, head, base, directory, only, say):
    r = Run(root, head, base, "mac", directory, only, say)
    runner = ssh_runner(MAC_HOST, MAC_REPO)
    say(f"[mac] sync: pushing {head[:12]} to {MAC_HOST}:{MAC_REPO}")
    try:
        push_revision(f"{MAC_HOST}:{MAC_REPO}", head)
    except Error as e:
        r.steps.append({"name": "sync", "command": "git push", "cwd": None, "exit": -1, "seconds": 0, "log": str(e)})
        return r.write({"unreachable": True})
    if not r.step("checkout", ["sh", "-c", 'test -z "$(git status --porcelain --untracked-files=no)" && git checkout --quiet --detach preflight && git rev-parse HEAD'], runner, always=True):
        return r.write()
    for s in MAC_STEPS:
        r.step(s["name"], s["argv"], runner, s.get("cwd"), s.get("env"))
    return r.write()


def powershell(script):
    p = subprocess.run(
        ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script],
        capture_output=True,
        text=True,
    )
    return p.returncode, (p.stdout + p.stderr).replace("\r", "")


def windows_repo_paths():
    """The Windows checkout's path as Windows sees it and as this WSL sees it."""
    code, out = powershell("$env:USERPROFILE")
    profile = out.strip().splitlines()[-1] if out.strip() else ""
    if code or not profile.startswith(("C:", "D:")):
        raise Error("the Windows host did not answer (powershell.exe): " + out.strip()[:200])
    winpath = profile.rstrip("\\") + "\\" + WINDOWS_REPO
    wslpath = subprocess.run(["wslpath", "-u", winpath], capture_output=True, text=True).stdout.strip()
    if not wslpath:
        raise Error("wslpath failed for " + winpath)
    return winpath, wslpath


# Where Node's installer puts its tools on Windows: a fresh install is not on
# the PATH this WSL inherited until WSL restarts, and a command PowerShell
# cannot find leaves $LASTEXITCODE untouched, which read as a pass once.
WINDOWS_TOOL_DIRS = {"npm": "$env:ProgramFiles\\nodejs", "node": "$env:ProgramFiles\\nodejs"}
FAILURE_MARKERS = ("CommandNotFoundException", "is not recognized as the name of a cmdlet")


def windows_tool(name, run=powershell):
    """The path of tool name on the Windows host: on its PATH, or where its
    installer puts it; None when it is nowhere."""
    where = WINDOWS_TOOL_DIRS.get(name)
    script = (
        f"$c = Get-Command {name} -ErrorAction SilentlyContinue; if ($c) {{ $c.Source }} "
        + (
            f"elseif (Test-Path \"{where}\\{name}.cmd\") {{ \"{where}\\{name}.cmd\" }} "
            f"elseif (Test-Path \"{where}\\{name}.exe\") {{ \"{where}\\{name}.exe\" }} "
            if where
            else ""
        )
        + "else { '' }"
    )
    code, out = run(script)
    lines = [l for l in out.strip().splitlines() if l.strip()]
    path = lines[-1].strip() if lines else ""
    return path if code == 0 and path and ":" in path else None


def _ps_script(where, argv, env, path_dirs=()):
    """One step as a PowerShell script: its exit code is the command's, a
    command that set none (not found, not run) is a failure. path_dirs go
    first on the step's PATH: the tools' own directories, so that what they
    spawn (npm's scripts run `node`) is found too."""
    command = " ".join(_ps_quote(a) for a in argv)
    sets = "".join(f"$env:{k} = {_ps_quote(v)}; " for k, v in (env or {}).items())
    if path_dirs:
        sets = f"$env:Path = {_ps_quote(';'.join(path_dirs))} + ';' + $env:Path; " + sets
    return (
        "$ErrorActionPreference = 'Continue'; "
        f"Set-Location -LiteralPath {_ps_quote(where)}; "
        + sets
        + f"& {command} 2>&1 | ForEach-Object {{ \"$_\" }}; "
        "if ($null -eq $LASTEXITCODE) { Write-Output 'preflight: the command set no exit code'; exit 1 }; "
        "exit $LASTEXITCODE"
    )


def windows_runner(winpath, tools=None):
    tools = {} if tools is None else tools
    path_dirs = []
    for path in tools.values():
        d = path.rsplit("\\", 1)[0] if path and "\\" in path else ""
        if d and d not in path_dirs:
            path_dirs.append(d)

    def run(argv, cwd, env, logfile):
        where = winpath + ("\\" + cwd.replace("/", "\\") if cwd else "")
        argv = list(argv)
        if argv[0] in tools:
            if not tools[argv[0]]:
                Path(logfile).write_text(f"preflight: {argv[0]} is not installed on the Windows host\n")
                return 127
            argv[0] = tools[argv[0]]
        code, out = powershell(_ps_script(where, argv, env, path_dirs))
        Path(logfile).write_text(out)
        if code == 0 and any(m in out for m in FAILURE_MARKERS):
            return 1
        return code

    return run


def _ps_quote(s):
    return "'" + str(s).replace("'", "''") + "'"


def run_windows(root, head, base, directory, only, say):
    r = Run(root, head, base, "windows", directory, only, say)
    try:
        winpath, wslpath = windows_repo_paths()
    except Error as e:
        r.steps.append({"name": "sync", "command": "powershell.exe", "cwd": None, "exit": -1, "seconds": 0, "log": str(e)})
        return r.write({"unreachable": True})
    tools = {name: windows_tool(name) for name in ("npm", "git")}
    say(f"[windows] tools: " + ", ".join(f"{k}={v or 'MISSING'}" for k, v in tools.items()))
    runner = windows_runner(winpath, tools)
    if not Path(wslpath, ".git").exists():
        say(f"[windows] init: {winpath}")
        code, out = powershell(
            f"git init -q {_ps_quote(winpath)}; git -C {_ps_quote(winpath)} config core.autocrlf false; exit $LASTEXITCODE"
        )
        if code:
            r.steps.append({"name": "init", "command": "git init", "cwd": None, "exit": code, "seconds": 0, "log": out})
            return r.write()
    say(f"[windows] sync: pushing {head[:12]} to {winpath}")
    try:
        push_revision(wslpath, head)
    except Error as e:
        r.steps.append({"name": "sync", "command": "git push", "cwd": None, "exit": -1, "seconds": 0, "log": str(e)})
        return r.write()
    if not r.step("checkout", ["git", "checkout", "--quiet", "--force", "--detach", "preflight"], runner, always=True):
        return r.write()
    for s in WINDOWS_STEPS:
        r.step(s["name"], s["argv"], runner, s.get("cwd"), s.get("env"))
    return r.write()


RUNNERS = {"linux": run_linux, "mac": run_mac, "windows": run_windows}


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--machines", default="linux", help="comma-separated subset of linux,mac,windows (default: linux)")
    ap.add_argument("--only", action="append", default=[], help="run only this step (repeatable): a partial run, no passing receipt; a remote machine's checkout runs regardless")
    ap.add_argument("--list", action="store_true", help="list the steps and exit")
    a = ap.parse_args(argv)
    if a.list:
        for machine, steps in (("linux", LINUX_STEPS), ("mac", MAC_STEPS), ("windows", WINDOWS_STEPS)):
            for s in steps:
                print(f"{machine:8} {s['name']:20} {' '.join(s['argv'])}")
        return 0
    machines = [m.strip() for m in a.machines.split(",") if m.strip()]
    unknown = [m for m in machines if m not in RUNNERS]
    if unknown:
        print("unknown machine(s): " + ", ".join(unknown), file=sys.stderr)
        return 2
    root = Path(git("rev-parse", "--show-toplevel"))
    if git("status", "--porcelain", "--untracked-files=no"):
        print("preflight: commit all changes first; receipts bind to the committed head", file=sys.stderr)
        return 2
    head = git("rev-parse", "HEAD")
    # origin/main as GitHub has it, before the base is read and the dco step
    # judges origin/main..HEAD (the docstring).
    try:
        git("fetch", "--quiet", "origin", "main")
    except Error as e:
        print(f"preflight: git fetch origin main failed: {e}", file=sys.stderr)
        return 2
    try:
        base = git("rev-parse", "origin/main")
    except Error:
        base = ""
    directory = receipt_dir()
    results = {}
    for m in machines:
        results[m] = RUNNERS[m](root, head, base, directory, a.only, print)
    ok = True
    for m, r in results.items():
        failed = [s["name"] for s in r["steps"] if s["exit"] != 0]
        state = "ok" if r["ok"] else ("partial" if r["partial"] and not failed else "FAILED: " + ", ".join(failed))
        print(f"preflight {m} at {head[:12]}: {state} -> {receipt_path(directory, head, m)}")
        ok = ok and r["ok"]
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
