package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/nvim"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// nvimEvents reads the nvim_event control frames a chanSink received, from
// index *from, until one of kind `kind` arrives (or the deadline), returning
// every event seen and advancing *from past them.
func nvimEvents(t *testing.T, sink *chanSink, from *int, kind string, within time.Duration) []proto.NvimEvent {
	t.Helper()
	var seen []proto.NvimEvent
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		frames := append([][]byte(nil), sink.frames[*from:]...)
		sink.mu.Unlock()
		for _, frame := range frames {
			*from++
			f, err := proto.Decode(frame)
			if err != nil || f.Type != proto.TypeControl {
				continue
			}
			var ev proto.NvimEvent
			if json.Unmarshal(f.Payload, &ev) != nil || ev.T != proto.CtlNvimEvent {
				continue
			}
			seen = append(seen, ev)
			if ev.Kind == kind {
				return seen
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no nvim_event of kind %q; saw %+v", kind, seen)
	return nil
}

// A controller on a session that allows editing opens the real nvim on a
// file: the opened event, the whole buffer, a key's change coming back as
// lines, :w writing the file and landing in Activity as the person's file
// event; a view role, the off policy, a path outside the cwd and the bounds
// are refused with their codes; the connection's end closes the editor.
func TestNvimBridgeThroughTheSession(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	s, _ := newLocal(t, dir)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	from := 0
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	evs := nvimEvents(t, sink, &from, proto.NvimLines, 10*time.Second)
	if evs[0].Kind != proto.NvimOpened || evs[0].ReqID != "r1" || !strings.HasSuffix(evs[0].Path, "/a.txt") || evs[0].ID == "" {
		t.Fatalf("opened: %+v", evs[0])
	}
	id := evs[0].ID
	if last := evs[len(evs)-1]; last.First != 0 || last.Last != -1 || len(last.Lines) != 2 || last.Lines[1] != "two" {
		t.Fatalf("the whole buffer: %+v", last)
	}
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: "dd"}); err != nil {
		t.Fatal(err)
	}
	evs = nvimEvents(t, sink, &from, proto.NvimLines, 10*time.Second)
	if last := evs[len(evs)-1]; last.First != 0 || last.Last != 1 || len(last.Lines) != 0 {
		t.Fatalf("dd: %+v", last)
	}
	// The buffer holds a change not written, and says so.
	if ev := nvimEvents(t, sink, &from, proto.NvimModified, 10*time.Second); !ev[len(ev)-1].Modified {
		t.Fatalf("modified after dd: %+v", ev[len(ev)-1])
	}
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: ":w<CR>"}); err != nil {
		t.Fatal(err)
	}
	evs = nvimEvents(t, sink, &from, proto.NvimWritten, 10*time.Second)
	// Written, it no longer does (the event comes with the write, either side of it).
	if !anyModified(evs, false) {
		if ev := nvimEvents(t, sink, &from, proto.NvimModified, 10*time.Second); ev[len(ev)-1].Modified {
			t.Fatalf("modified after :w: %+v", ev[len(ev)-1])
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "two\n" {
		t.Fatalf("file after :w: %q", b)
	}
	var wrote bool
	for _, e := range s.Activity() {
		if e.Type == ActivityFile && e.Op == FileOpWrite && e.Path == "a.txt" && e.Tool == "nvim" && e.ByName == "nate" {
			wrote = true
		}
	}
	if !wrote {
		t.Fatal("the write is not in Activity as the person's file event")
	}
	// Keys at machine speed, one message each and far past the chat's 20 at
	// once, all land: Neovim's keys have their own bound, sized for typing
	// (the shared one dropped every key past the 20th, seen in the Windows app).
	keys := []string{"O"}
	for i := 0; i < 60; i++ {
		keys = append(keys, "x")
	}
	keys = append(keys, "<Esc>", ":w<CR>")
	for i, k := range keys {
		if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: k, Seq: uint32(100 + i)}); err != nil {
			t.Fatalf("key %d of a quick burst refused: %v", i, err)
		}
	}
	nvimEvents(t, sink, &from, proto.NvimWritten, 10*time.Second)
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != strings.Repeat("x", 60)+"\ntwo\n" {
		t.Fatalf("file after a quick burst: %q", b)
	}
	// Refusals.
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r2", Path: "../outside.txt"}); err != ErrFileDenied {
		t.Fatalf("outside the cwd: %v", err)
	}
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: "nope", Keys: "x"}); err != ErrFileDenied {
		t.Fatalf("an unknown editor: %v", err)
	}
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: strings.Repeat("j", proto.MaxNvimKeys+1)}); err == nil {
		t.Fatal("over-long keys accepted")
	}
	view, _ := s.AttachWith(AttachOptions{Role: RoleView, Name: "guest"}, newChanSink(false))
	if err := s.NvimOpen(context.Background(), view, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r3", Path: "a.txt"}); err != ErrNvimUnavailable {
		t.Fatalf("a view role: %v", err)
	}
	// The bound per connection.
	for i := 2; i <= proto.MaxNvimPerSub; i++ {
		if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r4", Path: "a.txt"}); err != nil {
			t.Fatalf("editor %d: %v", i, err)
		}
	}
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r5", Path: "a.txt"}); err != ErrTooManyRequests {
		t.Fatalf("editor %d: %v", proto.MaxNvimPerSub+1, err)
	}
	// The refusal frame names the code.
	var ev proto.NvimEvent
	f, _ := proto.Decode(NvimRefused("r5", ErrTooManyRequests))
	json.Unmarshal(f.Payload, &ev)
	if ev.Kind != proto.NvimError || ev.Code != proto.ErrCodeTooManyRequests || ev.ReqID != "r5" {
		t.Fatalf("refusal: %+v", ev)
	}
	// Detach closes the editors, on their own goroutines: each leaves the
	// connection and the session's count once Neovim has gone. A Neovim
	// that does not leave as its connection closes (one of those that found
	// the first's swap file can wait at that question) is ended once
	// nvimEndWait has passed, so the wait here is longer than that.
	s.Detach(sub)
	deadline := time.Now().Add(nvimEndWait + 5*time.Second)
	for {
		sub.nvimMu.Lock()
		left := len(sub.nvims)
		sub.nvimMu.Unlock()
		if left == 0 && s.nvimCount.Load() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("editors left: %d (session %d)", left, s.nvimCount.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// editorsEnd makes the test wait, as it ends, for s's editors to have
// ended: a connection's editors end on goroutines of their own as it goes
// (closeNvims), and Neovim writes its state in the test's directories as it
// leaves.
func editorsEnd(t *testing.T, s *Local) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(15 * time.Second)
		for s.nvimCount.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})
}

func anyModified(evs []proto.NvimEvent, modified bool) bool {
	for _, ev := range evs {
		if ev.Kind == proto.NvimModified && ev.Modified == modified {
			return true
		}
	}
	return false
}

// Closing an editor whose buffer holds changes not written: as the
// connection's end does, its swap file stays for recovery (the next open
// offers it); with Discard, the person dropped the changes and the swap file
// goes with them. The bound is the session's own: another session's editors
// do not count against it.
func TestNvimCloseKeepsOrDiscardsTheSwapFile(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	swaps := func() []string {
		m, _ := filepath.Glob(filepath.Join(state, "nvim", "swap", "*"))
		return m
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Detach(sub)
	from := 0
	edit := func(req string) string {
		t.Helper()
		if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: req, Path: "a.txt"}); err != nil {
			t.Fatal(err)
		}
		id := nvimEvents(t, sink, &from, proto.NvimOpened, 10*time.Second)
		if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id[len(id)-1].ID, Keys: "ixx<Esc>"}); err != nil {
			t.Fatal(err)
		}
		nvimEvents(t, sink, &from, proto.NvimModified, 10*time.Second)
		return id[len(id)-1].ID
	}
	gone := func(id string) {
		t.Helper()
		for i := 0; i < 100; i++ {
			sub.nvimMu.Lock()
			_, open := sub.nvims[id]
			sub.nvimMu.Unlock()
			if !open {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("editor %s still open", id)
	}
	id := edit("kept")
	s.NvimClose(sub, proto.NvimClose{T: proto.CtlNvimClose, ID: id})
	gone(id)
	kept := swaps()
	if len(kept) != 1 {
		t.Fatalf("closed with changes not written, the swap files: %q", kept)
	}
	os.Remove(kept[0])
	id = edit("dropped")
	s.NvimClose(sub, proto.NvimClose{T: proto.CtlNvimClose, ID: id, Discard: true})
	gone(id)
	if left := swaps(); len(left) != 0 {
		t.Fatalf("discarded, the swap files: %q", left)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\n" {
		t.Fatalf("the file after discarding: %q", b)
	}
	// Neovim's own :q is let finish its exit, so its swap file goes too (it
	// was killed halfway before, leaving one for the next open to stumble on).
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "quit", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	opened := nvimEvents(t, sink, &from, proto.NvimOpened, 10*time.Second)
	id = opened[len(opened)-1].ID
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: ":q<CR>"}); err != nil {
		t.Fatal(err)
	}
	nvimEvents(t, sink, &from, proto.NvimClosed, 10*time.Second)
	gone(id)
	for i := 0; i < 100 && len(swaps()) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if left := swaps(); len(left) != 0 {
		t.Fatalf("after :q, the swap files: %q", left)
	}
	// Another session's editors leave this one's bound alone.
	other, _ := newLocal(t, dir)
	editorsEnd(t, other)
	osub, err := other.AttachWith(AttachOptions{Role: RoleControl, Name: "jane", Owner: true}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Detach(osub)
	if err := other.NvimOpen(context.Background(), osub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "o1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if s.nvimCount.Load() != 0 || other.nvimCount.Load() != 1 {
		t.Fatalf("counts: this %d, other %d", s.nvimCount.Load(), other.nvimCount.Load())
	}
}

// With FileEdit off nobody edits, whatever the role.
func TestNvimRefusedWhenEditingIsOff(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH")
	}
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096, FileEdit: "off"})
	os.WriteFile(filepath.Join(s.info.Cwd, "a.txt"), []byte("one\n"), 0o644)
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate", Owner: true}, newChanSink(false))
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"}); err != ErrNvimUnavailable {
		t.Fatalf("editing off: %v", err)
	}
}

// Neovim is kept for the owner's own connections. A controller through a
// link, or a guest with no link (a paste invite), may still edit (the
// welcome's fileEdit, saves through FileWrite) but is offered no Neovim:
// its welcome says nvimOwnerOnly, opening one is refused, and no key it
// sends reaches a Neovim, not even the owner's open one by its id.
func TestNvimIsKeptForTheOwnersConnections(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	marker := filepath.Join(t.TempDir(), "marker")
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)

	ownerSink := newChanSink(false)
	owner, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate", Owner: true}, ownerSink)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Detach(owner)
	if w := firstControl(t, ownerSink, proto.CtlWelcome); w["fileEdit"] != true || w["nvim"] != true || w["nvimOwnerOnly"] != nil || w["nvimSettled"] != true {
		t.Fatalf("the owner's welcome: %v", w)
	}
	from := 0
	if err := s.NvimOpen(context.Background(), owner, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "o1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	opened := nvimEvents(t, ownerSink, &from, proto.NvimOpened, 10*time.Second)
	ownerEditor := opened[len(opened)-1].ID

	guests := []AttachOptions{
		{Role: RoleControl, Name: "link", LinkID: "l1", LinkLabel: "laptop"},
		// A caller that says owner for a link is overruled by the link.
		{Role: RoleControl, Name: "link2", LinkID: "l2", Owner: true},
		{Role: RoleControl, Name: "pasted", LinkLabel: "paste"},
	}
	// Ordinary edits and a save: no key from such a connection reaches a Neovim, so none changes a file.
	keys := []string{
		"ggdd",
		"iwritten by a guest<Esc>",
		":w<CR>",
		":e other.txt<CR>ianother<Esc>:w<CR>",
		":w " + marker + "<CR>",
	}
	for _, o := range guests {
		sink := newChanSink(false)
		sub, err := s.AttachWith(o, sink)
		if err != nil {
			t.Fatal(err)
		}
		if w := firstControl(t, sink, proto.CtlWelcome); w["fileEdit"] != true || w["nvim"] != nil || w["nvimOwnerOnly"] != true || w["nvimSettled"] != nil {
			t.Fatalf("%s: the welcome: %v", o.Name, w)
		}
		if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "g1", Path: "a.txt"}); err != ErrNvimUnavailable {
			t.Fatalf("%s: open: %v", o.Name, err)
		}
		for _, id := range []string{ownerEditor, "nope"} {
			for _, k := range keys {
				if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: k}); err != ErrNvimUnavailable {
					t.Fatalf("%s: keys %q: %v", o.Name, k, err)
				}
			}
			if err := s.NvimSwap(sub, proto.NvimSwap{T: proto.CtlNvimSwap, ID: id, Choice: proto.NvimSwapEdit}); err != ErrNvimUnavailable {
				t.Fatalf("%s: swap answer: %v", o.Name, err)
			}
		}
		s.Detach(sub)
	}
	// The owner's editor took none of those keys: still open, its file as it was.
	if err := s.NvimInput(owner, proto.NvimInput{T: proto.CtlNvimInput, ID: ownerEditor, Keys: "j"}); err != nil {
		t.Fatalf("the owner's editor: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the marker exists: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\n" {
		t.Fatalf("the file: %q", b)
	}
	if n := s.nvimCount.Load(); n != 1 {
		t.Fatalf("editors on the session: %d", n)
	}
}

// fakeNvim puts on PATH an `nvim` that starts and never answers, as a
// Neovim stuck in its config does; it returns the file its process id is
// written to. Whatever is left of it is killed as the test ends.
func fakeNvim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(dir, nvim.Program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The fake is what nvim.Available finds now: the session's one-minute
	// cache of an earlier check (none, on a machine without Neovim) is
	// dropped, and again when the test ends, so the next check looks afresh.
	resetNvimCheck := func() {
		nvimCheckMu.Lock()
		nvimCheckAt = time.Time{}
		nvimCheckMu.Unlock()
	}
	resetNvimCheck()
	t.Cleanup(resetNvimCheck)
	t.Cleanup(func() {
		if pid := nvimPid(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pidFile
}

// nvimPid is the process id the fake nvim wrote, 0 before it has.
func nvimPid(pidFile string) int {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// gone waits for the process pid to be gone, for at most d.
func gone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// editableLocal is a session whose owner may edit a.txt through Neovim.
func editableLocal(t *testing.T) (*Local, *Subscription) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	return s, ownerOf(t, s)
}

// An editor's open ends with its context while Neovim is still starting:
// Neovim is ended and holds no editor's place.
func TestAnEditorOpenEndsWithItsContextWhileNeovimStarts(t *testing.T) {
	pidFile := fakeNvim(t)
	s, sub := editableLocal(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := returnsWithin(t, 5*time.Second, "the editor's open", func() error {
		return s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	})
	if err == nil {
		t.Fatal("an open that never finished starting succeeded")
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted after the open failed", n)
	}
	// The fake may not have written its pid when the open ran out (a loaded
	// machine): one that got that far is waited for and must be gone; one
	// that never wrote it was ended before it could, or never started, and
	// a pid of 0 is no Neovim running (#281).
	pid := nvimPid(pidFile)
	for deadline := time.Now().Add(2 * time.Second); pid == 0 && time.Now().Before(deadline); pid = nvimPid(pidFile) {
		time.Sleep(20 * time.Millisecond)
	}
	if pid != 0 && !gone(pid, 5*time.Second) {
		t.Fatalf("Neovim (pid %d) still runs after its open ended", pid)
	}
}

// A connection that goes while its editor still starts ends that Neovim:
// the open fails, and nothing of it is left on the session.
func TestAnEditorStillStartingEndsWithItsConnection(t *testing.T) {
	pidFile := fakeNvim(t)
	s, sub := editableLocal(t)
	done := make(chan error, 1)
	go func() {
		done <- s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for nvimPid(pidFile) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Neovim did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Detach(sub)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the open of a connection that went succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the open went on after its connection went")
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted after the connection went", n)
	}
	if !gone(nvimPid(pidFile), 5*time.Second) {
		t.Fatal("Neovim still runs after its connection went")
	}
}

// An open that comes after its connection has gone (a host's open runs
// apart from the frames, and may start as the viewer leaves) is refused:
// no editor is left running on a connection nothing will close.
func TestAnEditorOpenAfterItsConnectionWentIsRefused(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, sub := editableLocal(t)
	s.Detach(sub)
	t.Cleanup(func() { s.closeNvims(sub) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"}); err == nil {
		t.Fatal("an editor opened for a connection that had gone")
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted for a connection that had gone", n)
	}
}

// hangingQuitConfig gives Neovim a config whose quit hook hangs for a
// minute, as a plugin's can.
func hangingQuitConfig(t *testing.T) {
	t.Helper()
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.vim"), []byte("autocmd VimLeavePre * sleep 60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

// openEditor opens a.txt in an editor of sub and returns its id.
func openEditor(t *testing.T, s *Local, sub *Subscription) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	sub.nvimMu.Lock()
	defer sub.nvimMu.Unlock()
	for id := range sub.nvims {
		return id
	}
	t.Fatal("no editor")
	return ""
}

// A Neovim that does not leave (a quit hook that hangs) is ended once
// nvimEndWait has passed: a close waits no longer, and its place is freed.
func TestAnEditorThatDoesNotLeaveIsEndedInTime(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	hangingQuitConfig(t)
	old := nvimEndWait
	nvimEndWait = 300 * time.Millisecond
	t.Cleanup(func() { nvimEndWait = old })
	s, sub := editableLocal(t)
	id := openEditor(t, s, sub)
	returnsWithin(t, 10*time.Second, "the close", func() bool {
		s.NvimClose(sub, proto.NvimClose{T: proto.CtlNvimClose, ID: id, Discard: true})
		return true
	})
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted after the close", n)
	}
}

// Detach does not wait for the connection's editors to leave: they end on
// their own, within nvimEndWait.
func TestDetachDoesNotWaitForAnEditorToLeave(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	hangingQuitConfig(t)
	old := nvimEndWait
	nvimEndWait = time.Second
	t.Cleanup(func() { nvimEndWait = old })
	s, sub := editableLocal(t)
	openEditor(t, s, sub)
	start := time.Now()
	s.Detach(sub)
	if took := time.Since(start); took > 300*time.Millisecond {
		t.Fatalf("Detach waited %v for Neovim", took)
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.nvimCount.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the editor was never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// CloseAll, the server's shutdown, returns once the editors it ends have
// ended, a Neovim that does not leave included: none outlives the server.
func TestCloseAllWaitsForTheEditorsToEnd(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	hangingQuitConfig(t)
	old := nvimEndWait
	nvimEndWait = 500 * time.Millisecond
	t.Cleanup(func() { nvimEndWait = old })
	s, sub := editableLocal(t)
	openEditor(t, s, sub)
	returnsWithin(t, 10*time.Second, "CloseAll", func() bool {
		s.CloseAll(ErrSessionEnded)
		return true
	})
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors left as CloseAll returned", n)
	}
}

// CloseAll returns only once an editor still starting has been ended and
// reaped by its open: its Neovim does not outlive the server.
func TestCloseAllWaitsForAnEditorStillStarting(t *testing.T) {
	pidFile := fakeNvim(t)
	s, sub := editableLocal(t)
	opened := make(chan error, 1)
	go func() {
		opened <- s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for nvimPid(pidFile) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Neovim did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	returnsWithin(t, 10*time.Second, "CloseAll", func() bool {
		s.CloseAll(ErrSessionEnded)
		return true
	})
	if pid := nvimPid(pidFile); syscall.Kill(pid, 0) == nil {
		t.Fatalf("Neovim (pid %d) still there as CloseAll returned", pid)
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors as CloseAll returned", n)
	}
	if err := <-opened; err == nil {
		t.Fatal("the open of a session closed meanwhile succeeded")
	}
}

// An editor's open bounds Neovim's first answers too: a Neovim busy just
// as it has started (a plugin's work) is ended when the open's time runs
// out, the open returns, and the editor says it closed.
func TestAnEditorBusyAsItStartsEndsWithItsOpen(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	nvimStarted = func(ed *nvim.Editor) {
		// A loop, not :sleep, which goes on answering requests.
		go ed.Input(":while 1 | endwhile<CR>")
		time.Sleep(200 * time.Millisecond)
	}
	t.Cleanup(func() { nvimStarted = nil })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	returnsWithin(t, 10*time.Second, "the open", func() error {
		return s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	})
	from := 0
	nvimEvents(t, sink, &from, proto.NvimClosed, 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for s.nvimCount.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the editor was never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wrappedNvim puts on PATH an `nvim` that runs the real one as a child of
// its own, not in its place (an AppImage's runtime does so), with initVim
// as its config; it returns the file the wrapper's process id is written
// to.
func wrappedNvim(t *testing.T, initVim string) string {
	t.Helper()
	real, err := exec.LookPath(nvim.Program)
	if err != nil {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\n'" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, nvim.Program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.vim"), []byte(initVim), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Cleanup(func() {
		if pid := nvimPid(pidFile); pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pidFile
}

// groupGone waits for the process group led by pid to have no process
// left, for at most d.
func groupGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// An open whose context ends while Neovim, started by a wrapper as its
// child, is stuck in its config returns, and ends what the wrapper
// started, not the wrapper alone.
func TestAnEditorOpenThroughAWrapperEndsWithItsContext(t *testing.T) {
	pidFile := wrappedNvim(t, "while 1 | endwhile\n")
	s, sub := editableLocal(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := returnsWithin(t, 10*time.Second, "the open", func() error {
		return s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	})
	if err == nil {
		t.Fatal("an open that never finished starting succeeded")
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted after the open failed", n)
	}
	if pid := nvimPid(pidFile); pid == 0 || !groupGone(pid, 5*time.Second) {
		t.Fatalf("what the wrapper (pid %d) started still runs", pid)
	}
}

// A connection's editor whose Neovim, started by a wrapper as its child,
// does not leave (a quit hook that hangs) is ended in time, with what the
// wrapper started.
func TestAnEditorThroughAWrapperIsEndedWithWhatItStarted(t *testing.T) {
	// A loop, not :sleep, which lets Neovim see its connection close.
	pidFile := wrappedNvim(t, "autocmd VimLeavePre * let g:t = reltime() | while reltimefloat(reltime(g:t)) < 60 | endwhile\n")
	old := nvimEndWait
	nvimEndWait = 300 * time.Millisecond
	t.Cleanup(func() { nvimEndWait = old })
	s, sub := editableLocal(t)
	openEditor(t, s, sub)
	// Neovim itself, the wrapper's child.
	wrapper := nvimPid(pidFile)
	children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", wrapper, wrapper))
	if err != nil {
		t.Skipf("the wrapper's child is not to be found here: %v", err)
	}
	child, _ := strconv.Atoi(strings.Fields(string(children) + " 0")[0])
	if child == 0 {
		t.Fatal("the wrapper started no Neovim")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	s.Detach(sub)
	deadline := time.Now().Add(5 * time.Second)
	for s.nvimCount.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the editor was never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone(child, 5*time.Second) {
		t.Fatalf("Neovim (pid %d), the wrapper's child, still runs", child)
	}
}

// An open whose context ends while Neovim, started by a wrapper that also
// left a helper of its own holding Neovim's output, is stuck as it starts
// returns: the editor's connection is closed with its context, whatever
// holds the output.
func TestAnEditorOpenEndsThoughADescendantHoldsItsOutput(t *testing.T) {
	real, err := exec.LookPath(nvim.Program)
	if err != nil {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("setsid is not on PATH")
	}
	dir := t.TempDir()
	helperPid := filepath.Join(dir, "helper")
	// The helper, in a session of its own, outlives the editor's group and
	// holds the output a minute.
	script := "#!/bin/sh\n'" + setsid + "' sh -c 'echo $$ > \"" + helperPid + "\"; exec sleep 60' &\n'" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, nvim.Program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if pid := nvimPid(helperPid); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.vim"), []byte("while 1 | endwhile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, sub := editableLocal(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := returnsWithin(t, 10*time.Second, "the open", func() error {
		return s.NvimOpen(ctx, sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	}); err == nil {
		t.Fatal("an open that never finished starting succeeded")
	}
	if n := s.nvimCount.Load(); n != 0 {
		t.Fatalf("%d editors counted after the open failed", n)
	}
}

// exitingWrapper puts on PATH an `nvim` that starts the real one in the
// background, its own input kept (an asynchronous command's would be
// /dev/null), and exits at once, with initVim as Neovim's config; it
// returns the file Neovim's process id is written to.
func exitingWrapper(t *testing.T, initVim string) string {
	t.Helper()
	real, err := exec.LookPath(nvim.Program)
	if err != nil {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	dir := t.TempDir()
	childPid := filepath.Join(dir, "child")
	script := "#!/bin/sh\nexec 3<&0\n'" + real + "' \"$@\" <&3 &\necho $! > '" + childPid + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, nvim.Program), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.vim"), []byte(initVim), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Cleanup(func() {
		if pid := nvimPid(childPid); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return childPid
}

// detachAndWait detaches sub and waits for its editors to have ended.
func detachAndWait(t *testing.T, s *Local, sub *Subscription, within time.Duration) {
	t.Helper()
	s.Detach(sub)
	deadline := time.Now().Add(within)
	for s.nvimCount.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the editor was never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A wrapper that leaves Neovim running as it exits, Neovim's quit hook
// hanging: the editor's end ends Neovim too, though the process it started
// is long gone, before the editor leaves the session's count.
func TestAnEditorWhoseWrapperExitedFirstIsEndedWithNeovim(t *testing.T) {
	// A loop, not :sleep, which lets Neovim see its connection close.
	childPid := exitingWrapper(t, "autocmd VimLeavePre * let g:t = reltime() | while reltimefloat(reltime(g:t)) < 60 | endwhile\n")
	old := nvimEndWait
	nvimEndWait = 300 * time.Millisecond
	t.Cleanup(func() { nvimEndWait = old })
	s, sub := editableLocal(t)
	openEditor(t, s, sub)
	child := nvimPid(childPid)
	if child == 0 {
		t.Fatal("the wrapper started no Neovim")
	}
	detachAndWait(t, s, sub, 10*time.Second)
	if !gone(child, 5*time.Second) {
		t.Fatalf("Neovim (pid %d) still runs after its editor ended", child)
	}
}

// Neovim ends after the editor took its place and before its opened event
// (here it quits, as a config may as it starts): the page is told it opened
// and then that it closed, with why, never left with an editor that answers
// nothing. Its end, reported before the editor was ready, was told to no one.
func TestAnEditorEndedAsItOpensSaysItClosed(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	nvimStarted = func(ed *nvim.Editor) {
		_ = ed.Input(":qa!<CR>")
		// Its end is reported (the editor's place marked gone) before the open goes on.
		deadline := time.Now().Add(10 * time.Second)
		for {
			sub.nvimMu.Lock()
			ended := true
			for _, e := range sub.nvims {
				if e.ed == ed && !e.gone.Load() {
					ended = false
				}
			}
			sub.nvimMu.Unlock()
			if ended {
				return
			}
			if time.Now().After(deadline) {
				t.Error("the end was never reported")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Cleanup(func() { nvimStarted = nil })
	err = returnsWithin(t, 10*time.Second, "the open", func() error {
		return s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	})
	if err != nil {
		t.Fatalf("the open: %v", err)
	}
	from := 0
	evs := nvimEvents(t, sink, &from, proto.NvimClosed, 10*time.Second)
	if evs[0].Kind != proto.NvimOpened {
		t.Fatalf("the events: %+v", evs)
	}
	if closed := evs[len(evs)-1]; closed.Reason != "quit" || closed.ID != evs[0].ID {
		t.Fatalf("closed: %+v", closed)
	}
}

// A Neovim that died may have sent changes the client delivers after its
// end is told: they still reach the page, which keeps the text a lost
// Neovim last held; the end is told once. A close the page asked for (or the
// connection going) stops its frames.
// Keys Neovim's full input took only in part: the refusal of the rest is
// sent before any word that keys up to them are done, so the page never takes
// the refused end for done (the real Neovim, its input filled: a plugin's
// vim.wait in InsertCharPre lets it answer between the keys it reads).
func TestARefusalComesBeforeTheWordThatItsKeysAreDone(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "nvim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "nvim", "init.lua"), []byte("vim.api.nvim_create_autocmd('InsertCharPre', { callback = function() vim.wait(20) end })\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Neovim, busy with the keys it took, is ended soon after the test.
	old := nvimEndWait
	nvimEndWait = 300 * time.Millisecond
	t.Cleanup(func() { nvimEndWait = old })
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	id := openEditor(t, s, sub)
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: "i", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	from := 0
	nvimEvents(t, sink, &from, proto.NvimSettled, 10*time.Second)
	refused := uint32(0)
	keys := strings.Repeat("x", 250)
	for seq := uint32(2); seq < 200 && refused == 0; seq++ {
		if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: keys, Seq: seq}); err != nil {
			t.Fatal(err)
		}
		sink.mu.Lock()
		for _, frame := range sink.frames[from:] {
			if f, err := proto.Decode(frame); err == nil {
				var ev proto.NvimEvent
				if json.Unmarshal(f.Payload, &ev) == nil && ev.Kind == proto.NvimError && ev.ID == id {
					refused = ev.Ack
				}
			}
		}
		sink.mu.Unlock()
	}
	if refused == 0 {
		t.Fatal("Neovim took every key: the test filled nothing")
	}
	time.Sleep(time.Second)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, frame := range sink.frames[from:] {
		f, err := proto.Decode(frame)
		if err != nil {
			continue
		}
		var ev proto.NvimEvent
		if json.Unmarshal(f.Payload, &ev) != nil || ev.ID != id {
			continue
		}
		if ev.Kind == proto.NvimError {
			if ev.Ack != refused || ev.Text == "" || !strings.HasSuffix(keys, ev.Text) {
				t.Fatalf("the refusal: %+v", ev)
			}
			return
		}
		if ev.Kind == proto.NvimSettled && ev.Ack >= refused {
			t.Fatalf("keys up to %d said done before the refusal of %d", ev.Ack, refused)
		}
	}
	t.Fatal("no refusal among the frames")
}

func TestARefusedInputNamesItsEditorAndItsKeys(t *testing.T) {
	id := newNvimID()
	f, err := proto.Decode(NvimInputRefused(id, 7, ErrTooManyRequests))
	if err != nil {
		t.Fatal(err)
	}
	var ev proto.NvimEvent
	if err := json.Unmarshal(f.Payload, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Kind != proto.NvimError || ev.ID != id || ev.Ack != 7 || ev.Code != proto.ErrCodeTooManyRequests {
		t.Fatalf("the refusal: %+v", ev)
	}
	// Only the end of the keys refused, Neovim's input full: the refusal says which.
	f, _ = proto.Decode(NvimInputRefused(id, 8, &RefusedKeys{Rest: "yz"}))
	ev = proto.NvimEvent{}
	_ = json.Unmarshal(f.Payload, &ev)
	if ev.ID != id || ev.Ack != 8 || ev.Text != "yz" || ev.Code != proto.ErrCodeTooManyRequests {
		t.Fatalf("the partial refusal: %+v", ev)
	}
	// An id no editor could have is not said back: the refusal names none, and stays within a control frame's bound whatever the
	// request carried, the end of its keys at their longest besides.
	for _, other := range []string{"e1", strings.ToUpper(id), id + "0", strings.Repeat("a", proto.MaxControl)} {
		rest := strings.Repeat("\x01", proto.MaxNvimKeys)
		f, err := proto.Decode(NvimInputRefused(other, 9, &RefusedKeys{Rest: rest}))
		if err != nil {
			t.Fatalf("an id of %d bytes: %v", len(other), err)
		}
		ev = proto.NvimEvent{}
		_ = json.Unmarshal(f.Payload, &ev)
		if ev.ID != "" || ev.Ack != 9 || ev.Text != rest {
			t.Fatalf("an id of %d bytes: %+v", len(other), ev)
		}
	}
	// Keys for such an id reach no editor.
	s, _ := newLocal(t, t.TempDir())
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: strings.Repeat("a", proto.MaxControl), Keys: "x"}); err != ErrFileDenied && err != ErrNvimUnavailable {
		t.Fatalf("keys for an id no editor could have: %v", err)
	}
	// A refused open still carries its request's id alone.
	f, _ = proto.Decode(NvimRefused("r1", ErrNvimUnavailable))
	ev = proto.NvimEvent{}
	_ = json.Unmarshal(f.Payload, &ev)
	if ev.ReqID != "r1" || ev.ID != "" || ev.Code != proto.ErrCodeNvimUnavailable {
		t.Fatalf("the open's refusal: %+v", ev)
	}
}

func TestALostNeovimsLastChangesComeAfterItsEnd(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	e := &nvimEditor{id: "e1", cancel: func() {}, endWait: time.Second}
	e.ready.Store(true)
	h := &nvimHandler{s: s, sub: sub, e: e}
	h.Exited("error: neovim ended")
	h.Exited("error: msgpack/rpc: session closed")
	h.Lines(0, 1, []string{"late"})
	h.Settled(7)
	from := 0
	evs := nvimEvents(t, sink, &from, proto.NvimSettled, 5*time.Second)
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, " ") != "closed lines settled" || evs[0].Reason != "error: neovim ended" || evs[1].Lines[0] != "late" || evs[2].Ack != 7 {
		t.Fatalf("the events: %+v", evs)
	}
	// Closed by the page: nothing more.
	q := &nvimEditor{id: "e2", cancel: func() {}, endWait: time.Second}
	q.ready.Store(true)
	sub.nvimMu.Lock()
	sub.nvims = map[string]*nvimEditor{"e2": q}
	sub.nvimMu.Unlock()
	s.NvimClose(sub, proto.NvimClose{T: proto.CtlNvimClose, ID: "e2"})
	(&nvimHandler{s: s, sub: sub, e: q}).Lines(0, 1, []string{"after the close"})
	time.Sleep(100 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, f := range sink.frames[from:] {
		if strings.Contains(string(f), "after the close") {
			t.Fatal("a closed editor's frame was sent")
		}
	}
}

// keysTaken is when sub's last keys took their token: NvimInput takes it
// under the lock it finds their editor with, so once it has moved, those
// keys have their editor, and only its Neovim answers them, or its end.
func keysTaken(sub *Subscription) time.Time {
	sub.nvimKeys.mu.Lock()
	defer sub.nvimKeys.mu.Unlock()
	return sub.nvimKeys.last
}

// Keys on their way to a Neovim that dies are not refused: its end
// (`closed`, told once) is their word, and the page keeps them over the text
// Neovim last held and names them. A refusal of them came from another
// goroutine than the end (the keys' own call fails as the client's reading
// ends and tells it), and a page that had it first took the key off its text
// as if Neovim lived (#429). Neovim is stopped (SIGSTOP) before the keys, so
// it reads none of them, whichever goroutine then tells its end.
func TestKeysOnTheirWayToANeovimThatDiesAreNotRefused(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newLocal(t, dir)
	editorsEnd(t, s)
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "owner", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	id := openEditor(t, s, sub)
	// Insert mode, the key done: Neovim waits for the next.
	if err := s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: "A", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	from := 0
	nvimEvents(t, sink, &from, proto.NvimSettled, 10*time.Second)
	sub.nvimMu.Lock()
	pid := sub.nvims[id].ed.Pid()
	sub.nvimMu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// The keys go to the stopped Neovim; once they have their editor, it is killed under them.
	before := keysTaken(sub)
	answered := make(chan error, 1)
	go func() {
		answered <- s.NvimInput(sub, proto.NvimInput{T: proto.CtlNvimInput, ID: id, Keys: "x", Seq: 2})
	}()
	for deadline := time.Now().Add(10 * time.Second); !keysTaken(sub).After(before); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the keys never took their token")
		}
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	killed = true
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("keys on their way to a Neovim that died were refused: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("keys on their way to a Neovim that died were never answered")
	}
	evs := nvimEvents(t, sink, &from, proto.NvimClosed, 10*time.Second)
	if closed := evs[len(evs)-1]; closed.ID != id || !strings.HasPrefix(closed.Reason, "error:") {
		t.Fatalf("closed: %+v", closed)
	}
	// Once the editor has gone, nothing more can come of it: its end was told once, and nothing refused the keys.
	for deadline := time.Now().Add(10 * time.Second); s.nvimCount.Load() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the editor of a Neovim that died was never let go")
		}
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	ends := 0
	for _, frame := range sink.frames {
		f, err := proto.Decode(frame)
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		var ev proto.NvimEvent
		if json.Unmarshal(f.Payload, &ev) != nil || ev.T != proto.CtlNvimEvent || ev.ID != id {
			continue
		}
		switch {
		case ev.Kind == proto.NvimClosed:
			ends++
		case ev.Kind == proto.NvimError && ev.Ack == 2:
			t.Fatalf("the keys were refused: %+v", ev)
		}
	}
	if ends != 1 {
		t.Fatalf("the end was told %d times", ends)
	}
}

// A wrapper that leaves Neovim running as it exits: the editor's end lets
// Neovim leave in its own time (its quit hook done), its connection kept
// until it has, rather than killing it as soon as the wrapper is gone.
func TestAnEditorWhoseWrapperExitedFirstIsLetLeave(t *testing.T) {
	left := filepath.Join(t.TempDir(), "left")
	childPid := exitingWrapper(t, "autocmd VimLeavePre * sleep 300m | call writefile(['left'], '"+left+"')\n")
	s, sub := editableLocal(t)
	openEditor(t, s, sub)
	child := nvimPid(childPid)
	if child == 0 {
		t.Fatal("the wrapper started no Neovim")
	}
	detachAndWait(t, s, sub, 10*time.Second)
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("Neovim was ended before it had left: %v", err)
	}
	if !gone(child, 5*time.Second) {
		t.Fatalf("Neovim (pid %d) still runs after its editor ended", child)
	}
}

// The server's shutdown (CloseAll) does not wait nvimEndWait for a Neovim
// that will not leave: the desktop app kills a server still shutting down
// after 12 s, and a Neovim in its own process group would outlive it. The
// editor gets nvimShutdownWait, then is ended through its context, and
// CloseAll returns with no Neovim running (Codex's review of #281).
func TestAServerShutdownEndsAHungEditorWithinItsGrace(t *testing.T) {
	pidFile := fakeNvim(t)
	s, sub := editableLocal(t)
	// The fake never leaves on its own (it sleeps); the editor's own wait would be the full nvimEndWait.
	s.nvimEndWait = 30 * time.Second
	old := EditorShutdownWait
	EditorShutdownWait = 500 * time.Millisecond
	t.Cleanup(func() { EditorShutdownWait = old })
	done := make(chan error, 1)
	go func() {
		done <- s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for nvimPid(pidFile) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Neovim did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid := nvimPid(pidFile)
	start := time.Now()
	if err := returnsWithin(t, 10*time.Second, "the shutdown's close", func() error {
		s.CloseAll(errors.New("shutting down"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > EditorShutdownWait+nvimReap+3*time.Second {
		t.Fatalf("the shutdown waited %v for a Neovim that would not leave", took)
	}
	if !gone(pid, 5*time.Second) {
		t.Fatalf("Neovim (pid %d) outlived the server's shutdown", pid)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the open did not end with the shutdown")
	}
}

// An editor whose connection went just before the shutdown is still the
// session's to end: its subscription is gone from the hub, its own end
// waits the full nvimEndWait for a Neovim that will not leave (one busy in
// a long :sleep takes no quit meanwhile), and the shutdown ends it all the
// same, within its own wait (Codex's review of #281: the subscription no
// longer named it).
func TestAServerShutdownEndsAHungEditorWhoseConnectionWentBefore(t *testing.T) {
	if !nvim.Available() {
		t.Skip("nvim is not on PATH; the test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	s, _ := newLocal(t, dir)
	s.nvimEndWait = 30 * time.Second
	old := EditorShutdownWait
	EditorShutdownWait = 500 * time.Millisecond
	t.Cleanup(func() { EditorShutdownWait = old })
	sink := newChanSink(false)
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nate", Owner: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	from := 0
	if err := s.NvimOpen(context.Background(), sub, proto.NvimOpen{T: proto.CtlNvimOpen, ReqID: "r1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	evs := nvimEvents(t, sink, &from, proto.NvimLines, 10*time.Second)
	id := evs[0].ID
	// Neovim is stopped (SIGSTOP): it takes no quit and leaves on no channel close, as one hung would; only a kill ends it.
	sub.nvimMu.Lock()
	ed := sub.nvims[id].ed
	sub.nvimMu.Unlock()
	pid := ed.Pid()
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// The connection goes: its editor ends on a goroutine of its own, waiting nvimEndWait for a Neovim that will not leave.
	s.Detach(sub)
	time.Sleep(time.Second)
	if s.nvimCount.Load() == 0 {
		t.Fatal("Neovim left on its own within a second: the stopped Neovim did not hold its editor, and the test exercises nothing")
	}
	start := time.Now()
	if err := returnsWithin(t, 10*time.Second, "the shutdown's close", func() error {
		s.CloseAll(errors.New("shutting down"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > EditorShutdownWait+nvimReap+3*time.Second {
		t.Fatalf("the shutdown waited %v for a Neovim whose connection had gone", took)
	}
	for deadline := time.Now().Add(5 * time.Second); s.nvimCount.Load() > 0; {
		if time.Now().After(deadline) {
			t.Fatalf("%d editors still counted after the shutdown", s.nvimCount.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !gone(pid, 5*time.Second) {
		t.Fatalf("Neovim (pid %d) outlived the server's shutdown", pid)
	}
}
