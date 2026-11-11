# Conductor desktop

The desktop app is an Electron shell around the `conductor` server: it
starts `conductor serve` on a free loopback port with a workbench token minted
for the run, opens the embedded workbench in a window with that token handed
over through a context-isolated bridge, and stops the server when it quits.
Nothing moves out of Go: the shell only runs the binary, keeps its settings,
and shows its log.

## Layout

| Path | Responsibility |
|---|---|
| `src/main.ts` | the app's lifecycle: settings, the launcher, the supervisor, the window, tray and menu, quit |
| `src/server.ts` | `ServerSupervisor`: spawn, the handshake line, health polling, restart with backoff, stop by closing stdin |
| `src/handshake.ts` | the `--print-listen` line, read out of chunked output past a shell's noise |
| `src/launcher.ts`, `src/launcher-wsl.ts`, `src/wsl.ts` | where the binary runs: on this machine, or inside a WSL 2 distribution on Windows |
| `src/env.ts`, `src/shellPath.ts` | the server's environment: the login shell's PATH, the settings as `CONDUCTOR_*`, the token, and the app's own directory (`CONDUCTOR_DESKTOP_DIR`, under `/mnt` inside WSL), which the server's sessions never read |
| `src/settings.ts` | `userData/settings.json`: data directory, allowed roots, default directory, yolo, reach, close to tray, the WSL distribution |
| `src/install.ts` | the stable copy of the binary the server runs from (the hook assets name it) |
| `src/window.ts`, `src/ipc.ts`, `src/preload.ts` | the window's fences, the bridge the workbench calls (`window.conductorDesktop`), the sender checks |
| `src/logs.ts`, `static/log.html` | rotating logs under `userData/logs` and the log window |
| `static/setup.html` | the Windows first-run screen: what WSL is for, Install WSL, Try again |
| `test/` | vitest: every module above, the supervisor against a fake server script, WSL against a fake `wsl.exe` runner |

## Running it

```bash
make build-go          # the server the shell runs in development (bin/conductor)
make desktop-install   # npm ci
make desktop-dev       # electron . against bin/conductor (or CONDUCTOR_DESKTOP_BIN)
make desktop-test      # tsc --noEmit and vitest
```

Development uses the checkout's binary as it is; a packaged app copies the
bundled binary to a stable path (`~/.local/share/conductor/bin/conductor`,
`~/Library/Application Support/Conductor/bin/conductor`) once per version
and runs it from there, so an AppImage mounted somewhere new each start
still has hook assets that name a binary that exists.

## The token

The workbench token is 32 random bytes minted when the server starts and lives
in the shell's memory, the server's environment and the handshake line: it
is never written to disk. The workbench reads it from the bridge, never from
localStorage. "Open in browser" opens the system browser on the server's
address with the token in the URL fragment, which never reaches the server;
the workbench takes it and drops it from the address bar.

## Windows

There is no Windows build of the server: the installer bundles the Linux
binary and the shell runs it inside the user's WSL 2 distribution
(`wsl.exe -d <distro> --exec sh -lc …`, with the PATH the person's own
shell builds, read with `$SHELL -ilc` inside the distribution, so
nvm's and npm's programs are found; a shell that prints nothing in time
is asked once more with longer, and again at the server's next start
while nothing was read), after copying it into the
distribution's home. The settings' directories are the distribution's
paths; Windows-shaped ones from an earlier build move to its defaults.
Without WSL 2 the app shows the setup screen. WSL's default NAT mode is
left as it is (mirrored networking is never asked for: it changes WSL for
Docker and every other tool). For WebRTC the app runs a UDP forwarder on
Windows (`src/udp-forwarder.ts`): the server inside the distribution puts
every WebRTC connection on one UDP port (7877, `CONDUCTOR_ICE_UDP_PORT`)
and advertises the Windows LAN address, the forwarder listens on that port
on Windows and carries each remote peer to the distribution's address on a
socket of its own, so ICE sees one NAT, the router's. The installer adds the
inbound firewall rule for the port when it has the right; Settings offers it
again (an elevated `netsh`, fixed arguments) when it is missing. The person
configures no Windows networking by hand, ever: no port proxy, no
`.wslconfig` change. What else must cross Hyper-V's NAT (the TLS listener,
the router mapping) is the app's to forward in a later round; until then
sharing from WSL goes through a switchyard or a paste invite.

**Invites.** The app registers the `conductor:` URL scheme (electron-builder
`protocols`, `setAsDefaultProtocolClient`). An invite,
`conductor://<switchyard>/join/<token>`, reaches a running app through
`second-instance` (Windows, Linux) or `open-url` (macOS), or starts it; the
app opens its own workbench at `/join/<token>?server=https://<switchyard>`,
which signals to the switchyard from the app's page (`src/invite.ts` has
the parser, the same rules as the web's `utils/invite.ts`).

## Updates

`src/updater.ts`. A package names its own update feed: the app updates
itself (macOS, Windows, AppImage) only when it finds `app-update.yml` in
its resources. What decides is whether electron-builder writes that file,
and it writes it whenever a publish configuration resolves for the build
(26.15's `PublishManager`: on macOS for a dmg or zip, on Windows for an
nsis, on Linux for every package): the target's, the platform's or the top-level `publish`;
with none set, a provider named by a token in the build's environment
(`GH_TOKEN` or `GITHUB_TOKEN`, `GITLAB_TOKEN`, `KEYGEN_TOKEN`,
`BITBUCKET_TOKEN`); with none of those, the GitHub repository the project
names (`package.json`'s `repository`, a CI's repository variable or the
project's `.git/config`). Only `publish: null` (or `-c.publish=null`)
leaves it out for sure. `electron-builder.yml` names a GitHub `publish`,
so every package built from it has a feed unless the build sets it to
null. A package with none has no updater, so it never fails at every
check, and its log says "this build names no update feed", a deb or rpm
among them (it names no download page either). A deb or rpm that names
a feed is updated by its package manager, and the log names the download
page, by its address without a query or fragment: the packaged
`package.json`'s `conductor.downloadPage`, an `https://` URL of at most 200
bytes in the form `switchyard.appURL` takes (a host name of letters, digits,
hyphens and dots, printable ASCII; anything else is ignored), as the URL
standard writes it, which must be in that form and bound too, or the
repository's releases page (`/releases`: `/releases/latest` answers 404
while every release is a prerelease).

**An install under the earlier app id.** The rc line was built as
`dev.conductor.app`; its successor's id is `io.rocksolidlabs.conductor`, and
both install to `%LOCALAPPDATA%\Programs\Conductor`. An installer of the
successor replaces the earlier install (`build/installer.nsh`,
`customUnInstallCheck`, compiled in for that id alone: an open-source rc
and a staging installer hold none of it). In the install section, after the
pages and electron-builder's running-app check and before any file is
copied, it looks for the earlier id's uninstall entry (the GUID
electron-builder derives from `dev.conductor.app`) under HKCU and HKLM; with
none, it installs as usual. An entry under a key the installer cannot open
is not seen, and the install goes on, as electron-builder's own upgrade
step does not see an entry of the installer's own id that it cannot open.
It acts only on an entry that is, whole, what the earlier installer wrote
under that root: its `UninstallString` a quote, an absolute path to
`Uninstall Conductor.exe`, a quote, one space and the root's mode word
(`/currentuser` under HKCU, `/allusers` under HKLM), and nothing else; and
its `InstallLocation` there and naming that file's folder (the earlier
uninstaller takes its folder from that value). Such an entry, with the
uninstaller and `Conductor.exe` in its folder, is removed as electron-builder removes an earlier version of an
installer's own id: a copy of its uninstaller run in place with
`/S /KEEP_APP_DATA --updated` (user data kept, every file restored at one
it cannot move), tried five times a second apart; then the entry and the
files must be gone before the install goes on. The installer deletes no
key or file of the earlier app itself. Any other entry, every value the
earlier installer did not write among them, stops the install with nothing
copied, run or deleted; so does an entry whose uninstaller is not found,
since a file that is gone and a folder the installer cannot look into
answer it the same, and that is no proof the earlier app is gone. Every stop
comes before any of the new app is copied, and says why: the earlier app could not be removed, was not fully removed, is
installed for everyone and the installer is not running as administrator,
or cannot be removed by this installer, its uninstaller not found or its
entry not in the earlier installer's form (remove it from the installed
apps, then run the installer again). A stop does not undo what
electron-builder's own upgrade step did before it: run by hand over an
installed successor, the installer has already removed that successor's
earlier files, as any upgrade does, so after a stop the person follows the
message and runs the installer again. An update the app started
(`--updated`) never makes the move: a successor that updates itself was
installed by a run that made it.

## What stays by hand

The packaged app on each platform (dmg, deb, rpm, AppImage, nsis) and the
WSL path on a real Windows machine: `docs/features.md` lists the checklist.

The move from the earlier app id has never been run: its tests read the
script and a compile shows what it holds for each id, but no installer was
started. The first installer of `io.rocksolidlabs.conductor` does not ship
until its cases (the by-hand list of the pull request that added it) have
been run on a real Windows machine with an rc installed. Only that run shows
that `FileExists` is false for a file in a folder the installer cannot look
into, that a real rc's `UninstallString` is, byte for byte, in the form the
move takes, and that Windows' installed apps remove an entry whose
uninstaller is gone, the only way past the stop for one.

## Releasing

A release is built from a dispatch of `.github/workflows/release.yml` with
its version, never from a pushed tag:

```bash
gh workflow run release.yml --ref main -f version=0.7.0-rc.18
```

The run checks, builds every package from the commit at the head of `main`
and puts them on one draft release named `0.7.0-rc.18` for the tag
`v0.7.0-rc.18`, which does not exist yet. Its last job, `release-check`,
fails unless that draft is the version's only release, is for the commit
built, holds every package with its blockmap and the three `latest*.yml`,
and no `v0.7.0-rc.18` tag exists. Publishing the draft (as a prerelease for a
release candidate) makes the tag at that commit.

Dispatch from `main`, or from a branch kept until the release is published:
the draft is made after the checks, and GitHub refuses the workflow's token a
release at a commit no branch holds any more. `main` cannot lose one (its
ruleset forbids force pushes and deletion). Publishing as a person needs a
token with the `workflow` scope when the commit changes a workflow.

Do not push a `v*` tag. The apps' updater reads the repository's
`releases.atom`, which lists a pushed tag even while its release is a draft.
An app on the rc channel takes the newest rc entry there, finds no
`latest.yml` on a draft and updates to nothing until a newer release is
published or the tag is deleted (#375). The dispatch refuses a version whose
tag exists.
