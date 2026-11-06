package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/store"
)

func TestLoadFileAndEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(`{"listen":":9000","maxSessions":5,"exitedRetention":"1m","allowedRoots":["`+dir+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_MAX_SESSIONS", "7")
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", "secret")
	t.Setenv("CONDUCTOR_HOST_TOKENS", "a, b")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9000" {
		t.Fatalf("listen: %s", cfg.Listen)
	}
	if cfg.MaxSessions != 7 {
		t.Fatalf("env override lost: %d", cfg.MaxSessions)
	}
	if time.Duration(cfg.ExitedRetention) != time.Minute {
		t.Fatalf("retention: %v", time.Duration(cfg.ExitedRetention))
	}
	if cfg.WorkbenchToken != "secret" || len(cfg.HostTokens) != 2 || cfg.HostTokens[1] != "b" {
		t.Fatalf("tokens: %q %v", cfg.WorkbenchToken, cfg.HostTokens)
	}
	if cfg.AllowedRoots[0] != dir {
		t.Fatalf("roots: %v", cfg.AllowedRoots)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	// Config.Path is filled by Load, not read from the file: a "path" key, in
	// any case, is as unknown as any other.
	for _, body := range []string{`{"nope":1}`, `{"path":"/etc/other.json"}`, `{"Path":"/etc/other.json"}`} {
		path := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s: got %v, want an unknown field error", body, err)
		}
	}
}

// Load records the config file it read as an absolute path, so the server can
// keep it away from file reads (it holds the admin token).
func TestLoadRecordsTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("conf", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("conf", "conductor.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "conf", "conductor.json")
	for _, path := range []string{"conf/conductor.json", "./conf/../conf/conductor.json", want} {
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Path != want {
			t.Errorf("Load(%q): Path = %q, want %q", path, cfg.Path, want)
		}
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != "" {
		t.Errorf("without a config file Path = %q, want it empty", cfg.Path)
	}
}

// A catalogPath from the file or the environment is made absolute, relative to
// the current directory like allowedRoots, and still loads.
func TestLoadMakesTheCatalogPathAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	agents := `{"agents":[{"id":"extra","name":"Extra","command":["/bin/cat"]}]}`
	if err := os.WriteFile("agents.json", []byte(agents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("conductor.json", []byte(`{"catalogPath":"agents.json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "agents.json")
	for _, tc := range []struct{ name, env string }{
		{"from the config file", ""},
		{"from the environment", "./sub/../agents.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONDUCTOR_CATALOG_PATH", tc.env)
			cfg, err := Load("conductor.json")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.CatalogPath != want {
				t.Fatalf("CatalogPath = %q, want %q", cfg.CatalogPath, want)
			}
			cat, err := cfg.LoadCatalog()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cat.Get("extra"); !ok {
				t.Fatal("the catalog file was not loaded")
			}
		})
	}
	t.Run("unset stays unset", func(t *testing.T) {
		t.Setenv("CONDUCTOR_CATALOG_PATH", "")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CatalogPath != "" {
			t.Fatalf("CatalogPath = %q, want it empty", cfg.CatalogPath)
		}
	})
}

func TestValidateBounds(t *testing.T) {
	cfg := Defaults()
	cfg.ScrollbackBytes = 1
	cfg.FileView = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestResolveDataDirDefaults(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// home gives the test a home directory of its own.
	home := func(t *testing.T) string {
		t.Helper()
		h := t.TempDir()
		t.Setenv("HOME", h)
		return h
	}
	resolve := func(t *testing.T, cfg *Config, configPath string) string {
		t.Helper()
		notice, err := cfg.ResolveDataDir(configPath)
		if err != nil {
			t.Fatal(err)
		}
		// Agent processes started in other working directories are handed
		// paths under it.
		if !filepath.IsAbs(cfg.DataDir) {
			t.Fatalf("DataDir %q is not absolute", cfg.DataDir)
		}
		return notice
	}
	mkdir := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conductor.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("unset, it is ~/.conductor wherever the config is", func(t *testing.T) {
		h := home(t)
		t.Chdir(t.TempDir())
		for _, configPath := range []string{"", "/etc/x/conductor.json", "conf/conductor.json"} {
			cfg := Defaults()
			if notice := resolve(t, cfg, configPath); notice != "" || cfg.DataDir != filepath.Join(h, ".conductor") {
				t.Fatalf("config %q: DataDir %q, notice %q", configPath, cfg.DataDir, notice)
			}
		}
	})

	t.Run("an old ./conductor.d is kept while ~/.conductor does not exist", func(t *testing.T) {
		h := home(t)
		dir := t.TempDir()
		t.Chdir(dir)
		old := filepath.Join(dir, "conductor.d")
		mkdir(t, old)
		cfg := Defaults()
		notice := resolve(t, cfg, "")
		if cfg.DataDir != old {
			t.Fatalf("DataDir %q, want the old %q", cfg.DataDir, old)
		}
		for _, want := range []string{old, filepath.Join(h, ".conductor"), "dataDir", "CONDUCTOR_DATA_DIR"} {
			if !strings.Contains(notice, want) {
				t.Errorf("notice %q does not mention %q", notice, want)
			}
		}
	})

	t.Run("an old conductor.d next to the config file is kept", func(t *testing.T) {
		home(t)
		t.Chdir(t.TempDir())
		dir := t.TempDir()
		old := filepath.Join(dir, "conductor.d")
		mkdir(t, old)
		cfg := Defaults()
		if notice := resolve(t, cfg, filepath.Join(dir, "conductor.json")); cfg.DataDir != old || notice == "" {
			t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
		}
	})

	// conductor host writes ~/.conductor/hooks for itself. That is no data of
	// a server's: an upgraded server keeps its old directory, and says so,
	// until the operator moves it.
	t.Run("host created ~/.conductor/hooks, legacy conductor.d present: legacy kept with the notice", func(t *testing.T) {
		h := home(t)
		dir := t.TempDir()
		t.Chdir(dir)
		old := filepath.Join(dir, "conductor.d")
		mkdir(t, old)
		mkdir(t, filepath.Join(h, ".conductor", "hooks"))
		cfg := Defaults()
		notice := resolve(t, cfg, "")
		if cfg.DataDir != old {
			t.Fatalf("DataDir %q, want the old %q", cfg.DataDir, old)
		}
		for _, want := range []string{old, filepath.Join(h, ".conductor"), "dataDir", "CONDUCTOR_DATA_DIR"} {
			if !strings.Contains(notice, want) {
				t.Errorf("notice %q does not mention %q", notice, want)
			}
		}
		// Without an old directory, that ~/.conductor is the data directory.
		if err := os.Remove(old); err != nil {
			t.Fatal(err)
		}
		cfg = Defaults()
		if notice := resolve(t, cfg, ""); cfg.DataDir != filepath.Join(h, ".conductor") || notice != "" {
			t.Fatalf("without the old directory: DataDir %q, notice %q", cfg.DataDir, notice)
		}
	})

	t.Run("~/.conductor wins once it holds server data", func(t *testing.T) {
		for _, data := range []string{"catalog.json", "crews", "crews.json"} {
			t.Run(data, func(t *testing.T) {
				h := home(t)
				dir := t.TempDir()
				t.Chdir(dir)
				mkdir(t, filepath.Join(dir, "conductor.d"))
				def := filepath.Join(h, ".conductor")
				mkdir(t, filepath.Join(def, "hooks"))
				if data == "crews" {
					mkdir(t, filepath.Join(def, data))
				} else if err := os.WriteFile(filepath.Join(def, data), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg := Defaults()
				if notice := resolve(t, cfg, ""); cfg.DataDir != def || notice != "" {
					t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
				}
			})
		}
	})

	t.Run("a file called conductor.d is no old data directory", func(t *testing.T) {
		h := home(t)
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.WriteFile(filepath.Join(dir, "conductor.d"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Defaults()
		if notice := resolve(t, cfg, ""); cfg.DataDir != filepath.Join(h, ".conductor") || notice != "" {
			t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
		}
	})

	// In a shared working directory anyone may make ./conductor.d (or the
	// config's directory may be shared), and the catalog.json in it would
	// choose the commands the server launches. An old directory is kept only
	// when it is a real directory, not a link, of the user running conductor
	// serve; anything else under that name stops the server with an error
	// naming it, its owner and the settings that choose a data directory,
	// rather than leaving the old data behind without a word. The places it
	// is looked for, and a home that is unknown, change nothing.
	refusedCases := []struct {
		name           string
		noHome, config bool
	}{
		{"in the working directory", false, false},
		{"next to the config file", false, true},
		{"without a home directory", true, false},
	}
	// refused resolves the data directory with the old directory old, in
	// dir, made by the caller, and checks that it is refused, naming want.
	refused := func(t *testing.T, dir, old string, noHome, config bool, want ...string) {
		t.Helper()
		if noHome {
			t.Setenv("HOME", "")
		}
		t.Chdir(dir)
		configPath := ""
		if config {
			t.Chdir(t.TempDir())
			configPath = filepath.Join(dir, "conductor.json")
		}
		cfg := Defaults()
		notice, err := cfg.ResolveDataDir(configPath)
		if err == nil {
			t.Fatalf("no error: DataDir %q, notice %q", cfg.DataDir, notice)
		}
		for _, w := range append([]string{old, "dataDir", "CONDUCTOR_DATA_DIR"}, want...) {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error %q does not mention %q", err, w)
			}
		}
		if errors.Is(err, ErrNoHome) || cfg.DataDir != "" {
			t.Errorf("DataDir %q, error %v", cfg.DataDir, err)
		}
	}
	useEuid := func(t *testing.T, uid int) {
		t.Helper()
		old := geteuid
		geteuid = func() int { return uid }
		t.Cleanup(func() { geteuid = old })
	}

	t.Run("a conductor.d that is a link is refused", func(t *testing.T) {
		for _, tc := range refusedCases {
			t.Run(tc.name, func(t *testing.T) {
				home(t)
				dir := t.TempDir()
				old := filepath.Join(dir, "conductor.d")
				// A directory of the user's own, through a link.
				if err := os.Symlink(t.TempDir(), old); err != nil {
					t.Fatal(err)
				}
				want := []string{"symbolic link"}
				if fi, err := os.Lstat(old); err != nil {
					t.Fatal(err)
				} else if owner, ok := agents.FileOwner(fi); ok {
					want = append(want, fmt.Sprintf("uid %d", owner))
				}
				refused(t, dir, old, tc.noHome, tc.config, want...)
			})
		}
	})

	// Another user's directory is refused whoever runs the server, root too:
	// root could read it, but its owner could change what root launches.
	t.Run("a conductor.d of another user is refused", func(t *testing.T) {
		for _, tc := range refusedCases {
			t.Run(tc.name, func(t *testing.T) {
				home(t)
				dir := t.TempDir()
				old := filepath.Join(dir, "conductor.d")
				mkdir(t, old)
				fi, err := os.Lstat(old)
				if err != nil {
					t.Fatal(err)
				}
				owner, ok := agents.FileOwner(fi)
				if !ok {
					t.Skip("this platform does not report who owns a file")
				}
				if owner == 0 {
					// The tests run as root: make it another user's.
					if err := os.Chown(old, 65534, 65534); err != nil {
						t.Skipf("cannot give the directory to another user: %v", err)
					}
					owner = 65534
				}
				for _, euid := range []int{owner + 1, 0} {
					t.Run(fmt.Sprintf("euid %d", euid), func(t *testing.T) {
						useEuid(t, euid)
						refused(t, dir, old, tc.noHome, tc.config, fmt.Sprintf("uid %d", owner), fmt.Sprintf("uid %d", euid))
					})
				}
				// Its owner keeps it, as before.
				useEuid(t, owner)
				if tc.noHome {
					t.Setenv("HOME", "")
				}
				t.Chdir(dir)
				cfg := Defaults()
				if notice := resolve(t, cfg, ""); cfg.DataDir != old || notice == "" {
					t.Fatalf("its owner: DataDir %q, notice %q", cfg.DataDir, notice)
				}
			})
		}
	})

	// Once ~/.conductor holds server data, an old directory is not used,
	// and one that would be refused is no reason to stop.
	t.Run("a conductor.d that would be refused does not matter once ~/.conductor holds server data", func(t *testing.T) {
		h := home(t)
		dir := t.TempDir()
		t.Chdir(dir)
		old := filepath.Join(dir, "conductor.d")
		target := t.TempDir()
		mkdir(t, filepath.Join(target, "crews"))
		if err := os.Symlink(target, old); err != nil {
			t.Fatal(err)
		}
		def := filepath.Join(h, ".conductor")
		mkdir(t, def)
		if err := os.WriteFile(filepath.Join(def, "catalog.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Defaults()
		if notice := resolve(t, cfg, ""); cfg.DataDir != def || notice != "" {
			t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
		}
	})

	t.Run("without a home directory the error names the settings", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Chdir(t.TempDir()) // no old directory either
		cfg := Defaults()
		_, err := cfg.ResolveDataDir("")
		if err == nil || !strings.Contains(err.Error(), "dataDir") || !strings.Contains(err.Error(), "CONDUCTOR_DATA_DIR") || !errors.Is(err, ErrNoHome) {
			t.Fatalf("no home: %v", err)
		}
	})

	// An upgraded service started without HOME keeps working: the home is
	// needed only when there is no old directory to keep.
	t.Run("without a home directory an old conductor.d is kept with the notice", func(t *testing.T) {
		t.Setenv("HOME", "")
		dir := t.TempDir()
		t.Chdir(dir)
		old := filepath.Join(dir, "conductor.d")
		mkdir(t, old)
		for _, configPath := range []string{"", filepath.Join(dir, "conductor.json")} {
			cfg := Defaults()
			notice := resolve(t, cfg, configPath)
			if cfg.DataDir != old {
				t.Fatalf("config %q: DataDir %q, want the old %q", configPath, cfg.DataDir, old)
			}
			for _, want := range []string{old, "~/.conductor", "home directory is unknown", "dataDir", "CONDUCTOR_DATA_DIR"} {
				if !strings.Contains(notice, want) {
					t.Errorf("notice %q does not mention %q", notice, want)
				}
			}
		}
	})

	// ~/.conductor wins, and the old directory's own server data would be
	// lost from sight without a word: the notice names it.
	t.Run("an old directory that holds server data too is named when ~/.conductor wins", func(t *testing.T) {
		h := home(t)
		dir := t.TempDir()
		t.Chdir(dir)
		old := filepath.Join(dir, "conductor.d")
		mkdir(t, filepath.Join(old, "crews"))
		def := filepath.Join(h, ".conductor")
		mkdir(t, def)
		if err := os.WriteFile(filepath.Join(def, "catalog.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Defaults()
		notice := resolve(t, cfg, "")
		if cfg.DataDir != def {
			t.Fatalf("DataDir %q, want %q", cfg.DataDir, def)
		}
		for _, want := range []string{old, def, "dataDir", "CONDUCTOR_DATA_DIR"} {
			if !strings.Contains(notice, want) {
				t.Errorf("notice %q does not mention %q", notice, want)
			}
		}
	})

	// A ~/.conductor that cannot be looked into is reported as what it is,
	// never as a directory that holds no server data yet.
	t.Run("a ~/.conductor that cannot be checked is an error", func(t *testing.T) {
		for _, tc := range []struct {
			name, want string
			make       func(t *testing.T, def string)
		}{
			{"a file in its place", "not a directory", func(t *testing.T, def string) {
				if err := os.WriteFile(def, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{"no permission", "permission denied", func(t *testing.T, def string) {
				if os.Geteuid() == 0 {
					t.Skip("root looks into a directory of mode 0000")
				}
				mkdir(t, def)
				if err := os.Chmod(def, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(def, 0o700) })
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := home(t)
				dir := t.TempDir()
				t.Chdir(dir)
				mkdir(t, filepath.Join(dir, "conductor.d"))
				def := filepath.Join(h, ".conductor")
				tc.make(t, def)
				cfg := Defaults()
				notice, err := cfg.ResolveDataDir("")
				if err == nil {
					t.Fatalf("no error: DataDir %q, notice %q", cfg.DataDir, notice)
				}
				for _, want := range []string{def, tc.want, "dataDir", "CONDUCTOR_DATA_DIR"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				if strings.Contains(err.Error(), "no server data") {
					t.Errorf("error %q says there is no server data", err)
				}
			})
		}
	})

	t.Run("an absolute value from the config file is kept", func(t *testing.T) {
		home(t)
		t.Setenv("CONDUCTOR_DATA_DIR", "")
		path := writeConfig(t, `{"dataDir":"/srv/from-file"}`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if notice := resolve(t, cfg, path); cfg.DataDir != "/srv/from-file" || notice != "" {
			t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
		}
	})

	t.Run("a relative value from the config file is made absolute", func(t *testing.T) {
		home(t)
		t.Setenv("CONDUCTOR_DATA_DIR", "")
		path := writeConfig(t, `{"dataDir":"state/data"}`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		resolve(t, cfg, path)
		// Relative to the current directory like allowedRoots and defaultCwd,
		// not to the config file.
		if cfg.DataDir != filepath.Join(cwd, "state", "data") {
			t.Fatalf("DataDir %q", cfg.DataDir)
		}
	})

	t.Run("CONDUCTOR_DATA_DIR wins and is kept", func(t *testing.T) {
		home(t)
		t.Setenv("CONDUCTOR_DATA_DIR", "/srv/from-env")
		path := writeConfig(t, `{"dataDir":"/srv/from-file"}`)
		for _, configPath := range []string{path, ""} {
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if notice := resolve(t, cfg, configPath); cfg.DataDir != "/srv/from-env" || notice != "" {
				t.Fatalf("DataDir %q, notice %q", cfg.DataDir, notice)
			}
		}
	})

	t.Run("a relative CONDUCTOR_DATA_DIR is made absolute", func(t *testing.T) {
		home(t)
		t.Setenv("CONDUCTOR_DATA_DIR", "rel/dir")
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if resolve(t, cfg, ""); cfg.DataDir != filepath.Join(cwd, "rel", "dir") {
			t.Fatalf("DataDir %q", cfg.DataDir)
		}
	})

	// A value that was set stays relative when the working directory is gone:
	// filepath.Abs needs it, and the chosen value beats an empty one. The
	// default comes from the home directory and is absolute regardless.
	t.Run("the working directory is gone", func(t *testing.T) {
		h := home(t)
		gone := t.TempDir()
		t.Chdir(gone)
		if err := os.Remove(gone); err != nil {
			t.Skipf("cannot remove the working directory: %v", err)
		}
		if _, err := filepath.Abs("x"); err == nil {
			t.Skip("the working directory is still resolvable on this platform")
		}
		cfg := Defaults()
		cfg.DataDir = "rel/dir"
		if _, err := cfg.ResolveDataDir(""); err != nil || cfg.DataDir != "rel/dir" {
			t.Fatalf("explicit value: DataDir %q %v", cfg.DataDir, err)
		}
		cfg = Defaults()
		if _, err := cfg.ResolveDataDir(""); err != nil || cfg.DataDir != filepath.Join(h, ".conductor") {
			t.Fatalf("default: DataDir %q %v", cfg.DataDir, err)
		}
	})
}

func TestDataDirOverlap(t *testing.T) {
	base := t.TempDir()
	mkdir := func(parts ...string) string {
		t.Helper()
		p := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	proj := mkdir("proj")
	projData := mkdir("proj", "conductor.d")
	sibling := mkdir("proj-data") // shares a name prefix with proj, nothing else
	elsewhere := mkdir("var", "lib", "conductor")
	nested := mkdir("var", "lib", "conductor", "workspaces")
	link := filepath.Join(base, "link-to-proj")
	if err := os.Symlink(proj, link); err != nil {
		t.Fatal(err)
	}
	// A root reached through a symlinked parent, as every temp directory is on
	// macOS (/var is a symlink to /private/var).
	mkdir("real", "root")
	viaLink := filepath.Join(base, "link", "root")
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		roots   []string
		dataDir string
		want    string
	}{
		{"data directory inside a root", []string{proj}, projData, proj},
		{"data directory not created yet inside a root", []string{proj}, filepath.Join(proj, "later"), proj},
		{"data directory is a root", []string{proj}, proj, proj},
		{"root inside the data directory", []string{nested}, elsewhere, nested},
		{"the second root overlaps", []string{elsewhere + "-x", proj}, projData, proj},
		{"root given through a symlink", []string{link}, projData, link},
		{"data directory given through a symlink", []string{proj}, filepath.Join(link, "conductor.d"), proj},
		// The data directory does not exist yet, so only its existing ancestors
		// resolve; the missing part is joined back on to compare.
		{"data directory not created yet under a symlinked root", []string{viaLink}, filepath.Join(viaLink, "conductor.d"), viaLink},
		{"data directory two levels below a symlinked root, not created yet", []string{viaLink}, filepath.Join(viaLink, "state", "conductor.d"), viaLink},
		{"data directory not created yet beside a symlinked root", []string{viaLink}, filepath.Join(base, "link", "root-data", "conductor.d"), ""},
		{"sibling with a common name prefix", []string{proj}, sibling, ""},
		{"separate directories", []string{proj}, elsewhere, ""},
		{"the file system root holds everything", []string{"/"}, elsewhere, "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.AllowedRoots = tc.roots
			cfg.DataDir = tc.dataDir
			if got := cfg.DataDirOverlap(); got != tc.want {
				t.Fatalf("DataDirOverlap() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The config file is held to what the store holds its files to: one JSON
// value and nothing after it.
func TestLoadRejectsTrailingDataAndEmptyFiles(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"listen":":9000"} oops`, "after the JSON value"},
		{`{"listen":":9000"}{"listen":":9001"}`, "more than one JSON value"},
		{"", "empty document"},
	} {
		path := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%q: %v, want an error naming %s and saying %q", tc.body, err, path, tc.want)
		}
	}
}

// An agent of the config that names an adapter Conductor does not have stops
// startup, as the Agents page refuses it.
func TestLoadCatalogChecksTheAdapter(t *testing.T) {
	cfg := Defaults()
	cfg.Catalog = catalog.File{Agents: []catalog.Agent{{ID: "g", Name: "g", Command: []string{"g"}, Adapter: "gemini"}}}
	if _, err := cfg.LoadCatalog(); err == nil || !strings.Contains(err.Error(), `config catalog: agent g: unknown adapter "gemini"`) {
		t.Fatalf("inline: %v", err)
	}
	cfg.Catalog.Agents[0].Adapter = "claude"
	if _, err := cfg.LoadCatalog(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"id":"h","name":"h","command":["h"],"adapter":"nope"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = Defaults()
	cfg.CatalogPath = path
	if _, err := cfg.LoadCatalog(); err == nil || !strings.Contains(err.Error(), "catalog "+path+`: agent h: unknown adapter "nope"`) {
		t.Fatalf("catalog file: %v", err)
	}
	// With both, each agent is named with the catalog it comes from.
	cfg.Catalog.Agents = []catalog.Agent{{ID: "g", Name: "g", Command: []string{"g"}, Adapter: "gemini"}}
	_, err := cfg.LoadCatalog()
	if err == nil || !strings.Contains(err.Error(), `config catalog: agent g: unknown adapter "gemini"`) || !strings.Contains(err.Error(), "catalog "+path+`: agent h: unknown adapter "nope"`) {
		t.Fatalf("both: %v", err)
	}
}

// CONDUCTOR_EXAMPLES turns the example-crew seeding on; it is not a config-file key.
func TestExamplesComesFromTheEnvironmentOnly(t *testing.T) {
	for _, v := range []string{"1", "true"} {
		t.Setenv("CONDUCTOR_EXAMPLES", v)
		if cfg, err := Load(""); err != nil || !cfg.Examples {
			t.Fatalf("CONDUCTOR_EXAMPLES=%s: %+v %v", v, cfg, err)
		}
	}
	for _, v := range []string{"", "0", "yes"} {
		t.Setenv("CONDUCTOR_EXAMPLES", v)
		if cfg, err := Load(""); err != nil || cfg.Examples {
			t.Fatalf("CONDUCTOR_EXAMPLES=%q: %+v %v", v, cfg, err)
		}
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"examples": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "examples") {
		t.Fatalf("a config file with examples should be rejected as an unknown field: %v", err)
	}
}

// yolo comes from the config file, and CONDUCTOR_YOLO overrides it either way.
func TestYoloFromTheFileAndTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"yolo": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file, env string
		want      bool
	}{
		{"", "", false},
		{path, "", true},
		{"", "1", true},
		{"", "true", true},
		{path, "0", false},
		{path, "false", false},
		{"", "yes", false},
	} {
		t.Setenv("CONDUCTOR_YOLO", tc.env)
		if cfg, err := Load(tc.file); err != nil || cfg.Yolo != tc.want {
			t.Errorf("file %q, CONDUCTOR_YOLO=%q: %v %v", tc.file, tc.env, cfg.Yolo, err)
		}
	}
}

// publicUrl names this machine alone by default and in the example config:
// a share link built on it would reach no one else, so it reads as local.
func TestPublicURLIsLocal(t *testing.T) {
	for u, want := range map[string]bool{
		"http://localhost:8080": true, "http://127.0.0.1:8080": true, "http://[::1]:8080": true, "HTTP://LOCALHOST": true,
		"https://conductor.example.com": false, "http://192.168.1.20:8080": false, "http://conductor.lan": false,
	} {
		if got := (&Config{PublicURL: u}).PublicURLIsLocal(); got != want {
			t.Errorf("%s: local %v, want %v", u, got, want)
		}
	}
	cfg := Defaults()
	cfg.PublicURL = "localhost:8080"
	if err := cfg.Validate(); err == nil {
		t.Fatal("a publicUrl without a scheme passed")
	}
}

func TestReachConfigBoundsAndEnv(t *testing.T) {
	cfg := Defaults()
	if cfg.Reach.Mode != ReachAuto || cfg.Reach.PublicPort != 443 || cfg.STUNServer() != "stun:stun.l.google.com:19302" {
		t.Fatalf("defaults %+v stun=%q", cfg.Reach, cfg.STUNServer())
	}
	env := map[string]string{"CONDUCTOR_REACH": "manual", "CONDUCTOR_REACH_PUBLIC_PORT": "8443", "CONDUCTOR_REACH_STUN": "stun:stun.example.net:3478", "CONDUCTOR_REACH_PUBLIC_PORT_80": "true"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Reach.Mode != ReachManual || cfg.Reach.PublicPort != 8443 || !cfg.Reach.PublicPort80 || cfg.STUNServer() != "stun:stun.example.net:3478" {
		t.Fatalf("env %+v", cfg.Reach)
	}
	for name, mutate := range map[string]func(*Config){
		"mode": func(c *Config) { c.Reach.Mode = "always" },
		"port": func(c *Config) { c.Reach.PublicPort = 0 },
		"no stun": func(c *Config) {
			c.Reach.STUNServer = ""
			c.ICEServers = []ICEServer{{URLs: []string{"turn:relay.example.net"}}}
		},
	} {
		c := Defaults()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	off := Defaults()
	off.Reach.Mode = ReachOff
	off.ICEServers = nil
	if err := off.Validate(); err != nil {
		t.Fatalf("off needs no STUN server: %v", err)
	}
}

func TestTLSConfigValidation(t *testing.T) {
	ok := func(name string, mutate func(*Config)) *Config {
		t.Helper()
		c := Defaults()
		mutate(c)
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return c
	}
	bad := func(name, want string, mutate func(*Config)) {
		t.Helper()
		c := Defaults()
		mutate(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	if c := Defaults(); c.TLSEnabled() || c.TLS.Listen != "" {
		t.Fatalf("defaults %+v", c.TLS)
	}
	c := ok("acme for the public address", func(c *Config) { c.TLS.ACME = &ACME{Email: "me@example.net"} })
	if c.TLS.Listen != ":8443" || c.TLS.ACME.Challenge != ChallengeTLSALPN || c.TLS.ACME.Profile != "shortlived" || !c.ACMEIdentifiersAreIPs() {
		t.Fatalf("filled %+v %+v", c.TLS, c.TLS.ACME)
	}
	c = ok("acme for a domain", func(c *Config) {
		c.TLS.ACME = &ACME{Domains: []string{"home.example.net"}, Challenge: ChallengeDNS, DNSProvider: "cloudflare"}
	})
	if c.TLS.ACME.Profile != "" || c.ACMEIdentifiersAreIPs() {
		t.Fatalf("domain %+v", c.TLS.ACME)
	}
	ok("files", func(c *Config) { c.TLS.CertFile = "cert.pem"; c.TLS.KeyFile = "key.pem" })
	ok("literal ip", func(c *Config) { c.TLS.ACME = &ACME{Domains: []string{"203.0.113.9"}} })
	ok("http-01 with port 80 mapped", func(c *Config) { c.TLS.ACME = &ACME{Challenge: ChallengeHTTP}; c.Reach.PublicPort80 = true })
	ok("acme with reach manual", func(c *Config) { c.TLS.ACME = &ACME{}; c.Reach.Mode = ReachManual })
	bad("files and acme", "exclusive", func(c *Config) { c.TLS.CertFile = "a"; c.TLS.KeyFile = "b"; c.TLS.ACME = &ACME{} })
	bad("cert without key", "go together", func(c *Config) { c.TLS.CertFile = "a" })
	bad("same listen", "differ", func(c *Config) { c.TLS.ACME = &ACME{}; c.TLS.Listen = ":8080" })
	bad("listen without a source", "need tls.acme", func(c *Config) { c.TLS.Listen = ":8443" })
	bad("dns-01 for an ip", "cannot prove an IP", func(c *Config) { c.TLS.ACME = &ACME{Challenge: ChallengeDNS, DNSProvider: "exec"} })
	bad("wrong profile for an ip", "shortlived", func(c *Config) { c.TLS.ACME = &ACME{Profile: "classic"} })
	bad("no domains and reach off", "needs reach", func(c *Config) { c.TLS.ACME = &ACME{}; c.Reach.Mode = ReachOff })
	bad("public port not 443", "443", func(c *Config) { c.TLS.ACME = &ACME{}; c.Reach.PublicPort = 8443 })
	bad("http-01 without port 80", "publicPort80", func(c *Config) { c.TLS.ACME = &ACME{Challenge: ChallengeHTTP} })
	bad("dns-01 without a provider", "dnsProvider", func(c *Config) { c.TLS.ACME = &ACME{Domains: []string{"a.example"}, Challenge: ChallengeDNS} })
	bad("unknown provider", "cloudflare, exec or httpreq", func(c *Config) {
		c.TLS.ACME = &ACME{Domains: []string{"a.example"}, Challenge: ChallengeDNS, DNSProvider: "route53"}
	})
	bad("unknown challenge", "challenge must be", func(c *Config) { c.TLS.ACME = &ACME{Challenge: "dns-02"} })
	bad("bad ca", "https", func(c *Config) { c.TLS.ACME = &ACME{CADirectory: "http://ca.example/dir"} })
	bad("bad domain", "not a DNS name", func(c *Config) { c.TLS.ACME = &ACME{Domains: []string{"a b"}} })
}

func TestTLSEnvOverrides(t *testing.T) {
	cfg := Defaults()
	env := map[string]string{
		"CONDUCTOR_TLS_LISTEN": ":9443", "CONDUCTOR_TLS_ACME_EMAIL": "me@example.net", "CONDUCTOR_TLS_ACME_DOMAINS": "home.example.net, alt.example.net",
		"CONDUCTOR_TLS_ACME_CHALLENGE": "dns-01", "CONDUCTOR_TLS_ACME_DNS_PROVIDER": "cloudflare", "CONDUCTOR_TLS_ACME_DNS_ENV": "CLOUDFLARE_DNS_API_TOKEN=tok,X=1",
		"CONDUCTOR_TLS_ACME_CA": "https://acme-staging-v02.api.letsencrypt.org/directory", "CONDUCTOR_TLS_ACME_PROFILE": "classic",
	}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a := cfg.TLS.ACME
	if cfg.TLS.Listen != ":9443" || a == nil || a.Email != "me@example.net" || len(a.Domains) != 2 || a.Domains[1] != "alt.example.net" || a.Challenge != "dns-01" || a.DNSProvider != "cloudflare" || a.DNSEnv["CLOUDFLARE_DNS_API_TOKEN"] != "tok" || a.DNSEnv["X"] != "1" || a.Profile != "classic" || !strings.Contains(a.CADirectory, "staging") {
		t.Fatalf("%+v %+v", cfg.TLS, a)
	}
	plain := Defaults()
	if err := applyEnv(plain, func(k string) string { return map[string]string{"CONDUCTOR_TLS_ACME": "1"}[k] }); err != nil {
		t.Fatal(err)
	}
	if err := plain.Validate(); err != nil || plain.TLS.ACME == nil || plain.TLS.Listen != ":8443" {
		t.Fatalf("CONDUCTOR_TLS_ACME=1: %v %+v", err, plain.TLS)
	}
}

func TestRendezvousConfig(t *testing.T) {
	cfg := Defaults()
	env := map[string]string{"CONDUCTOR_RENDEZVOUS_SERVER": "https://team.example.net/", "CONDUCTOR_RENDEZVOUS_TOKEN": "tok", "CONDUCTOR_RENDEZVOUS_HOST_NAME": "office", "CONDUCTOR_RENDEZVOUS_RELAY_ONLY": "1"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if r := cfg.Rendezvous; r.Server != "https://team.example.net" || r.Token != "tok" || r.HostName != "office" || !r.RelayOnly || cfg.RendezvousServer() != "https://team.example.net" {
		t.Fatalf("%+v", r)
	}
	bad := Defaults()
	bad.Rendezvous = Rendezvous{Server: "team.example.net", Token: "t"}
	if err := bad.Validate(); err == nil {
		t.Error("bad url: want an error")
	}
	// Itself is no config error: conductor serve refuses it once its flags
	// are applied (TestPublishesToItself).
	self := Defaults()
	self.PublicURL = "https://me.example.net"
	self.Rendezvous = Rendezvous{Server: "https://me.example.net/"}
	if err := self.Validate(); err != nil || !self.PublishesToItself() {
		t.Errorf("itself: %v, publishes to itself %v", err, self.PublishesToItself())
	}
	for name, mutate := range map[string]func(*Config){
		"no token":   func(c *Config) { c.Rendezvous.Server = "https://team.example.net" },
		"token only": func(c *Config) { c.Rendezvous.Token = "t" },
	} {
		c := Defaults()
		mutate(c)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Publishing is on by default, to the public switchyard; the environment or
// the file turns it off or names another; a switchyard publishes nowhere.
func TestRendezvousDefaultsToThePublicSwitchyard(t *testing.T) {
	cfg := Defaults()
	if cfg.RendezvousServer() != DefaultRendezvousServer || DefaultRendezvousServer != "https://switchyard.rslabs.net" {
		t.Fatalf("default: %q", cfg.RendezvousServer())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults validate: %v", err)
	}
	off := Defaults()
	if err := applyEnv(off, func(k string) string { return map[string]string{"CONDUCTOR_RENDEZVOUS": "0"}[k] }); err != nil || off.RendezvousServer() != "" {
		t.Fatalf("CONDUCTOR_RENDEZVOUS=0: %v %q", err, off.RendezvousServer())
	}
	on := Defaults()
	if err := applyEnv(on, func(k string) string { return map[string]string{"CONDUCTOR_RENDEZVOUS": "1"}[k] }); err != nil || on.RendezvousServer() != DefaultRendezvousServer {
		t.Fatalf("CONDUCTOR_RENDEZVOUS=1: %v %q", err, on.RendezvousServer())
	}
	no := false
	file := Defaults()
	file.Rendezvous.Enabled = &no
	if file.RendezvousServer() != "" {
		t.Fatal("enabled: false in the file")
	}
	sy := Defaults()
	sy.Switchyard.Enabled = true
	if sy.RendezvousServer() != "" {
		t.Fatal("a switchyard publishes to no switchyard")
	}
	// A server whose own public URL is the public switchyard must not publish
	// to itself; as a switchyard it publishes nowhere.
	self := Defaults()
	self.PublicURL = DefaultRendezvousServer + "/"
	if err := self.Validate(); err != nil || !self.PublishesToItself() {
		t.Fatalf("itself by default: %v, publishes to itself %v", err, self.PublishesToItself())
	}
	self.Switchyard.Enabled = true
	if self.PublishesToItself() {
		t.Fatal("a switchyard whose public URL is the public switchyard publishes to itself")
	}
}

// The switchyard is this server when publicUrl, naming another machine,
// names the same server, or when a listener of this server takes the
// switchyard's address: its port, at a host that listener accepts. A local
// publicUrl is not compared (serve moves it to the bound port), nor a
// listener on port 0.
func TestPublishesToItself(t *testing.T) {
	resolveLocalhostTo(t, "127.0.0.1", "::1")
	for _, tc := range []struct {
		name              string
		listen, tlsListen string // a tlsListen turns TLS on, by certificate files
		publicURL, server string // "" keeps the default
		want              bool
	}{
		{"the desktop app: a free port, the switchyard on 8080", "127.0.0.1:0", "", "", "http://localhost:8080", false},
		{"a free port, publicUrl the switchyard's, both local", "127.0.0.1:0", "", "http://localhost:9000", "http://localhost:9000", false},
		{"the default listen, the switchyard on its port", ":8080", "", "", "http://localhost:8080", true},
		{"its own port by localhost", "127.0.0.1:9000", "", "", "http://localhost:9000/", true},
		{"its own port at another path", "127.0.0.1:9000", "", "", "http://localhost:9000/yard", true},
		{"in capitals", "127.0.0.1:9000", "", "HTTP://LOCALHOST:9000", "HTTP://LocalHost:9000", true},
		{"a leading zero", "127.0.0.1:9000", "", "", "http://localhost:09000", true},
		{"a service name for the listener's port", ":http", "", "", "http://localhost", true},
		{"port 80 by the scheme", ":80", "", "", "http://127.0.0.1/", true},
		{"port 443 by the scheme", "127.0.0.1:8080", "127.0.0.1:443", "", "https://localhost", true},
		{"the TLS listener", ":8080", ":8443", "", "https://localhost:8443", true},
		{"the TLS listener, by an unspecified address", ":8080", "[::]:8443", "", "https://127.0.0.1:8443", true},
		{"TLS off: its listen is not a listener", ":8080", "", "", "https://localhost:8443", false},
		{"another port", ":8080", "", "", "http://localhost:8081", false},
		{"another loopback address", "127.0.0.1:9000", "", "", "http://127.0.0.2:9000", false},
		{"::1, the listener on 127.0.0.1", "127.0.0.1:9000", "", "", "http://[::1]:9000", false},
		{"a mapped 127.0.0.1", "127.0.0.1:9000", "", "", "http://[::ffff:127.0.0.1]:9000", true},
		{"::1, the listener on all addresses", ":9000", "", "", "http://[::1]:9000", true},
		{"127.8.9.10, the listener on 0.0.0.0", "0.0.0.0:9000", "", "", "http://127.8.9.10:9000", true},
		{"0.0.0.0, the listener on ::", "[::]:9000", "", "", "http://0.0.0.0:9000", true},
		{":: , the listener on ::1", "[::1]:9000", "", "", "http://[::]:9000", true},
		{"localhost, the listener on ::1", "[::1]:9000", "", "", "http://localhost:9000", true},
		{"localhost, the listener on 127.0.0.2", "127.0.0.2:9000", "", "http://127.0.0.2:9000", "http://localhost:9000", false},
		{"0.0.0.0, the listener on 127.0.0.2", "127.0.0.2:9000", "", "", "http://0.0.0.0:9000", false},
		{"127.0.0.2, the listener on 127.0.0.2", "127.0.0.2:9000", "", "", "http://127.0.0.2:9000", true},
		{"localhost, the listener on localhost", "localhost:9000", "", "", "http://LOCALHOST:9000", true},
		{"127.0.0.1, the listener on localhost", "localhost:9000", "", "", "http://127.0.0.1:9000", true},
		{"::1, the listener on localhost (127.0.0.1)", "localhost:9000", "", "", "http://[::1]:9000", false},
		{"0.0.0.0, the listener on localhost", "localhost:9000", "", "", "http://0.0.0.0:9000", true},
		{"::, the listener on 127.0.0.1 (the dial falls back to 0.0.0.0)", "127.0.0.1:9000", "", "", "http://[::]:9000", true},
		{"::, the listener on 127.0.0.2", "127.0.0.2:9000", "", "", "http://[::]:9000", false},
		{"no host, the listener on 127.0.0.1", "127.0.0.1:9000", "", "", "http://:9000", true},
		{"no host (dialed over IPv4), the listener on ::1", "[::1]:9000", "", "", "http://:9000", false},
		{"127.0.0.2, the listener on localhost", "localhost:9000", "", "", "http://127.0.0.2:9000", false},
		{"localhost, the listener on a LAN address", "192.0.2.5:8080", "", "", "http://localhost:8080", false},
		{"0.0.0.0, the listener on a LAN address", "192.0.2.5:8080", "", "", "http://0.0.0.0:8080", false},
		{"the same LAN address", "192.0.2.5:8080", "", "", "http://192.0.2.5:8080", true},
		{"the same name", "conductor.lan:8080", "", "", "http://Conductor.LAN:8080", true},
		{"another machine, the same port", ":8080", "", "", "https://yard.example:8080", false},
		{"publicUrl the same server", "127.0.0.1:0", "", "https://c.example", "https://C.example:443/", true},
		{"publicUrl the same server, a path", "127.0.0.1:0", "", "https://c.example/team", "https://c.example/team/", true},
		{"publicUrl the public switchyard", "127.0.0.1:0", "", DefaultRendezvousServer, "", true},
		{"publicUrl, the switchyard at another path", "127.0.0.1:0", "", "https://c.example", "https://c.example/yard", false},
		{"publicUrl, the switchyard at another port", "127.0.0.1:0", "", "https://c.example", "https://c.example:8443", false},
		{"publicUrl over http, the switchyard over https", "127.0.0.1:0", "", "http://c.example", "https://c.example", false},
	} {
		c := Defaults()
		c.Listen = tc.listen
		if tc.tlsListen != "" {
			c.TLS.CertFile, c.TLS.KeyFile, c.TLS.Listen = "cert.pem", "key.pem", tc.tlsListen
		}
		if tc.publicURL != "" {
			c.PublicURL = tc.publicURL
		}
		c.Rendezvous.Server = tc.server
		if got := c.PublishesToItself(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	no := false
	for name, c := range map[string]*Config{"publishing off": {Rendezvous: Rendezvous{Enabled: &no}}, "a switchyard": {Switchyard: Switchyard{Enabled: true}}} {
		c.Listen = ":8080"
		c.Rendezvous.Server = "http://localhost:8080"
		if c.PublishesToItself() {
			t.Errorf("%s publishes to itself", name)
		}
	}
}

// resolveLocalhostTo makes localhost resolve to addrs, in that order, for
// the rest of the test.
func resolveLocalhostTo(t *testing.T, addrs ...string) {
	t.Helper()
	was := localhostAddrs
	t.Cleanup(func() { localhostAddrs = was })
	localhostAddrs = func() []netip.Addr {
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out
	}
}

// localhost is what this machine's resolver makes it: where it is ::1
// alone, a listener on localhost is on ::1, and a dial of localhost reaches
// ::1 alone; where it is 127.0.0.1 alone, the same for 127.0.0.1.
func TestPublishesToItselfFollowsWhatLocalhostResolvesTo(t *testing.T) {
	for _, tc := range []struct {
		localhost      string
		listen, server string
		want           bool
	}{
		{"::1", "localhost:9000", "http://[::1]:9000", true},
		{"::1", "localhost:9000", "http://127.0.0.1:9000", false},
		{"::1", "localhost:9000", "http://localhost:9000", true},
		{"::1", "127.0.0.1:9000", "http://localhost:9000", false},
		{"::1", "[::1]:9000", "http://localhost:9000", true},
		{"127.0.0.1", "[::1]:9000", "http://localhost:9000", false},
		{"127.0.0.1", "127.0.0.1:9000", "http://localhost:9000", true},
		{"127.0.0.1", "localhost:9000", "http://[::1]:9000", false},
	} {
		resolveLocalhostTo(t, tc.localhost)
		c := Defaults()
		c.Listen = tc.listen
		c.Rendezvous.Server = tc.server
		if got := c.PublishesToItself(); got != tc.want {
			t.Errorf("localhost %s, listen %s, switchyard %s: %v, want %v", tc.localhost, tc.listen, tc.server, got, tc.want)
		}
	}
}

// dialedAddrs agrees with the standard library's dial on this machine: a
// listener on 127.0.0.1, and on ::1 where the machine has it, answers a
// dial of each host at its port exactly when dialedAddrs names its address.
// The answer counts only from that listener's own address, whatever else
// may hold the port on the other family. And a listener on localhost is
// bound to localhostListenAddr.
func TestDialedAddrsAgreesWithTheDial(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	if bound, _ := netip.ParseAddrPort(ln.Addr().String()); bound.Addr().Unmap() != localhostListenAddr() {
		t.Errorf("a listener on localhost is bound to %s, not %s (localhost is %v)", ln.Addr(), localhostListenAddr(), localhostAddrs())
	}
	ln.Close()
	hosts := []string{"localhost", "", "0.0.0.0", "::", "127.0.0.1", "::1", "::ffff:127.0.0.1"}
	for _, lhost := range []string{"127.0.0.1", "::1"} {
		l := netip.MustParseAddr(lhost)
		reached, ok := dialsReaching(t, hosts, l)
		if !ok {
			continue
		}
		for _, host := range hosts {
			if want := slices.Contains(dialedAddrs(host), l); reached[host] != want {
				t.Errorf("a dial of %q at a listener on %s: reached %v, dialedAddrs %v", host, lhost, reached[host], dialedAddrs(host))
			}
		}
	}
}

// dialsReaching listens on l at a free port and dials each of hosts there,
// reporting which reached the listener, the answer counted from its own
// address. A port taken on 127.0.0.1 is free on ::1, and the other way
// round: another program holding it there would answer a dial first, so
// such a port is given up and another tried. Not ok on a machine without
// ::1, or when five ports in a row had another program on the other side.
func dialsReaching(t *testing.T, hosts []string, l netip.Addr) (reached map[string]bool, ok bool) {
	t.Helper()
	for try := 1; try <= 5; try++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(l.String(), "0"))
		if err != nil {
			if l.Is4() {
				t.Fatal(err)
			}
			t.Logf("no listener on %s here: %v", l, err)
			return nil, false
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		reached = map[string]bool{}
		var other netip.AddrPort
		for _, host := range hosts {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
			if err != nil {
				continue
			}
			peer, _ := netip.ParseAddrPort(c.RemoteAddr().String())
			c.Close()
			if peer.Addr().Unmap() != l {
				other = peer
				break
			}
			reached[host] = true
		}
		ln.Close()
		if !other.IsValid() {
			return reached, true
		}
		t.Logf("another program answered at %s; trying another port", other)
	}
	t.Skipf("a listener on %s: five ports in a row had another program on the other loopback address", l)
	return nil, false
}

// The TLS listener counts as Load leaves it: its default :8443 filled in,
// or the environment's, with TLS on by certificate files or ACME alone.
func TestPublishesToItselfThroughTheTLSListenerAsLoaded(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"certificate files, the default port": {"CONDUCTOR_TLS_CERT_FILE": "cert.pem", "CONDUCTOR_TLS_KEY_FILE": "key.pem", "CONDUCTOR_RENDEZVOUS_SERVER": "https://localhost:8443"},
		"CONDUCTOR_TLS_LISTEN":                {"CONDUCTOR_TLS_CERT_FILE": "cert.pem", "CONDUCTOR_TLS_KEY_FILE": "key.pem", "CONDUCTOR_TLS_LISTEN": "127.0.0.1:9443", "CONDUCTOR_RENDEZVOUS_SERVER": "https://127.0.0.1:9443"},
		"ACME alone":                          {"CONDUCTOR_TLS_ACME": "1", "CONDUCTOR_RENDEZVOUS_SERVER": "https://[::1]:8443"},
	} {
		c := Defaults()
		if err := applyEnv(c, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !c.PublishesToItself() {
			t.Errorf("%s: the switchyard at %s is not this server's TLS listener %q", name, c.Rendezvous.Server, c.TLS.Listen)
		}
	}
}

// A plain switchyard on another machine is a config error unless the config
// says insecure: from the file, the environment, with publishing on or off,
// and in a switchyard's config alike. A config file that names one is
// refused at Load, and left as it was.
func TestRendezvousPlainServerOffThisMachineRefused(t *testing.T) {
	const want = "rendezvous.server yard.example:8080 is plain http and not localhost or a loopback address"
	for name, mutate := range map[string]func(*Config){
		"the file": func(c *Config) { c.Rendezvous.Server = "http://yard.example:8080/" },
		"the environment": func(c *Config) {
			if err := applyEnv(c, func(k string) string {
				return map[string]string{"CONDUCTOR_RENDEZVOUS_SERVER": "http://yard.example:8080"}[k]
			}); err != nil {
				t.Fatal(err)
			}
		},
		"publishing off": func(c *Config) {
			off := false
			c.Rendezvous = Rendezvous{Enabled: &off, Server: "http://yard.example:8080"}
		},
		"a switchyard": func(c *Config) {
			c.Switchyard.Enabled = true
			c.Rendezvous.Server = "http://yard.example:8080"
		},
		"an address": func(c *Config) { c.Rendezvous.Server = "http://192.0.2.7:8080"; c.Rendezvous.Token = "t" },
	} {
		c := Defaults()
		mutate(c)
		err := c.Validate()
		if name == "an address" {
			if err == nil || !strings.Contains(err.Error(), "rendezvous.server 192.0.2.7:8080 is plain http and not localhost or a loopback address") {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "rendezvous.insecure (CONDUCTOR_RENDEZVOUS_INSECURE=1)") {
			t.Errorf("%s: %v", name, err)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	body := []byte(`{"allowedRoots":["` + dir + `"],"rendezvous":{"server":"http://yard.example:8080"}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", "")
	t.Setenv("CONDUCTOR_RENDEZVOUS_INSECURE", "")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("the file changed: %q %v", got, err)
	}
}

// rendezvous.server is the switchyard's address alone: one with user
// information (an @ alone included), a query or a fragment (an empty one
// included) is refused, from the file and the environment, with publishing
// on or off, in words that repeat nothing of it. A path stays allowed, an @
// in it too.
func TestRendezvousServerIsItsAddressAlone(t *testing.T) {
	const want = "rendezvous.server must carry no user name, password, query or fragment: the switchyard's host token goes in rendezvous.token (CONDUCTOR_RENDEZVOUS_TOKEN)"
	for _, server := range []string{
		"https://me:SECRET@yard.example", "https://SECRET@yard.example", "https://:SECRET@yard.example", "https://@yard.example", "https://:@yard.example",
		"https://yard.example/?token=SECRET", "https://yard.example?", "https://yard.example/#SECRET", "https://yard.example#",
		"http://me:SECRET@localhost:9090", "http://me:SECRET@yard.example:8080",
	} {
		for name, set := range map[string]func(*Config){
			"the file": func(c *Config) { c.Rendezvous.Server = server },
			"the environment": func(c *Config) {
				if err := applyEnv(c, func(k string) string { return map[string]string{"CONDUCTOR_RENDEZVOUS_SERVER": server}[k] }); err != nil {
					t.Fatal(err)
				}
			},
			"publishing off": func(c *Config) {
				off := false
				c.Rendezvous = Rendezvous{Enabled: &off, Server: server}
			},
		} {
			c := Defaults()
			set(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("%s from %s: %v", server, name, err)
			}
		}
	}
	for _, server := range []string{"https://yard.example/prefix", "https://yard.example/team@two", "http://localhost:9090/yard"} {
		c := Defaults()
		c.Rendezvous.Server = server
		if err := c.Validate(); err != nil || c.RendezvousServer() != server {
			t.Errorf("%s: %v %q", server, err, c.RendezvousServer())
		}
	}
}

// The desktop app and its Settings page take a switchyard only when the
// config does, for each URL in testdata/switchyard-urls.json, which their
// tests read too (desktop/src/settings.ts switchyardRefusal,
// web/app/utils/desktop.ts switchyardProblem): the app hands its server no
// switchyard URL the server refuses. It refuses none the server takes but the
// few the list marks so ("server": "takes"), written as the browser's URL
// parser does not read them.
func TestTheAppTakesTheSwitchyardsTheConfigTakes(t *testing.T) {
	f, err := os.Open("testdata/switchyard-urls.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var urls []struct {
		URL    string `json:"url"`
		App    string `json:"app"`
		Server string `json:"server"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&urls); err != nil || len(urls) == 0 {
		t.Fatalf("%d urls: %v", len(urls), err)
	}
	for _, u := range urls {
		if u.Server != "" && (u.Server != "takes" || u.App != "shape") {
			t.Errorf("%q: the server %q of a URL the app takes as %q", u.URL, u.Server, u.App)
		}
		c := Defaults()
		c.Rendezvous.Server = u.URL
		if err := c.Validate(); (err == nil) != (u.App == "" || u.Server == "takes") {
			t.Errorf("%q, which the app takes as %q: %v", u.URL, u.App, err)
		}
	}
}

// ShownURL is a URL's scheme, host and path, nothing else of it.
func TestShownURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://yard.example": "https://yard.example",
		"https://me:SECRET@yard.example:8443/a/b?t=SECRET#SECRET": "https://yard.example:8443/a/b",
		"https://@yard.example/?":                                 "https://yard.example/",
		"http://[::1]:9000/yard#x":                                "http://[::1]:9000/yard",
		"https://yard.example/a%2Fb?t=1":                          "https://yard.example/a%2Fb",
		"https://yard.example:SECRET/c":                           "",
	} {
		if got := ShownURL(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// A plain switchyard on this machine needs nothing: localhost by name, in
// any case, and the loopback addresses.
func TestRendezvousPlainServerOnLoopbackAllowed(t *testing.T) {
	for _, server := range []string{"http://localhost:9090", "http://localhost:9090/", "http://127.0.0.1:9000", "http://127.8.9.10", "http://[::1]:8080", "http://[::ffff:127.0.0.1]:8080", "https://yard.example"} {
		c := Defaults()
		c.Rendezvous.Server = server
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", server, err)
		}
	}
}

// rendezvous.insecure, from the file or CONDUCTOR_RENDEZVOUS_INSECURE, lets
// a plain switchyard on another machine through; the environment's 0 turns
// the file's off.
func TestRendezvousInsecureAllowsIt(t *testing.T) {
	file := Defaults()
	file.Rendezvous = Rendezvous{Server: "http://yard.example:8080", Insecure: true}
	if err := file.Validate(); err != nil || file.RendezvousServer() != "http://yard.example:8080" {
		t.Fatalf("insecure in the file: %v %q", err, file.RendezvousServer())
	}
	for v, want := range map[string]bool{"1": true, "true": true, "0": false, "false": false, "": true} {
		c := Defaults()
		c.Rendezvous = Rendezvous{Server: "http://yard.example:8080", Insecure: true}
		if err := applyEnv(c, func(k string) string { return map[string]string{"CONDUCTOR_RENDEZVOUS_INSECURE": v}[k] }); err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); (err == nil) != want || c.Rendezvous.Insecure != want {
			t.Errorf("CONDUCTOR_RENDEZVOUS_INSECURE=%q over the file's true: %v, insecure %v", v, err, c.Rendezvous.Insecure)
		}
	}
	env := Defaults()
	if err := applyEnv(env, func(k string) string {
		return map[string]string{"CONDUCTOR_RENDEZVOUS_SERVER": "http://yard.example:8080", "CONDUCTOR_RENDEZVOUS_INSECURE": "1"}[k]
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.Validate(); err != nil || !env.Rendezvous.Insecure {
		t.Fatalf("CONDUCTOR_RENDEZVOUS_INSECURE=1: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(`{"allowedRoots":["`+dir+`"],"rendezvous":{"server":"http://yard.example:8080","insecure":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_RENDEZVOUS_SERVER", "")
	t.Setenv("CONDUCTOR_RENDEZVOUS_INSECURE", "")
	if cfg, err := Load(path); err != nil || !cfg.Rendezvous.Insecure {
		t.Fatalf("Load with insecure: %v", err)
	}
}

// PlainOffMachine is the rule's one predicate: plain (http or ws) to a host
// that is not this machine. A name that only starts like localhost, an
// address written in a short form or one that is not a loopback address is
// another machine.
func TestPlainOffMachine(t *testing.T) {
	for server, want := range map[string]bool{
		"http://yard.example":            true,
		"ws://yard.example/ws/host":      true,
		"HTTP://yard.example":            true,
		"http://192.168.1.10:8080":       true,
		"http://0.0.0.0:8080":            true,
		"http://127.1:8080":              true,
		"http://localhost.yard.example":  true,
		"http://127.0.0.1.yard.example":  true,
		"http://[fe80::1]:8080":          true,
		"https://yard.example":           false,
		"wss://yard.example":             false,
		"http://localhost:8080":          false,
		"ws://LocalHost:8080":            true,
		"http://127.0.0.1:8080":          false,
		"http://127.255.0.1":             false,
		"http://[::1]:8080":              false,
		"http://[::ffff:127.0.0.1]:8080": false,
		"http://user:pw@localhost:8080":  false,
		"ftp://yard.example":             false,
		"::not a url":                    false,
	} {
		if got := PlainOffMachine(server); got != want {
			t.Errorf("PlainOffMachine(%q) = %v, want %v", server, got, want)
		}
	}
}

// What the rule lets through in the clear stays on this machine: no plain
// URL it takes for this machine goes through a proxy the environment names
// (HTTP_PROXY, as the standard library reads it), and every one that would
// go through it is refused without --insecure.
func TestPlainOnThisMachineIsNeverProxied(t *testing.T) {
	proxy := (&httpproxy.Config{HTTPProxy: "http://proxy.example:3128"}).ProxyFunc()
	for _, server := range []string{
		"http://localhost:8080", "http://LOCALHOST:8080", "http://LocalHost", "http://localhost.:8080",
		"http://127.0.0.1:8080", "http://127.255.0.1", "http://127.1:8080", "http://[::1]:8080",
		"http://[::ffff:127.0.0.1]:8080", "http://[0:0:0:0:0:0:0:1]:8080", "http://[::1%25lo]:8080",
		"http://yard.example", "http://0.0.0.0", "http://user:pw@localhost:8080", "http://me@yard.example",
	} {
		u, err := url.Parse(server)
		if err != nil {
			t.Fatal(err)
		}
		via, err := proxy(u)
		if err != nil {
			t.Fatal(err)
		}
		if plain := PlainOffMachine(server); plain != (via != nil) {
			t.Errorf("%s: refused without --insecure %v, proxied %v", server, plain, via)
		}
		ws := strings.Replace(server, "http://", "ws://", 1)
		if PlainOffMachine(ws) != PlainOffMachine(server) {
			t.Errorf("%s: ws and http differ", ws)
		}
	}
}

// The agents' switches: self-service and the launch-time skill install are
// on unless the config or the environment turns them off.
func TestAgentSwitchesAndEnv(t *testing.T) {
	cfg := Defaults()
	if !cfg.SelfService() || !cfg.InstallSkill() || !cfg.MCP() {
		t.Fatal("off by default")
	}
	mcpOff := Defaults()
	if err := applyEnv(mcpOff, func(k string) string { return map[string]string{"CONDUCTOR_AGENT_MCP": "0"}[k] }); err != nil || mcpOff.MCP() {
		t.Fatalf("CONDUCTOR_AGENT_MCP=0: %v %v", err, mcpOff.MCP())
	}
	for _, tc := range []struct {
		env     map[string]string
		self    bool
		install bool
	}{
		{map[string]string{"CONDUCTOR_AGENT_SELF_SERVICE": "0"}, false, true},
		{map[string]string{"CONDUCTOR_AGENT_INSTALL_SKILL": "false"}, true, false},
		{map[string]string{"CONDUCTOR_AGENT_SELF_SERVICE": "true", "CONDUCTOR_AGENT_INSTALL_SKILL": "1"}, true, true},
		{map[string]string{"CONDUCTOR_AGENT_SELF_SERVICE": "maybe"}, true, true},
	} {
		cfg := Defaults()
		if err := applyEnv(cfg, func(k string) string { return tc.env[k] }); err != nil {
			t.Fatal(err)
		}
		if cfg.SelfService() != tc.self || cfg.InstallSkill() != tc.install {
			t.Errorf("%v: self %v install %v", tc.env, cfg.SelfService(), cfg.InstallSkill())
		}
	}
	off := false
	cfg = Defaults()
	cfg.Agents.InstallSkill = &off
	cfg.Agents.SelfService = &off
	if cfg.SelfService() || cfg.InstallSkill() {
		t.Fatal("the config does not turn them off")
	}
}

// history.sessions is on by default and off by the config or by
// CONDUCTOR_HISTORY_SESSIONS; a word the variable does not know leaves it.
func TestHistorySessionsSwitch(t *testing.T) {
	if !Defaults().HistorySessions() {
		t.Fatal("off by default")
	}
	for env, want := range map[string]bool{"0": false, "false": false, "1": true, "true": true, "maybe": true} {
		cfg := Defaults()
		if err := applyEnv(cfg, func(k string) string { return map[string]string{"CONDUCTOR_HISTORY_SESSIONS": env}[k] }); err != nil {
			t.Fatal(err)
		}
		if cfg.HistorySessions() != want {
			t.Errorf("CONDUCTOR_HISTORY_SESSIONS=%s: %v", env, cfg.HistorySessions())
		}
	}
	var cfg Config
	if err := json.Unmarshal([]byte(`{"history": {"sessions": false}}`), &cfg); err != nil || cfg.HistorySessions() {
		t.Fatalf("the config file's false: %v %v", err, cfg.HistorySessions())
	}
}

// The switchyard mode and its relay and origins come from the config or
// the environment; origins are host patterns, never URLs.
func TestSwitchyardConfig(t *testing.T) {
	cfg := Defaults()
	if cfg.Switchyard.Enabled || !cfg.SwitchyardRelay() || strings.Join(cfg.SwitchyardOrigins(), ",") != "127.0.0.1:*,localhost:*" {
		t.Fatalf("defaults: %+v %v", cfg.Switchyard, cfg.SwitchyardOrigins())
	}
	env := map[string]string{"CONDUCTOR_SWITCHYARD": "1", "CONDUCTOR_SWITCHYARD_RELAY": "0", "CONDUCTOR_SWITCHYARD_ORIGINS": "app.example:*, other.example"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if !cfg.Switchyard.Enabled || cfg.SwitchyardRelay() || strings.Join(cfg.SwitchyardOrigins(), ",") != "app.example:*,other.example" {
		t.Fatalf("env: %+v %v", cfg.Switchyard, cfg.SwitchyardOrigins())
	}
	cfg = Defaults()
	cfg.Switchyard.AllowedOrigins = []string{"http://app.example"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "switchyard.allowedOrigins") {
		t.Fatalf("a URL passed as an origin pattern: %v", err)
	}
}

// The token's old name, adminToken in the file and CONDUCTOR_ADMIN_TOKEN in
// the environment, still works and is reported as renamed; the new name wins
// when both are given.
func TestTheOldAdminTokenNameStillWorks(t *testing.T) {
	cfg := Defaults()
	cfg.AdminToken = "old"
	if err := applyEnv(cfg, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkbenchToken != "old" || !cfg.WorkbenchTokenRenamed || cfg.AdminToken != "" {
		t.Fatalf("file: %q renamed=%v old=%q", cfg.WorkbenchToken, cfg.WorkbenchTokenRenamed, cfg.AdminToken)
	}
	cfg = Defaults()
	env := map[string]string{"CONDUCTOR_ADMIN_TOKEN": "old-env"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkbenchToken != "old-env" || !cfg.WorkbenchTokenRenamed {
		t.Fatalf("env: %q renamed=%v", cfg.WorkbenchToken, cfg.WorkbenchTokenRenamed)
	}
	cfg = Defaults()
	env = map[string]string{"CONDUCTOR_ADMIN_TOKEN": "old-env", "CONDUCTOR_WORKBENCH_TOKEN": "new-env"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkbenchToken != "new-env" || cfg.WorkbenchTokenRenamed {
		t.Fatalf("both: %q renamed=%v", cfg.WorkbenchToken, cfg.WorkbenchTokenRenamed)
	}
}

// Open hosts: off by default with the limits set; the environment turns it
// on and sets each limit; the bounds and the need for switchyard.enabled.
func TestSwitchyardOpenHostsConfigAndEnv(t *testing.T) {
	cfg := Defaults()
	if cfg.Switchyard.OpenHosts || cfg.Switchyard.OpenHostSessions != 4 || cfg.Switchyard.OpenHostRegistrationsPerMinute != 6 || cfg.Switchyard.OpenHostRelayKBps != 128 {
		t.Fatalf("defaults: %+v", cfg.Switchyard)
	}
	env := map[string]string{"CONDUCTOR_SWITCHYARD": "1", "CONDUCTOR_SWITCHYARD_OPEN_HOSTS": "true", "CONDUCTOR_SWITCHYARD_OPEN_HOST_SESSIONS": "9", "CONDUCTOR_SWITCHYARD_OPEN_HOST_REGISTRATIONS": "30", "CONDUCTOR_SWITCHYARD_OPEN_HOST_RELAY_KBPS": "64"}
	if err := applyEnv(cfg, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if !cfg.Switchyard.OpenHosts || cfg.Switchyard.OpenHostSessions != 9 || cfg.Switchyard.OpenHostRegistrationsPerMinute != 30 || cfg.Switchyard.OpenHostRelayKBps != 64 {
		t.Fatalf("env: %+v", cfg.Switchyard)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no sessions":      func(c *Config) { c.Switchyard.OpenHostSessions = 0 },
		"no registrations": func(c *Config) { c.Switchyard.OpenHostRegistrationsPerMinute = 0 },
		"negative relay":   func(c *Config) { c.Switchyard.OpenHostRelayKBps = -1 },
	} {
		c := Defaults()
		c.Switchyard.Enabled = true
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What the viewers of one address send hosts is bounded by default, on any
// server; the file and the environment set the bound, 0 is none, and a
// negative or huge one is refused, naming the key.
func TestViewerRelayKBps(t *testing.T) {
	cfg := Defaults()
	if cfg.ViewerRelayKBps != 128 {
		t.Fatalf("default %d", cfg.ViewerRelayKBps)
	}
	if err := store.DecodeStrict([]byte(`{"viewerRelayKBps":0}`), cfg); err != nil || cfg.ViewerRelayKBps != 0 {
		t.Fatalf("file: %d %v", cfg.ViewerRelayKBps, err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("no bound: %v", err)
	}
	if err := applyEnv(cfg, func(k string) string { return map[string]string{"CONDUCTOR_VIEWER_RELAY_KBPS": "64"}[k] }); err != nil || cfg.ViewerRelayKBps != 64 {
		t.Fatalf("env: %d %v", cfg.ViewerRelayKBps, err)
	}
	for _, n := range []int{-1, 1<<20 + 1} {
		c := Defaults()
		c.ViewerRelayKBps = n
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "viewerRelayKBps") {
			t.Errorf("%d: %v", n, err)
		}
	}
}

// paths.browse is roots unless the file or CONDUCTOR_PATHS_BROWSE says any;
// another value is refused, naming the key.
func TestPathsBrowse(t *testing.T) {
	cfg := Defaults()
	if cfg.Paths.Browse != BrowseRoots {
		t.Fatalf("default %q", cfg.Paths.Browse)
	}
	if err := store.DecodeStrict([]byte(`{"paths":{"browse":"any"}}`), cfg); err != nil || cfg.Paths.Browse != BrowseAny {
		t.Fatalf("file: %q %v", cfg.Paths.Browse, err)
	}
	cfg = Defaults()
	if err := applyEnv(cfg, func(k string) string { return map[string]string{"CONDUCTOR_PATHS_BROWSE": "any"}[k] }); err != nil || cfg.Paths.Browse != BrowseAny {
		t.Fatalf("env: %q %v", cfg.Paths.Browse, err)
	}
	cfg.Paths.Browse = "maybe"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "paths.browse") {
		t.Fatalf("maybe: %v", err)
	}
}
