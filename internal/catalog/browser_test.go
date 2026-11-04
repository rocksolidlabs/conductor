package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// A browser names one of the five, or none (the default); a profile is for
// the Chromium three alone, at most 100 characters, with no control
// character, quote or NUL.
func TestBrowserIsValidated(t *testing.T) {
	agent := func(b *Browser) Agent { return Agent{ID: "a", Name: "A", Command: []string{"a"}, Browser: b} }
	for _, b := range []*Browser{nil, {}, {Name: "chrome"}, {Name: "edge", Profile: "Profile 2"}, {Name: "brave", Profile: "Default"}, {Name: "firefox"}, {Name: "safari"}, {Name: "chrome", Profile: strings.Repeat("é", MaxBrowserProfile)}} {
		if err := validate(agent(b)); err != nil {
			t.Errorf("%+v: %v", b, err)
		}
	}
	for _, b := range []Browser{
		{Name: "opera"}, {Name: "Chrome"}, {Name: "chrome --incognito"},
		{Name: "firefox", Profile: "work"}, {Name: "safari", Profile: "x"}, {Profile: "Profile 1"},
		{Name: "edge", Profile: strings.Repeat("p", MaxBrowserProfile+1)},
		{Name: "edge", Profile: "a\x00b"}, {Name: "edge", Profile: "a\nb"}, {Name: "edge", Profile: `a"b`}, {Name: "edge", Profile: "a'b"}, {Name: "edge", Profile: " Profile 1"},
		{Name: "edge", Profile: "\xff"},
	} {
		if err := validate(agent(&b)); err == nil || !strings.Contains(err.Error(), "browser") {
			t.Errorf("%+v: %v", b, err)
		}
	}
}

// An override that says nothing of the browser keeps the replaced agent's;
// {} is the default over it; a clone shares nothing with the original; the
// field is in the catalog's JSON unmasked, and a config file's agent takes
// it.
func TestBrowserIsInheritedClonedAndRead(t *testing.T) {
	base, err := Load(File{DisableDefaults: true, Agents: []Agent{{ID: "work", Name: "Work", Command: []string{"claude"}, Browser: &Browser{Name: "edge", Profile: "Profile 2"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := base.Clone()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "work", Name: "Work, renamed", Command: []string{"claude"}}}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("work"); a.Browser == nil || *a.Browser != (Browser{Name: "edge", Profile: "Profile 2"}) {
		t.Fatalf("an override without a browser: %+v", a.Browser)
	}
	c = base.Clone()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "work", Name: "Work", Command: []string{"claude"}, Browser: &Browser{}}}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("work"); a.Browser.Named() {
		t.Fatalf("an override with the default: %+v", a.Browser)
	}
	a, _ := base.Get("work")
	cl := a.Copy()
	cl.Browser.Profile = "changed"
	if a.Browser.Profile != "Profile 2" {
		t.Fatal("a copy shares its browser")
	}
	b, _ := json.Marshal(a.Redacted())
	if !strings.Contains(string(b), `"browser":{"name":"edge","profile":"Profile 2"}`) {
		t.Fatalf("the agent's JSON: %s", b)
	}
	var f File
	if err := json.Unmarshal([]byte(`{"agents":[{"id":"mine","name":"Mine","command":["codex"],"browser":{"name":"firefox"}}]}`), &f); err != nil {
		t.Fatal(err)
	}
	cat, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := cat.Get("mine"); !m.Browser.Named() || m.Browser.Name != "firefox" {
		t.Fatalf("a config file's agent: %+v", m.Browser)
	}
	if _, err := Load(File{Agents: []Agent{{ID: "bad", Name: "Bad", Command: []string{"x"}, Browser: &Browser{Name: "netscape"}}}}); err == nil {
		t.Fatal("a config file's unknown browser was taken")
	}
}
