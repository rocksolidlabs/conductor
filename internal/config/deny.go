package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// FileDeny returns what no file read or write of a session may reach, even
// inside its working directory (session.Options.FileDeny): the data
// directory, whose catalog.json holds the agents' env secrets, and dirs, such
// as the directory the store writes to should it ever differ; the config file
// (Path), which holds the workbench token and host tokens; and the catalog
// file (CatalogPath), which can hold env secrets. Each of the two files comes
// with its name entries (session.DenyList), so an editor's or a backup's
// copy beside it is refused too. A directory is denied with everything in
// it, a file on its own. What the other Conductors on this machine keep is
// refused as well (machineFiles): a session must not read the desktop
// app's settings, which hold its switchyard token, nor another server's
// data directory.
func (c *Config) FileDeny(dirs ...string) []string {
	more, files := c.machineFiles()
	return c.fileDeny(append(slices.Clone(dirs), more...), files)
}

// FileDenyFunc is FileDeny(dirs...) for a server that runs a long time
// (session.Options.FileDenyFunc, the files route): the function it returns
// computes the list again at each call, so that what the other Conductors
// on the machine come to keep while the server runs (a data directory the
// desktop app's settings move to) is refused from then on, and keeps every
// entry it has named before, with the path a link on its way led to then
// (withTargets), so that what was refused once stays refused until the
// server stops. The paths of c are taken when it is called. It is safe to
// call from many goroutines, and the slices it returns are never changed.
func (c *Config) FileDenyFunc(dirs ...string) func() []string {
	own := &Config{DataDir: c.DataDir, Path: c.Path, CatalogPath: c.CatalogPath, DesktopDir: c.DesktopDir}
	dirs = slices.Clone(dirs)
	var mu sync.Mutex
	list := withTargets(nil, own.FileDeny(dirs...))
	return func() []string {
		next := own.FileDeny(dirs...)
		mu.Lock()
		defer mu.Unlock()
		list = withTargets(list, slices.Concat(list, next))
		return list
	}
}

// machineFiles are the directories and files of the other Conductors this
// machine may run: ~/.conductor, the default data directory, whichever
// this server uses; the desktop app's (desktopFiles: its own directory
// under both names, its settings, the data directory they name, and inside
// WSL the one it gives a server there); and the directory the app named
// for the server it started (DesktopDir), with its settings file, which
// for the app on Windows is in the Windows profile, under /mnt inside WSL.
// Settings of the app that cannot be read leave out only the data
// directory they would name: the directory that holds them, and holds the
// app's data directory unless they say otherwise, is refused in any case.
// A server does not stop for them, as conductor host does (LocalFileDeny):
// the app starts its server with settings it read itself.
func (c *Config) machineFiles() (dirs, files []string) {
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		dirs = append(dirs, filepath.Join(home, ".conductor"))
	}
	dirs = append(dirs, hostStateDirs()...)
	d, f, _ := desktopFiles()
	dirs = append(dirs, d...)
	files = append(files, f...)
	if c.DesktopDir != "" {
		dirs = append(dirs, c.DesktopDir)
		files = append(files, filepath.Join(c.DesktopDir, "settings.json"))
	}
	return dirs, files
}

// fileDeny is FileDeny with more files, each denied as the two are.
func (c *Config) fileDeny(dirs, files []string) []string {
	return session.DenyList(append([]string{c.DataDir}, dirs...), append([]string{c.Path, c.CatalogPath}, files...))
}

// LocalFileDeny is the deny list of a session that runs on this machine but
// not in conductor serve, as conductor host's do: what a session of the
// server that would start here refuses (FileDeny). That server reads the
// config file at path ("" for none: conductor serve reads one only when
// told), with CONDUCTOR_DATA_DIR and CONDUCTOR_CATALOG_PATH over it, and
// finds its data directory as ResolveDataDir does. The default data
// directory, ~/.conductor, is refused whichever directory that is, as are
// the copies beside the config file and the catalog file, and the
// directories and settings of the desktop app's server (desktopFiles). A
// config file that cannot be read or parsed is an error, and so are the
// desktop app's settings when they are there but cannot be read, and a data
// directory or a catalog file given as a relative path: conductor serve
// resolves it against the directory it runs in, which a session elsewhere
// cannot know, and what it names would go unrefused. Nothing is created or
// written.
func LocalFileDeny(path string) ([]string, error) {
	c := &Config{}
	dataFrom, catalogFrom := "", ""
	// read is the file the config is read from, path with its links resolved
	// first as the system resolves them (a ".." after a link leads out of the
	// link's target, not back beside the link): it is refused with the
	// copies beside it even should a link on the way to it be pointed
	// elsewhere while it is read.
	var read string
	if path != "" {
		c.Path = path
		if abs, err := filepath.Abs(path); err == nil {
			c.Path = abs
		}
		var err error
		if read, err = physicalPath(path); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		b, err := readConfigFile(read)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := store.DecodeStrict(b, c); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		dataFrom, catalogFrom = "dataDir in "+c.Path, "catalogPath in "+c.Path
	}
	if v := os.Getenv("CONDUCTOR_DATA_DIR"); v != "" {
		c.DataDir, dataFrom = v, "CONDUCTOR_DATA_DIR"
	}
	if v := os.Getenv("CONDUCTOR_CATALOG_PATH"); v != "" {
		c.CatalogPath, catalogFrom = v, "CONDUCTOR_CATALOG_PATH"
	}
	for _, f := range []struct{ from, p string }{{dataFrom, c.DataDir}, {catalogFrom, c.CatalogPath}} {
		if f.p != "" && !filepath.IsAbs(f.p) {
			return nil, fmt.Errorf("%s is %q, a path relative to the directory conductor serve runs in, which is not known here: give it as an absolute path", f.from, f.p)
		}
	}
	if c.CatalogPath != "" {
		c.CatalogPath = filepath.Clean(c.CatalogPath)
	}
	// The directory chosen, or none where ResolveDataDir finds none (no home
	// directory, an older directory it refuses): ~/.conductor stands below.
	// An older conductor.d is looked for beside the config file, as serve
	// looks for it, or without one in this process's working directory.
	if _, err := c.ResolveDataDir(c.Path); err != nil {
		c.DataDir = ""
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		dirs = append(dirs, filepath.Join(home, ".conductor"))
	}
	dirs = append(dirs, hostStateDirs()...)
	desktopDirs, desktopSettings, err := desktopFiles()
	if err != nil {
		return nil, err
	}
	return c.fileDeny(append(dirs, desktopDirs...), append([]string{read}, desktopSettings...)), nil
}

// HostStateDir is where conductor host keeps what is its own on this
// machine, its identity provider's directory among it (<state>/ext/<name>):
// $XDG_STATE_HOME/conductor when that is set and absolute, else
// ~/.local/state/conductor. Every session's file reads, saves and editors
// are refused it (machineFiles, LocalFileDeny, through hostStateDirs).
func HostStateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(state) {
		return filepath.Join(state, "conductor"), nil
	}
	return defaultHostStateDir()
}

// defaultHostStateDir is ~/.local/state/conductor.
func defaultHostStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("the home directory %q is not an absolute path", home)
	}
	return filepath.Join(home, ".local", "state", "conductor"), nil
}

// hostStateDirs is every place a deny list names for the host's state:
// HostStateDir as this process's environment gives it and the default
// under the home directory, since a host and a server on one machine may
// run with different environments. A host started with XDG_STATE_HOME set
// to a place this process's environment does not name keeps its state
// where this list does not reach: the default and this process's own
// setting are all it knows.
func hostStateDirs() []string {
	var dirs []string
	for _, f := range []func() (string, error){HostStateDir, defaultHostStateDir} {
		if d, err := f(); err == nil && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// ProviderStateMark names the file that marks a directory as a
// provider's state in host mode (<state>/ext): written before the provider
// can write there, and an export leaves every directory that holds it out,
// wherever it lies.
const ProviderStateMark = ".conductor-provider-state"

// readConfigFile is os.ReadFile: a test replaces it to move a link while
// the config file is read.
var readConfigFile = os.ReadFile

// physicalPath is the absolute path of the file p names, with every link on
// the way resolved in order, as opening p resolves them: a relative p is
// taken from the working directory with its own links resolved, never from
// a spelling of it that cleaning would shorten across a link.
func physicalPath(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(real) {
		return real, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if wd, err = filepath.EvalSymlinks(wd); err != nil {
		return "", err
	}
	return filepath.Join(wd, real), nil
}

// LocalFileDenyFunc is LocalFileDeny(path) for a session that runs a long
// time (session.Options.FileDenyFunc): the function it returns computes the
// list again at each call, so that what the config file or the desktop
// app's settings come to name while the session runs is refused from then
// on, and keeps every entry it has named before, so that what was refused
// once stays refused until the session ends. An entry is kept with the path
// a symbolic link on its way led to when it was named (withTargets), so that
// a link changed later still leaves what it named refused. The first
// computation's error is returned; while a later one lasts (a config file
// broken, or a path in it made relative), the list holds the file system's
// root as well, which refuses every file. It is safe to call from many
// goroutines, and the slices it returns are never changed.
func LocalFileDenyFunc(path string) (func() []string, error) {
	first, err := LocalFileDeny(path)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	list := withTargets(nil, first)
	everything := []string{string(filepath.Separator)}
	return func() []string {
		next, err := LocalFileDeny(path)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			return slices.Concat(list, everything)
		}
		// The entries kept are looked at again too: a link among them
		// pointed elsewhere since leaves its new target named as well.
		list = withTargets(list, slices.Concat(list, next))
		return list
	}, nil
}

// withTargets returns list with each entry of next it lacks, and after each
// entry the path it names with the symbolic links on its way resolved, when
// that differs (session.Leads: a link to nothing yet resolved to where its
// target would be): for a name entry ("*name*"), its directory resolved; for a
// file, also the name entry beside the file it leads to (session.NameEntry),
// so the copies beside whatever a kept link leads to are refused. list is
// returned as it is when nothing is added, and never changed: a new slice
// holds what is added.
func withTargets(list, next []string) []string {
	var added []string
	add := func(e string) {
		if e != "" && !slices.Contains(list, e) && !slices.Contains(added, e) {
			added = append(added, e)
		}
	}
	for _, e := range next {
		add(e)
		// Where a link on the way leads, one to nothing yet included
		// (session.Leads): pointed elsewhere later, it leaves that path
		// refused, whether or not anything is there yet.
		base := filepath.Base(e)
		if len(base) >= 3 && base[0] == '*' && base[len(base)-1] == '*' {
			if dir := session.Leads(filepath.Dir(e)); dir != "" {
				add(filepath.Join(dir, base))
			}
		} else if real := session.Leads(e); real != "" {
			add(real)
			if fi, err := os.Stat(real); err == nil && !fi.IsDir() {
				add(session.NameEntry(real))
			}
		}
	}
	if len(added) == 0 {
		return list
	}
	return slices.Concat(list, added)
}

// desktopAppNames name the desktop app's own directory, Electron's userData:
// the user's config directory (os.UserConfigDir, as Electron's appData) and
// the app's name. Electron takes that name from desktop/package.json, which
// gives only its name, conductor-desktop; Conductor, the name its packages
// carry (productName in desktop/electron-builder.yml), is the one it would
// take should package.json ever give a productName. Both are refused.
var desktopAppNames = []string{"conductor-desktop", "Conductor"}

// maxDesktopSettings bounds what is read of the desktop app's settings.
const maxDesktopSettings = 1 << 20

// desktopFiles are the directories and files of the server the desktop app
// runs on this machine, which it starts with CONDUCTOR_DATA_DIR from its own
// settings rather than with a config file (desktop/src/env.ts). The
// directories: the app's own (desktopAppNames), which holds its settings
// (desktop/src/main.ts) and, unless they say otherwise, the data directory
// (conductor in it); the data directory its settings name; and, inside WSL,
// where the app on Windows runs the server, the default it gives a server
// there, ~/.local/share/conductor/data (desktop/src/settings.ts,
// wslDefaults). The files: each settings.json, so that one which is a
// symbolic link is refused at its target too, with the copies beside it, as
// a config file is. The settings of the app on Windows are not on this side,
// so a data directory they choose for the server inside WSL is not known
// here. Each path is named whether it exists or not: one that does not is
// refused by its path (session.ResolvePath). Settings that are
// there but cannot be read for their data directory are an error
// (desktopDataDir): the app may hold another in memory. The error comes
// with the rest of what is named, all but that data directory.
func desktopFiles() (dirs, files []string, err error) {
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "conductor", "data"))
	}
	base, berr := os.UserConfigDir()
	if berr != nil || !filepath.IsAbs(base) {
		return dirs, nil, nil
	}
	for _, name := range desktopAppNames {
		app := filepath.Join(base, name)
		settings := filepath.Join(app, "settings.json")
		dirs = append(dirs, app)
		files = append(files, settings)
		d, derr := desktopDataDir(settings)
		if derr != nil {
			if err == nil {
				err = fmt.Errorf("the desktop app's settings %s, which name its server's data directory: %w", settings, derr)
			}
			continue
		}
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs, files, err
}

// desktopDataDir is the data directory the desktop app's settings file
// names: "" when there is no such file or it names none, an error when the
// file is there but cannot be read (a link to nothing included), is larger
// than maxDesktopSettings, is
// not a JSON object, or names one that is not an absolute path. Only dataDir
// is read of the settings, which hold many other keys.
func desktopDataDir(file string) (string, error) {
	f, err := os.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		if danglingLink(file) {
			return "", errors.New("a link on the way to it leads to nothing")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxDesktopSettings+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxDesktopSettings {
		return "", fmt.Errorf("larger than %d bytes", maxDesktopSettings)
	}
	var settings struct {
		DataDir string `json:"dataDir"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return "", err
	}
	if settings.DataDir == "" {
		return "", nil
	}
	if !filepath.IsAbs(settings.DataDir) {
		return "", fmt.Errorf("dataDir %q is not an absolute path", settings.DataDir)
	}
	return filepath.Clean(settings.DataDir), nil
}

// danglingLink reports whether p is missing because a link on the way to it
// leads to nothing, rather than because it, or a directory above it, is not
// there: the deepest part of p that is there is a link whose target is not.
func danglingLink(p string) bool {
	for q := filepath.Clean(p); ; q = filepath.Dir(q) {
		if fi, err := os.Lstat(q); err == nil {
			if fi.Mode()&fs.ModeSymlink == 0 {
				return false
			}
			_, err := os.Stat(q)
			return err != nil
		}
		if filepath.Dir(q) == q {
			return false
		}
	}
}
