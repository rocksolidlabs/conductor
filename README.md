# Conductor

<img src="web/public/brand/conductor-logo.svg" alt="Conductor" width="320">

**Shared agent terminals.** Launch coding agents such as Claude Code, Codex or
Antigravity in real terminals, watch and drive them from the browser, and share
a live session with other developers through a link. Inspired by
[webtty](https://github.com/maxmcd/webtty), rebuilt as a small server with a
Nuxt UI workbench.

Two ways to run a session:

| | Server-hosted | Developer-hosted |
|---|---|---|
| Where the agent runs | On the machine running `conductor serve` (in the desktop app, this computer) | On the computer you are at, via `conductor host`, when the server is elsewhere |
| How browsers reach it | WebSocket relay through the server | WebRTC data channel straight to your machine, with an automatic relay fallback through the server |
| Who starts it | Anyone with the workbench token, from the UI or API | You, from your shell |

Both kinds show up in the same session list, use the same share links, the same
terminal and the same in-browser file viewer.

## Quick start

Requirements: Go 1.26+, Node 22+, and the agent CLIs you want to launch on the
`PATH` of whichever machine runs them.

```bash
make deps                 # checks go/node/npm/python3, then go mod download + npm ci
make build                # generates the SPA and builds bin/conductor with it embedded
CONDUCTOR_WORKBENCH_TOKEN=change-me ./bin/conductor serve
```

Open <http://localhost:8080>, paste the workbench token when prompted, and press
**Launch agent**. Without `CONDUCTOR_WORKBENCH_TOKEN` the server makes a random
token at startup and keeps it in `workbench-token` in its data directory, mode
0600, while it runs: `cat ~/.conductor/workbench-token` (the default data
directory) as the server's user. The terminal and the log name the file, never
the token. Every start without a token writes a new one there; a start with one
configured removes the file. A second server started on the same data directory
while the first runs cannot keep its own generated token there, so it stops
unless a token is configured for it (or its parent reads `--print-listen`).

Host a session from your own machine instead:

```bash
CONDUCTOR_HOST_TOKEN=host-token ./bin/conductor host --server http://localhost:8080 -- claude
```

Your terminal is attached as a controller; the session appears in the UI as
`hosted`. Add `--relay-only` to skip WebRTC entirely, `--no-local` to run it
headless, or `--stun stun:host:3478` to override the ICE servers.
A server on another machine is `https://`: a plain `http://` (or `ws://`)
one that is not this machine (`localhost` in lower case, `127.0.0.0/8`, `::1`) is refused
before anything is dialed, unless `--insecure` says to go ahead, and then
the host says that what passes between it and the server (the session's
output, its viewers' keys) travels in the clear and unverified. A redirect
the server answers with is held to the same rule. The server is named by its
address alone: a `--server` (or `CONDUCTOR_SERVER`) with a user name,
password, query or fragment is refused before anything is dialed; the host
token goes in `--token` (`CONDUCTOR_HOST_TOKEN`).
`--server-is-mine`, for a server on this machine only, says that server is
yours: a window its workbench opens with no link is then one of yours and gets
the editor's Neovim (without it, only this terminal is).
`--link view` (or `control`) has the server mint a share link to the session
once it is registered, good for two hours, and prints it: the way to share a
session hosted on a switchyard, whose workbench token mints none; the line
names `kill -USR1 <pid>`, which mints another (once the first expired, or
could not be had). Either way
`conductor host` sends its server the session's chat, activity and full
details, for that server's workbench, and obeys its Stop: point it at a
server you trust.
`--ice-udp-port 7877` puts every WebRTC connection on that one UDP port and
`--ice-public-ip 192.168.1.20` advertises that address as the host's own
(`CONDUCTOR_ICE_UDP_PORT`, `CONDUCTOR_ICE_PUBLIC_IP`): what a forwarder in
front of the host needs, such as the desktop app's on Windows, which carries
the port into WSL. An agent with
no hooks and no bell can still raise the needs-input badge:
`--signal-pattern '<regexp>'` (RE2, at most 200 bytes, not matching an empty
line) is matched against the last line of the terminal after 500 ms without
output; the Launch dialog's command carries it for agents whose catalog entry
has a pattern signal. On a machine that also runs `conductor serve --config
<file>`, `--server-config <file>` names that file, so the session's Files tab
refuses it, its data directory and its catalog file as the server's own
sessions do (see the Files tab).

## The workbench

The sidebar is the session list, grouped into **Needs you**, **Running** and
**Exited**, with a filter box and a **Launch agent** button (**N**); **/** or
**Ctrl+K** opens the command palette (see Sidebar and keyboard shortcuts).
When the workbench is served from another machine, Launch asks where the
agent runs: **On this server**, or **On this computer**, which shows the exact
`conductor host` command to paste into a terminal here (it carries your
workbench token; keep it private) and closes the dialog by itself when that
session connects. On a workbench opened over plain `http://` from another
machine that command carries `--insecure`, which the host needs for such a
server, and the dialog says that the session's output and its viewers' keys
then travel in the clear. In the desktop app, or on a workbench opened at this
computer's own address, there is one answer and the question is not asked. The workbench opens dark; the theme button at the foot of the
sidebar switches to light and remembers it. The session page shows the
terminal, a reply bar whenever the agent
is waiting (type an answer, or press the numbered buttons a Claude Code
dialog offers, read off the dialog itself), and an inspector whose rail on
its outer edge holds **People** (who is attached, their role and link, who is
typing), **Explorer**, **Changes** and **Commits** (the file browser, the git
status and the commits since the session started), **Activity** (joins,
answers, signals and link changes, with the files the agent touched as a
filter) and **Chat**; each item opens its view, the open one collapses the
panel to the rail, whose count pills still read. The header names the
agent, where it runs, the working directory and the git branch, shows a
**yolo** badge for a session launched with its agent's yolo recipe (see
[Yolo](#yolo)) and the agent's own session id when Conductor knows it (click
to copy); an ended session offers **Resume** or **Relaunch** (see
[Resume and relaunch](#resume-and-relaunch)). A reply typed in a reply bar
goes in as Conductor types a crew prompt: the text, then Enter on its own a
quarter of a second later, so that an agent that reads a fast burst as a paste
still runs it.

## Sharing

On a session page press **Share**: a link good for two hours is made and
copied at once, and the dialog says where it reaches. It is **View** (watch
and open files) until you pick **Control** (types into the agent, answers
prompts) on the link: that makes a control link in its place, copies it, and
the next Share makes control too, until you pick View again. With the session published to a switchyard (the default) that is
"works from anywhere": the link is minted and checked there, and the
terminal comes to this machine over WebRTC, or through the switchyard's
relay when no direct path forms ([what a switchyard is trusted
with](#security-model-and-limits)). Below it, another link with a
role, a label and an expiry of your own. Opening Share again shows the link already made. The link URL is
`<base>/join/<token>`; the token is shown once. Guests type a display name
before joining; nothing connects until they press **Join**, so a fetched link
never exposes terminal content. Revoking a link disconnects everyone using
it, a link minted at the switchyard too, and a link that expires disconnects
them as it expires. A session that ends takes its links
with it: they stop working and leave the dialog.

One window sizes a session: your own window, never a guest's. A guest on a
laptop sees your session's columns and rows scaled to their window, and the
terminal's corner says who sizes it ("Sized by Nate · 212 × 54"); with a
control link **Fit to my window** takes the size, and your window then shows
the same corner to take it back. Neither window changing size moves it; only
that button does. When the window that sizes it closes, the size goes to
your other window of it, if one is open.

The base is the address you opened the workbench at (`http://192.168.1.20:8080`,
a tunnel's URL, a reverse proxy's host, with the scheme and host the proxy
forwards in `X-Forwarded-Proto` and `X-Forwarded-Host`), unless `publicUrl`
names another machine, in which case it is `publicUrl`; the default and the
example config name `localhost`, which no one else can reach, so they do not
count. Whoever you send the link to must reach the server at that address;
what reaches whom:

| The server runs | Who can open a link |
|---|---|
| on your machine, the link opened there | you |
| on your machine, a teammate on the same network | them, at your machine's address and port (`listen` binds every interface) |
| on your machine behind a home router | no one outside until the router forwards a port to the server and the link carries your public address. With `reach.mode` `auto` (the default) the server learns its public address from STUN and, once it has a TLS listener with a certificate, maps that port on the router and builds links on `https://<public address>`; until then links keep the address you opened the workbench at, and the Share dialog says so. `GET /api/reach` reports the state. A mapping is reported as mapped, never as reachable: the server's own check through the public address is refused by most home routers, so open a link once from a phone on mobile data |
| on your machine under WSL2 | through the desktop app: it forwards WebRTC into the distribution itself (a switchyard invite, or a paste invite), and WSL's default NAT mode stays as it is; you configure no Windows networking. A plain `http://` link to a server inside WSL reaches no one outside the distribution, and the public-address path (TLS and the router mapping) from WSL waits for the app to forward that listener and map the router itself |
| on a machine with a public address, or behind a reverse proxy | anyone, at `publicUrl` or the proxy's forwarded host |
| anywhere, by default | the public switchyard: this server publishes every session to `switchyard.rslabs.net` through the host protocol (`rendezvous`, on unless turned off), where it is listed as hosted by this machine and the link is minted and checked; the terminal goes between the viewer and this machine over WebRTC, through the switchyard's relay when no direct path forms. Publish only to a switchyard whose operator you trust ([Security model and limits](#security-model-and-limits)) |
| behind carrier-grade NAT or a corporate network, with publishing off | a tunnel or a reverse proxy in front of the server; or `conductor host` from your machine against a server anyone can reach |

The ICE servers play no part in a link. STUN tells a browser or a `conductor
host` its own public address and port so the two can connect the terminal
channel of a hosted session directly over WebRTC once the join page has loaded
from the server; it carries no data and does not make the server's page
reachable, which only a forwarded port, a public address or a proxy does.

**Links you open stay in the sidebar.** In the desktop app, or a browser that
holds the workbench token, a shared session opens beside your own sidebar
instead of taking the window over, and a link you have joined is kept under
**Shared with you** at the top of the sidebar (name, role, which server it was
shared through, a dot for its state, looked at again every minute). Opening
it joins at once; **Leave** keeps it; **×** forgets it. The list is kept in
this browser (`conductor.joined`, at most 20) and holds each link's token, as
the browser keeps the workbench token, so forget the links you are done with.
A link shared through a switchyard works from the app and from a browser on
the same machine, or from an origin the switchyard's `allowedOrigins` names; a
guest's browser, without the workbench token, keeps nothing.

### Switchyard

A small public Conductor run as a **switchyard** introduces machines no one
can reach: every Conductor publishes its sessions to one (`rendezvous`; the
public `switchyard.rslabs.net` unless the config names another or turns it
off), viewers join by links minted there, and the terminal goes over WebRTC
between the viewer and the publishing machine, through the switchyard's
relay for the pairs ICE cannot connect. A switchyard is trusted with the
sessions published to it ([Security model and
limits](#security-model-and-limits)); a Conductor sends it each session's
name, agent, folder name and machine name and its status and attention
state, never its chat, its activity or its command line. The desktop app
says so once, on its first start, with the way to turn publishing off
(Settings → Switchyard). A switchyard that admits
**open hosts** (`switchyard.openHosts`) needs no token from a publisher:
each address may hold `openHostSessions` live sessions (4), register
`openHostRegistrationsPerMinute` times (6) and relay `openHostRelayKBps`
(128 KiB/s) for all its sessions together; a host token (`rendezvous.token`) marks a trusted machine
outside those limits. A session such a limit refuses is tried again for a
few minutes (a slot frees as another session ends), and its Activity says
it is not shared yet and why. The switchyard launches nothing of its own:

```bash
CONDUCTOR_SWITCHYARD_OPEN_HOSTS=1 CONDUCTOR_MAX_SESSIONS=500 CONDUCTOR_MAX_VIEWERS_PER_SESSION=8 CONDUCTOR_HOST_TOKENS=a-host-token CONDUCTOR_REACH=manual CONDUCTOR_TLS_LISTEN=:443 CONDUCTOR_TLS_ACME=1 conductor switchyard --listen :80
```

That is the shape for a VPS: `reach.mode: manual` (there is no router to map;
STUN finds the address and 443 is open as it is; in `auto` a certificate is
only ordered once a router mapping exists, which a VPS never gets), the TLS
listener on 443 itself (`setcap cap_net_bind_service=+ep` on the binary, or
root), and an ACME account without an email, which Let's Encrypt accepts.

`conductor switchyard` is `conductor serve --switchyard`: the sessions,
crews, runs, catalog and integrations routes answer `403 switchyard`, and
hosted sessions, links and the events stream work as on any server, but
for the workbench token. On a switchyard it lists the sessions published
there and stops a publication (the session runs on at its home, and its
owner's next Share publishes it again), and has no role on them: it views,
controls and shares none, reports nothing for them (their attention and
events routes take the session's agent token alone), and the events stream
carries no chat or activity of them. A link to a session there is minted
by the machine that publishes it, over its own connection: a Conductor's
Share, or `conductor host --link`. It serves no workbench: its root is a landing page that says
what the server is, how a link looks, how a machine publishes to it and
whether it is up (version, uptime, the certificate's renewal, the relay),
every workbench path (`/sessions/…`, `/crews`, `/yard`, `/agents`,
`/events`, `/settings`) is a 404 page pointing at the Conductor on your own
computer, and the app is served only for `/join/<token>` and `/paste`. The
operator pastes the workbench token on the landing page to see the hosts
connected, the sessions published, the viewers and the bytes relayed this
hour (`GET /api/switchyard/status`); the token stays in that browser. `switchyard.relay: false`
(`CONDUCTOR_SWITCHYARD_RELAY=0`) turns the relay off: a viewer whose WebRTC
fails is told `relay_off` and a host that would use the relay alone is
refused. `switchyard.allowedOrigins` (host patterns, `127.0.0.1:*` and
`localhost:*` by default) lets the desktop app's own workbench join from its
loopback page: the join route answers those origins across origins and the
session WebSocket accepts them. A switchyard needs what any public server
does: a public address or a mapped port, and a certificate ([TLS](#tls)).
Its load is signaling, a few kilobytes per connection, plus the relayed
terminals; the smallest VPS carries hundreds.

**Invites.** Every link reply carries the same link as an invite,
`conductor://<host>/join/<token>` (`?http=1` for a plain-http server, which
only a loopback one may be). The desktop app registers the scheme: an
invite opens the app's own join page, which fetches the link from the
server named and connects the session there, so no page is loaded from
the switchyard. A link made on a machine that publishes to a switchyard
is minted there (over the host connection: `link` and `link_created` in
`docs/protocol.md`), so its URL and invite are the switchyard's. The Share
dialog shows both, with a copy button each; the join page takes `?server=`
(an `https://` origin, or `http://` on loopback) for the same thing in a
browser.

### Paste invites

With no server either side can reach, two blobs through any messenger do
it: the viewer opens `/paste` on any Conductor (the app's workbench, a
server of their own), makes an invite, and sends it; the person sharing
opens the session's Share dialog, **Answer a paste invite**, pastes it, and
sends the answer back; the viewer pastes that, and the terminal runs over
WebRTC between the two machines with nothing in between. Each blob is the
side's SDP with every ICE candidate gathered (`cpi1.…`, a few hundred
bytes). It works for most home-to-home pairs; a carrier NAT or an office
network on either side needs a switchyard, since there is no relay. A
session keeps at most 16 such viewers; they go with the session.

### TLS

A link that leaves your network should be `https`. The server serves the
workbench over TLS on a second listener (`tls.listen`, `:8443`) with a
certificate from Let's Encrypt through [lego](https://github.com/go-acme/lego):

- **For your public address, with nothing to configure:** `tls.acme` with no
  `domains` (`CONDUCTOR_TLS_ACME=1`). With `reach.mode` `auto` the server asks
  STUN for its public address, maps port 443 on the router to the TLS
  listener, and orders an IP-address certificate for that address (Let's
  Encrypt issues those since January 2026, as six-day certificates under the
  `shortlived` profile, proven with `tls-alpn-01` on 443). It renews after
  four days and orders again when the address changes, which makes old links
  stale: the Share dialog says so. Until the first certificate is issued,
  links keep the address you opened the workbench at and the TLS listener
  refuses every handshake; nothing self-signed is ever served.
- **For a domain:** `tls.acme.domains: ["home.example.net"]`, proven with
  `dns-01` through `cloudflare`, `exec` (a script of your own, `EXEC_PATH`)
  or `httpreq`, with `http-01` on a mapped port 80 (`reach.publicPort80`), or
  with `tls-alpn-01` on the mapped 443. Keeping the name pointed at your
  address (dynamic DNS) is yours to arrange.
- **With your own certificate:** `tls.certFile` and `tls.keyFile`, re-read
  when they change.

The plain listener stays as it is for the desktop window, local hosts and
`http-01`; there is no redirect to TLS. `GET /api/reach` reports the
certificate (`tls`: identifiers, `notAfter`, `renewAt`, `ready`, the last
error); the account key and the certificates live under `dataDir/tls`, mode
`0600`. The certificate flow is tested against Let's Encrypt's Pebble in CI
(`make test-pebble`).

## Chat beside the terminal

The inspector's **Chat** view is for the people watching a session together:
everyone on it, the owner and anyone who joined by a link, view or control.
It goes over the terminal's own connection, so it works wherever a link
works, on a `conductor host` session and through a switchyard too, and
needs no account: people are the names they joined with. Through a
switchyard it goes between the viewer and the publishing machine, which
sends the switchyard none of it; a viewer relayed through the switchyard
has it in the relayed frames, which the switchyard can read. Plain text, up to
2 KiB, links clickable; Enter sends, Shift+Enter is a new line. A
controller's **To agent** sends the message and types it into the agent,
and **Send to agent** on any message does the same later; the thread marks
what went ("Sent to agent by Nate · 08:32:40"). The session keeps the last
200 messages, joins and leaves among them, and replays them to whoever
joins; the chat ends with the session. While the tab is closed it counts
what others wrote. Chat lines are not events: they reach no hook, webhook
or feed.

When the agent needs input, its question is in the chat too, from the
agent, with its choices as buttons; a controller answers there as from the
quick-reply bar, and a line says who answered and which choice ("Answered
by Nate: Keep both"); the question keeps its choices as text, the one
picked marked. A line you typed as an answer is shown under the question
to you alone, never sent to the other viewers: the agent may have asked
for a credential. In a run's chat a member's question
comes on the member's name ("on core") and an answer from there goes to
that member.

Where the inspector has no room, the header's **Chat** button carries the
count and opens the chat as a sheet: on a phone from the bottom, two thirds
of the screen and dragging to full height, the terminal live behind it,
Return sends. A guest on a link gets the same chat beside the terminal on
their join page (the button folds it away), as a sheet on a phone; a
view-only guest reads "You are view only: what you write reaches the people
here, not the agent." and has no agent actions.

A crew run has one chat for everyone on it, over the members' own
connections: the run page's **Chat** button (and a run link's, for a guest
beside the tiles or with a member open in full) opens it as a drawer beside
the tiles, a sheet on a phone. A message says which member its sender was
looking at ("on review"). A person with control picks beside Send where a
message goes: **Chat only**, or **Also send to core**, typed into that
member's terminal as the broadcast bar types it; a member waiting on a prompt
is skipped, with the same words; hovering a message offers **Send to…** the
same way. The run keeps the last 500 messages, replays them to whoever
joins, and keeps them in its record once it ends, read-only there; a resumed
run starts a new chat.

What you have not read shows where you are: a neutral pill with a speech
bubble on a session's row and a run's header in the sidebar, the number in
the rail square's bottom-right corner (with new events, the tooltip naming
both), one count per session and a run's own chat on its header. The count
is this browser's, kept across reloads and tabs, fed by the chat you have open
and by the server's event stream for the chats you do not (a `conductor
host` session's chat reaches that stream too, sent by the host as it is
said); it clears when you open the thread, and nothing counts for system
lines or what you sent.

## Files, clickable links and the file viewer

The inspector's **Explorer** opens on the session's working directory as a
tree: folders expand in place, files show their size, the breadcrumb runs
from the top of the path, and the box above the tree filters it as you type
or, given a path (or `path:line`) and Enter, opens that file at that line.
A crumb brings the tree back, opened down to that folder. Paths the agent
prints in the terminal are clickable too, as the view's last line says.

A file chosen in the tree, typed in the box or clicked in the terminal opens
in an **editor above the terminal**: Monaco, the VS Code editor core, with
the gutter and folding, the minimap, find and replace (Ctrl+F, Ctrl+H), go
to line (Ctrl+G); read-only for now. The editor and the terminal share the
column, stacked or side by side (a button in the editor's tab strip
switches, on a wide window), split by a bar you drag or move with the
arrow keys, the terminal keeping at least six lines stacked and a
readable width beside; the layout and each layout's split are kept per
browser.
Several files are tabs (Ctrl+Tab and Ctrl+Shift+Tab switch, Ctrl+W or ×
closes, the strip's × closes all, and a tab's menu (right-click, or Shift+F10
or the Menu key on the tab) closes the others, those to the right or the left, or all, as
VS Code's does; closing the last takes the editor away).
**T** (Alt+T while typing in the terminal) folds the editor to its tab strip
and brings it back. The header shows the breadcrumb, the position, copy
path and open raw; for a Markdown file, **Preview** (Alt+P) shows it
rendered, Mermaid diagrams drawn, and **Source** brings the text back. A plan
Claude Code saves outside the folder (`~/.claude/plans`) opens too, from the
approval's **Open plan**, the Files tab's **Plans** entry or the path the
dialog prints, and saves back to its file. An
image shows on a checker with its size, a binary
file says so with Open raw, a URL the agent printed previews in a
sandboxed frame, a refused read says why, and a hosted session whose
machine is away says files return with it. The Yard's focused tile and a
guest's join page open files the same way, with the Files pane beside the
editor (a view-only guest gets both read-only); on a phone the editor takes
the screen and the terminal folds to a bar at the foot that says what the
agent is doing and comes back on a tap.

The Files tab's **Changes** section is the working directory's git status
with each file's added and removed lines, refreshed every few seconds while
it shows (and on demand), the totals at the foot; the Explorer marks changed
files with M, A or D. A change opens as a diff in Monaco's diff editor, side
by side or inline, the base's version beside the working directory's, with
the file itself one click away; a working directory with no repository says
so. The status and the base's version come over the terminal's connection
like a read, from the machine the session runs on, under the same file
policy and deny list. Conductor's git starts no program the repository's
configuration names, no clean filter either: a file a filter transforms
(Git LFS, git-crypt) that git has to read again is compared as it is on
disk, so it can show as changed, with its raw lines, when your own `git
status` would not. A change inside a submodule shows once the submodule's
commit moves. The status is of the folder's own working tree: the folder, or
the repository it is in; a repository whose `core.worktree` points anywhere
else lists nothing from there and says so.

A view-only guest opens nothing of a repository's `.git`, whose settings
often hold the tokens git signs in with (a `.git/config` reached through a
link or under another name included), nor a `.git-credentials`, a
`.gitconfig` or git's own files in `~/.config/git`, nor those a commit keeps;
the editor says git's files are for controllers. A controller opens them as
before.

The box at the top of the Explorer filters the tree as you type, and
reaches folders you have not opened: a search on the session's machine
lists the matching files below the tree under "In folders not opened yet"
(`.git` and `node_modules` are left out).

The **Touched** section lists every file the agent read, edited, wrote or
deleted since the session started, one row per file, the most recently
touched first, each with the latest tool, the agent, the time and how many
times it was touched; the Explorer marks them with a dot. The session keeps
that list itself (up to 2,000 files), so a page opened late, a guest or the
Yard's tile sees the whole session, not only the last events. The events come from the agent's own hooks:
Claude Code's settings get a hook on its file tools (Read, Edit, MultiEdit,
NotebookEdit and Write) that reports the files alone, so Touched fills
whether or not the agent's tool events are on; Codex's apply_patch,
Copilot's view, edit and create, Antigravity's view_file, write_to_file and
replace_file_content, Goose's write and edit, and Cursor's file edits report
theirs with their tool calls, and a shell command any of them runs names what it plainly reads or
writes (`cat README.md`, `> out.txt`, `tee`, `sed -i`); a file saved forty
times is one line. An agent without hooks reports with `conductor notify
--event file --op edit --path …` or the MCP `report` tool, as the skill
says. A file the agent changed with no hook naming it (a shell redirect,
a generator, an agent whose hooks report no paths) still shows: in a git
repository the session compares `git status` after each tool call and
lists what moved as "seen by git" (writes, edits and deletes; reads cannot
be seen this way, and what was changed before the session started is left
out). The same events are in the Activity tab and on the Events page, in
the feed only by default.

The **Commits** section lists the commits on the branch since the session
started, newest first, each with its subject, short id, author and age (a
crew member's: the commits since the run's base). A commit opens to its
message and its files with their lines; a file opens as its diff against
the commit's parent, in a tab named by the commit's short id. Nothing is
pushed from Conductor. The history is read with go-git where the session
runs, a crew's worktree included.

**Editing.** A controller edits a file in the editor and saves it with
**Save** or Ctrl+S; it is written on the machine that runs the session and
lands in Activity and Touched as your write. An unsaved tab wears a dot and
asks before it closes. If the file changed on disk since you opened it (the
agent edited it, say), the save stops and says who and when, with
**Compare** (disk and yours side by side), **Reload** (take the disk's) and
**Save anyway**. A file larger than about 960 KiB opens cut short, marked
truncated, and stays read-only, as does a binary one, a file in a repository's
`.git` (its folder, a worktree's `.git` file, wherever a link or another
spelling leads; Neovim does not open one either; a file the repository's
configuration names outside it, such as an `include.path` file or a
`core.hooksPath` folder like `.husky`, stays an ordinary file), and everything
for a view-only guest. The server's or host's `fileEdit` setting (`control`,
the default, or `off`) turns editing off for everyone.

**Comments on lines.** Select lines in the editor and a bar offers
**Comment**, **Ask the agent** and **Copy** (Ctrl+Shift+M and Ctrl+Shift+A
too). The comment goes to the session's chat as a card with the file, the
lines and your words; asked of the agent, it is also typed into the agent
as `internal/api/users.go:14-16` with the lines quoted, then your words.
Anyone on the session can comment; asking the agent needs control. A card's
**Open** shows the lines in the editor, and when they moved since (the agent
added lines above them, say) the card says "Lines moved since · now 18–20"
and Open goes there.

**Neovim in the editor.** The editor's keys are Monaco's unless you press
the **Keys** button in the tab strip and choose **Neovim**: then a file you
open is held by the real Neovim on the machine that runs the session (your
own config and plugins), shown in Monaco. Every key goes to Neovim; the
status line under the tabs shows the mode, the command line as you type it
and Neovim's messages; `:w` writes the file there (a file event by you, so
Changes and Touched follow) and `:q` closes the tab. A tab whose changes
are not written yet wears a dot and keeps its Neovim while another tab is in
front, so they are there when you come back; closing it asks first (**Save**
writes through Neovim, **Don't save** drops them), and the keymap button
waits until nothing is left unsaved. The choice is kept per
browser and is off until chosen, so nobody who never asked for Vim keys
meets one. It needs `nvim` on that machine (the button says when it is
missing), one of your own windows (not a share link or an invite; for a `conductor host` session, only with `--server-is-mine`, which says the server on this machine is yours), and the
server's or host's `fileEdit` setting left at `control`
(`CONDUCTOR_FILE_EDIT`, `conductor host --file-edit`; `off` turns editing
off for everyone). A control-link guest still edits and saves, with the
editor's own keys. A view-only guest keeps the read-only editor. A file another Vim has open, or left a swap file for
when it died, opens read-only with a banner naming that Vim and what
applies: **Edit anyway**, and once that Vim is gone **Recover** (its
unsaved text, written with `:w`) and **Delete the swap file**. Neovim's own
questions, such as `:confirm q` over unsaved changes, show their choices as
buttons. Text that comes with no key press reaches Neovim too: an input
method's committed word (not the composition on the way), a dead key's
character, dictation. The real Neovim runs with your own config and
plugins, so it is kept for your own windows: a guest through a share link or
an invite edits with the editor's own keys and **Save**, and its keymap
button says Neovim is kept for the session's own window. The Neovim child
never carries Conductor's own variables. In insert mode a plain character shows at once,
before the machine answers, then settles as Neovim has it; one Neovim has
not handled after a tenth of a second is underlined. When Neovim's text is
not the guess (an autopair, an abbreviation, a mapping) its text wins once
and the guessing stops until insert mode is left.

URLs printed by an agent are clickable: a click offers **Open in new tab** or
**Preview in pane** (a sandboxed iframe; sites that forbid embedding stay blank).
File locations such as `internal/api/server.go:42`, `./README.md` or Python
traceback lines are underlined when the file exists in the session's working
directory; clicking opens it in the Files tab at that line with syntax
highlighting and a copy-path button. Reads go through the
terminal connection, so for hosted sessions the file comes from the developer's
machine. Limit reads with the `fileView` setting (`view`, `control` or `off`;
`--file-view` for `conductor host`). Server sessions never serve the server's
data directory, config file or catalog file (`catalogPath`), even inside a
working directory, nor what the other Conductors on the machine keep:
`~/.conductor`, the desktop app's own directory (`conductor-desktop` or
`Conductor` in the user's config directory, and the one the app names with
`CONDUCTOR_DESKTOP_DIR` for the server it starts: on Windows its folder in the
Windows profile, under `/mnt` inside WSL), which holds its settings and their
switchyard token, and the data directory those settings name. A hosted session refuses the same files of the server on
its own machine: the data directory `CONDUCTOR_DATA_DIR` names, else the one
the server would choose (`~/.conductor`, which is refused in any case); the
catalog file `CONDUCTOR_CATALOG_PATH` names; with `conductor host
--server-config <file>`, that config file and its `dataDir` and
`catalogPath`; and for the desktop app's server, the app's own directory, its
settings file (wherever a link takes it), the data directory its settings
name and, inside WSL, `~/.local/share/conductor/data`. The Windows app's
settings are not visible inside WSL: when they move that data directory, set
`CONDUCTOR_DATA_DIR` to it in the shell that runs `conductor host`. The host
looks these up again at each request and keeps refusing what it refused
before, so a data directory moved while the session runs is refused at both
places. The server resolves a relative path against the directory it runs
in, which the host cannot know, so the host does not start when one of these
paths is relative or the config file or the desktop app's settings cannot be
read, and refuses every file while that is so later. The host knows of no
other config file, so do not run `conductor host` from a directory that
holds a server's config file without naming it.

## The Yard

`/yard` (the Wall before it was renamed; `/wall` still lands there) is a grid of live tiles, one per active session, sized so that every
session fits on screen without scrolling; tiles shrink as sessions are added.
Each tile is the session's terminal at the tile's size, filling it. One
window sizes a session at a time (see Sharing): the first of yours to open
it, then the next when that one closes. So opening the Yard sizes each
session to its tile, opening a session's page (the Yard's tiles close) sizes
it to that page, and coming back to the grid sizes it to its tile again; a
session open in two windows at once keeps the first one's size, the other
showing it scaled until **Fit to my window**. Click into a
tile and type: the keys go to that session, the plain-key shortcuts pause
while the tile has focus and the **Alt** chords still work. The chips in the
header filter tiles (**All**, **Needs you**, **Running**). A queue on the left
lists every session waiting for input with its prompt: answer from there
(**J**/**K** select, **Enter** types a reply) without opening the session,
and see who answered what under **Answered**. The expand button in a tile's
header, a double-click on the header, or **Enter** on a tile whose frame has
keyboard focus expands it in place to a full-size terminal (`/yard?focus=<id>`,
so the view is linkable); **Esc**, the back arrow or the browser's Back button
return to the grid, and **Open page** goes to the full session page. The
fullscreen button turns a spare monitor into a status board.

## The Roundhouse

`/roundhouse` (`/carousel` still lands there) rotates through the active sessions one at a time, full size and
interactive: click into the terminal and type. Rotation pauses while the mouse
is over the pane or a terminal has keyboard focus, and the interval, auto-rotate
and pause controls are in the navbar. With **Follow routed events** on, the
Roundhouse follows what the **Roundhouse jump** switch on the Events page's Routing tab routes to
it, input requests and handoffs unless you change it. It jumps to a session
that needs input and holds there for two intervals (at least 20 s) before
rotating on; click **Holding** to release it sooner. Any other routed event
moves it to its session once, without holding, and never while it holds on a
session that needs input. It never jumps away while you are typing. A film
strip under the terminal shows every session with the rotation progress, and
the footer says which session is next. Drag the strip's chips into the order
you want (or pick one up with Space, move it with the arrows and drop it
with Space); the order is kept per browser, and stepping, rotation and
"Next up" follow it, a session started since going at the end. The button
in the strip's corner shows the strip as thumbnails instead: the screens of
the current session and its neighbours as text, read every second from the
views the Roundhouse already has, the idle mark for the rest.

## Sidebar and keyboard shortcuts

The sidebar lists the agents you can talk to, the sessions, ordered by what
needs you: **Needs you**, then **Running**. Sessions launched together are a
run, listed once as one block where its most urgent member is, never split:
the crew's name (or the run's own) and its start time on top, which open the
run page, and its sessions beneath, joined by a line, the one asking first;
an exited member stays inside with its **Resume**. The crew itself, the plan
the run came from, is not listed (it lives on the Crews page). A session
another machine hosts here (`conductor host`) says so on its row, a laptop
and the machine's name; this server's own say `server`. Rows say agent ·
where · how long; the path is the row's tooltip and the session page's. The
links shared with you (see Sharing) sit below your own sessions, and
**Exited** folds to one line. Every section folds from its header, which keeps
its count and a preview of what is inside; the choice is remembered per
browser (`conductor.sidebar.folds`), Exited starts folded, and Needs you opens
again by itself when a new prompt arrives. The run of the page you are on is
marked and scrolled into view; nothing is filtered out for it. A session
launched with yolo carries the **yolo** badge. Hovering a row offers
**Share**, **Stop** and **More** in place of the dot; a run's header offers
**Share run**, **Stop run** and **Open run**, and a member its own Share and
Stop. Stop asks first, in the row ("Stop docs-sweep? Its terminal closes.
Resume brings the conversation back from Exited."). A right-click, or a long
press on a touch screen, opens the same menu as More: Open, Open the run (a
member), Share…, Show in the Yard, Stop…. A prompt is answered in the row:
its choices are numbered buttons (1 Yes, 2 No), a free-text prompt gets a
reply field where Enter sends; the answer goes over a short-lived connection
as the Yard's does, and the row moves on as the prompt clears. A hosted
session whose host is away keeps its prompt, disabled, with "The host is
away".

**The command palette** is the sidebar's row actions and the Yard's queue
from anywhere: **Ctrl+K** (**⌘K** on a Mac) or **/** on any page, **Alt+/**
while typing into an agent (⌘K too on a Mac; Ctrl+K stays the shell's
kill-line), and the rail's search button. It opens over the page on what
needs you first: each waiting session with its question and its choices as
numbered buttons, then the page's own actions (Share, Stop, the marks, the
inspector's views and the open tabs on a session page, and with a file open
the editor's Keys → Neovim, Save, Close file and Ask the agent about the
selection; the broadcast, the
view, the chat and the members on a run page; the chips on the Yard, the
rotation on the Roundhouse, Launch on Crews, the hooks on Events, Save on
Settings), then Running, Exited (folded to a line), Runs, Shared with you,
Artifacts, Go to and every other action. Typing filters the rows, the match
marked; **↓** moves into the list, where the sidebar's keys work on the
focused row: **J K** move, **Enter** opens or runs, **1–9** answer its
prompt (a digit typed into the box is a search), **R** opens a member's run,
**S** shares, **Shift+S** copies a view link at once, **X** asks to stop
(Enter stops, Escape cancels), **→** reveals the row's actions and **←**
folds them, **Ctrl+Enter** opens in a new tab, **Shift+Enter** shows it in
the Yard, **Escape** goes back to the box and, from there, closes. A first
character picks a kind: **>** actions, **#** runs and crews (Enter launches
a saved crew, Launch named under → takes a name), **:** files (`path` or
`path:line` opens in the page's editor, then the open tabs, the changes,
the commits, the files touched lately across sessions, the artifacts),
**@** people, **!** events, **?** the shortcuts. A line that starts with a
verb and names one session, run or crew reads as one row that nothing runs
before Enter: `stop docs`, `share docs view`, `answer review 2`, `reply
review looks good`, `broadcast nightly ship it`, `run docs-crew as
tonight`, `resume docs`, `open src/main.go:12`, `mark docs working`, `go
yard`, `say docs looks good` (a line typed into a session that is not
asking; its owner says when the line was entered, refuses one into a
session that asks meanwhile, and the refused line waits in the row's field), `launch claude in ~/work`. Nothing is fetched when it opens; what
was used lately ranks first within its group (`conductor.palette.recent`).
**]** and **[** (**Alt+]** / **Alt+[** in a terminal) step through the
sessions that need you without opening the palette: a pill at the top of
the window shows the agent, the name, the question, "n of m" and the
choices as numbered buttons for four seconds; **1–9** answer it there,
**Enter** opens the session, **Escape** dismisses it. In the list **I**
types a line into a running session; **Answer all** at the top of Needs
you answers the same question asked by several sessions in one go; **Stop
every session that is done** and **Clear the new-event badges** sit under
**>**. On a session page **S** shares it and **X** asks to stop it
(**Alt+X** in its terminal); **G S** opens Settings in the desktop app
(**Alt+,**). A link can name a view: `?tab=files|chat|activity|people`
on a session page, `?view=grid|graph|timeline|tasks` on a run page (`&task=`
names the task shown),
`?filter=all|needs|running` on the Yard. **Alt+Q** leaves a terminal's
focus on the Yard and the Roundhouse everywhere (Alt+Esc too, except on
Windows, which keeps that one for its windows); the shortcuts modal
(**?**) shows the chords of the system you are on and says what that
system or its browser takes before the app sees it (Firefox's Ctrl+B, the
browser's Ctrl+Tab and Ctrl+W, the Mac's Option+letter).

The sidebar collapses to an icon rail with the panel button in its header or
**Ctrl+B** (**⌘B** on a Mac). On the rail a square is a session (its agent's
initials, solid while it needs you, dashed once exited), a capsule is a run
with its sessions inside (the play icon amber while one needs you), and the
corners say the rest: top right the state (amber needs you, green running,
grey idle), bottom right new events on it (the badges the Events page routes
to the sidebar), bottom left where it comes from (a laptop for another
machine, a globe for a link shared with you). The session open on the page
carries a primary bar down the rail's edge beside its square, and two
squares never look the same: two of one agent show their folders' letters
instead of the agent's initials, with an ordinal top left when the letters
still match. Two
counts sit on top: how many
need you, then how many have new events. The order is the list's: needs you,
running, shared, exited; loose exited sessions fold into **+N**, which opens
the full sidebar on Exited. Every shape has a tooltip naming it in words
("users api · run started 08:31 · review needs you · core running · lead
exited"). The rail keeps the mark, Launch, a search button that opens the
command palette, the pages (the Yard's count as an amber chip) and
the header's two menus. The panel button at the bottom of the rail brings the
full sidebar back, as does **Ctrl+B**. The mode and the full sidebar's
width are remembered per browser (localStorage keys `conductor.sidebar.mode`
and `conductor.sidebar.size`; a hidden sidebar from an earlier version becomes
the rail). On a phone the list is the home screen, not a drawer (`/sessions`):
the filter on top, prompts answered in place with full-width buttons, a row
opening its page and **Back** returning to the list; a long press on a row
opens its actions as a sheet (Open, Share…, Show in the Yard, Stop…; a run's
header: Open run, Share run, Stop run), Stop asking again, and there are no
swipe actions. The pages sit in a bar at the foot: Sessions, Yard (with the
count), Crews, Events, and under More: Roundhouse, Agents, Settings, Alerts
and your menu. **F** toggles fullscreen on
every page (**Alt+F** in a terminal); every page header has the button (where
the browser can do it), and the key is ignored while you type in a field or
have a select focused. Press **?** (or **Keyboard shortcuts** in your menu,
behind your initials beside the sidebar's name, with **Your name…**, **Toggle
theme**, **Workbench token…** and **Forget token**; **Alerts** sits beside it,
so the foot holds only the pages) for the list of shortcuts on the current
screen. The list itself takes keys once
a row has the focus (**↓** from the filter puts the first there; a click or
Tab, any): **↑ ↓** or **J K** move, **Enter** opens the session or the run
from its header, **1**–**9** answer the focused row's prompt as on its page,
**R** opens a member's run, **S** shares, **X** asks to stop in the row
(Enter stops, Escape cancels), **Escape** leaves the list. The Yard's J and
K and the quick reply's digits never see a key the list took.
Plain keys reach the agent while a terminal has focus, so they only work
outside it; hold **Alt** with the same key (**Alt+N**, **Alt+Y**, **Alt+←**)
to use a shortcut without leaving the terminal. Your display name defaults to
the server's user and can be changed from the person button in the sidebar.

**Copy and paste in a terminal** work as in Windows Terminal: **Ctrl+Shift+C**
(or **Ctrl+Insert**) copies the selection, **Ctrl+C** on a selection copies it
and clears it (with nothing selected it is still the interrupt), and
**Ctrl+Shift+V**, **Shift+Insert** or **Ctrl+V** paste, as one bracketed paste
when the program asked for that. A right-click copies a selection and
otherwise pastes; **Shift+right-click** opens the terminal's menu (Copy,
Paste, Select all, and **Right-click pastes**, which turns that off so a
right-click opens the menu, kept per browser). While the program asks for
the mouse (Codex's TUI does), a click, a drag, the wheel and a right-click are
the program's, as in Windows Terminal; **Shift+drag** still selects and
**Shift+right-click** still opens the menu (on a Mac those are the program's
too, and **⌘C** and **⌘V** still copy and paste). A view link copies and never
pastes. On macOS **⌘C** and **⌘V** stay as they are; on Linux a middle-click
pastes the selection as the browser does.

## Events and hooks

Every session's process gets `CONDUCTOR_NOTIFY_URL` and `CONDUCTOR_NOTIFY_TOKEN`
in its environment, and `conductor notify` reports with them: an attention
state (`--state needs_input`, `working`, `done` or `clear`) or an event
(`--event progress`, `artifact`, `handoff`, `tool_use`, `tool_denied` or
`error`). Outside a Conductor session the command exits silently, so it is
safe to install globally. An attention report (the state flags, `--event` with
an attention word, or a hook mapped to one) that the server answers `429` is
tried again after 0.1, 0.25, 0.5, 1 and 2 s, within the 5 s the command takes
at most; an event is tried once. Sessions that need a human show an amber
**needs input** badge, move to the top of the sidebar, count in the tab title,
can raise a browser notification, and appear in the Yard's queue. Claude Code's
dialogs (a tool's permission, the plan's approval, AskUserQuestion's choices)
offer their own choices wherever the prompt is shown, read off the dialog as
it is drawn: a choice that changes the permission mode (auto mode, accept
edits, bypass permissions) gets no number key and is never the highlighted
one. A permission question whose dialog the screen does not show (a hosted
session's) is answered in the terminal. Every report also lands in the session's
activity log and in the live feed of the **Events** page.

**The Events page** has three tabs. **Feed** (the default) lists the events
newest first in groups of one minute: the time, the event's icon, who reported
it (a crew member as `<crew> / <member>`), what happened and the one thing to
do about it (**Answer** a question, **Open run** for a handoff, **Open** an
artifact). Beside it: the last hour as bars per minute (what needs you,
handoffs, errors and the rest, as the tab saw them), where each kind of event
goes now, and whether each agent reports. **Routing** lists the ten events
grouped by how loud they are (needs you, worth knowing, background), each
with its destinations as switches: a labelled badge on its session in the
sidebar and on its Yard tile (until the session reports `working` or
`needs_input`, or you open it), a browser notification with the chime (as
switched on under the sidebar's alerts), a Roundhouse jump while it follows, and
a line in the live feed, which keeps the last 500 events in memory. Routes are
saved in the browser. **Integrations** is a table of the agents: whether their
hooks reach them, what they report, where an install writes, and **Install on
this machine**, with the snippet of an agent nothing wires open below. An
artifact's URL becomes a link only when it is `http(s)`.

**Wired at launch.** Conductor has a hook adapter for every built-in agent but
the shell. At startup `conductor serve` writes the hook files into `hooks/` in
its data directory, naming its own binary, and an agent whose catalog signal
is `hook` gets them when the server launches it, with nothing written to the
agent's own config: Claude Code through `--settings`, Codex through
`-c notify=…`, pi through `--extension`, aider through its notification
environment variables. `conductor host --agent <id> -- <command>` does the same
on your machine, with the hook files in `~/.conductor/hooks` (an older
`~/.local/state/conductor/hooks` is kept while it exists). `conductor serve`
warns and goes on when it cannot set the modes of the files in a `hooks/` of
its own (0600 files in a 0700 directory); a `hooks/` that belongs to another
user stops it before anything is written there, root included, since those
files choose the commands agents run.

**At rest is done, not a question.** An agent that finished its turn and waits
for its next line reports `done`, whatever its hooks call it: Codex's
`agent-turn-complete` and Claude Code's `idle_prompt` notification (sent after
a minute at rest) both do, as Claude Code's `Stop` does. Needs input is kept
for a real question: a permission request, a dialog, the bell. The bell an
agent rings as its turn ends (Codex rings the one Conductor asks it for right
after reporting `done`) is that turn's own: it does not undo the `done` until
the next Enter starts another turn. A crew relies
on this: a handoff or a broadcast is typed only into a member that is not
waiting on a question, and an idle member is not.

**Installed on demand.** The other agents read hooks only from their own
config, and so do the agents above when something other than Conductor starts
them. The **Events** page lists every adapter with what it reports and whether
its hooks are installed, the snippet to paste, and **Install on this machine**,
which adds Conductor's hooks to the agent's config in the server user's home,
next to your own settings and hooks and never over them: entries in its hook
lists, a marked block, or a file of Conductor's own. An entry of Conductor's
that names an older binary is rewritten where it is, but only when its program
is named `conductor`: a `conductor-dev` or `conductor.exe` entry is left as it
is. The same from a shell, for whoever runs it, a host included:

```bash
conductor hooks install claude    # one adapter, or: conductor hooks install all
conductor hooks status            # which agents have the hooks, and where
```

`install` prints the files it wrote, and a second run changes nothing. What it
cannot do from a file it leaves to you, and says how: DeepSeek Harness, a
developer preview, is always installed by hand, and a Codex `hooks.json` or a
`SKILL.md` of your own is never overwritten. It prints the snippet only when
the snippet is what is missing, the agent's hooks; a `SKILL.md` of your own
gets its own instruction instead (`conductor skill` prints Conductor's). Both
commands take the hooks, and the conductor binary they run, from `hooks/` in
the data directory `--data-dir DIR` names, by default the one
`conductor serve` uses without `dataDir`: `CONDUCTOR_DATA_DIR`, else
`~/.conductor` (or an older `./conductor.d` while `~/.conductor` holds no
server data); when no server wrote hooks there, or the binary they name is
gone, they say so and use the binary you run; when the hooks there were
written by another version of Conductor than the one you run, or by one too
old to record its version, they warn and go on. They refuse a `hooks/` that is
not yours or not 0700, as
`conductor serve` writes it (so a `conductor.d/hooks` that a checkout made 0755
is refused too, should an older `./conductor.d` still be the data directory),
since its commands would go into your agents' configs. `--home DIR` names
another home directory of yours. A home that belongs to another user is
refused, because what Conductor wrote there would belong to you: install for
that user as that user (`sudo -u <user> conductor hooks install …`). The one
exception is your own home, the one `HOME` names, when you may write it, as in
a container whose home belongs to another uid; it never applies to root, since
under `sudo` `HOME` may still name the invoking user's home.

**Agents without hooks.** Any tool that rings the terminal bell or emits an
OSC 9 / OSC 777 notification is detected with no configuration at all. A
catalog signal of kind `pattern` watches the last line of the screen instead,
as the built-in Cursor CLI entry does for its prompt. Run `conductor notify
--state needs_input --message "approve?"` from a script for anything else;
`--state clear` resets it. A question with a few answers takes them as
`--choices "Postgres|SQLite|Keep both"` (at most 6, each at most 40 bytes):
the session tile, the Yard's queue and the run page show them as buttons, and
a click types the choice into the session as a line, so an agent names the
choices in its terminal first and gets the same words back.

**The Conductor skill.** `conductor skill` prints a `SKILL.md` that teaches an
agent to report on its own: progress (`--event progress --message "4/7
handlers"`), an artifact such as a pull request (`--event artifact --url …`),
a handoff to another crew member (`--event handoff --to …`), a decision it
cannot make (`--state needs_input`) and a question with a few answers
(`--choices "a|b|c"`, answered with one click); and to form a crew around its own
session when the work splits (`conductor crew`, see
[Agents that form crews](#crews)). Every agent that reads skills gets it
without asking: the first time an agent is launched after the server starts,
the skill is put in that agent's skills directory in the server user's home
(`~/.claude/skills/conductor/` for Claude Code, `~/.codex/skills/conductor/`
for Codex, `~/.gemini/antigravity-cli/skills/conductor/` for the Antigravity
CLI, and `~/.agents/skills/conductor/`, the directory of the Agent Skills
convention, for pi, Goose, Cursor, Copilot, OpenCode, oh-my-pi, Amp and
DeepSeek Harness; aider reads none), one attempt per agent per server start,
and a `SKILL.md` of your own there is never touched (the log says so once).
`agents.installSkill: false` or `CONDUCTOR_AGENT_INSTALL_SKILL=0` turns that
off; installing the hooks (`conductor hooks install`, the Events page) puts
the skill in place too. The server keeps its copy in
`hooks/skills/conductor/SKILL.md`, and every session names it in
`CONDUCTOR_SKILL`, so an agent with no skills directory can be told to read
`$CONDUCTOR_SKILL` (a crew's member prompt can say so). Its commands run
`"${CONDUCTOR_BIN:-conductor}"`: every session carries `CONDUCTOR_BIN`, the
absolute path of the binary its hooks run, and `CONDUCTOR_AGENT`, the id of
its agent in the catalog, while the manual snippets below assume `conductor`
on the `PATH`.

<details>
<summary>Wiring the hooks by hand</summary>

`conductor hooks install` and the Events page write these for you, naming the
binary by its absolute path. Written by hand as below, `conductor` must be on
the `PATH` the agent runs its hooks with. A session Conductor launches already
gets Claude Code's hooks through `--settings`: add them to your own settings
for Claude Code started elsewhere.

- Claude Code (`~/.claude/settings.json` or a project's `.claude/settings.json`):

  ```json
  {
    "hooks": {
      "Notification": [{ "hooks": [{ "type": "command", "command": "conductor notify --claude-hook" }] }],
      "Stop": [{ "hooks": [{ "type": "command", "command": "conductor notify --claude-hook" }] }],
      "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "conductor notify --claude-hook" }] }]
    }
  }
  ```

- Codex (`~/.codex/config.toml`, above its first `[table]`; set
  `tui.notification_method = "bel"` too for the bell):

  ```toml
  notify = ["conductor", "notify", "--codex"]
  ```

- Every other agent: the snippet on its card on the Events page, which is also
  its file in `hooks/` in the server's data directory.

</details>

### Webhooks

The server can also pass events on to other services. A webhook in the
config is a URL the server POSTs to for every entry, from any session, of
the event types the webhook lists. The **Events** page shows the webhooks,
read-only, as the **Webhook** switch of each event on the Routing tab.

```json
{
  "webhooks": [
    {
      "url": "https://hooks.example.com/conductor",
      "events": ["needs_input", "exit_nonzero", "artifact", "error"],
      "secret": "a long random string"
    }
  ]
}
```

`events` takes the Events page's names (`needs_input`, `done`, `working`,
`tool_denied`, `progress`, `artifact`, `handoff`, `error`, `exit_nonzero`,
`tool_use`) and the entry types (`attention`, which covers all three
attention states, `status`, `join`, `leave`, `input` and `link`).
`exit_nonzero` is a process that exited on its own with a non-zero code,
never one an admin stopped. There are at most 16 webhooks, and a URL is
`http` or `https` of at most 2048 bytes. `CONDUCTOR_WEBHOOKS` takes the same
array as JSON and replaces the config file's.

Each entry is one request, naming its session and carrying the entry as the
Events feed streams it, without `state` (an attention entry's state is the
request's `X-Conductor-Event`):

```http
POST /conductor HTTP/1.1
Content-Type: application/json
X-Conductor-Event: needs_input
X-Conductor-Signature: sha256=<hex HMAC-SHA256 of the body, keyed with the secret>

{"sessionId":"…","session":{"id":"…","name":"api-sweep","agentId":"claude"},"entry":{"at":"2026-09-29T12:00:00.123Z","type":"attention","message":"Claude needs your permission to use Bash"}}
```

`X-Conductor-Event` is the event type the webhook listed that the entry is:
an attention entry is the attention state it records, the `status` entry of
a failed process is `exit_nonzero`, and when a webhook lists both, the
Events page's name wins. `X-Conductor-Signature` is sent only when the
webhook has a `secret`. Check it against the body as received, before
parsing it:

```go
func signed(body []byte, header, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(header), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
}
```

or by hand, with the body saved byte for byte in `body.json`:

```bash
echo "sha256=$(openssl dgst -sha256 -hmac "$SECRET" -r < body.json | cut -d' ' -f1)"
```

Each webhook has a queue of 256 entries, which drops its oldest when it is
full, and delivers them one at a time, so a slow endpoint delays neither the
sessions nor the other webhooks. A delivery is tried once, with 5 seconds to
answer; redirects are not followed, and an answer other than 2xx is logged as
a warning. When the server shuts down it stops the webhooks first, so the
`stopped` statuses of the sessions it stops then are not delivered. User info
in a URL (`https://user:password@host/…`) is sent as `Authorization: Basic`,
as Go's HTTP client does. The Events page shows each webhook by its host.
`GET /api/integrations` returns its URL without its user info, query string and
fragment, and never its secret, but with its path: for a service that puts its
credential in the path (Slack, Discord), an admin reading that route sees it.
Logs name a webhook by its place in the list and its host.

**Private addresses.** A webhook may not point at a loopback, link-local,
private (unique-local in IPv6), shared (CGNAT, `100.64.0.0/10`, where some
clouds keep their metadata service) or unspecified address, nor at a
deprecated IPv4-compatible IPv6 address (`::a.b.c.d`); an IPv6 address that
carries an IPv4 address counts as the IPv4 address it reaches: NAT64
(`64:ff9b::/96`, or a /96 in `64:ff9b:1::/48`), 6to4 (`2002::/16`), Teredo
(`2001::/32`, its server and its client) and SIIT (`::ffff:0:a.b.c.d`). An
address of `64:ff9b:1::/48` is read as a /96; one in the form a shorter NAT64
prefix gives (u octet zero and the last three bytes zero), or with a non-zero
u octet, is refused; `allowPrivate` sends to it. That keeps webhooks away from
the services of the server's machine and network that are not published; it
does not refuse the server's own public address. At startup the server looks
up the webhooks' hosts, all at once and for at most 2 seconds, and refuses to
start when one resolves to such an address; a host that does not resolve in
time is only logged as a warning, since DNS may be down while the server
starts. Before every connection it looks the host up again and connects only
to the addresses that pass, trying the next one when an address does not
answer (and the other address family in parallel), so a name pointed at a
private address later (DNS rebinding) reaches nothing; it never goes through
a proxy.
`"allowPrivate": true` lifts the rule for one webhook, for an endpoint on
your own network.

## Crews

A crew is a saved team of agents; launching one starts a **run**, with a
session for each member as it starts. Build crews on the **Crews** page (**G**
then **C**): a name, a shared goal, a working directory, and up to 12 members.
Each member has a name, an agent from the catalog, optional extra arguments (for
agents that take them), a role prompt and a start condition. The goal reaches the role prompts
as `$GOAL` (or `${GOAL}`), which is replaced by the goal before the prompt is
typed. Crews are saved one file each, `crews/<id>.json` in the data directory
(a `crews.json` from an earlier version is split up at the first start), and run
on the server only for now (members are ordinary server sessions, under
`allowedRoots`); the **My machine** option is disabled.

The working-directory field completes as you type: the server lists the
directories under its allowed roots, at most 50 at a time (hidden ones once
you type the dot), and marks git repositories: "git" for one with a commit,
which a crew with worktrees needs, "git, no commit" for one without. Enter
keeps what you typed; to take an entry, move to it with the arrow keys or the
pointer and press Enter, or click it. A path outside the roots shows which
roots there are. The editor says under the field whether
the crew could launch there with worktrees; the launch itself still decides
(`409 not_a_repo`). The Launch dialog's working directory completes the same
way. A crew whose member's agent is not installed on the server is refused at
launch (`invalid_crew`, naming the member).

**Example crews.** `conductor serve --examples` (or `CONDUCTOR_EXAMPLES` set to
`1` or `true`) adds five example crews the first time: `example-todo-app` (a
lead that plans, two builders after it, a tester after the second builder),
`example-todo-app-tasks` (the same app run by Yardmaster's tasks:
see below), `example-test-fixer`, `example-docs-writer` and
`example-dependency-upgrade`, each using Claude Code and Codex, the server's
default working directory and a worktree per member. They are ordinary crews once saved: edit or delete them
freely. A crew whose id has a file is never touched, so edits survive the
flag, and a deleted example comes back on the next `--examples`. The empty
**Crews** page offers **Load the examples**, which does the same
(`POST /api/crews/examples`). A worktree starts from the checkout's `HEAD`, so
what an earlier member wrote is on its branch only: a member that starts after
another begins by merging that member's branch (`crew/$CONDUCTOR_RUN/<member>`,
the run's id being in its environment), and the todo app's tester merges the
lead's and the second builder's, then waits for the first builder's and merges
that too. The todo app writes files in its working directory, so point it at a
fresh repository with one commit, not at a real project.

**Example: todo app, with tasks** is the way to see Yardmaster, Conductor's
agent harness, at work. It builds the same app (in Python, with its standard
library only, so each check is a fixed command with nothing to install) as
five tasks instead of start rules: `plan` (the lead writes PLAN.md, checked by
`test -s PLAN.md`), then `core` and `cli` at once, each from the plan's result
and checked by `python3 -m unittest discover -s tests -t .`, then `tests`,
which needs both (its worktree starts at core's result and merges cli's) and
runs the whole suite, then `review`, which approves or sends `tests` back with
findings. Loading the examples again adds it for anyone who loaded them before
it existed. Point it at a fresh repository with one commit, launch it, and
the run page's **Tasks** tab shows each task wait, run, get checked and pass
or go back.

A member starts `immediately` at launch, `after <member>` once that member,
with its own prompt typed, first reports it is done (idle), or `manual`, when
you press **Start now** on its tile. A member's role prompt is typed into its
terminal as one line (line breaks and tabs become spaces, as in a handoff or a
broadcast; the crew keeps the prompt as you wrote it) once the agent is ready
for it: it reports that it waits for input or is done, or, at least two
seconds after the start, its output has been quiet for a second. An agent that
only animates a spinner in its window title counts as quiet, and one between
two screens of its start (Claude Code turns bracketed paste off while it
loads) is waited for. Conductor types the prompt as a terminal pastes text:
the text, as a bracketed paste when the agent asks for one, then Enter on its
own 250 ms later, so that Codex (which reads a fast burst of keys with Enter
in it as a paste) and Claude Code (which collapses a long one) both run it;
handoffs, broadcasts and the reply bars go in the same way. Claude Code reports
taking a prompt: when it has not within 3 seconds, Enter is pressed once more
and the run's log says so. A question that comes up in the 250 ms before the
Enter keeps the Enter back: the text waits in the agent's input, nothing is
typed twice, and the run's log tells you to press Enter once you have answered. For Claude Code
and Codex, whose trust question Conductor watches for on the screen, the pause
lasts until the screen has been quiet long enough for the watcher to have looked
at what was drawn since the text (at most two seconds more), so a trust
question drawn in that moment holds the Enter back too.
After 60 seconds the prompt is typed anyway and the run's log says so, except
while a trust question shows (see below). Every member's process sees
`CONDUCTOR_CREW` (the crew's id), `CONDUCTOR_RUN` (the run's id),
`CONDUCTOR_MEMBER` (its name) and `GOAL`, next to the usual
`CONDUCTOR_SESSION_ID` and notify variables.

**Trust the repository once.** Claude Code and Codex ask whether to trust a
folder the first time they start in it, yolo or not, and a worktree of a
repository they trust asks nothing. Before a crew's first run in a repository,
including the fresh one the todo-app example wants, open `claude` and `codex`
there once and accept. A member that meets the question anyway is held, never
typed into: its tile and the sidebar show that it needs you, with the
question's words, the run's log says so once, and its prompt is typed after
you answer it in its terminal with Enter (an arrow key that moves the
selection does not count as an answer). With yolo on, Codex is trusted for that
launch alone, through `-c projects={…}` naming the repository, and nothing is
written to `~/.codex/config.toml`; Claude Code has no such option, so trust it
once yourself.

The question's answers are choices wherever the prompt shows: the session
page's reply bar, the sidebar's row, the Yard's card and the chat, from the
agent's `trustAnswers` in the catalog (the label and the keys that pick it,
the trusting answer first). The keys matter: Claude Code highlights "No,
exit", so a plain Enter there ends the session, and its trusting answer is
Down then Enter; Codex highlights the trusting answer. Typed text is refused
while the question shows ("the agent asks whether to trust the folder…"),
since its Enter would pick whatever the dialog highlights.

Codex asks a second startup question when its hooks are new or changed
(Codex 0.161 runs hooks only once trusted): "Hooks need review", with Review
hooks highlighted. Conductor holds it the same way: a crew member waits,
typed text is refused, and the choices are **Trust all and continue** and
**Continue without trusting**. Two more are held too, seen when Codex runs
beside a newer Codex background server: "Background server has incompatible
feature settings" (**Run without the daemon this time**, or **Cancel**, which
ends Codex) and "Update available 0.161.0 → 0.162.1" (**Skip this update**,
or **Skip until next version**); an Enter typed there would have cancelled
Codex or run `npm install -g` to update it. An agent's further startup
questions are its `questions` in the catalog, each a `prompt`, its `answers`
and, when the words to show are not the ones that find it, its `words`.

Codex also asks during its work, before a command it would run outside its
sandbox: "Would you like to run the following command?" with the command and
**Yes, proceed**, **Yes, and don't ask again** and **No, tell Codex what to
do differently**. Conductor reads that dialog off the screen for the
session's whole life, whatever Codex's hooks file holds and whether anyone
watches: the member is in Needs you with the question, the command and the
three choices, each of which types its own key (`y`, `p`, Esc); typed text
is refused while it shows, since its letters are the dialog's keys and its
Enter would approve. A catalog question marked `anytime` is held this way.
Claude Code's dialogs during the work are held the same way, their choices
read off the dialog each time it shows (the catalog's `answersFromScreen`),
since they change with the tool, the permission mode and the release.

**Worktrees.** With isolation set to *Git worktree per agent*, the working
directory must be in a git repository that has a commit (otherwise the launch
answers `not_a_repo`), and `git` must be on the server's `PATH` (otherwise it
answers `launch_failed`: git is not installed on the server). Each member gets its own checkout and branch:

```
git worktree add --no-checkout -b crew/<run>/<member> <cwd>/.conductor/worktrees/<run>/<member> HEAD
git -C <cwd>/.conductor/worktrees/<run>/<member> reset --hard
```

Conductor runs it, as every git it runs, without the programs the
repository's configuration names: no hook (no `post-checkout`) and no
clean or smudge filter, your global configuration's included, so a file a
filter stores differently comes out as the repository stores it (a Git LFS
file as its pointer, until `git lfs checkout` in the worktree). The
run's id is the crew's id and eight hex digits. A crew whose `cwd` is
below the top of the repository starts in the same subdirectory of its
worktree. The first worktree adds a `.conductor/` line to the repository's
`.git/info/exclude`, so the worktrees stay out of `git status` without touching
a tracked file. Conductor never deletes a worktree or a branch, not when the
run stops and not when the crew is deleted: remove them yourself with `git
worktree remove` and `git branch -d` once you have merged what you want. With
*Shared working directory* every member works in the same directory.

**Two nouns.** A *crew* is a saved plan: members, agents, first prompts, who
waits for whom. A *run* is one launch of it: live sessions, branches, a log.
The Crews page (`/crews`) shows the runs going now above the saved crews; a
saved crew's page (`/crews/<id>`) has its **Setup** and its **Runs**, and
says when a run of it is live; a run's page (`/runs/<id>`) is named by its
crew and its start ("users api · run started 08:31") with its short id, or by
the name it was given at launch (the arrow beside **Launch run**, or
`conductor up <crew> --name`), which a resume keeps.

**The run page.** `/runs/<id>` shows a live tile for every member, with its
branch and a diff count (`+12 −3`), a feed of the members' events and the run's
own log, and how many members need input. A tile is the member's terminal at
the tile's size, as on the Yard: click into it and type; its expand button, or
a double-click on its header, opens the session's page. The diff counts the
lines of tracked files the member's worktree adds and removes against the
commit it began from, committed or not; untracked files are not counted, and
the numbers are read again when the run changes, at most every 10 seconds. The
page follows the run through the event stream and polls nothing. A member that
has not started yet shows a placeholder ("starts after lead is done", "waits
for Start now"), a pending one offers **Start now**, and an ended one
**Resume** (see [Resume and relaunch](#resume-and-relaunch)). A member a stop
cut off reads "not started" or "stopped" in grey; red is kept for a member
that ended with an error of its own. The header has **Add agent** (a member
joins the run, not the saved crew), **Share** and **Stop**; stopping ends every
session and leaves the worktrees, and a stopped run offers **Resume as new
run**.

- **Broadcast.** Every member with a session is ticked, and one that starts
  later is ticked when its tile appears; untick the ones to leave out (the
  ticks stay when the page reads the run again). Type one line and it goes into
  all of them at once. The button counts the members the line will reach and
  says apart how many it will skip because they wait on a prompt: such a member
  is skipped, so the line cannot answer it by accident, as is one that is not
  running, and one where a question comes up in the 250 ms before its Enter
  keeps the line in its input without Enter. The toast names who got the line
  and who was skipped, and why. Each line is recorded as an input in the
  member's activity under your display name, or the server's user when you
  have not set one.
- **Share** creates a run link with the **View** or **Control** role. It
  grants that role on the session of every member of the run, members added
  later included, and on no other session; the join page lists the members.
  With a **Control** link their tiles take keys as the run page's do; with a
  **View** link each tile shows the member's whole screen scaled to fit, and
  never resizes the session.
  Revoking it disconnects everyone who came in through it. A crew saved with
  **Create a view link (8h)** gets a view-only link, labelled `launch`, when it
  is launched. Its URL is shown once on the run page and printed by `conductor
  up`; nothing shows it again. Whoever joins through a link has **Leave** in
  the page's header: the terminals go, the card comes back, and the link
  still joins.

**The Crews page.** `/crews` opens on **Running now**: one card per run
going now, named by its crew and its start, with a badge (**running**, or
how many members **need you**), the members as chips in the shape of the
crew with a status dot each, a line for every member that asks a question
with **Answer** beside it, how long it has been up, **Stop** and **Open run**.
Below, **Saved crews** is a table of plans: name and goal (and "1 live run"
when one is going, leading to it), the crew's shape in miniature with a few
words ("5 · a chain of 3, 1 by hand, 1 alone"), where its runs work, its
last runs as bars (height how long each went, colour how it ended: green
finished, grey stopped, red a member failed, an amber edge when someone had
to answer) with when the last one started, **Edit** and **Launch run**. A
saved crew never says "Running": status belongs to runs.

**A saved crew's page.** `/crews/<id>` has the crew's name in its header
(edit it there), **Duplicate**, a menu with **Delete crew**, and **Launch
run**. A banner says when a run of it is live ("started 08:31 · 3 running ·
review needs you · docs waits for Start now") and leads to it. **Setup** holds
the goal, where runs work (**Runs in**, with the directory's git state and a folder button that browses the allowed roots), what
each run does (one working directory or a git worktree each; the line under
it, which opens the switches, says whether a launch opens the run page,
makes a view link and which yolo it uses), and the members as a table (name,
agent, first prompt, when it starts: **At launch**, **After lead is done**,
**When you press Start**; a row's menu adds extra arguments, moves or removes
it) or as the graph of their start rules. A sentence beside the heading reads
the start order ("lead → core → tests start in a chain; docs waits for you;
review starts at once"). An edit gets an **Unsaved changes** bar at the
bottom naming what changed, with **Discard** and **Save**; while a run is
live it says the run keeps the version it launched with. A crew not saved
yet is drafted at `/crews/new`.

**Runs.** The crew's **Runs** tab charts its last twelve runs as bars (minutes,
with the median, how many needed an answer and how many failed) and lists
every run, newest first, the live ones and the records of those that ended:
when it started with its short id, its state (**needs you**, **running**,
**finished**, **stopped**, **failed**: a member ended with an error of its
own, whether the run was then stopped or not), its members as tiles with a
status dot each, how long it took, a note (who asks a question, what failed,
how many a stop cut off, what changed as `+412 −58 on 5 branches`), and
**Stop** and **Open** while it goes, **Resume as new run** and **Open** after.
Chips above the table filter the rows by state (all, live, finished, stopped,
failed, each with its count) and a select by when they started (today, 7 or
30 days, all time); the browser keeps the choice. A member that reports it is done keeps its run running, for a done agent is
idle, not gone. The pages follow the runs through the event stream, as the
run page and the sidebar do. A crew's **Yolo** setting (the server's default,
on or off; see [Yolo](#yolo)) is fixed on the run when it is launched, so
every member, one started or added later included, follows it.

**The graph.** The run page has a **Graph** tab beside the tile grid: one
node per member (agent, name, status dot, a needs-input badge, branch and
diff, Start now or Resume where they apply), solid edges labelled "when done"
for "starts after X is done" and dashed, moving edges for the handoffs delivered in this run,
with a count and the last message on hover. Roots sit on the left (or on top:
the direction and the handoff switch are remembered by the browser), a click
selects, a double click or Enter opens the member's session. A managed run's
graph has a **Tasks | Members** switch and shows the tasks unless switched:
one node per task (kind, id, state, member, attempt, what it does now, Open)
in columns by what it needs, with a dotted `needs` edge from each task to the
tasks that need its result. The crew editor
has the same graph as a second mode of the members section: drag from a
member's right handle to another's left and that one starts after it; the ×
on an edge removes the rule (the member starts at launch); a node's menu
sets a rule by hand or removes the member, and a member started by hand is
drawn dashed. A member waits for at most one
other, so a second parent, a self edge and a cycle are refused with a word
on why. On a phone the graph is a list indented by depth with the same
badges, and the Crews table shows each crew's shape in miniature.

**The tasks.** Yardmaster, Conductor's agent harness, runs a crew with tasks
as a managed run (see [Crews](#crews)); its run page has a **Tasks** tab first: one row per task with its state,
kind, member, attempt, what it does in words (what it waits for, which
attempt runs or is checked, how it ended, why a ready task does not start),
its result commit and its actions: **Open** the attempt's session, **Retry** a
failed or cancelled task (one more attempt), **Accept anyway** a failed task's
last submitted commit as its result (asked first), **Cancel** a task (asked
first), **Copy branch**. A row opens the task: its result (summary, issues, a
review's findings) and every attempt with where it started, what it submitted
and what failed it. The header says `3/7 tasks`, `1 failed`, `2 running`,
`1 checking` and `paused`, and **Pause** holds scheduling (attempts in
flight go on, no task starts) until **Resume**. Every one of these is a
command with a receipt: the browser mints an id, a reply lost to the network
is asked for once more with the same id and answered as the first, and a
refusal says why. The palette lists the tasks under the page's group, each
with the same actions. A managed run outlives the server: started again on
the same data, the server concludes what was left open, the run reads as
interrupted, and **Continue** (the resume route) makes a new run that keeps
every result that stood, verifies again a result that was being checked,
takes up the rest, and leaves an attempt whose launch was never known for a
person to retry. The members pass each other what the next task needs to
know beyond a result as messages, typed into the recipient's session once it
has its task and read from its inbox; a member that something stops says so
(the task shows as blocked, and the session asks the people watching); and a
member saves checkpoints as it goes, so an attempt that ends without a
result, or a continuation, starts from the last one rather than from
scratch.

**The timeline and the charts.** A **Timeline** tab beside the graph draws
one bar per member from its start to its end or now, amber where its session
waited for input (the feed's attention entries), a mark with a line to the
other member's row for each handoff, and the run's stopped line; a hover
reads the exact times. Runs that end (stopped or finished) are recorded as
`runs/<id>.json` in the data directory (the run as the API answers it, never
a terminal's contents; at most 500 kept) and read back after a restart, so
a crew's Runs tab lists them and charts the last twelve as bars (minutes,
coloured by outcome, an amber edge when someone had to answer), once there
are two, and the Crews table draws the same bars beside each crew.
The Events page's feed draws the last hour's events per minute (what needs
you, handoffs, errors, the rest) once two are in it, and the Yard's header shows its sessions by attention state as a slim
stacked bar from two sessions on. Nothing is charted with fewer than two points.

**Handoffs.** A member passes work to another with an event, which the
[Conductor skill](#events-and-hooks) teaches the agent to send:

```bash
"${CONDUCTOR_BIN:-conductor}" notify --event handoff --to tests --message "/v1/users is ready"
```

Conductor types `Handoff from core: /v1/users is ready` into the member named
by `--to`, on one line and as a prompt is typed (the text, then Enter on its
own), as soon as that member is running and not waiting on a prompt; a
question that comes up before the Enter keeps the line in its input without
Enter, noted in the run's log. Up to 10 handoffs wait for a member; past that
the oldest is dropped.
A handoff to a name that is not in the run, or to a member that has not
started, is noted in the run's log and
nothing else happens.

**From a shell.**

```bash
conductor crews                        # one line per saved crew: id, name, members
conductor crews --ids                  # the ids only, one per line (for shell completion)
conductor up <crew-id> [--name N] [--open]  # launch a crew (named, with --name); prints the run, its URL and its view link
conductor up --resume <run-id> [--open]     # resume a stopped run as a new run; prints the new run and its URL
```

Both talk to the server at `--server` (env `CONDUCTOR_SERVER`, default
`http://localhost:8080`) with the workbench token from `--token` (env
`CONDUCTOR_WORKBENCH_TOKEN`). A plain `http://` server on another machine
is refused before any request unless `--insecure` says to go ahead (the
token would travel to it in the clear); one on this machine needs nothing.
`--open` opens the run's page in your browser.
`conductor up` prints the run, the URL of its page and, for a crew set to create
a view link, that link on a third line, `view <url>`.

**MCP tools.** For agents that take an MCP server at launch, Claude Code
(`--mcp-config`) and Codex (`-c mcp_servers.conductor.…`), every launch
registers `conductor mcp`, a stdio server with the skill's commands as tools:
`report`, `set_state`, `ask` (with choices), `form_crew`, `add_member`,
`run_status`, `link`, for a member of a managed run `task_context`,
`task_result`, `message_send`, `inbox_read`, `inbox_ack`, `task_blocked` and
`task_checkpoint` (`conductor task context|result|send|inbox|ack|blocked|
checkpoint` on the command line), and for a member of a run linked to a
Dispatch ticket `ticket_read` and `ticket_comment` (`conductor task ticket`,
see [Dispatch](#dispatch)); outside a session the tools say so and do
nothing.
`agents.mcp: false` (`CONDUCTOR_AGENT_MCP=0`) turns the registration off.
The server's launches register it; `conductor host` does not yet, and the
other agents get the skill's commands alone.

**Agents that form crews.** An agent running in a Conductor session can form a
crew around its own session, from inside it, with no workbench token: the session's
own token (the one `conductor notify` uses) is accepted on four routes scoped to
that session, on by default (`agents.selfService: false` or
`CONDUCTOR_AGENT_SELF_SERVICE=0` turns them off). The skill teaches the agent
to write a `crew.json` and run:

```bash
"${CONDUCTOR_BIN:-conductor}" crew create crew.json --self lead --open   # the crew, launched, with this session as "lead"
"${CONDUCTOR_BIN:-conductor}" crew add member.json                       # one more member of this session's run
"${CONDUCTOR_BIN:-conductor}" crew status                                # the run and its members
"${CONDUCTOR_BIN:-conductor}" crew link --ttl 2h --label "for the PR"    # a view-only link to this session
```

`crew.json` is the crew as the API takes it (`name`, `goal`, `members` with
their `start` rules, `self` naming the member this session becomes); `-` reads
stdin. The crew's working directory and yolo setting are the session's own and
cannot be chosen, the session is adopted as the member named by `--self`
(running, with its prompt counted as typed, so members that start "after" it
wait for its next done, and handoffs reach it), and the other members start
as at a launch. `--open` makes the workbench offer the run: a toast on every
page with **Open**; nothing navigates on its own. Links are view-only and last a
day at most. Bounds: 2 crews per session per hour, 5 links per session per day,
the crew limits below; a hosted session cannot form a crew (its directory is not
on the server). Outside a session the commands exit 0 silently
(`--quiet=false` says why), as `conductor notify` does.

Limits:

- No limit on the number of crews (the Crews page and `conductor crews` page
  through them); 12 members a crew, and a run takes no more than that.
- Name 60 characters, goal 2000, role prompt 4000; with the goal in it a prompt
  is at most 32756 bytes, what one write carries with the paste markers around
  it.
- 32 extra arguments a member, 8 KiB in all; 1 MiB for a whole crew as its file;
  request bodies of 2 MiB on the crew routes.
- A broadcast line is at most 4096 bytes.
- 10 handoffs waiting for a member; 200 entries in a run's log; 100 links for a
  run.
- Runs live in memory: the server keeps up to 100, and a restart forgets them
  (the worktrees stay); the records of runs that ended survive, 500 at most.
  Saved crews survive.

## Dispatch

Dispatch is Conductor's own work tracking: projects, and tickets with keys
that never change (`CND-12`), their acceptance criteria, owner, discussion and
history, in the server's data directory (see `docs/protocol.md`, Dispatch). A
ticket links to the managed run working on it, and the run's members read it.

**From a shell.**

```bash
conductor dispatch projects                 # one line per project: key, name, open and done tickets
conductor dispatch tickets [--project CND] [--status ready]... [--q "cache key"] [--json]  # the whole list, a line a ticket
conductor dispatch show CND-12              # the ticket whole: its fields, description, criteria, links and last comments
conductor dispatch create --project CND --title "Try the CLI" [--description-file notes.md] [--priority high] [--owner Alice] [--label api]...
conductor dispatch edit CND-12 --status in_review --assignee +Bob --label -api --milestone 1.0 --horizon now
conductor dispatch comment CND-12 "the fix is in"
conductor dispatch accept CND-12 --note "checked by hand"
conductor dispatch link CND-12 --kind run --ref <run-id>   # or --kind delivery|external --url <url>
```

They take `--server`, `--token` and `--insecure` as `conductor crews` does
([Crews](#crews), From a shell), before the key for `comment`, whose text is
every word after the key, one that starts with `-` among them. `create` prints
the new ticket's key; labels and milestones are given by name; a write whose reply is lost to the network
is sent once more with the same operation id, which the server answers
without a second change. No command makes or edits a project: the routes do
(`POST /api/dispatch/projects`, `docs/protocol.md`).

**From an attempt.** In a session of a run linked to a ticket, the agent reads
the ticket and comments on it, nothing more, with its session's own token; the
skill teaches it:

```bash
"${CONDUCTOR_BIN:-conductor}" task ticket                          # the ticket as Markdown (--json for its JSON)
"${CONDUCTOR_BIN:-conductor}" task ticket comment "found the cause"   # a comment of the run's member, agent:<member>
```

or the MCP tools `ticket_read` and `ticket_comment`. A run is linked to one
ticket at most; with none, both say so.

## Yolo

With yolo on (`"yolo": true` in the config, `CONDUCTOR_YOLO` set to `1` or
`true`, or `conductor serve --yolo`), every agent the server launches skips its
permission prompts, and the server says so in a warning at startup.
`CONDUCTOR_YOLO` set to `0` or `false` turns the config file's `yolo` off. A
launch overrides the server: the Launch dialog has a **Yolo** switch, and
`POST /api/sessions` takes `yolo: true` or `false`; so does a crew (the
editor's **Yolo** select: the server's default, on or off). Each built-in
carries a yolo recipe, arguments put after its command and your extra
arguments, and variables set in its environment through the same filtered
environment as its own `env` (a recipe cannot set `CONDUCTOR_*`):

| Agent | Recipe | What it turns off |
|---|---|---|
| Claude Code | `--dangerously-skip-permissions`; the warning Claude Code shows before its first such launch is skipped through the hooks settings file | every permission prompt; protected paths such as `.git` and `.claude` become writable; deny rules still apply |
| Codex CLI | `--dangerously-bypass-approvals-and-sandbox`, and the repository trusted for this launch | every approval **and the sandbox**: the agent reaches the network and the whole filesystem as the server's user |
| Antigravity | `--dangerously-skip-permissions` | every tool permission request |
| Copilot CLI | `--yolo`, `COPILOT_ALLOW_ALL=true` | every tool, path and URL prompt, and its folder-trust question |
| Cursor CLI | `--yolo --trust` | command and MCP approvals, and its workspace-trust question |
| OpenCode | `--auto` | every permission that is not explicitly denied |
| oh-my-pi | `--yolo` | approvals, unless a deny policy matches (already its default) |
| aider | `--yes-always` | every confirmation, shell commands included |
| Goose | `GOOSE_MODE=auto` | tool approvals (already its default) |
| Amp | `--dangerously-allow-all` | every tool approval |
| DeepSeek Harness | `DSH_PERMISSION_MODE=danger-full-access` | its sandbox and every approval |

pi has no permission system to skip and Shell has nothing to ask, so they have
no recipe. The recipes of Claude Code, Codex and Copilot were checked against
the real CLIs; the others come from their documentation (the adapter matrix in
[docs/features.md](docs/features.md) says which). A session launched with its
agent's recipe shows a **yolo** badge in its header, its tile and the sidebar:
the badge means Conductor applied the recipe, not that no question can appear
(a trust question still can; see Crews). An agent without a recipe is launched
as it would be without yolo, with a note in its activity (and, for a crew
member, in the run's log) and no badge. Edit a recipe on the Agents page
(**Yolo recipe**) or in the catalog (`"yolo": {"args": [...], "env": {...}}`):
a saved agent that replaces a built-in and leaves `yolo` out keeps the
built-in's, and `"yolo": {}` gives it none. Sessions started with
`conductor host` take no yolo in this version.

## Resume and relaunch

An ended session (exited or stopped) offers **Resume** in its header, on its
tile on the run page (the Yard's grid shows only running sessions, so on the
Yard it is in the expanded view's header), on its row in the sidebar's
**Exited** section, and on an ended member's tile on the run page. Resume
starts a new session with the same agent, name, working directory, arguments
and yolo choice and, for a crew member, the same run, branch and worktree,
launched with the agent's resume arguments for its own session id:

| Agent | Its session id | Resumed with |
|---|---|---|
| Claude Code | chosen by Conductor at launch: `--session-id <uuid>` | `--resume <id>` |
| Codex CLI | reported by its notify payload (`thread-id`) | `codex resume <id>` |
| Copilot CLI | chosen by Conductor at launch: `--session-id <uuid>` | `--session-id <id>` |
| Antigravity | reported by its hooks (`conversationId`), once they are installed | `--conversation <id>` |
| Cursor CLI | reported by its hooks (`conversation_id`), once they are installed | `--resume <id>` |
| pi | chosen by Conductor at launch: `--session-id <uuid>` | `--session-id <id>` |
| Goose | chosen by Conductor at launch: `goose session --name cdr-<uuid>` | `goose session --resume --name <id>` |

The agent's conversation comes back; its permission mode and model come from
the new launch, which applies the yolo recipe again when the session had it.
An agent without a recipe (OpenCode, oh-my-pi, aider, Amp, DeepSeek Harness,
Shell), or one that has had no turn yet (Claude Code keeps nothing to resume
until it has been prompted), offers **Relaunch** instead: a fresh session, and
the toast says it started anew; a crew member that is relaunched gets its role
prompt again, while a resumed one gets none, since its conversation has it. The
original arguments are passed again, so a prompt given as an argument
(`claude "fix the tests"`) is sent again as well. One running session holds a
conversation: resuming it a second time while that one runs is refused. Resume
is there while the ended session is listed (`exitedRetention`, 10 minutes by
default) and, for a crew member, while the server keeps the run; a member of a
stopped run, and a hosted session, cannot be resumed in this version.

A stopped run can go on in two ways. **Resume** on an ended member of a run
whose stop completed resumes that member in place and reopens the run: the
run is running again, the other ended members stay ended until you resume
them one by one, and **Stop** stops it again. **Resume as new run** (the run
page's header, or the run's row on the crew's Runs tab) starts a new run of the
crew in which every member whose conversation is resumable continues it in
its kept worktree and branch, without a new prompt (its next done starts the
members after it), while the others start afresh under their start rules;
the new run names the old one it resumed, and the old one names it.

## Desktop app

Conductor also runs as a desktop app (macOS, Linux as deb, rpm and
AppImage, Windows through WSL 2): an Electron shell that starts the server
on a free loopback port with a workbench token minted for the run, opens the
workbench in a window signed in with it, keeps the server in the tray when
the window closes, and stops it when the app quits. Its **Settings** page
holds what the server starts with (the data directory, the allowed roots,
the default directory, yolo, reach) and restarts the server when those
change, which the save's toast then says; **Open in browser** opens the same server in your browser, signed
in. On Windows the server runs inside your WSL 2 distribution (the app sets
it up on first run; the agents are the ones installed there). While the
server starts, a small splash shows the version and what the app is doing.
Ctrl+= and Ctrl+- (Cmd on macOS) zoom the workbench, Ctrl+0 resets it, and
the app remembers the level.
The macOS, Windows and AppImage builds update themselves from the GitHub
releases: the app checks on start and every six hours and downloads an
update in the background, and the update installs when you quit. The app
never opens a dialog for an update. The workbench says it once per version,
in a toast with **Restart to update**, and keeps a button in the sidebar's
header until the update installs or a newer one replaces it; the tray's
menu offers **Restart to update** too. Whichever asks, the workbench first
says what the restart ends: the server, and the sessions running on this
computer. An update the app downloaded that did
not install (it started again as the version before) is said once, with
the way to the app's log. The deb and rpm builds are updated by your
package manager.
`make desktop-dev` runs it from a checkout; `desktop/README.md` has the rest.

**Windows.** There is no Windows build of the server: the installer bundles
the Linux binary and the app runs it inside your WSL 2 distribution
(`wsl --install` once, restart, open the distribution to make your user).
The agents must be installed inside the distribution: the app asks your own
shell there (`$SHELL -ilc`, so nvm's, npm's and `~/.local/bin`'s programs
count) for its PATH and starts the server with it (a shell that prints
nothing in time is asked again, and again at the server's next start, so
Restart server in Settings repairs a start that was left with the plain
login shell's PATH); a `claude`, `codex` or
`npm` that Windows put on the PATH under `/mnt/c` is named on the Agents
page as found on Windows, not used. The settings' directories are the
distribution's own (`/home/<user>/…`), and the folder picker browses the server's own folders, so it lists the distribution's and never a Windows path;
projects under `/mnt/c` work but are slow, and need the Windows-folders
switch; keep repositories in the distribution's home.
WSL's default NAT mode stays as it is (the app never asks for mirrored
networking, which changes WSL for Docker and every other tool): for WebRTC
the app forwards one UDP port into the distribution itself and the server
advertises the Windows address, so sharing through a switchyard, or by a
paste invite, works from WSL with nothing to configure; the installer adds
the firewall rule for the port, and the Settings page offers it when it is
missing. You never configure Windows networking by hand for Conductor: a
plain link to the server inside WSL is for the distribution alone, and the
public-address path from WSL (the TLS listener mapped on the router) is the
app's to forward in a later round. Closing the
window keeps the server running in the tray; quitting the app stops it
with `kill` inside the distribution, never `wsl --terminate`, so your
other WSL shells are left alone.

## Shell completion

`conductor completion zsh` or `conductor completion bash` prints a completion
script: subcommands, flags (`conductor serve`'s `--examples` and `--yolo`
among them) and their values, and for `conductor up` the crew
ids, read from the server as you type through `conductor crews --ids` (which
prints ids only, and nothing, with exit 0, when there is no workbench token in
`CONDUCTOR_WORKBENCH_TOKEN` or the server in `CONDUCTOR_SERVER` does not answer
within two seconds). The ids are only ever offered as words: nothing the server
answers is run by your shell, and an id not shaped like a crew's is dropped on
both sides. Load it with `source <(conductor completion zsh)` in `~/.zshrc`
(after `compinit`), or let `conductor completion install` append a line that
does so, marked `# conductor completion`, to `~/.zshrc` or `~/.bashrc` (the
shell from `$SHELL`, or `--shell`; the file from `--rc`); run again it changes
nothing, since the installed line is matched as a whole line (spaces around it
aside; a comment of yours that contains the mark does not count), and it
refuses a file, a link or a directory that is not yours, naming it, or a link
of yours that leads to someone else's file. After a zsh install it says the rc
file must run `compinit` before that line.

## Configuration

`conductor serve --config conductor.json` reads a JSON file; every field has a
`CONDUCTOR_*` environment override. See [`conductor.example.json`](conductor.example.json).

| Key | Env | Default | Purpose |
|---|---|---|---|
| `listen` | `CONDUCTOR_LISTEN` | `:8080` | bind address |
| `publicUrl` | `CONDUCTOR_PUBLIC_URL` | `http://localhost:8080` | base for share links and the agents' notify URL; while it names localhost, a share link takes the address its request came through instead (see Sharing) |
| `workbenchToken` | `CONDUCTOR_WORKBENCH_TOKEN` | generated (kept in `<dataDir>/workbench-token` while the server runs) | the operator's token: opens the workbench and every management route (the old names `adminToken` and `CONDUCTOR_ADMIN_TOKEN` are still read, with a warning at start) |
| `hostTokens` | `CONDUCTOR_HOST_TOKENS` | workbench token only | tokens accepted from `conductor host` |
| `allowedRoots` | `CONDUCTOR_ALLOWED_ROOTS` | current directory | where server sessions may run |
| `defaultCwd` | `CONDUCTOR_DEFAULT_CWD` | current directory | working directory when a launch omits one |
| `allowedOrigins` | `CONDUCTOR_ALLOWED_ORIGINS` | same host | extra WebSocket origin patterns |
| `iceServers` | `CONDUCTOR_ICE_SERVERS` | Google STUN | handed to browsers and hosts |
| `relayTimeoutMs` | `CONDUCTOR_RELAY_TIMEOUT_MS` | `8000` | wait before falling back to relay |
| `viewerRelayKBps` | `CONDUCTOR_VIEWER_RELAY_KBPS` | `128` | what the viewers of one address send hosted sessions' hosts through this server a second, all of them together, in KiB (relayed frames and signaling; one bucket per address, a burst of twice that and never less than 4 MiB, so three 1 MiB saves at once pass at once): a viewer past it is slowed, never cut; on a switchyard and a plain server alike; `0` is no bound |
| `fileView` | `CONDUCTOR_FILE_VIEW` | `view` | who may read session files |
| `scrollbackBytes`, `maxSessions`, `maxViewersPerSession`, `exitedRetention`, `envPassthrough` | matching `CONDUCTOR_*` | see example | limits |
| `catalog` / `catalogPath` | `CONDUCTOR_CATALOG_PATH` | built-ins | launchable agents |
| `dataDir` | `CONDUCTOR_DATA_DIR` | `~/.conductor` (an older `conductor.d` next to the config, or in the current directory, is kept while `~/.conductor` holds no server data, when it is a real directory owned by the server's user) | UI-managed state; must be writable, best outside `allowedRoots` |
| `webhooks` | `CONDUCTOR_WEBHOOKS` (a JSON array) | none | where the server POSTs events, see [Webhooks](#webhooks) |
| — | `conductor serve --print-listen`, `--exit-on-stdin-close` | off | for a parent process (the desktop app): once listening, print one JSON line to stdout, `{listen, publicUrl, pid, version, workbenchToken?, tlsListen?}` (the token only when generated for this run); and shut down when stdin closes, so a parent that dies takes the server with it. A `publicUrl` that names this machine follows the port the listener got (`--listen 127.0.0.1:0`) |
| — | `CONDUCTOR_DESKTOP_DIR` | none | the desktop app's own directory, named by the app for the server it starts (on Windows, its folder in the Windows profile as WSL mounts it): no session's file read reaches it; an absolute path, not a config-file key |
| — | `CONDUCTOR_EXAMPLES` | off | `1` or `true` seeds the example crews once at startup, as `conductor serve --examples` does; not a config-file key |
| `switchyard.enabled` | `CONDUCTOR_SWITCHYARD` (`1`/`true`), or `conductor switchyard` | `false` | coordinate hosted sessions and launch nothing: the launching routes answer `403 switchyard` (see [Switchyard](#switchyard)) |
| `switchyard.relay` | `CONDUCTOR_SWITCHYARD_RELAY` (`1`/`true` on, `0`/`false` off) | `true` | on a switchyard, relay the terminal for viewers whose WebRTC fails; off, they get `relay_off` and relay-only hosts are refused |
| `switchyard.relayKBps` | `CONDUCTOR_SWITCHYARD_RELAY_KBPS` | `0` (no bound) | on a switchyard, how much one host may send through the relay a second (its relayed output and its messages), in KiB, with a burst of twice that and never less than 4 MiB (three 1 MiB files a viewer reads at once pass at once): a host past it is slowed, never cut; a public switchyard's protection against a session that streams |
| `switchyard.openHosts` | `CONDUCTOR_SWITCHYARD_OPEN_HOSTS` | `false` | on a switchyard, register a host that presents no token, under the per-address bounds below; a wrong token is still refused, and a host token marks a trusted machine outside them |
| `switchyard.openHostSessions` | `CONDUCTOR_SWITCHYARD_OPEN_HOST_SESSIONS` | `4` | live hosted sessions the open hosts of one address may hold |
| `switchyard.openHostRegistrationsPerMinute` | `CONDUCTOR_SWITCHYARD_OPEN_HOST_REGISTRATIONS` | `6` | how often one address may register an open host |
| `switchyard.openHostRelayKBps` | `CONDUCTOR_SWITCHYARD_OPEN_HOST_RELAY_KBPS` | `128` | what the open hosts of one address relay a second, all of them together, in KiB (one bucket per address, a burst of twice that and never less than 4 MiB, as `relayKBps`); `0` falls back to `relayKBps`, per connection |
| `switchyard.openHostLinks` | `CONDUCTOR_SWITCHYARD_OPEN_HOST_LINKS` | `32` | the links the open hosts of one address keep on the switchyard at a time (kept links outlive restarts, so they are counted); past it, a link whose host has been gone more than five minutes gives its place to the new one and is ended, and with none such the request is refused with `link_refused` |
| `switchyard.allowedOrigins` | `CONDUCTOR_SWITCHYARD_ORIGINS` (comma-separated host patterns) | `127.0.0.1:*`, `localhost:*` | the browser origins a switchyard answers across origins on the join route and accepts on a hosted session's WebSocket: the desktop app's own workbench |
| `agents.selfService` | `CONDUCTOR_AGENT_SELF_SERVICE` (`1`/`true` on, `0`/`false` off) | `true` | an agent may form a crew around its own session, add members to its run, read its run and mint a view-only link to itself, with its session's own token (see [Agents that form crews](#crews)) |
| `agents.mcp` | `CONDUCTOR_AGENT_MCP` (`1`/`true` on, `0`/`false` off) | `true` | register `conductor mcp` with agents that take an MCP server at launch (Claude Code, Codex), so the skill's reports and crew actions are tools |
| `agents.installSkill` | `CONDUCTOR_AGENT_INSTALL_SKILL` (`1`/`true` on, `0`/`false` off) | `true` | a launch puts the Conductor skill in the agent's skills directory, in the server user's home, once per agent per server start (see [The Conductor skill](#events-and-hooks)) |
| `crews.managed` | `CONDUCTOR_CREWS_MANAGED` (`1`/`true` on, `0`/`false` off) | `true` | a crew with tasks may launch as a managed run (#334); off, such a launch answers `409 managed_off` while managed runs' records stay readable |
| `checks.maxConcurrent` | `CONDUCTOR_CHECKS_MAX_CONCURRENT` (1 to 64) | `1` | how many of the managed runs' checks run at once across the server: a `go test` beside four agents on a laptop is one at a time |
| `yolo` | `CONDUCTOR_YOLO` (`1`/`true` on, `0`/`false` off) | `false` | launch every agent with its yolo recipe, skipping its permission prompts, unless a launch or a crew says otherwise; `conductor serve --yolo` turns it on (see [Yolo](#yolo)) |
| `paths.browse` | `CONDUCTOR_PATHS_BROWSE` | `roots` | what `GET /api/paths` may list: `roots` (the allowed roots alone) or `any` (every directory the server's user can read, asked for with `scope=any`); the desktop app sets `any` for its own server, whose Settings picker chooses the roots and the data directory |
| `reach.mode` | `CONDUCTOR_REACH` | `auto` | how the server finds out it can be reached from outside its network: `auto` asks a STUN server for the public address and, once there is a TLS listener, maps its port on the router (UPnP IGD, PCP or NAT-PMP, renewed and deleted on shutdown; plain http is never mapped); `manual` asks for the address only, for a port you forwarded yourself; `off` asks nothing. `GET /api/reach` reports the result; the Share dialog says what a link reaches |
| `reach.publicPort` | `CONDUCTOR_REACH_PUBLIC_PORT` | `443` | the port on the public address for the TLS listener: mapped in `auto`, forwarded by you in `manual` |
| `reach.publicPort80` | `CONDUCTOR_REACH_PUBLIC_PORT_80` | `false` | also map port 80 to the plain listener, for an ACME `http-01` challenge |
| `reach.stunServer` | `CONDUCTOR_REACH_STUN` | first `stun:` of `iceServers` | the STUN server that answers the public address |
| `tls.listen` | `CONDUCTOR_TLS_LISTEN` | `:8443` once a certificate source is set | the TLS listener, serving the same workbench; `reach.publicPort` (443) is mapped to it. HTTP/1.1 only; no redirect from the plain listener |
| `tls.acme` | `CONDUCTOR_TLS_ACME=1` and the keys below | off | a certificate from Let's Encrypt (or `caDirectory`) through ACME, kept under `dataDir/tls`, renewed on its own (at two thirds of a short certificate's life, thirty days before a long one's end, or when the CA suggests), see [TLS](#tls) |
| `tls.acme.email` | `CONDUCTOR_TLS_ACME_EMAIL` | none | the account's contact |
| `tls.acme.domains` | `CONDUCTOR_TLS_ACME_DOMAINS` (comma-separated) | none: the public address | DNS names or IP addresses the certificate is for; empty orders an IP-address certificate for the address reach finds, once its port is mapped |
| `tls.acme.challenge` | `CONDUCTOR_TLS_ACME_CHALLENGE` | `tls-alpn-01` | `tls-alpn-01` (answered on the TLS listener through the mapped 443), `http-01` (on the plain listener through a mapped 80: `reach.publicPort80`) or `dns-01` (names only, through `dnsProvider`) |
| `tls.acme.dnsProvider`, `tls.acme.dnsEnv` | `CONDUCTOR_TLS_ACME_DNS_PROVIDER`, `CONDUCTOR_TLS_ACME_DNS_ENV` (`K=V,K=V`) | none | `cloudflare`, `exec` or `httpreq`, with its settings under lego's names (`CLOUDFLARE_DNS_API_TOKEN`, `EXEC_PATH`, `HTTPREQ_ENDPOINT`, …); never logged or shown |
| `tls.acme.caDirectory`, `tls.acme.profile` | `CONDUCTOR_TLS_ACME_CA`, `CONDUCTOR_TLS_ACME_PROFILE` | Let's Encrypt; `shortlived` for IP addresses | the ACME directory (`https`), and the certificate profile |
| `rendezvous.enabled` | `CONDUCTOR_RENDEZVOUS` (`1`/`true` on, `0`/`false` off) | `true` | publish every session to a switchyard, where share links are minted and work from anywhere; off, links work where this server is reachable |
| `rendezvous.server`, `rendezvous.token` | `CONDUCTOR_RENDEZVOUS_SERVER`, `CONDUCTOR_RENDEZVOUS_TOKEN` | `https://switchyard.rslabs.net`, no token | the switchyard every session is published to, through the host protocol (as `conductor host` does), named by its address alone (a path allowed: a user name, password, query or fragment in it is a config error that stops the server at start); the token is optional (a host token of a private switchyard, or a trusted seat on the public one); the session is listed there as hosted by this server (`rendezvous.hostName`, the machine's name by default), its viewers served over WebRTC or the relay (`rendezvous.relayOnly`), and share links minted there; the local session's activity records where it is published. The switchyard is told the session's name, agent and folder's last element and its status and attention state, never its command line, OS user, branch, chat or activity; a stop it sends ends the publication, not the session |
| `rendezvous.insecure` | `CONDUCTOR_RENDEZVOUS_INSECURE` (`1`/`true`, `0`/`false`) | `false` | allows a `rendezvous.server` that is plain `http://` on another machine (not `localhost` in lower case, `127.0.0.0/8` or `::1`), which is otherwise a config error that stops the server at start; what is published there then travels in the clear and unverified, and the server logs a warning as it starts. A switchyard on this machine needs nothing |
| `ice.udpPort`, `ice.publicIp` | `CONDUCTOR_ICE_UDP_PORT`, `CONDUCTOR_ICE_PUBLIC_IP` | none | how this server's published sessions (`rendezvous`) gather their WebRTC candidates: one UDP port for every connection, and the address advertised as this machine's in place of its interfaces' own, for a forwarder in front of it (the desktop app on Windows, in front of WSL); with the address set, the connections gather on its family alone, so a machine's IPv6 addresses are never advertised beside an IPv4 forwarder's |
| `tls.certFile`, `tls.keyFile` | `CONDUCTOR_TLS_CERT_FILE`, `CONDUCTOR_TLS_KEY_FILE` | none | a certificate of your own (PEM, with its chain), re-read when the files change; exclusive with `tls.acme` |

### Upgrading

The server keeps UI-managed state (agents added on the **Agents** page, crews,
the hook files) in a data directory that it must be able to create and write
at startup. Unless `dataDir` or `CONDUCTOR_DATA_DIR` says otherwise, that is
`~/.conductor` in the home of the user running `conductor serve`. Earlier
versions used `conductor.d` next to the config file, or in the current
directory without one. A server that finds that old directory, and no server
data in `~/.conductor` (`catalog.json`, `crews/` or `crews.json`; the `hooks/`
that `conductor host` writes there does not count), keeps using it and logs a
warning naming both paths. It keeps it only when it is a real directory, not
a symbolic link, owned by the user running the server: its `catalog.json`
chooses the commands agents run, and anyone may make a `conductor.d` in a
shared directory. A symbolic link or another user's directory under that
name, when it would be kept, stops the server with an error naming it, its
owner and the two settings; set `dataDir` or `CONDUCTOR_DATA_DIR` instead.
To move it, stop the server, move the files in it
into `~/.conductor` (`hooks/` need not move: the server writes it at every
start) and start it again; to keep it, set `dataDir` or `CONDUCTOR_DATA_DIR`
to it. When both `~/.conductor` and the old directory hold server data, the
server uses `~/.conductor` and logs a warning naming the old directory, which
it does not read. Crews an earlier version kept in one `crews.json` move at
the first start to a file each in `crews/<id>.json`, and `crews.json` is
renamed `crews.json.migrated`, which keeps every crew as it was. An agent
override saved on the **Agents** page by an earlier version holds its env
values in full; at start the server stores `***` in
`catalog.json` for every one equal to the config's (and logs the agents it
changed), so that key follows the config from then on and a secret rotated
after the upgrade reaches the agent. A server started without a home directory
(no `HOME`, as some services run) keeps using an old directory too, and needs
`dataDir` or `CONDUCTOR_DATA_DIR` only when there is none. A directory the
server cannot create stops it with `data directory … is not usable`. The Docker
image sets `CONDUCTOR_DATA_DIR=/var/lib/conductor`, declared as a volume. At startup the
server logs the directory it uses, and warns when it overlaps an allowed root:
agents working there can read and commit its secrets.

### Agent catalog

Built-in entries, one for each agent with a hook adapter (see
[Events and hooks](#events-and-hooks)): `claude`, `codex`, `agy`, `copilot`,
`cursor` (runs `cursor-agent`), `opencode`, `pi`, `omp`, `aider`, `goose`,
`amp` and `dsh`, then `shell` (`/bin/bash -l`, which takes no extra
arguments). Add your own or override an entry by ID:

**Identity.** The catalog lists each agent as available when a program of
its command's name is on the server's `PATH`; a name proves little (`goose`
is also a Go migrations tool). For an agent with a hook adapter the server
also runs the program with its version flag, argv only in a spartan
environment, and matches the answer against what the agent prints: the
**Agents** page then shows "Claude Code 2.1.287" or "Codex CLI 0.159.0".
A known other program of the agent's name (the `goose` migrations tool) is
not the agent: the catalog lists that agent as not installed, the tooltip
naming what the program printed, the Launch dialog leaves it out and every
launch of it is refused. A verified check that matches nothing (a new
release may print its version differently) keeps the agent offered with a
"Not Claude Code" warning; a crew with it is refused, and a plain launch
goes ahead with the note in the session's activity. Claude Code's and Codex's version lines were
verified live; the other adapters' come from their documentation and refuse
nothing until the nightly recipes job confirms them. `probe: false` on an
agent turns the check off; `POST /api/catalog/check` runs it for an adapter.
`GET /api/catalog` carries the result as `identity`.

```json
{
  "catalog": {
    "agents": [
      { "id": "claude", "name": "Claude Code", "command": ["claude", "--model", "opus"], "allowArgs": true },
      { "id": "my-tool", "name": "My tool", "command": ["/opt/tool/bin/run"], "env": { "TOOL_HOME": "/opt/tool" } }
    ]
  }
}
```

Commands are argv arrays and never pass through a shell. `allowArgs` lets the
launch form append extra arguments. `disableDefaults: true` drops the built-ins.
`icon` names a Lucide icon (`i-lucide-…`). The workbench carries the icons it
uses and fetches none at runtime: the built-in agents' icons, `i-lucide-wrench`
and `i-lucide-bot` (`web/app/utils/agentIcons.ts`). Any other name shows the
generic agent icon.

**Adding agents from the UI.** Admins can add, change and hide agents from the
**Agents** page (**Add agent**) without editing the config file. The changes are
saved as `catalog.json` in the data directory (`dataDir`) and layered over the
configured catalog at startup: an agent with the ID of a built-in or configured
one replaces it, and deleting that entry brings the original back. Such an
entry inherits what it leaves out: the original's `adapter`, `signal`, `site`,
`yolo` recipe, `trustPrompt`, `trustAnswers` and `session` recipe (an explicit `"yolo": {}` or
`"session": {}` says it has none), and every `env` value it holds as `***`,
which the Agents page stores
for a value the form did not change (or one equal to the original's), so a
change to that value in the config file reaches it. Env keys it does not list
are not inherited: an API client that omits `env` drops every value of the
original, and a key the config file adds later does not reach the entry. An
agent in the config file that replaces a built-in replaces it whole. Hiding an
agent removes it from the launch dialog and lists it under **Hidden** on the
Agents page, where **Restore** brings it back as it was; sessions already
running are not affected. **Clone** makes a copy of an agent with its whole
configuration (its hooks, trust answers, startup questions, session recipe
and stored values included) under a name and ID of its own, so that, say, a
second Claude Code account runs beside the first under its own
`CLAUDE_CONFIG_DIR`; a copy launched with that variable set also gets the
Conductor skill under that directory. The config file is never written. The server
refuses to start when `catalog.json` cannot be parsed or holds an invalid
agent, rather than overwrite it. The form checks the command against the
server's `PATH`, and an agent whose program is missing there can still be
saved, for use with `conductor host`; a command given as a relative path
(`./agent.sh`) is not looked up there, and the form says it is resolved at
launch in the session's directory.

**Installed agents.** The Agents page says which agents are installed on the
server (the program of their command resolves there, checked on every visit
and kept for 30 seconds; a check that takes more than two seconds, as on a
`PATH` that reaches a stalled network mount, shows the agent as installed
meanwhile) and links to the website of an agent that has one: all built-ins
but Shell name theirs, and the form takes one (`site`, an `https://`
address) for an agent you add. Any program of that name on the server's `PATH`
counts, so the Go migration tool `goose` makes the Goose agent show as
installed, and a command given as a relative path (`./agent.sh`) counts as
installed, because it resolves in the session's directory. The Launch dialog's
**Server** tab offers only the installed ones (**My machine** offers them all:
what is installed there is your machine's business), the crew editor marks the
others, and a crew whose member's agent is not installed is refused at launch.

Every agent, from the config file or the UI, is held to the same limits: the ID
matches `[a-z0-9-]{1,32}`, the name is at most 60 characters, the description
200, `command` has at most 32 elements of at most 4096 bytes each, `env` has at
most 32 keys, `envPassthrough` (names of server environment variables the agent
may inherit) at most 32 names, an `env` key or `envPassthrough` name is at most
128 bytes and an `env` value at most 16384 bytes, `cwd` is at most 4096 bytes
without NUL, `icon` matches `[a-z0-9][a-z0-9:-]{0,63}`, `site` is an `https://`
URL with a host name and a valid port, no user info or white space, of at most
200 bytes, and a signal `pattern` (a regular expression) at most 200 bytes that
does not match an empty line. An `adapter`, if an agent names one, matches
`[a-z0-9-]{1,32}` and must be one Conductor has, in the config file (or the
catalog file) as on the Agents page: the server refuses to start with an
unknown one. A `yolo` recipe has at most 16 `args` of 1 to 4096 bytes without
NUL and at most 16 `env` variables, each named like an `envPassthrough` name
but never `CONDUCTOR_*`, with a value of at most 4096 bytes; its values are
shown as they are, not masked: they are switches, not secrets. A `trustPrompt`
(the words of the agent's workspace-trust question, which holds a crew prompt)
is held to the rules of a signal pattern. A `session` recipe has at most 16
`startArgs` and 16 `resumeArgs` of 1 to 4096 bytes, `{id}` a whole argument and
once in each list that has any, `newId` `uuid` or `name`, `idFrom` `hook` or
nothing, `idPolicy` `latest` or `lowest`, startArgs or `idFrom` to say how the
id is known, and an `idPattern` anchored with `^` and `$`, at most 200 bytes,
that matches neither an empty id nor one that begins with a dash.

## Security model and limits

- The workbench token gates launching, listing, stopping, link management and
  editing the agent catalog of its own server, and is control on every
  session there; share tokens grant their role on one session; host tokens
  only allow registering hosted sessions. A switchyard that admits open
  hosts registers a host with no token at all, limited per address (sessions
  held, registrations a minute, relayed bytes). Tokens are compared in
  constant time and stored hashed. A crew run's link grants its role on every
  member session of that run, members added to the run later included, and
  on no other session.
- A switchyard is trusted with the sessions published to it. It mints and
  checks their share links and tells the publishing machine each viewer's
  role, which the machine accepts: a switchyard could let anyone in, with
  either role. It receives each session's name, agent and folder's last
  element, the machine's name, and the session's status and attention state;
  a Conductor publishing there sends it no command line, OS user or branch,
  and no chat, activity, attention words or prompts Conductor types for
  crews (`conductor host` treats its server as your own and sends it all).
  It carries the WebRTC signalling, so it could place itself between both
  ends of a direct connection, and it reads whatever it relays: the
  terminal, files and chat of a viewer relayed through it. Its operator's
  workbench token lists the sessions published there and can end a
  publication (the session runs on at home), and has no role on them: it
  views, controls and shares none. Publish only to a switchyard whose
  operator you trust: `rendezvous.enabled: false` (Settings → Switchyard in
  the desktop app) keeps sessions off it, and `conductor switchyard` on a
  machine of your own keeps that trust in-house.
- Editing the catalog is as powerful as the server user. An admin can add or
  replace any agent, built-in and configured ones included, with any argv and
  env, and it runs as the user running `conductor serve`; `disableDefaults` or
  a curated `catalog` does not limit what the **Agents** page can add. Env
  values saved from the UI, secrets included, are stored in `catalog.json`
  (mode 0600) in the data directory. The file viewer of a server session never
  serves that directory, the config file or the catalog file, nor an editor or
  backup copy beside those two (a hosted session refuses the same files of the
  server on its machine, see the Files tab): any file there whose name contains theirs,
  ignoring case (`conductor.json.bak`, `conductor.json~`,
  `.conductor.json.swp`, `#conductor.json#`), but agents run as the same user
  and can read them.
  Changing a built-in or configured agent stores the form's command, cwd, icon,
  adapter and signal in full; only the env values left unchanged follow the
  original, so a secret rotated in the config file reaches the agent at the
  next start, while a value set on the Agents page is stored in `catalog.json`.
- Server sessions run with an allowlisted environment and a working directory
  under `allowedRoots`. Hosted sessions run as you, with your environment.
- Yolo launches agents without their permission prompts, as the user running
  `conductor serve`, and Codex's recipe turns its sandbox off too: turn it on
  only on a server whose allowed roots you would let an agent change unasked.
  Recipes are catalog data, held to the catalog's limits, passed as arguments
  and through the filtered environment. An agent session id that Resume passes
  to an agent must match its agent's `idPattern` and begin with a letter or a
  digit, so it can never read as a flag, and goes in as one argument.
- Terminal output is not persisted. Sessions and links live in memory and are
  lost on restart; hosted sessions reconnect and resume while the server is up.
  A switchyard is the exception for links: it keeps the ones it mints for
  hosts that register under an instance (every Conductor and `conductor host`
  does) in `links/` of its data directory, the token's hash and never the
  token, so a restart loses none; until the host registers again (under the
  same session id) a join answers that the machine is not connected. A host
  that has gone finds its links when it comes back within seven days (a
  connected host keeps them however long it stays), unless, for an open
  host, another host of its address needed their places once it had been
  gone five minutes.
- WebRTC needs UDP between the browser and the host; otherwise the relay is
  used automatically. No TURN credential minting yet.
- Share links outside a trusted network over TLS only: the `tls` listener
  with a certificate from Let's Encrypt (or your own), or a reverse proxy
  that terminates TLS. Plain http is never mapped on the router or
  advertised on the public address.

## Development

```bash
make run        # Go API on :8080 with --dev (CORS and origins for localhost:3000)
make web-dev    # Nuxt dev server on :3000 proxying /api and /ws to :8080
make test       # go test -race ./...
make lint       # gofmt + go vet
npm --prefix web run typecheck && npm --prefix web test
make test-e2e   # builds bin/conductor, then the Playwright suite (web/e2e) against a stub agent
```
The end-to-end suite starts its own server with its own home, data directory
and port, and a catalog whose `claude` and `codex` are a stub script; its
live test, which drives the real `claude` and `codex` with a tiny prompt,
runs only with `CONDUCTOR_E2E_LIVE=1` and both on the `PATH`.


If the Nuxt dev proxy does not upgrade WebSockets in your setup, run the dev
server with `NUXT_PUBLIC_API_BASE=http://localhost:8080`.

The wire protocol is documented in [docs/protocol.md](docs/protocol.md), the
architecture in [docs/architecture.md](docs/architecture.md), and the brand in
[docs/design/brand.md](docs/design/brand.md). A `Dockerfile` builds a server
image without agent CLIs; install them in a derived image or use `conductor host`.
The image keeps its data directory on the `/var/lib/conductor` volume. Give it
`CONDUCTOR_WORKBENCH_TOKEN` (`docker run -e`), or read the token it generates
from that volume: `docker exec <container> cat /var/lib/conductor/workbench-token`.

---

<a href="https://rocksolidlabs.io"><img src="web/public/sponsor/rocksolidlabs-logo.png" alt="RockSolid Labs" height="20"></a>

Sponsored and maintained by [RockSolid Labs](https://rocksolidlabs.io).
Open source under the [Apache License 2.0](LICENSE); see [NOTICE](NOTICE)
for the marks. © 2026 the Conductor Authors and RockSolid Labs, Inc. The
software it includes, each under its own licence, is listed with those
licences in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES): every server serves
it at `/third-party-notices.txt` (Settings → The app links it), and the
desktop packages and the image carry it.
