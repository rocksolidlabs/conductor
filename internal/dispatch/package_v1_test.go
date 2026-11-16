package dispatch

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// updatePackageV1 writes testdata/package-v1-main.json from the package
// this code builds. It is run once on origin/main's code, which builds
// version 1 (this file copied there as it is), never on this branch:
// `go test ./internal/dispatch -run TestAPackageWithNothingOfGitHubsIsVersionOnes -update-package-v1`.
var updatePackageV1 = flag.Bool("update-package-v1", false, "write testdata/package-v1-main.json (on origin/main's code)")

// packageNormal is the package's bytes with what differs from one run to
// the next (times, SHA-256 digests, run ids, ids) written as placeholders.
func packageNormal(raw []byte) string {
	s := string(raw)
	for _, r := range []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9]{2}:[0-9]{2})`), "<time>"},
		{regexp.MustCompile(`[0-9a-f]{64}`), "<sha256>"},
		{regexp.MustCompile(`crew-[0-9a-f]{8}`), "<run>"},
		{regexp.MustCompile(`"[0-9a-f]{16}"`), `"<id>"`},
	} {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// A ticket with nothing of GitHub's (no linked item, no evidence) is
// launched with the package origin/main builds, but for its version: no
// member of version 2 is written, empty or null.
func TestAPackageWithNothingOfGitHubsIsVersionOnes(t *testing.T) {
	s, _, key := planTicket(t)
	v := mustLaunch(t, s, "l1", key, simplePlan(t, s, key), "")
	got := packageNormal(v.Package)
	golden := filepath.Join("testdata", "package-v1-main.json")
	if *updatePackageV1 {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := strings.CutPrefix(string(main), `{"version":1,`)
	if !ok || got != `{"version":2,`+want {
		t.Fatalf("the package is not origin/main's but for its version:\n%s\norigin/main's:\n%s", got, main)
	}
}
