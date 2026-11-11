package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// FileDeny names the data directory and the directories given, then what
// the other Conductors on the machine keep (~/.conductor, the desktop app's
// directories and the one it names for its server), then the config file,
// the catalog file and the desktop app's settings, each with the name
// entry beside it; what is not set is left out, and so is a directory named
// twice. Settings of the app that cannot be read leave out only the data
// directory they would name.
func TestFileDeny(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	base := userConfigDir(t)
	dir := realTempDir(t)
	data, other := filepath.Join(dir, "data"), filepath.Join(dir, "store")
	cfg, cat := filepath.Join(dir, "conductor.json"), filepath.Join(dir, "agents.json")
	app := filepath.Join(dir, "mnt", "c", "Users", "me", "AppData", "Roaming", "conductor-desktop")
	machine := []string{filepath.Join(home, ".conductor"), filepath.Join(home, ".local", "share", "conductor", "data"), filepath.Join(base, "conductor-desktop"), filepath.Join(base, "Conductor")}
	var settings []string
	for _, name := range []string{"conductor-desktop", "Conductor"} {
		settings = append(settings, filepath.Join(base, name, "settings.json"), filepath.Join(base, name, "*settings.json*"))
	}
	for _, c := range []struct {
		name string
		c    Config
		dirs []string
		want []string
	}{
		{"nothing set", Config{}, nil, slices.Concat(machine, settings)},
		{"the data directory", Config{DataDir: data}, nil, slices.Concat([]string{data}, machine, settings)},
		{"the store's directory, the same", Config{DataDir: data}, []string{data}, slices.Concat([]string{data}, machine, settings)},
		{"the store's directory, another", Config{DataDir: data}, []string{other}, slices.Concat([]string{data, other}, machine, settings)},
		{"every file", Config{DataDir: data, Path: cfg, CatalogPath: cat}, []string{data}, slices.Concat([]string{data}, machine, []string{cfg, filepath.Join(dir, "*conductor.json*"), cat, filepath.Join(dir, "*agents.json*")}, settings)},
		{"no data directory", Config{Path: cfg}, []string{other}, slices.Concat([]string{other}, machine, []string{cfg, filepath.Join(dir, "*conductor.json*")}, settings)},
		{"the app's own directory", Config{DataDir: data, DesktopDir: app}, nil, slices.Concat([]string{data}, machine, []string{app}, settings, []string{filepath.Join(app, "settings.json"), filepath.Join(app, "*settings.json*")})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.FileDeny(c.dirs...); !slices.Equal(got, c.want) {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
	// The data directory the app's settings name is refused too; settings
	// that cannot be read leave it out, and nothing else.
	if err := os.MkdirAll(filepath.Join(base, "conductor-desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(dir, "app-data")
	if err := os.WriteFile(filepath.Join(base, "conductor-desktop", "settings.json"), []byte(`{"dataDir": "`+named+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (&Config{}).FileDeny(); !slices.Contains(got, named) || !slices.Contains(got, filepath.Join(base, "conductor-desktop")) {
		t.Errorf("the data directory the settings name: %q", got)
	}
	if err := os.WriteFile(filepath.Join(base, "conductor-desktop", "settings.json"), []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (&Config{}).FileDeny(); !slices.Equal(got, slices.Concat(machine, settings)) {
		t.Errorf("settings that cannot be read:\ngot %q\nwant %q", got, slices.Concat(machine, settings))
	}
}

// CONDUCTOR_DESKTOP_DIR names the desktop app's own directory for the server
// it starts; it is not a config-file key, and a relative path is refused.
func TestDesktopDirComesFromTheEnvironmentOnly(t *testing.T) {
	app := filepath.Join(realTempDir(t), "conductor-desktop")
	t.Setenv("CONDUCTOR_DESKTOP_DIR", app+string(filepath.Separator))
	if cfg, err := Load(""); err != nil || cfg.DesktopDir != app {
		t.Fatalf("CONDUCTOR_DESKTOP_DIR: %q %v", cfg.DesktopDir, err)
	}
	t.Setenv("CONDUCTOR_DESKTOP_DIR", "relative/conductor-desktop")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "CONDUCTOR_DESKTOP_DIR") {
		t.Fatalf("a relative path: %v", err)
	}
	t.Setenv("CONDUCTOR_DESKTOP_DIR", "")
	path := filepath.Join(realTempDir(t), "c.json")
	if err := os.WriteFile(path, []byte(`{"desktopDir": "/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "desktopDir") {
		t.Fatalf("a config file with desktopDir should be rejected as an unknown field: %v", err)
	}
	// The app sets the variable the server reads, from its own directory.
	b, err := os.ReadFile(filepath.Join("..", "..", "desktop", "src", "env.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "out.CONDUCTOR_DESKTOP_DIR = appDir") {
		t.Fatal("desktop/src/env.ts no longer names the app's directory as CONDUCTOR_DESKTOP_DIR")
	}
}

// The desktop app's own directory is named after the name Electron gives it,
// from desktop/package.json (its productName, else its name), and the name
// its packages carry (electron-builder.yml): desktopAppNames holds both.
func TestDesktopAppNamesFollowTheApp(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "desktop", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Name        string `json:"name"`
		ProductName string `json:"productName"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	electron := pkg.ProductName
	if electron == "" {
		electron = pkg.Name
	}
	yml, err := os.ReadFile(filepath.Join("..", "..", "desktop", "electron-builder.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^productName:\s*(\S+)\s*$`).FindSubmatch(yml)
	if m == nil {
		t.Fatal("no productName in electron-builder.yml")
	}
	for _, name := range []string{electron, string(m[1])} {
		if !slices.Contains(desktopAppNames, name) {
			t.Errorf("the desktop app's directory may be named %q, which desktopAppNames %q lacks", name, desktopAppNames)
		}
	}
}

// realTempDir is a temporary directory of the test's with its symbolic
// links resolved: on macOS it is under /var, a link to /private/var, and a
// deny list built from the unresolved path would hold the resolved one
// beside it (session.Leads), where Linux holds one entry per path.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// userConfigDir is where the desktop app's directories are looked for, as
// the product looks (os.UserConfigDir): ~/.config, or XDG_CONFIG_HOME, on
// Linux; ~/Library/Application Support on macOS, where XDG_CONFIG_HOME is
// not read. Asked after HOME and XDG_CONFIG_HOME are set.
func userConfigDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// LocalFileDeny finds the files of the conductor serve that would start on
// this machine as serve does: the config file named, if any, the
// environment over it, the data directory as ResolveDataDir finds it, and
// ~/.conductor whichever that is; and the directories of the desktop app's
// server. It writes nothing, and a config file it cannot read or parse, or
// a relative path it cannot place, is an error.
func TestLocalFileDeny(t *testing.T) {
	// setup gives the test a home and a working directory of its own, the
	// config directory in the home, and no CONDUCTOR_DATA_DIR or
	// CONDUCTOR_CATALOG_PATH; it returns both.
	setup := func(t *testing.T) (home, cwd string) {
		t.Helper()
		home, cwd = realTempDir(t), realTempDir(t)
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("CONDUCTOR_DATA_DIR", "")
		t.Setenv("CONDUCTOR_CATALOG_PATH", "")
		t.Chdir(cwd)
		return home, cwd
	}
	// local is what is refused on every machine with a home: ~/.conductor,
	// the desktop app's data directory inside WSL and its own directory,
	// under either name, in the user's configuration directory.
	local := func(t *testing.T, home string) []string {
		config := userConfigDir(t)
		return []string{filepath.Join(home, ".conductor"), filepath.Join(home, ".local", "share", "conductor", "data"), filepath.Join(config, "conductor-desktop"), filepath.Join(config, "Conductor")}
	}
	// settings are the desktop app's settings files in config, each with
	// the name entry beside it.
	settings := func(config string) []string {
		var out []string
		for _, name := range []string{"conductor-desktop", "Conductor"} {
			out = append(out, filepath.Join(config, name, "settings.json"), filepath.Join(config, name, "*settings.json*"))
		}
		return out
	}
	// expect is the list for the server's data directories dirs and its
	// files' entries, with what every machine refuses.
	expect := func(t *testing.T, home string, dirs []string, files ...string) []string {
		return slices.Concat(dirs, local(t, home), files, settings(userConfigDir(t)))
	}
	writeFile := func(t *testing.T, path, body string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	deny := func(t *testing.T, path string) []string {
		t.Helper()
		got, err := LocalFileDeny(path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("no config file: ~/.conductor", func(t *testing.T) {
		home, _ := setup(t)
		if got, want := deny(t, ""), expect(t, home, nil); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
		for _, d := range local(t, home) {
			if _, err := os.Lstat(d); err == nil {
				t.Errorf("%s was made", d)
			}
		}
	})

	t.Run("the config file's dataDir and catalogPath, and the copies beside the two files", func(t *testing.T) {
		home, _ := setup(t)
		etc := realTempDir(t)
		data := filepath.Join(realTempDir(t), "data")
		catalogFile := filepath.Join(etc, "agents", "..", "agents.json")
		cfg := writeFile(t, filepath.Join(etc, "conductor.json"), `{"workbenchToken":"w","dataDir":"`+data+`","catalogPath":"`+catalogFile+`"}`)
		want := expect(t, home, []string{data}, cfg, filepath.Join(etc, "*conductor.json*"), filepath.Join(etc, "agents.json"), filepath.Join(etc, "*agents.json*"))
		if got := deny(t, cfg); !slices.Equal(got, want) {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})

	t.Run("a relative path to the config file", func(t *testing.T) {
		home, cwd := setup(t)
		writeFile(t, filepath.Join(cwd, "conf", "conductor.json"), `{}`)
		want := expect(t, home, nil, filepath.Join(cwd, "conf", "conductor.json"), filepath.Join(cwd, "conf", "*conductor.json*"))
		if got := deny(t, filepath.Join("conf", "conductor.json")); !slices.Equal(got, want) {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})

	// A path to the config file with ".." after a link is opened as the
	// system opens it, out of the link's target: the config read there is
	// the one serve reads, and that file is refused.
	t.Run("a config path with .. after a link", func(t *testing.T) {
		setup(t)
		base := realTempDir(t)
		v1 := filepath.Join(base, "releases", "v1")
		if err := os.MkdirAll(v1, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(v1, filepath.Join(base, "current")); err != nil {
			t.Fatal(err)
		}
		served, beside := filepath.Join(base, "served-data"), filepath.Join(base, "beside-data")
		real := writeFile(t, filepath.Join(base, "releases", "conductor.json"), `{"dataDir":"`+served+`"}`)
		writeFile(t, filepath.Join(base, "conductor.json"), `{"dataDir":"`+beside+`"}`)
		for _, p := range []string{filepath.Join(base, "current") + "/../conductor.json", "current/../conductor.json"} {
			if !filepath.IsAbs(p) {
				t.Chdir(base)
			}
			got := deny(t, p)
			if !slices.Contains(got, served) || slices.Contains(got, beside) || !slices.Contains(got, real) || !slices.Contains(got, filepath.Join(base, "releases", "*conductor.json*")) {
				t.Errorf("%s: %q; want %s and the file read, %s, not %s", p, got, served, real, beside)
			}
		}
	})

	// A relative dataDir or catalogPath is resolved by conductor serve
	// against the directory it runs in, which need not be this one: the
	// server below runs in its own directory, the session in a home that
	// holds a directory of the same name. Neither is guessed at.
	t.Run("a relative dataDir or catalogPath is refused", func(t *testing.T) {
		for _, c := range []struct{ name, body, env, value, want string }{
			{"dataDir in the file", `{"dataDir":"state"}`, "", "", "dataDir in "},
			{"catalogPath in the file", `{"catalogPath":"agents.json"}`, "", "", "catalogPath in "},
			{"dataDir in the file, a dot path", `{"dataDir":"./state"}`, "", "", "dataDir in "},
			{"CONDUCTOR_DATA_DIR", `{}`, "CONDUCTOR_DATA_DIR", "state", "CONDUCTOR_DATA_DIR"},
			{"CONDUCTOR_CATALOG_PATH", `{}`, "CONDUCTOR_CATALOG_PATH", "agents.json", "CONDUCTOR_CATALOG_PATH"},
		} {
			t.Run(c.name, func(t *testing.T) {
				setup(t)
				service := realTempDir(t)
				cfg := writeFile(t, filepath.Join(service, "conductor.json"), c.body)
				if c.env != "" {
					t.Setenv(c.env, c.value)
				}
				got, err := LocalFileDeny(cfg)
				if err == nil || got != nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "absolute") {
					t.Fatalf("%q, %v; want an error naming %q", got, err, c.want)
				}
				if c.env != "" {
					// Without a config file too.
					if got, err := LocalFileDeny(""); err == nil || got != nil || !strings.Contains(err.Error(), c.env) {
						t.Fatalf("without a config file: %q, %v", got, err)
					}
				}
			})
		}
	})

	t.Run("an older conductor.d beside the config file, and ~/.conductor", func(t *testing.T) {
		home, _ := setup(t)
		service := realTempDir(t)
		cfg := writeFile(t, filepath.Join(service, "conductor.json"), `{}`)
		old := filepath.Join(service, "conductor.d")
		if err := os.Mkdir(old, 0o700); err != nil {
			t.Fatal(err)
		}
		want := expect(t, home, []string{old}, cfg, filepath.Join(service, "*conductor.json*"))
		if got := deny(t, cfg); !slices.Equal(got, want) {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})

	t.Run("CONDUCTOR_DATA_DIR and CONDUCTOR_CATALOG_PATH over the file", func(t *testing.T) {
		home, _ := setup(t)
		dir := realTempDir(t)
		cfg := writeFile(t, filepath.Join(dir, "conductor.json"), `{"dataDir":"/file/data","catalogPath":"/file/agents.json"}`)
		envData, envCat := filepath.Join(dir, "env-data"), filepath.Join(dir, "env-agents.json")
		t.Setenv("CONDUCTOR_DATA_DIR", envData)
		t.Setenv("CONDUCTOR_CATALOG_PATH", envCat)
		want := expect(t, home, []string{envData}, cfg, filepath.Join(dir, "*conductor.json*"), envCat, filepath.Join(dir, "*env-agents.json*"))
		if got := deny(t, cfg); !slices.Equal(got, want) {
			t.Errorf("got %q\nwant %q", got, want)
		}
		// Without a config file too.
		want = expect(t, home, []string{envData}, envCat, filepath.Join(dir, "*env-agents.json*"))
		if got := deny(t, ""); !slices.Equal(got, want) {
			t.Errorf("without a config file: %q", got)
		}
	})

	t.Run("a config file that is a link: the copies beside its target too", func(t *testing.T) {
		home, _ := setup(t)
		target := writeFile(t, filepath.Join(realTempDir(t), "prod.json"), `{}`)
		dir := realTempDir(t)
		link := filepath.Join(dir, "conductor.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		want := expect(t, home, nil, link, filepath.Join(dir, "*conductor.json*"), filepath.Join(filepath.Dir(target), "*prod.json*"), target)
		if got := deny(t, link); !slices.Equal(got, want) {
			t.Errorf("got %q\nwant %q", got, want)
		}
		// The link pointed elsewhere while the file is read: the file read
		// stays named, with the copies beside it.
		other := writeFile(t, filepath.Join(realTempDir(t), "other.json"), `{}`)
		readConfigFile = func(name string) ([]byte, error) {
			b, err := os.ReadFile(name)
			os.Remove(link)
			if lerr := os.Symlink(other, link); lerr != nil {
				t.Error(lerr)
			}
			return b, err
		}
		t.Cleanup(func() { readConfigFile = os.ReadFile })
		got := deny(t, link)
		for _, e := range []string{target, filepath.Join(filepath.Dir(target), "*prod.json*")} {
			if !slices.Contains(got, e) {
				t.Errorf("the link moved during the read: %q lacks %s", got, e)
			}
		}
	})

	t.Run("an older ./conductor.d the server would keep, and ~/.conductor", func(t *testing.T) {
		home, cwd := setup(t)
		old := filepath.Join(cwd, "conductor.d")
		if err := os.Mkdir(old, 0o700); err != nil {
			t.Fatal(err)
		}
		if got, want := deny(t, ""), expect(t, home, []string{old}); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// The desktop app starts its server with CONDUCTOR_DATA_DIR from its
	// settings, which a shell elsewhere does not have: its own directory is
	// refused, and the data directory its settings name.
	t.Run("the desktop app's directories", func(t *testing.T) {
		home, _ := setup(t)
		t.Setenv("XDG_CONFIG_HOME", realTempDir(t))
		config := userConfigDir(t)
		app, renamed := filepath.Join(config, "conductor-desktop"), filepath.Join(config, "Conductor")
		dirs := []string{filepath.Join(home, ".conductor"), filepath.Join(home, ".local", "share", "conductor", "data"), app, renamed}
		base := slices.Concat(dirs, settings(config))
		if got := deny(t, ""); !slices.Equal(got, base) {
			t.Errorf("no settings: %q, want %q", got, base)
		}
		data := filepath.Join(realTempDir(t), "elsewhere")
		writeFile(t, filepath.Join(app, "settings.json"), `{"dataDir":"`+data+`/","allowedRoots":["`+home+`"],"switchyardToken":"t","zoomLevel":0}`)
		want := slices.Concat([]string{dirs[0], dirs[1], app, data, renamed}, settings(config))
		if got := deny(t, ""); !slices.Equal(got, want) {
			t.Errorf("settings naming a data directory: %q, want %q", got, want)
		}
		// The settings under the other name count too.
		other := filepath.Join(realTempDir(t), "other")
		writeFile(t, filepath.Join(renamed, "settings.json"), `{"dataDir":"`+other+`"}`)
		if got, want := deny(t, ""), slices.Concat([]string{dirs[0], dirs[1], app, data, renamed, other}, settings(config)); !slices.Equal(got, want) {
			t.Errorf("settings under both names: %q", got)
		}
		os.Remove(filepath.Join(renamed, "settings.json"))
		// Settings without a data directory name the default, inside the
		// app's directory.
		for _, body := range []string{`{}`, `{"dataDir":""}`, `{"zoomLevel":1}`} {
			writeFile(t, filepath.Join(app, "settings.json"), body)
			if got := deny(t, ""); !slices.Equal(got, base) {
				t.Errorf("settings %q: %q, want %q", body, got, base)
			}
		}
		// Settings that are there but cannot be read for their data
		// directory are an error: the app may hold another in memory.
		for _, body := range []string{`{oops`, `{"dataDir":"relative/data"}`, `{"dataDir":7}`, `[]`, ``, `{"dataDir":"/x","pad":"` + strings.Repeat("p", 1<<20) + `"}`} {
			writeFile(t, filepath.Join(app, "settings.json"), body)
			if got, err := LocalFileDeny(""); err == nil || got != nil || !strings.Contains(err.Error(), filepath.Join(app, "settings.json")) {
				t.Errorf("settings %.40q: %q, %v; want an error naming the file", body, got, err)
			}
		}
		os.Remove(filepath.Join(app, "settings.json"))
		if err := os.Mkdir(filepath.Join(app, "settings.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := LocalFileDeny(""); err == nil {
			t.Error("settings that are a directory: no error")
		}
		os.Remove(filepath.Join(app, "settings.json"))
		if err := os.Symlink(filepath.Join(realTempDir(t), "gone.json"), filepath.Join(app, "settings.json")); err != nil {
			t.Fatal(err)
		}
		if got, err := LocalFileDeny(""); err == nil || got != nil {
			t.Errorf("settings that are a link to nothing: %q, %v; want an error", got, err)
		}
		os.Remove(filepath.Join(app, "settings.json"))
		// The app's directory a link to nothing: the same. Moved aside and
		// put back after.
		aside := filepath.Join(realTempDir(t), "aside")
		if err := os.Rename(app, aside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(realTempDir(t), "gone"), app); err != nil {
			t.Fatal(err)
		}
		if got, err := LocalFileDeny(""); err == nil || got != nil {
			t.Errorf("the app's directory a link to nothing: %q, %v; want an error", got, err)
		}
		os.Remove(app)
		if err := os.Rename(aside, app); err != nil {
			t.Fatal(err)
		}
		// Settings that are a link to a file elsewhere: that file and the
		// copies beside it are refused, as a config file's are.
		dotfiles := filepath.Join(home, "dotfiles")
		target := writeFile(t, filepath.Join(dotfiles, "desktop.json"), `{"switchyardToken":"t"}`)
		writeFile(t, filepath.Join(dotfiles, "desktop.json.bak"), `{"switchyardToken":"t"}`)
		writeFile(t, filepath.Join(dotfiles, "notes.md"), "ordinary\n")
		os.Remove(filepath.Join(app, "settings.json"))
		if err := os.Symlink(target, filepath.Join(app, "settings.json")); err != nil {
			t.Fatal(err)
		}
		got := deny(t, "")
		if !slices.Contains(got, filepath.Join(dotfiles, "*desktop.json*")) {
			t.Errorf("linked settings: %q, want the copies beside the target among them", got)
		}
		for _, p := range []string{"dotfiles/desktop.json", "dotfiles/desktop.json.bak", "dotfiles/.desktop.json.swp"} {
			if r, err := session.ResolvePath(home, p, got); err == nil {
				t.Errorf("%s: resolved to %s, want it refused", p, r)
			}
		}
		if _, err := session.ResolvePath(home, "dotfiles/notes.md", got); err != nil {
			t.Errorf("dotfiles/notes.md: %v", err)
		}
	})

	t.Run("no home directory", func(t *testing.T) {
		setup(t)
		t.Setenv("HOME", "")
		if got := deny(t, ""); len(got) != 0 {
			t.Errorf("got %q, want nothing", got)
		}
		data := realTempDir(t)
		t.Setenv("CONDUCTOR_DATA_DIR", data)
		if got := deny(t, ""); !slices.Equal(got, []string{data}) {
			t.Errorf("with CONDUCTOR_DATA_DIR: %q", got)
		}
	})

	t.Run("a config file that cannot be read or parsed", func(t *testing.T) {
		setup(t)
		dir := realTempDir(t)
		for _, c := range []struct{ name, path, want string }{
			{"missing", filepath.Join(dir, "missing.json"), "read config"},
			{"a directory", dir, "read config"},
			{"not JSON", writeFile(t, filepath.Join(dir, "bad.json"), `{oops`), "parse config"},
			{"an unknown key", writeFile(t, filepath.Join(dir, "unknown.json"), `{"dataDirectory":"/x"}`), "parse config"},
			{"trailing data", writeFile(t, filepath.Join(dir, "trailing.json"), `{} {}`), "parse config"},
		} {
			if got, err := LocalFileDeny(c.path); err == nil || !strings.Contains(err.Error(), c.want) || got != nil {
				t.Errorf("%s: %q, %v; want an error about %q", c.name, got, err, c.want)
			}
		}
	})
}

// A server's FileDenyFunc follows what the desktop app's settings name while
// the server runs, and keeps what it named before: a data directory the app
// moves to is refused from then on, and the one it left stays refused.
// Settings that cannot be read meanwhile take nothing away.
func TestFileDenyFuncFollowsTheDesktopApp(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(realTempDir(t), "data")
	deny := (&Config{DataDir: data}).FileDenyFunc()
	first := deny()
	if !slices.Contains(first, data) || !slices.Contains(first, filepath.Join(home, ".conductor")) {
		t.Fatalf("first: %q", first)
	}
	settings := filepath.Join(base, "conductor-desktop", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(home, "work", "app-data-a"), filepath.Join(home, "work", "app-data-b")
	for _, d := range []string{a, b} {
		if slices.Contains(first, d) {
			t.Fatalf("%s named before the settings did: %q", d, first)
		}
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"dataDir": "` + a + `"}`)
	if got := deny(); !slices.Contains(got, a) {
		t.Fatalf("after the app named %s: %q", a, got)
	}
	write(`{"dataDir": "` + b + `"}`)
	if got := deny(); !slices.Contains(got, a) || !slices.Contains(got, b) {
		t.Fatalf("after the app moved to %s: %q", b, got)
	}
	write(`{oops`)
	if got := deny(); !slices.Contains(got, a) || !slices.Contains(got, b) || !slices.Contains(got, data) {
		t.Fatalf("settings that cannot be read: %q", got)
	}
}

// A deny entry that is a link to nothing yet is kept with the path its
// target would be: pointed elsewhere later, the path it led to stays
// refused, so what a revision keeps there is not read.
func TestFileDenyFuncKeepsWhereALinkToNothingLed(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	old := filepath.Join(home, "repo", "old-data")
	if err := os.MkdirAll(filepath.Join(home, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".conductor")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	deny := (&Config{DataDir: filepath.Join(realTempDir(t), "data")}).FileDenyFunc()
	if got := deny(); !slices.Contains(got, old) {
		t.Fatalf("the path the link leads to: %q", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	got := deny()
	if !slices.Contains(got, old) {
		t.Fatalf("after the link moved: %q", got)
	}
	if r, err := session.ResolvePath(home, "repo/old-data/catalog.json", got); err == nil {
		t.Fatalf("a path the link led to resolved to %s", r)
	}
}

// LocalFileDenyFunc follows what the config file and the desktop app's
// settings name while a session runs, and keeps what it named before: a
// data directory moved is refused at both places. A config file that cannot
// be used at the start is an error; later, it leaves the list as it was.
func TestLocalFileDenyFunc(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", realTempDir(t))
	config := userConfigDir(t)
	t.Setenv("CONDUCTOR_DATA_DIR", "")
	t.Setenv("CONDUCTOR_CATALOG_PATH", "")
	t.Chdir(realTempDir(t))
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LocalFileDenyFunc(filepath.Join(home, "missing.json")); err == nil {
		t.Error("a config file that is not there: no error")
	}
	cfg := filepath.Join(home, "conductor.json")
	first, second, third := filepath.Join(home, "first"), filepath.Join(home, "second"), filepath.Join(home, "third")
	write(cfg, `{"dataDir":"`+first+`"}`)
	deny, err := LocalFileDenyFunc(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := deny()
	if !slices.Contains(start, first) || slices.Contains(start, second) {
		t.Fatalf("at the start: %q", start)
	}
	settings := filepath.Join(config, "conductor-desktop", "settings.json")
	write(settings, `{"dataDir":"`+second+`"}`)
	write(cfg, `{"dataDir":"`+third+`"}`)
	got := deny()
	for _, d := range []string{first, second, third} {
		if !slices.Contains(got, d) {
			t.Errorf("after the moves: %q lacks %s", got, d)
		}
	}
	if !slices.Equal(start, deny()[:len(start)]) {
		t.Errorf("what was named first is not kept as it was: %q, then %q", start, deny())
	}
	// A config file broken later, or a path in it made relative: every file
	// is refused (the root joins the list) until it can be read again, and
	// what was named stays named.
	root := string(filepath.Separator)
	for _, body := range []string{`{oops`, `{"dataDir":"relative"}`, `{"catalogPath":"agents.json"}`} {
		write(cfg, body)
		again := deny()
		if !slices.Contains(again, root) || !slices.Equal(again[:len(got)], got) {
			t.Errorf("config %s later: %q, want the list as it was and %s", body, again, root)
		}
		if _, err := session.ResolvePath(home, "notes.txt", again); err == nil {
			t.Errorf("config %s later: a file is served", body)
		}
	}
	write(cfg, `{"dataDir":"`+third+`"}`)
	if again := deny(); slices.Contains(again, root) || !slices.Equal(again, got) {
		t.Errorf("the config file fixed: %q, want %q", again, got)
	}
	// The desktop app's settings broken later: the same.
	write(settings, `{oops`)
	if again := deny(); !slices.Contains(again, root) {
		t.Errorf("desktop settings broken later: %q, want %s among them", again, root)
	}
	write(settings, `{"dataDir":"`+second+`"}`)
	if again := deny(); slices.Contains(again, root) {
		t.Errorf("desktop settings fixed: %q", again)
	}
	// A data directory given as a link that is pointed elsewhere later: the
	// directory it led to before stays refused, beside the new one.
	oldData, newData := filepath.Join(home, "data-old"), filepath.Join(home, "data-new")
	write(filepath.Join(oldData, "catalog.json"), "secret\n")
	write(filepath.Join(newData, "catalog.json"), "secret\n")
	link := filepath.Join(realTempDir(t), "data")
	if err := os.Symlink(oldData, link); err != nil {
		t.Fatal(err)
	}
	write(cfg, `{"dataDir":"`+link+`"}`)
	deny()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newData, link); err != nil {
		t.Fatal(err)
	}
	after := deny()
	for _, p := range []string{"data-old/catalog.json", "data-new/catalog.json"} {
		if r, err := session.ResolvePath(home, p, after); err == nil {
			t.Errorf("after the link moved, %s resolved to %s, want it refused", p, r)
		}
	}
	// The config file names another directory; the link, kept, is pointed
	// elsewhere twice: each directory it led to stays refused.
	write(cfg, `{"dataDir":"`+third+`"}`)
	deny()
	for _, next := range []string{"data-b", "data-c"} {
		write(filepath.Join(home, next, "catalog.json"), "secret\n")
		os.Remove(link)
		if err := os.Symlink(filepath.Join(home, next), link); err != nil {
			t.Fatal(err)
		}
		deny()
	}
	after = deny()
	for _, p := range []string{"data-old/catalog.json", "data-new/catalog.json", "data-b/catalog.json", "data-c/catalog.json"} {
		if r, err := session.ResolvePath(home, p, after); err == nil {
			t.Errorf("after the kept link moved twice, %s resolved to %s, want it refused", p, r)
		}
	}
	// A catalog file given as a link, then another catalog file named: the
	// link, kept and pointed at a third file, leaves that file and the
	// copies beside it refused.
	catalogs := filepath.Join(home, "catalogs")
	for _, name := range []string{"one.json", "two.json", "two.json.bak", "other.json"} {
		write(filepath.Join(catalogs, name), "{}")
	}
	catLink := filepath.Join(realTempDir(t), "agents.json")
	if err := os.Symlink(filepath.Join(catalogs, "one.json"), catLink); err != nil {
		t.Fatal(err)
	}
	write(cfg, `{"dataDir":"`+third+`","catalogPath":"`+catLink+`"}`)
	deny()
	write(cfg, `{"dataDir":"`+third+`","catalogPath":"`+filepath.Join(catalogs, "other.json")+`"}`)
	deny()
	os.Remove(catLink)
	if err := os.Symlink(filepath.Join(catalogs, "two.json"), catLink); err != nil {
		t.Fatal(err)
	}
	after = deny()
	for _, p := range []string{"catalogs/one.json", "catalogs/two.json", "catalogs/two.json.bak", "catalogs/.two.json.swp"} {
		if r, err := session.ResolvePath(home, p, after); err == nil {
			t.Errorf("after the kept catalog link moved, %s resolved to %s, want it refused", p, r)
		}
	}
	// Many requests at once.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if l := deny(); !slices.Contains(l, first) {
					t.Errorf("concurrent: %q", l)
				}
			}
		}()
	}
	wg.Wait()
}

// The list a hosted session of this machine is given (LocalFileDenyFunc for
// the server's config) refuses each folder the server's accounts document
// registers, as the server's own sessions do: reached by its path, by a link
// to it, and one registered through a link; kept once named, so a folder
// whose account is removed stays refused. A document that cannot be read is
// an error at the start and, later, refuses every file until it can be.
func TestLocalFileDenyRefusesAccountFolders(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", realTempDir(t))
	data := filepath.Join(realTempDir(t), "data")
	t.Setenv("CONDUCTOR_DATA_DIR", data)
	t.Setenv("CONDUCTOR_CATALOG_PATH", "")
	t.Chdir(realTempDir(t))
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	work, other, registered := filepath.Join(home, "logins", "work"), filepath.Join(home, "logins", "other"), filepath.Join(home, "links", "other")
	write(filepath.Join(work, "auth.json"), "login\n")
	write(filepath.Join(other, "auth.json"), "login\n")
	write(filepath.Join(home, "notes.md"), "# beside\n")
	for link, to := range map[string]string{registered: other, filepath.Join(home, "alias"): work} {
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	doc := filepath.Join(data, "capacity", "accounts.json")
	account := func(id, folder string) string {
		return `{"id":"` + id + `","adapter":"codex","label":"L","folder":"` + folder + `","createdAt":"2026-11-11T00:00:00Z"}`
	}
	write(doc, `{"rev":2,"accounts":[`+account("a-aaaaaaaaaaaa", work)+`,`+account("a-bbbbbbbbbbbb", registered)+`]}`)
	deny, err := LocalFileDenyFunc("")
	if err != nil {
		t.Fatal(err)
	}
	refused := func(when string) {
		t.Helper()
		list := deny()
		for _, p := range []string{"logins/work/auth.json", "alias/auth.json", "links/other/auth.json", "logins/other/auth.json"} {
			if r, err := session.ResolvePath(home, p, list); err == nil {
				t.Errorf("%s: %s resolved to %s, want it refused", when, p, r)
			}
		}
	}
	refused("registered")
	write(doc, `{"rev":3,"accounts":[]}`)
	refused("removed")
	if _, err := session.ResolvePath(home, "notes.md", deny()); err != nil {
		t.Errorf("the file beside: %v", err)
	}
	root := string(filepath.Separator)
	write(doc, `not json`)
	if list := deny(); !slices.Contains(list, root) {
		t.Errorf("the accounts made unreadable later: %q, want %s among them", list, root)
	}
	if _, err := LocalFileDenyFunc(""); err == nil {
		t.Error("the accounts unreadable at the start: no error")
	}
	write(doc, `{"rev":3,"accounts":[]}`)
	if list := deny(); slices.Contains(list, root) {
		t.Errorf("the accounts readable again: %q", list)
	}
}

// The accounts of every data directory the list names are read, the other
// Conductors' of this machine too: conductor host's list, on a machine whose
// desktop app's settings name another data directory with an account
// registered there, refuses that account's folder, as the server's own list
// does; that document made unreadable, the list holds the root.
func TestEveryDataDirectoryNamedHasItsAccountsRead(t *testing.T) {
	home, base := realTempDir(t), realTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("CONDUCTOR_DATA_DIR", filepath.Join(realTempDir(t), "data"))
	t.Setenv("CONDUCTOR_CATALOG_PATH", "")
	t.Chdir(realTempDir(t))
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	desktop, folder := filepath.Join(realTempDir(t), "desktop-data"), filepath.Join(home, "logins", "work")
	write(filepath.Join(base, "conductor-desktop", "settings.json"), `{"dataDir": "`+desktop+`"}`)
	write(filepath.Join(folder, "auth.json"), "login\n")
	write(filepath.Join(home, "notes.md"), "# beside\n")
	doc := filepath.Join(desktop, "capacity", "accounts.json")
	write(doc, `{"rev":1,"accounts":[{"id":"a-aaaaaaaaaaaa","adapter":"codex","label":"L","folder":"`+folder+`","createdAt":"2026-11-11T00:00:00Z"}]}`)
	deny, err := LocalFileDenyFunc("")
	if err != nil {
		t.Fatal(err)
	}
	server := (&Config{DataDir: filepath.Join(realTempDir(t), "own")}).FileDenyFunc()
	for who, list := range map[string][]string{"conductor host's": deny(), "the server's": server()} {
		if r, err := session.ResolvePath(home, "logins/work/auth.json", list); err == nil {
			t.Errorf("%s list: the desktop server's account's folder resolved to %s", who, r)
		}
		if _, err := session.ResolvePath(home, "notes.md", list); err != nil {
			t.Errorf("%s list: the file beside: %v", who, err)
		}
	}
	write(doc, `not json`)
	root := string(filepath.Separator)
	for who, list := range map[string][]string{"conductor host's": deny(), "the server's": server()} {
		if !slices.Contains(list, root) {
			t.Errorf("%s list, the desktop server's accounts unreadable: %q, want %s among them", who, list, root)
		}
	}
}

// Refuses answers whether the deny list given a folder refuses it: a folder,
// a link to one, a folder gone and one whose parent is gone are refused; a
// link the list does not refuse as it is spelt is not.
func TestRefuses(t *testing.T) {
	dir := realTempDir(t)
	work := filepath.Join(dir, "logins", "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{filepath.Join(dir, "alias"): work, filepath.Join(dir, "*work*"): work} {
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{work, filepath.Join(dir, "alias"), filepath.Join(dir, "gone"), filepath.Join(dir, "gone", "work")} {
		if !Refuses(f) {
			t.Errorf("%s: not refused", f)
		}
	}
	if f := filepath.Join(dir, "*work*"); Refuses(f) {
		t.Errorf("%s: refused", f)
	}
}
