package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A hosted session's host fields travel under their JSON names, and each
// is left out when empty, so a server session's Info, and a hosted one's
// from a host that said nothing of itself, read as before.
func TestHostedInfoCarriesHostFields(t *testing.T) {
	away := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	info := Info{ID: "h1", Kind: KindHosted, HostPlatform: "linux/amd64+wsl", HostVersion: "v1.2.3", HostKey: strings.Repeat("ab", 16), HostKeyFrom: HostKeyMachine, HostLink: HostLinkDown, HostAwaySince: &away}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"hostPlatform":  "linux/amd64+wsl",
		"hostVersion":   "v1.2.3",
		"hostKey":       strings.Repeat("ab", 16),
		"hostKeyFrom":   "machine",
		"hostLink":      "down",
		"hostAwaySince": "2026-11-21T10:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	b, _ = json.Marshal(Info{ID: "s1", Kind: KindServer})
	for k := range want {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("an empty %s is on the wire: %s", k, b)
		}
	}
}
