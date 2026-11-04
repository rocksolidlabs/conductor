package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The browsers an agent's links may open in (#436). The desktop app finds
// each by its name on the computer it runs on, so one setting names the same
// browser on any of the person's computers.
const (
	BrowserChrome  = "chrome"
	BrowserEdge    = "edge"
	BrowserFirefox = "firefox"
	BrowserBrave   = "brave"
	BrowserSafari  = "safari"
)

// BrowserNames is every name a Browser may have, in the order the Agents
// page offers them.
var BrowserNames = []string{BrowserChrome, BrowserEdge, BrowserFirefox, BrowserBrave, BrowserSafari}

// MaxBrowserProfile bounds a browser profile, in characters.
const MaxBrowserProfile = 100

// Browser is the browser an agent's links open in: the desktop app starts it
// with the profile, a Chromium profile directory passed as
// --profile-directory ("Default", "Profile 2"), when there is one. An empty
// Name is the computer's default browser, as no Browser at all is; a saved
// override says it ({}) to leave a browser the replaced agent names.
type Browser struct {
	Name    string `json:"name,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// Named reports whether b names a browser: not nil, and not the default.
func (b *Browser) Named() bool { return b != nil && b.Name != "" }

// clone returns a copy of b that shares nothing with it.
func (b *Browser) clone() *Browser {
	if b == nil {
		return nil
	}
	c := *b
	return &c
}

// profiled reports whether the browser takes a profile: the Chromium ones.
func profiled(name string) bool {
	return name == BrowserChrome || name == BrowserEdge || name == BrowserBrave
}

// validateBrowser checks a browser: a name of BrowserNames or none, and a
// profile only for a Chromium one, of at most MaxBrowserProfile characters,
// with no control character, quote or NUL, nor space at either end. Its
// errors carry no agent id; validate adds it.
func validateBrowser(b Browser) error {
	if b.Name != "" && !slices.Contains(BrowserNames, b.Name) {
		return fmt.Errorf("name must be one of %s, or empty for the default browser", strings.Join(BrowserNames, ", "))
	}
	if b.Profile == "" {
		return nil
	}
	if !profiled(b.Name) {
		return errors.New("a profile is only for chrome, edge and brave")
	}
	if !utf8.ValidString(b.Profile) || utf8.RuneCountInString(b.Profile) > MaxBrowserProfile {
		return fmt.Errorf("profile must be at most %d characters", MaxBrowserProfile)
	}
	if strings.ContainsFunc(b.Profile, func(r rune) bool { return unicode.IsControl(r) || r == '"' || r == '\'' || r == '`' }) {
		return errors.New("profile must have no control character, quote or NUL")
	}
	if strings.TrimSpace(b.Profile) != b.Profile {
		return errors.New("profile must not begin or end with a space")
	}
	return nil
}
