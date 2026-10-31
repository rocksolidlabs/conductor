# Conductor wire protocol

Source of truth for `internal/proto` (Go) and `web/app/utils/protocol.ts`
(TypeScript). Change all three together.

## Framing

Every terminal-stream message is a binary frame. Byte 0 is the type, the rest is
the payload. Frames travel unchanged over WebSockets, WebRTC data channels and the
relay envelope, so a client handles them identically regardless of transport.

| Type | Name | Direction | Payload | Limit |
|---|---|---|---|---|
| `0x01` | OUTPUT | owner → client | raw PTY bytes | 16 KiB |
| `0x02` | INPUT | client → owner | raw keystrokes; rejected for `view` | 32 KiB |
| `0x03` | CONTROL | both | UTF-8 JSON with a `t` discriminator | 8 KiB |
| `0x04` | SCROLLBACK | owner → client | replay bytes, only between `welcome` and `ready` | 16 KiB |
| `0x05` | SIGNAL | viewer ↔ server | JSON WebRTC signaling | 64 KiB |
| `0x06` | FILE | owner → client | `[8-byte reqId][uint32 headerLen][JSON header][bytes]` | 1 MiB body |
| `0x07` | CHUNK | data channel only | `[uint16 msgId][uint32 total][uint32 offset][data]` | 32 KiB |
| `0x08` | FILE_WRITE | client → owner | `[8-byte reqId][uint32 headerLen][JSON header][part]`: one part of a save (see File saves) | 32 KiB part, 4 KiB header |
| `0x10` | RELAY | host ↔ server | `[16-byte viewerId][inner frame 0x01–0x04, 0x06, 0x08]` | |

"Owner" is whichever process holds the PTY: the server for `server` sessions,
`conductor host` for `hosted` sessions.

CHUNK frames exist because browsers cap data channel messages (Chromium at
256 KiB). A host fragments any frame larger than 32 KiB; the browser reassembles
by `msgId` and then processes the inner frame.

## Control messages

Client → owner:

| `t` | Fields | Notes |
|---|---|---|
| `hello` | `proto:1, cols, rows, client, name?, chatOnly?` | Must be the first frame, within 5 s. `cols, rows`: a controller's two values in 1–500 set the session's size (see Resize policy); `0, 0` follows the session's size and changes nothing, as does any other pair out of range; a viewer's never changes it. The size never decides the role, which comes from the token. `name` is the display name other viewers see (≤ 160 bytes on the wire; cleaned to ≤ 40 runes, control characters stripped, empty → `guest`) |
| `resize` | `cols, rows, take?` | Controllers only; 1–500. One viewer sizes the session (round 14, an owner whose `welcome` says `sizer`): a controller's `resize` sizes it only while that controller is the sizer; from any other viewer it is kept as the size its window would give and refused without a word, except from one of the owner's own windows (a controller with no link) while nobody holds the size. `take` (Fit to my window) makes the controller the sizer at that size. The hello's size is decided the same way as the viewer attaches |
| `ping` | `ts` | Answered with `pong` |
| `file_get` | `reqId, path, stat?, op?, base?, rev?` | Answered with a FILE frame. `op` `status` asks for the working directory's git status against `base` (HEAD when empty): a header of kind `status` with `branch`, `base` (the short id, empty when it does not resolve), `changes[{path, status, added?, removed?, binary?}]` (at most 500, `truncated` then) and the totals `added`, `removed`; `op` `show` asks for `path` at revision `rev`, which names one commit, `path` read from its top (none, or one that starts with `-` or holds `:`, is `bad_request`; a tree or a range is an error): kind `show` with the content as the body (cut as a read's body is, `truncated` then). `op` `log` lists the commits on HEAD after `base`'s merge base (a crew member's run base) or, with no `base`, since the session started: kind `log` with `branch`, `base` (the short id), `since` (RFC 3339, when no base) and `commits[{sha, short, subject, author, at, parent, parents}]` newest first (at most 200, `truncated` then); `op` `commit` is commit `rev`'s files against its first parent: kind `commit` with `rev` (the full id), `commit` (the same fields and `body`, at most 4 KiB), `changes[{path, from?, status A M D R, added, removed, binary}]` (at most 500; `from` a rename's old path) and the totals. `op` `touched` (round 13) answers from the session's own index of the files its agent touched, kept beside the activity log from its `file` entries (up to 2,000 files, the least recently touched dropped first): kind `touched`, `path` the working directory, `touched[{path, op, ops, count, tool?, by?, first, last}]` one per file (`path` absolute, a relative event path joined to the working directory; `op` the latest, `ops` every op seen in the order first seen, `count` the touches, a repeat the activity log coalesces not counted; `tool` and `by` the latest touch's; `first` and `last` RFC 3339), the most recently touched first, the deny list applied, cut to the header's size with `truncated`. `op` `find` takes the Explorer's filter words as `path` (at most 200 bytes) and walks the working directory for files whose name holds them, in any case: kind `find` with `path` the directory and `matches` the paths from it (at most 200; `truncated` when the walk stopped at 20,000 entries read in all, 3 s or the header's size), `.git` and `node_modules` left out and the deny list applied. `op` `plans` (#289) lists the agent's plans outside the working directory (File reads, the plans directory; `path` ignored): kind `plans` with `path` the plans directory, `exists` once it is open (the agent reported a plan) and `entries[{name, dir, size}]` the session's plans that are files there now, `name` the path from the directory, in order; none for an agent without a plans directory. All go through the session's file policy and deny list as a read does; outside a repository the kind is `error` with code `not_repo`. The status (and the look after a tool call that files seen by git take) lists the folder's own working tree only: the folder, one inside it, or the one git finds above it by its `.git`; one a repository's `core.worktree` puts anywhere else is `denied`, and an untracked link's lines are not counted. A view link's `show` of git's settings or credentials a revision keeps is `file_denied`, as a read of them is |
| `nvim_open` | `reqId, path` | The editor's Neovim (design round 12, F8): start the real `nvim` on the session's machine for `path` (resolved like a read, under the deny list), for one of the owner's own windows on a session whose `fileEdit` allows it (`nvim` in the welcome; the owner's config and plugins load, so a link's or an invite's connection is not offered it and edits with the editor's own keys and `file_write`). The child does not inherit Conductor's own variables. Answered by an `nvim_event` of kind `opened` carrying `reqId`, the editor's `id` and the resolved `path`, then the whole buffer as `lines` events; or kind `error` with `reqId` and the code (`nvim_unavailable`: no `nvim` there, or Neovim is not offered this connection; `file_denied`, also for a file in a repository's `.git`, which Neovim would write; `too_many_requests`: 4 editors per connection, 8 per session; `revoked` or `expired` for a link's controller whose link no longer opens the session, asked after the role and before anything else). `nvim_input`, `nvim_close` and `nvim_swap` from a connection not offered Neovim are refused the same way. The switchyard drops all four from view-role connections |
| `nvim_input` | `id, keys, seq?` | Keys for editor `id` in Neovim notation (`dd`, `<Esc>`, `<C-x>`, `<lt>` for a literal `<`), at most 256 bytes, bounded per connection at 100 messages a second with a burst of 400 (one message per key; the chat's 10 and 20, which these keys shared before, dropped every key past the 20th of a quick burst). `seq` (round 13, the local echo) is the page's number for these keys, rising: once Neovim has handled them, and it is not waiting for a command's next key, the `cursor` event that follows carries it as `ack`, after every `lines` event those keys caused (the acknowledgement rides Neovim's own channel, through `rpcnotify`, in order with the buffer's changes). An acknowledgement can run early when Neovim answers requests in the middle of a key (`:sleep`, `vim.wait` in a plugin), and it covers later keys when those were handled too: a page settles its guesses by what the line holds, and takes keys for done only at `settled`. Keys Neovim's input buffer has no room for are given again once it has some, for about a second; keys refused (that bound, the rate, a host's queue, an editor no longer there) are answered by an `nvim_event` of kind `error` with the editor's `id` and the keys' `seq` as `ack` (and, when Neovim took the first of them, the end it did not as `text`), so the page can say which never reached Neovim. Keys that meet the end of the editor's Neovim (it died with them on their way, or as they came) are not refused: its `closed` is their word, and the page keeps every key a lost Neovim never said it had done over the text it last held, and names them; a refusal, sent beside that end, could come before it, and the page took the key off as if Neovim lived (#429) |
| `nvim_close` | `id, discard?` | Ends editor `id`. With `discard` it ends as `:qa!` does: the changes not written are dropped and its swap file goes (the editor sends it once the person said Don't save, or for a tab closed with nothing to keep). Without it, and when the connection ends (which ends every editor it opened), Neovim ends as on a lost connection: the swap file of a buffer with changes not written stays, and the next open of the file offers to recover it. An older owner reads `discard` as a plain close. Neovim's own `:q` is let finish its exit, so it leaves no swap file |
| `nvim_swap` | `id, choice` | Answers the swap file editor `id` found (round 13): `choice` is `edit` (leave read-only and edit), `recover` (read the swap file's text into the buffer, which is sent whole; the buffer is modified until written) or `delete` (remove that swap file, only a regular file named `*.sw?` that Neovim reported, and edit); `recover` and `delete` only once the process that wrote it is gone, else the editor's `message` of kind `emsg` says no; fixed choices mapped to fixed commands, never a command from the page. The switchyard drops it from view-role connections |
| `chat` | `nonce?, scope?, text, on?, to?, quote?` | Every role, once the owner's `welcome` said `chat` (an older owner closes on an unknown message). `text` is kept cleaned (line breaks as `\n`, other control characters but tabs dropped, trimmed) and must be 1–2048 bytes after that (0 with a `quote`); `quote` `{path, from, to, lines[], cut?}` is lines of a file the message is about (design round 12, F7): the path (at most 512 bytes), the 1-based range, at most 12 lines of at most 200 bytes, control characters but tabs dropped, its last lines left out (`cut`) when the message would pass 8 KiB, dropped without a path or range; a message with a quote sent to the agent types `path:from-to`, the lines as `> ` lines, then the text (at most 4096 bytes); `scope` is `session` when absent, or `run` for the chat of the run the session is a member of (`welcome.runChat`; `bad_frame` otherwise); `nonce` (≤ 32 bytes) is the client's own id of the post, echoed in the message; `to`, a controller's, also types the text as `chat_send` does: `"agent"` into this session's agent, a member's name into that member in scope `run`; `on` (scope `run`) names the member the sender looks at, kept when it is one. 10 a second with a burst of 20 per connection, beyond which `error{too_many_requests, requestId: nonce}` and the connection stays. Refusals name the nonce: `bad_frame` (empty, too long, a scope or `to` not here), `read_only` (`to` from a view link), `session_ended`, `revoked` or `expired` (the viewer's link no longer opens the session: nothing kept or sent) |
| `chat_send` | `ref, scope?, to?` | Controllers only (`read_only`, also on the relay). Types the text of the kept message `ref` into the agent as a `submit` would, then keeps and sends a `sent_to_agent` marker. In scope `run`, into the member `to` of the run as a broadcast does (nothing while that member waits on a prompt), with the marker in the run's chat, or `error{not_sent, message: needs_input|not_running|unknown|no_enter, requestId: ref}`. `bad_frame` with `requestId: ref` for a message not kept or not a message, or a run send naming no member; `revoked` or `expired` with `requestId: ref` once the viewer's link no longer opens the session (nothing typed), as it is taken and as its text is typed |
| `submit` | `text` | Controllers only; `text` at most 4096 bytes (`bad_frame` beyond, `read_only` from a view link, also on the relay). The owner submits it as a line: line breaks and tabs become spaces and other control characters are dropped, the text is written as a bracketed paste while the program has that mode on (`ESC[?2004h`), then a carriage return is written on its own 250 ms later, so that a TUI takes it as Enter rather than as part of a paste. The Enter answers the prompt that was showing when the text was typed; a prompt raised during the pause is not answered and the Enter is left out. The submission goes on if the client disconnects meanwhile (or its time runs out once its text is typed: see below), but not once its link is revoked or expires (its run forgotten, its link gone) before its carriage return is written: before its text is typed, nothing of it is written; once its text is typed, its carriage return is not written either, the text stays in the program's input (it cannot be taken back out), and the session's turn stays held, so that no other submission (a reply, a crew's prompt, a handoff) is typed after it and entered with it, until a person types into the terminal (INPUT from a controller, a terminal's own reports and mouse reports aside, any number of them in a write, mixed, or one a write begins and the next finishes, at any byte: the pointer, moved or clicked, types nothing; a lone Escape counts with the key after it), who deals with the text, or the session ends. Meanwhile the session is `needs_input` with `source:"submit"` and the words `Text from a link that was let go was typed and not entered: type in the terminal to continue`, which that input clears. While the turn is held (or becomes held as it waits), a server connection's `submit`, `chat` to the agent or `chat_send` waits for it apart from the connection's other frames, so that its own INPUT can end the hold; one at a time, another meanwhile refused with `error{too_many_requests}` (its `requestId` as for a host's) and the connection stays; a connection that closes keeps its viewer until it is done, as one whose frames waited for it does. At most 64 submissions wait for a session's turn together, every connection's and Conductor's own: one more is refused at once (`too_many_requests`). A host is told when a viewer's link goes (`viewer_leave` with a reason, `link_gone`), also once the viewer has left, and does the same. On a host, one that still waits for the keys the viewer sent before it is dropped as the viewer goes. The reply boxes use it to an owner whose `welcome` lacks `answer`; keys typed into the terminal stay INPUT. With `reqId` (#322; at most 32 bytes of printable ASCII, as `answer`'s) the owner answers it, to a client whose `welcome` said `submitAck`: `submit_ok{requestId}` once the line was entered; else `error{code, requestId}`: `not_sent` with the question's words when `unlessWaiting: true` held it back (the session was asking, looked at as the owner holds the session's turn, so no prompt slips between the look and the write: the broadcast's guard, what the palette's typed line asks for; nothing typed), `input_failed` when the line was typed and not entered (the turn held, as above), `too_many_requests`, `session_ended`, `read_only`, `bad_frame` (past its bounds; no `requestId` when the id itself is). Without `reqId` nothing is answered, as before, and `unlessWaiting` still holds |
| `answer` | `reqId?, question, input?, text?` | A reply to the question the person was shown (see Attention, Answering a question), sent only to an owner whose `welcome` says `answer` (an older one closes on an unknown message). Controllers only (`read_only`, also on the relay). `question` is that question's `id` (1–32 bytes of printable ASCII, no space); exactly one of `input` (a choice's keys, at most 48 bytes, written as INPUT is) and `text` (a line, at most 4096 bytes, submitted as `submit` is); `reqId` at most 32 bytes of the same. Out of those bounds: `error{bad_frame, requestId}` (no `requestId` when it is itself out of bounds), the connection stays. The owner writes nothing unless the question is still the one shown, nobody is answering it and nobody answered it in the terminal, looked at as the reply arrives and again, together with claiming the question, as the keys (a line's text, its Enter for a line with none) are written: first to claim wins; otherwise `error{question_answered, requestId}`, whose `message` says what became of the question when the owner knows (`this question was already answered by <name>: nothing was sent` once a result is recorded, `this question is being answered: nothing was sent` while another reply's keys are on their way, `this question was answered in the terminal: nothing was sent` once keys typed in the terminal reached it, else `this question was already answered: nothing was sent`). `answer_ok{requestId}` is the final word: the keys landed, or the line was entered, and the question is resolved by this reply (or was withdrawn by the agent moving on while they were written); it is never sent while the outcome is open (a line the program takes slowly is told once it is entered). A line typed whose Enter was not written (a question came in its pause, its link went) is `error{input_failed}` with `the line was typed and not entered: type in the terminal to continue`: the session's turn is held. Any other refusal is an `error` naming it as INPUT's or `submit`'s would be (`not_sent`, `input_failed`, `too_many_requests`, `session_ended`). An `answer`, a choice's keys too, is withdrawn as a `submit` is once its link's credential no longer stands (revoked or expired; on a host, no ping of its own sent within 15 s answered by the server: see the host control connection), whether or not its connection has closed for it yet: nothing of it written, `input_failed`. A `text` waits for the session's turn as `submit` does, also while it is held for a person, on a server connection apart from its other frames as `submit` does; an `input` answers a `trust` or `question` prompt showing over a held turn without ending the hold, the hold's words shown again once it is answered; an `answer` naming the hold itself (`source:"submit"`) is refused (`question_answered`), nothing written. Every quick reply uses it: the sidebar's row and its 1–9, the Yard's card and queue, the session page's and a link's reply bar and its digits, a question's choices in the chat |

Owner → client:

| `t` | Fields |
|---|---|
| `welcome` | `proto, sessionId, role, subscriberId, cols, rows, status, scrollbackBytes, transport, fileView, chat, runChat?, cwd?` (+ `viewerId, iceServers, relayTimeoutMs` on the signaling welcome); `chat: true` says the owner takes `chat` and `chat_send`; `runChat: true` that the session is a run's member with a run chat (scope `run`, `chat_roster`); `answer: true` that the owner takes `answer` and refuses one whose question no longer waits (absent from an older owner, which a client replies to with INPUT and `submit`); `submitAck: true` that the owner answers a `submit` naming a `reqId` with `submit_ok` or an `error` naming it, and takes `unlessWaiting` (#322; absent from an older owner, which types the line and says nothing); `sizer: true` that the owner sizes the session by one viewer, and `sizedBy` which (its subscriber id; absent, nobody holds the size): a view that is not it shows the session's grid scaled to its pane (round 14); `fileEdit: true` that this connection may save a file; `nvim: true` that it may also use the editor's Neovim (one of the owner's own windows, the machine has `nvim`); `nvimOwnerOnly: true` that it may edit but not through Neovim, which is kept for the owner's own windows (a link's or an invite's controller edits with the editor's own keys); `nvimSettled: true`, with `nvim`, that the owner's Neovim says when keys are done (`nvim_event` `settled`; an older owner sends neither, and a page takes a cursor's `ack` for that word, as it did); `cwd` is the session's working directory on the machine that runs it, to a viewer whose role may read files (at most 4096 bytes as encoded in the welcome, escapes included, so the welcome always fits one CONTROL frame; absent past that, to a role that may read none, and from an older owner), which roots a guest's Files pane: a switchyard lists a published session with its folder's last element alone |
| `ready` | end of scrollback; live output follows |
| `resize` | `cols, rows, by` (the viewer that sizes the session now; absent when nobody holds the size, as when it left with none of the owner's windows to take it). Sent on every change of size or of sizer, the same size included. A viewer that moves to the relay keeps the size it held: its hello there takes it back, at the hello's size, unless a `take` moved it meanwhile. An owner without `sizer` (an older host) names the subscriber that resized |
| `status` | `status, exitCode?` |
| `attention` | `state, message?, source?, kind?, options?[{label, input}], id?, answered?` (see Attention; `id` names the question, absent with the empty state and from an older owner; `answered:"terminal"` marks a question keys typed in the terminal have answered) |
| `answer_ok` | `requestId` — the owner took the `answer` of that `reqId`: its keys are written, or its line accepted |
| `submit_ok` | `requestId` — the owner entered the line of the `submit` of that `reqId` (#322): its text typed and its Enter written; never sent while the outcome is open (a line the program takes slowly is told once it is entered) |
| `activity` | `at, type, by?, byName?, message?, url?, to?, tool?, op?` — one activity-log entry: `attention`, `input`, `join`, `leave`, `link`, `status` or one of the seven event types (see Events). An `input` entry carries `op` `typed` when the line was typed by Conductor (a crew's prompt, a broadcast) rather than a reply to a question. The last 50 entries replay right after `ready`; new ones follow live. |
| `viewers` | `count, list[{id, name, role, link?, since, lastInputAt?}]` — the full roster, sent on every join and leave and at most every 2 s per viewer while they type, a quiet connection (`hello.chatOnly`, a run chat's) left out. `link` is the share link's label. |
| `error` | `code, message, requestId?` — `requestId` names the chat post (its nonce), send (its ref), answer or submit (its `reqId`) the error answers |
| `pong` | `ts` |
| `chat` | `id, at, scope, kind, by{id, name, role}, text?, ref?, to?, on?, event?, nonce?, options?, quote?` — one message of the chat (see Chat), live: `kind` `message` with `text` and, for a comment on lines, `quote`; `system` with `event` `join` or `leave` about `by`, `answered` with `ref` the question `by` answered and `text` the label of the choice whose keys the reply typed, when it was one (absent for a digit that is no choice's keys, for keys that say nothing readable, an Enter alone, and always for a line typed: the agent may have asked for a credential, the terminal may not echo it, and the chat goes to every viewer, so a typed line is shown to its own person by their browser alone; #364), or `withdrawn` with `ref` a question that left without an answer through Conductor and `text` why: `terminal` (keys typed in the terminal answered it) or `agent` (the agent moved on), so that its card offers its choices no longer (#358); `sent_to_agent` with `ref` (the message typed) and `to` (a run's member, else the session's agent); `question` from the agent (`by` `{id: "agent", name: the agent id, role: "agent"}`) as the session goes `needs_input`, its `text` the question and `options[{label, input}]` its choices (at most 6), `on` the member in a run's chat. `by` is otherwise the sender's subscription as the roster lists it; `nonce` the sender's own id, echoed |
| `chat_history` | `scope, messages[], more?` — the kept chat replayed to a new viewer right after the activity replay, oldest first, in frames of at most 8 KiB; `more` on every frame but the last; the session's series, then the run's (scope `run`) when the session is a member |
| `nvim_event` | `id, kind, …` | What editor `id` reports. `kind` says the fields: `opened` (`reqId` on the first, `path`), `lines` (the buffer lines `[first, last)` become `lines`, `last` -1 meaning to the end, each event under the control bound, `truncated` when a line past 4 KiB was cut), `cursor` (`line`, `col`, 1-based, `mode`, `visualLine`/`visualCol` the other end of a visual selection), `mode`, `cmdline` (`show`, `content`, `pos`, `prompt`), `message` (`text`, `messageKind`), `written` (`path`; the write is also a `file` event `write` by the person with tool `nvim`), `closed` (`reason`), `error` (`reqId`, `code`, `message`), `swap` (`swap` `{file, pid?, running, user?, host?, modified?, mtime?}`: another editor's swap file for the file opened, which Neovim then opened read-only instead of asking; `running` when its writer still runs on the session's machine, a zombie not counted; `modified` when it holds changes not written; round 13), `modified` (`modified`: the buffer began to hold changes not written, or no longer does: written, undone back, or another file shown; the field is absent when false), `settled` (`ack`: Neovim has done the keys numbered up to it, nothing left pending: every change they made has been sent before it; a `cursor`'s `ack` can come before those changes, since Neovim answers requests in the middle of a key, a plugin's `vim.wait`; the editor keeps a tab's Neovim while its keys are not settled). The editor keeps a tab's Neovim while another tab is in front as long as its buffer holds changes not written, which is why a connection may keep 4. Neovim's own questions (`:confirm q`, a write over a file changed outside) come as a `message` of kind `confirm` whose last line holds the choices (`[Y]es, (N)o, (C)ancel: `), which the editor shows as buttons sending the key; a substitute's question at each match (its `c` flag) is a `message` of kind `confirm_sub` (`replace with b (y/n/a/q/l/^E/^Y)?`), answered with typed keys. Neovim leaves a question shown once answered, so a `message` of kind `clear` comes before the next acknowledgement (`ack`) after one: Neovim acknowledges no keys while it asks. The `lines` are the buffer's alone: Neovim's live preview of a substitute is off ('inccommand' is emptied after the person's config loads), and a preview turned on again later (Neovim reports it with no changedtick, and undoes it without a word) is never sent. The bridge passes over a notification of a shape it does not expect rather than lose Neovim over it; a Neovim that dies is told as soon as the bridge's reading of it ends (or at the next key, when its output is held open), `closed` with a `reason` beginning `error:`, and the changes it sent before may still follow that `closed` (the client delivers them on a goroutine of its own); its own quit is told before its output ends; an end before `opened` is told right after it; nothing follows an `nvim_close`. `closed` with `reason` `quit` is the person's own quit (`:q`), which closes the tab; any other is Neovim lost: the tab stays with the text Neovim last held, its later `lines` applied, read only, and Reopen starts Neovim again, which offers the lost one's swap file. A tab not in front follows its Neovim's `lines` too. A buffer Neovim reloads (`:e!`) is followed again and sent whole |
| `chat_roster` | `scope: "run", count, list[{id, name, role, on?}]` — who is on the run's chat: every connection to any member, one row per name, `on` the member a person looks at (a connection that is not quiet); sent to every member's viewers whenever any member's roster changes; `count` counts everyone, `list` the first 32 |

Error codes: `read_only`, `slow_consumer`, `bad_frame`, `hello_timeout`,
`revoked`, `expired` (the link reached its expiry; both also refuse what a
link's viewer sends once its link no longer opens the session, see below),
`session_ended`,
`host_disconnected`, `file_denied`, `too_many_requests`, `not_sent` (a run
chat's `chat_send` whose member could not take the text, the message saying
why; or a `submit`, a `chat` to the agent or a `chat_send` while the agent's
trust question shows, the message saying so: its Enter would pick whatever
the dialog highlights, and the question's own choices answer it),
`question_answered` (an `answer` whose question takes no reply: another
reply answered it first or is answering it, keys typed in the terminal
reached it, or it went; nothing was written; the message says which, see
`answer`).

WebSocket close codes: `1000` normal, `1001` server shutdown, `4400` protocol
error, `4401` unauthorized, `4403` link revoked or expired, `4404` unknown
session, `4409` too many viewers, `4410` session or host gone. The workbench
token is control on every session of its own server and has no role on a
switchyard's (`4401`): a session published there is opened by the links its
publishing machine had minted.

A client that a session's owner (the server for its own sessions, a host
for its session) closes, revoked, expired or a slow consumer, is sent
nothing more that was queued for it: the frame being written as it is
closed is the last, then its `error` frame and the close. One closed at the
session's end (the viewers attached through its links, which end with it,
on a server session and a hosted one alike, a session published to a
switchyard included), or a hosted session's viewer as its host goes, is
first sent what was queued for it before: the session's last output and its
`status`, then its `error`
(`session_ended`, or `host_disconnected`) and `4410`, whatever link it came
through; a revoke or the expiry of its link that lands meanwhile (its host
gone or not) stops that, at the frame being written, as does the session
leaving the server (removed, or past its retention), which closes every
viewer as its end (`4410`) and writes none anything more; a link revoked
before the end is not the end's to close.
A server gives a connection it closes, for any of these reasons, 5 s from
the close to be written all of it, and then cuts its socket, a close
handshake under way included: a viewer that reads too little, or does not
answer the close, is let go then, with what it had not taken. What it sends
from then on is not acted on, and until its connection has closed it still
counts toward the session's viewer limit (`4409`). Each client's connection
is written and closed apart from the session and the other clients, so one
that stops reading holds up nobody else. On a server session a revoke (and
the session's end, of its links) answers once the connections of the link's
viewers have closed.

A share link is checked when its token is presented and again as the viewer
attaches (on a server session, after its `hello`), under the lock a revoke
takes to close the link's viewers: a link revoked, expired or gone (its run
forgotten, a switchyard run no longer naming the session) in between closes
the connection with an `error` (`revoked`, or `expired`) and `4403`, before
any `welcome`. A viewer attached through a link that expires is closed as it
expires, the same way with `expired`; every revoke of a link, one revoked
before included, closes whoever is attached through it.

What a viewer attached through a link sends is held to its link again as it
is acted on, by whoever runs the session (the server for its own sessions, a
host for its session, which also needs to vouch for its server: a ping of
its own, sent within 15 s, answered; see the host control connection; the
viewer attaches on its link alone), whether or not the connection has been
closed for it yet. While the link no longer opens the session, its INPUT is refused
with `error{revoked}` (or `expired`), asked as the keys are queued for the
program and again as the session's writer takes them, so keys that waited
behind another write are never written; a FILE_WRITE part is refused with a
FILE error of the same code and the save under way is dropped, and a save
whose last part was taken is asked once more just before its rename, a
refusal then removing its temporary file and leaving the file as it was (see
File saves); a `file_get` is answered with a FILE error of that code, nothing
read; an `nvim_open` with an `nvim_event` error of that code; a `chat` or a
`chat_send` with `error{revoked, requestId}` (or `expired`), nothing kept or
typed. The role is checked first (`read_only`, and `nvim_unavailable` for a
view link's `nvim_open`). Each refusal carries words a page shows as they
come: `this link no longer opens the session`, `this link has expired`, or on
a host the words saying the server has not been heard from. The one
exception is a write the program had begun to take when the link went (one
INPUT frame, or a submission's text or its carriage return): while the
program does not read, it lands when the program reads, as a submission's
text does (see `submit`); nothing queued after it is begun. A viewer with no
link (one of the owner's windows, a paste invite's viewer, a host's own
terminal) has no credential and is never refused so. A `submit` or an
`answer` withdrawn so is answered as it was (`input_failed`).

## Resize policy

The most recent resize from any `control` client wins and is broadcast to every
attached client, which resizes its terminal to match. `view` clients never
resize. Attaching as a controller applies the client's size immediately, unless its
`hello` says `0, 0`: a viewer that shows the session scaled at the session's
own size (the view-only run tiles, a quick reply) follows the size and never
resets it. A terminal's automatic reports (a cursor position report, device
attributes, focus in and out, colour replies, the kitty keyboard protocol's
flags; at most 256 bytes, nothing else
in the frame) are written to the process like any INPUT but do not answer the
prompt the session waits on. When a session's process ends, its attention is
cleared in the same change that sets `exited` or `stopped`.

## Chat

The people on a session talk to each other beside the terminal, over the
connection the terminal takes, so the chat exists wherever a share link
works: on a server session, on a `conductor host` session (the host keeps
it), and on a session published through a switchyard (the publishing
machine sends the switchyard none of it; a viewer relayed through the
switchyard has it in relayed frames, which pass through the switchyard and
can be read there). Every viewer may post, whatever its role;
a controller may also have a message typed into the agent, which leaves a
`sent_to_agent` marker in the chat. The owner keeps the last 200 messages
of a session, join and leave lines and markers among them (a person on two
tabs joins and leaves once), replays them to a new viewer as
`chat_history` (the newest that fit 128 KiB), and sends each new one to
every viewer as `chat`. A message's text is at most 2048 bytes after
cleaning; chat frames are encoded without HTML escaping, so a message of
the bound always fits a control frame. Each connection may post 10 a second
with a burst of 20. A session's chat ends with the session: an ended
session takes no post. Chat lines are not activity entries and reach no
hook or webhook; what is unread is the browser's own count, kept per
browser (`conductor.chat.unread`), fed by the thread it has open and by
the `chat` events of the admin stream for the threads it has not.

The agents ask in the chat too: as a session goes `needs_input`, its
agent's question (the attention's message, or "Waiting for input", and its
choices) is kept and sent as a `question` message from the agent, and the
input that answers it (a choice typed, a line submitted, a key in the
terminal) is followed by a `system` line with `event` `answered` naming the
question and who answered; a run's member says the same in the run's chat,
on the member. The question's message has the question's own `id` (the
attention's `id`, in the run's chat too), so answering from the chat
takes the paths a page has, naming the question as every quick reply
does (`answer`): the choice's keys into the session, or into the member,
refused once the question no longer waits.

A crew run has a chat of its own, one thread across every member: the run
engine keeps it (the last 500 messages) and every member session is made
with it, so it is read and posted over any member's connection with scope
`run`, and exists wherever the members do, for a guest on a run link and
through a switchyard too. A member's new viewer is replayed the run's
chat after the session's. The people on any member make its roster
(`chat_roster`), one row per name with the member they look at; a
person's join and leave lines are counted once across the members. A run
page, which shows tiles and no terminal of its own, opens one quiet
connection (`hello.chatOnly`) to a live member for the chat: it gets no
scrollback or output, is not among that session's viewers and leaves no
line in its chat, and is on the run's roster. A controller's `chat_send`
with scope `run` types a kept message into the member it names as a
broadcast does, nothing while that member waits on a prompt
(`not_sent`), and marks the message with the member. The run's chat goes
into its record (`chat` on the run object of `GET /api/runs/{run}` and of
the record; lists carry none) and stays open while the run is kept; a
resumed run starts a new one.

## Hosted sessions: signaling and relay

For a hosted session the viewer's WebSocket first receives
`welcome{transport:"webrtc", viewerId, iceServers, relayTimeoutMs, relayOnly?}`.
A host started with `--relay-only` sets `relayOnly`, and the viewer requests the
relay immediately instead of attempting WebRTC. The viewer
creates the data channel and the SDP offer; the host answers. Candidates trickle
in both directions.

Viewer → server (SIGNAL): `offer{sdp}`, `ice{candidate}`, `relay{reason}`.
Server → viewer (SIGNAL): `answer{sdp}`, `ice{candidate}`, `relay_ok`, `error`.
No SIGNAL frame leaves the server past the 64 KiB the type holds: the
server encodes the host's answer or candidate anew, which can grow it (`<`
as six bytes), and one that would pass the limit is never sent; the viewer
gets a CONTROL `error{code:"bad_frame"}` saying what was not sent and why.
Its WebRTC attempt cannot succeed without the answer: where the server
relays, the viewer keeps its connection and goes to the relay as
`relayTimeoutMs` runs out; a switchyard that does not relay closes it with
that error instead, as the attempt has nowhere to go. The host's connection
stays either way: its message was within its own bound.

When the data channel opens the viewer sends `hello` over it and the host replies
with the normal welcome/scrollback/ready sequence (`transport:"webrtc"`).

If the channel has not opened after `relayTimeoutMs`, or ICE fails, the viewer
sends `relay`. A switchyard without a relay (`switchyard.relay: false`)
answers a CONTROL `error{code:"relay_off"}` instead and keeps the connection,
so the viewer stays on its WebRTC attempt; such a switchyard also refuses a
host that registers with `relayOnly` (close 1002, `relay_off`). With
`switchyard.relayKBps` set, a host's relayed output, and its messages with
it (each counted as read, or as the server forwards it to a viewer when
that is larger, an answer encoded anew, before the viewer is sent it), is
read no faster than that many
KiB a second: the host is slowed by
its own connection, and every frame still arrives whole and in order. The
other way, on any server, what a hosted session's viewers send (SIGNAL and
relayed frames) is read under one budget per address, `viewerRelayKBps`
(128 KiB a second by default), shared by every hosted viewer of the
address and kept after the last one leaves until it has refilled: a viewer
past it is slowed the same way, and each frame is charged whole, in the
order the frames come, as the larger of what the viewer sent and what goes
to the host for it (a relayed frame in its envelope, an offer or a
candidate as encoded for the host). Each of these bounds, and
`switchyard.openHostRelayKBps` (the open hosts of one address together),
allows a burst of twice its rate and never less than 4 MiB: what a
person's tabs ask for or send at once, three files of 1 MiB read or saved
among it, passes at once whatever the rate, where the browser gives up on
a file request after 20 s (and says a save is slow then); the rate bounds
what goes over time.
What waits for a host's connection is bounded in bytes
as well as in messages (8 MiB, 8192 messages), in two lanes, each in its
order. The server's own messages (`link_gone`, a `viewer_leave` that gives
a reason, `stop`, `attention`, `activity`, the answers to the host's
requests, `error`) have a lane of 1 MiB and 512 messages, which the server
writes first: one of them waits behind the one message being written when
it was queued, never behind the viewers'; one that finds its lane full
closes the connection, as a host too slow to keep. The viewers' messages
(`viewer_join`, `offer`, `ice`, `relay_start`, their relayed frames,
`viewer_behind`, a `viewer_leave` with no reason) fill the rest, at most
7 MiB, and then wait in their own read loops, in the order they came, for
the host to take some (a viewer whose connection fails meanwhile is let
go), which neither holds up the server's messages nor closes the
connection. A link's going overtakes what its viewers had queued, so
nothing of a viewer of a link that went (revoked, expired), still attached
or gone already, reaches the host after it: the server marks the link gone
and closes its viewers before it queues `link_gone`, and drops each message
of a viewer of that link still queued as it comes to write it, its
`viewer_join` among them; a viewer whose join was dropped so is never named
to the host, nor its leave (a leave follows a join that was written, and
only such a join). A viewer that left for any other reason, its link
standing, has what it sent before its leave written first, then its
`viewer_leave`, with no reason, in the viewers' lane. What a viewer sent
before its connection closed reaches
the host before its `viewer_leave`: once a write to the viewer or a ping
has failed, its connection is still read for at most 2 s, and a frame that
needs no wait is relayed; one that was waiting (the budget, room in the
host's queue) is dropped, and nothing the viewer sent after it goes. What waits for a viewer is bounded at 16 MiB beside its
2048 frames: past either the viewer is closed as too slow (1008,
`slow_consumer`). Once more than 512 KiB of what its host relays a
viewer waits for it on the server, the host is told the viewer is behind
(`viewer_behind{viewerId, behind:true}`), and once no more than 128 KiB
waits, that it caught up (`behind:false`): at most one of a viewer's waits
in the host's queue, the changes made meanwhile going as the latest once it
is taken; they go in the viewers' lane of the queue, never in the
server's own, and one that finds no room waits for some rather than close
the connection;
a hosted viewer's socket holds at most 128 KiB unsent (on Linux, which
bounds it), so what a viewer has not taken waits where the server counts
it, not in a buffer of megabytes. The host holds a viewer's frames while it is
behind, as it holds a WebRTC viewer's while its data channel is full, so
its connection, which its relayed viewers share, carries what each of them
takes; what waits for a viewer waits on the host, under its 1 MiB for a
viewer. Once the session has ended, what the host held for a viewer goes
whether it is behind or not, ahead of the end: the session's last output,
which the server writes the viewer as the end closes it. An older host ignores `viewer_behind`, and its viewers are bounded
by the 16 MiB alone. On the host's side, what it writes on its control
connection goes one message at a time: its own messages (its status,
attention and activity, the answers and candidates for its WebRTC viewers,
its link requests) ahead of the frames it relays, and those viewer by
viewer in turn, each viewer's in order; but the `status` that says the
session ended, and a `link_revoke` sent once it has, wait at most 2 s for
the viewers' last frames to go first, the WebRTC viewers' acknowledged by
their data channels: the server ends an ended session's links, and closes
the viewers attached through them as the session's end once they have what
was relayed to them, but a browser on WebRTC lets go of its data channel
as that connection closes. A `link_revoke` while the session runs goes at
once. A publishing server does not revoke its session's links at the
switchyard as the session ends: the switchyard ends them as it hears the
`status`, which a revocation sent then could overtake. A relayed frame that waits more than
30 s for its turn, its viewer's catching up included, is never sent: that viewer alone is let go
(`slow_consumer`, then `viewer_closed`), as a WebRTC viewer is whose data
channel stays full; one of the host's own messages that waits more than 15 s
is dropped, but for `viewer_closed` and `viewer_error` (a viewer's first),
which wait as long as the connection lasts and the viewer has not left, and
for an attention change, the latest of which is sent once the connection
takes it. However slowly the server reads the
connection (a relay bound pauses its reading), the host answers its pings
and keeps the connection; it gives the connection up only when its socket
takes nothing at all for 2 minutes, or when it would hold more than 4 MiB
unsent (a server that pings and does not read), or when it has pinged the
server for a link's viewer (see the host control connection) and heard
nothing at all for 2 minutes. After `relay_ok` the same WebSocket carries terminal frames: the
server wraps the viewer's frames in RELAY envelopes for the host and unwraps
the host's envelopes for the viewer. The welcome then reports `transport:"relay"`.
View-role INPUT, FILE_WRITE, `resize`, `submit`, `answer`, `chat_send` and the `nvim_*` messages are dropped by the server before they reach the host (`read_only`; the host refuses a view-role `answer` itself too, `error{read_only, requestId}`, for an older server that passes it); a `chat` post passes for every role, and the host refuses one that is also for the agent. A control connection's FILE_WRITE is carried, so the editor's saves work on the relay the same as a file read does (the editor's Neovim is the owner's own window only, so a link's controller edits with the editor's keys and saves).

The host types a viewer's `submit`, `chat` to the agent and `chat_send`
(over the data channel or the relay alike) one at a time, in the order the
viewer sent them, as the server does, with at most 8 waiting behind the one
being typed: one more is refused with `error{too_many_requests}` (`requestId`
the chat message's id or the send's ref, none for a `submit`) and the
connection stays. Each has 10 s from its arrival, its wait included: one
still waiting when its time runs out is refused then (`input_failed`), never
typed late. When the viewer goes (its connection or data channel closes, or
it moves to the relay and attaches again), what waits is dropped, and does
not count against the 8; the one being typed is finished, unless the
viewer's link goes (a `viewer_leave` with a reason, a `link_gone`, the host's
connection to the server lost, or no ping of the host's sent within 15 s
answered: see the host control connection), then or later: it is then
withdrawn as on
the server,
never typed if its text was not, and with no carriage return if it was, the
session's turn held for a person (see `submit`). The viewer's keys and
editor requests not yet begun are dropped as it goes, whatever the reason. The viewer's `nvim_open`s start apart, at
most 4 at once (the editors a connection may hold), past which the
`nvim_event` error is `too_many_requests`.

The host writes a viewer's INPUT off the loop that carries its frames (the
relay's carries every relayed viewer's), in the order the viewer sent it and
apart from its submissions: the keys sent before a `submit`, a `chat` to the
agent or a `chat_send` are written (or refused) before it, and those sent
after are not held back by its pause. Each INPUT frame has 10 s from its
arrival for the program to take it: one still waiting then is refused
(`error{input_failed}`) and never written. A link's viewer's INPUT is held to
its link and to the host vouching for its server (a ping of the host's,
sent within 15 s, answered) as the session's writer takes it: refused then
with `error{revoked}` (or `expired`), never written.
At most 256 frames and 128 KiB
wait behind the one being written, per viewer; one more is refused with
`error{too_many_requests}` (no `requestId`) and the connection stays. When
the viewer goes, the keys that wait are dropped. The editor's `nvim_input`,
`nvim_swap` and `nvim_close` likewise wait for Neovim off that loop, in the
order sent, at most 400 behind the one Neovim handles (past them the
`nvim_event` error is `too_many_requests`).

A viewer's FILE_WRITE parts are taken off that loop too, over the relay
and the data channel alike, so that a save's disk work (its checks, its
hash, its temporary file, its rename) never holds up the loop, which for
the relay also carries the server's messages: in the order sent, one at a
time, at most 64 parts and 2 MiB (two whole saves of the largest size)
waiting behind the one being taken, per viewer. One more is refused with a
FILE error of code `too_many_requests` naming its save (the words say to
save again once the others are done) and the connection stays. When the
viewer goes, the parts that wait are dropped, unanswered. A viewer's
`offer` (host control connection) is answered off the loop that reads the
control connection as well: the answer waits for its turn among the host's
own messages (15 s at most, then dropped: the viewer falls back to the relay
on its own timeout) while the loop goes on reading, and candidates that
come before the offer is taken wait for it (64 at most); another offer of
the same viewer while one is being answered is ignored.

The first line, key, save, chat post or `file_get` of a viewer who joined
through a link, after the host's start, a reconnect or a spell with no link
held, waits within its own time (a save's part and a read, 15 s) for the
answer to the ping its join sent (see the host control connection), off the
loop that carries its frames, and goes on once it comes: a `chat` then
waits on the typing queue, in its turn among the viewer's submissions, and
the viewer's chat posts after it follow it there until it is done, so they
keep their order. One that finds the host not vouching once the server has
failed to answer a ping since its last answer is refused at once.

A viewer's `answer` waits as what it carries does: its `input` among the
viewer's INPUT frames (its 10 s, the same bounds, `too_many_requests` naming
its `reqId`), its `text` among the submissions (the typing queue). Whatever
waited before it, of this viewer or another, its question is looked at as
the session's writer takes it: of two viewers' answers to one question the
first written is taken (`answer_ok`) and the other refused
(`question_answered`), nothing of it written.

## Host control connection

`GET /ws/host?token=…` (host token or workbench token; on a switchyard with
`switchyard.openHosts`, none at all: an open host, limited per address in how
often it registers, how many live sessions it holds and what it relays). A
refusal is an HTTP error before the upgrade, `{"error": {code, message}}`:
`429 rate_limited` (registrations), `429 open_host_limit` (live sessions),
`401 unauthorized`. A server publishing its sessions tries a 429 again after
15 s, 30 s, 1 min, 2 min and 2 min, or after the `Retry-After` it is given
when longer, and stops when the session ends. Text frames are JSON; binary
frames are RELAY envelopes.

Host → server: `register{proto, host{name,version,user?}, session{name,agentId,command,cwd,cols,rows,relayOnly?,agentToken?,branch?}, resume?{sessionId,secret}}`
(`user` ≤ 64 bytes is the OS user for "hosted by"; `cwd` ≤ 4096 bytes; `branch` ≤ 200 bytes is read from `.git/HEAD`;
a server publishing its sessions to a switchyard registers each with less: `command` is one word, the
agent's id or else the program's name, never an argument; `cwd` the folder's last element; no `user`
and no `branch`),
`status{sessionId,status,exitCode?}`, `resize{sessionId,cols,rows}`,
`answer{viewerId,sdp}`, `ice{viewerId,candidate}`, `viewer_error{viewerId,code,message}`,
`viewer_closed{viewerId}`, `attention{sessionId,state,message?,source,kind?,options?,id?,ask?}`
(`id` the id the host's session gave the change, which the server keeps,
only as 1–32 bytes of printable ASCII, so that an answer from its listings
names the question the host holds; an older server ignores it; a question
asked again with the same words is sent again, its `id` new; `ask: true`,
with `needs_input` only, says the question the host's session shows is the
agent's own (see Attention), which the server's copy then keeps through a
`done` it forwards; never sent to a switchyard),
`activity{sessionId,entry,state?}`,
`chat{sessionId,message}` (a chat message the host's session kept, as `chat` carries it to a viewer; the server takes the connection's session, keeps `message`, `system` and `sent_to_agent` kinds, scope `session`, bounded ids, cleaned names, the text cleaned and cut to 2048 bytes, `to` only as `agent`, and mirrors it into the events stream; a switchyard drops it),
`link{requestId, role, ttlSeconds?, label?}` (a share link to the session,
minted by the server: `requestId` ≤ 32 bytes, `label` ≤ 120 bytes,
`ttlSeconds` ≤ 86400, at most 5 requests a minute per connection; one
without a `requestId` is a protocol error), `link_revoke{requestId, linkId}`
(revoke a link the server minted for this session, or a run link its
instance minted: `linkId` ≤ 64 bytes; never counted against the link bucket;
a missing or oversize field is a protocol error),
`link_run{requestId, role, ttlSeconds?, label?, run: {id, name, members: [{name, sessionId?, agentId, status}], revision?}}`
(one link to the sessions of a crew run's members, each `sessionId` the
member's session on this server; the run id ≤ 64 bytes, its name ≤ 120, 1 to
32 members with unique names ≤ 40 bytes, an agent ≤ 64, a status of
`pending`, `starting`, `running` or `ended`, a `revision` from 0 to 2^53 − 1
(see below); it needs a host registered with
an instance, else `error{no_instance}`, and every session must be one this
instance registered, else `error{invalid_request}`; it shares the link bucket)
and `link_run_update{requestId, run}` (the run's members now: the run's links
follow them, and a viewer attached through one of them to a session the run
no longer names is closed with `4403`; at most 30 a minute per connection;
`error{not_found}` when the run has no link here).
`run.revision` orders the members a host sends of one run: each group it
sends, by `link_run` or `link_run_update`, has a higher revision than every
one before it. A server that holds a revision for the run (the highest of
its links', revoked ones included) refuses a group whose revision is not
above it with `error{stale}`: no link is minted with it and no link changes,
so members sent before others and delivered after them (over another
member's connection, or the connection before a reconnect) never replace
them. A group with no revision (0, an older host) is taken as it comes, and
the links keep the highest revision they held. The server holds revisions in
memory alone, never in the files it keeps links in: a group sent before it
started cannot reach it after. A server that knows no revision ignores the
field (it decodes host messages without refusing unknown fields), and an
older host sends none.

Server → host: `registered{sessionId, secret, shareBaseUrl, resumed, iceServers, links}`
(`links`: the ids of the live links the server holds for the session, `[]`
when none; an older server sends no field, which says nothing),
`viewer_join{viewerId, role, linkId?, linkLabel?}`, `offer{viewerId,sdp}`, `ice{viewerId,candidate}`,
`relay_start{viewerId}`, `viewer_leave{viewerId, reason?}`, `link_gone{linkId}`, `stop{sessionId}`,
`viewer_behind{viewerId, behind}` (a relayed viewer fell behind what the
server holds for it, or caught up: above; a host refuses one whose
`viewerId` is not 16 bytes, and one for a viewer it no longer has changes
nothing),
(`reason`: `revoked` when the viewer was let go for its link (revoked, its
run forgotten, its run's session no longer named) or `expired` (the link
reached its expiry), none for any other end (it left, its connection failed,
it was too slow); `link_gone`: a link a viewer joined the session through
(`linkId`, at most 64 bytes) no longer opens it (revoked, expired as the
server's clock says, its run forgotten, its run's session no longer named),
sent once, whether a viewer is still attached through it or not, in the
server's own lane, ahead of the viewers' messages queued for the host and
of the leaves of the link's viewers; those viewers are closed before it is
queued, and nothing of theirs still queued is sent after it (see the
queue's lanes above), so a host, older or newer, never hears of a viewer of
a link after its going; no link no viewer joined through is named,
and a host away is not told again. The server keeps at most 1024 joined
links per session (far more than can open it at once); one pushed out is
named gone then (it may still open the session: a viewer joining through it
later starts afresh on the host). The host types nothing more of what a
viewer asked for once its link went, by either of these, also after the
viewer left (see the host's queues below); and as it loses its connection
to the server, which would tell it, it counts every link its viewers joined
through as gone for what they asked for and is still under way (a viewer
joining through one once it is back starts afresh). A connection can also
stop carrying what the server sends without the host noticing (the server
has closed it, or the network drops what it carries, and nothing the host
wrote has failed yet), or carry it late (it waits on the way, or the server
pauses its reading), and a link going then would not reach the host in
time: so the host acts on what a viewer who joined through a link sends (a
line: a `submit`, a `chat` to the agent, a `chat_send`, and its carriage
return; an `answer`; INPUT; a save's parts and its rename; a `file_get`, an
`nvim_open`, a `chat`) only while it vouches for its server: while a ping
of its own (a WebSocket ping, which every server answers, an older one too),
sent within the last 15 s, has been answered. A pong follows everything the
server wrote before it, so what the host read before the answer is no
older than the ping, and a going the server queued by then has been read,
or follows the answer behind at most one relayed message and the server's
own lane. Nothing else vouches: not a frame the server relays, not the
server's own pings, not bytes read. Past the bound the host does with what
the viewer sent what it does for a link gone (a line's text not typed, or
typed with no carriage return and the session's turn held for a person;
the rest refused `revoked` with words saying the server has not been heard
from, which for a host means no ping of its own answered).
A viewer's attach needs its link alone. The host pings at once as a viewer
joins through a link (before its hello can arrive), and every 5 s while a
viewer who joined through a link, or something of theirs, is there, quiet
or not, each ping waiting 15 s for its pong, several side by side; the
first line, key or save of such a viewer after the host's start, a
reconnect or a spell with no link held waits within its own time for the
answer to its join's ping, and goes on once it comes. The cost, accepted:
while a server pauses its reading for longer than 15 s (a switchyard's
bound on what open hosts relay, about 35 s at its worst; a large frame of
the host's ahead of its ping on a slow uplink), a link's line waits, and is
refused when its time is up, rather than being entered. The server pings
the host every 30 s, and every 5 s while a link a viewer joined the session
through stands, for an older host, which counts whatever it reads as the
server heard from. Having pinged and heard nothing at all for 2 minutes
(whatever it reads counts for this, as the connection's TLS gives it,
record by record, a large frame that comes in slowly as it comes), the
host gives the connection up and connects again (its pings keep a
connection that drops what it carries from being idle, which TCP's
keepalive would otherwise have ended). The lines of the owner's own
windows, the host's own terminal and a paste invite's viewer need no such
word. A host reads a reason it does not know, or
one over 16 bytes, as none; an older server sends neither, and an older
host ignores both, as before: each side decodes the other's messages
without refusing unknown fields),
`attention{state,message?,source,kind?,options?}` (API-originated change to broadcast),
`activity{entry}` (an event reported through the API, for the host to record),
`link_created{requestId, url, invite, linkId, role, label?, expiresAt?, runId?}`
(`runId` for a link minted by `link_run`), `link_run_updated{requestId, runId, links}`
(how many of the run's links follow the new members),
(the link on the server's public base, and the same as an invite,
`conductor://<host>/join/<token>` with `?http=1` for a plain-http base,
which the desktop app opens itself), `link_revoked{requestId, linkId}`
(the link is revoked and its viewers closed; `error{not_found, requestId}`
for a link the server does not hold, which, once the session has ended and
taken its links, still stops what its end writes their viewers;
`error{store_unavailable, requestId}`
from a switchyard that revoked it but could not yet delete what it keeps
of it on disk, which the host's next revoke of it, or the switchyard's
sweep, tries again; until then `registered.links` still lists it, so the
host keeps its record), `error{code, message, requestId?}`
(`requestId` names the link request a refusal answers: `invalid_role`,
`invalid_request`, `rate_limited`, `link_refused`, `no_instance`, `not_found`,
`store_unavailable`, `stale`).

A run link minted by `link_run` resolves on the join page as a run link does
(`{run: {id, name, members}, role, label, switchyard}`), each member with
`kind: "hosted"`; a member whose session is not registered now reads `ended`,
and none registered at all answers `503 host_offline`. The publishing server
mints it when its run's Share asks (`POST /api/runs/{run}/links` answers
`201 {link: {…, runId, remote: true}, url, invite, remote: true}`, or a local
link with `rendezvous: {server, error}` when no member is published), sends
`link_run_update` two seconds after the run changes (only when the members it
would send differ from the last), and revokes it with `link_revoke`. Its
link requests and updates of one run take turns, each reading the members
as they are when its turn comes and giving them a revision above every one
it gave before (at least its clock's milliseconds). An update that gets no
answer within 15 s, or is refused with any code but `not_found` (`stale`,
`rate_limited`, or `invalid_request`, which a member not registered there
yet answers while the members' connections come back after a switchyard
restart), is made again with the members as they are then, whatever was
sent last: five seconds later, twice as long after each failure in a row,
a minute at most, until one lands or the run has no link there; the one
that got no answer, should it land after, is refused as stale. A change
of the run is still sent two seconds after it, whatever try is waiting.

`activity` carries one activity-log entry, the fields of the `activity`
control message above (`entry` has its own `t`), in both directions. Host to
server, it carries every entry the host's session records, so that
`GET /api/events` shows a hosted session like a server one. The host queues
them (at most 256 waiting; a burst past that is dropped, and so is anything
recorded while the connection is down, for the session log has it all) and
sends them in order. With an `attention` entry the host sends `state`
(`needs_input`, `working` or `done`): the attention state the entry records,
which its session hands on with the entry (the state set with the entry's
stamp). The host sends a change of state in its own `attention` message on
another path, so the server may receive the entry first; `state` is how it
types the entry for webhooks all the same (see Events). The server takes the
session from the connection and ignores `sessionId`, drops an entry whose
`type` it does not know instead of closing the connection, cuts every field to
the limits of Events (`by` too: it loses its control characters and surrounding
space, and one still over 64 bytes is dropped rather than cut), keeps `state`
only on an `attention` entry and only as one of the three states (it ignores
anything else), stamps the time of receipt on an entry without a readable `at`,
and never sends the entry back.
Server to host, it carries an event that an agent reported through
`POST /api/sessions/{id}/events` for the hosted session: the server has no
activity log for it, so the host records it (its own event limit applies)
and reports it back like any other entry. The server sends at most 20 of
these a second, 40 at once, together with the
`attention` messages it forwards (see Events); it never sends `state`. Both
directions are text frames under the host-message limit (64 KiB): `entry`
is bounded so that it fits one 8 KiB CONTROL frame, and the envelope,
`state` included, adds under 100 bytes. A peer that does not know `activity`
ignores it, so a hosted session of an older host produces no events; one that
does not know `state` ignores that field.

A server publishing its sessions to a switchyard (`rendezvous`) tells it as
little as lists a session there and introduces its viewers: besides the
lesser `register` above, it sends no `chat` and no `activity`, and
`attention` with `state`, `kind` and `source` alone (no `message`, no
`options`); it takes no `attention` and no `activity` from the switchyard
(the session's agent reports to its own server); and a `stop` from the
switchyard ends the publication, not the process: the server sends
`status{stopped}`, closes the viewers' peers and the connection, and the
session runs on, its next Share publishing it again under another `localId`.
When the session ends, its publication sends the `status` and keeps the
connection 5 s more, as long as the switchyard may still write the viewers
of the session's links what it relayed them last, so that a revoke of one
of those links is still carried there (`link_revoke`).
A switchyard drops the `chat` and `activity` a host sends. `conductor host`
publishes to the owner's own server: it sends everything and obeys `stop`.

The host registers before it starts the process so the session ID can be
placed in the agent's environment.

A host that loses its connection reconnects with `resume` and the secret from
`registered`. The session shows `host_disconnected` in the meantime and is
removed after 60 s without the host. A server that no longer knows the session (a restarted switchyard keeps
hosted sessions in memory) closes the resume with `4404`; the host then
registers afresh. On every connection it starts, the host sends its
session's `attention`, the empty state included: the server keeps the
attention it had when the host went, so a question answered at the host's
own terminal while it was away leaves the listing when the host is back.

`register` may carry `host.instance` (a secret of the host process, at most
64 bytes, never logged) and `session.localId` (the session's id on the host,
at most 64 bytes), both or neither, else `ErrBadRegister` closes the
connection. With them the session's id is fixed: a hash of the pair, so a
host that registers again after the server restarted gets the same id, and
a link minted for it before names it again; a session still held under that
id whose host is away is taken back (`resumed: true`), one whose host is
connected refuses the second. `conductor host` makes an instance when it
starts; a Conductor publishing to a switchyard makes one for its life and
uses each session's own id. The publishing server keeps its records of the
links minted for a session while `registered.links` (then `link_created` and
`link_revoked`) says the switchyard holds them, and, with an older
switchyard that says nothing, only while the session keeps its id. A
switchyard holds a host's links while any of its instance's connections
is up, and for seven days after the last one ends; an open host's link
also gives its place in its address's count (`switchyard.openHostLinks`)
to a new link of that address that has no other once its host has been
gone five minutes, and is ended (`registered.links` leaves it out). A run
the publishing server forgets (idle and evicted past its hundred, or one
whose launch failed) has the links minted for it at the switchyard revoked
over a member's publication still up, else any of the server's (the links
are the publisher's there), as a revoke of one of them from the Share
dialog goes; with nothing of the server's published, nothing can carry the
revoke, and they expire or go with the host's others.

## Attention

Each session carries `attention{state, message, source, since, kind?, options?, id?, answered?, plan?}`
in its `Info`. States: `""` (nothing), `working`, `needs_input`, `done`.
`plan` (#289) is the path of the plan a question asks approval for (Claude
Code's ExitPlanMode: its hook names it as `planFilePath`, reported as the
attention route's `plan`, or the dialog's last row prints it), once the session
has seen it to be one of the agent's plans (File reads, the plans directory),
at most 1,024 bytes; a page opens it with a `file_get` of that path. Absent
from any other question, and from an older owner.
Sources: `api` (the agent's own token), `admin`, `bell`, `osc`, `pattern` (the
screen-pattern detector, below), `trust` (the agent's workspace-trust
question, below), `question` (a question asked during the work, below),
`submit` (a submission's text typed and its carriage return not written, its
link revoked or expired: the session's turn is held until a person types into
the terminal; see `submit`), `title` (`working`: the agent's window title says
it works, below), `status` (the agent's program status, an OSC 7501 sequence
Codex 0.163 and later write: `working`, or `needs_input` with words naming
what blocks it, below), `input` (the empty state after a reply resolved the
question, or an Enter typed in the terminal withdrew a bell's, an OSC
notification's, the pattern's or the status's wait), `screen` (the empty
state after the screen moved on from a question without an answer: a bell's
or a status's wait whose screen changed past the snapshot's threshold, a
startup question or a dialog gone from the screen, or a status's wait that a
later status ended; see Who may answer a question). A question that keys
typed in its terminal have answered (a dialog's key, a startup question's
Enter, the held text entered: *consumed*, below) stays `needs_input` while
the program's dialog is on the screen, marked `answered:"terminal"` from the
moment of the key, in the stream's `Info` and the terminal's `attention`
message alike; a client offers such a question nowhere (no row prompt, reply
bar, chat choice or Needs you place), a reply that still names it is refused
`question_answered` with the terminal's words, and a client that hears that
refusal drops the question it named at once, whatever the stream has said so
far. The mark goes with the question, when the screen shows it gone. While
that turn is held, the hold is what shows, but for a `trust` or `question`
prompt on the screen, which shows over it whether it came first or after
(and, answered while the hold still stands, gives it its words back);
another change (an agent's report, an admin's clear) is recorded in the
activity log, with its state, and not shown. A key that ends the hold
without answering such a question leaves the question showing, and the
submissions it refuses refused. The question the hold took the place of
as it showed (one the agent reported, which the held text may have been
typed for) shows again, with its `id`, when the keys that end the hold do
not answer it (no Enter, none of its choices: Ctrl+U, the text killed),
and an `answer` to it waiting for the turn is taken; once the agent has
reported another state during the hold, it does not. `id` names the
change as `since` stamps it: 16 hex characters, new with every change that
shows a state (the same words again change nothing, the id included; a
dialog asked again as the question just answered is asked answered, see
Answering a question), none with the empty state. A question read off the
screen (`trust`, `question`) is told by its words whole, as the screen's
rows show them: drawn again at another width, its rows broken elsewhere,
it is the same question, told by its rows at that width from then on; at
one width its rows are the program's own;
two that differ anywhere, past the 500 bytes of `message` too, by one
space within a row or by a row break at one width, are two. Half a frame
(a synchronized update under way) forgets no dialog answered. A hosted session's is its
host's, as the host reports
it; a change the server makes for a hosted session (a report through its
routes) has none in the server's listings until the host reports it back with
its own: the host tells its attention, with its id, after every report the
server forwards, one its session takes as changing nothing included, so the
listing never holds an older id or none for long.

`kind` describes the shape of a prompt so clients can offer one-click answers:
`permission` (a numbered permission dialog), `prompt` (a free-text reply is
expected), `done` (the agent finished its turn), or empty when unknown.
`options` is at most 6 entries of `{label, input, mode?}`: `label` is shown on a
button (≤ 60 runes; a longer one is cut where a word ends, with an ellipsis)
and `input` (≤ 48 bytes) is what is written when the
human picks it: an `answer`'s `input`, naming the question (see Answering a
question), or, to an owner that predates `answer`, an INPUT frame. `mode:
true` marks a choice that changes the session's permission mode (Claude
Code's "switch to auto mode", "accept edits", "bypass permissions"): pages
give it no number key and never offer it first, highlighted; it is omitted
when false. Clearing the state drops `kind` and `options`. A `needs_input`
of kind `permission` with no `options` is a dialog whose choices the page is
not given: pages take no typed reply for it (its Enter would pick what the
dialog highlights) and say to answer it in the terminal.
`conductor notify --state needs_input --message "…" --choices "a|b|c"` (at most
6 choices of 40 bytes) reports `kind:"prompt"` with one option per choice whose
`input` is the choice and a CR, so a click types it as a line; the agent is
told to name the choices in the terminal first, so a typed answer matches.
`conductor notify --ask --message "…" [--choices "a|b|c"]` reports the same
with `ask: true`, the agent's own question (below); `--ask` with another
`--state`, an `--event`, a hook flag or `--codex` is a usage error. A server
that refuses `ask` as an unknown field (an older one) is sent the report
again without it, and the MCP tool `ask` does the same.
`conductor notify --claude-hook` maps Claude Code `PermissionRequest` hooks
(and `permission_prompt` notifications) to `kind:"permission"` with no
options: the words are the hook's message, else "Claude Code has a plan for
you to approve" for `ExitPlanMode`, the first question for `AskUserQuestion`
(both come as permission requests), "Allow <tool>?" otherwise. Claude Code's
dialogs differ by tool, permission mode and release (2.1.304's Bash dialog
has four choices, its 3 switching to auto mode; the plan's approval offers
auto mode or bypass first), so their choices are read off the dialog itself
(the Claude Code entry's question asked at any time, below); other
notifications are `kind:"prompt"` with no options.

Automatic detection runs on whichever process owns the PTY. A bare BEL
(`0x07`) outside an escape sequence, `ESC ] 9 ; text ST` (iTerm2/ConEmu style)
or `ESC ] 777 ; notify ; title ; body ST` (urxvt style) marks the session
`needs_input`; the BEL that terminates an ordinary OSC (window title, hyperlink)
does not. A bare BEL leaves a session that is `needs_input` already as it is
(a question's words and choices, a hook's report): it says nothing more. An
OSC notification, which has words, replaces a wait without choices, never a
prompt with choices or a question the session holds (`source` `trust` or
`question`). Neither undoes a `done` until the next Enter is written (a
`control` client's input with a carriage return, or a submission's): the turn
the `done` ended asks nothing more, and Codex rings the bell it is asked for as
its turn ends, after or before its notify reports `done`. A `done` that
changes nothing shown (a turn that ends with the last one's words) ends its
turn too; after the next Enter, or any change of state, a bell asks again.
Bursts are limited to one change per 500 ms: a bell or notification that
changes something starts the 500 ms, one that changes nothing (a turn's own
after its `done`, a bare BEL at a wait that stays) does not, and the next
turn after a `done` starts them again, so its first bell asks however soon
it follows the last turn's. That turn begins with the first Enter written
after the `done` or the first other state shown (a `working` report: the
agent went on by itself); an Enter within a turn (an answer) keeps the 500
ms. Output read before the next turn began and looked at after is the ended
turn's own: its bells ask nothing, nor does an OSC notification begun in it
and ended in output read after (one split between two reads). Keys typed
into the terminal by a `control` client answer no question (see Who may
answer a question): an Enter among them withdraws a wait of the bell's, an
OSC notification's or the pattern's (`source:"input"`, no answer recorded),
and a bell's wait is also withdrawn once its screen has moved on (the
snapshot, below); the agent's own report waits for the agent to move on
(its hook, its title, its next report, its done rules) or for a reply naming
it. An input that is
only a terminal's own automatic reports, at most 256 bytes of them, is written
and changes nothing, nor stamps its viewer as typing: a cursor position report
(`ESC[<row>;<col>R`, with or without `?`, and the extended one with its page,
`ESC[?<row>;<col>;<page>R`), a status report (`ESC[0n`…`ESC[3n`),
device attributes (`ESC[?…c`, `ESC[>…c`), a mode report (`ESC[?<mode>;<n>$y`),
a window report (`ESC[<n>;…t`), a colour query's reply (`ESC]4;…`, `ESC]10;`
to `ESC]12;` with `rgb:…`, ended by BEL or ST), a DCS reply such as XTVERSION
(`ESC P >|… ESC \`), focus in and out (`ESC[I`, `ESC[O`), or the kitty
keyboard protocol's flags (`ESC[?<flags>u`, the reply to the `ESC[?u` Codex
asks before it turns the protocol on): a browser's xterm
answers Codex's cursor position query after every turn, and that answer is not
a person answering Codex. A trust question (`source:"trust"`), and a question
asked during the work (`source:"question"`), is consumed by an input with a
carriage return in it or one of its choices (a reply to it refused from then,
`answered in the terminal`), and withdrawn once the screen shows it gone
(`source:"screen"`); an arrow key that moves its selection leaves it as it
is. When a session's process exits or is stopped, its
attention state is cleared in the step that makes it `exited` or `stopped`, and
viewers get an `attention` message with the empty state: an ended session never
shows `needs_input`, and its activity log keeps the entries. A hosted session's
state is cleared when its host reports it ended; a host that disconnects leaves
it as it was, since the host may come back.

The agent's own question (#193): a `needs_input` report that says the agent
asks it, `ask: true` on the attention route or the events route's attention
word (the MCP tool `ask`, `conductor notify --ask`), shows until it is
answered, also once the turn it was asked in has ended, which is when the
agent waits for the answer. It is said on purpose, never inferred: a plain
`needs_input` (a hook's permission prompt, OpenCode's, DeepSeek Harness's and
aider's waits, `--state needs_input`) gives way to `done` as before, and
`ask` with another state changes nothing. The session keeps one such
question, shown or set aside:
- In its turn (no `done` since it was asked), the agent's work (a `working`
  report, Codex's working title) and another wait (a hook's report without
  the mark, a dialog on the screen) take its place and set it aside; a
  `done`, any (its turn's, with `turn` or without; Claude Code's
  `idle_prompt` a minute later), leaves it showing, or shows it again from
  aside in place of what shows, the same question: its `id` and `since`, so
  that a reply under way to it keeps its carriage return, and its line in the
  chat, where it is not put a second time. The `done` still ends the turn and
  is recorded as the report it is: its `attention` entry, with the state
  `done`, reaches the Events page, the webhooks and a crew's start-after rule;
  the question shown again records its own `needs_input` entry.
- After its turn's `done`, it shows until a person's keys or reply answer it
  (as any question), the agent clears it (`clear`) or asks another, another
  state takes its place (the agent at work in a new turn: a `working` report
  after the `done`, or after the first Enter written since), or the session
  ends; a `done` changes nothing shown and is recorded, and Codex's working
  title read after the `done` changes nothing until the next Enter, as for a
  `done`.
- The bell, an OSC notification and the screen pattern never take its place,
  shown or set aside. A question a reply has claimed, or that a person's keys
  reached, is removed, never set aside; one set aside is removed by a
  person's keys or reply typed meanwhile (the person has taken part), not by
  Conductor's own submissions (a crew's prompt, a handoff, a broadcast). A
  line typed while it is set aside whose carriage return follows the `done`
  that shows it again is entered all the same: a handoff's answers nothing,
  and begins the next turn, in which the agent's work removes the question;
  a person's reply or keys begun while it was set aside and written once the
  `done` has shown it again answer it. The hold of the turn (`submit`) holds
  it over, through its turn's `done` and its work in that turn, as it holds
  over any question the agent reported, and an OSC notification leaves it
  held over. Handoffs and broadcasts wait while it shows, as for any
  `needs_input`, and a reply naming it while it is set aside is refused, as
  one to a question no longer shown.

A hosted session's host is sent the mark with the report (`attention` with
`ask`) and applies the same rules; it tells its attention back with the mark,
and the server's copy keeps a marked question it shows through a forwarded
`done` (the host told the `done`), while a question the host has set aside
shows the forwarded `done` there until the host tells its attention again. An
older host ignores the field, and the question shows there as before.

An agent with no hook and no bell can be given a screen pattern instead: a
catalog signal `{"kind": "pattern", "pattern": "<RE2>"}` for server sessions,
`conductor host --signal-pattern '<RE2>'` for hosted ones; the pattern is at most
200 bytes and must not match an empty line. The
process that owns the PTY keeps the text of the last screen line: escape
sequences are stripped, `\r` rewrites the line from column 0, `\b` deletes a
character, and only the last 4096 bytes of a longer line are kept. After 500 ms
without output the line is matched against the pattern; an empty line or one of
white space never counts. A match marks the session `needs_input` with
`source:"pattern"`, `kind:"prompt"` and the message `prompt: <line>` (of a line
longer than a message can be, its last 492 bytes, cut on a character boundary),
unless it is `needs_input` already: a prompt a hook or a bell raised keeps its
own message, kind and options, and the same prompt does not report itself twice.
Output that never pauses (a spinner, a TUI that redraws continuously) never
gives the 500 ms of silence, so such an agent is not served by a pattern. A
host reports the change to the server like any other attention change.

A bell's wait, or an OSC notification's, has no rows of its own, so the
session reads the agent moving on from its screen (the snapshot): every
session, server or host, keeps a model of its screen; the first time the
output has been quiet for 500 ms after such a wait showed, the screen's rows
are the baseline (the program draws what it rang for in the same read or
the next); at each later quiet, while the wait still shows and nobody is
answering it, the rows are compared with the baseline, and the wait is
withdrawn (`source:"screen"`, nothing recorded) once at least 3 rows differ
by more than 8 cells each: a spinner's frame, a countdown, a clock or a
status line's words change a row or two by a few cells and never count;
output printed (every row shifts) or the prompt area redrawn does. A resize
drops the baseline, taken again at the next quiet (500 ms from the resize
for a program that draws nothing for it); a screen the program has not
erased since a resize is compared all the same. A question read off the
screen (`trust`, `question`) is withdrawn by its own watcher, the pattern's
prompt by an Enter; a bell rung with a dialog (Codex's approval) is replaced
by the dialog's question at the first quiet, where the snapshot only takes
its baseline. Reading the prompt's words per agent is #195.

An agent's catalog signal can carry a `workingTitle` (RE2, at most 200 bytes,
not matching an empty title; with any `kind`): the window title it writes while
it works. Codex's built-in has one, `^(?:\S+\s+)?(?:Working|Thinking|Waiting)$`,
and Conductor launches Codex with `-c tui.terminal_title=["activity","run-state"]`
(the person's `config.toml` is not written), so Codex 0.161 titles its window
"⠋ Working" (a braille frame, changing every 100 ms; the word alone with
animations off) through a turn, "Ready" at rest, "⠋ Starting" while its MCP
servers start and "[ ! ] Action Required" while a view waits on a person. A
server session of such an agent reads every window title (`ESC ] 0 ;` or
`ESC ] 2 ;` to BEL or ST), control characters dropped; a title longer than the
256 bytes the scanner keeps of a sequence, with its number, matches nothing,
nor does any title of an agent without a `workingTitle`. A title that matches
shows `working` with `source:"title"`, as the agent's own working report would,
over a bell, an OSC notification, the agent's report (with choices or without),
the pattern's prompt, the empty state, or the `done` of a turn an Enter has
followed; it changes nothing over `working`, over a question the session holds
(`trust`, `question`), while the session's turn is held (`submit`; nothing is
recorded), over the `done` of the turn just ended (until the next Enter, as for
the bell, with the same rule for a title read in the ended turn and looked at
in the next), on an ended session, or over a wait shown less than 1 second ago:
Codex rings the bell for an approval as it draws the dialog, and its title then
says Action Required. A wait (a bell, an OSC notification, the agent's report
or the pattern's prompt, not a question the session holds) shown while the last
title read matched, or met within its second by a title that matches, gives
way to `working` once its second has passed, unless a title that does not match
came meanwhile (Action Required, Ready): Codex writes its title only when its
text changes, once a turn with animations off. The last title read is its
turn's: the next turn (the first Enter after a `done`, or another state shown
after it) begins with none, and a title saying Working of the ended turn read
after that settles nothing. A title asks the event bucket
for nothing, does not start the bell's 500 ms, and records an attention entry
only when it changes the state. Hosted sessions (`conductor host`) do not
read titles.

Codex 0.163 and later also declare their state in a program status, OSC 7501
(`ESC ] 7501 ; state=S:app=codex[:kind=K] ST`, written once per change):
`idle`, `working`, `blocked`, and from 0.164 `done`, `error` and the kind of
what blocks it, `permission`, `question` or `auth`. Once a session has read
one, the window title is not weighed any more: the status says what the
title said, and from 0.164 says it right where the title cannot, since a
turn that goes on with one of Codex's async questions pending reports
`working` while the title says Action Required. `working` is weighed as a
matching title (the same rules and settle, source `status`); `blocked`,
`idle`, `done`, `error`, `clear` and a state Conductor does not understand as
a title that does not match, keeping the wait that shows. A `blocked` with
a kind raises a wait of its own where nothing with words of its own shows
(`needs_input`, source `status`, kind `permission` or `prompt`, message "The
agent asks for a permission", "The agent asks a question" or "The agent
needs you to sign in"); a `blocked` without one (0.163, where a pending
async question blocks too) raises nothing. A wait the status raised is
withdrawn by a later status that is neither `blocked` nor `working`, by an
Enter typed in the terminal and by the screen moving on, as an OSC
notification's is; one read while a question held on the screen showed is
remembered and shows once the screen moves on from that question. The state
outlives Conductor's turns, since Codex writes it only when it changes. With
Codex 0.163 a wait raised while an async question is pending stands until
the status says `working` again, when the question is answered or resolves
by itself, as under the title alone; with 0.164 it gives way after its
`TitleSettle`.

An agent's catalog entry can carry a `trustPrompt`, the words of its
workspace-trust question (RE2, at most 200 bytes, not matching an empty text).
A server session of that agent keeps the end of its screen's text, each escape
sequence and control character read as one space and every run of white space
as one, its last 1024 bytes, so that words a TUI draws with cursor moves
between them still read as a sentence. After 500 ms without output, output
that only sets the window title (OSC 0 or 2) not counting, the text is matched
against the pattern; a match marks the session `needs_input` with
`source:"trust"`, `kind:"prompt"` and the matched words as `message`, unless
it is `needs_input` already. Watching ends with the session's first
submission (below). Once keys typed in the terminal reach the question (an
Enter, its choice), or a reply resolves it, only the
question drawn again raises it again (consumed when it is the one just
answered, before the program moved on). The built-ins carry the questions of
Claude Code 2.1.287 (`Is this a project you created or one you trust?`) and
Codex 0.159.0 (`Trust this folder?`). It exists for crew runs: a member's
prompt is never typed while it shows (see Crew runs).

Explicit updates: `POST /api/sessions/{id}/attention` with
`{state: "needs_input"|"working"|"done"|"clear", message?, kind?, options?, agentSession?, turn?, ask?, plan?}` (`ask`
a boolean: with `needs_input`, the agent's own question, see above; a value
that is no boolean is `400 invalid_request`; `plan`, with `needs_input`, the
path of the plan the question asks approval for, at most 1,024 bytes, #289:
in the one step the report takes, a server session keeps it as one of its
plans when it is a file in the agent's plans directory and sets it on the
question, a hosted session sends it to its host with the question, and the
question carries it as `attention.plan`, see Attention; a report refused
names nothing) and
`Authorization: Bearer <agent token>` (or the workbench token). Every session's
process receives `CONDUCTOR_SESSION_ID`, `CONDUCTOR_NOTIFY_URL` and
`CONDUCTOR_NOTIFY_TOKEN`, which `conductor notify` and `conductor crew` read,
`CONDUCTOR_BIN`, the absolute path of the conductor binary its hooks run,
which the Conductor skill runs its commands with, and, for a server session,
`CONDUCTOR_AGENT`, the id of its agent in the catalog, and `CONDUCTOR_SKILL`,
the server's copy of the skill (`<dataDir>/hooks/skills/conductor/SKILL.md`). Only Conductor sets them: `CONDUCTOR_*` from the
server's or the developer's environment and from an agent's `env` never reach
a session. The token is stored
hashed and only ever authorizes this route and `POST /api/sessions/{id}/events`
for this one session. Each report, the agent's or the admin's, spends a token
of the bucket the events of its session spend (see Events), on a server
session and on a hosted session, its host connected or not: with none left the
answer is `429 rate_limited` and the state does not change. For a hosted
session the state is also sent on to the host; while no host is connected
the report is applied on the server, and spends its token all the same. What
a session sees for itself (the bell, an OSC notification, the screen pattern)
spends no token, and a change of state a session applies always records its
`attention` activity entry: a state that shows has its entry (see Events).

`agentSession` is the agent's own session id as its hook payload names it, at
most 128 bytes (`400 invalid_request` past that), and `turn` says the payload
reports a turn: a prompt taken or a turn finished. They change no attention:
for a server session whose agent's `session` recipe takes ids from its hooks
(`idFrom: "hook"`, see HTTP API), an id that begins with a letter or a digit,
holds only letters, digits and `._:-`, and matches the recipe's `idPattern`
becomes the session's `agentSession.id` when it has none, and afterwards as the
recipe's `idPolicy` says: `latest` takes each new id, `lowest` keeps the
smallest (Codex's hidden title thread reports with a later id than the
conversation's). `turn` makes it `resumable`. Any other id is dropped without
an error, and a hosted session ignores both fields. `conductor notify` fills
them from Claude Code's `session_id` (with `turn` on `Stop` and
`UserPromptSubmit`), Codex's notify `thread-id` (every
`agent-turn-complete`; the turn of the hidden thread that names a Codex
conversation, whose first input begins `Generate a concise, single-line task
title`, is not reported at all), Copilot's `sessionId`, Cursor's
`conversation_id` and Antigravity's `conversationId`. A server that refuses
the two fields as unknown (an older one) gets the report again without them.

Session `Info` also carries `branch` (the git branch of the working
directory, read from `.git/HEAD` at launch; server and host alike) and, for
hosted sessions, `hostUser`. A server session carries `yolo: true` when its
launch applied its agent's yolo recipe (absent otherwise, an agent without a
recipe included), `agentSession{id, resumable, source}` once Conductor knows
the agent's own session (`source` `set` when Conductor chose the id at launch,
`hook` when the agent reported it, `resumed` when this session resumed it;
`resumable` once the agent has reported a turn, and always for a resumed one),
and `resumedFrom`, the session it resumed or relaunched.
`GET /api/sessions/{id}/links` adds `active` to every link: the number of
viewers currently attached through it.

When a controller's input clears `needs_input`, the session records
`lastAnswer{by, byName, at, message}` in its `Info` (the prompt that was
answered and who answered it) and an `input` activity entry.

What Conductor types itself (a crew member's prompt, a handoff or a
broadcast) and a reply sent with the `submit` control message are submitted as
a terminal sends a paste and then a key press, one submission at a time per
session. The text has each line break, carriage return and tab made a space,
every other control character (escape among them) and any invalid UTF-8
dropped, and its surrounding space trimmed; once cleaned it is at most 32756
bytes. It is written wrapped in `ESC[200~` and `ESC[201~` while the program
has bracketed paste on (the session follows `ESC[?2004h` and `ESC[?2004l` in
the output, as a terminal does, a sequence split between two reads included),
and as it is otherwise; then, 250 ms later, a carriage return is written on its
own. A TUI that reads a text and its carriage return in one read takes the
carriage return as part of a paste (Codex's paste-burst detector makes it a
newline; Claude Code collapses a read of over 800 bytes into a pasted block),
so written apart it is Enter. The carriage return answers no question: a
line naming none (`submit`, a `chat` to the agent, a crew's prompt, a handoff,
a broadcast) begins the next turn, and a question the agent reported goes
when the agent moves on; an `answer` with `text` resolves the question it
names once its carriage return is written (see Answering a question); an
Enter alone withdraws a bell's, an OSC notification's or the pattern's wait.
When a new `needs_input` comes up during the 250 ms, the carriage return is
left out, and the text, already written, waits in the program's input and is
never written again (for an `answer`'s line the session's turn is then held
for a person, as for a revoked link's: `submit` under Attention); in a
session with a `trustPrompt` the pause lasts until the screen watcher has had
its 500 ms look at what was drawn since the text (at most 2 s more), so a
trust question drawn then holds the carriage return back too. While the
screen, a whole frame, still shows a startup question, answered or not (the
program slow to move on), no line is typed into it: a person's is refused
(`not_sent` with the trust question's words), Conductor's own waits. No lock
of the session is held
across the pause, and a person's keystrokes (INPUT) are written as they come,
not held back. What Conductor submits always records one `input` entry as the
text is written, `byName` `crew` (for a broadcast, the admin's display name)
and the text as `message`, cut to the 500 bytes of Events (the terminal gets
all of it); a `submit` reply records the answer as a controller's input does.
A handoff or a broadcast is never submitted while the session is
`needs_input` (see Crew runs).

What the session types into the program (a controller's INPUT, a
submission's text and its carriage return) goes through one writer, in the
order it came, and each write waits at most 10 s for the program to take
it. A program that has stopped reading its terminal fills the terminal's
buffer: a write not begun by then is never made, and its sender gets
`error{input_failed}` (a submission ends there and gives the next its turn);
one begun lands when the program reads again: a submission whose text has
begun is accepted, with no error, and finished as the program reads (its
carriage return after its pause, the session's turn held till then, so no
later line joins it); so is one whose text is written and whose carriage
return has no turn in time, and one whose carriage return has begun counts
as entered. One whose link is revoked or expires before its carriage return
has begun gets none, its text written or not (see `submit`); its text
written, the turn is held for a person, also when a prompt or a dialog that
came meanwhile left the carriage return out. A write that
waited behind another is looked at again as the
writer takes it: a submission's text or carriage return, or several keys
with an Enter, that a question or a dialog come meanwhile would take are
held back then as they would have been before they waited (a reply or the
keys refused, a handoff not sent, the carriage return left out), and
nothing of them is written. Once one write has waited its 10 s,
the next are refused at once until the program takes it, rather than 10 s
each. A controller's keys not yet written when its connection ends (a
revoked link, a viewer that left) are never written, nor, its connection
still open, once its link no longer opens the session (`revoked`, `expired`;
see Control messages): one INPUT frame whose write began before lands when
the program reads, and nothing after it is begun. The other viewers and
the output go on meanwhile. An `nvim_open` gives Neovim 15 s to start and
show the file (one still busy then is ended, and its editor says `closed`),
and one whose connection goes meanwhile ends the Neovim it started; a
connection's editors end as it goes without holding it, each given 30 s to
leave before it is ended (Neovim removes its swap file last as it leaves, so
only a kill leaves one; a kill after the wait is logged with the file), and a
server or host that stops waits for them.

Session `Info` carries `crew{runId, crewId, member}` for a member of a crew
run (see Crew runs), and no `crew` otherwise.

Changes are pushed to attached clients as the `attention` control message and
to admins as `session` events on `GET /api/events` (Server-Sent Events over a
header-authenticated `fetch`: `snapshot` with the full list first, then
`session` per change, a server session's as it is when the event goes out,
so that one session's events never undo a later change (a rename) out of
order, `removed{id}` when a session leaves the registry,
`activity` for every activity entry, see Events, `run{id}` when a crew run
changes in a way no session change carries, or `run{id, removed: true}` when
the server forgets it, see Crew runs, and `dispatch{seq, entity, id, ticket,
project}` when something of Dispatch's changes, see Dispatch).

A trust question (`source` `trust`) carries the agent's answers as its
`options`, from the catalog's `trustAnswers`: the label a page shows and the
keys that pick it, the trusting answer first (Claude Code highlights "No,
exit", so its trusting answer is Down then Enter; Codex highlights the trusting
answer, so Enter). While it shows, the owner refuses a person's `submit`, a
`chat` to the agent and a `chat_send` with `not_sent`. The agent's other
startup questions, the catalog's `questions` (`[{prompt, words?, anytime?, answers, answersFromScreen?, modeAnswers?, typedAnswers?}]`,
at most 8, each prompt and its answers bounded as `trustPrompt` and
`trustAnswers` are; a saved agent that leaves them out keeps the replaced
agent's), are held the same way, with `source` `trust`, the words of the one
showing as the `message` and its answers as the `options`. A question's
`words`, a pattern bounded as its prompt, picks those words out of the same
text: what its last match's capturing groups matched, joined by a space (the
whole match without a group); without it, or when it matches nothing, the
words are what the prompt matched. Codex's built-in entry has
"Hooks need review" (round 13), which Codex 0.161 draws when its hooks are
new or changed: Trust all and continue is `2` then Enter, Continue without
trusting `3` then Enter; the highlighted Review hooks is what a bare Enter
would pick. Beside a newer background server it also draws "Background
server has incompatible feature settings" (a digit picks at once: `1` runs
without the daemon, `3` cancels and Codex exits; Cancel is highlighted) and
"Update available" (Update now highlighted, which runs `npm install -g`;
Escape skips, `3` alone skips until the next version), both held the same
way; the update question is found by its highlighted "Update now (runs" and
its words are its heading, "Update available 0.161.0 → 0.162.1". An input
that is exactly one of the showing question's choices answers it with or
without an Enter.

A question marked `anytime` (`{prompt, words?, anytime: true, answers}`) is
one the agent asks during its work, not only as it starts. A server session
watches for it for its whole life (the startup questions' watch ends with
the first submission's Enter), on the screen as the program drew it: the
session keeps a model of the terminal (its size, each cell's character, the
cursor, the scroll region, the alternate screen; cursor moves, erasing,
inserting and deleting, scrolling and autowrap applied, colours and the
rest skipped), and the question's `prompt` and `words` are matched against
its rows, each without the spaces at its end, joined by line breaks,
without the blank rows at the bottom, its last 80 rows at most (a dialog is
the bottom of what a TUI draws). A dialog a TUI redraws only where it
changed (ratatui does; Codex advances its queued approvals in place) reads
whole there, and one the program has taken away is gone, however few bytes
took it. After 500 ms without output, title updates aside, a match marks
the session `needs_input` with `source:"question"`, `kind:"permission"`,
its words and its answers. It takes the place of whatever the session
shows but a startup question (a bell, an OSC notification, a pattern's
prompt, a report with `options` or without, a `done`, its own question with
other words): the dialog is what the terminal waits on, and another
question's choices would land on it. A report of `needs_input` that comes
after it, with `options` or without, does not hide it. While it shows it is
held as a trust question is: a crew types no prompt, handoff or broadcast
into it (a prompt the readiness wait typed anyway is refused, nothing
written, and waits for the answer), and the owner refuses a person's
`submit`, a `chat` to the agent and a `chat_send` with `not_sent` ("the
agent asks a question on its screen…"). Its choices, an Enter, a digit from 1 to 9 (a dialog's list
picks on its number) and Ctrl+C answer it; the same dialog drawn again
after an answer, still there after a key that did nothing, or the next one
in a queue, is asked again, and a dialog the screen shows with other words
than the question held takes its place. When a person has typed into the
session while it showed and a quiet screen no longer shows it (the dialog
went with a key Conductor does not know as an answer: d, a click), it is
recorded as answered by that person; with no key typed, it stays until the
agent reports something else. The model is as large as any terminal a
session can have (500 by 500). Before a submission writes anything, and
again before its Enter (after a wait for a quiet screen of at most 2 s),
the owner looks at the screen whether quiet or not: a dialog drawn just
before the text holds it back whole (Codex's overlay would drop the
paste), and one drawn during the pause holds the Enter. A dialog on the
screen holds every submission whatever the session shows: a person's and a
crew's prompt get `not_sent`, a handoff waits. Right after a key that
answers the dialog the screen is still the answered one: the look waits
until the program draws again or the end of a quiet period has looked
(a key that did nothing, a digit past the dialog's lines, leaves the
dialog asked again), so that a handoff coming as a person answers neither
asks the answered dialog again nor lands on one still there. While the
question shows, an input of several keys with a carriage return that is
not one of its choices (another question's choice clicked late, a paste)
is refused with `not_sent`, nothing written; the screen is looked at before
such an input, so that a dialog drawn and not yet seen quiet is the
question it is weighed against; right after a key that answers a dialog
on the screen, until the screen has been looked at again, such an input is
refused. A key that answers a dialog on the screen (an Enter, one of the
answers' keys, a digit, Ctrl+C) is noted against that dialog, asked or not
yet: the question asked with the same words, the program being slow to
draw after the key, gives way to it once the dialog is gone. The words are
bounded as a message is; longer ones end with "…". Codex's built-in entry has its
command approval (Codex 0.161: "Would you like to run the following
command?", its reason, `$ <command>`, then "1. Yes, proceed (y)", "2. Yes,
and don't ask again for commands that start with `<prefix>` (p)", "3. No,
and tell Codex what to do differently (esc)", then "Press enter to confirm
or esc to cancel"): found by its choices' keys and its footer on one of the
last rows shown (the dialog is the bottom of what Codex draws, so its words
in a file on the screen do not count), its words the heading and the
command from its first `$ ` row to the first choice, every row of it, its
answers `y`, `p` and Escape, each of which picks at once (an Enter picks the
highlighted Yes); the same dialog with "No, continue without running it
(d)" is offered `y`, `d` and Escape (Cancel), the one without the "don't ask
again" choice Yes and No alone, and any other of Codex's approvals with the
same footer (network access: "Do you want to approve network access to
…?", "Yes, just this once (y)") its `y` and Escape.

A question with `answersFromScreen: true` (only with `anytime`, and no
`answers` of its own) has its answers read off the screen each time it is
asked: the numbered list its dialog draws at the bottom, from the last row
numbered N up through the rows numbered N−1 to 1 at that column, at most 8
rows apart (a choice's description, a rule, the rest of a long label), each
choice its first row's label and its number as the key (no Enter). Its
words, without a `words` pattern, are the rows above the list up to the
dialog's top rule (a row of `─` alone), rules within it left out. A screen
its prompt finds with no such list (fewer than two choices, a number out of
turn) asks nothing. `modeAnswers`, a pattern bounded as a prompt, marks
`mode` the answers it matches, each read whole: its label with the rows
that follow it up to the next choice (a narrow screen wraps "switch to" and
"BYPASS PERMISSIONS" onto two rows). `typedAnswers`, a pattern bounded the
same way, matches the answers that take typed words, each read whole: while
the dialog's highlight stands before one, its keys type into that field,
so the question offers no choices and is answered in the terminal. A reply
to such a question is refused, nothing written, when the dialog on the
screen shows other choices than it was asked with, read whole, beyond what
a label shows (a long folder's path, cut at the label's bound) and however
the screen wrapped them. The dialog a key or a reply answered is forgotten,
and its question withdrawn when keys typed in the terminal answered it, at
the first whole frame drawn after the answer that no longer shows it,
quiet or not, each synchronized update of a read looked at, so that the
next dialog with its words (the same command asked again, a program
animating as it works between them) is a question of its own. Claude Code's built-in entry has two (Claude Code 2.1.304): a
row numbered with the highlight `❯` before it, then one of its footers as
the last thing the screen shows ("Esc to cancel · Tab to amend" under a
tool's permission, "Enter to select · … · Esc to cancel" under
AskUserQuestion's choices, "ctrl+g to edit in <editor> · <plan>" under the
plan's approval), a narrow screen wrapping the footer over at most three
rows that are neither a rule nor the prompt's; and AskUserQuestion's last
page, "Ready to submit your answers?" with its two choices the last rows
the screen shows. Its prompt box and status line, gone while a dialog
shows, keep a dialog quoted in the transcript or typed into the prompt box
from counting, and that last page left above the prompt too. A digit picks
at once in its dialogs; the plan's "Tell Claude what to change" takes the
highlight into its feedback field (its hint reads "shift+tab to approve
with this feedback") and arrows can take it to AskUserQuestion's "Type
something." (a rule between it and "Chat about this"), fields its
`typedAnswers` finds, whose words are typed in the terminal. Its mode answers are
`(?i)auto mode|accept edits|auto-accept|bypass permissions`.

### Answering a question

A question is single-assignment, and the first valid answer wins
(`docs/plans/codex-needs-you.md`, "Who may answer a question: first
response wins", 2026-10-15). Every quick reply (a choice clicked or keyed
with 1–9, a line typed in a reply box, a choice in the chat) is sent as
`answer` with the `id` of the question the page showed as the person acted,
to an owner whose `welcome` says `answer`. The owner (the server for its
sessions, the host for its own; the same code, whatever the transport: the
server's WebSocket, WebRTC, or the relay through the switchyard) keeps each
question as one of:

- **open:** shown, nobody answering it. A reply naming it is taken: looked
  at as it arrives and again, as the last check before the session's writer
  takes its keys (a line's text, its Enter for a line with none), after every
  write queued before it. Then, in the same step, the reply *claims* the
  question: a fresh token per claim, so a claim on a question shown again
  with the same id (after a held turn, or by its turn's `done`) is never
  taken for an earlier one. A question set aside in its turn (an agent's
  own, see Attention) or held over by a held turn is not shown and takes no
  reply (`question_answered`) until it shows again.
- **claimed:** one reply's keys are being written. Every other reply is
  refused (`this question is being answered`). A claim is kept per
  question, the one shown and one held over by the held turn: a dialog
  shown over the hold takes a reply of its own, and the held-over
  question's claim stands and resolves it hidden. The claim stands while the
  write can still land: a program not reading (the keys, a line's text or
  its Enter land when it reads; a line's Enter had no turn in time) keeps
  it, and no
  timer on the way (the session's 10 s wait for the program, a connection's
  10 s for a submission, a host's deadline, a page's 15 s for the word) ends
  a question or a claim whose write began. Only a write refused before it
  began, or a line typed whose Enter is not written for good (its link
  revoked or expired, a prompt or a startup question come in its pause),
  *releases* the claim: the question is open again, and for a stranded line
  the session's turn is held (`source:"submit"`) with the question held
  over, the claim let go in the step that shows the hold (no reply claims
  the question between the two and writes its keys after the text), so no
  other line joins the text; keys that end the hold without
  entering it (Ctrl+U) show the question again open, keys that enter it or
  pick its choice show it consumed.
- **resolved:** the claimer's last byte was written while its claim was still
  the one standing, checked under the lock that clears the question. One
  function makes that transition and no other path clears a question as
  answered: the result is recorded (`lastAnswer`, an `input` entry, the
  chat's `answered` line naming the question, and the result kept by id for
  the last 64 questions) before the empty state is broadcast, and
  `answer_ok` follows. A late or duplicate reply gets the recorded result
  (`already answered by <name>`), nothing written.
- **consumed:** keys typed in the terminal reached a question read off the
  screen (a startup question's or a dialog's answer picked: an Enter, one of
  its choices, a dialog's digit or Ctrl+C, a key its rows name), or the hold
  of the turn ended with the held text entered, or the screen drew again the
  words of the question just answered before the program moved on (its
  status, title, hook or a report saying it works or is done ends that: a
  dialog with the same words after it is a fresh question). It takes
  no reply (`answered in the terminal`), holds no claim, resolves nothing,
  shows as `answered:"terminal"` the while (Attention), and goes once the
  screen shows it gone or the next question takes its place. Only an open
  question is consumed: one claimed keeps its claim.
- **withdrawn:** the agent moved on (its report, its clear, its next
  question, its work in a new turn, the done rules), the screen moved on (a
  startup question or a dialog no longer on a whole frame of the screen, a
  bell's wait past the snapshot's threshold), or the session ended. Nothing
  is recorded; a claim on it resolves nothing, and its reply's word is
  `answer_ok` when its keys were written (the attention says the question
  went) or the refusal otherwise.

Keys typed in the terminal, a line that names no question (`submit` without
`question`, a chat sent to the agent, Conductor's own lines: a crew's prompt,
a handoff, a broadcast) and a terminal's own reports resolve nothing: they
consume a question on the screen as above, an Enter among them withdraws a
bell's, an OSC notification's or the pattern's wait, a person's keys withdraw
the agent's own question set aside, and a question the agent reported waits
for the agent to move on or for a reply naming it. Nobody is recorded as
having answered in the terminal.

A reply to a question read off the screen is taken only while the screen
still shows that question, too (a next question drawn after it and not asked
yet, or none, refuses it), for its keys reach whatever the screen shows: a 1
meant for Codex's background-server question never reaches its update
question, where 1 runs `npm install -g @openai/codex`. A look of the startup
questions' watcher that took the screen's text before an answer and fires
after it asks nothing (weighed under the lock that sets the question); the
startup question asked is the one drawn last in the output the watcher keeps;
keys that pick an answer of the startup question the session shows put its
screen behind and consume it, the one it shows read again as they are
written; keys that pick an answer of one drawn and not asked yet (the
watcher asks once the screen is quiet) hold it consumed by its words, asked
so, nothing put behind, until the screen shows it gone or the next takes
its place, for the program may not have read them (Codex discards what was
typed before its question's screen took input, right after drawing it; a
question asked has had its screen quiet since). They pick one of its answers
by one of its choices, an Enter, and, where a digit picks at once (one of its
choices is a digit alone), the digit of one of its numbered items as the
output the watcher keeps shows them (with them not read, its heading gone
from that output, only its choices), offered as a choice or not (Codex's
background-server question takes `2`, to restart; its hooks review, whose
digit only moves the highlight, is answered by its Enter). A startup
question answered and drawn again before another takes its place (the
program slow to move on) is asked consumed, a reply to it refused, and
withdrawn once the screen the questions asked during the work are read
from shows no startup question (the program moved on to its prompt; looked
at as a held Escape's is, below: a frame the program has finished drawing,
nothing of it lost to a window made smaller since the program last erased
the screen (one that cuts only blank cells loses nothing), as it is and as
it was laid out at least the size the screen had when the keys were
written; the window grown since counts, its rows autowrap went on from
read joined to the width they had then, since a program that erases its dialog's lines rather than
the whole screen draws nothing for a resize); one answered never keeps the next from
being asked, and nor does one still waiting that the screen has left for
another drawn after it (the program took an answer not read as one): the
question drawn last is asked with an `id` of its own, and a reply naming
the one left is refused. A dialog's rows are read from the screen as the key
is written: the run of rows that holds its last numbered line and the footer
below, so that a numbered list in the output above is not counted. Keys that
pick an answer of a dialog on the screen that the session has not asked yet
(it asks once the screen is quiet) consume that dialog, also while the
session still shows the one answered before it. Keys written at once (a
terminal's read, the host's own terminal forwards what it reads) are each a
key of the write: a batch of an arrow and a digit picks by its digit. A key
that is not surely a key, an Escape that ends a write (it may be the Escape
key and may be the start of what the next write goes on with: a report's
rest, an Alt chord), counts for what refuses a reply, never for what clears
a question or puts its screen behind: a startup question it may answer is
held consumed until the screen shows it gone (a whole frame, after output fed
since the Escape, at least as large as the screen was), a key surely
answers it, or the next startup question takes its place; a page's Escape
sent as a reply is a structured answer, and resolves the question as it is
written. Keys that answered a dialog hold the next look at the screen until
output comes or the end of a quiet period, so the look before a submission
does not ask the answered dialog again before the program has drawn what
follows; a dialog with other words drawn meanwhile is asked, and a reply to
it taken, while the one the session still shows, consumed, waits for the
screen's look to withdraw it.

## Events

An event is an activity entry that an agent reports about its work, next to
the entries the session records itself (`attention`, `input`, `join`, `leave`,
`link`, `status`). All thirteen types travel as `activity` control messages,
live and in the replay after `ready`. The seven event types:

| `type` | Meaning | Fields |
|---|---|---|
| `progress` | a step finished | `message` says which |
| `artifact` | something was produced | `url` points at it, `message` describes it |
| `handoff` | work passed to another member | `to` names them, `message` says why; in a crew run it is typed into that member (see Crew runs) |
| `tool_use` | the agent ran a tool | `tool` names it |
| `tool_denied` | a tool call was refused | `tool` names it |
| `error` | the agent hit an error | `message` says what, `tool` names the tool involved |
| `file` | the agent read, edited, wrote or deleted a file (design 4e) | `op` is `read`, `edit`, `write` or `delete`; `path` the file as the agent named it (≤ 1024 bytes); `tool` the tool. The hook mappers yield one per file a tool call touched (Claude Code's Read, Edit, MultiEdit, NotebookEdit and Write; Codex's apply_patch, each file of the patch in `tool_input.command`; Copilot's view, edit and create by `toolArgs.path`; Antigravity's view_file, write_to_file, replace_file_content, multi_replace_file_content, edit_file and delete_file; Goose's write and edit by `tool_input.path`, relative to `working_dir`; Cursor's afterFileEdit), and a shell tool's command (Claude Code's and Codex's Bash, Copilot's bash, Antigravity's run_command, Goose's shell) the files it plainly reads (cat, head, tail, nl, bat, less, sed -n, `<`) or writes (`>`, `&>`, tee) or edits (`>>`, tee -a, sed -i), no command substitution or variable guessed at, only files that exist, at most 32; payloads verified against live runs of Codex 0.161, Copilot CLI 1.0.91, Antigravity 1.2.14 and Goose 1.54.0 (`internal/notify/testdata`); `conductor notify --event file --op … --path …` and the MCP `report` tool for the rest. A repeat of the newest file entry (the same path and op within 3 s) moves its time instead of adding a line. Files seen by git (round 13): a server or `conductor host` session in a git work tree snapshots `git status` (porcelain, untracked files listed) when it starts and looks again 1.5 s after a `tool_use` or `attention` entry (a burst is one look); each file whose status, size or time moved since the last look, and that no other `file` entry named meanwhile, becomes a `file` entry with `tool` `git` and `byName` `agent` (`write` for a new untracked or added file, `delete` for one gone, `edit` otherwise; a file that left the list is `edit` when its time moved and nothing when only committed), at most 32 a look, its path relative to the working directory when inside it; what was dirty before the session started is not reported, and reads cannot be seen this way |

The reporter chooses which fields an event carries; none is required.

Limits, applied to every entry as the session stores it: control characters
are dropped and surrounding space is trimmed. `message` is at most 500 bytes
and `tool` at most 100; both keep line breaks and tabs. `url` (at most 2048
bytes), `to` and `byName` (at most 40 runes each) are single lines. `url`,
`to`, `tool` and `byName` are cut at a character boundary. The session stores
`url` as text and does not look at its scheme, so a client links it only when
it is `http:` or `https:`. JSON writes `&`, `<` and `>` as six bytes each, so
a URL made of them could push the message past the 8 KiB CONTROL limit, which
a relay rejects: such a URL is dropped and the rest of the entry kept.

Each session records at most 20 entries a second on average and 40 at once (a
token bucket that refills continuously). An entry beyond that is not stored or
broadcast; the session counts it and logs the first drop and every 100th at
debug level. `join`, `leave`, `input`, `link` and `status` entries, which the
session and the server produce themselves, skip the limit, so a chatty hook
cannot crowd out the roster or the final `status` row. So does the
`attention` entry of a change of attention state the session applied: a
report through the API paid its token before it changed anything (below), and
the bell, an OSC notification and the screen pattern are the session's own
observations, which spend none. A state that shows always has its entry, in
the log, on the stream and at the webhooks, however many tool events came
before it. That entry's `at` is the change's `since`, taken as the state is
set, so no one sees the state before the time its entry gives.

Reporting: `POST /api/sessions/{id}/events` with `Authorization: Bearer
<agent token>` (or the workbench token) and the body `{type, message?, url?, to?,
tool?, kind?, options?, ask?}`. `type` is one of the seven event types or an
attention word: `needs_input`, `working`, `done` or `clear`. An attention word
is applied exactly as `POST /api/sessions/{id}/attention` applies it, with the
same checks, `kind`, `options`, `ask` and effect, and the same `source` (`api` for the
agent token, `admin` for the admin); only the reply differs. The entries a
session records itself (`attention`, `input`, `join`, `leave`, `link`,
`status`) cannot be reported: they skip the limit above, and only the session
may make them. The reply is `202 {"accepted": true}`, and the entry has
`byName` `agent`. Errors: `400 invalid_type`, `400 invalid_request` (a body
that is not one JSON object of known fields, and for the attention words the
checks of `/attention`), `400 invalid_kind`, `401 unauthorized`, `404
not_found`, `409 session_ended` and `429 rate_limited`, which has two sources.
One is the session's bucket, when it has no token left (below). The other is the
per-client limiter that the API's credential checks share: a client refused with
`401` or `404` 20 times in quick succession (and 5 times a second after that) is
answered `429` in place of the next refusal.

A server session spends a token of its bucket on each event it records and
on each attention word, by this route or by `/attention`, before the word
changes anything: with no token the answer is `429 rate_limited` and neither
the state nor an entry is left behind. A hosted session keeps no log on the
server, but the server holds a bucket for it, of the same size (20 a second,
40 at once), and spends a token on every event and every attention word it
sends on to the host, by this route or by `/attention`: the host's connection
also carries its viewers' input, and closes when its queue is full, so a flood
of reports must not reach it. With no token the answer is `429 rate_limited`
and nothing is sent or changed. Every attention word through the routes spends
one, its host connected or not; an event needs a connected host (below) and
spends nothing without one. The host's own reports spend none. The host
applies an attention word it is sent without a token of its own and records
its entry, as a server session does. An event the server sends to the host
(see Host control connection) is answered `202` once it is on its way. The
server cannot know whether the host's own bucket takes it, so an event the
host drops is dropped without a word to the caller; `409 host_disconnected`
says that no host is connected.

`conductor notify --event <type> [--message M] [--url U] [--to T] [--tool N]`
sends an event from inside a session: it turns the `CONDUCTOR_NOTIFY_URL` of
the session (`…/attention`) into `…/events`. `--event` cannot be combined with
`--state`, `--codex` or a `--<agent>-hook` flag, and `--url`, `--to` and
`--tool` are refused without it: nothing else carries them. A mistake exits 2,
or 1 in a hook's command line, where 2 would block the agent. An attention
report (the state flags, `--event` with an attention word, or a hook mapped to
one) that the server answers `429` is tried again after 0.1, 0.25, 0.5, 1 and
2 s, within the 5 s the command takes at most. An event is tried once.

Streaming: every entry a session records, whether an agent reported it or the
session made it, reaches admins as an `activity` event on `GET /api/events`,
next to `session` and `removed`:
`data: {"sessionId", "at", "type", "by"?, "byName"?, "message"?, "url"?, "to"?, "tool"?, "state"?}`,
where `state` is, for an attention entry, the state it records (`needs_input`,
`working` or `done`), absent for other entries and for an attention entry from
a host that does not send one. Entries arrive as the sessions record them, so
a few can be out of order; `at` says when each happened. The feed is live, not
a log: a client that has fallen behind (its queue is three quarters full)
misses entries and keeps its stream, with room left for the `session` and
`removed` events it cannot do without. The session's own `activity` control
messages replay the log.

Webhooks: the server also POSTs every entry whose event type a configured
webhook lists (README, Webhooks) to its URL, one entry a request, with the
body `{"sessionId", "session": {"id", "name", "agentId"}, "entry": {…}}`, the
entry as the stream above sends it less `sessionId` (beside it in the body)
and `state`, which the body does not carry: an attention entry's state reaches
a webhook that lists it as `X-Conductor-Event` (below). The headers are
`Content-Type: application/json`, `X-Conductor-Event` and, for a webhook with
a secret, `X-Conductor-Signature: sha256=<hex HMAC-SHA256 of the body>`.
`X-Conductor-Event` is the type the webhook listed: an entry type, or the
Events page's name for the entry, which wins when both are listed. An
attention entry is the attention state it records, which comes with the entry:
a server session hands it on as it records the entry (the state set with the
entry's stamp), and a hosted session's host sends it (`state` of the host's
`activity` message).
An entry that comes without one is the state it names as its message, if it
names one (as it does for a report without a message), and otherwise only an
`attention` entry. That happens for two reasons: the host is older and sends no
state, or it sent a state the server does not take (anything but `needs_input`,
`working` or `done`), which the server drops. A `status` entry
`exited (exit N)` with N other than 0 is `exit_nonzero`. Each webhook queues
at most 256 entries and drops its oldest; a delivery is tried once, for at
most 5 s, without following redirects.

## File reads

`file_get` resolves `path` against the session working directory (`~` expands to
the owner's home). After symlink resolution the target must stay inside the
working directory, and `.git/objects` is never readable. The working
directory is opened once, when the session starts, and every read, listing,
find and save goes through it: the path checked, it is opened from that
directory one element at a time, following no link (the check resolved
them), so an element that became a link or another file since the check is
refused (`denied`, "the path changed while it was opened; try again"), and
nothing outside the working directory is reached whatever a link does
meanwhile. The rules below are applied again to what is opened, by identity.
When the session's path comes to name another directory (the folder moved,
or a link put in its place), its files, git reads included, are refused
(`denied`, "the session's folder is no longer at its path"). Server sessions also
refuse the server's data directory (`dataDir`, which holds agent secrets) with
everything in it, its config file (admin and host tokens) and its catalog file
(`catalogPath`), and the editor and backup copies beside those two: any file or
directory in the same directory (and in the target's directory, when either path
is a symbolic link) whose name contains theirs, ignoring case as a
case-insensitive file system does (`conductor.json.bak`, `conductor.json~`,
`.conductor.json.swp`, `#conductor.json#`), even inside the working directory;
and what the other Conductors on the machine keep: `~/.conductor`, the desktop
app's own directory (`conductor-desktop` and `Conductor` in the user's config
directory, and the one `CONDUCTOR_DESKTOP_DIR` names, which the app sets for
the server it starts: on Windows its folder in the Windows profile, under
`/mnt` inside WSL), its settings file as a config file, the data directory
those settings name and `~/.local/share/conductor/data`, looked up again at
each request and kept once named, so a data directory the app moves to while
the server runs is refused from then on. The config file, the
catalog file and the settings file are matched as files, so a link to them is
refused as well; the copies are matched by name alone. One of these paths that is not on
disk (a data directory never made, a config file moved away) is matched by its
path instead: below the deepest directory on it that is there, compared as a
file, its names compared ignoring case, so that what a revision keeps there
(`op: show`) and a file event naming it are refused as well. Every one of
these rules answers `denied`. A hosted session applies them all for the server on its own machine:
the data directory `CONDUCTOR_DATA_DIR` names or the server would choose, and
`~/.conductor` in any case; the catalog file `CONDUCTOR_CATALOG_PATH` names;
the config file `conductor host --server-config` names, with its `dataDir`
and `catalogPath`; and for the desktop app's server, the app's own directory,
its settings file as a config file (its link target and the copies there
included), the data directory its settings name and, inside WSL,
`~/.local/share/conductor/data`. The host looks these up again at each
request and keeps refusing what it refused before, so a data directory moved
while the session runs is refused at both places. While one of these paths
is relative, or the config file or the desktop app's settings cannot be read,
every file is refused (and the host does not start). Saves, and opening a file
in the editor's Neovim, go through the same rules.
Responses carry a JSON header
`{reqId, path, kind:"file"|"dir"|"error", size, truncated, binary, mime, exists, entries?, error?, sha256?, mtime?, readOnly?}`
followed by the bytes for text files: at most 983,027 in a FILE frame (1 MiB,
less the largest header of 64 KiB and the frame's 13 bytes, so that the frame
fits the client's 1 MiB queue whatever its header), 1 MiB through
`GET /api/sessions/{id}/files`; a longer file is cut there and marked
`truncated`. `sha256` (hex) and `mtime` (RFC 3339) come with a file read
whole, for a save to tell the file changed since; `readOnly` marks a file in
a repository's `.git` (the directory, a linked worktree's or a submodule's
`.git` file, the git directory such a file or a `.git` link names, a bare
repository), which reads as any other but is never saved. Files with a NUL byte in the
first 8 KiB are reported `binary` without bytes; images are sent as bytes. A
directory lists at most 2,000 entries, directories first, then by name, of
the first 20,000 it reads (`truncated` when there are more). Up to
four requests may be in flight per client. `stat:true` returns only the header.
A link's viewer whose link no longer opens the session is answered with a
FILE error of code `revoked` (or `expired`), nothing read (see Control
messages, the share link's check).

The `fileView` server setting decides who may read: `view` (both roles, the
default), `control` (controllers only) or `off`; `conductor host --file-view`
does the same for a hosted session. A view link is not given a repository's
`.git` nor git's settings and credentials, which often hold access tokens (a
remote URL with one in it, an extra header, a credential store): nothing of a
repository's `.git`, however the path reaches it (the rules that make it read
only: a `.git` element, a linked worktree's or a submodule's `.git` file, the
git directory one names, a bare repository, a file of the working tree a
metadata link leads to, a hard link to an entry); anywhere, a name that
contains `git-credentials`, a `.gitconfig` or `gitconfig` and its copies; and
git's settings and credentials in the home (`~/.gitconfig`,
`~/.git-credentials`, `config` and `credentials` in `~/.config/git` and in
`$XDG_CONFIG_HOME/git`), held as the deny list's files are: as files, so the
file a link of theirs leads to is held under any name, with the copies beside
it, and by their paths where they are not there. Names are compared ignoring
case, at the path asked for and at its link's target. What the repositories
beside the path stood for at its check is held to each folder and to the file
as they are opened, as a save is, so metadata put in the path's place since is
refused too. Such a read, whole or `stat`, or a `show` of one at a revision,
answers `file_denied` ("a repository's .git and git's settings and credentials
are for controllers"), 403 over HTTP; a controller reads them as before.

**The plans directory (#289).** An agent that keeps its plans outside the
project (Claude Code: `~/.claude/plans/<slug>.md`, or `plans` under
`CLAUDE_CONFIG_DIR`, as its adapter resolves it from the environment the
process was given, never from a page) has that one directory opened as a
second root beside the working directory, the first time the agent reports a
file in it: the files the agent reports there, through its hooks' `file`
events (a delete forgets one), the `plan` its approval names (the attention
route) or the path its dialog prints, are the session's plans, and a
`file_get`, a `file_write` or an `nvim_open` of one goes through that root
under every check above (the deny list, a link out of the directory refused
once resolved, a view link held to a controller's files): a request whose
path leads into the directory is routed to that root, which allows the file
the path resolves to only when it is one of the plans, and then opens that
file element by element with no link followed, so a link made to lead
elsewhere between the two reaches nothing it was not allowed. Any other path in
the directory (another session's plan), and the rest of the agent's config
directory (its credentials, every project's history), are refused as any
outside path is. Only an admin or the holder of the session's agent token
reports file events and plans. A hosted session's host does the same for its
own agent.

## File saves

The editor saves a file (design round 12, F6) with FILE_WRITE frames, the
FILE frame's layout in the other direction: the header
`{reqId, path, offset, total, baseSha256?, force?}` and the bytes
`[offset, offset+len)` of a file of `total` bytes (at most 1 MiB), in parts of
at most 32 KiB (under a data channel's message cap), in order, one save at a
time per connection; a save's next part is waited for however long it takes
(a relayed save's parts can be slowed on their way), and an unfinished save
goes when the connection's next save starts, or with the connection. A part
that cannot be read (a header past 4 KiB, or one that does not parse) names
no save to answer and is a protocol error: the owner sends
`error{bad_frame, "malformed file write"}` and closes the connection (1002),
a host letting the viewer go with that error as it does one whose hello
cannot be read (a paste invite's viewer, which no server serves, is told on
its data channel and the channel closed); no client of Conductor's sends
one (the editor refuses such a part before sending it).
When the last part arrives the owner resolves `path` as a read does (the same
deny list), and refuses with a FILE frame of kind `error` and code
`changed_on_disk` when the file's sha256 is not `baseSha256` (the read's),
unless `force`: the header then carries the file's `sha256`, `mtime` and, from
the newest file event that changed it, `by`, `tool` and `at`; a file deleted
since is the same code with `exists: false`. Otherwise it writes the file
atomically (a temporary file beside it, its mode kept, a rename, both in the
file's directory as opened from the working directory) and answers
kind `written` with the new `sha256`, `mtime` and `size`; the save is a `file`
event `write` by the person with tool `editor`. Other refusals: `read_only`
(not a controller, the session's `fileEdit` is `off`, or the file is in a
repository's `.git`, however the path reaches it, a folder on its way
being a git directory by its own files as it is opened included), `denied`,
`too_large`, `out_of_order`, `write`, and `revoked` or `expired` for a
link's viewer whose link no longer opens the session: asked at each part,
after the role, the save under way dropped (none of its later parts is taken
as its continuation: `out_of_order`, should the link stand again), and once
more just before the rename, after the checks, the hash and the
temporary file, which a refusal removes, the file left as it was. The
`fileEdit` setting (`control`, the
default, or `off`; `CONDUCTOR_FILE_EDIT`, `conductor host --file-edit`) is the
same that offers Neovim, and the welcome's `fileEdit` says whether this
connection may save. The switchyard drops FILE_WRITE from view-role
connections.

Since the owner answers every save, written or refused, the browser never
gives a save up while its connection lives: one given up on could still be
written, its answer lost and the editor left on the old `sha256`, so the
next save would be refused as `changed_on_disk`. After 20 s without an
answer the editor says the save goes on (a relayed save can be slowed on
its way: the relay's bounds, a slow link, a host that reads slowly), and the
answer, when it lands, is taken as ever; a connection that closes ends the
save with an error. The answer marks saved the text that was sent: what was
typed meanwhile stays, unsaved, and a tab in front by then keeps its own
changes. Since every save sent is waited for, the browser sends none the
owner could not read: one whose part's header would pass 4 KiB (a very long
path) is refused before anything is sent. A file request still gives up
after 20 s.

## HTTP API

On a switchyard (`switchyard.enabled`, `conductor switchyard`) every route
that launches, edits or lists what the server launches, the sessions'
creation, resume and rename, the catalog, the crews, the runs, the paths, the git
check, the integrations, an agent's self-service crew routes and Dispatch's
routes (a switchyard keeps no tickets, and opens no Dispatch store), answers
`403 {error: {code: "switchyard"}}`; the rest, hosted sessions, links,
`/api/join`, the events stream, health, reach and whoami, is as below. A
switchyard serves no workbench either: `GET /` is its landing page, every
workbench path is its 404 page (both server-rendered HTML), and the app is
served only under `/join/` and `/paste` and for its assets.

Every `/api/...` route, with the credential it needs. Admin means
`Authorization: Bearer <workbench token>`; a share token is also accepted where the
table says so. JSON request bodies are limited to 64 KiB (2 MiB on the crew
routes, 128 KiB when adding a member to a run, 192 KiB on a Dispatch
ticket's create and edit, where a body past its bound is `413 bound`) and
unknown fields are rejected; a Dispatch attachment's upload is
`multipart/form-data` of at most 16 MiB of file and 64 KiB beside it.
A query string's parameters are separated by `&` alone, as a browser reads
a form's, so a `;` is part of a value (a folder's path may hold one), sent
as it is or as `%3B`. A query string holding a `%`-escape that is not one,
or more than 10,000 parameters, is `400 invalid_request` where a route
reads its parameters, and presents no token where a route takes one from
the query; on `GET /ws/host`, where no token is an open host's
registration, it is `400 invalid_request`.
Errors are
`{"error":{"code","message"}}`, with more fields where the table says so. The
WebSocket routes, `GET /ws/sessions/{id}` and `GET /ws/host`, are described
above. A run link's token is a share token on the session of every member of
its run, and on no other.

| Route | Auth | Purpose |
|---|---|---|
| `GET /api/health` | none | liveness: `ok`, `version`, `commit`, `sessions`, `instance` (a random id of this server process, which the reach self-check matches); `switchyard` (whether this server is one) and `relay` (whether it relays hosted sessions: always on a plain server) |
| `GET /api/reach` | admin | what the server knows about being reached from outside its network (`reach.mode`): `{mode, mapped, method?, publicUrl?, externalIp?, externalPort?, listenPort?, expiresAt?, renewedAt?, verified?, error?, tls?}`; `mapped` says a port on the public address reaches the server (`method` `upnp`, `pcp` or `nat-pmp`; `manual` with `mapped` false names a port the person forwarded); `publicUrl` is `https://<externalIp>[:port]`, the base share links take once `tls.ready`; `verified` is `ok` when the server reached itself through that URL and `unverified` when it did not, which is usual from inside the network and proves nothing; `error` is the last failure in words; `tls` is the TLS listener's certificate, absent without a TLS listener: `{mode (acme or files), identifiers?, challenge?, notAfter?, renewAt?, ready, lastError?, nextTry?}`, `ready` saying a certificate is served (the discovered address becomes a link's base only then) |
| `GET /.well-known/acme-challenge/{token}` | none | the key authorization of an open ACME `http-01` order (RFC 8555 §8.3), on the plain listener; `404` for any other token, a token over 128 bytes, or without a TLS listener |
| `GET /api/switchyard/status` | admin | on a switchyard only (`404` elsewhere): the landing page's operator figures, `{hosts, sessions, viewers, relayBytesHour, hostList: [{name, since, sessions, viewers}]}`, the live hosted sessions grouped by the publishing host, earliest first, and the bytes the relay carried in the last sixty minutes |
| `GET /api/whoami` | admin | OS user running the server (the default display name); `host` is the machine's name (`""` when it cannot tell) and `switchyard` whether this server launches nothing; `home` the server user's home directory, when it has one (the settings picker's Home); `version` the server's version, as `conductor version` prints it (Settings → The app) |
| `GET /api/catalog` | admin | `{agents, hidden, yoloDefault}`: the launchable agents, `env` values masked as `***`, and the IDs hidden from the catalog; each agent, here and in the replies of the catalog routes below, also carries `source` (`built-in`, `config` or `saved`) and, for a saved agent that replaces a built-in or configured one, `replaces` (where that one came from); `available`, whether `command[0]` resolves on the server (the check of `POST /api/catalog/check`; any program of that name on the server's `PATH` counts, and a relative `command[0]` with a path separator counts without a lookup, since it resolves in the session's directory, which the server's own cannot stand for; the answer is kept 30 s per program, and the programs with no fresh answer are looked up together, at most 8 lookups at a time across all requests; a lookup that does not answer within 2 s (a `PATH` entry on a mount that stalls), or before the client goes away, counts as available, so the launch of such an agent is allowed and its spawn fails if the program is missing, and the lookup's answer is kept when it lands); and `site` when the agent has a website (an `https` URL: a built-in's, or the saved agent's); each agent also carries, when it has them, its `yolo` recipe `{args?, env?}` (its `env` values shown as they are: switches, not secrets), its `trustPrompt` and its `session` recipe `{startArgs?, newId?, idFrom?, idPolicy?, resumeArgs?, idPattern?, resumeNeedsCwd?}` (limits below); `yoloDefault` is the server's `yolo` setting, which a launch or a crew without `yolo` follows; each agent whose adapter has an identity probe (the real agent's `--version` line, checked against what it prints: Claude Code and Codex verified live, the others from their docs until the nightly recipes job confirms them) and whose program the lookup found at an absolute path also carries `identity` `{ran, pending?, identified, impostor?, verified, name?, version?, output?, error?}`: the program on the server run with its version flag (argv only, a spartan environment, 3 s, 4 KiB), its answer kept 10 min; `identified` with `version` when it is the agent, `impostor` when it is a known other program of that name (the Go migrations tool called goose; the agent's `available` is then `false`: it is not offered, and `POST /api/sessions` refuses it), `output` the first line it printed otherwise, `pending` when the probe is still under way (ask again), `verified` whether the adapter's expectation was checked against the real CLI (a verified probe that matches nothing means the program is not the agent; an unverified one leaves it unidentified and refuses nothing); an agent with `probe: false` is not probed |
| `POST /api/catalog` | admin | add an agent or replace the one with the same `id` (a built-in too); body is the agent, reply `{agent}`; `source`, `replaces` and `available` in the body are ignored; an `env` value of `***` (what `GET /api/catalog` shows) keeps the value the saved entry holds for that key; for an agent that replaces a built-in or configured one, a `***` value the saved entry does not hold is stored as `***` and means "the replaced agent's value", so a change in the config reaches it, and a value equal to the replaced agent's is stored as `***` too; a saved agent that leaves out `adapter`, `signal`, `site`, `yolo`, `trustPrompt` or `session` takes those of the agent it replaces, and a `signal` without `workingTitle` the replaced agent's `workingTitle`, and `"yolo": {}` or `"session": {}` says it has none; `***` is rejected for a key neither has; `site`, when present, must be an `https://` URL with a host name, a valid port if it has one, no user info and no white space, at most 200 bytes, a rule that also holds for the config's agents and for `catalog.json`; `400 invalid_agent` carries the validation message, an unknown `adapter` or a bad `site` included (the adapter check also runs for the config's agents and for `catalog.json` at startup); `503 store_unavailable` when there is no data directory |
| `DELETE /api/catalog/{id}` | admin | remove the saved override with that `id`, which restores a built-in it replaced; an agent with no override is hidden instead; `204`, `404` when unknown |
| `POST /api/catalog/{id}/unhide` | admin | take a hidden `id` off the hidden list, which brings back the agent it hid as it was; reply `{agent}`, or `{}` when no agent has that `id` any more; `404` when the `id` is not hidden |
| `POST /api/catalog/{id}/clone` | admin | make a copy of the agent `{id}` under another id (#291): the body is the copy as `POST /api/catalog` takes it, its `id` one no agent has (listed, saved, hidden or configured; `409 id_taken` otherwise); what the body leaves out that the Agents page has no control for (`adapter`, `signal`, `cwd`, `icon`, `yolo`, `trustPrompt`, `trustAnswers`, `questions`, `session`, `probe`) is the source's as the catalog resolves it, so the copy launches as the source does, with its hooks, trust answers, startup questions and Resume; an `env` value of `***` is the source's value for that key, copied and never shown (`400 invalid_agent` for a key the source has none for); the copy is a saved agent of its own, which replaces nothing and follows nothing: a later change to the source, or to the configured agent the source follows, does not reach it; reply `201 {agent}` as `GET /api/catalog` lists it; `404` for an unknown `{id}`, `400 invalid_agent` and `503 store_unavailable` as for a save. A launch of a Claude Code copy whose environment sets `CLAUDE_CONFIG_DIR` (a Codex one `CODEX_HOME`) to an absolute path also puts the Conductor skill under that directory (`skills/conductor/SKILL.md`, the directory made when missing), once per directory since the server started, as it does under the home |
| `POST /api/catalog/check` | admin | body `{command, adapter?, env?}`, reply `{found, path?, unknown?, identity?}` (`identity` as `GET /api/catalog` carries it, when `adapter` names an adapter with a probe and the program was found: the probe runs now on the program found, with `env` as the agent's variables; `400 invalid_request` for an unknown adapter): whether `command[0]` resolves on the server (`exec.LookPath`), asked now and kept 30 s for the `available` of `GET /api/catalog`; nothing is run, and a missing program is `found:false`, not an error; `unknown` (with `found:false`) says the server did not judge the program, which `available` counts as installed: `relative` for a relative `command[0]` with a path separator, resolved at launch in the session's directory and not looked up here, and `timeout` when the lookup does not answer within 2 s (its answer is kept when it lands) |
| `GET /api/paths` | admin | `scope` (optional): `roots` (the default, everything below) or `any`, which lists any directory the server's user can stat, a symbolic link to any directory included, the stat still first; `any` only with `paths.browse: any` (the desktop app sets it for its own server, for its settings picker), else `403 browse_off`; another value `400 invalid_request`. `prefix` (≤ 4096 bytes, no NUL; a relative one resolves against the server's own working directory, as a session's `cwd` does) and `limit` (a positive integer, default 50, at most 50: more counts as 50) in the query; reply `{dir, entries, truncated, more?, moreUnknown?}`: `dir` is the longest leading part of `prefix` that is an existing directory under an allowed root (symbolic links resolved, as a session's `cwd` is checked; the server's default working directory for an empty `prefix`), `entries` its child directories whose names start with the path element typed after `dir` (all of them when `prefix` names a directory), in name order, at most `limit` of the first 2000 entries the directory gives, hidden ones only when that element starts with a dot (a lone `.` after the last separator included), a symbolic link only when it leads to a directory under an allowed root, each `{name, path, git: {repo, commits}}` (`repo`: in a git working tree; `commits`: its `HEAD` is a commit; a child with no `.git` of its own carries its parent's marks, a symbolic link its target's; neither without `git` on the server; git is asked at most once per entry, one call at a time), `truncated` when more match than `limit`, when the directory has more than 2000 entries (the rest are not read), when the listing stopped at a symbolic link (the one after 50 that led nowhere usable, or any once the deadline has passed), or when the deadline passed before every entry was marked; `more` how many directories matching the typed element were left out past `limit` (round 14: the pickers say "30 more folders here"), and `moreUnknown` that there may be others besides (more than 2000 entries, or the listing stopped at a link); the 5 s deadline covers the whole request, and when it passes after the directory is found the listing keeps its entries, the git marks not yet asked for left false and `truncated` set, which is not an error; a directory the server cannot read lists nothing (`200`, no entries); the listing and the git check below refuse a chain of symbolic links longer than the kernel follows (about 40), which a launch's own resolution of a `cwd` would still accept; `400 invalid_cwd` when no part of `prefix` is such a directory, its message naming the allowed roots (`no directory in the prefix is under the allowed roots: <root>, <root>`), which the picker shows as it is; `400 invalid_request` for a bad `prefix` or `limit`; `500 list_failed` when the deadline passes before the directory is found (a prefix of very many elements through symbolic links); the path asked about is logged at debug level only |
| `GET /api/git/check` | admin | `cwd` in the query (≤ 4096 bytes, no NUL; the server's default working directory when empty; resolved as the listing resolves it); reply `{inRepo, toplevel?, hasCommit, message}` by the rules a launch with `isolation: worktree` applies (`git -C <cwd> rev-parse`, under the allowed roots): `inRepo` when `cwd` is in a git working tree, `toplevel` its top, left out when it is not under an allowed root (a root may lie inside a repository, and a repository's `core.worktree` can name any directory), `hasCommit` when `HEAD` is a commit, `message` the words the launch's refusal would use, or that worktrees can be made: that `cwd` is in no repository, or in one with no commit, or that `git` is not installed (then `inRepo:false`), or, for a repository git refuses (owned by another user, unreadable), `git cannot use the working directory's repository:` and git's first line of stderr, which may name a configuration file or a repository path outside the roots; in a repository with a commit, a `.conductor` or `.conductor/worktrees` in `cwd` that is a symbolic link, which the launch refuses first, answers `inRepo:true, hasCommit:true` with that refusal in `message`, so `message` is the verdict, not the two booleans; a preview: the launch's own refusal stays the authority (`409 not_a_repo`, or `400 invalid_crew` for the symbolic link); `400 invalid_cwd` as for a session; `500 git_failed` when git does not answer within 5 s (another failure to run it answers `200` with git's message) |
| `GET /api/crews` | admin | `{crews, total}`: a page of the saved crews' summaries ordered by name (ignoring case), then id, each `{id, name, cwd, where, isolation, members: [{name, agentId, start}], updatedAt}`; `?offset=` from 0 (default 0), `?limit=` 1 to 500 (default 100), `400 invalid_request` otherwise; `total` counts every crew; `{crews: [], total: 0}` without a data directory |
| `GET /api/crews/{id}` | admin | one crew in full, `{crew}`, shaped as before: `{id, name, goal, cwd, where, isolation, openAfterLaunch, viewLinkTtlSeconds?, tasks?, setup?, manifest?, manifestRevision?, yolo?, members, createdAt, updatedAt}` (`yolo` absent: the server's default; `true` or `false`: the crew's own choice; `tasks`, `setup` and `manifest` as the crew limits below have them; `manifestRevision` the server's count of the crew's project manifest changes, absent while it is 0), a member being `{name, agentId, prompt, args?, start: {when, member?}}`; `404` when unknown; `409 crew_unreadable` when its file cannot be used, the message saying why; `503 store_unavailable` |
| `POST /api/crews` | admin | create a crew: the body is a crew without `id`, `createdAt`, `updatedAt` and `manifestRevision`, which the server sets and rejects like any unknown field (`manifestRevision` 1 for a crew with a project manifest or a `setup`, absent for one without); the `id` comes from the name (lower case, every other run of characters a `-`, at most 40 characters, `crew` when nothing is left), then `-2`, `-3`… when taken; reply `201 {crew}`; `400 invalid_crew` carries the validation message, an agent the catalog does not have included; `503 store_unavailable` when there is no data directory |
| `POST /api/crews/examples` | admin | seed the example crews (`example-todo-app`, `example-todo-app-tasks`, `example-test-fixer`, `example-docs-writer`, `example-dependency-upgrade`), as `conductor serve --examples` (or `CONDUCTOR_EXAMPLES` set to `1` or `true`) does at startup: each is saved unless an entry named `<id>.json` exists in `crews/`, usable or not (a symbolic link included), which is left alone whatever it holds; reply `200 {added, skipped}`, the ids each way in that order, each a list (`[]` when none); their `cwd` is the server's default working directory, their members use the `claude` and `codex` built-ins (not checked against the catalog: the launch checks), `where` is `server` and `isolation` `worktree`; each member that starts `after` another has a role prompt that begins by merging that member's branch, `crew/$CONDUCTOR_RUN/<member>`; `example-todo-app-tasks` has tasks instead of start rules (every member `start.when` `task`); `503 store_unavailable` without a data directory; `500 store_failed` when the directory cannot be read or a crew cannot be saved (the ones saved before it stay) |
| `PUT /api/crews/{id}` | admin | replace a crew's fields with the body, shaped as for create, so a crew as `GET` answers it goes back without `id`, `createdAt`, `updatedAt` and `manifestRevision`; `id` and `createdAt` never change, `updatedAt` is now, and `manifestRevision` is one more when the project manifest, a `setup` included, changed (a removal too) and stays as it was otherwise; reply `{crew}`; `400 invalid_crew` as for create; `404` when unknown; `409 crew_unreadable` when its file cannot be used (it is not replaced) |
| `DELETE /api/crews/{id}` | admin | delete a crew; `204`, `404` when unknown |
| `POST /api/crews/{id}/duplicate` | admin | save a copy of a crew as `<id>-copy` (then `<id>-copy-2`…) named `<name> copy`, with new times and the `manifestRevision` of a new crew (1 with a project manifest or a `setup`, absent without), whatever the original's; reply `201 {crew}`; `400 invalid_crew` when one of its agents is no longer in the catalog or the copy would be over 1 MiB; `404` when unknown; `409 crew_unreadable` when its file cannot be used |
| `POST /api/crews/{id}/launch` | admin | launch a crew as a run (see Crew runs); an optional body `{label}` names the run (at most 60 characters, no control characters, trimmed; kept on the run as `label` and by a resume; `400 invalid_request` for a bad label or an unknown field, the body at most 4 KiB); reply `201 {run}` once the session of every member that starts immediately exists, each member `starting` until its prompt is typed; the run's `yolo` is the crew's, else the server's, fixed for every member of the run, one started or added later included; a crew with `viewLinkTtlSeconds` also gets a run link with role `view`, label `launch` and that lifetime, created as `POST /api/runs/{run}/links` would (noted in the run log), and the reply is `201 {run, viewLink: {link, token, url}}`: the token is in this reply only (a link the store refuses leaves the launch standing, without `viewLink`, with an `error` entry in the run log); `400 invalid_crew` for a crew with no members, one that runs on a host, or, with the file form, one whose `conductor.manifest.json` at the run's base (`HEAD` of `cwd`, read once) is missing or broken (the file and the commit named), an agent the catalog does not have, an agent whose program is not installed on the server (`command[0]` does not resolve, by the check of `GET /api/catalog`'s `available`, so a lookup that does not answer within 2 s does not refuse; the message names the member and the agent), arguments to an agent that takes none, or with `isolation: worktree` a `.conductor` or `.conductor/worktrees` in `cwd` that is a symbolic link, or a symbolic link on the way to a member's directory in its worktree; `400 invalid_cwd` as for a session; `409 not_a_repo` with `isolation: worktree` when `cwd` is in no git working tree (`git -C <cwd> rev-parse --show-toplevel` fails), or in one whose `HEAD` is no commit, each with its own message, and a `rev-parse` failure other than 'not a git repository' (a repository owned by another user, or one that cannot be read) says what git said; `500 launch_failed` with `git is not installed on the server` with `isolation: worktree` when `git` is not on the server's `PATH`, before anything is made; `409 run_stopped` when the run is stopped while its sessions start (the run stays, stopped); a member whose session cannot be created answers as `POST /api/sessions` would, naming the member; `500 launch_failed` otherwise; `404` when unknown; `409 crew_unreadable` when its file cannot be used; `503 store_unavailable` without a data directory; `400 invalid_crew` also for a member whose agent's program the identity probe found to be another one (a known impostor, or a verified probe that matched nothing), the message naming the member, the agent, what the program is not and what it printed; a pending or failed probe refuses nothing |
| `GET /api/crews/{id}/runs` | admin | `{runs, live}`: the crew's runs, newest first, each as `GET /api/runs/{run}` answers it (without diffs): the `live` first ones the server holds, then the records of runs the server no longer holds, saved under `runs/<id>.json` in the data directory, at most 500 kept (the oldest by start dropped), never a terminal's contents; a run's record is written from its launch on (again a couple of seconds after each change, and as it ends, a reopened run again when it ends again) and read back after a restart, where the record of a run that was running when the server last stopped is marked `interrupted` (its running members `ended` with that `error`), never relaunched on its own; `404 not_found` for a crew neither saved nor recorded |
| `POST /api/sessions/{id}/paste` | admin | answer a viewer's paste invite for a server session: `{offer, role, label?}`, `offer` a `cpi1.` blob (the viewer's SDP with every candidate gathered, deflated and base64url-encoded, ≤ 64 KiB), reply `201 {answer, role}` with the session's blob of the same form, which the viewer pastes back; the data channel then runs with no server between the two, as a host's does (hello over it, welcome, scrollback, input by role); at most 16 such peers a session, idle ones dropped after two minutes; `400 invalid_offer` for a blob that is not an offer, `400 hosted_session` for a hosted session (shared from its own machine), `409 session_ended` |
| `GET /api/ice` | none (rate-limited) | `{stun}`: the configured ICE servers' `stun:` URLs, for a viewer gathering a paste invite's offer before it has any session; a TURN server's credentials never leave the server |
| `GET /api/runs` | admin | `{runs}`: the runs in the server's memory, newest first, each with its `state` and `needsInput` (see Crew runs) |
| `GET /api/runs/{run}` | admin | `{run}` with each worktree member's `diff{added, removed}`, the run's `log`, whose entries Crew runs lists, and its `chat` (the run's chat messages, oldest first, at most 500; see Chat); for a run the server no longer holds (it ended before the server last started, or was evicted) but has a record of, `{run, record: true}` as it was last written (its `state` as recorded, `interrupted` for a run the server's end cut short), no diffs, and its page opens read-only: the run's actions (stop, members, broadcast, links) answer `404` for it as for any run not held, and `POST /api/runs/{run}/resume` resumes it from the record; `404` when unknown |
| `POST /api/runs/{run}/members` | admin | add a member mid-run: body a crew member; reply `201 {run}`, once the session of a member that starts immediately exists; `400 invalid_crew` for an invalid member, a name the run has (`the name is used twice`), a prompt over 32756 bytes with the run's goal in it, an `after` naming no member of the run, a 13th member or an agent as at launch; a member whose session cannot be created answers as at launch (`409 not_a_repo`, `500 launch_failed`), and stays in the run, `ended` with its `error`, its name taken: adding it again under that name is `400 invalid_crew`; `409 run_stopped`; `404` when unknown |
| `POST /api/runs/{run}/members/{name}/start` | admin | start a pending member by hand, whatever its start condition; reply `{run}` once its session exists, the member `starting` until its prompt is typed; a session that cannot be created answers as at launch (`409 not_a_repo`, `500 launch_failed`), the member `ended` with its `error`; `409 member_started`, `409 run_stopped`; `404` for an unknown run or member |
| `POST /api/runs/{run}/members/{name}/resume` | admin | resume an ended member of the run (no body), in its working directory, its worktree and branch kept: a session tagged with the run that resumes the member's last `agentSession` with its agent's `session` recipe when the agent has one and the session was `resumable` (`resumed: true`; no prompt is typed, its conversation has it), and otherwise a fresh one whose role prompt is typed again once it is ready (`resumed: false`, with a `notice`); the member is `starting` with the new `sessionId` until its session is ready for a prompt (a resumed conversation, whose agent takes a while to show its prompt; a trust question holding it is noted) or until that prompt is typed (a fresh one), then `running`, so a handoff typed into it meanwhile waits, queued, and is typed once it is; noted in the run log; reply `201 {session, resumed, notice?}`, the new session's `Info` with `resumedFrom`; `404` for an unknown run or member; `409 still_running` for a member that is pending, starting or running; `409 run_stopped` only while a stop is under way or was cut short: an ended member of a run whose stop completed is resumed in place, which reopens the run (`stoppedAt` cleared, the log noting `reopened: <member> resumes`; the other ended members stay ended until resumed one by one, and a later stop stops it again); `409 already_resumed` when a running session holds that agent session; `400 invalid_agent` when the member's agent is no longer in the catalog; a session that cannot be created answers as at launch, the member `ended` with its `error` |
| `POST /api/runs/{run}/resume` | admin | start a new run of a stopped (or finished) run's crew in which every member whose last `agentSession` is `resumable` continues its conversation in its kept worktree and branch, without a prompt (`starting` until its session is ready for one, then `running`; its next done starts the members after it), and the others start afresh under their start rules; reply `201 {run}`, the new run with `resumedFrom`, the old run from then on carrying `resumedBy` and staying stopped; `409 run_running` while the run goes; `409 run_stopped` when its stop was cut short; `409 already_resumed` for a run resumed already, or being resumed, with `run` naming the continuation; for a run the server no longer holds but has a record of (stopped, finished or interrupted), the same from the record: the crew as the record has it (each member's `prompt`, `args` and `start` as they ran, not the crew as saved since), each member with a `resumable` `agentSession` continuing it in its recorded `worktree` and `branch` (a worktree gone fails that member's start), the record then carrying `resumedBy` and resumed no more (`409 already_resumed`); `404` when unknown; a member whose session cannot be made ends with its `error`, as at launch |
| `POST /api/runs/{run}/stop` | admin | stop every member's session and, in a managed run, every service of its trees beside them (`SIGTERM` to the service's process group, `SIGKILL` after 5 s), before the stop is recorded and the trees end, their cleanup steps run (Project manifests, under Managed runs); reply `{run}` with `stoppedAt`, and `cleaning` above zero when a tree's end was still under way as the stop's 30 s ran out (it goes on); the worktrees stay; stopping again changes nothing; a session that does not stop cleanly is logged by the server and the run is stopped all the same; `404` when unknown |
| `POST /api/runs/{run}/broadcast` | admin | type a line into members of the run: body `{text, members?, byName?}`, `members` empty or missing for every member; reply `{sent, skipped}`, `sent` the names typed into and `skipped` entries `{member, reason}` with `reason` `needs_input`, `not_running`, `unknown` or `no_enter` (typed, its carriage return left out because a question came up; see Crew runs); `400 invalid_request` for a text with nothing left, or over 4096 bytes, once made one line; `404` when unknown |
| `GET /api/runs/{run}/links` | admin | `{links, published?, republish?}`: the run's share links, each with `runId` and `active`, the viewers attached through it to its members' sessions; with a switchyard configured, `published` says a member is published there now, and `republish: true` that a running member's publication the switchyard ended has not been made again, which the next run link does; `404` when unknown |
| `GET /api/runs/{run}/commands/{command}` | admin | a command's receipt from the managed runs' ledger (#334): `{command: {id, intent, status, receipt?, createdAt}}`, `status` one of `accepted`, `done`, `failed`, `uncertain`; `404 not_found` for a run or command unknown, or one that is not the admin's; `400 invalid_request` for a command id that is not 1 to 32 of letters, digits, `.`, `_`, `~` and `-`. A read: it never acts, so a client that lost a reply looks here before it sends again |
| `GET /api/runs/{run}/tasks/{task}` | admin | a managed run's task with every attempt (#334, row 6): `{task, attempts}`, `task` as the run reports it (Managed runs) and `attempts` each `{n, sessionId?, worktree?, branch?, start?, result?, state, stage?, error?, startedAt?, endedAt?, submitted?, checkpoint?, steps?, preview?}`, `submitted` what the attempt submitted (`{summary, commit, issues, verdict, findings}`), `steps` its receipts as the steps route below answers them and `preview` its worktree's preview while the tree answers behind it (Project manifests, under Managed runs); for a run the server no longer holds (evicted, or run by a server before this one) the task and its attempts as the ledger has them, with no preview, a result a continuation imported read from the run that made it; `404 not_found` for a run or task neither the server nor the ledger has; `503 ledger_unavailable` for a run not held when there is no ledger to read; `400 invalid_request` for an id that is not shaped as one |
| `GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps` | admin | an attempt's receipts from the ledger (Project manifests below): `{steps}`, by when each started, each `{phase, id, k, state, argv?, url?, dir?, subject?, manifest?, exit?, timedOut?, duration?, error?, ports?, tries, startedAt, finishedAt?, logBytes, logDropped?, logSha256?}`, the row whole but for its log (`duration` in milliseconds; `exit` once it finished); a run the server let go answers the same; `404 not_found` for a run or attempt the ledger has not; `503 ledger_unavailable`; `400 invalid_request` for an id or number that is not shaped as one |
| `GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log` | admin | one receipt's log: `200 text/plain`, the bytes the ledger keeps (at most 1 MiB: the output's head, a line saying how many bytes were dropped, and its last bytes), their SHA-256 in `X-Conductor-Log-SHA256` (exposed to a dev origin's script; a log kept empty answers `200`, empty, with its hash); `404 not_found` for a row the ledger has not, one still running, or one that ended with no log (a check its attempt closed under, a step or prerequisite the recovery concluded); `410 log_dropped` for a log not kept past the run's 64 MiB of logs (its hash and tail stay on the row); fetched with the bearer header, never as a link the browser follows |
| `GET /api/runs/{run}/prerequisites` | admin | a run's prerequisites' receipts from the ledger: `{steps}` as the steps route answers them, each of phase `prerequisite`, `k` counting its runs in the run; `404 not_found` for a run the ledger has not; `503 ledger_unavailable` |
| `GET /api/runs/{run}/prerequisites/{id}/{k}/log` | admin | one prerequisite receipt's log, as a step's log route answers it |
| `POST /api/runs/{run}/pause`, `POST /api/runs/{run}/unpause` | admin | pause a managed run's scheduling (attempts in flight go on, none starts) or resume it; body `{commandId, expectedRevision?}` (Commands with receipts below); reply `200 {receipt}`; `409 run_stopped` with the receipt once the run is stopped |
| `POST /api/runs/{run}/tasks/{task}/cancel` | admin | cancel a task: its attempt in flight ends (its session stopped once the cancel is recorded), the task is `cancelled`, what needs it is blocked for good, and the run ends when every task is terminal; body `{commandId, expectedRevision?}`; `200 {receipt}` with the task; `409 task_state` with the receipt for a task already succeeded, failed or cancelled |
| `POST /api/runs/{run}/tasks/{task}/retry` | admin | allow a failed or cancelled task one more attempt: it is `blocked` or `ready` again (a repair it carries stays), the run open again; `200 {receipt}`; `409 task_state` with the receipt otherwise |
| `POST /api/runs/{run}/tasks/{task}/accept` | admin | take a failed task's last submitted commit as its result all the same ("Accept anyway", a person's decision over a failed check): the task `succeeded` with it, what needs it goes on; `200 {receipt}`; `409 task_state` with the receipt for a task not failed, or one no attempt of which submitted a commit |
| `POST /api/runs/{run}/links` | admin | create a run link: `{role, label?, ttlSeconds?}`, reply `201 {link, token, url}` as for a session link (its `url` on the same base); it grants its role on the session of every member of the run, a member added later included (see Crew runs); `409 too_many_links` past 100 links for the run; `404` when unknown |
| `DELETE /api/runs/{run}/links/{linkId}` | admin | revoke a run link, which closes every viewer attached through it (`4403`); `204`, `404` when the link is not the run's |
| `GET /api/dispatch/projects` | admin | Dispatch's projects (see Dispatch below): `{projects, next?}`, a page of at most `limit` (1 to 200, 200 when absent) summaries by key after `cursor` (a page's opaque `next`), each `{id, key, name, tracker, archivedAt?, open, done, rev, updatedAt}`, `open` counting its live tickets neither done nor cancelled and `done` those done, without its description, folder, tracker URL and close policy, which its read carries, so a page is under 128 KiB (a name of 60 characters encodes to at most 360 bytes); the archived ones only with `archived=1`; `400 invalid_request` for a bad value |
| `POST /api/dispatch/projects` | admin | make a project (`project.create`, Dispatch operations below): `{opId, byName?, key, name, description?, cwd?, tracker?, trackerUrl?, closeRequires?}`, `key` one upper-case letter then one to five upper-case letters or digits, chosen now and never changed, `name` 1 to 60 characters, `description` Markdown of at most 16 KiB, `cwd` a folder kept as text (at most 4096 bytes), `tracker` `native` (the default) or `external` with an `http` or `https` `trackerUrl` (at most 2 KiB), `closeRequires` any of `acceptance` and `delivery` (`["acceptance"]` for a native project and `[]` for an external one when absent; `verified` refused); reply `201 {receipt, project}`; `409 key_taken` for a key another project holds, archived or not |
| `GET /api/dispatch/projects/{key}` | admin | a project, archived or not, with what its pages offer: `{project, labels, milestones, views}`, its live labels by name, its live milestones by target, and the live saved views of the project and of every project, a view's `query` the JSON it is; at most 3 MiB with every field and set at its bound (`maxProjectReply`); `404 not_found` |
| `POST /api/dispatch/projects/{key}/edit` | admin | set the fields given of a project (`project.edit`): `{opId, expectedRev?, byName?, name?, description?, cwd?, tracker?, trackerUrl?, closeRequires?}`, each held to its create's rule, the key never changing; reply `{receipt, project}`; `409 archived` for an archived project |
| `POST /api/dispatch/projects/{key}/archive`, `…/unarchive` | admin | archive a project, which refuses every write under it (`409 archived`) until it is unarchived, or unarchive it: `{opId, expectedRev?, byName?}`; reply `{receipt, project}`; `409 invalid` for one archived already, or not archived; a project is never deleted |
| `POST /api/dispatch/projects/{key}/labels`, `…/labels/{id}/edit`, `…/labels/{id}/delete` | admin | a project's labels: make one (`label.create`: `{opId, byName?, name, color}`, `name` 1 to 40 characters that no live label of the project holds, whatever its case, `color` one of `gray`, `brown`, `red`, `orange`, `amber`, `yellow`, `lime`, `green`, `teal`, `cyan`, `blue` and `indigo`; at most 100 live), set the fields given of a live one (`label.edit`: `{opId, expectedRev?, byName?, name?, color?}`), or delete one (`label.delete`: `{opId, expectedRev?, byName?}`): a tombstone that leaves every ticket that had it, deleted ones included, with one change row and its `rev` bumped on each, its name free for a new label; reply `{receipt, label}`, `201` for a create; `404 not_found` for a project missing, or a label missing, deleted or another project's; `400 invalid_request` for an `id` that is not one (16 lower-case hex characters), as on every route below that names a row by its id |
| `POST /api/dispatch/projects/{key}/milestones`, `…/milestones/{id}/edit`, `…/milestones/{id}/delete` | admin | a project's milestones: make one (`milestone.create`: `{opId, byName?, name, description?, target?, state?}`, `name` 1 to 60 characters, `description` Markdown of at most 4 KiB, `target` a date as `YYYY-MM-DD` or `""` for none, `state` `open` (the default) or `closed`; at most 100 live), set the fields given of a live one (`milestone.edit`: `{opId, expectedRev?, byName?, name?, description?, target?, state?}`), or delete one (`milestone.delete`: `{opId, expectedRev?, byName?}`): a tombstone that leaves every ticket naming it, deleted ones included, its `milestoneId` cleared with one change row and its `rev` bumped on each; reply `{receipt, milestone}`, `201` for a create; `404 not_found` for a project missing, or a milestone missing, deleted or another project's |
| `GET /api/dispatch/people` | admin | the directory of names the owner and assignee pickers offer, by name: `{people}`, each `{name, createdAt}`; a name a ticket's owner or a `person:` assignee gives is added to it |
| `POST /api/dispatch/people` | admin | add a name (`person.add`): `{opId, byName?, name}`, 1 to 60 characters with no control character; reply `201 {receipt, person}`; `409 invalid` for a name held already |
| `GET /api/dispatch/tickets` | admin | a page of a list of tickets, filtered and sorted on the server: `{tickets, next?, total}`, each a summary without its description, acceptance criteria and acceptance (`{id, projectId, number, key, title, status, priority, horizon, owner, assignees, labels, milestoneId, parentId, blocked, createdAt, updatedAt, closedAt?, deletedAt?, rev}`), `total` how many the whole list holds; `project` a project's key (every project's when absent); `status`, `priority` (its number, 0 none to 4 urgent), `label` (an id), `assignee` (`person:<name>` or `agent:<catalog id>`) and `horizon` (`now`, `next`, `later`, or empty for not planned) as often as wanted, a ticket holding any of them; `owner`, `milestone` (an id), `parent` (a ticket's key, its live children whether it is live or not) and `blocked=1` (a live ticket neither done nor cancelled blocks it); `q`, at most 200 characters of words, every one in the ticket's key, title, description or acceptance criteria or in one of its comments, the last a prefix, never search syntax; `sort` (`updated`, the default, `created`, `priority`, `status`, `title`, `key`, `milestone`) and `desc=1`; `deleted=1` for the tombstones beside the live tickets, which a list leaves out otherwise; `limit` 1 to 200 (100 when absent) and `cursor` (a page's opaque `next`, of the same sort); `400 invalid_request` for a bad value or a parameter the route does not take |
| `POST /api/dispatch/tickets` | admin | make a ticket (`ticket.create`): `{opId, byName?, project, title, description?, acceptance?, status?, priority?, horizon?, owner?, assignees?, labels?, milestone?, parent?}`, at most 192 KiB (every field at its bound with each character doubled by escaping): `title` 1 to 200 characters, `description` Markdown of at most 64 KiB and `acceptance` of at most 16 KiB, `status` `backlog` (the default), `ready`, `in_progress`, `in_review`, `done` or `cancelled`, `priority` 0 to 4, `owner` a person's name, at most 8 `assignees` and 20 `labels` (ids), `milestone` an id and `parent` a key of the same project; its key `<project>-<n>` from the project's next number, never reassigned; reply `201 {receipt, ticket}`, the ticket in full; `404 not_found` for a project, parent, label or milestone missing or deleted; `409` for what the store refuses (`archived`, `invalid`, `bound`, `cycle`, `close_policy`) |
| `GET /api/dispatch/tickets/{key}` | admin | a ticket, deleted or not, in full from one read transaction: `{ticket}`, its fields with `assignees`, `labels` (ids), `parent` (`{id, key, title, status, deletedAt?}`, inactive while it is a tombstone), `children` (`{tickets, total}`: the first 50 live by number, the rest through the list's `parent`), `relations` both ways (the first 100, the active first, each with its other end and that end's `deletedAt`) and `relationsTotal`, `blocked`, `links`, `attachments`, the last 50 `comments` (each with its number `n`), and `close: {requires, met: {acceptance, delivery}, missing, why?}`, its close policy's state computed now, `why` the words for an acceptance recorded and unmet ("accepted for Alice; the owner is now Bob", "accepted before the criteria changed"); at most 3 MiB with every field and set at its bound (`maxTicketReply`); `404 not_found` |
| `POST /api/dispatch/tickets/{key}/edit` | admin | edit a live ticket (`ticket.edit`), every change one operation: `{opId, expectedRev?, byName?, set?: {title?, description?, acceptance?, status?, priority?, horizon?, owner?, parent?, milestone?}, add?: {assignees?, labels?}, remove?: {assignees?, labels?}}`, at most 192 KiB, `parent` a key and `milestone` an id (`""` for none); reply `{receipt, ticket}`; `409 close_policy` for a move to `done` the project's close policy refuses, the receipt's `missing` naming what is lacking; `409 cycle` for a parent that would make a cycle or a chain of more than 8 |
| `POST /api/dispatch/tickets/{key}/accept`, `…/unaccept` | admin | record the owner's acceptance of a live ticket (`ticket.accept`: `{opId, expectedRev?, byName?, note?}`, by the display name, for the owner the ticket has now and against its acceptance criteria as they stand, the note at most 1,000 characters), or clear it (`ticket.unaccept`: `{opId, expectedRev?, byName?}`); reply `{receipt, ticket}`; `409 invalid` for a ticket with no owner to accept it, or not accepted; neither touches the status |
| `POST /api/dispatch/tickets/{key}/delete`, `…/undelete` | admin | delete a live ticket (`ticket.delete`): a tombstone, nothing else written, its children, relations and run links kept, inactive, and back with its undelete (`ticket.undelete`), which removes a run link of its naming a run another live ticket has linked meanwhile; `{opId, expectedRev?, byName?}`; reply `{receipt, ticket}`; a deleted ticket keeps its key, is listed only with `deleted=1`, its read answers, and every other write on it is `404 not_found`; `409 invalid` to undelete a live ticket |
| `GET /api/dispatch/tickets/{key}/history` | admin | a page of a ticket's history, deleted or not, as the store recorded it, field by field: `{changes, next?}`, the changes of the ticket and of its comments after `after` (a change's `seq`, 0 when absent), at most `limit` (1 to 500, 100 when absent) and about 1 MiB of their values, each `{seq, entity, entityId, ticketId?, projectId?, field?, op, value, prior?, at, actor, by?, opId, n}`; `next` the `seq` to read after |
| `GET /api/dispatch/tickets/{key}/comments` | admin | a ticket's comments beyond the page its read carries: `{comments}`, those numbered after `after` (0 when absent), at most `limit` (1 to 50, 50 when absent: as many as the read carries), each with its number `n`, tombstones among them so the numbers have no gap; at most 3 MiB with every comment at its bound (`maxTicketReply`) |
| `POST /api/dispatch/tickets/{key}/comments`, `…/comments/{id}/edit`, `…/comments/{id}/delete` | admin | a live ticket's discussion: comment on it (`comment.add`: `{opId, expectedRev?, byName?, body}`, Markdown of 1 to 16 KiB, its author the admin, kept as the actor `admin` with `by` the display name; `expectedRev` the ticket's; at most 2,000 on a ticket, tombstones among them), replace a live comment's body (`comment.edit`: `{opId, expectedRev?, byName?, body}`, the body it replaces kept in the history; `expectedRev` the comment's), or delete one (`comment.delete`: `{opId, expectedRev?, byName?}`): a tombstone that keeps its place, so no number moves; reply `{receipt, comment}`, the comment with its number `n`, `201` for an add; `404 not_found` for a ticket missing or deleted, or a comment missing, deleted or another ticket's; `409 invalid` for an edit to the body the comment holds |
| `POST /api/dispatch/tickets/{key}/relations`, `…/relations/{id}/delete` | admin | relate a live ticket to another of its project (`relation.add`: `{opId, expectedRev?, byName?, kind, to}`, `kind` `blocks` (the ticket blocks `to`; "depends on" is a `blocks` read from the other end) or `relates`, `to` the other's key; at most 100 on either), or remove a live relation at either of its ends (`relation.remove`: `{opId, expectedRev?, byName?}`); both tickets' `rev` bumped, `expectedRev` that of the ticket the route names; reply `{receipt, ticket}`, that ticket in full, `201` for an add, the receipt's `id` the relation's; `409 cycle` for a `blocks` that would close a cycle, a ticket blocking itself among them; `409 invalid` for a `to` of another project, a relation held already (a `relates` either way) or a kind that is neither; `404 not_found` for either ticket missing or deleted, or a relation missing, removed or not the ticket's |
| `POST /api/dispatch/tickets/{key}/links`, `…/links/{id}/delete` | admin | link a live ticket outward (`link.add`: `{opId, expectedRev?, byName?, kind, ref?, url?, note?}`), or remove a live link (`link.remove`: `{opId, expectedRev?, byName?}`): a `delivery` link (a merged pull request's or a release's URL; a merge is such a link, its merged state never read) and an `external` one name their `url` (`http` or `https`, at most 2 KiB) and take it as their `ref` when they name none; a `run` link names its run as `ref`, one the engine holds or has a record of (`404 not_found` otherwise) and that no other live ticket's live `run` link names (`409 run_linked`, the receipt's `key` naming that ticket), so a run belongs to one ticket; `note` at most 1,000 characters; a second live link of the same `kind` and `ref` is `409 invalid`, and one added again after its removal is a new link; at most 50 live; the ticket's `rev` bumped; reply `{receipt, ticket}`, `201` for an add, the receipt's `id` the link's; `404 not_found` for a ticket missing or deleted, or a link missing, removed or another ticket's |
| `POST /api/dispatch/tickets/{key}/attachments` | admin | attach a file to a live ticket (`attachment.add`): a `multipart/form-data` body whose parts are `opId`, then `expectedRev` and `byName` when given, each once, and last `file`, its filename the attachment's name (1 to 255 bytes, no `/`, `\` or control character, neither `.` nor `..`) and its `Content-Type` the attachment's type as given (at most 100 bytes); at most 16 MiB of file and 64 KiB beside it (the fields, the parts' headers, the boundaries and whatever comes before the first or after the last), a body past 16 MiB and 64 KiB `413 bound` whatever it holds and one with more than 64 KiB beside its file `413 bound` too, neither kept, and every body read to its end before the answer; at most 50 live attachments on a ticket; `expectedRev` the ticket's; reply `201 {receipt, attachment}`, the attachment `{id, ticketId, name, size, sha256, contentType, createdAt, createdBy}` and the receipt's `id` its; `409 expired` when the upload's lease was swept before its row was written (see Attachments below), the file sent again under a new `opId`; `409 bound` for a file past 16 MiB, a 51st attachment or a type past its bound; `409 invalid` for a name that is no file's; `404 not_found` for a ticket missing or deleted; `400 invalid_request` for a body that is not those parts in that order; `500 store_failed` when the file could not be written or made durable |
| `POST /api/dispatch/tickets/{key}/attachments/{id}/delete` | admin | remove a live attachment (`attachment.remove`): `{opId, expectedRev?, byName?}`, `expectedRev` the ticket's; a tombstone, its file kept until no live attachment names its content and the store's sweep at a start deletes it; reply `{receipt, attachment}`, the attachment with `deletedAt`; `404 not_found` for a ticket missing or deleted, or an attachment missing, removed or another ticket's |
| `GET /api/dispatch/attachments/{id}` | admin | a live attachment's content, of a ticket deleted or not, byte for byte: `Content-Disposition: attachment; filename*=UTF-8''…` (its name, percent-encoded), `X-Content-Type-Options: nosniff`, `Content-Length`, and `Content-Type` its type when it is `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `text/plain`, `application/pdf` or `application/json`, else `application/octet-stream` (an SVG is served as a download, never inline, since it can script); a `Range` is served as one; `404 not_found` for an attachment missing or removed; `400 invalid_request` for an id that is not one and for any parameter |
| `GET /api/dispatch/links?run={id}` | admin | the ticket a run is linked to, for the run page's chip: `{ticket: {id, key, title, status, deletedAt?}}`, the live ticket whose live `run` link names the run (one at most); else, when only deleted tickets' links name it, the one whose link is latest, with `deletedAt`; `{ticket: null}` when no live link names the run; `400 invalid_request` for a run id that is not one |
| `GET /api/dispatch/views` | admin | the live saved views, by name: `{views}`, each `{id, projectId, name, kind, query, createdAt, updatedAt, rev}`, `projectId` `""` for a view of every project and `query` the JSON it is; with `project` (a project's key), the views its pages offer, its own and every project's, else every live view; at most 200, under 3 MiB as a project's read is; `404 not_found` for a project missing |
| `POST /api/dispatch/views`, `…/views/{id}/edit`, `…/views/{id}/delete` | admin | save a view (`view.create`: `{opId, byName?, project?, name, kind, query}`, of the project with the key or, without one, of every project; `name` 1 to 60 characters, `kind` `list`, `board` or `roadmap`, `query` a JSON object, never a string of one, of a list's filter and sort as the tickets' list takes them (`status`, `priority`, `labels`, `owner`, `assignees`, `milestone`, `horizon`, `parent`, `blocked`, `q`, `deleted`, `sort`, `desc`) and its grouping (`group`: `status`, `priority`, `horizon`, `milestone` or `owner`), each value in its vocabulary, at most 8 KiB as kept, compacted; at most 200 live), set the fields given of a live one (`view.edit`: `{opId, expectedRev?, byName?, name?, kind?, query?}`, its project never changing), or delete one (`view.delete`: `{opId, expectedRev?, byName?}`, a tombstone); reply `{receipt, view}`, `201` for a create; `404 not_found` for a project missing, or a view missing or deleted; `409 invalid` for a query that is not a list's; `409 archived` for a view of an archived project |
| `GET /api/dispatch/ops/{opId}` | admin | an operation's receipt of the admin's without acting, as `GET /api/runs/{run}/commands/{command}` reads a command's: `{receipt}`; `404` when the store holds none (never given, or pruned past the newest 20,000) |
| `GET /api/integrations` | admin | `{integrations, host, webhooks}`: every hook adapter in a stable order, each `{id, name, events, launchInjection, installable, installsSkill, installed, where, snippet, experimental}`, the server's host name (`""` when it cannot tell), and the configured webhooks, each `{url, events}` with the URL as `scheme://host[:port]/path` (no user info, query or fragment) and never its secret; `installable` is true for an adapter with a file to install into (`POST …/install` can do something); `installsSkill` is true for an agent whose install also brings the Conductor skill; `installed` and `where` check the home of the user running the server, writing nothing, and are `false` and `""` for an adapter with no file to install |
| `POST /api/integrations/{id}/install` | admin | install the adapter's hooks (and, for Claude Code, Codex, pi and Goose, the Conductor skill) into the agent's own config in the server user's home, and nowhere else; reply `{changed}`, the files written, `[]` when all was in place; `400 no_file_route` when there is no file to install into or a step is left to do by hand, the error carrying `changed` (files already written) and `snippet`, which is present only when what is left by hand is the agent's hooks, not a skill file (the message says what to do about a `SKILL.md` of the user's own); `500 install_failed` with `changed`; `404` for an unknown `id` |
| `GET /api/sessions` | admin | list sessions |
| `GET /api/touched` | admin | the files every listed session's agent touched (#304), from the index each session keeps (`op: touched` above): `{sessions[{id, name, agentId, kind, status, cwd, touched[{path, op, ops, count, tool?, by?, first, last}], truncated?}], truncated?}`, the sessions in the list's order (the newest first), each session's files the most recently touched first, a server session's deny list applied; a session with nothing touched is left out; a hosted session's index holds the `file` entries its host reported since it connected (the host's own index, which the Files tab reads, begins with the session). `?limit=` is the files per session (default 500, at most 2,000, `400 invalid_request` otherwise), a session cut to it marked `truncated`; the whole reply is held to about 2 MiB of JSON, the session that would pass it cut short and marked, and the reply marked too |
| `POST /api/sessions` | admin | launch a server session: `{agentId, name?, cwd?, args?, cols?, rows?, yolo?}`, reply `201` with the session `Info`; `yolo` overrides the server's `yolo` for this launch: with it on, the agent's yolo recipe is applied (its `args` after the command and `args`, its `env` over the agent's own, through the filtered environment; for Codex also `-c projects={"<dir>"={trust_level="trusted"}}` naming the launch directory's repository and, for a worktree, its main repository), and `Info.yolo` says so; an agent with no recipe is launched without one and its activity says so; an agent with a `session` recipe and `startArgs` gets a fresh id there, right after its command (`Info.agentSession`, `source:"set"`); `400 not_the_agent` when the identity probe's cached answer names the agent's program a known other one (`identity.impostor`; nothing is run at launch, so an answer not yet cached refuses nothing), the message naming the program, the agent and what it printed; `probe: false` on the agent launches it anyway; a verified probe that matched nothing does not refuse (the launch goes ahead, with an `error` entry in the activity) |
| `GET /api/sessions/{id}` | admin or share token | one session with the caller's `role`; admins also get its `links`; on a switchyard the workbench token's `role` is `""` |
| `DELETE /api/sessions/{id}` | admin | stop a running session; on an ended session, remove it from the list; on a switchyard, a session a Conductor published there ends its publication, its process running on at home |
| `PATCH /api/sessions/{id}` | admin | rename a server session, running or ended: `{name}` (the body at most 4 KiB); the name loses its control characters (C0, DEL and C1) and the space around it and is cut to 120 bytes at a character boundary (`proto.MaxSessionName`, the bound a launch's name has too); reply `200` with the session `Info`; the change reaches `GET /api/events` as a `session` event, and a resume of the session starts under the new name (`POST /api/sessions/{id}/resume`; a crew member's too, the member keeping its own name in its run); `400 invalid_name` when nothing is left; `400 hosted_session` for a hosted session (its host names it); `400 invalid_request` for a body that is not one `{name}` (an unknown field, or past its 4 KiB); `404` when unknown. A session a Conductor published to a switchyard keeps there the name it was published with |
| `POST /api/sessions/{id}/resume` | admin | resume an ended server session (no body), while it is listed: a new session with the same agent, name (the one it was renamed to, if it was: `PATCH /api/sessions/{id}`), working directory, arguments, environment and yolo choice, launched with its agent's `resumeArgs` for the stored `agentSession`, right after the command, when the agent has a `session` recipe and the session was `resumable` (`resumed: true`), and plainly otherwise, with a `notice`: `<agent> has no session recipe: started anew`, or `nothing to resume yet (the agent had no turn): started anew`; reply `201 {session, resumed, notice?}`, the new session's `Info` with `resumedFrom`; a crew member's session is resumed as its member, as `POST /api/runs/{run}/members/{name}/resume` does; `404` when unknown; `400 hosted_session` for a hosted session; `409 still_running`; `409 already_resumed` when a running session holds that agent session; `400 invalid_agent` when its agent is no longer in the catalog; `400 invalid_cwd` when its working directory no longer passes the check; otherwise as `POST /api/sessions` answers |
| `POST /api/sessions/{id}/crew` | the session's agent token, or admin | an agent forms a crew around its own session (`agents.selfService`, on by default; `403 self_service_off`): body `{name, goal, members, self, isolation?, open?}` (the crew's `cwd`, `where` and `yolo` are the session's own, never in the body; `self` names the member this session becomes, whose `agentId` is the session's agent or empty, meaning that); the crew is saved, its run launched with the session adopted as `self` (`running`, its prompt its own: the members after it start on its next done, handoffs reach it) and the others started as at launch; reply `201 {run, crew, member, url}`, `url` the run's page; the session is tagged with the run and records `formed crew <name>` (a `link` entry with `(open)` when asked, which the workbench offers as a toast); at most 2 per session per hour (`429`); `409 in_a_run` while the session is a member of a run, `409 session_ended`, `400 hosted_session`, `400 invalid_crew` as a launch answers (an impostor among the members included) |
| `POST /api/sessions/{id}/run/members` | the session's agent token, or admin | add a member (one `crew.Member`) to the session's own run, as `POST /api/runs/{run}/members` does; `201 {run}`; `409 no_run` |
| `GET /api/sessions/{id}/task` | the session's agent token, or admin | the task of an attempt's session in a managed run (#334, row 6): `{task, markdown, page, pages, bytes}`, `task` the attempt's `task.json` and `markdown` a page of its `task.md` (32 KiB a page, `?page=` from 1, page 1 unless asked; `400 invalid_request` past the last); `404 not_an_attempt` for a session that is no attempt; `409 not_running` for an attempt whose worktree is not made yet; never gated by `agents.selfService` |
| `POST /api/sessions/{id}/messages` | the session's agent token, or admin | send a message from the session's member to another member of its managed run (#334, row 9): body `{to, text}` (one to 4,000 characters); reply `201 {message}`, `{seq, from, to, text, createdAt, deliveredAt?, ackedAt?}`; the message is typed into the recipient's session as one line, `Message from <from> (#<seq>): <text>`, after its task line, once its attempt has one; `404 no_member`; `429 inbox_full` once the recipient has 50 messages not acknowledged; `400 invalid_request` for an empty text, one too long, or a message to oneself; `409 task_state` on a legacy run |
| `GET /api/sessions/{id}/inbox` | the session's agent token, or admin | the session's member's messages after `?after=` (a sequence number, 0 unless given), at most 50, in order, delivered or not: `{messages}` |
| `POST /api/sessions/{id}/inbox/ack` | the session's agent token, or admin | acknowledge the session's member's messages up to a sequence number: body `{upTo}`; reply `{acked}`, how many now; acknowledged messages count no more against the member's inbox |
| `GET /api/runs/{run}/messages` | admin | a managed run's messages in sequence: `{messages}` |
| `POST /api/sessions/{id}/task/blocked` | the session's agent token, or admin | report what blocks the session's attempt (#334, row 10): body `{reason, message?}` (a reason of one to 500 characters, a message of at most 4,000); reply `{task}` with its `blocker` (`{reason, message?, by, at}`); the session then asks the message (else the reason) as its own question; a `working` or `done` report of the attempt, or its result, clears the blocker; `409 not_running` for an attempt not running; `404 not_an_attempt` |
| `POST /api/sessions/{id}/task/checkpoint` | the session's agent token, or admin | save where the session's attempt got to, tied to the commit its worktree is at: body `{done, remaining, notes?}` (each at most 4,000 characters, `done` and `remaining` given); reply `{attempt}` with its `checkpoint` (`{done, remaining, notes?, commit, at}`), kept in the ledger; the task's next attempt reads it in its task file, and one after an attempt that ended without a result, or a continuation's first, starts at that commit; `409 no_commit` for a worktree with none; `409 not_running` |
| `POST /api/sessions/{id}/task/result` | the session's agent token only | submit the attempt's result: body `{summary?, commit?, issues?, verdict?, findings?}` (a work task: the commit, HEAD when not given, with a summary of at most 4,000 characters and at most 20 issues; a review: the verdict, `approved` or `needs_changes`, with at most 50 findings `{severity, path?, line?, message}`); reply `201 {task}` once recorded (the task `verifying`, or `succeeded` when it has no checks), `200 {task}` for the same commit submitted again while the first is verified; `409` with why otherwise, the attempt running on: `dirty_worktree` (the paths in the message), `off_branch`, `commit_mismatch` (the worktree is not at the commit named), `not_from_input`, `review_changed`, `reserved_path`, `submodules`, `stale_attempt` (not the task's active attempt, or another commit while one is verified), `not_running`; `400 bad_result` for a result that does not fit the task's kind or its bounds; `403 agent_only` for the workbench token, as a result is always the agent's own; `404 not_an_attempt` |
| `GET /api/sessions/{id}/ticket` | the session's agent token, or admin | the Dispatch ticket the session's run is linked to (see Dispatch, An attempt's ticket), for `conductor task ticket` and the MCP tool `ticket_read`: `{ticket, labels, milestone}`, `ticket` as `GET /api/dispatch/tickets/{key}` reads it (its last 50 comments among it), `labels` the rows of its live labels, by name, and `milestone` its live milestone's row or `null`, which the project's routes give the admin alone; the live ticket whose live `run` link names the session's run, one at most, a deleted ticket's link holding none; `404 no_ticket` for a session that is no member of a run and for a run no live ticket holds; `400 invalid_request` for any parameter; under 3 MiB, as the ticket's own read; a switchyard's `403 switchyard` |
| `POST /api/sessions/{id}/ticket/comments` | the session's agent token, or admin | comment on that ticket (`comment.add`, Dispatch operations), for `conductor task ticket comment` and the MCP tool `ticket_comment`: body `{opId, body}`, `body` Markdown of 1 to 16 KiB; the comment is the session's member's, `agent:<member>` its author and the actor its operation is kept under, whichever token sent it, with no display name; reply `201 {receipt, comment}`, `200` for the same `opId` sent again, as the admin's comment route; `404 no_ticket` as above; `400 invalid_request` for a body that is not `{opId, body}`, `413 bound` past 64 KiB, and the store's refusals as the admin's route answers them; an agent reads its ticket and comments on it, nothing more; a switchyard's `403 switchyard` |
| `GET /api/sessions/{id}/run` | the session's agent token, or admin | the session's run with the last 50 log entries, and the session's member name: `{run, member}`; `404 no_run` |
| `POST /api/sessions/{id}/links/agent` | the session's agent token, or admin | a view-only link to the session for an agent to hand out (a PR, a message): body `{ttlSeconds?, label?}`, the TTL two hours unless given, a day at most (`400`); reply as `POST /api/sessions/{id}/links`; at most 5 per session per day (`429`); recorded in the session's activity |
| `GET /api/sessions/{id}/links` | admin | share links of a session (`{links, published?, republish?}`), each with `active` viewers; with a switchyard configured, `published` says the session is published there now, and `republish: true` that the publication the switchyard ended has not been made again, which the next link does (the Share dialog makes a link anew then, and in place of one it made here while the session was not published, once it is); for a session published to a rendezvous, the links minted there too, with `remote: true` and no `active` (their viewers are counted there); a rendezvous restart that re-registered the session drops those records; an ended session lists none |
| `POST /api/sessions/{id}/links` | admin | create a share link (`403 switchyard` on a switchyard, where the publishing machine mints a session's links over its own connection, `link`): `{role, label?, ttlSeconds?}`, reply `201 {link, token, url, invite}` (`invite` is the same link as `conductor://<host>/join/<token>`, which the desktop app opens itself); for a session published to a rendezvous the link is minted there over the host connection and the reply is `201 {link{id, role, label, expiresAt, sessionId, remote: true}, url, invite, remote: true}` with the rendezvous's URL and no `token` (`502 rendezvous_unavailable` when it does not answer within 15 s); while that session's publication is still being made the request waits up to 10 s for it, and when it failed or is not there the link is made here and the reply adds `rendezvous: {server, error}` (why the session is not at the switchyard); `url` is `<base>/join/<token>`, where the base is `publicUrl` when it names another machine, and otherwise (unset, or localhost, as the default is) the address the request came through: `X-Forwarded-Proto` and `X-Forwarded-Host` when a reverse proxy sets them, else the request's scheme and `Host` (a malformed host falls back to `publicUrl`) |
| `DELETE /api/sessions/{id}/links/{linkId}` | admin | revoke a share link, which closes every viewer attached through it (`4403`); `204`; revoking a revoked link answers `204`, records nothing and closes any viewer still attached through it; a session that ended took its links with it (local ones revoked and dropped, their viewers closed as the session's end, `4410`; those minted at the rendezvous forgotten here and ended there by the session's end, which the rendezvous hears from the publication; a hosted session's at its end too), so they are no longer listed and a revoke of one answers `204`, and stops what the session's end still writes their viewers (here, and at the rendezvous over the publication, which outlasts the session's end by 5 s for it: `502 rendezvous_unavailable` when the rendezvous does not answer then); a link minted at the rendezvous is revoked there over the host connection (`link_revoke`), `204` also when the rendezvous no longer holds it, `502 rendezvous_unavailable` when it does not answer|
| `GET /api/sessions/{id}/files` | admin or share token | read a file of a server session (`path`, `stat`, `raw` query), see File reads |
| `POST /api/sessions/{id}/attention` | agent token or admin (not a switchyard's) | report an attention state, and with it the agent's own session id (`agentSession`, at most 128 bytes) and whether the report is of a turn (`turn`), see Attention |
| `POST /api/sessions/{id}/events` | agent token or admin (not a switchyard's) | report an event or an attention word, reply `202 {accepted}`, see Events |
| `GET /api/events` | admin | Server-Sent Events of session changes (`snapshot`, `session`, `removed`), of activity entries (`activity`, with `state`, the state an attention entry records, absent for other entries and for an attention entry from a host that does not send one), of chat messages (`chat`: the message as `chat` carries it, with `sessionId` for a session's chat or `runId` for a run's; as droppable as `activity`; for the browsers' unread counts; a `conductor host` session's chat arrives the same way, the host sending each message on its control connection), and of run changes no session event carries (`run`: `{id}`, read the run again with `GET /api/runs/{run}`, or `{id, removed: true}` when the server forgot it; at most 128 bytes; sent for a member reserved, started, prompted, failed or ended early, every entry of the run's log, and a stop; a client that cannot keep up is dropped as for a session change, and reads every run again on its next `snapshot`), and of Dispatch's changes (`dispatch`: `{seq, entity, id, ticket, project}`, one per entity a commit changed, but one with `id` and `ticket` `""` for more than 64 of a kind in a project, a commit's arriving together and in order, at most 160 bytes; dropped with the client as a `run` event is, see Dispatch), see Attention and Events |
| `GET /api/join/{token}` | share token in the path | resolve a share link for the join page (rate limited): `{session, role, label}` for a session link; `{run: {id, name, members}, role, label}` for a run link, each with `switchyard` (whether this server is one, which the join page names), each member `{name, sessionId?, agentId, status}` in the run's order, with `sessionId` only while its session runs (`agentId` and `status` then the session's) and otherwise its state in the run, `pending`, `starting` or `ended`; `404` with `invalid_link`, `revoked`, `expired`, `session_gone` or `run_gone`; on a switchyard, `503 host_offline` for a link it keeps whose host is not connected now (it works again once the host registers) |

The catalog routes persist their changes as `catalog.json` in the data
directory (`dataDir`): `{"agents": [...], "hidden": [...]}`. At startup that
overlay is applied over the configured catalog, and the server refuses to start
when the file cannot be parsed or an agent in it is invalid. An env value of an
entry that equals the replaced agent's is saved back as `***` at startup (once;
a failure to save stops the server), as a save stores it. An entry of
`catalog.json` with the ID of a built-in or configured agent replaces it but
inherits what it leaves out (`adapter`, `signal`, a `signal`'s
`workingTitle`, `site`, `yolo`, `trustPrompt`, `session`, `***` env values
and env values equal to the replaced agent's; `"yolo": {}` and `"session": {}`
say it has none); env keys it does not list are not
inherited, so an API client that omits `env` drops every value of the replaced
agent, and a key the config adds later does not reach it. An agent in the
config file that replaces a built-in replaces it whole. Agents are
held to these limits everywhere: `id` matches `^[a-z0-9-]{1,32}$`, `name` at
most 60 characters, `description` 200, `command` 1 to 32 elements of at most
4096 bytes each, `env` 32 keys, `envPassthrough` 32 names, a signal `pattern`
200 bytes that does not match an empty line, a signal `workingTitle` 200 bytes
that does not match an empty title, `cwd` at most 4096 bytes without
NUL, `icon` matching `^[a-z0-9][a-z0-9:-]{0,63}$`, `site` an `https://` URL
with a host name, a valid port if any and no user info or white space, at most
200 bytes, `adapter` matching `^[a-z0-9-]{1,32}$` and naming an adapter
Conductor has, an `env` key or `envPassthrough` name at most 128 bytes and an
`env` value at most 16384 bytes. A `yolo` recipe: at most 16 `args` of 1 to
4096 bytes without NUL, at most 16 `env` variables named like an
`envPassthrough` name and never `CONDUCTOR_*`, each value at most 4096 bytes
without NUL. A `trustPrompt`, and a question's `prompt` and `words`: as a signal
`pattern`. A `session` recipe: at
most 16 `startArgs` and 16 `resumeArgs` of 1 to 4096 bytes without NUL, `{id}`
only as a whole argument, once in `resumeArgs` and once in `startArgs` when it
has any; `newId` `uuid` (the default) or `name` (`cdr-` and a UUID); `idFrom`
`hook` or absent; `idPolicy` `latest` (the default) or `lowest`; `startArgs`
or `idFrom: "hook"`, so that the id can be known; `idPattern` anchored with `^`
and `$`, at most 200 bytes, matching neither an empty id nor one that begins
with a dash; `resumeNeedsCwd` documents an agent that resumes only in the
directory its session ran in (a resume always runs there). The recipe's
arguments go right after the agent's command, before the launch's `args`, so
that Codex resumes with its subcommand. The empty recipe `{}` passes and means
none.

The crew routes keep each crew in a file of its own, `crews/<id>.json` in the
data directory, written whole (a temp file renamed into place). The list is
read from that directory, so a file added, changed or removed by hand shows at
the next listing. A file that cannot be used, a symbolic link included (it is
not followed), is named in the server's log at startup and left out of the
list; it is never overwritten (a new crew of the same name takes the next id),
and `DELETE` removes it. A file that cannot be read for now (its mode, say) is
left out of that listing and read again at the next. A `crews.json` from an
earlier version is split into these files at the first start and renamed
`crews.json.migrated`, which keeps every crew; the move never overwrites a crew
file, and a crew whose file exists with something else in it is not moved,
which the log says naming both files.
A `crews.json` that cannot be parsed stops startup, as before. So does one that
holds an invalid crew or an `id` twice; nothing is moved then. Without a data
directory the listing is empty and every other crew route answers
`503 store_unavailable`. A failure of the store is `500 store_failed`: "could
not save the crews" for a write, which changes nothing, and "could not read the
crews" for a read. `openAfterLaunch` is for the workbench, which opens the crew
view after a launch when it is set; the server stores it and does nothing else
with it.
Crews are held to these limits: `id` matches `^[a-z0-9][a-z0-9-]{0,63}$`; `name`
not blank, at most 60 characters and without control characters (surrounding
space is trimmed), `goal` at most 2000 characters, `cwd` at most 4096 bytes;
`where` is `server` or `host` and `isolation` is `none` or `worktree`;
`viewLinkTtlSeconds` 0 to 31536000 (a year); at most 12 members, each with a
`name` matching `^[a-z0-9][a-z0-9._-]{0,39}$`, unique in its crew and one git
takes for a branch (no `..`, no `.` or `.lock` at the end), an `agentId`
matching `^[a-z0-9-]{1,32}$` that the catalog has when the crew is saved or
copied, a `prompt` of at most 4000 characters that, with the goal in place of
`$GOAL` and `${GOAL}`, is at most 32756 bytes (it is submitted as one line,
which changes no length, inside the 12 bytes of the bracketed-paste markers,
and a session takes at most 32 KiB at once), and at most 32 `args` of at most
4096 bytes, 8 KiB in all; `yolo`, when present, is `true` or `false`;
`start.when` is `immediately`, `after` or
`manual`, `start.member`, set with `after` alone, names another member of the crew, and
following `start.member` from member to member never goes round in a cycle.
A crew's file, as the server writes it with its `id` and times, is at most
1 MiB; a create or update body may be up to 2 MiB, so a crew sent indented
still fits. There is no limit on the number of crews. Create and update check
the body first: a crew that breaks one of these rules is refused with
`400 invalid_crew` for that rule, ahead of an agent the catalog does not have
and an unknown `id` (`404`); an agent the catalog does not have is `400 invalid_crew` whatever else
holds. An error message quotes at most 80 characters of a value, with `…` where
it was cut. A crew with `tasks` is a managed one (#334): every member's `start.when` is `task` (refused in a crew without tasks), `isolation` is `worktree`, at most 128 tasks, each `{id, member, kind, instructions, needs?, checks?}` with an `id` matching `^[a-z0-9][a-z0-9_-]{0,39}$` and unique, a `member` of the crew, a `kind` of `work` or `review`, `instructions` of at most 8000 characters, `needs` naming other tasks with no cycle (a review needs exactly one work task), and at most 8 `checks`, none for a review, each `{id, argv, timeoutSeconds?}` with an `id` as a task's, `argv` naming a program within the member-args bounds (at most 32 entries of 4096 bytes, 8 KiB in all; never a shell line) and `timeoutSeconds` 1 to 3600 (600 when left out); every member has a task; `setup`, only with tasks, is at most 8 such commands run in every new attempt worktree. A crew with tasks launches as a managed run (Managed runs below) when `crews.managed` is on (`409 managed_off` otherwise) and the ledger is the server's (`503 ledger_unavailable` otherwise); a managed run's members are never started, added or resumed by hand (`409 managed_run`), and its record is continued, not resumed, by the resume route.
A crew with tasks may have a project manifest, `manifest`
(`docs/plans/project-manifests.md`): what makes a fresh tree of the project
usable (a tree is an attempt's worktree or the checkout of its result), how
it is checked and how it is cleaned up; it is not an agent's recipes (the
catalog's yolo, trust and session recipes). `setup` is its legacy form, the
manifest's setup phase as a crew saved before manifests holds it: a crew has one
or the other ("setup is the project manifest's setup phase: put it there"),
and a manifest, as `setup`, is only set with tasks; one with nothing in it is
none. A manifest is `{file?, prerequisites?, env?, ports?, setup?, services?,
ready?, preview?, verify?, cleanup?}`. `file: true` reads
`conductor.manifest.json` from the crew's directory at each tree's commit, with
nothing else set. `prerequisites`, `setup`, `verify` and `cleanup` are at most
8 steps each and `services` at most 4, each step `{id, argv,
timeoutSeconds?}` held to a check's bounds; left out, a prerequisite's
timeout is 60 s, a setup or verify step's 600 s and a cleanup step's 60 s, and
a service has none (it runs as long as its tree; `timeoutSeconds` is refused
on it). `ready` is at most 8 probes `{id, argv | url, timeoutSeconds?,
intervalSeconds?}`, never both `argv` and `url`, the timeout 1 to 3600 (60
when left out) and the interval 1 to 60 (2). Every id matches a check's,
is unique across the manifest's lists and is none of the tasks' check ids (a
legacy `setup` keeps its own rule, unique within `setup`). `env` is at most
32 names of the host's variables, each matching
`^[A-Za-z_][A-Za-z0-9_]{0,127}$`, unique, never `CONDUCTOR_*` and never a
loader (`LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`): the manifest
holds names, the values stay the host's, and a name leaves the host only when
the server's `envPassthrough` allows it (Project manifests, under Managed
runs). `ports` is at most 8 names matching
`^[a-z][a-z0-9_]{0,15}$`, unique, each `CONDUCTOR_PORT_<NAME>` (its upper
case) in a tree. `${CONDUCTOR_PORT_<NAME>}` and `${CONDUCTOR_STATE_DIR}` are
placeholders, expanded literally inside one argv element, a probe's `url` and
`preview` and nowhere else; one naming no port of the manifest is refused, and
so is any in a prerequisite ("a prerequisite runs before any tree: no
placeholders"). A probe's `url` (at most 2048 bytes) and `preview` (at most
512) are `http` or `https` on `127.0.0.1`, `localhost` or `[::1]`, with a port
of 1 to 65535 or a port placeholder, and no space or control character. A
manifest is at most 64 KiB as JSON as the server writes it: compact, `<`, `>`
and `&` as they are, U+2028 and U+2029 escaped (six bytes each), and a lone
surrogate's escape read as U+FFFD (three). `conductor.manifest.json` is read at a
commit, a regular file of at most 256 KiB, decoded strictly (an unknown field
refused), and says `"schema": 1` (a larger one needs a newer Conductor);
a crew's manifest leaves `schema` out. `manifestRevision` is the server's count
of the crew's manifest changes, read with the crew and refused in a create or
update body as `id` is: a new crew starts at 1 with a manifest (a `setup`
counts) and 0 without; a save that changes the manifest, a removal included,
makes it one more, and one that does not leaves it, so a number names one
manifest of a crew for good. Every phase of a manifest runs, from the crew or
from `conductor.manifest.json` (Project manifests, under Managed runs): its
prerequisites, its setup phase, its services and its `ready` probes, its
`preview`, its `env` names, each tree's ports, state directory and
placeholders, its `verify` steps after a result's checks and its `cleanup`
steps at each tree's end.

### Crew runs

`POST /api/crews/{id}/launch` starts a run: every member becomes an ordinary
server session, created as `POST /api/sessions` creates one (its `cwd` through
the same check, its agent from the catalog, its hooks injected), named after the
member and tagged with `crew`. Its process also receives `CONDUCTOR_CREW`,
`CONDUCTOR_RUN`, `CONDUCTOR_MEMBER` and `GOAL`. A run's `id` is `<crew id>-<8
hex>`. Members that start `immediately` start at launch; an `after` member
starts when the member it names first reports `done` once its own prompt is
typed; a `manual` member waits for its start route. With `isolation: worktree`,
`cwd` is the top of a git working tree or a directory in one, and each member
gets `git -C <cwd> worktree add --no-checkout -b crew/<run>/<member>
<cwd>/.conductor/worktrees/<run>/<member> HEAD` and then `git reset --hard`
in that worktree (with no hook and no filter of the repository's), a
worktree of the whole repository, and starts in the directory of that worktree which `cwd` is of the
repository (`git rev-parse --show-prefix`), made when no commit has a file
there. The first worktree also adds a `.conductor/` line, once, to the file
`git -C <cwd> rev-parse --git-path info/exclude` names (making `info/` when it
is missing), so that the worktrees stay out of the main checkout's `git status`. A `.conductor`
or `.conductor/worktrees` in `cwd` that is a symbolic link is refused before git
runs, and so is a member whose directory in its worktree lies through a
symbolic link that a commit holds, before anything is made through it.
Conductor never deletes a worktree or a branch.

A launch, a start or an added member answers once the member's session exists;
the member is `starting` until its prompt is typed, then `running`. A member is
ready for its prompt when its agent reports `needs_input` or `done`, or after
one second without output that follows its first output and at least two seconds
after its start, checked every 250 ms. Output that only sets the window title
(OSC 0 or 2: a spinner in the title) is not output for this. A program that
has turned bracketed paste on and then off again is between screens (Claude
Code does so while it loads, and what is typed then loses its Enter) and is not
ready until it turns it on again or reports `needs_input` or `done`. After 60
seconds the prompt is typed anyway and the run log says so. A trust question on
the member's screen (`source:"trust"`, see Attention) holds the prompt
instead: the member's session shows `needs_input` with the question's words,
the run log says once that the member asks it, the 60 seconds do not run, and
once a person has answered it with Enter the wait for readiness starts over.
The prompt, `$GOAL` and `${GOAL}` replaced by the goal, is one line, as a
handoff and a broadcast are (each line break, carriage return and tab, in the
prompt or in the goal, becomes a space; the crew keeps the prompt as written),
submitted as Attention describes: the text, as a bracketed paste while the
program has that mode on, then a carriage return 250 ms later. For an agent
whose hooks report `working` as it takes a prompt (Claude Code with the `hook`
signal), a prompt not taken within 3 seconds of its carriage return (no change
of attention, other than typing's, stamped after it) gets one more carriage
return, unless the session is `needs_input` by then; the run log says so. The
member is prompted as its carriage return is written: a `done` whose
`attention` entry is stamped after that counts for the members after it, and
one stamped before it never does, however late the entry reaches the run: it
is the idle state the prompt has not reached. When a question comes up during
the 250 ms (a new `needs_input`), the carriage return is left out: the text
waits in the agent's input, is never typed again, the member is `running` and
prompted as of then, and the run log says to press Enter in its terminal once
the question is answered. A member whose process ends first gets no prompt: it
is `ended` with its exit in `error`, and the run goes on; a member whose prompt
cannot be typed ends alone, its session stopped, the reason in `error`. When a
member's session cannot be created at launch, the ones started are stopped and
no run is kept; when the run is stopped while its sessions start, it stays,
stopped. A member whose start fails in a run that goes on stays in the run,
`ended`, and keeps its name: `POST /api/runs/{run}/members`
with that name is refused as a name used twice. A run is `{id, crewId, name,
goal, cwd, isolation, startedAt, stoppedAt?, members, log, state, needsInput,
yolo}`, a member `{name, agentId, start, sessionId?, branch?, worktree?,
status, startedAt?, endedAt?, error?, needsInput?, agentSession?, diff?,
prompt?, args?, base?}` with `status` `pending`, `starting`, `running` or
`ended`, and `prompt`, `args` and `base` (the commit its worktree branched
from) as it ran, which a resume from the run's record uses. A run's `state`
is derived as it is read: `stopped` once stopped; `finished` when every member
has ended and none is pending; `needs_input` while the session of a starting
or running member is `needs_input` (`needsInput` counts them, and each such
member has `needsInput: true`; a member whose own question outlasts its
turn's `done`, see Attention, is one, while the members that start after it
start at that `done`); `running` otherwise (a member's `done` keeps it
running: a done agent is idle, not gone). `yolo` is the run's yolo choice,
fixed at launch. A member's `agentSession` is the one its latest session last
showed (see Attention), kept after that session leaves the server, for
`POST /api/runs/{run}/members/{name}/resume`;
`diff` counts the lines of tracked files the member's worktree adds and removes
against the commit it began from, committed or not (`git diff --shortstat
<base> --`, read at most every 10 s, the last value kept when a read fails);
untracked files do not count, and a branch merged into the member's (main, say)
counts with it. `log` is the run's own, at most 200 activity entries (below);
the `status` entry of a delivered handoff (`handoff delivered from a to b`)
carries `byName` (who sent it) and `to` (who got it), which the crew graph
draws from.
Runs live in memory: a server restart forgets them, and past 100 runs a launch
forgets the oldest with nothing running.

A `handoff` event a member reports (see Events) goes to the member of its run
that `to` names. When that member is `running` and not `needs_input`,
Conductor submits `Handoff from <member>: <message>` into its session (the
text, then its carriage return 250 ms later, as a prompt is), recorded as an
`input` entry by `crew`; the line breaks and tabs the message keeps become
spaces, so a handoff is one line.
While the member waits for input, or is still `starting`, the handoff waits
for it: at most 10 wait per member, and another drops the oldest. They are
typed in order, one line each, once the member runs and no longer waits for
input, looked at again whenever its session records an entry or changes (a
prompt cleared records no entry, only a change) and when the member starts
running. The session looks at `needs_input` in the same step in which it
finds the prompt an input would answer, so a handoff never answers a prompt:
one the session shows when a handoff is about to be typed sends the handoff
back to the head of the queue, and one raised in the 250 ms before its
carriage return leaves the handoff typed without it, noted in the run log and
never typed again. A handoff's Enter answers no question: a line naming no
question resolves nothing (see Answering a question).
A handoff to a name no member of the run has, to the member that reports it,
or to a member with no session yet or whose session has ended, is only noted
in the run log, and so is each waiting handoff dropped when its member's
session ends or the run stops. A handoff from a session outside any run, or
reported as its run stops, is an event like any other and nothing more.

`POST /api/runs/{run}/broadcast` types a line into the members of the run
that `members` names, a name given twice once, or into every member when it
is empty. The text has its line breaks and tabs made spaces, as a handoff's
message has, its other control characters dropped and surrounding space
trimmed; what is left must not be empty or over 4096 bytes (`400
invalid_request`). It is submitted to the members together, each as a prompt
is (the text, then its carriage return 250 ms later), and the submissions go on
for up to 15 s when the client goes away, so that none is cut between its text
and its carriage return; each is recorded as an
`input` entry by `byName`, cleaned as a viewer's display name is (`guest` when
empty; the web client sends the display name, or the server's user from
`GET /api/whoami` when none is set). A member is skipped, and listed in `skipped` with the reason, when its
session waits on a prompt (`needs_input`: as for a handoff, the session looks
at its state in the same step in which it finds the prompt the text would
answer, so a broadcast never answers a prompt), when it is not `running`
(`not_running`: `pending`, `starting` with its prompt not typed yet, or
`ended`), or when no member of the run has the name (`unknown`). A write to a
member's process that fails before the text is written, as it does when the
process has just exited, is reported as `not_running` too; the server logs it,
without the text. A member where a question comes up in the 250 ms before the
carriage return, or whose carriage return cannot be written, gets the text
without it (`no_enter`). `sent` and `skipped` keep the order of `members`, or
of the run.

A run link (`POST /api/runs/{run}/links`) grants its role on the session of
every member of the run, one added later included, and on no other session:
the viewer WebSocket, `GET /api/sessions/{id}` and the file route take its
token as they take a session link's for its session, and `GET
/api/join/{token}` answers the run and its members. A run link is listed,
capped at 100 and revoked through its run alone; revoking it closes the
viewers attached through it on every session (`4403`), a member's whose
session ended included (a run link keeps its viewers there until then):
they are written nothing more. Creating and revoking
one are noted in the run log; revoking a revoked link answers `204`, logs
nothing and closes any viewer still attached through it. When the server
forgets the run (past 100 runs, the oldest with nothing running), its links
go with it, as a session's go with the session:
they open nothing, the join route answers `404 invalid_link`, and the viewers
still attached through them, to the ended sessions of its members, are closed
with `4403` as on a revoke. The join route answers `404 run_gone` only when
the run is forgotten as the link is being resolved. A restart forgets links
and runs alike.

The run log is the run's own record beside its sessions' activity, oldest
first; each entry is an activity entry of type `status`, `error` or `link`
whose `message` is one of these:

| Entry | `type` | `message` |
|---|---|---|
| launched | `status` | `launched <crew>: <n> members, <k> starting now` |
| member started | `status` | `<member> started` |
| member joined | `status` | `<member> joined the run` (added mid-run) |
| not ready | `status` | `<member> was not ready after 1m0s: typing its prompt anyway` |
| prompt typed | `status` | `typed <member>'s prompt` |
| trust question | `status` | `<member> asks "<question>": answer it in <member>'s terminal; its prompt waits`, once each time the question holds the prompt |
| prompt without its Enter | `error` | `typed <member>'s prompt without its Enter: <member> waits on a question; answer it, then press Enter in its terminal` |
| Enter again | `status` | `pressed Enter again for <member>: it did not report taking its prompt within 3s` |
| no yolo recipe | `status` | `<member>: yolo is on, but <agent name> has no yolo recipe: launched without one` |
| resumed | `status` | `<member> resumed its conversation` |
| relaunched | `status` | `<member> started anew` (its prompt is typed again once it is ready) |
| ended before its prompt | `status` | `<member> ended before its prompt was typed: <how it ended>` |
| could not start | `error` | `<member> could not start: <why>` |
| done | `status` | `<member> is done: starting <members>` |
| handoff queued | `status` | `handoff queued from <a> to <b>: <b> is waiting for input`, or `: <b> has not had its prompt yet`; see below |
| handoff delivered | `status` | `handoff delivered from <a> to <b>` |
| handoff without its Enter | `error` | `handoff from <a> to <b> typed without its Enter: <b> waits on a question; answer it, then press Enter in its terminal`, or `: <the error>` when the run stopped or the write failed during the pause |
| handoff dropped | `error` | `handoff dropped from <a> to <b>: <why>`, where why is `10 already waiting` (the oldest waiting is dropped), `<b> is not running`, `the run is stopped` or the error writing it |
| handoff to unknown member | `error` | `handoff to unknown member "<name>" from <a>` |
| handoff to itself | `error` | `handoff to itself from <a>` |
| handoff to a member that is not running | `error` | `handoff to a member that is not running, from <a> to <b>` |
| exclude | `error` | `could not add .conductor/ to the repository's info/exclude: <why>`, at most once a run |
| stopped | `status` | `stopped` |
| link created | `link` | `link created: <label> (<role>)`, the label `unlabelled` when there is none, as for a session's link |
| link revoked | `link` | `link revoked: <label>`, likewise |
| view link refused | `error` | `the view link could not be created: <why>` (at launch) |

Every handoff a member reports while its run is not stopping is noted once as
delivered, dropped, to an unknown member, to itself or to a member that is not
running. A handoff that waits is noted as queued once, when it is found
waiting rather than when it arrives: Conductor looks at a member's queue as
handoffs arrive and as the member changes, and notes every handoff in it not
noted yet when the member has not had its prompt, or is `needs_input` as the
handoff at the head of the queue is about to be typed. A handoff dropped to
make room before it was found waiting is noted as dropped only.

Limits of runs, beside those of crews above: a run has at most 12 members (an
added one included), a log of at most 200 entries (the oldest goes first) and
at most 100 links; the server keeps 100 runs, and a launch past that forgets
the oldest with nothing running, never one whose launch has not answered yet;
at most 10 handoffs wait for a member; a broadcast is at most 4096 bytes once
made one line; and whatever Conductor
submits into a session, a prompt, a handoff or a broadcast, is one write of at
most 32756 bytes (32 KiB with the paste markers) and then its carriage return,
250 ms later. A launch, a start, an added or resumed member has 2 minutes to
make its worktrees and start its sessions, a broadcast's submissions 15
seconds, and a stop 30 seconds.
A member's diff is read at most every 10 s, four reads at a time, and a member
not ready for its prompt gets it after 60 s.

An ended member can be resumed (`POST /api/runs/{run}/members/{name}/resume`,
or `POST /api/sessions/{id}/resume` on its session): a new session in its
working directory, its worktree and branch untouched, tagged with the run and
launched with the run's yolo choice. The session takes the member's name,
whatever the session it follows was launched as (a session adopted as a
crew's `self` included), unless a person renamed that session (`PATCH
/api/sessions/{id}`, or the session it was itself resumed from) and it is
still listed: then it takes the name they gave, as a member of a run resumed
as a new run does. The member keeps its own name in the run. It resumes the member's agent session
when the agent has a `session` recipe and that session had a turn, and gets no
prompt; otherwise it starts anew and its role prompt is typed again once it is
ready, and only a `done` after that prompt counts for the members after it. A
member of a stopped run is not resumed (`409 run_stopped`).

#### Managed runs

A crew with `tasks` launches a managed run (docs/plans/agent-harness-order.md;
#334): no member starts by a rule, and the run dispatches its tasks as
attempts, in the crew's order, as their needs have results: at most four
attempts in flight per run, three per task, one per member, whose previous
session is stopped first. An attempt is a fresh member session, named
`<member> · <task> #<n>`, in the worktree
`<cwd>/.conductor/worktrees/<run>/<task>.<n>` on the branch
`crew/<run>/<task>.<n>`, made at the commit the task starts from: its first
need's result commit, the run's base (`HEAD` of `cwd` at launch), or its own
last attempt's result when that failed a check. A result refused before its
checks ran (its checkout refused: the crew's directory a symbolic link or a
file, or a commit git would not check out; or the crew's setup, its
project manifest's `setup` phase or its legacy `setup`, failing or changing
tracked files there) is no tree to build on: the next attempt starts where
that attempt started, and its task file names the refused result and its
branch (`previous.result`, `previous.branch`). A work task's result, with
checks or none, is refused at checkout when the crew's directory in its tree
is a symbolic link or a file, so no dependent is made at it. The task is
written as
`.conductor/task.md` and `.conductor/task.json` in the attempt's working
directory (with a `.gitignore` of `*`; refused when the commit tracks
`.conductor/` there, or `.conductor` is not a directory), each kept within
64 KiB: what is needed least is cut (the inputs' summaries, then the
checkpoint, then findings from the last, then the review's summary, the
checks' commands, the inputs after the first, the attempt before's error
past 2,000 characters) into `.conductor/task-more.json` in full, which
`task.json`'s `more` and `cut` and a "Cut to fit" section of `task.md` name.
The crew's own text is never cut: a crew is refused as it is saved when a
task's goal, role and instructions take more than 40 KiB as `task.json`
keeps them, or a task needs the same task twice, so a task file always
fits. A result is refused at submission (`400
bad_result`, nothing changed) when it, or the repair a review's findings
make, is larger than the ledger keeps as JSON. The crew's setup steps
(its project manifest's `setup`, or its legacy `setup`) run in it first (a
step that fails there is not counted: Project manifests below), and the
session's process receives `CONDUCTOR_TASK`,
`CONDUCTOR_ATTEMPT` and `CONDUCTOR_TASK_FILE` beside the crew variables (with
a project manifest, the tree's own names and the manifest's approved `env`
names too: Project manifests below). The
line typed once the session is ready says where the task file is and how to
submit. A `done` finishes nothing (one reminder line is typed per attempt, when
the session is not waiting); a `handoff` is noted in the run log, never typed;
a session that ends before its result fails its attempt, and the task gets its
next attempt while it has one. A place among the server's sessions is reserved
before anything is made: refused at the cap, the task waits as `ready` and
nothing is spent.

A result is the attempt's commit (a review's its verdict): the worktree must
be on the attempt's branch and clean apart from `.conductor/`, `HEAD` must
descend from every input (a review's `HEAD` must be the candidate's), and the
result tree may hold no `.conductor` directory anywhere and no submodule;
otherwise the submit is refused with why, and the attempt goes on. A work
task's checks then run in a detached checkout of the result commit
(`<worktree>.check`), after the crew's setup steps there (what the worker had,
the checks have; a setup step that changes tracked content fails the attempt
at that step, the checkout compared after each),
one at a time across the server (`checks.maxConcurrent`), each recorded
before it starts and after it ends with its exit, timing, log (at most 1 MiB,
its SHA-256 and a tail) and whether it left the checkout as it found it:
after each check the checkout is compared with the commit as git would store
it (content, mode and link target of every tracked path, through an index of
its own that no flag of the checkout's can hide a change from, and `HEAD`
still the commit); a check that cannot run, exits non-zero, times out, moves
the checkout or changes tracked files fails the attempt at that check, and
the rest do not run; untracked outputs are listed with it. A review, or a
task with no checks, succeeds at once. Every change of a run's state is made and written to the ledger in
one transaction of the run's, and the side effect that rests on it (a
dispatch, a session, a line, a check) follows the commit; a run's
transactions commit in order, each with the state as it was; while one
commits, the run as read is one change ahead of the ledger, and a commit
the ledger refuses is undone or, where it cannot be, pauses the run's
scheduling and says so in the run log. The run's `managed.revision` follows
each commit. A submitted result is kept whole in the attempt's row (its
summary, issues, verdict and findings), so a review's findings outlive the
server.

**Project manifests.** A crew's project manifest (its `manifest`, or
`conductor.manifest.json` at a tree's commit with `file`; a legacy `setup` is
its setup phase) makes each tree of a managed run usable, a tree being an
attempt's worktree or the checkout of its result. Its prerequisites run once
per run, in the crew's directory at the run's base, before the first attempt
is dispatched: the launch answers while they run (the run's
`manifest.prerequisites[].state` says `running`), and nothing is dispatched
until they passed; they run again at each unpause (unless a run of them is
under way, whose result stands), in a continuation (one whose every task was
carried ended is finished at its start and runs none, each `pending`, until a
retry reopens it), and when a retry or an acceptance reopens a run whose end
cut a run of them short (`interrupted`).
One that fails pauses the run's scheduling with its words and spends
nothing. Before their commands, the manifest's `env` names are checked, the
values never read beyond their presence nor recorded: each must be in the
server's `envPassthrough` and set in the server's environment, both as the
server started (nothing reloads either), else the run pauses before any
command runs with "the prerequisites failed: NPM_TOKEN is not in the
server's envPassthrough" or "the prerequisites failed: the host has no
NPM_TOKEN: set it where the server starts, start it again and continue the
run", and an unpause alone finds the host as it was; a continuation of the
run, on a server started again with it, checks them again. A prerequisite's
command is given the approved names.

Every tree is resolved before anything is written or runs in it. Its
manifest's `env` names are checked again, as for the prerequisites, so a
manifest at a result commit adds no name the server does not allow or the
host does not have: the tree fails at `manifest` with those words, by the
attribution below. Its ports are drawn on the server's loopback (a bind of
`127.0.0.1:0`, the number taken and the port let go), each leased to the
tree from its `manifest.resolved` until its `tree.ended` is committed: no two
live trees of the server share a number (one another tree holds is drawn again, at most 16
times a port), and so an attempt's worktree and its checkout never do. Its
state directory is `.conductor/state` under its working directory (in a
checkout with `.conductor`'s `.gitignore` of `*` beside it, so the
comparisons after its steps and checks list nothing of it), its own and no
other tree's. Its manifest is expanded: `${CONDUCTOR_PORT_<NAME>}` and
`${CONDUCTOR_STATE_DIR}` are put in literally inside each argv element, a
probe's `url` and `preview`, and the expanded form is held to the bounds the
manifest's own is (an argv element at most 4096 bytes and an argv 8 KiB in
all, a `url` 2048, `preview` 512): one past its bound fails the tree at
`manifest` with the size it expands to ("preview expands to 61,440 bytes,
more than 512"), before anything is written or run in it. A step and a
check run in a tree are given the tree's names, `CONDUCTOR_PORT_<NAME>` (the
port's name in upper case) for each port, `CONDUCTOR_STATE_DIR` (its absolute
path), with a preview `CONDUCTOR_PREVIEW_URL`, and `CONDUCTOR_RUN`,
`CONDUCTOR_TASK` and `CONDUCTOR_ATTEMPT`, and no other `CONDUCTOR_` name, a
token least of all; and the manifest's approved `env` names, and no other
name of the server's passthrough. The attempt's session is given the tree's
names and the approved names beside its own (a session keeps its crew's
names and the passthrough every session has). The setup steps run in
each new worktree, the manifest resolved for it at the attempt's start commit
and the task files written first (`task.json`'s `manifest`, `{source,
revision | digest, ports?, preview?, services?}`, the ports by name, the
preview and the services' ids, and sentences in `task.md` naming the
manifest, the ports, the services, the preview and the state directory),
before the session; and in
the checkout of a result, the manifest read at the result commit (a result
that changes `conductor.manifest.json` is checked by the one it changed),
before the checks, the checkout compared with the result after each step,
before its receipt is finished, so a change fails that step in its receipt
as in the attempt (`setup deps changed tracked files: …`). A step's timeout is 60 s for a
prerequisite and a cleanup step and 600 s for a setup or verify step when it
gives none.

After the setup steps a tree's services start, in order, each in the tree's
working directory with the tree's names, a process group of its own beside
the tree for as long as the tree lives (a service has no timeout): its
receipt (phase `service`) is put `started` before its process and `running`
with its pid, group and start time before the next service starts, and a
`running` row the ledger refuses stops it and fails the tree at it. A
service is a foreground process: one that exits before the tree is ready
fails the tree at it (stage `service <id>`, "web exited 1 before the tree
was ready: …"), a probe's try under way cut and a probe it kept from its
first try left with `tries` 0 and no exit; one that exits 0, then or once
the tree is in use (with no `ready` probe a daemonizer's exit comes after
`tree.ready`), is told "a service must stay in the foreground", for what a
service puts outside its group (a daemon, a `setsid` child) is not the
tree's, neither stopped nor reported. Then the `ready` probes, in order,
each one receipt (phase `ready`) with its `tries`: a command run again
every interval until it exits 0, or a URL fetched every interval until it
answers under 300, one `GET` with the interval as its
timeout, no redirect followed (a `3xx` is the try's answer, so a redirect
off the loopback makes the server fetch nothing there) and the body read
nothing of; a probe that has not passed at its timeout fails the tree at
it (stage `ready <id>`, "page did not pass within 60s (30 tries): answered
302 Found"), and a probe over HTTP keeps its expanded URL as its command
(`url`). In a checkout the tree is compared with the result once more after
the probes, the services having run beside them: a change there is the
phase's (stage `ready`, "tracked files changed while the service web ran
and the probe up was tried: …"), and a change found after a check in a tree
with services names them as its other possible author ("check unit, or a
service of the tree (web), changed tracked files: …"), counted as a failing
check. Then `tree.ready`, the tree's row `ready`, before its session starts
or its first check runs. The attempt's `preview` (in the run's and the
task's reads) is its worktree's preview URL from that `tree.ready` until
the first of the attempt's close and a service of the worktree exiting on
its own (`service.ended`, and a line in the run log: "task build: attempt 2:
service web exited 1 while the tree was in use; the preview is withdrawn",
or for an exit 0 "… exited 0 while the tree was in use: a service must stay
in the foreground; …"; the attempt goes on), and none while the run stops:
it is never offered before the probes passed, behind a service stopped or
gone, or once its port may be another tree's. It is on the server's
loopback and is not proxied.

In a checkout, once the tree is ready, the task's checks run and after them
the manifest's `verify` steps, in order, each a check of the crew's: one at
a time across the server with the checks (`checks.maxConcurrent`), in the
checkout's working directory with the tree's names, the checkout compared
with the result after it, its receipt of phase `verify` naming the manifest
that ran it. One that cannot run, exits non-zero, times out or changes
tracked content fails the attempt at `verify <id>` (its words "exited 3:
…", or beside a service "verify e2e, or a service of the tree (web),
changed tracked files: …"), counted and decided as a failing check is: the
next attempt starts at the result with the step's words in its task file,
and the steps after it do not run. Its events are the steps'
(`step.started`, `step.running` and `step.finished`, phase `verify`, tree
`checkout`), not the checks'. A check or a verify step whose running
receipt the ledger refuses is ended with its group and fails the attempt
at its stage with the ledger's words ("the ledger could not record the
step e2e: …"); one the attempt's close interrupts, under it or while it
waits for its turn, is recorded `interrupted` with what its process left
(its pid, its output and the output's hash and tail).

A tree ends once its attempt's close is committed, however the attempt
closed (a result taken or refused, a failed check, a cancel, a withdrawal,
a stop, a failure that stops the session): its services are stopped first,
then its `cleanup` steps run, in order, in its working directory with its
names (a close that comes while the tree is being made, before its state
directory and task files are there, ends it once they are), each a receipt of phase `cleanup`, every one whatever the one before
it did. One that fails is its receipt and a line in the run log ("task
build: attempt 2: cleanup db exited 1: …", "… the checkout's cleanup …" for
a checkout's), never a change to the attempt's state, and `tree.ended`
names the first that failed (`cleanupFailed`). The cleanup runs under the
run's teardown, which no stop cuts: a stop whose time ran out returns with
`cleaning` above zero while it goes on. The server's shutdown cut-off
(below) kills a cleanup step under way with its group (its receipt
`interrupted`: "slow was interrupted: the server's shutdown cut it"),
starts none after it (the run log names those not run), and `tree.ended`
says `interrupted`, the tree's row too. Nothing is removed: the worktree,
the checkout and the state directory stay.

A step, a service or a probe that fails in an attempt's worktree, or a
manifest that cannot be resolved there (stage `setup <id>`, `service <id>`,
`ready <id>`, `ready` for a `tree.ready` the ledger refused, or `manifest`),
is the host's or the manifest's, not the agent's, in the run and in a
continuation of it: the attempt is `failed` and not counted (`attempt.failed` with `uncounted:
true`; the task `ready` with "attempt 2 could not be made usable at setup
deps: …" and one more attempt allowed), the run's scheduling pauses with the
same words (the run's `pausedWhy`), and the next attempt, once a person
unpauses, starts where that one started, its task file naming the uncounted
attempt (`unusable`) beside the last counted one (`previous`). One at a
checkpoint the agent saved (a commit of its own that nothing verified)
refuses the checkpoint instead, the run not paused (`attempt.failed` with
`checkpoint`): the next attempt starts at once where the checkpointed attempt
started, its task file naming the checkpoint as not usable
(`previous.checkpoint`) and carrying no checkpoint section; a second failure
there pauses. One in the checkout is the result's, counted: a result refused
before its checks (stage `setup <id>`, `service <id>`, `ready <id>`, `ready`
or `manifest`), the next attempt starting where the refused one did. Every step is a receipt in the ledger (the
steps and prerequisites routes above): its phase, its id and `k` (the k-th
run of its id on the attempt, a check's included, so a legacy setup step that
shares a check's id keeps every row), the manifest that ran it
(`crew:<revision>` or `file:<blob>`), where it ran, its command, exit, timing,
log (at most 1 MiB: the output's head, a line saying how many bytes were
dropped, its last bytes) and SHA-256, the words of its failure, and its
tree's ports (a check run in a tree carries them too). A row the
ledger refuses fails its step ("the ledger could not record the step deps:
…"), a refused `running` row ending the step's process first, so nothing runs
that the ledger does not record. The
events: `manifest.resolved {tree, task, attempt, source, revision | digest,
env}` and, for a tree with ports or a preview, `manifest.ports {tree, task,
attempt, ports, preview}` in the same transaction, with the ports' lease;
`step.started {phase, id, k, manifest}`, `step.running {…, pid, group}`
and `step.finished {…, exit, timedOut, duration, error, interrupted}` (a
probe's with its `tries`), each with `task`, `attempt` and `tree` but for a
prerequisite's; `service.ended {task, attempt, tree, id, k, exit, signal,
why}` when a service ends, stopped or on its own, its row `finished`;
`tree.ready {tree, task, attempt, preview}` once a tree's phases passed;
`prerequisites.passed {manifest}` before the first `attempt.reserved`;
`run.paused {paused, why}`; and `tree.ended {tree, task, attempt, why,
cleanupFailed?, interrupted?}` once an attempt's close is committed and the
tree is quiet (its step or check returned, a session being stopped ended),
its services are stopped (`SIGTERM` to each one's group, `SIGKILL` after 5
s, each one's `service.ended`) and its cleanup steps have run, each tree once, also when the task succeeded and its
session stays idle, its ports let go once that transaction is committed
(one the ledger refused, once the run's next commit carries it; nothing is
removed: the state directory stays). A run's stop ends the services of its
live trees itself, beside its sessions and before it records the stop, so a
stop the ledger refuses leaves no service running; its trees end once the
stop is recorded, and a stop whose time ran out returns with `cleaning`
above zero while they do. The server's shutdown kills what is left of a
tree's teardown (a service, a cleanup step) at its deadline less 3 s (the cut-off: 10 s for `conductor
serve`, so 7 s), which leaves the rows their time, and waits for the trees'
ends before the ledger closes. The run's JSON gains `pausedWhy`, `manifest`
(`{source, revision?, digest?, prerequisites: [{id, k?, state, exit?, error?,
duration?}]}`, `state` `pending`, `running`, `passed`, `failed` or
`interrupted`), `cleaning` (its trees being ended: an `outcome` says every
attempt is closed, not that every tree has ended) and each attempt's
`preview` (above). A run whose crew has no manifest and no `setup` makes no
tree and keeps no step receipt.

An agent reads its task and submits its result through its session's task
routes (`GET /api/sessions/{id}/task`, `POST /api/sessions/{id}/task/result`
above), the MCP tools `task_context` and `task_result`, or `conductor task
context` and `conductor task result`, all with the session's own token; the
skill's "Your task" section says so. A review whose verdict is `needs_changes`
reopens the task it reviewed: the task has no result again, its next attempt
starts at the commit reviewed with the review's findings in its task file (a
repair, `task.json`'s `repair` and "Findings to address" in `task.md`), and
what rested on the result is withdrawn, transitively: the review, and every
task that needed the reopened one, is `blocked` again with its result
dropped, an attempt of theirs in flight is `cancelled` at stage `withdrawn`
(its session stopped once that is recorded), and neither a withdrawn attempt
nor a withdrawn result counts against its task's attempts; a task that had
failed or been cancelled stays so until a retry. Every attempt so far of
every such task is taken back, whatever the task's state (the task row's
`withdrawn`, the number of its last attempt taken back): nothing of them is
used again, not their results (as a start, or for Accept anyway, which
answers `409 task_state` with `no attempt since the withdrawal submitted a
commit to accept` when none since did), not where they started after a
refusal, not their checkpoints, not a result being verified again, and not
in the next attempt's task file, which starts at the new input. A repair
such a task carried goes too (its findings were on the old input); the
reopened task keeps its own. The review runs again on the repair. A
task reopened with no attempt left fails instead, keeping the repair: a retry
allows one more attempt, which starts at the reviewed commit with the
findings. The run log says what was reopened and withdrawn, and the events
`task.reopened`, `task.withdrawn` and `attempt.withdrawn` are recorded with
the review's result.

**Commands with receipts.** A person's change to a managed run (pause,
unpause, cancel, retry, accept) is a command: the client mints a `commandId`
(1 to 32 of letters, digits, `.`, `_`, `~` and `-`), the run keeps the
outcome under the admin and that id in the same transaction as the change,
and the receipt comes back as `{receipt: {commandId, intent, revision,
repeat?, status, error?, refusal?, task?, paused?}}`: `status` is `done` or
`failed` (with `error` and `refusal`, for a command the run's state refused,
such as a cancel of a task already finished: `refusal` is the code the
answer carries, `task_state`, `run_stopped`, `run_continued` or
`not_found`, on the first
answer and on every repeat), `task` the task as it stands after the command
(`{id, state, reason?, attempts, result?: {attempt, commit?}}`, the reason
at most 2,000 characters; the run has the rest, so a receipt always fits
what the ledger keeps),
`paused` whether the run's scheduling is, and `revision` the run's ledger
revision after it. The same id sent again with the same content answers with
the same receipt and `repeat: true`, never a second change, also for a
failed one; the same id with other content is `409 command_conflict`.
`expectedRevision`, when given, refuses the command with `409 stale_revision`
once the run's `managed.revision` has moved on: kept as a failed receipt
(`refusal: stale_revision`), which the same id answers with again. A command
is taken in a transaction of its own before it acts (its row `accepted`;
the same id again, other content and the run's cap are answered there, and
nothing changes), then acted on and settled in a second (`done` or
`failed`); a command taken whose settlement the server's end cut off is
`uncertain` after the recovery, and read as such. A run keeps 4,096
commands (`409 too_many_commands`); `GET /api/runs/{run}/commands/{command}`
reads a receipt without acting; a legacy run takes no command (`409
task_state`), and a run continued, or being continued, takes none either
(`409 run_continued`: its continuation owns its tasks, and a change here
would fork their history). The engine's own `PauseRun`, `CancelTask` and `RetryTask` make
the same changes without a receipt.

**After the server's end, a continuation.** When the server starts and
takes the ledger's owner lock, it concludes what the last server left open
(the ledger's recovery): an attempt reserved and not launched is
`uncertain`, one launched, running, submitting or verifying `interrupted`,
a check, a step or a prerequisite under way `interrupted`, a service that
was running `interrupted` and reported, never signalled (a process the last
server started that outlived it is nobody's to signal: a `service.ended` of
its own with `recovered: true` and its pid, group and start time, the
`recovered` event counting them as `Services` and those whose start was not
recorded as `Unconfirmed`, and a line in the server's log naming each one as
possibly alive), a tree whose end
was not recorded ended and `interrupted` (with a `tree.ended` of its own, its
cleanup not run), a command accepted and not settled
`uncertain`, and a run that was running `interrupted`. Then every managed
run whose record lags the ledger (a snapshot cut short by the server's
end), has none, or says a tree is being ended or a prerequisite runs (a
record at the ledger's revision can, written between a commit and the end
of what it finished, and nothing runs after the server's end) is written
again as the ledger has it (`crew.Recovered`):
its state (`interrupted` when the recovery found it running), each task as
its row and its last attempt say (a task whose attempt was interrupted is
`ready` with why, one whose attempt is uncertain `failed` with why), its
prerequisites as their receipts say (the last run of them, a prerequisite it
did not reach `pending`; with no record, the manifest and its prerequisites
as the run's ledger row keeps the one at its base), nothing `cleaning`, the
members' sessions ended; it reads so, read-only, until it is continued. `POST /api/runs/{run}/resume` on a managed run
(interrupted, stopped, or finished with an outcome; the engine's or its
record) makes its continuation (`crew.ContinueRun`): a new run of the crew
as launched, from the same base, named in the old run's `resumedBy` (the
ledger's `continuedBy`) and naming it in `resumedFrom`. Each task is taken
up as its rows say: a result that stood is kept by reference (`tasks[].
importedFrom`, `<run>/<task>/<n>`; no attempt is made, a dependent starts
from it and its task file names the old run's branch); a task failed or
cancelled stays so; a work task whose attempt was interrupted, or stopped
with the run, while its result was being verified gets an attempt of its
own, with no session, that verifies the same commit again (`result.reverified`; the attempt is
not counted against the task); a task whose last attempt's launch is
uncertain is `failed` with why until a retry, since nothing is retried on
its own; the rest is ready or blocked as its needs say, and the attempts
spent before count against every task and number the next (`crew/<new
run>/<task>.<n>` continues the numbering). A repair a review asked for and
the attempts a task was allowed past the bound are the task's row's
(`repair`, `extra`) and carry over, as does `withdrawn`: an attempt taken
back is not the last one, here or found further back along the runs
continued, nor is an attempt past the task row's count; a result imported
before keeps its source (`importedFrom` names the attempt that made it, in
whichever run; a result made since in a run is that run's own) and its
summary, issues and findings through every later continuation, and a
result whose attempt is in none of the runs continued is not imported: the
task is `failed` with `its result's attempt <n> is not in the ledger:
retry it`. The run log says what was kept,
verified again and left for a decision; the continuation is made once
(`409 already_resumed` names it), a run still going is not continued
(`409 run_running`), and a run holding changes the ledger refused (its
commits failed, and failed again as the next carried them) is not
continued until the ledger takes them, which the continuation tries
first (`503 ledger_unavailable`).

**Messages, blockers and checkpoints.** The members of a managed run
exchange messages (`POST /api/sessions/{id}/messages`, the MCP tool
`message_send`, `conductor task send`): a message is kept in the ledger
(`message.sent`) and typed into the recipient's session as one line after
its task line, through the handoff queue, once the recipient's attempt has
one: the intent is recorded before the line (`message.typing`), the
delivery once the line is entered (`message.delivered`), and a line not
typed after all, the recipient's session ended under it, is recorded so
(`message.untyped`) and the message waits for the recipient's next
session; a member not started yet gets it then; a continuation carries every message under its
own rows, and one set out to be typed and not entered before the server
ended is not typed again (`uncertain` in the inbox, said in the run log),
since whether it reached the terminal cannot be told; the recipient reads its
inbox (`GET …/inbox`, `inbox_read`, `conductor task inbox`) and
acknowledges what it has read (`POST …/inbox/ack`, `inbox_ack`,
`conductor task ack`; `message.acked`), and keeps at most 50 messages not
acknowledged. A handoff reported in a managed run is a message to the
member it names. An attempt reports a blocker (`POST …/task/blocked`,
`task_blocked`, `conductor task blocked`): the task carries it
(`tasks[].blocker`, `task.blocked`), the session asks the message as the
agent's own question, and a `working` or `done` report of the attempt, or
its result, clears it (`task.unblocked`). An attempt saves a checkpoint
(`POST …/task/checkpoint`, `task_checkpoint`, `conductor task
checkpoint`): what is done, what remains and notes, tied to the commit its
worktree is at, on the attempt (`attempt.checkpoint`) and in the ledger
(`task.checkpoint`; at most 16 KiB as kept, refused before anything
changes otherwise); the task's next attempt reads it in its task file
("Where the last attempt got to"), and an attempt after one that ended
without a result, or a continuation's first attempt at the task, starts at
the checkpoint's commit, so the work is kept. A review saves no checkpoint
(`400`): it commits nothing, and its attempt always starts at its
candidate; a task whose input is withdrawn by a review loses its
checkpoint with it, since the next attempt starts from the new result. The ledger's schema is
version 3 for the checkpoint column and the messages table; a version 2
database is brought to it as it opens.

The run reports `tasks` (each with its `state`, `reason`, `attempts`, latest
`attempt`, `result` and, in a continuation, `importedFrom`), `paused` and, once every task is terminal, `outcome`
(`succeeded` when all succeeded, `cancelled` when one was cancelled and none
failed, `failed` otherwise): the run is `finished` then, its idle sessions
kept for a look until a stop; a managed run is not finished by its sessions
ending. Each member carries the `task` and `attempt` its latest session is.
A stop ends the attempts in flight (`stopped`; their tasks `ready` again for
a continuation) and waits for the checks, which end with it. Pause, cancel,
retry and the task routes come with the next rows.

### Dispatch

Dispatch is Conductor's own work tracking (`docs/plans/dispatch.md`):
projects, tickets with stable keys (`CND-12`), their discussion and their
history, in one SQLite database the server keeps, `dispatch/dispatch.sqlite`
in the data directory, and its attachments' files beside it in
`dispatch/attachments/`, which the file routes never reach. It is not the
engine's scheduling of a managed run's attempts (Managed runs): it starts
nothing, and a ticket links to a run the engine made. Every Dispatch route
is the admin's, a share link's viewers seeing no ticket, but the two of an
attempt's ticket (below), which take the session's own token too; on a
switchyard each answers `403 switchyard`. Without a data directory every
route answers `503 store_unavailable`; with a store that could not be
opened, `503 dispatch_unavailable`. A store opened read-only (a newer
Conductor wrote it, it fails its check, another writer held it past the
busy timeout as the server started, or the server may not write its file;
the server's log says which) answers its reads as it
can and every write `503 dispatch_unavailable` with why. A failure of the
database is `500 store_failed`, logged. A read takes the query
parameters its row names and no other: any other is `400
invalid_request`, as a body's unknown field is, and so is one that takes
a single value given twice. The parameters are separated by `&` alone, as
a browser reads a form's, so a `;` is part of a value (a search or a name
may hold one), sent as it is or as `%3B`. Replies are written with HTML
escaping off: `<`, `>` and `&` are one byte each, so a text field encodes
to at most twice its bytes (U+2028 and U+2029 are written as six whatever
the escaping), and the largest ticket's read, the largest page of its
comments and the largest project's read, every field and set at its bound
and every text at its escaped worst, are each under 3 MiB.

**Dispatch operations.** Every Dispatch write is a typed operation, as a
managed run's commands are (Commands with receipts, above): beside the
operation's fields the body carries `opId`, which the client mints (1 to 32
of letters, digits, `.`, `_`, `~` and `-`), `expectedRev`, the revision of
the entity it acts on that the client expects (absent or 0 for none), and
`byName`, the display name the store records as `by` (at most 60
characters, no control character; never in the query string). The admin's
operations are kept under the actor `admin`, a run member's under
`agent:<member>` (An attempt's ticket, below). Each is one transaction: the
receipt is looked up (the same `opId` with the same content answers with
the receipt given before, `repeat: true`, and changes nothing; with other
content it is `409 op_conflict`), the revision compared (`409
stale_revision`, nothing changed), the change made with one row of the
store's log per field it set, added or removed, the entity's `rev` bumped
once, the receipt kept, the commit, and then the `dispatch` events. The
receipt is `{opId, intent, status, rev?, id?, key?, repeat?, error?,
refusal?, missing?}`, at most 4 KiB: `status` `done` or `failed`, `rev` the
entity's revision after the operation, `id` and `key` what a create made,
and for a refusal the rule that refused it (`not_found`, `invalid`,
`stale_revision`, `op_conflict`, `close_policy`, `cycle`, `bound`,
`archived`, `run_linked`, `expired` or `key_taken`), its words as `error`
and, for `close_policy`, `missing`, what the close policy lacks. Done, an
operation answers `200 {receipt, <entity>}`, the entity as read after it
(`null` when there is nothing to read: a create's whose receipt was
pruned), `201` for a create made now. Refused by a rule, it keeps its
failed receipt, which the same `opId` answers again (an `op_conflict` keeps
none: the first receipt stands), and answers `409 {error, receipt}`, the
refusal its `error.code`, but `404` for `not_found`. A body the store
would not look at (not one JSON object of the route's fields, an `opId`
that is not one, an `expectedRev` below 0, a display name that is not one,
a path's key that is not a project's or a ticket's, a path's id that is
not a row's, 16 lower-case hex characters) is `400 invalid_request` and
keeps no receipt; a body past its route's bound, 64
KiB but 192 KiB on a ticket's create and edit and 16 MiB and 64 KiB on an
attachment's upload, is `413 bound`; a field past its own bound once
decoded, a file past 16 MiB among them, is the store's `409 bound`. The newest 20,000
receipts are kept: an operation that ran whose receipt was pruned is still
answered `{repeat: true, status: done}`, from the log, whatever its
content, and a refused one runs again. `GET /api/dispatch/ops/{opId}` reads
a receipt without acting, so a client that lost a reply reads it before it
sends again.

**Attachments.** A ticket's files are kept beside the database, each
once per content, named by its SHA-256 (`dispatch/attachments/<the
hash's first two characters>/<the hash>`): two attachments of one content
share a file. An upload writes its file and holds its hash with a lease
before the file is published under it, and the attachment's row is
written, the lease dropped with it, only once the file's name is on the
disk. A removal is a tombstone and deletes no file; the store's sweep,
when the server starts, deletes the files no live attachment and no lease
names, and drops a lease an hour old, a crashed upload's. An upload
suspended for that long between its file and its row (the server stopped,
the machine asleep) finds its lease dropped and is refused `409 expired`,
a failed receipt kept, rather than name a file the sweep may have
deleted: the client sends the file again under a new `opId`. A download is
served from the file the row names, never from a path a client gave.

**An attempt's ticket.** A run belongs to one live ticket at most, the
one whose live `run` link names it, so a session of the run's member has
one ticket to read and comment on, over HTTP with its own token
(`CONDUCTOR_NOTIFY_TOKEN`), as every task route takes it: `GET
/api/sessions/{id}/ticket` and `POST /api/sessions/{id}/ticket/comments`,
which `conductor task ticket` and the MCP tools `ticket_read` and
`ticket_comment` call. A deleted ticket's link holds no run, and with no
live ticket both answer `404 no_ticket`. The comment is a `comment.add`
of the actor `agent:<member>`, its receipts kept under that actor, apart
from the admin's (`GET /api/dispatch/ops/{opId}` reads none of them).
The ticket is found inside the comment's own transaction, after its
receipt is looked up, and the operation's content is the run and the
body, never the ticket: the same `opId` sent again, or sent while the
first is still in flight, answers that operation's receipt wherever the
run's link is now and whether its ticket is live, a repeat naming the
comment on the ticket it went to, or `op_conflict` for another body (a
refused comment's too), never a comment on another ticket or `404
no_ticket`; a new `opId` goes to the ticket that holds the run when it
commits, and with none answers `404 no_ticket` with nothing kept. An
agent reads its ticket and comments on it, nothing more: no route of
its creates, edits, links or closes a ticket.

**The `dispatch` event.** After each commit the server sends one
`dispatch` event per entity the commit changed on `GET /api/events`, the
commit's events together and in the store's order, no other event
between them: `{seq, entity, id, ticket, project}`, `seq` the commit's
last change in the store's log (local to the database), `entity`
`project`, `ticket`, `comment`, `label`, `milestone`, `view` or
`person`, `id` its id (`""` for a person, whose id is its name: the
pickers read the directory again whole), `ticket` the id of the ticket
it belongs to and `project` its project's key, `""` where there is
none. A relation counts for both its tickets, and a label's or a
milestone's delete for it and for every ticket it left. When a commit
changed more than 64 entities of one kind in one project, the server
sends one event for them, where the first of them would have been: that
`entity`, `id` and `ticket` `""`, the project's key and the commit's
`seq`, so a label deleted on 300 tickets is two events, the label's and
its tickets'. An event is at most 160 bytes (the longest is 119: every
value is digits, a word, hex or a key, never escaped). A page reads
again what an event names, or what derives from it; an event with `id`
and `ticket` `""` (but a person's, whose `id` is always `""`) names no
one entity, and a page whose scope is its project reads again what it
shows of the project, a ticket page its ticket among it. A `view` event
whose `project` is `""` is of a view of every project, which every
project's pages offer beside the project's own (`GET
/api/dispatch/views?project=` answers both): a page that shows a
project's views reads them again on it, whatever its project. Like a `run`
event it is state a page cannot do without: a client that cannot keep
up is dropped and reads everything again on its next `snapshot`, never
skipped and kept as an `activity` entry is. A commit's events take one
place in the server's queue for a client, however many there are, so a
commit that changed many entities (a label's delete on hundreds of
tickets) never drops a client that keeps up. It reaches the pages alone,
never a webhook or the run engine.
