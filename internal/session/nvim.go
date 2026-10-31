package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/nvim"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The editor's Neovim bridge (design round 12, F8): one of the owner's own
// connections, on a session whose FileEdit allows editing, opens the real
// `nvim` on this machine for a file; keys go in, the buffer's changes and
// the editor's state come back as nvim_event control frames on the same
// connection.
//
// Neovim runs as the person who runs the session, with their config, and
// takes any command it is typed; nothing inside it can be relied on to keep
// what is typed to the session's folder (Neovim has no restricted mode). So
// it is the owner's alone: a controller through a link or a paste invite
// edits with the page's own keys and saves through FileWrite, which resolves
// every path as a read does.

// ErrNvimUnavailable says `nvim` is missing here, or the session offers no
// Neovim to this connection.
var ErrNvimUnavailable = errors.New("session: neovim is not available")

// nvimEditor is one open editor of one subscription.
type nvimEditor struct {
	id   string
	path string // the resolved file
	// ed is Neovim, once it has started; set under the subscription's
	// nvimMu, and never once the editor is gone.
	ed *nvim.Editor
	// cancel ends Neovim, starting or started.
	cancel context.CancelFunc
	// gone says the editor was closed (its connection went, a close,
	// Neovim's own exit); set under the subscription's nvimMu, so that an
	// open still under way either sees it or is seen by whoever ends it.
	gone atomic.Bool
	// quiet stops its frames: the page closed it (NvimClose), or its
	// connection is going (closeNvims). Neovim's own end (Exited) does not:
	// a Neovim that died may have sent changes the client delivers after its
	// end is told, and the page keeps the text it last held.
	quiet atomic.Bool
	// told says its end (`closed`) was sent, once.
	told atomic.Bool
	// ready gates the frames: nothing before `opened` is sent. It is set
	// under the subscription's nvimMu, as Exited reads it.
	ready atomic.Bool
	// lost is why Neovim ended before the editor was ready (Exited), told
	// once `opened` is: set under the subscription's nvimMu.
	lost string
	// ending ends Neovim once, however many ways ask (end).
	ending sync.Once
	// endWait is nvimEndWait as the editor opened.
	endWait time.Duration
	// late, when set, is told that Neovim had not ended within endWait and
	// its context was cancelled, which kills it (#281: a swap file may be
	// left behind then).
	late func()
}

// nvimEndWait bounds how long end lets Neovim leave on its own (a quit hook
// that hangs holds `:qa!` and the exit) before it ends it with its context:
// as long as the bridge's own close lets it (nvim.exitWait), so that a
// Neovim slow to leave on a loaded machine still writes its state and
// removes its swap files; ended sooner, it leaves a swap file the next open
// of the file stumbles on (#281). Half a minute: a loaded machine makes an
// exit slow, never a hang. A session takes it as it is made
// (Local.nvimEndWait); tests shorten it.
var nvimEndWait = 30 * time.Second

// nvimReap is how long, past nvimEndWait, WaitEditors gives an editor
// ended with its context to be reaped.
const nvimReap = 2 * time.Second

// EditorShutdownWait is what the server's shutdown (CloseAll) gives an
// editor to leave on its own before it is ended through its context: the
// desktop app kills a server still shutting down after 12 s, and a Neovim
// in its own process group would outlive that, so nvimEndWait is too long
// there. The wait is one deadline for every session the shutdown closes
// (CloseAllBy), not one per session (Codex's review of #281). A variable so
// tests can shorten it.
var EditorShutdownWait = 5 * time.Second

// nvimStarted, when set, runs as an editor's Neovim has started, before
// the file is shown: tests make Neovim busy there.
var nvimStarted func(*nvim.Editor)

// WaitEditors waits for the editors ending on the session to have ended,
// at most nvimEndWait and a moment more: those of a connection end on
// their own goroutines as it goes (closeNvims), and a process about to
// exit (the server's shutdown, through CloseAll; a host's end) waits here,
// after its connections have gone, so that no Neovim outlives it.
func (s *Local) WaitEditors() {
	deadline := time.Now().Add(s.nvimEndWait + nvimReap)
	for s.nvimCount.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// endEditorsBy is WaitEditors for the server's shutdown (CloseAllBy): the
// session's editors, every connection's, those of a connection that went
// before included, may leave on their own until deadline; then every one
// still here is ended through its context (its Neovim killed), and a
// moment is given for them to be reaped, so that no Neovim outlives a
// server that must be gone before the app's own grace runs out.
func (s *Local) endEditorsBy(deadline time.Time) {
	for s.nvimCount.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.nvimCount.Load() == 0 {
		return
	}
	s.editorsMu.Lock()
	live := make([]*nvimEditor, 0, len(s.editors))
	for e := range s.editors {
		live = append(live, e)
	}
	s.editorsMu.Unlock()
	for _, e := range live {
		if e.late != nil {
			e.late()
		}
		e.cancel()
	}
	reap := time.Now().Add(nvimReap)
	for s.nvimCount.Load() > 0 && time.Now().Before(reap) {
		time.Sleep(10 * time.Millisecond)
	}
}

// end ends Neovim and reaps it, once: with discard as `:qa!` does, else as
// a lost connection does. Neovim that quit on its own (`:q`) is let finish
// its exit, which removes its swap files last, before it is reaped; the
// context's cancel comes after, never during, so it is not killed halfway,
// unless it has not left within nvimEndWait. A second caller waits for the
// first's end, so no more than that.
func (e *nvimEditor) end(discard bool) {
	e.ending.Do(func() {
		if e.ed != nil {
			left := make(chan struct{})
			go func() {
				defer close(left)
				if discard {
					e.ed.Discard()
				} else {
					e.ed.Close()
				}
			}()
			t := time.NewTimer(e.endWait)
			select {
			case <-left:
			case <-t.C:
				if e.late != nil {
					e.late()
				}
				e.cancel()
				<-left
			}
			t.Stop()
		}
		e.cancel()
	})
}

// nvimAvailable is `nvim` on PATH, checked once a minute at most.
var (
	nvimCheckMu sync.Mutex
	nvimCheckAt time.Time
	nvimCheck   bool
)

func nvimAvailable() bool {
	nvimCheckMu.Lock()
	defer nvimCheckMu.Unlock()
	if time.Since(nvimCheckAt) > time.Minute {
		nvimCheck, nvimCheckAt = nvim.Available(), time.Now()
	}
	return nvimCheck
}

// editAllowed reports whether role may edit files under the FileEdit policy.
func (s *Local) editAllowed(role Role) bool {
	return role == RoleControl && s.opts.FileEdit != "off" && s.fileAllowed(role)
}

// nvimAllowed reports whether sub may use the editor's Neovim: it may edit
// and it is one of the owner's own connections.
func (s *Local) nvimAllowed(sub *Subscription) bool {
	return sub.owner && s.editAllowed(sub.Role)
}

// nvimOwnerOnly reports whether sub may edit but not through Neovim, which
// is kept for the owner's own connections (the welcome's nvimOwnerOnly).
func (s *Local) nvimOwnerOnly(sub *Subscription) bool {
	return !sub.owner && s.editAllowed(sub.Role)
}

// NvimOpen starts an editor for sub on req.Path and answers with an
// nvim_event of kind opened (then the whole buffer as lines), or returns
// why not: ErrNvimUnavailable, why sub's credential no longer stands
// (revoked: asked after the role, before anything else), ErrFileDenied
// (the path or the policy), errGitDir (a file in a repository's .git),
// ErrTooManyRequests (the bounds), errGone (the connection went: nothing
// would close an editor opened then), or an error of Neovim's
// start. ctx bounds the start: Neovim still starting when it ends is ended,
// and so is one whose connection goes meanwhile (closeNvims).
func (s *Local) NvimOpen(ctx context.Context, sub *Subscription, req proto.NvimOpen) error {
	if req.ReqID == "" || len(req.ReqID) > 64 || len(req.Path) > 4096 {
		return errors.New("session: invalid nvim request")
	}
	if !s.editAllowed(sub.Role) {
		return ErrNvimUnavailable
	}
	if err := revoked(sub); err != nil {
		return err
	}
	if !s.nvimAllowed(sub) || !nvimAvailable() {
		return ErrNvimUnavailable
	}
	// Checked as a read is (one of the agent's plans through the plans
	// folder, #289), the session's path still naming its folder; Neovim
	// then opens and writes the file by its path itself.
	target, _, err := s.folderFor(req.Path).resolve(req.Path, newDenySet(s.fileDeny()))
	if err != nil {
		return ErrFileDenied
	}
	if gitMetadata(req.Path, target) {
		// Neovim would write it.
		return errGitDir
	}
	// Neovim outlives the request that opens it: its own context, which
	// ctx ends only while it starts.
	ectx, cancel := context.WithCancel(context.Background())
	id := newNvimID()
	e := &nvimEditor{id: id, path: target, cancel: cancel, endWait: s.nvimEndWait}
	e.late = func() {
		s.log.Warn("neovim did not end within the wait; cancelled", "editor", id, "file", target, "wait", s.nvimEndWait)
	}
	sub.nvimMu.Lock()
	// Registered only while the connection is there: closeNvims, as it
	// goes, marks its editors closed under this lock, and ends each one it
	// finds, a Neovim still starting included.
	gone := sub.nvimsClosed
	select {
	case <-sub.done:
		gone = true
	default:
	}
	if gone {
		sub.nvimMu.Unlock()
		cancel()
		return errGone
	}
	if sub.nvims == nil {
		sub.nvims = map[string]*nvimEditor{}
	}
	if len(sub.nvims) >= proto.MaxNvimPerSub || s.nvimCount.Load() >= proto.MaxNvimPerSession {
		sub.nvimMu.Unlock()
		cancel()
		return ErrTooManyRequests
	}
	sub.nvims[id] = e
	s.nvimCount.Add(1)
	sub.nvimMu.Unlock()
	s.editorsMu.Lock()
	if s.editors == nil {
		s.editors = map[*nvimEditor]struct{}{}
	}
	s.editors[e] = struct{}{}
	s.editorsMu.Unlock()
	// ctx bounds the start, up to the file shown: its end ends Neovim.
	stop := context.AfterFunc(ctx, cancel)
	h := &nvimHandler{s: s, sub: sub, e: e}
	ed, err := nvim.Open(ectx, s.info.Cwd, target, h)
	if err == nil {
		ed.OnKill = func(after time.Duration) {
			s.log.Warn("neovim did not leave within the wait and was killed; its swap file may be left behind", "editor", id, "file", target, "wait", after)
		}
	}
	if err == nil && ctx.Err() != nil {
		// ctx ended as the start finished: the cancel ends Neovim.
		cancel()
		ed.Close()
		err = ctx.Err()
	}
	if err != nil {
		stop()
		cancel()
		s.forgetNvim(sub, e)
		if ctx.Err() != nil {
			return fmt.Errorf("session: neovim did not start in time: %w", ctx.Err())
		}
		return errors.New("session: neovim: " + err.Error())
	}
	sub.nvimMu.Lock()
	if e.gone.Load() {
		// Closed while it started (closeNvims, NvimClose, Neovim's own
		// exit): whoever closed it had no Neovim to end but through its
		// cancel, and it is ended here.
		connGone := sub.nvimsClosed
		sub.nvimMu.Unlock()
		stop()
		cancel()
		ed.Close()
		s.forgetNvim(sub, e)
		if connGone {
			return errGone
		}
		return errors.New("session: neovim: closed as it started")
	}
	e.ed = ed
	sub.nvimMu.Unlock()
	if nvimStarted != nil {
		nvimStarted(ed)
	}
	sub.send(proto.MustControl(proto.NvimEvent{T: proto.CtlNvimEvent, ID: id, Kind: proto.NvimOpened, ReqID: req.ReqID, Path: target}))
	sub.nvimMu.Lock()
	e.ready.Store(true)
	lost := e.lost
	sub.nvimMu.Unlock()
	if lost != "" {
		// Neovim ended between taking its place and `opened`: its Exited,
		// which ends the editor, could tell no one.
		stop()
		if e.told.CompareAndSwap(false, true) {
			sub.send(proto.MustControl(proto.NvimEvent{T: proto.CtlNvimEvent, ID: id, Kind: proto.NvimClosed, Reason: lost}))
		}
		return nil
	}
	if lines, err := ed.Lines(); err == nil {
		h.Lines(0, -1, lines)
	}
	ed.Report()
	// A swap file found while the file opened was told to no one yet.
	if sw := ed.FoundSwap(); sw != nil {
		h.Swap(*sw)
	}
	if !stop() {
		// ctx ended before Neovim had shown the file (busy as it
		// started): its cancel has ended Neovim, and the editor closes.
		h.Exited("error: neovim did not answer in time")
	}
	return nil
}

// NvimSwap answers the swap file one of sub's editors found (round 13, G3)
// with one of the fixed choices; an answer that does not apply (no swap
// file, or recover and delete while its writer runs) comes back as the
// editor's error message.
func (s *Local) NvimSwap(sub *Subscription, req proto.NvimSwap) error {
	switch req.Choice {
	case proto.NvimSwapEdit, proto.NvimSwapRecover, proto.NvimSwapDelete:
	default:
		return errors.New("session: invalid swap choice")
	}
	if !s.nvimAllowed(sub) {
		return ErrNvimUnavailable
	}
	sub.nvimMu.Lock()
	e := sub.nvims[req.ID]
	var ed *nvim.Editor
	if e != nil {
		ed = e.ed
	}
	sub.nvimMu.Unlock()
	if ed == nil {
		return ErrFileDenied
	}
	if err := ed.SwapChoice(req.Choice); err != nil {
		h := &nvimHandler{s: s, sub: sub, e: e}
		h.emit(proto.NvimEvent{Kind: proto.NvimMessage, Text: truncateRunes(err.Error(), proto.MaxNvimLine), MessageKind: "emsg"})
	}
	return nil
}

// NvimInput sends keys to one of sub's editors, or says why they are
// refused (the bounds, the policy, an editor no longer there, the rate),
// which the connection answers with NvimInputRefused. Keys that meet the end
// of the editor's Neovim, on their way (it died before reading them) or as
// they come (its end told, the editor not yet let go), are not refused: that
// end, `closed`, is their word, and the page keeps every key a lost Neovim
// never said it had done over the text it last held, and names them. A
// refusal would be a second word, from another goroutine than the end (the
// keys' call fails as the client's reading ends, and that reading tells the
// end), and a page that had it first took the key off its text as if Neovim
// lived (#429).
func (s *Local) NvimInput(sub *Subscription, req proto.NvimInput) error {
	if len(req.Keys) == 0 || len(req.Keys) > proto.MaxNvimKeys {
		return errors.New("session: invalid nvim input")
	}
	if !s.nvimAllowed(sub) {
		return ErrNvimUnavailable
	}
	if !validNvimID(req.ID) {
		return ErrFileDenied
	}
	sub.nvimMu.Lock()
	e := sub.nvims[req.ID]
	var ed *nvim.Editor
	if e != nil {
		ed = e.ed
	}
	if sub.nvimKeys.rate == 0 {
		// Keys, not posts: sized for typing (proto.NvimKeysPerSecond).
		sub.nvimKeys.rate, sub.nvimKeys.burst = proto.NvimKeysPerSecond, proto.NvimKeyBurst
	}
	ok := sub.nvimKeys.take(time.Now())
	sub.nvimMu.Unlock()
	if ed == nil {
		return ErrFileDenied
	}
	if !ok {
		return ErrTooManyRequests
	}
	if err := ed.InputSeq(req.Keys, req.Seq); err != nil {
		if errors.Is(err, nvim.ErrClosed) {
			// Neovim ended: its end answers the keys.
			return nil
		}
		var full *nvim.InputFullError
		if !errors.As(err, &full) {
			return err
		}
		// The end Neovim's full input did not take is said first, then the
		// part it took is acknowledged: no word that the keys are done
		// comes before the word that some never came.
		sub.send(NvimInputRefused(req.ID, req.Seq, &RefusedKeys{Rest: full.Rest}))
		if len(full.Rest) < len(req.Keys) {
			ed.Acknowledge(req.Seq)
		}
	}
	return nil
}

// RefusedKeys is NvimInput's error when only the end of the keys (Rest) did
// not reach Neovim, its input full: it is ErrTooManyRequests.
type RefusedKeys struct{ Rest string }

func (e *RefusedKeys) Error() string        { return "session: neovim's input is full; keys were not taken" }
func (e *RefusedKeys) Is(target error) bool { return target == ErrTooManyRequests }

// NvimClose ends one of sub's editors: with Discard as `:qa!` does (the
// person chose to drop the changes not written; the swap file goes), else as
// a lost connection does (the swap file of changes not written stays).
func (s *Local) NvimClose(sub *Subscription, req proto.NvimClose) {
	sub.nvimMu.Lock()
	e := sub.nvims[req.ID]
	started := false
	if e != nil {
		e.gone.Store(true)
		e.quiet.Store(true)
		started = e.ed != nil
	}
	sub.nvimMu.Unlock()
	if e != nil {
		e.end(req.Discard)
		if started {
			s.forgetNvim(sub, e)
		}
	}
}

// closeNvims ends every editor of sub (the connection is going), those
// still starting included, and refuses any open for it after (NvimOpen).
// They end on goroutines of their own, each within nvimEndWait, and leave
// the session's count as they have: whoever detaches the connection (a
// host's frame loop among them) waits for no Neovim.
func (s *Local) closeNvims(sub *Subscription) {
	sub.nvimMu.Lock()
	sub.nvimsClosed = true
	for _, e := range sub.nvims {
		e.gone.Store(true)
		e.quiet.Store(true)
		started := e.ed != nil
		go func() {
			e.end(false)
			if started {
				s.forgetNvim(sub, e)
			}
		}()
	}
	sub.nvimMu.Unlock()
}

// forgetNvim takes e off sub's editors and the session's count; ending it
// is end's. An editor closed while still starting (no Neovim yet) is ended
// through its context alone, and its open, which reaps that Neovim, is the
// one to forget it (NvimOpen): it counts until then (WaitEditors).
func (s *Local) forgetNvim(sub *Subscription, e *nvimEditor) {
	sub.nvimMu.Lock()
	if _, ok := sub.nvims[e.id]; ok {
		delete(sub.nvims, e.id)
		s.nvimCount.Add(-1)
	}
	sub.nvimMu.Unlock()
	s.editorsMu.Lock()
	delete(s.editors, e)
	s.editorsMu.Unlock()
}

// nvimHandler turns the editor's reports into frames for the subscription.
type nvimHandler struct {
	s   *Local
	sub *Subscription
	e   *nvimEditor
}

func (h *nvimHandler) emit(ev proto.NvimEvent) {
	if h.e.quiet.Load() || !h.e.ready.Load() {
		return
	}
	ev.T, ev.ID = proto.CtlNvimEvent, h.e.id
	h.sub.send(proto.MustControl(ev))
}

func (h *nvimHandler) Lines(first, last int, lines []string) {
	for _, ev := range proto.NvimLineEvents(h.e.id, first, last, lines) {
		h.emit(ev)
	}
}
func (h *nvimHandler) Cursor(line, col int, mode string, vl, vc int) {
	h.emit(proto.NvimEvent{Kind: proto.NvimCursor, Line: line, Col: col, Mode: mode, VisualLine: vl, VisualCol: vc})
}
func (h *nvimHandler) Mode(mode string) { h.emit(proto.NvimEvent{Kind: proto.NvimMode, Mode: mode}) }
func (h *nvimHandler) Cmdline(show bool, content string, pos int, prompt string) {
	h.emit(proto.NvimEvent{Kind: proto.NvimCmdline, Show: show, Content: truncateRunes(content, proto.MaxNvimLine), Pos: pos, Prompt: prompt})
}
func (h *nvimHandler) Message(text, kind string) {
	h.emit(proto.NvimEvent{Kind: proto.NvimMessage, Text: truncateRunes(text, proto.MaxNvimLine), MessageKind: kind})
}
func (h *nvimHandler) Written(path string) {
	h.emit(proto.NvimEvent{Kind: proto.NvimWritten, Path: path})
	// The write is the person's: Changes and Touched follow it (design 4d, 4e).
	rel := path
	if r, err := filepath.Rel(h.s.info.Cwd, path); err == nil {
		rel = r
	}
	h.s.Record(ActivityEntry{Type: ActivityFile, Op: FileOpWrite, Path: rel, Tool: "nvim", By: h.sub.ID, ByName: h.sub.Name})
}
func (h *nvimHandler) Buffer(path string) {
	h.e.path = path
	h.emit(proto.NvimEvent{Kind: proto.NvimOpened, Path: path})
}
func (h *nvimHandler) Ack(seq uint32, line, col int, mode string, vl, vc int) {
	h.emit(proto.NvimEvent{Kind: proto.NvimCursor, Line: line, Col: col, Mode: mode, VisualLine: vl, VisualCol: vc, Ack: seq})
}
func (h *nvimHandler) Modified(modified bool) {
	h.emit(proto.NvimEvent{Kind: proto.NvimModified, Modified: modified})
}
func (h *nvimHandler) Settled(seq uint32) {
	h.emit(proto.NvimEvent{Kind: proto.NvimSettled, Ack: seq})
}
func (h *nvimHandler) Swap(sw nvim.Swap) {
	info := &proto.NvimSwapInfo{File: sw.File, Pid: sw.Pid, Running: sw.Running, User: sw.User, Host: sw.Host, Modified: sw.Modified}
	if sw.Mtime > 0 {
		info.Mtime = time.Unix(sw.Mtime, 0).UTC().Format(time.RFC3339)
	}
	h.emit(proto.NvimEvent{Kind: proto.NvimSwapFound, Swap: info})
}
func (h *nvimHandler) Exited(reason string) {
	h.sub.nvimMu.Lock()
	ready := h.e.ready.Load()
	if !ready && h.e.lost == "" {
		// Before `opened`: the open tells it once it has (NvimOpen).
		h.e.lost = reason
	}
	h.sub.nvimMu.Unlock()
	if ready && h.e.told.CompareAndSwap(false, true) {
		h.emit(proto.NvimEvent{Kind: proto.NvimClosed, Reason: reason})
	}
	h.sub.nvimMu.Lock()
	h.e.gone.Store(true)
	started := h.e.ed != nil
	h.sub.nvimMu.Unlock()
	// Off this goroutine: reaping waits for the client, which runs this.
	// It leaves the session's count once it has ended (WaitEditors).
	go func() {
		h.e.end(false)
		if started {
			h.s.forgetNvim(h.sub, h.e)
		}
	}()
}

func newNvimID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// nvimIDLen is the length of an editor's id (newNvimID).
const nvimIDLen = 16

// validNvimID reports whether id is one newNvimID could have made: what a
// request names is said back only then, so an answer stays within its
// bounds whatever the request carried.
func validNvimID(id string) bool {
	if len(id) != nvimIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// truncateRunes cuts s to at most n bytes on a rune boundary.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// NvimInputRefused is the nvim_event of kind error for keys that did not
// reach editor id's Neovim (the bounds, its input full, an editor no longer
// there; keys that meet its Neovim's end are not refused, NvimInput): with
// the editor's id and the keys' seq as its ack, so the page knows which.
func NvimInputRefused(id string, seq uint32, err error) []byte {
	ev := refusal(err)
	// Only an id an editor could have: any other names none, and is not said back.
	if !validNvimID(id) {
		id = ""
	}
	ev.ID, ev.Ack = id, seq
	// Only the end of them, when Neovim took the rest: its Text.
	var some *RefusedKeys
	if errors.As(err, &some) {
		ev.Text = truncateRunes(some.Rest, proto.MaxNvimKeys)
	}
	return proto.MustControl(ev)
}

// NvimRefused is the nvim_event of kind error for a refused request: the
// code names the reason (nvim_unavailable, too_many_requests, file_denied;
// revoked or expired for a link that no longer opens the session).
func NvimRefused(reqID string, err error) []byte {
	ev := refusal(err)
	ev.ReqID = reqID
	return proto.MustControl(ev)
}

func refusal(err error) proto.NvimEvent {
	if code, msg, ok := LinkRefusal(err); ok {
		return proto.NvimEvent{T: proto.CtlNvimEvent, Kind: proto.NvimError, Code: code, Message: msg}
	}
	code := proto.ErrCodeFileDenied
	switch {
	case errors.Is(err, ErrNvimUnavailable):
		code = proto.ErrCodeNvimUnavailable
	case errors.Is(err, ErrTooManyRequests):
		code = proto.ErrCodeTooManyRequests
	}
	return proto.NvimEvent{T: proto.CtlNvimEvent, Kind: proto.NvimError, Code: code, Message: err.Error()}
}
