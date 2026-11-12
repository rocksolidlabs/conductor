package export_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/export"
)

// A tracker's token, kept in credentials/ (internal/api, trackers.go),
// never leaves the data directory in an archive and is never planted by
// one: the export lists credentials/ as left out, secret, and carries
// none of its bytes; an archive holding anything under credentials/, in
// any case or with a trailing dot, is refused before a byte is written.
func TestLeftOutCredentials(t *testing.T) {
	const planted = "ghp_PLANTEDexportTOKEN0123456789abcdefWXYZ"
	f := newFixture(t)
	put(t, f.dir, "credentials/github.json", `{"github":[{"host":"github.com","token":"`+planted+`"}]}`)
	b, m := archive(t, f.source())
	left := map[string]string{}
	for _, l := range m.Left {
		left[l.Path] = l.Why
	}
	if left["credentials/"] != export.WhySecret {
		t.Fatalf("credentials/ left out as %q, not %q: %v", left["credentials/"], export.WhySecret, left)
	}
	if bytes.Contains(b, []byte(planted)) || bytes.Contains(b, []byte(planted[4:16])) {
		t.Fatal("the archive holds the token's bytes")
	}
	names, files := entries(t, b)
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "credentials") || bytes.Contains(files[name], []byte(planted[4:16])) {
			t.Fatalf("the archive holds %s", name)
		}
	}
	for _, f := range m.Files {
		if strings.HasPrefix(strings.ToLower(f.Path), "credentials") {
			t.Fatalf("the manifest lists %s", f.Path)
		}
	}
	for _, rel := range []string{"credentials/github.json", "Credentials/github.json", "credentials./github.json", "CREDENTIALS/x", "credentials"} {
		refused(t, craft(t, [2]string{"crews/team.json", "{}"}, [2]string{rel, planted}), "which no restore takes")
	}
}
