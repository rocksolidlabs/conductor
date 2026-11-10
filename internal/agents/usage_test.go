//go:build unix

package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	creack "github.com/creack/pty"
)

// The usage readers' tests run this test binary as the agent's tool: with
// USAGE_HELPER set it answers as Claude Code or Codex from the fixtures
// the test names, logs each run, and can write the pid of a child it
// starts in its process group, hang, close its output early or fail.

func TestMain(m *testing.M) {
	switch os.Getenv("USAGE_HELPER") {
	case "claude":
		os.Exit(claudeHelper())
	case "codex":
		os.Exit(codexHelper())
	case "ttyread":
		os.Exit(ttyReadHelper())
	}
	os.Exit(m.Run())
}

// helperCall is one logged run of the helper.
type helperCall struct {
	Args      []string `json:"args"`
	Dir       string   `json:"dir"`
	Env       []string `json:"env"` // names only
	ConfigDir string   `json:"configDir"`
	Method    string   `json:"method,omitempty"`
}

func helperLog(c helperCall) {
	path := os.Getenv("USAGE_LOG")
	if path == "" {
		return
	}
	if c.Method == "" {
		c.Args = os.Args[1:]
		c.Dir, _ = os.Getwd()
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			c.Env = append(c.Env, k)
		}
		c.ConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	b, _ := json.Marshal(c)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// helperStart writes the pid of a child in the helper's process group
// (renamed into place whole), then holds before any output when asked.
func helperStart() {
	if path := os.Getenv("USAGE_PIDFILE"); path != "" {
		child := exec.Command("sleep", "600")
		if child.Start() == nil {
			_ = os.WriteFile(path+".new", []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			_ = os.Rename(path+".new", path)
		}
	}
	if os.Getenv("USAGE_BEFORE") == "hang" {
		time.Sleep(time.Hour)
	}
}

// helperNoise writes what every run of the helper writes first: the
// marker (USAGE_MARK) on stderr, and USAGE_STDERR thousand-byte writes
// there (so a write crosses any keep's limit) before it goes on.
func helperNoise() {
	if m := os.Getenv("USAGE_MARK"); m != "" {
		os.Stderr.WriteString("warning: " + m + "\n")
	}
	n, _ := strconv.Atoi(os.Getenv("USAGE_STDERR"))
	chunk := strings.Repeat("e", 999) + "\n"
	for i := 0; i < n; i++ {
		os.Stderr.WriteString(chunk)
	}
}

// helperTTY opens the controlling terminal when USAGE_TTY names a file:
// it writes "none" there and fails when there is none, "opened" and
// holds when there is one (a tool asking its question there).
func helperTTY() {
	path := os.Getenv("USAGE_TTY")
	if path == "" {
		return
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		_ = os.WriteFile(path+".new", []byte("none"), 0o600)
		_ = os.Rename(path+".new", path)
		os.Exit(5)
	}
	_ = os.WriteFile(path+".new", []byte("opened"), 0o600)
	_ = os.Rename(path+".new", path)
	b := make([]byte, 1)
	_, _ = f.Read(b)
	time.Sleep(time.Hour)
}

// helperEnd ends the main run as USAGE_THEN says.
func helperEnd() int {
	switch os.Getenv("USAGE_THEN") {
	case "fail":
		os.Stderr.WriteString("fixture failure on stderr " + os.Getenv("USAGE_MARK") + "\nsecond line\n")
		return 3
	case "close":
		os.Stdout.Close()
		time.Sleep(time.Hour)
	case "hang":
		time.Sleep(time.Hour)
	}
	return 0
}

func catFile(env string) {
	if f, err := os.Open(os.Getenv(env)); err == nil {
		_, _ = io.Copy(os.Stdout, f)
		f.Close()
	}
}

// helperHold holds the run of the given kind (version, auth, usage,
// app-server) when USAGE_HOLD names it: it writes its pid to
// USAGE_HOLDFILE (renamed into place whole) and sleeps, before any output.
func helperHold(kind string) {
	if os.Getenv("USAGE_HOLD") != kind {
		return
	}
	path := os.Getenv("USAGE_HOLDFILE")
	_ = os.WriteFile(path+".new", []byte(strconv.Itoa(os.Getpid())), 0o600)
	_ = os.Rename(path+".new", path)
	time.Sleep(time.Hour)
}

// writePid writes pid to path, renamed into place whole.
func writePid(path string, pid int) {
	_ = os.WriteFile(path+".new", []byte(strconv.Itoa(pid)), 0o600)
	_ = os.Rename(path+".new", path)
}

// helperVersion answers --version as USAGE_VERSION says (else want),
// first starting a child in its group that outlives it when
// USAGE_VERSION_PIDFILE names a file (holding the output when
// USAGE_VERSION_HOLD is "child", through a child that leaves, a
// grandchild, when it is "grandchild"), and exits USAGE_VERSION_EXIT.
// USAGE_VERSION_THEN "close-gate" closes the output and waits for the
// file USAGE_GATE before it exits; "close-hang" closes it and never exits.
func helperVersion(want string) int {
	helperHold("version")
	if path := os.Getenv("USAGE_VERSION_PIDFILE"); path != "" {
		switch os.Getenv("USAGE_VERSION_HOLD") {
		case "grandchild":
			sh := exec.Command("sh", "-c", `sleep 600 & printf %s $! > "$1.new" && mv "$1.new" "$1"`, "sh", path)
			sh.Stdout = os.Stdout
			_ = sh.Run()
		case "child":
			child := exec.Command("sleep", "600")
			child.Stdout = os.Stdout
			if child.Start() == nil {
				writePid(path, child.Process.Pid)
			}
		default:
			child := exec.Command("sleep", "600")
			if child.Start() == nil {
				writePid(path, child.Process.Pid)
			}
		}
	}
	v := os.Getenv("USAGE_VERSION")
	if v == "" {
		v = want
	}
	if m := os.Getenv("USAGE_MARK"); m != "" {
		os.Stderr.WriteString(m + "\n")
	}
	os.Stdout.WriteString(v + "\n")
	switch os.Getenv("USAGE_VERSION_THEN") {
	case "close-gate":
		os.Stdout.Close()
		os.Stderr.Close()
		for {
			if _, err := os.Stat(os.Getenv("USAGE_GATE")); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "close-hang":
		os.Stdout.Close()
		os.Stderr.Close()
		time.Sleep(time.Hour)
	}
	code, _ := strconv.Atoi(os.Getenv("USAGE_VERSION_EXIT"))
	return code
}

func claudeHelper() int {
	helperLog(helperCall{})
	args := os.Args[1:]
	switch {
	case len(args) == 1 && args[0] == "--version":
		return helperVersion(claudeUsageQualified + " (Claude Code)")
	case len(args) >= 2 && args[0] == "auth" && args[1] == "status":
		helperHold("auth")
		helperNoise()
		helperTTY()
		catFile("USAGE_AUTH")
	case len(args) > 0 && args[0] == "-p":
		helperHold("usage")
		helperNoise()
		// A version that no longer takes one of the read's flags refuses
		// it as Claude Code's parser does, before any output.
		if f := os.Getenv("USAGE_REFUSE"); f != "" && slices.Contains(args, f) {
			os.Stderr.WriteString("error: unknown option '" + f + "' " + os.Getenv("USAGE_MARK") + "\n")
			return 1
		}
		helperStart()
		catFile("USAGE_STREAM")
		return helperEnd()
	default:
		return 2
	}
	return 0
}

func codexHelper() int {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		helperLog(helperCall{})
		return helperVersion("codex-cli " + codexUsageQualified)
	}
	helperLog(helperCall{})
	if len(args) == 0 || args[0] != "app-server" {
		return 2
	}
	helperHold("app-server")
	helperNoise()
	helperStart()
	reply := func(id json.RawMessage, file string) {
		result, err := os.ReadFile(os.Getenv(file))
		if err != nil {
			result = []byte("null")
		}
		os.Stdout.WriteString(`{"id":` + string(id) + `,"result":` + strings.TrimSpace(string(result)) + "}\n")
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			return 4
		}
		helperLog(helperCall{Method: m.Method})
		if m.Method != "" && m.Method == os.Getenv("USAGE_RPC_ERROR") {
			os.Stdout.WriteString(`{"id":` + string(m.ID) + `,"error":{"code":-32600,"message":"refused for ` + os.Getenv("USAGE_MARK") + `"}}` + "\n")
			continue
		}
		switch m.Method {
		case "initialize":
			os.Stdout.WriteString(`{"method":"configWarning","params":{"summary":"fixture warning","details":null}}` + "\n")
			os.Stdout.WriteString(`{"id":99,"result":{}}` + "\n")
			reply(m.ID, "USAGE_INIT")
		case "account/read":
			os.Stdout.WriteString(`{"method":"account/updated","params":{"authMode":"chatgpt","planType":"pro"}}` + "\n")
			reply(m.ID, "USAGE_ACCOUNT")
		case "account/rateLimits/read":
			reply(m.ID, "USAGE_LIMITS")
		case "initialized":
		default:
			os.Stdout.WriteString(`{"id":` + string(m.ID) + `,"error":{"code":-32601,"message":"fixture: no such method"}}` + "\n")
		}
	}
	return helperEnd()
}

// stub is one test's tool: its folder, its log and its environment.
type stub struct {
	dir, log string
	env      []string
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "usage", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newStub makes a stub of kind (claude or codex) with the fixtures every
// read of it takes, then kv.
func newStub(t *testing.T, kind string, kv ...string) *stub {
	t.Helper()
	slowProbes(t)
	s := &stub{dir: t.TempDir(), log: filepath.Join(t.TempDir(), "calls.jsonl")}
	// GORACE: a race-built helper otherwise sleeps a second as it exits.
	s.env = []string{"PATH=" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0", "USAGE_HELPER=" + kind, "USAGE_LOG=" + s.log, "CONDUCTOR_TOKEN=fixture-secret",
		"USAGE_AUTH=" + fixture(t, "claude-auth.json"), "USAGE_STREAM=" + fixture(t, "claude-usage.jsonl"),
		"USAGE_INIT=" + fixture(t, "codex-initialize.json"), "USAGE_ACCOUNT=" + fixture(t, "codex-account.json"), "USAGE_LIMITS=" + fixture(t, "codex-limits.json")}
	s.env = append(s.env, kv...)
	return s
}

func (s *stub) input(command []string, admit func(string) (Admission, error)) UsageInput {
	return UsageInput{Command: command, Env: s.env, Dir: s.dir, Admit: admit}
}

func (s *stub) calls(t *testing.T) []helperCall {
	t.Helper()
	b, err := os.ReadFile(s.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []helperCall
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c helperCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, c)
	}
	return out
}

// started counts the runs that were not a version check.
func (s *stub) started(t *testing.T, first string) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if c.Method == "" && len(c.Args) > 0 && c.Args[0] == first {
			n++
		}
	}
	return n
}

func self(t *testing.T) []string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{p}
}

// fakeAdmit is an admission that records what it was asked and told.
type fakeAdmit struct {
	mu     sync.Mutex
	asked  []string
	done   []Outcome
	refuse error
}

func (f *fakeAdmit) admit(v string) (Admission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, v)
	if f.refuse != nil {
		return nil, f.refuse
	}
	return f, nil
}

func (f *fakeAdmit) Done(o Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = append(f.done, o)
}

func (f *fakeAdmit) outcomes() []Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Outcome{}, f.done...)
}

// expireOnPid replaces the read's deadline with one the test expires,
// with the timeout's own cause, once the helper's pid file is written:
// the timeout is the mechanism, the pid file the moment.
func expireOnPid(t *testing.T, pidfile string, want time.Duration) {
	t.Helper()
	was := withUsageTimeout
	t.Cleanup(func() { withUsageTimeout = was })
	withUsageTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		if d != want {
			t.Errorf("the read's timeout is %s, want %s", d, want)
		}
		ctx, cancel := context.WithCancelCause(ctx)
		go func() {
			for ctx.Err() == nil {
				if _, err := os.Stat(pidfile); err == nil {
					cancel(errUsageTimeout)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		return ctx, func() { cancel(context.Canceled) }
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the helper wrote no pid file: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil || pid <= 0 {
		t.Fatalf("pid file %q", b)
	}
	return pid
}

// waitGone waits for the process to be gone: the group was killed. The
// bound only bounds a hang.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("pid %d outlived the read: the process group was not ended", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUsageReadersDeclared(t *testing.T) {
	for _, a := range All() {
		switch a.ID {
		case "claude", "codex":
			if a.Usage == nil || a.Usage.Read == nil || a.Usage.Qualified == "" || a.Usage.MinVersion == "" || versionLess(a.Usage.Qualified, a.Usage.MinVersion) {
				t.Errorf("%s: reader %+v", a.ID, a.Usage)
			}
		default:
			if a.Usage != nil {
				t.Errorf("%s has a usage reader: Conductor knows no way to read its usage", a.ID)
			}
		}
	}
	a, _ := Get("claude")
	a.Usage.Qualified = "0.0.1"
	if b, _ := Get("claude"); b.Usage.Qualified != claudeUsageQualified {
		t.Fatal("Get hands out the registry's reader")
	}
}

func TestUsageBounds(t *testing.T) {
	cases := map[string]func(f *os.File){
		"line": func(f *os.File) { _, _ = f.WriteString(strings.Repeat("x", usageMaxLine+1) + "\n") },
		"output": func(f *os.File) {
			for i := 0; i < 5; i++ {
				_, _ = f.WriteString(`{"type":"assistant","pad":"` + strings.Repeat("x", 900<<10) + `"}` + "\n")
			}
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			big := filepath.Join(t.TempDir(), "big.jsonl")
			f, err := os.Create(big)
			if err != nil {
				t.Fatal(err)
			}
			write(f)
			f.Close()
			pidfile := filepath.Join(t.TempDir(), "pid")
			s := newStub(t, "claude", "USAGE_STREAM="+big, "USAGE_PIDFILE="+pidfile, "USAGE_THEN=hang")
			adm := &fakeAdmit{}
			rep := readClaudeUsage(context.Background(), s.input(self(t), adm.admit))
			want := map[string]string{"line": "longer than 1 MiB", "output": "more than 4 MiB"}[name]
			if rep.State != UsageError || !strings.Contains(rep.Words, want) || rep.Outcome != Inconclusive {
				t.Fatalf("%+v", rep)
			}
			if got := adm.outcomes(); len(got) != 1 || got[0] != Inconclusive {
				t.Fatalf("Done: %v", got)
			}
			waitGone(t, readPid(t, pidfile))
		})
	}
}

func TestUsageTimeout(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			s := newStub(t, kind, "USAGE_PIDFILE="+pidfile, "USAGE_BEFORE=hang")
			adm := &fakeAdmit{}
			var rep UsageReport
			if kind == "claude" {
				expireOnPid(t, pidfile, claudeUsageTimeout)
				rep = readClaudeUsage(context.Background(), s.input(self(t), adm.admit))
			} else {
				expireOnPid(t, pidfile, codexUsageTimeout)
				rep = readCodexUsage(context.Background(), s.input(self(t), adm.admit))
			}
			if rep.State != UsageError || !strings.Contains(rep.Words, "did not answer within") || rep.Outcome != Inconclusive {
				t.Fatalf("%+v", rep)
			}
			waitGone(t, readPid(t, pidfile))
		})
	}
}

func TestUsageVersionFromProgram(t *testing.T) {
	bin := self(t)[0]
	dir := t.TempDir()
	prog := filepath.Join(dir, "claude")
	put := func(path, version string) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\nexec '" + bin + "' \"$@\"\n"
		if err := os.WriteFile(path+".new", []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	put(prog, claudeUsageQualified+" (Claude Code)")
	s := newStub(t, "claude")
	adm := &fakeAdmit{}
	if rep := readClaudeUsage(context.Background(), s.input([]string{prog}, adm.admit)); rep.State != UsageOK || rep.Version != claudeUsageQualified {
		t.Fatalf("the qualified program: %+v", rep)
	}
	// The same path, now a newer program: the read asks it, not a cache.
	put(prog, "2.9.0 (Claude Code)")
	rep := readClaudeUsage(context.Background(), s.input([]string{prog}, nil))
	if rep.State != UsageUnqualified || rep.Version != "2.9.0" || !strings.Contains(rep.Words, "2.9.0 is newer than the "+claudeUsageQualified) {
		t.Fatalf("a newer program, no admission: %+v", rep)
	}
	if n := s.started(t, "-p"); n != 1 {
		t.Fatalf("%d /usage runs, want the first one only", n)
	}
	// Replaced between the two notes.
	put(prog, claudeUsageQualified+" (Claude Code)")
	was := afterVersion
	t.Cleanup(func() { afterVersion = was })
	afterVersion = func() { put(prog, claudeUsageQualified+" (Claude Code)") }
	rep = readClaudeUsage(context.Background(), s.input([]string{prog}, adm.admit))
	if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "changed while it was checked") {
		t.Fatalf("replaced while checked: %+v", rep)
	}
	afterVersion = was
	// No version printed.
	put(prog, "no version here")
	if rep := readClaudeUsage(context.Background(), s.input([]string{prog}, adm.admit)); rep.State != UsageUnqualified || rep.Version != "" {
		t.Fatalf("no version: %+v", rep)
	}
	if n := s.started(t, "-p"); n != 1 || len(adm.asked) != 1 {
		t.Fatalf("%d /usage runs and %d admissions asked, want 1 each", n, len(adm.asked))
	}
	// Codex below its MinVersion: app-server is never started.
	cs := newStub(t, "codex", "USAGE_VERSION=codex-cli 0.100.0")
	rep = readCodexUsage(context.Background(), cs.input(self(t), nil))
	if rep.State != UsageUnqualified || rep.Version != "0.100.0" || !strings.Contains(rep.Words, "read only from "+codexUsageMinVersion) || cs.started(t, "app-server") != 0 {
		t.Fatalf("an old Codex: %+v", rep)
	}
}

func TestUsageWrappedCommand(t *testing.T) {
	bin := self(t)[0]
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "stub.sh")
	put := func(version string) {
		t.Helper()
		body := "if [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\nexec '" + bin + "' \"$@\"\n"
		if err := os.WriteFile(script+".new", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(script+".new", script); err != nil {
			t.Fatal(err)
		}
	}
	put(claudeUsageQualified + " (Claude Code)")
	shReal, _ := filepath.EvalSymlinks(sh)
	scriptReal, _ := filepath.EvalSymlinks(script)
	s := newStub(t, "claude")
	adm := &fakeAdmit{}
	rep := readClaudeUsage(context.Background(), s.input([]string{sh, script}, adm.admit))
	if rep.State != UsageOK || rep.Version != claudeUsageQualified {
		t.Fatalf("%+v", rep)
	}
	// The read's argv: the resolved shell, the resolved script, the
	// reader's own arguments (the helper sees what the script passes on).
	var usage []string
	for _, c := range s.calls(t) {
		if len(c.Args) > 0 && c.Args[0] == "-p" {
			usage = c.Args
		}
	}
	if strings.Join(usage, "\x00") != strings.Join(claudeUsageArgs, "\x00") {
		t.Fatalf("usage args %q", usage)
	}
	p, err := resolveProgram([]string{sh, script}, s.dir)
	if err != nil || len(p.argv) != 2 || p.argv[0] != shReal || p.argv[1] != scriptReal || len(p.files) != 2 {
		t.Fatalf("resolved %+v, %v", p, err)
	}
	// The script replaced between the notes.
	was := afterVersion
	afterVersion = func() { put(claudeUsageQualified + " (Claude Code)") }
	rep = readClaudeUsage(context.Background(), s.input([]string{sh, script}, adm.admit))
	afterVersion = was
	if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "changed while it was checked") {
		t.Fatalf("script replaced: %+v", rep)
	}
	// A wrapper that prints its own version.
	put("GNU bash, version 5.2.21")
	rep = readClaudeUsage(context.Background(), s.input([]string{sh, script}, adm.admit))
	if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "does not answer as Claude Code") {
		t.Fatalf("a wrapper's own version: %+v", rep)
	}
	if n := s.started(t, "-p"); n != 1 {
		t.Fatalf("%d /usage runs, want 1", n)
	}
	// Codex's app-server through a wrapper.
	put("codex-cli " + codexUsageQualified)
	cs := newStub(t, "codex")
	rep = readCodexUsage(context.Background(), cs.input([]string{sh, script}, nil))
	if rep.State != UsageOK || rep.Version != codexUsageQualified || cs.started(t, "app-server") != 1 {
		t.Fatalf("codex wrapped: %+v", rep)
	}
	put("GNU bash, version 5.2.21")
	rep = readCodexUsage(context.Background(), cs.input([]string{sh, script}, nil))
	if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "does not answer as Codex") || cs.started(t, "app-server") != 1 {
		t.Fatalf("codex wrapper's own version: %+v", rep)
	}
}

func TestBoundedLines(t *testing.T) {
	b := &boundedLines{r: bufio.NewReaderSize(strings.NewReader("one\n\n  two  \r\nthree"), 16)}
	var got []string
	for {
		line, err := b.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line))
	}
	if strings.Join(got, "|") != "one|two|three" {
		t.Fatalf("%q", got)
	}
	if clip("aé", 2) != "a" || clip("abc", 5) != "abc" {
		t.Fatal("clip cuts inside a character")
	}
}

// ttyReadHelper is a server with a controlling terminal: started by
// TestUsageNoControllingTerminal on a terminal of its own, it reads
// Claude Code's usage from the stub (USAGE_HELPER=claude for the stub)
// and writes the report to USAGE_REPORT. Should the stub open the
// terminal (USAGE_TTY says "opened"), the read's deadline is expired
// then, so a read that would wait out its timeout ends at once.
func ttyReadHelper() int {
	marker := os.Getenv("USAGE_TTY")
	withUsageTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancelCause(ctx)
		go func() {
			for ctx.Err() == nil {
				if b, err := os.ReadFile(marker); err == nil && string(b) == "opened" {
					cancel(errUsageTimeout)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		return ctx, func() { cancel(context.Canceled) }
	}
	probeTimeout = 30 * time.Second
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "USAGE_HELPER=") {
			env = append(env, kv)
		}
	}
	env = append(env, "USAGE_HELPER=claude")
	self, err := os.Executable()
	if err != nil {
		return 7
	}
	rep := readClaudeUsage(context.Background(), UsageInput{Command: []string{self}, Env: env, Dir: os.Getenv("USAGE_DIR"), Admit: (&fakeAdmit{}).admit})
	b, _ := json.Marshal(rep)
	if os.WriteFile(os.Getenv("USAGE_REPORT"), b, 0o600) != nil {
		return 8
	}
	return 0
}

// TestUsageOnlyCheckedVersions: a Claude Code below the version the reader
// was checked on is not read, not even its auth status, however close it
// is; MinVersion is a checked version, never a guess below one.
func TestUsageOnlyCheckedVersions(t *testing.T) {
	for _, a := range All() {
		if a.Usage != nil && a.Usage.MinVersion != a.Usage.Qualified {
			t.Errorf("%s reads from %s but was checked on %s: the versions between were never checked", a.ID, a.Usage.MinVersion, a.Usage.Qualified)
		}
	}
	for _, v := range []string{"2.1.318", "2.1.320", "2.1.321"} {
		s := newStub(t, "claude", "USAGE_VERSION="+v+" (Claude Code)")
		adm := &fakeAdmit{}
		rep := claudeRead(t, s, adm)
		if rep.State != UsageUnqualified || rep.Version != v || !strings.Contains(rep.Words, "read only from "+claudeUsageQualified) || s.started(t, "auth") != 0 || len(adm.asked) != 0 {
			t.Errorf("%s: %+v", v, rep)
		}
	}
}

// TestUsageStderrPastItsKeep: a tool that writes far more on stderr than
// the read keeps, in writes that cross the keep's limit, and then goes on
// writing, is still read: the keep never closes its pipe.
func TestUsageStderrPastItsKeep(t *testing.T) {
	var b limitedBuffer
	b.max = 10
	if n, err := b.Write([]byte("0123456")); n != 7 || err != nil {
		t.Errorf("write %d, %v", n, err)
	}
	if n, err := b.Write([]byte("789abcdef")); n != 9 || err != nil || b.String() != "0123456789" {
		t.Errorf("a write across the limit: %d, %v, kept %q", n, err, b.String())
	}
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			s := newStub(t, kind, "USAGE_STDERR=64")
			var rep UsageReport
			if kind == "claude" {
				rep = claudeRead(t, s, &fakeAdmit{})
			} else {
				rep = codexRead(t, s)
			}
			if rep.State != UsageOK {
				t.Fatalf("%+v", rep)
			}
		})
	}
}

// TestUsageVersionGroupEnded: what the version's run left in its group goes
// when the version is read, as every run of a read ends its group.
func TestUsageVersionGroupEnded(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			s := newStub(t, kind, "USAGE_VERSION_PIDFILE="+pidfile)
			var rep UsageReport
			if kind == "claude" {
				rep = claudeRead(t, s, &fakeAdmit{})
			} else {
				rep = codexRead(t, s)
			}
			if rep.State != UsageOK {
				t.Fatalf("%+v", rep)
			}
			waitGone(t, readPid(t, pidfile))
		})
	}
}

// TestUsageVersionMustSucceed: a version printed by a command that then
// fails is not a version: nothing more is started.
func TestUsageVersionMustSucceed(t *testing.T) {
	s := newStub(t, "claude", "USAGE_VERSION_EXIT=1")
	adm := &fakeAdmit{}
	if rep := claudeRead(t, s, adm); rep.State != UsageUnqualified || rep.Version != "" || s.started(t, "auth") != 0 || len(adm.asked) != 0 {
		t.Fatalf("claude: %+v", rep)
	}
	cs := newStub(t, "codex", "USAGE_VERSION_EXIT=2")
	if rep := codexRead(t, cs); rep.State != UsageUnqualified || rep.Version != "" || cs.started(t, "app-server") != 0 {
		t.Fatalf("codex: %+v", rep)
	}
}

// TestUsageRelativeScript: a wrapper's script named relative to the
// read's folder is noted like an absolute one, so a script replaced
// between the version's print and the start is caught.
func TestUsageRelativeScript(t *testing.T) {
	bin := self(t)[0]
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	s := newStub(t, "claude")
	script := filepath.Join(s.dir, "stub.sh")
	put := func(version string) {
		t.Helper()
		body := "if [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\nexec '" + bin + "' \"$@\"\n"
		if err := os.WriteFile(script+".new", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(script+".new", script); err != nil {
			t.Fatal(err)
		}
	}
	put(claudeUsageQualified + " (Claude Code)")
	adm := &fakeAdmit{}
	if rep := readClaudeUsage(context.Background(), s.input([]string{sh, "stub.sh"}, adm.admit)); rep.State != UsageOK {
		t.Fatalf("%+v", rep)
	}
	was := afterVersion
	t.Cleanup(func() { afterVersion = was })
	afterVersion = func() { put(claudeUsageQualified + " (Claude Code)") }
	rep := readClaudeUsage(context.Background(), s.input([]string{sh, "stub.sh"}, adm.admit))
	if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "changed while it was checked") || s.started(t, "-p") != 1 {
		t.Fatalf("relative script replaced: %+v", rep)
	}
}

// TestUsageNoControllingTerminal: a server run from a terminal starts its
// readers with none, so a tool that asks there fails at once (and the
// read says how it exited) instead of waiting out the read's timeout,
// stopped.
func TestUsageNoControllingTerminal(t *testing.T) {
	master, tty, err := creack.Open()
	if err != nil {
		t.Skipf("no terminal to give: %v", err)
	}
	defer master.Close()
	go func() { _, _ = io.Copy(io.Discard, master) }()
	s := newStub(t, "claude")
	marker := filepath.Join(t.TempDir(), "tty")
	report := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command(self(t)[0])
	cmd.Env = append(append([]string{}, s.env...), "USAGE_HELPER=ttyread", "USAGE_TTY="+marker, "USAGE_REPORT="+report, "USAGE_DIR="+s.dir)
	cmd.Stdin = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		tty.Close()
		t.Fatal(err)
	}
	tty.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the server with a terminal: %v", err)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var rep UsageReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	m, _ := os.ReadFile(marker)
	if string(m) != "none" || rep.State != UsageError || strings.Contains(rep.Words, "did not answer") || !strings.Contains(rep.Words, "(exit status 5)") {
		t.Fatalf("the tool's terminal: %q; the read: %+v", m, rep)
	}
}

// recipeSummary is all the recipes tier prints of a read: the tool's
// version, the state, the outcome and which fields were present. Never
// the words, a name or a figure: nothing of what the tool said.
func recipeSummary(rep UsageReport) string {
	var kinds []string
	for _, w := range rep.Windows {
		kinds = append(kinds, w.Kind)
	}
	var credits []string
	for _, c := range rep.Credits {
		credits = append(credits, c.Kind)
	}
	return "version " + rep.Version + ", state " + rep.State + ", outcome " + rep.Outcome.String() +
		", plan given " + strconv.FormatBool(rep.Plan != "") + ", windows [" + strings.Join(kinds, " ") + "], credits [" + strings.Join(credits, " ") + "]" +
		", limits given " + strconv.FormatBool(rep.Limits.Reached != "" || rep.Limits.SpendControl != nil || rep.Limits.OrdinaryAllowed != nil)
}

// recipeAdmission is the recipes tier's leave for a /usage run: the
// versions the reader was checked on (min to qualified), and the one
// version named in CONDUCTOR_USAGE_CHECK (check), which is the admin's
// explicit check; any other is refused and noted, so `make test-recipes`
// on a signed-in machine never starts a version nobody asked to check.
type recipeAdmission struct {
	fakeAdmit
	min, qualified, check string
	refused               []string
}

func (r *recipeAdmission) admit(v string) (Admission, error) {
	if (versionLess(v, r.min) || versionLess(r.qualified, v)) && (r.check == "" || v != r.check) {
		r.refused = append(r.refused, v)
		return nil, errNotAsked
	}
	return r.fakeAdmit.admit(v)
}

var errNotAsked = errors.New("this version of the tool is not one Conductor has checked, and no check of it was asked for")

func TestRecipeAdmission(t *testing.T) {
	r := &recipeAdmission{min: "2.1.322", qualified: "2.1.322"}
	for v, ok := range map[string]bool{"2.1.322": true, "2.1.321": false, "2.1.323": false, "2.2.0": false} {
		if _, err := r.admit(v); (err == nil) != ok {
			t.Errorf("%s admitted %v, want %v", v, err == nil, ok)
		}
	}
	r.check = "2.1.323"
	for v, ok := range map[string]bool{"2.1.323": true, "2.1.324": false, "2.1.322": true} {
		if _, err := r.admit(v); (err == nil) != ok {
			t.Errorf("asked for 2.1.323: %s admitted %v, want %v", v, err == nil, ok)
		}
	}
}
