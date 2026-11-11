package proto

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/rocksolidlabs/conductor/ext"
)

// What a host reports of its session (docs/plans/history.md, PR-12): each
// field at its bound is taken and one past it refused, the register whole.
func TestAHostsReportIsHeldToItsBounds(t *testing.T) {
	sha1, sha256 := strings.Repeat("a", 40), strings.Repeat("0f", 32)
	path := "/" + strings.Repeat("p", MaxReportPath-1)
	cases := []struct {
		name string
		r    HostReport
		ok   bool
	}{
		{"none, an older host's", HostReport{}, true},
		{"every field at its bound", HostReport{Where: "w" + strings.Repeat("-", MaxReportWhere-1), AgentVersion: strings.Repeat("é", MaxAgentVersion/2), Repo: path, RepoCommon: path, StartCommit: sha256}, true},
		{"native, a SHA-1 commit", HostReport{Where: "native", StartCommit: sha1}, true},
		{"a Windows drive's and a UNC path", HostReport{Repo: `C:\src\app`, RepoCommon: `\\server\share\app\.git`}, true},
		{"where past its bound", HostReport{Where: "w" + strings.Repeat("a", MaxReportWhere)}, false},
		{"where not a slug", HostReport{Where: "VM"}, false},
		{"where beginning with a digit", HostReport{Where: "9vm"}, false},
		{"a version past its bound", HostReport{AgentVersion: strings.Repeat("1", MaxAgentVersion+1)}, false},
		{"a version of two lines", HostReport{AgentVersion: "2.1\nfake"}, false},
		{"a version not UTF-8", HostReport{AgentVersion: "\xff"}, false},
		{"a repository past its bound", HostReport{Repo: path + "p"}, false},
		{"a common directory past its bound", HostReport{RepoCommon: path + "p"}, false},
		{"a relative repository", HostReport{Repo: "src/app"}, false},
		{"an address for a repository", HostReport{Repo: "https://user:secret@example.com/app.git"}, false},
		{"a path with a control character", HostReport{RepoCommon: "/src/\x1b[2Japp"}, false},
		{"a short commit", HostReport{StartCommit: sha1[:39]}, false},
		{"a commit one past", HostReport{StartCommit: sha1 + "a"}, false},
		{"an uppercase commit", HostReport{StartCommit: strings.Repeat("A", 40)}, false},
	}
	for _, c := range cases {
		if err := c.r.Check(); (err == nil) != c.ok {
			t.Errorf("%s: Check answered %v", c.name, err)
		}
	}
	// Where is ext.Where's form, at the same bound.
	if MaxReportWhere != ext.MaxWhere {
		t.Fatalf("MaxReportWhere %d, ext.MaxWhere %d", MaxReportWhere, ext.MaxWhere)
	}
	for _, w := range []string{"native", "vm", "a-1", "VM", "9vm", "-vm", "vm_1", "vm 1", strings.Repeat("a", ext.MaxWhere), strings.Repeat("a", ext.MaxWhere+1)} {
		if whereSlug(w) != (ext.Where(w).Valid() == nil) {
			t.Errorf("where %q: the report's check and ext.Where's differ", w)
		}
	}
}

// A reported field is one line drawn as it reads: a character that
// changes how the line is drawn without being seen (a direction override
// or isolate, a zero-width space or joiner, a line or paragraph separator)
// is refused in every field, as a control character is.
func TestAReportedFieldIsOneLineDrawnInOrder(t *testing.T) {
	// Built from their code points, so that none is in this file itself.
	for _, c := range []rune{0x202e, 0x2066, 0x200b, 0x200d, 0xfeff, 0x2028, 0x2029} {
		r := string(c)
		for name, rep := range map[string]HostReport{
			"where":        {Where: "native" + r},
			"agentVersion": {AgentVersion: "2.1.304" + r + "evil"},
			"repo":         {Repo: "/src/" + r + "app"},
			"repoCommon":   {RepoCommon: "/src/app/" + r + ".git"},
			"startCommit":  {StartCommit: strings.Repeat("a", 39) + r},
		} {
			if rep.Check() == nil {
				t.Errorf("%s with %U was taken", name, c)
			}
		}
	}
	if (HostReport{AgentVersion: "2.1.304 (Claude Code)", Repo: "/Users/adá/app"}).Check() != nil {
		t.Fatal("plain text was refused")
	}
}

// A reported field is made of what its form allows, and of nothing else:
// one character of every general category the standard library can tell
// (the first of each two-letter one of unicode.Categories; Cs none, which
// UTF-8 cannot hold), at the start, in the middle and at the end of each
// field of the report and of a session_info, is taken exactly when the
// field's form takes it. A text field (a version; a path, after its root)
// takes a letter, a number, punctuation, a symbol and the space, and a
// combining mark after one of those, never one first; where its slug's
// characters; a commit id none. The grapheme joiner and the variation
// selectors of both ranges are refused after a letter; accented text, a
// path in another script, a decomposed accent and a version's usual
// punctuation are taken.
func TestAReportedFieldIsMadeOfWhatItsFormAllows(t *testing.T) {
	commit := strings.Repeat("a", 40)
	text := map[string]func(string) HostReport{
		"agentVersion": func(v string) HostReport { return HostReport{AgentVersion: v} },
		"repo":         func(v string) HostReport { return HostReport{Repo: "/" + v} },
		"repoCommon":   func(v string) HostReport { return HostReport{RepoCommon: "/" + v} },
	}
	for _, cat := range slices.Sorted(maps.Keys(unicode.Categories)) {
		if len(cat) != 2 || cat == "Cs" {
			continue
		}
		tab := unicode.Categories[cat]
		r := rune(0)
		if len(tab.R16) > 0 {
			r = rune(tab.R16[0].Lo)
		} else {
			r = rune(tab.R32[0].Lo)
		}
		c := string(r)
		base := r == ' ' || strings.ContainsRune("LNPS", rune(cat[0]))
		for i, at := range []func(string, string) string{
			func(s, c string) string { return c + s },
			func(s, c string) string { return s[:2] + c + s[2:] },
			func(s, c string) string { return s + c },
		} {
			for name, rep := range text {
				// A version's start is the field's; a path's is after its root,
				// which is punctuation.
				want := base || (cat[0] == 'M' && (i > 0 || name != "agentVersion"))
				if got := rep(at("1.4.0", c)).Check() == nil; got != want {
					t.Errorf("%s %U (%s) at %d: taken %v, want %v", name, r, cat, i, got, want)
				}
			}
			slug := r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '-')
			if got := (HostReport{Where: at("native", c)}).Check() == nil; got != slug {
				t.Errorf("where %U (%s) at %d: taken %v, want %v", r, cat, i, got, slug)
			}
			if (HostReport{StartCommit: at(commit, c)}).Check() == nil {
				t.Errorf("startCommit %U (%s) at %d taken", r, cat, i)
			}
			if b, _ := json.Marshal(HostSessionInfoMsg{T: HostSessionInfo, EndCommit: at(commit, c)}); func() bool { _, err := ParseSessionInfo(b); return err == nil }() {
				t.Errorf("endCommit %U (%s) at %d taken", r, cat, i)
			}
		}
	}
	for _, r := range []rune{0x034f, 0xfe00, 0xfe0f, 0xe0100, 0xe01ef} {
		for name, rep := range text {
			if rep("é"+string(r)+"1.4").Check() == nil {
				t.Errorf("%s with %U after a letter was taken", name, r)
			}
		}
	}
	for _, ok := range []HostReport{
		{AgentVersion: "2.1.304 (Claude Code)", Repo: "/Users/adá/app"},
		{AgentVersion: "v0.163.0-alpha.2+build_7", Repo: "/home/ада/проект", RepoCommon: "/home/ада/проект/.git"},
		{Repo: "/Users/李/项目", RepoCommon: "C:\\Users\\Zoë\\app\\.git"},
		{Repo: "/Users/ade\u0301/app"},
	} {
		if err := ok.Check(); err != nil {
			t.Errorf("%+v refused", ok)
		}
	}
}

// An older host's register, with none of the report's fields, decodes with
// an empty report, and a report's fields ride beside the session's own.
func TestAnOldHostsRegisterHasNoReport(t *testing.T) {
	var old Register
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"box","version":"v"},"session":{"name":"s","agentId":"claude","command":["claude"],"cwd":"/src","cols":80,"rows":24}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Session.HostReport != (HostReport{}) || old.Session.HostReport.Check() != nil {
		t.Fatalf("an old host's report: %+v", old.Session.HostReport)
	}
	b, _ := json.Marshal(HostSession{Name: "s", HostReport: HostReport{Where: "native", StartCommit: strings.Repeat("a", 40)}})
	if !strings.Contains(string(b), `"where":"native"`) || !strings.Contains(string(b), `"startCommit":"`) || strings.Contains(string(b), "HostReport") {
		t.Fatalf("the session's encoding: %s", b)
	}
	if b, _ := json.Marshal(Registered{T: HostRegistered, Links: []string{}}); strings.Contains(string(b), "sessionInfo") {
		t.Fatalf("a registered that does not take session_info says so: %s", b)
	}
}

// A session_info at its bound is read; one byte past it, one of another
// type and one whose end commit is no commit id are refused.
func TestASessionInfoIsHeldToItsBounds(t *testing.T) {
	commit := strings.Repeat("c", 64)
	body := `{"t":"session_info","endCommit":"` + commit + `"}`
	at := body + strings.Repeat(" ", MaxSessionInfo-len(body))
	m, err := ParseSessionInfo([]byte(at))
	if err != nil || m.EndCommit != commit {
		t.Fatalf("a session_info at its bound: %+v %v", m, err)
	}
	if _, err := ParseSessionInfo([]byte(at + " ")); err == nil {
		t.Fatal("a session_info one past its bound was read")
	}
	if m, err := ParseSessionInfo([]byte(`{"t":"session_info"}`)); err != nil || m.EndCommit != "" {
		t.Fatalf("a session_info with no end commit: %+v %v", m, err)
	}
	for _, bad := range []string{`{"t":"status","endCommit":"` + commit + `"}`, `{"t":"session_info","endCommit":"HEAD"}`, `{"t":"session_info","endCommit":"` + commit + `a"}`, `{"t":"session_info"`} {
		if _, err := ParseSessionInfo([]byte(bad)); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
}
