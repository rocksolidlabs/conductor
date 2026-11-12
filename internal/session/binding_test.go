package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// What a session was launched with carries its login and its model for a
// resume (docs/plans/capacity.md, PR-4), through a rename, and its Info,
// what every viewer reads, carries neither.
func TestLaunchedCarriesAccount(t *testing.T) {
	b := BoundAccount{ID: "a-aaaaaaaaaaaa", Label: "Work", Folder: "/logins/work", Source: SourceAccount, Login: "fingerprint"}
	s, _ := newLocalWith(t, Options{Launched: Launched{AgentID: "claude", Name: "first", Account: b, Model: "opus"}})
	s.Rename("second")
	got := s.Launched()
	if got.Account != b || got.Model != "opus" || got.Name != "second" {
		t.Fatalf("Launched() = %+v", got)
	}
	info, _ := json.Marshal(s.Info())
	for _, leak := range []string{b.ID, b.Folder, b.Login, "opus"} {
		if strings.Contains(string(info), leak) {
			t.Fatalf("the session's Info carries %q: %s", leak, info)
		}
	}
}
