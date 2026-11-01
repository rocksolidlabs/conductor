package nvim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recorder keeps what the editor reported, in order.
type recorder struct {
	mu     sync.Mutex
	events []string
	lines  [][]string
	// changes are the lines events whole: "first:last" and the lines.
	changes []string
	swaps   []Swap
	done    chan struct{}
	// linesDelay makes each lines event take that long, as a slow page's
	// connection would hold the client's notifications back.
	linesDelay time.Duration
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}
func (r *recorder) Lines(first, last int, lines []string) {
	r.mu.Lock()
	delay := r.linesDelay
	r.mu.Unlock()
	time.Sleep(delay)
	r.mu.Lock()
	r.lines = append(r.lines, lines)
	r.changes = append(r.changes, fmt.Sprintf("%d:%d %q", first, last, lines))
	r.mu.Unlock()
	r.add("lines")
}
func (r *recorder) Cursor(line, col int, mode string, vl, vc int) { r.add("cursor " + mode) }
func (r *recorder) Mode(mode string)                              { r.add("mode " + mode) }
func (r *recorder) Cmdline(show bool, content string, pos int, prompt string) {
	if show {
		r.add("cmdline " + prompt + content)
	} else {
		r.add("cmdline hidden")
	}
}
func (r *recorder) Message(text, kind string) { r.add("message " + kind + ":" + text) }
func (r *recorder) Written(path string)       { r.add("written " + filepath.Base(path)) }
func (r *recorder) Buffer(path string)        { r.add("buffer " + filepath.Base(path)) }
func (r *recorder) Ack(seq uint32, line, col int, mode string, vl, vc int) {
	r.add(fmt.Sprintf("ack %d %d:%d %s", seq, line, col, mode))
}
func (r *recorder) Modified(m bool)    { r.add(fmt.Sprintf("modified %v", m)) }
func (r *recorder) Settled(seq uint32) { r.add(fmt.Sprintf("settled %d", seq)) }
func (r *recorder) Swap(sw Swap) {
	r.mu.Lock()
	r.swaps = append(r.swaps, sw)
	r.mu.Unlock()
	r.add("swap " + filepath.Base(sw.File))
}
func (r *recorder) Exited(reason string) {
	r.add("exited " + reason)
	close(r.done)
}
func (r *recorder) has(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
func (r *recorder) wait(t *testing.T, prefix string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if r.has(prefix) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("no %q among %q", prefix, r.events)
}

// The real nvim: a file loads, keys change the buffer and come back as line
// events, the mode and the command line show, :w writes the file and says
// so, :q ends the editor.
func TestEditorAgainstTheRealNeovim(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	file := filepath.Join(dir, "sample.txt")
	os.WriteFile(file, []byte("line one\nline two\nline three\n"), 0o644)
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "sample.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if lines, err := e.Lines(); err != nil || len(lines) != 3 || lines[0] != "line one" {
		t.Fatalf("lines: %q %v", lines, err)
	}
	if err := e.Input("dd"); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "lines")
	r.mu.Lock()
	first := r.lines[0]
	r.mu.Unlock()
	if len(first) != 0 {
		t.Fatalf("dd should report a deletion, got %q", first)
	}
	e.Input("i")
	r.wait(t, "mode insert")
	e.Input("hello <lt>world<Esc>")
	r.wait(t, "cursor n")
	e.Input(":w")
	r.wait(t, "cmdline :w")
	e.Input("<CR>")
	r.wait(t, "written sample.txt")
	r.wait(t, "message")
	b, _ := os.ReadFile(file)
	if string(b) != "hello <worldline two\nline three\n" {
		t.Fatalf("written file: %q", b)
	}
	// Two-key commands sent a key at a time, as a browser sends them: gg, then f and its character.
	e.Input("ggOone two<Esc>G")
	for _, k := range []string{"g", "g", "0", "f", "t"} {
		if err := e.Input(k); err != nil {
			t.Fatal(err)
		}
	}
	var col int
	for i := 0; i < 50; i++ {
		var pos []int
		if err := e.v.Eval("[line('.'), col('.')]", &pos); err == nil && len(pos) == 2 && pos[0] == 1 && pos[1] == 5 {
			col = pos[1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if col != 5 {
		t.Fatal("gg then ft, a key at a time, did not land on the t of two")
	}
	e.Input(":q!<CR>")
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no exit after :q")
	}
	if err := e.Input("x"); err != ErrClosed {
		t.Fatalf("input after exit: %v", err)
	}
}

// Another editor's swap file (round 13, G3): the file opens read-only and
// the swap file is reported with its writer; while that runs only "edit
// anyway" applies; once it is gone (killed, the swap file left behind) the
// swap file's unsaved text can be recovered and the swap file deleted.
// Neovim's state and config go to temporary folders, so the person's own
// swap folder is never touched.
func TestASwapFileOpensReadOnlyWithItsChoices(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	file := filepath.Join(dir, "sample.txt")
	os.WriteFile(file, []byte("one\n"), 0o644)
	readonly := func(e *Editor) bool {
		var ro int
		if err := e.v.Eval("&readonly", &ro); err != nil {
			t.Fatal(err)
		}
		return ro == 1
	}
	// The first editor holds the file with an unsaved change.
	r1 := &recorder{done: make(chan struct{})}
	e1, err := Open(context.Background(), dir, "sample.txt", r1)
	if err != nil {
		t.Fatal(err)
	}
	defer e1.Close()
	e1.Input("ixx<Esc>")
	r1.wait(t, "cursor n")
	var pid int
	if err := e1.v.Eval("getpid()", &pid); err != nil {
		t.Fatal(err)
	}
	// A second opens it read-only and says whose swap file it is.
	r2 := &recorder{done: make(chan struct{})}
	e2, err := Open(context.Background(), dir, "sample.txt", r2)
	if err != nil {
		t.Fatalf("the open was refused: %v", err)
	}
	if lines, _ := e2.Lines(); len(lines) != 1 || lines[0] != "one" {
		t.Fatalf("lines %q", lines)
	}
	sw := e2.FoundSwap()
	if sw == nil || sw.Pid != pid || !sw.Running || !strings.HasPrefix(filepath.Ext(sw.File), ".sw") || !readonly(e2) {
		t.Fatalf("swap %+v, read-only %v", sw, readonly(e2))
	}
	if err := e2.SwapChoice(SwapDelete); err != ErrSwap {
		t.Fatalf("delete while its writer runs: %v", err)
	}
	if err := e2.SwapChoice(SwapEdit); err != nil || readonly(e2) {
		t.Fatalf("edit anyway: %v, read-only %v", err, readonly(e2))
	}
	e2.Close()
	// The first editor writes its change into the swap file (Neovim does that
	// lazily, after a pause or some typing), then dies without cleaning up:
	// the swap file stays, stale.
	if err := e1.v.Command("preserve"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	e1.Close()
	for i := 0; i < 100 && processRuns(pid); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	r3 := &recorder{done: make(chan struct{})}
	e3, err := Open(context.Background(), dir, "sample.txt", r3)
	if err != nil {
		t.Fatal(err)
	}
	defer e3.Close()
	sw = e3.FoundSwap()
	if sw == nil || sw.Running || !sw.Modified {
		t.Fatalf("stale swap %+v", sw)
	}
	if err := e3.SwapChoice(SwapRecover); err != nil {
		t.Fatal(err)
	}
	if lines, _ := e3.Lines(); len(lines) != 1 || lines[0] != "xxone" || readonly(e3) {
		t.Fatalf("recovered %q, read-only %v", lines, readonly(e3))
	}
	// The recovery reloads the buffer: the bridge follows it again and
	// reports the recovered text, so the editor shows it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r3.mu.Lock()
		got := len(r3.lines) > 0 && len(r3.lines[len(r3.lines)-1]) == 1 && r3.lines[len(r3.lines)-1][0] == "xxone"
		r3.mu.Unlock()
		if got {
			break
		}
		if time.Now().After(deadline) {
			r3.mu.Lock()
			t.Fatalf("no lines event with the recovered text: %q (events %q)", r3.lines, r3.events)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := e3.SwapChoice(SwapDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sw.File); !os.IsNotExist(err) {
		t.Fatalf("the swap file is still there: %v", err)
	}
	if e3.FoundSwap() != nil {
		t.Fatal("answered, the swap is still pending")
	}
}

// Neovim's own questions (:confirm q with changes not written) come as a
// message of kind "confirm" carrying the choices' words, which the editor
// shows as buttons (round 13, G3); Cancel (c) leaves the editor open.
func TestAConfirmQuestionIsAConfirmMessage(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("one\n"), 0o644)
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "sample.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.Input("ix<Esc>")
	r.wait(t, "cursor n")
	e.Input(":confirm q<CR>")
	r.wait(t, "message confirm:")
	r.mu.Lock()
	var text string
	for _, ev := range r.events {
		if strings.HasPrefix(ev, "message confirm:") {
			text = ev
		}
	}
	r.mu.Unlock()
	if !strings.Contains(text, "[Y]es") || !strings.Contains(text, "(C)ancel") {
		t.Fatalf("confirm %q", text)
	}
	e.Input("c")
	time.Sleep(300 * time.Millisecond)
	if r.has("exited") {
		t.Fatal("Cancel ended the editor")
	}
}

// Text with no key press reaches the bridge as keys (round 13, G4): a
// composed word, a dead key's character, `<` as <lt>, typed as written.
func TestComposedTextIsTypedAsWritten(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("end\n"), 0o644)
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "sample.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.Input("I")
	r.wait(t, "mode insert")
	for _, keys := range []string{"café ", "日本 ", "<lt>tag> "} {
		if err := e.Input(keys); err != nil {
			t.Fatal(err)
		}
	}
	e.Input("<Esc>")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if lines, _ := e.Lines(); len(lines) == 1 && lines[0] == "café 日本 <tag> end" {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("lines %q", lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Numbered keys are acknowledged after the buffer changes they made, on the
// same channel (round 13, G5): a page that showed them at once knows which
// guesses the buffer holds. While Neovim waits for a command's next key
// (the first g of gg) nothing is acknowledged; the next keys' ack covers it.
func TestNumberedKeysAreAcknowledgedAfterTheirChanges(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("end\none\n"), 0o644)
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "sample.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.InputSeq("I", 1)
	r.wait(t, "ack 1 1:1 i")
	e.InputSeq("a", 2)
	e.InputSeq("b", 3)
	e.InputSeq("c", 4)
	r.wait(t, "ack 4 1:4 i")
	// Every change before an ack is in the lines reported before it.
	r.mu.Lock()
	var lines []string
	li := 0
	for _, ev := range r.events {
		if ev == "lines" {
			lines = r.lines[li]
			li++
		}
		if ev == "ack 4 1:4 i" {
			break
		}
	}
	r.mu.Unlock()
	if len(lines) != 1 || lines[0] != "abcend" {
		t.Fatalf("the lines before ack 4: %q", lines)
	}
	e.InputSeq("<Esc>", 5)
	r.wait(t, "ack 5 1:3 n")
	e.InputSeq("j", 6)
	r.wait(t, "ack 6 2:")
	e.InputSeq("g", 7)
	time.Sleep(300 * time.Millisecond)
	if r.has("ack 7") {
		t.Fatal("acknowledged while a command waits for its next key")
	}
	e.InputSeq("g", 8)
	r.wait(t, "ack 8 1:")
}

// The buffer's changes not written are reported as they begin and end (a
// change, :w, a change, undo back to what was written); Close keeps the swap
// file of a buffer that holds some, as a lost connection does, for recovery;
// Discard drops them and the swap file with them, as :qa! does.
func TestModifiedIsReportedAndDiscardDropsTheSwapFile(t *testing.T) {
	if !Available() {
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
	count := func(r *recorder, ev string) int {
		r.mu.Lock()
		defer r.mu.Unlock()
		n := 0
		for _, e := range r.events {
			if e == ev {
				n++
			}
		}
		return n
	}
	waitCount := func(r *recorder, ev string, n int) {
		t.Helper()
		for i := 0; i < 200 && count(r, ev) < n; i++ {
			time.Sleep(25 * time.Millisecond)
		}
		if count(r, ev) < n {
			r.mu.Lock()
			defer r.mu.Unlock()
			t.Fatalf("%d of %q wanted among %q", n, ev, r.events)
		}
	}
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "a.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	e.Input("ixx<Esc>")
	waitCount(r, "modified true", 1)
	e.Input(":w<CR>")
	waitCount(r, "modified false", 1)
	e.Input("Ayy<Esc>")
	waitCount(r, "modified true", 2)
	e.Input("u")
	waitCount(r, "modified false", 2)
	e.Input("Azz<Esc>")
	waitCount(r, "modified true", 3)
	e.Close()
	kept := swaps()
	if len(kept) != 1 {
		t.Fatalf("closed with changes not written, the swap files: %q", kept)
	}
	os.Remove(kept[0])

	r = &recorder{done: make(chan struct{})}
	e, err = Open(context.Background(), dir, "a.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	e.Input("Aqq<Esc>")
	waitCount(r, "modified true", 1)
	e.Discard()
	if left := swaps(); len(left) != 0 {
		t.Fatalf("discarded, the swap files: %q", left)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "xxone\n" {
		t.Fatalf("the file: %q", b)
	}
	e.Discard() // a second end is harmless
}

// childEnv drops Conductor's own variables and the dynamic linker's
// injection variables, and keeps the rest the person's config and plugins
// need (HOME, the XDG directories, PATH).
func TestChildEnvDropsSecretsKeepsConfig(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/nate",
		"XDG_CONFIG_HOME=/home/nate/.config",
		"XDG_STATE_HOME=/home/nate/.local/state",
		"TERM=xterm",
		"CONDUCTOR_NOTIFY_TOKEN=secret",
		"CONDUCTOR_SESSION_ID=s1",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp",
		"LD_AUDIT=/tmp/evil.so",
		"LD_PROFILE=x",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"DYLD_LIBRARY_PATH=/tmp",
		"malformed",
	}
	got := map[string]bool{}
	for _, kv := range childEnv(in) {
		got[kv] = true
	}
	for _, kv := range []string{"PATH=/usr/bin", "HOME=/home/nate", "XDG_CONFIG_HOME=/home/nate/.config", "XDG_STATE_HOME=/home/nate/.local/state", "TERM=xterm"} {
		if !got[kv] {
			t.Errorf("dropped a kept variable: %q", kv)
		}
	}
	for _, kv := range []string{"CONDUCTOR_NOTIFY_TOKEN=secret", "CONDUCTOR_SESSION_ID=s1", "LD_PRELOAD=/tmp/evil.so", "LD_LIBRARY_PATH=/tmp", "LD_AUDIT=/tmp/evil.so", "LD_PROFILE=x", "DYLD_INSERT_LIBRARIES=/tmp/evil.dylib", "DYLD_LIBRARY_PATH=/tmp", "malformed"} {
		if got[kv] {
			t.Errorf("kept a dropped variable: %q", kv)
		}
	}
}

// The Neovim child does not inherit Conductor's secrets: a CONDUCTOR_
// variable set for the server is not in the editor's environment.
func TestNeovimChildHasNoConductorSecret(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CONDUCTOR_NOTIFY_TOKEN", "super-secret")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("one\n"), 0o644)
	r := &recorder{done: make(chan struct{})}
	e, err := Open(context.Background(), dir, "sample.txt", r)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var tok string
	if err := e.v.Eval("$CONDUCTOR_NOTIFY_TOKEN", &tok); err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Fatalf("the editor sees the token: %q", tok)
	}
	var path string
	if err := e.v.Eval("$PATH", &path); err != nil || path == "" {
		t.Fatalf("the editor lost PATH: %q %v", path, err)
	}
}

// unreadableShaDa is a ShaDa file Neovim refuses, as the one the e2e's
// Neovims left under the server's HOME did (#281): an item of type 0, which
// "must not be there", and every `:edit` that reads it throws E576.
var unreadableShaDa = []byte{0x00, 0x00, 0x01, 0xc0}

// Two embedded Neovims never share a ShaDa path, with each other or with
// the person (#281): each says 'shadafile' is NONE, both open their files
// though the person's ShaDa file is unreadable, and both leave (one as a
// lost connection does, one as `:qa!` does) without writing it or leaving a
// temporary file beside it. The same holds when the person's config names a
// ShaDa file of its own, which the argument alone would not cover.
func TestEditorsShareNoShaDa(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	for _, tc := range []struct {
		name string
		// config is the init.vim written into the config's nvim folder, %s
		// the ShaDa folder ("" for none).
		config string
		// file is the ShaDa file the person's Neovim would use, in it.
		file string
	}{
		{name: "the default ShaDa file", file: "main.shada"},
		{name: "a ShaDa file the config names", config: "set shadafile=%s/mine.shada\n", file: "mine.shada"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, config := t.TempDir(), t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			shada := filepath.Join(state, "nvim", "shada")
			if err := os.MkdirAll(shada, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(shada, tc.file), unreadableShaDa, 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				if err := os.MkdirAll(filepath.Join(config, "nvim"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(config, "nvim", "init.vim"), []byte(fmt.Sprintf(tc.config, shada)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			var eds []*Editor
			for _, name := range []string{"one.txt", "two.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				e, err := Open(context.Background(), dir, name, &recorder{done: make(chan struct{})})
				if err != nil {
					t.Fatalf("open %s beside an unreadable ShaDa file: %v", name, err)
				}
				defer e.Close()
				eds = append(eds, e)
			}
			for i, e := range eds {
				var file string
				if err := e.v.Eval("&shadafile", &file); err != nil {
					t.Fatal(err)
				}
				if file != "NONE" {
					t.Errorf("editor %d's 'shadafile' is %q, not NONE", i, file)
				}
				// Something a ShaDa file would keep: a register and a
				// command in the history, as requests Neovim has done
				// once they return.
				for _, c := range []string{"normal! yy", "call histadd(':', 'echo 1')"} {
					if err := e.v.Command(c); err != nil {
						t.Fatalf("%s: %v", c, err)
					}
				}
			}
			eds[0].Close()
			eds[1].Discard()
			entries, err := os.ReadDir(shada)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != tc.file {
				var names []string
				for _, en := range entries {
					names = append(names, en.Name())
				}
				t.Errorf("the ShaDa folder holds %q, not the person's %s alone", names, tc.file)
			}
			if got, err := os.ReadFile(filepath.Join(shada, tc.file)); err != nil || string(got) != string(unreadableShaDa) {
				t.Errorf("the person's ShaDa file was written: %x %v", got, err)
			}
		})
	}
}

// An embedded Neovim does not read the person's ShaDa file as it starts
// either (#281): a register the person's Neovim kept there is not the
// editor's, and the file is as it was once the editor has left.
func TestEditorReadsNoShaDaAtStart(t *testing.T) {
	if !Available() {
		t.Skip("nvim is not on PATH; the bridge's test needs the real Neovim")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	shada := filepath.Join(state, "nvim", "shada")
	if err := os.MkdirAll(shada, 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(shada, "main.shada")
	// The person's Neovim, keeping a register in its ShaDa file.
	if out, err := exec.Command(Program, "--headless", "--clean", "-i", main, "-c", "let @a = 'kept'", "-c", "wshada!", "-c", "qa!").CombinedOutput(); err != nil {
		t.Fatalf("write the person's ShaDa file: %v %s", err, out)
	}
	before, err := os.ReadFile(main)
	if err != nil || len(before) == 0 {
		t.Fatalf("the person's ShaDa file: %d bytes, %v", len(before), err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("one\n"), 0o644)
	e, err := Open(context.Background(), dir, "sample.txt", &recorder{done: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var a string
	if err := e.v.Eval("@a", &a); err != nil {
		t.Fatal(err)
	}
	if a != "" {
		t.Errorf("the editor read the person's register a: %q", a)
	}
	if err := e.v.Command("let @a = 'the editor''s'"); err != nil {
		t.Fatal(err)
	}
	e.Discard()
	if after, err := os.ReadFile(main); err != nil || string(after) != string(before) {
		t.Errorf("the person's ShaDa file changed: %d bytes before, %d after, %v", len(before), len(after), err)
	}
}
