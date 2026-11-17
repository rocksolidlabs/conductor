package crew

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// treeSecretPlaceholder is how a preview names the tree's secret: no
// placeholder expands to it, and the label puts it in for the admin alone.
const treeSecretPlaceholder = "${CONDUCTOR_TREE_SECRET}"

// The repository's own conductor.manifest.json, which serves Conductor
// itself in a tree (its own server on the loopback, its data under the
// tree's state directory, publishing nothing, its workbench token the
// tree's secret; the web app's dev server proxying to it), passes the
// manifest's own parse and rules, expands with drawn ports and a state
// directory, and names the secret in its preview alone: no argv holds it,
// raw or expanded, and the inner server takes it by the variable's name.
// Its probes wait 900 s at 5 s intervals, since a fresh tree compiles the
// server and builds the dev app on its first request.
func TestTheRepositorysManifestIsValid(t *testing.T) {
	path := filepath.Join("..", "..", manifestFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxManifestFile {
		t.Fatalf("%s is %d bytes, more than %d", manifestFile, len(b), maxManifestFile)
	}
	var rec Manifest
	if err := store.DecodeStrict(b, &rec); err != nil {
		t.Fatalf("%s: %v", manifestFile, err)
	}
	if err := rec.validate(manifestFile, nil, true); err != nil {
		t.Fatal(err)
	}
	argvs := func(m Manifest) map[string][]string {
		out := map[string][]string{}
		for phase, list := range map[string][]Check{"prerequisite": m.Prerequisites, "setup": m.Setup, "service": m.Services, "verify": m.Verify, "cleanup": m.Cleanup} {
			for _, ch := range list {
				out[phase+" "+ch.ID] = ch.Argv
			}
		}
		for _, p := range m.Ready {
			out["ready "+p.ID] = p.Argv
		}
		return out
	}
	if got := map[string]bool{"go version": slices.ContainsFunc(rec.Prerequisites, func(c Check) bool { return slices.Equal(c.Argv, []string{"go", "version"}) }), "node --version": slices.ContainsFunc(rec.Prerequisites, func(c Check) bool { return slices.Equal(c.Argv, []string{"node", "--version"}) })}; !got["go version"] || !got["node --version"] {
		t.Errorf("the prerequisites are not go version and node --version: %+v", rec.Prerequisites)
	}
	if len(rec.Env) != 0 {
		t.Errorf("the manifest asks for the host's variables %v", rec.Env)
	}

	state := t.TempDir()
	ports := map[string]int{"api": 41001, "web": 41002}
	if !slices.Equal(rec.Ports, []string{"api", "web"}) {
		t.Fatalf("ports %v, want api and web", rec.Ports)
	}
	out, err := expandManifest(rec, ports, state)
	if err != nil {
		t.Fatal(err)
	}

	// The preview: the web port on the loopback, the secret in its fragment as the placeholder alone.
	if want := "http://127.0.0.1:41002/#token=" + treeSecretPlaceholder; out.Preview != want {
		t.Errorf("the preview expands to %q, want %q", out.Preview, want)
	}
	if !loopbackURLPattern.MatchString(out.Preview) {
		t.Errorf("the preview %q is not on the loopback", out.Preview)
	}
	// No argv holds the secret, as written or expanded; no argv holds a token at all.
	for _, m := range []Manifest{rec, out} {
		for step, argv := range argvs(m) {
			for _, a := range argv {
				if strings.Contains(a, "TREE_SECRET}") || strings.Contains(a, "WORKBENCH_TOKEN") || strings.HasPrefix(a, "--token=") || a == "--token" {
					t.Errorf("%s: argv %q holds a token or names one as a value", step, argv)
				}
			}
		}
	}
	// The probes: on the loopback at each drawn port, 900 s at 5 s intervals.
	if len(out.Ready) != 2 {
		t.Fatalf("ready probes %+v, want one for each port", out.Ready)
	}
	for i, p := range out.Ready {
		if p.TimeoutSeconds != 900 || p.IntervalSeconds != 5 || len(p.Argv) != 0 || !loopbackURLPattern.MatchString(p.URL) {
			t.Errorf("ready[%d] %+v: want a loopback URL, 900 s, every 5 s", i, p)
		}
	}
	if !strings.HasPrefix(out.Ready[0].URL, "http://127.0.0.1:41001/") || !strings.HasPrefix(out.Ready[1].URL, "http://127.0.0.1:41002/") {
		t.Errorf("the probes are not at the api and web ports: %q, %q", out.Ready[0].URL, out.Ready[1].URL)
	}

	// The services: the inner server on the loopback, its data in the tree's state directory, publishing nothing, its token
	// the variable the tree's secret is in; the dev server proxying to that server.
	services := map[string][]string{}
	for _, s := range out.Services {
		services[s.ID] = s.Argv
	}
	api, web := services["api"], services["web"]
	for _, want := range [][]string{
		{"env", "CONDUCTOR_DATA_DIR=" + state + "/data"},
		{"CONDUCTOR_RENDEZVOUS=0"},
		{"serve", "--dev"},
		{"--token-env", "CONDUCTOR_TREE_SECRET"},
		{"--listen", "127.0.0.1:41001"},
	} {
		if !argvHolds(api, want) {
			t.Errorf("the api service %q does not hold %q", api, want)
		}
	}
	for _, want := range [][]string{{"env", "CONDUCTOR_DEV_API=127.0.0.1:41001"}, {"--port", "41002"}, {"--host", "127.0.0.1"}} {
		if !argvHolds(web, want) {
			t.Errorf("the web service %q does not hold %q", web, want)
		}
	}
	if len(services) != 2 {
		t.Errorf("services %v, want api and web", services)
	}
}

// argvHolds reports whether argv holds want as consecutive elements.
func argvHolds(argv, want []string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
