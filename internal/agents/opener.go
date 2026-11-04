package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// openerName is the file name of Conductor's opener (#436): the program an
// agent with a browser set gets as $BROWSER.
const openerName = "open-url"

// openerTempDir is the directory under os.TempDir the opener goes into when
// the hooks dir's path cannot be a $BROWSER (a space in macOS's Application
// Support): one of this user's own, 0700. Tests replace it.
var openerTempDir = func() string { return filepath.Join(os.TempDir(), "conductor-"+strconv.Itoa(geteuid())) }

// OpenerPathOK reports whether p can be the value of $BROWSER: one absolute
// path with no space, colon or percent sign, which xdg-open and Python's
// webbrowser read as separators or a placeholder, and no control character
// or quote.
func OpenerPathOK(p string) bool {
	return filepath.IsAbs(p) && !strings.ContainsFunc(p, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(":%\"'`\\", r)
	})
}

// openerScript is the opener: the URL, its one argument, goes to conductor
// open as one argument, never into a command line; nothing evaluates it.
func openerScript(bin string) string {
	return "#!/bin/sh\n# Conductor's opener ($BROWSER, #436): conductor open sends the link to the browser this agent's settings name.\nexec " + shellQuote(bin) + " open -- \"$1\"\n"
}

// Opener writes Conductor's opener for bin, the conductor binary, and returns
// its path, the value of $BROWSER for an agent whose links open in the
// browser its settings name. It goes into hooksDir when that path can be a
// $BROWSER (OpenerPathOK), else into a directory of this user's own under
// the system's temporary directory; a file that already holds it is left as
// it is. It is 0700, as the directory it is in.
func Opener(hooksDir, bin string) (string, error) {
	if err := checkBin(bin); err != nil {
		return "", err
	}
	if strings.ContainsFunc(bin, unicode.IsControl) {
		return "", fmt.Errorf("the conductor binary path %q has a control character", bin)
	}
	dir := hooksDir
	if dir == "" || !OpenerPathOK(filepath.Join(dir, openerName)) {
		dir = openerTempDir()
		if err := ownDir(dir); err != nil {
			return "", err
		}
	}
	p := filepath.Join(dir, openerName)
	if !OpenerPathOK(p) {
		return "", fmt.Errorf("no directory for the opener has a path $BROWSER can hold (no space, colon or percent sign): %s", p)
	}
	if _, err := replaceFile(p, p, []byte(openerScript(bin)), 0o700); err != nil {
		var me *modeError
		if !errors.As(err, &me) {
			return "", err
		}
	}
	return p, nil
}

// ownDir makes dir, 0700, and refuses one that is a link, not a directory,
// another user's or open to its group or others.
func ownDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if owner, ok := FileOwner(fi); ok && owner != geteuid() {
		return fmt.Errorf("%s belongs to uid %d, not to the user running conductor", dir, owner)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to its group or others (mode %04o)", dir, fi.Mode().Perm())
	}
	return nil
}

// WithOpener is env, a session's environment, with BROWSER set to opener
// when env has no BROWSER: one from the agent's own env, its
// envPassthrough or the launch is the person's opener, and wins.
func WithOpener(env []string, opener string) []string {
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == "BROWSER" {
			return env
		}
	}
	return append(env, "BROWSER="+opener)
}
