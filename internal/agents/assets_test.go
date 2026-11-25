package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// writeAssets is WriteAssets for a test: the binary WriteAssets records for
// the whole process is put back as it was when the test ends.
func writeAssets(t *testing.T, hooksDir, bin string) error {
	t.Helper()
	t.Cleanup(ForgetBinary())
	return WriteAssets(hooksDir, bin)
}

// Every adapter's assets land under the hooks dir with the binary path in
// place of the placeholder: files 0600, directories 0700, JSON indented with
// two spaces.
func TestWriteAssetsWritesEveryAsset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "hooks")
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("hooks dir: %v %v", fi.Mode(), err)
	}
	count := 0
	for _, a := range All() {
		for rel := range a.Assets {
			count++
			p := filepath.Join(dir, filepath.FromSlash(rel))
			fi, err := os.Lstat(p)
			if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s: %v %v", rel, fi.Mode(), err)
			}
			for d := filepath.Dir(p); d != dir; d = filepath.Dir(d) {
				if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
					t.Fatalf("directory %s: %v %v", d, fi.Mode(), err)
				}
			}
			b, _ := os.ReadFile(p)
			if commandless[rel] {
				continue
			}
			if strings.Contains(string(b), "{{BIN}}") || !strings.Contains(string(b), "/opt/conductor") {
				t.Fatalf("%s:\n%s", rel, b)
			}
			if path.Ext(rel) == ".json" {
				var indented bytes.Buffer
				if err := json.Indent(&indented, b, "", "  "); err != nil {
					t.Fatalf("%s is not JSON: %v", rel, err)
				}
				var compact bytes.Buffer
				json.Compact(&compact, b)
				indented.Reset()
				json.Indent(&indented, compact.Bytes(), "", "  ")
				if indented.String()+"\n" != string(b) {
					t.Fatalf("%s is not indented with two spaces:\n%s", rel, b)
				}
			}
		}
	}
	if count < 12 {
		t.Fatalf("only %d assets", count)
	}
}

// Starting again with the binary somewhere else rewrites the assets.
func TestWriteAssetsFollowsTheBinary(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"/opt/conductor", "/usr/local/bin/conductor"} {
		if err := writeAssets(t, dir, bin); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "claude.json"))
	if strings.Contains(string(b), "/opt/conductor") || !strings.Contains(string(b), "/usr/local/bin/conductor notify --claude-hook") {
		t.Fatalf("claude.json:\n%s", b)
	}
}

// An asset replaced by a link is not written through; the error is the
// server's to report, not a step for the user.
func TestWriteAssetsRefusesALink(t *testing.T) {
	dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "elsewhere.json")
	os.WriteFile(outside, []byte("{}"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "claude.json")); err != nil {
		t.Fatal(err)
	}
	err := writeAssets(t, dir, "/opt/conductor")
	if err == nil || errors.Is(err, ErrByHand) || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("WriteAssets: %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "{}" {
		t.Fatalf("wrote through the link: %s", b)
	}
}

// WriteAssets owns the hooks dir: the directory is 0700 and every asset 0600,
// whatever they were before, even when the content is already right.
func TestWriteAssetsTightensModes(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(dir, "claude.json")
	os.Chmod(asset, 0o644)
	os.Chmod(dir, 0o755)
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("hooks dir %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(asset); fi.Mode().Perm() != 0o600 {
		t.Fatalf("asset %v", fi.Mode().Perm())
	}
}

// The hooks name the conductor on PATH when that is this binary, as a package
// manager's link to a versioned install is: the link survives an upgrade, the
// versioned path does not. Anything else names this binary.
func TestBinaryPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(link, "conductor")); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "conductor"), []byte("#!/bin/sh\n"), 0o755)
	for _, c := range []struct{ name, path, want string }{
		{"a link to this binary", link, filepath.Join(link, "conductor")},
		{"another conductor", other + string(os.PathListSeparator) + link, exe},
		{"none on PATH", t.TempDir(), exe},
	} {
		t.Setenv("PATH", c.path)
		if got, err := BinaryPath(); err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	// A relative PATH entry is not a path to name in a config file.
	t.Chdir(link)
	t.Setenv("PATH", ".")
	if got, err := BinaryPath(); err != nil || got != exe {
		t.Errorf("relative PATH: %q %v, want %q", got, err, exe)
	}
}

// keepBinary restores what the adapters take for the conductor binary when
// the test ends, and starts it from nothing: no assets written, no lookup.
func keepBinary(t *testing.T) {
	t.Helper()
	assetBinMu.Lock()
	oldBin, oldLookup := assetBin, lookedUp
	assetBin = ""
	assetBinMu.Unlock()
	t.Cleanup(func() {
		assetBinMu.Lock()
		assetBin, lookedUp = oldBin, oldLookup
		assetBinMu.Unlock()
	})
}

// The adapters name the binary the hooks dir was written for: after an
// upgrade changes what PATH finds, a running server's Status still compares
// an install with what its Install copies, and codex is still launched with
// that binary.
func TestAdaptersNameTheBinaryTheAssetsWereWrittenFor(t *testing.T) {
	keepBinary(t)
	lookedUp = sync.OnceValues(func() (string, error) { return "/opt/conductor-1.1/conductor", nil })
	hooks, home := t.TempDir(), t.TempDir()
	if err := writeAssets(t, hooks, "/opt/conductor-1.0/conductor"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"copilot", "claude", "codex"} {
		a, _ := Get(id)
		if _, err := a.Install(home, hooks); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Status(home); !ok {
			t.Fatalf("%s: status disagrees with the install it made", id)
		}
	}
	argv, _ := InjectFor("codex", hooks, catalog.Signal{Kind: "hook"}, false)
	if len(argv) != 8 || argv[1] != `notify=["/opt/conductor-1.0/conductor","notify","--codex"]` {
		t.Fatalf("codex launch: %q", argv)
	}
}

// WriteAssets leaves the binary it wrote the assets for in the hooks dir, as
// .bin, and another process adopts it from there: its adapters then name
// that binary, as the process that wrote the assets does, so an install it
// makes from the hooks dir reads as installed. A hooks dir without it is
// fs.ErrNotExist; a record that is not an absolute path, or is a link, is
// refused and changes nothing.
func TestAdoptBinary(t *testing.T) {
	keepBinary(t)
	lookedUp = sync.OnceValues(func() (string, error) { return "/opt/cli/conductor", nil })
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, "/opt/server/conductor"); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(hooks, ".bin")
	if b, err := os.ReadFile(record); err != nil || string(b) != "/opt/server/conductor" {
		t.Fatalf("%s: %v %q", record, err, b)
	}
	if fi, err := os.Lstat(record); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", record, fi.Mode(), err)
	}
	ForgetBinary() // from here on, a process that did not write the assets
	if bin, err := Binary(); err != nil || bin != "/opt/cli/conductor" {
		t.Fatalf("before adopting: %q %v", bin, err)
	}
	if bin, err := AdoptBinary(hooks); err != nil || bin != "/opt/server/conductor" {
		t.Fatalf("AdoptBinary: %q %v", bin, err)
	}
	if bin, err := Binary(); err != nil || bin != "/opt/server/conductor" {
		t.Fatalf("after adopting: %q %v", bin, err)
	}
	home := t.TempDir()
	for _, id := range []string{"copilot", "codex"} {
		a, _ := Get(id)
		if _, err := a.Install(home, hooks); err != nil {
			t.Fatal(err)
		}
		if ok, _ := a.Status(home); !ok {
			t.Fatalf("%s: status disagrees with the install", id)
		}
	}

	if _, err := AdoptBinary(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no record: %v", err)
	}
	for name, content := range map[string]string{"empty": "", "relative": "conductor", "not UTF-8": "/opt/\xffconductor"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".bin"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := AdoptBinary(dir); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s record: %v", name, err)
		}
	}
	link := t.TempDir()
	if err := os.Symlink(record, filepath.Join(link, ".bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptBinary(link); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a link: %v", err)
	}
	if _, err := AdoptBinary("hooks"); err == nil {
		t.Error("a relative hooks dir was accepted")
	}
	if bin, _ := Binary(); bin != "/opt/server/conductor" {
		t.Fatalf("a refused record changed the binary: %q", bin)
	}
}

// Before any assets are written the binary is looked up once, and a later
// lookup that would answer otherwise changes nothing.
func TestTheBinaryIsLookedUpOnce(t *testing.T) {
	keepBinary(t)
	calls := 0
	lookedUp = sync.OnceValues(func() (string, error) {
		calls++
		return fmt.Sprintf("/opt/conductor-%d/conductor", calls), nil
	})
	for range 3 {
		if bin, err := executable(); err != nil || bin != "/opt/conductor-1/conductor" {
			t.Fatalf("executable() = %q %v", bin, err)
		}
	}
	if calls != 1 {
		t.Fatalf("looked up %d times", calls)
	}
}

func TestWriteAssetsNeedsAbsolutePaths(t *testing.T) {
	if err := writeAssets(t, t.TempDir(), "conductor"); err == nil {
		t.Fatal("a relative binary path was accepted")
	}
	if err := writeAssets(t, "hooks", "/opt/conductor"); err == nil {
		t.Fatal("a relative hooks dir was accepted")
	}
	if err := writeAssets(t, t.TempDir(), "/opt/\xffconductor"); err == nil {
		t.Fatal("a binary path that is not UTF-8 was accepted")
	}
}

func TestAssetPathsAreLocalAndUnique(t *testing.T) {
	seen := map[string]string{}
	for _, a := range append(All(), Adapter{ID: "the skill", Assets: skillAssets}) {
		for rel := range a.Assets {
			if !filepath.IsLocal(rel) || rel != path.Clean(rel) {
				t.Errorf("%s: asset path %q", a.ID, rel)
			}
			if other, dup := seen[rel]; dup {
				t.Errorf("%s and %s both write %s", other, a.ID, rel)
			}
			seen[rel] = a.ID
		}
	}
}

func TestHooksDirs(t *testing.T) {
	if got := HooksDir("/var/lib/conductor"); got != "/var/lib/conductor/hooks" {
		t.Fatalf("HooksDir = %q", got)
	}
	if got := HooksDir(""); got != "" {
		t.Fatalf("HooksDir without a data dir = %q", got)
	}
	host := func(t *testing.T) (string, bool) {
		t.Helper()
		dir, legacy, err := HostHooksDir()
		if err != nil {
			t.Fatal(err)
		}
		return dir, legacy
	}
	mkdir := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the default is ~/.conductor/hooks", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_STATE_HOME", t.TempDir()) // no old directory in it
		if dir, legacy := host(t); dir != filepath.Join(home, ".conductor", "hooks") || legacy {
			t.Fatalf("%q legacy=%v", dir, legacy)
		}
	})
	t.Run("an old directory under XDG_STATE_HOME is kept", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		old := filepath.Join(state, "conductor", "hooks")
		mkdir(t, old)
		if dir, legacy := host(t); dir != old || !legacy {
			t.Fatalf("%q legacy=%v, want %q", dir, legacy, old)
		}
	})
	// The XDG spec says to ignore a relative XDG path; so does an empty one.
	for _, xdg := range []string{"", "state"} {
		t.Run("an old ~/.local/state directory is kept, XDG_STATE_HOME="+xdg, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", xdg)
			old := filepath.Join(home, ".local", "state", "conductor", "hooks")
			mkdir(t, old)
			if dir, legacy := host(t); dir != old || !legacy {
				t.Fatalf("%q legacy=%v, want %q", dir, legacy, old)
			}
		})
	}
	t.Run("~/.conductor/hooks wins once it exists", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_STATE_HOME", "")
		mkdir(t, filepath.Join(home, ".local", "state", "conductor", "hooks"))
		mkdir(t, filepath.Join(home, ".conductor", "hooks"))
		if dir, legacy := host(t); dir != filepath.Join(home, ".conductor", "hooks") || legacy {
			t.Fatalf("%q legacy=%v", dir, legacy)
		}
	})
	// The host's directory is its own. A server's old ./conductor.d, which
	// the server keeps by the legacy rule, is not where the host writes, and
	// the host's ~/.conductor/hooks does not end that rule
	// (config.ResolveDataDir, TestResolveDataDirDefaults).
	t.Run("a server's old ./conductor.d is not the host's", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_STATE_HOME", "")
		cwd := t.TempDir()
		mkdir(t, filepath.Join(cwd, "conductor.d", "hooks"))
		t.Chdir(cwd)
		if dir, legacy := host(t); dir != filepath.Join(home, ".conductor", "hooks") || legacy {
			t.Fatalf("%q legacy=%v", dir, legacy)
		}
	})
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/conductor":          "/opt/conductor",
		"/usr/local/bin/cond-2.1": "/usr/local/bin/cond-2.1",
		"/opt/my apps/conductor":  "'/opt/my apps/conductor'",
		`/opt/it's/conductor`:     `'/opt/it'\''s/conductor'`,
		`/opt/$HOME/c`:            `'/opt/$HOME/c'`,
		"":                        "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// oddBinary makes a directory whose name has a space, both quotes and a
// backslash, with a stand-in for conductor in it that waits $ARGS_DELAY
// seconds, then writes its arguments, one per line, to $ARGS_OUT.
func oddBinary(t *testing.T) (bin, out string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), `my "odd' dir\`)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "conductor")
	script := "#!/bin/sh\nsleep \"${ARGS_DELAY:-0}\"\nprintf '%s\\n' \"$@\" > \"$ARGS_OUT.tmp\" && mv \"$ARGS_OUT.tmp\" \"$ARGS_OUT\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, filepath.Join(t.TempDir(), "args")
}

// waitArgs waits for the stand-in to have written its arguments.
func waitArgs(t *testing.T, out string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(out); err == nil {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatal("the binary did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runShell runs command the way hook runners do, with sh -c, and returns the
// arguments the stand-in received.
func runShell(t *testing.T, command, out string) string {
	t.Helper()
	os.Remove(out)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "ARGS_OUT="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -c %q: %v %s", command, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("sh -c %q did not run the binary: %v", command, err)
	}
	return string(b)
}

// commands returns every string in a JSON document that runs conductor notify.
func commands(v any) []string {
	var out []string
	switch v := v.(type) {
	case string:
		if strings.Contains(v, " notify --") {
			out = append(out, v)
		}
	case []any:
		for _, e := range v {
			out = append(out, commands(e)...)
		}
	case map[string]any:
		for _, e := range v {
			out = append(out, commands(e)...)
		}
	}
	return out
}

// commandless lists the assets that run no command: settings without hooks
// (TestClaudeYoloSettings checks what they hold).
// claude-mcp.json names the binary as a program with its arguments apart (an
// MCP server entry), not a command line: its path is checked as JSON alone.
var commandless = map[string]bool{"claude-yolo-only.json": true, "claude-mcp.json": true, "claude-protocol-mcp.json": true}

var jsConst = regexp.MustCompile(`(?m)^const CONDUCTOR = (".*");$`)

// The binary path is the one thing in an asset that is not fixed text. A path
// with a space, both quotes and a backslash leaves every asset well formed,
// and every hook, launch flag and environment value still runs that binary.
func TestAssetsEscapeTheBinaryPath(t *testing.T) {
	bin, out := oddBinary(t)
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, bin); err != nil {
		t.Fatal(err)
	}
	for _, a := range All() {
		for rel := range a.Assets {
			b, err := os.ReadFile(filepath.Join(hooks, filepath.FromSlash(rel)))
			if err != nil || strings.Contains(string(b), "{{BIN}}") {
				t.Fatalf("%s: %v\n%s", rel, err, b)
			}
			if commandless[rel] {
				continue
			}
			switch path.Ext(rel) {
			case ".json":
				var doc any
				if err := json.Unmarshal(b, &doc); err != nil {
					t.Fatalf("%s: %v\n%s", rel, err, b)
				}
				cmds := commands(doc)
				if len(cmds) == 0 {
					t.Fatalf("%s runs no command", rel)
				}
				for _, c := range cmds {
					_, flags, _ := strings.Cut(c, " notify ")
					// The flags are plain words (`--claude-hook --files`): one argument each.
					if got := runShell(t, c, out); got != "notify\n"+strings.ReplaceAll(flags, " ", "\n")+"\n" {
						t.Fatalf("%s: %q ran with %q", rel, c, got)
					}
				}
			case ".ts", ".js":
				m := jsConst.FindSubmatch(b)
				var got string
				if m == nil || json.Unmarshal(m[1], &got) != nil || got != bin {
					t.Fatalf("%s: CONDUCTOR is %q, want %q\n%s", rel, got, bin, b)
				}
			default:
				t.Fatalf("%s: no check for this kind of asset", rel)
			}
		}
	}

	useBin(t, bin)
	hook := catalog.Signal{Kind: "hook"}
	argv, _ := InjectFor("codex", hooks, hook, false)
	var notify []string
	value, ok := strings.CutPrefix(argv[1], "notify=")
	if !ok || json.Unmarshal([]byte(value), &notify) != nil || !slices.Equal(notify, []string{bin, "notify", "--codex"}) {
		t.Fatalf("codex notify %q", argv)
	}
	_, env := InjectFor("aider", hooks, hook, false)
	if got := runShell(t, env["AIDER_NOTIFICATIONS_COMMAND"], out); got != "notify\n--state\ndone\n--message\naider is waiting\n" {
		t.Fatalf("aider command ran with %q", got)
	}
	// The snippet a user pastes into a shell sets the same command.
	a, _ := Get("aider")
	if got := runShell(t, a.Snippet(hooks)+"\n/bin/sh -c \"$AIDER_NOTIFICATIONS_COMMAND\"", out); got != "notify\n--state\ndone\n--message\naider is waiting\n" {
		t.Fatalf("aider snippet ran with %q", got)
	}

	home := t.TempDir()
	codex, _ := Get("codex")
	if _, err := codex.Install(home, hooks); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	line := regexp.MustCompile(`(?m)^notify = (\[.*\])$`).FindSubmatch(config)
	if line == nil || json.Unmarshal(line[1], &notify) != nil || !slices.Equal(notify, []string{bin, "notify", "--codex"}) {
		t.Fatalf("config.toml:\n%s", config)
	}
}

// With node at hand, every script asset loads as a module with an odd binary
// path, registers its handlers with the agent and, when one fires, runs that
// binary without waiting for it: a slow or unreachable server must never hold
// up the agent.
func TestScriptAssetsRunInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	bin, out := oddBinary(t)
	hooks := t.TempDir()
	if err := writeAssets(t, hooks, bin); err != nil {
		t.Fatal(err)
	}
	// The harness hands a plugin a fake agent: a default export gets an
	// object with on(), and its first handler is called; OpenCode's plugin gets
	// a fake Bun shell, and its event handler is told the session is idle.
	const harness = `
const mod = await import(process.argv[1]);
if (typeof mod.default === "function") {
  const handlers = [];
  mod.default({ on: (name, fn) => handlers.push([name, fn]) });
  const start = Date.now();
  await handlers[0][1]({});
  console.log(JSON.stringify({ on: handlers.map(([name]) => name), ms: Date.now() - start }));
} else {
  const calls = [];
  const $ = (strings, ...values) => { calls.push(values); const r = { quiet: () => r, nothrow: () => r, then: (ok) => ok() }; return r; };
  const plugin = Object.values(mod).find((v) => typeof v === "function");
  const hooks = await plugin({ $ });
  await hooks.event({ event: { type: "session.idle" } });
  console.log(JSON.stringify({ calls }));
}
`
	firstHandler := map[string]string{
		"pi-conductor.ts":        "agent_end",
		"omp-conductor.ts":       "agent_end",
		"amp/conductor/index.ts": "agent.end",
		"dsh-conductor.js":       "session-completed",
	}
	for _, a := range All() {
		for rel := range a.Assets {
			if ext := path.Ext(rel); ext != ".ts" && ext != ".js" {
				continue
			}
			src, _ := os.ReadFile(filepath.Join(hooks, filepath.FromSlash(rel)))
			mod := filepath.Join(t.TempDir(), "plugin.mjs")
			os.WriteFile(mod, src, 0o600)
			os.Remove(out)
			cmd := exec.Command(node, "--input-type=module", "-e", harness, (&url.URL{Scheme: "file", Path: mod}).String())
			// The stand-in takes a second: the handler must not wait for it.
			cmd.Env = append(os.Environ(), "ARGS_OUT="+out, "ARGS_DELAY=1")
			stdout, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v\n%s", rel, err, stdout)
			}
			var res struct {
				On    []string `json:"on"`
				MS    int      `json:"ms"`
				Calls [][]any  `json:"calls"`
			}
			if err := json.Unmarshal(stdout, &res); err != nil {
				t.Fatalf("%s: %v %s", rel, err, stdout)
			}
			if first, ok := firstHandler[rel]; ok {
				if len(res.On) == 0 || res.On[0] != first || res.MS > 500 {
					t.Fatalf("%s: handlers %v, the first took %d ms", rel, res.On, res.MS)
				}
				if args := waitArgs(t, out); args != "notify\n--state\ndone\n" {
					t.Fatalf("%s: the binary ran with %q", rel, args)
				}
				if strings.Contains(string(src), "spawnSync") {
					t.Fatalf("%s still waits for the binary", rel)
				}
				continue
			}
			// OpenCode: the command is the binary, then notify's arguments.
			if len(res.Calls) != 1 || len(res.Calls[0]) != 2 || res.Calls[0][0] != bin {
				t.Fatalf("%s: shell calls %v", rel, res.Calls)
			}
			if args, _ := json.Marshal(res.Calls[0][1]); string(args) != `["--state","done"]` {
				t.Fatalf("%s: notify arguments %s", rel, args)
			}
		}
	}
}

// A mode WriteAssets cannot set (a file that belongs to another user, say) is
// no reason to stop: every asset is still written, and the error, a
// ModeError, says which modes are wrong. A real failure is no ModeError.
func TestWriteAssetsReportsModesItCannotSetAndGoesOn(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(dir, "claude.json")
	if err := os.Chmod(asset, 0o644); err != nil {
		t.Fatal(err)
	}
	later := filepath.Join(dir, "cursor-hooks.json") // written after claude.json
	if err := os.Remove(later); err != nil {
		t.Fatal(err)
	}
	chmod = func(p string, m fs.FileMode) error {
		if p == asset || p == dir {
			return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrPermission}
		}
		return os.Chmod(p, m)
	}
	t.Cleanup(func() { chmod = os.Chmod })
	err := writeAssets(t, dir, "/opt/conductor")
	var me *ModeError
	if !errors.As(err, &me) || len(me.Errs) != 2 || !strings.Contains(err.Error(), asset) {
		t.Fatalf("WriteAssets: %v", err)
	}
	if _, err := os.Stat(later); err != nil {
		t.Fatalf("an asset after the one whose mode failed was not written: %v", err)
	}
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "x.json"), filepath.Join(linked, "claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeAssets(t, linked, "/opt/conductor"); err == nil || errors.As(err, &me) {
		t.Fatalf("a link is a real failure: %v", err)
	}
}

// A new or changed asset is written to a temporary file that is renamed over
// it: a mode that cannot be set on that file (a file system that keeps no
// modes) stops nothing either. The rename still puts the content in place,
// every other asset is written, and the error is a ModeError naming the
// asset, not its temporary file.
func TestWriteAssetsReportsTheModeOfANewAssetAndGoesOn(t *testing.T) {
	want := t.TempDir()
	if err := writeAssets(t, want, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "hooks") // a first write
	asset := filepath.Join(dir, "claude.json")
	var failed []string
	t.Cleanup(ReplaceChmod(func(p string, m fs.FileMode) error {
		if strings.HasPrefix(filepath.Base(p), ".claude.json.conductor-") {
			failed = append(failed, p)
			return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrPermission}
		}
		return os.Chmod(p, m)
	}))
	err := writeAssets(t, dir, "/opt/conductor")
	var me *ModeError
	if !errors.As(err, &me) || len(me.Errs) != 1 || !strings.Contains(err.Error(), asset+":") || len(failed) != 1 || strings.Contains(err.Error(), failed[0]) {
		t.Fatalf("WriteAssets: %v (failed on %q)", err, failed)
	}
	var names []string
	if err := filepath.WalkDir(want, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(want, p)
		names = append(names, rel)
		a, _ := os.ReadFile(p)
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s: %v\n%s", rel, err, b)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, "claude.json") || len(names) < 12 {
		t.Fatalf("assets %v", names)
	}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), ".conductor-") {
			t.Errorf("a temporary file is left: %s", p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The temporary file is made 0600: the asset is no more open than that.
	if fi, err := os.Stat(asset); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("claude.json: %v %v", fi, err)
	}
}

// A hooks dir that belongs to another user is no place to take the commands
// agents run from: its owner could change them. WriteAssets refuses it before
// it writes anything, whether or not it could set the directory's mode, and
// whoever runs it, root too, which can set any mode. That is a plain error,
// no ModeError, naming the directory and its owner.
func TestWriteAssetsRefusesAHooksDirOfAnotherUser(t *testing.T) {
	// anothers returns a directory of another user than the one running the
	// tests, and its owner.
	anothers := func(t *testing.T) (string, int) {
		dir := t.TempDir()
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		owner, ok := FileOwner(fi)
		if !ok {
			t.Skip("this platform does not report who owns a file")
		}
		if owner == 0 {
			// The tests run as root: make it another user's.
			if err := os.Chown(dir, 65534, 65534); err != nil {
				t.Skipf("cannot give the directory to another user: %v", err)
			}
			owner = 65534
		}
		return dir, owner
	}
	for _, root := range []bool{false, true} {
		for _, chmodFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("root %v, chmod fails %v", root, chmodFails), func(t *testing.T) {
				dir, owner := anothers(t)
				euid := owner + 1
				if root {
					euid = 0
				}
				if chmodFails {
					t.Cleanup(ReplaceChmod(func(p string, m fs.FileMode) error {
						if p == dir {
							return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrPermission}
						}
						return os.Chmod(p, m)
					}))
				}
				old := geteuid
				geteuid = func() int { return euid }
				t.Cleanup(func() { geteuid = old })
				err := writeAssets(t, dir, "/opt/conductor")
				var me *ModeError
				if err == nil || errors.As(err, &me) || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), fmt.Sprintf("uid %d", owner)) {
					t.Fatalf("WriteAssets: %v", err)
				}
				if entries, _ := os.ReadDir(dir); len(entries) != 0 {
					t.Fatalf("wrote %v", entries)
				}
			})
		}
	}
}

// The hooks dir records the version of the conductor that wrote it, beside
// the binary, for conductor hooks to compare with its own.
func TestWriteAssetsRecordsTheVersion(t *testing.T) {
	dir := t.TempDir()
	if err := writeAssets(t, dir, "/opt/conductor"); err != nil {
		t.Fatal(err)
	}
	if v, err := RecordedVersion(dir); err != nil || v != version.Version {
		t.Fatalf("RecordedVersion: %q %v", v, err)
	}
	if _, err := RecordedVersion(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no record: %v", err)
	}
}
