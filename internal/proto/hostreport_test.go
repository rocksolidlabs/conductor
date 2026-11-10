package proto

import (
	"encoding/json"
	"strings"
	"testing"

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
