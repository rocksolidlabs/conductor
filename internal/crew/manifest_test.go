package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// fullManifest is a project manifest with every phase, valid beside
// managedCrew's tasks (whose one check is "unit").
func fullManifest() *Manifest {
	return &Manifest{
		Prerequisites: []Check{{ID: "node", Argv: []string{"node", "--version"}}},
		Env:           []string{"NPM_TOKEN"},
		Ports:         []string{"web", "db"},
		Setup:         []Check{{ID: "deps", Argv: []string{"npm", "ci"}, TimeoutSeconds: 900}},
		Services: []Check{
			{ID: "dbserver", Argv: []string{"postgres", "-p", "${CONDUCTOR_PORT_DB}", "-D", "${CONDUCTOR_STATE_DIR}/db"}},
			{ID: "webserver", Argv: []string{"npm", "run", "dev", "--", "--port", "${CONDUCTOR_PORT_WEB}"}},
		},
		Ready: []Probe{
			{ID: "dbready", Argv: []string{"pg_isready", "-p", "${CONDUCTOR_PORT_DB}"}},
			{ID: "webready", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health", TimeoutSeconds: 120, IntervalSeconds: 1},
		},
		Preview: "http://localhost:${CONDUCTOR_PORT_WEB}/",
		Verify:  []Check{{ID: "e2e", Argv: []string{"npm", "run", "e2e"}}},
		Cleanup: []Check{{ID: "dropdb", Argv: []string{"rm", "-rf", "${CONDUCTOR_STATE_DIR}/db"}}},
	}
}

// manifestCrew is managedCrew with fullManifest in place of its legacy setup.
func manifestCrew() Crew {
	c := managedCrew()
	c.Setup = nil
	c.Manifest = fullManifest()
	return c
}

// steps returns n steps whose ids start with prefix.
func steps(prefix string, n int) []Check {
	var out []Check
	for i := range n {
		out = append(out, Check{ID: fmt.Sprintf("%s%d", prefix, i), Argv: []string{"true"}})
	}
	return out
}

func TestAProjectManifestWithEveryPhaseIsValid(t *testing.T) {
	if err := manifestCrew().ValidateSave(); err != nil {
		t.Fatal(err)
	}
	// Every list at its bound.
	c := manifestCrew()
	rec := c.Manifest
	rec.Prerequisites, rec.Setup, rec.Verify, rec.Cleanup = steps("p", maxManifestSteps), steps("s", maxManifestSteps), steps("v", maxManifestSteps), steps("c", maxManifestSteps)
	rec.Services = steps("svc", maxManifestServices)
	rec.Ready = nil
	for i := range maxManifestSteps {
		rec.Ready = append(rec.Ready, Probe{ID: fmt.Sprintf("r%d", i), URL: "https://[::1]:8443/", TimeoutSeconds: maxProbeTimeout, IntervalSeconds: maxProbeInterval})
	}
	rec.Env = nil
	for i := range maxManifestEnv {
		rec.Env = append(rec.Env, fmt.Sprintf("VAR_%d", i))
	}
	rec.Ports = []string{"a", "b", "c", "d", "e", "f", "web", strings.Repeat("p", 16)}
	rec.Preview = "http://127.0.0.1:${CONDUCTOR_PORT_" + strings.ToUpper(strings.Repeat("p", 16)) + "}/" + strings.Repeat("x", maxPreview-len("http://127.0.0.1:${CONDUCTOR_PORT_PPPPPPPPPPPPPPPP}/"))
	if len(rec.Preview) != maxPreview {
		t.Fatalf("the preview is %d bytes", len(rec.Preview))
	}
	if err := c.ValidateSave(); err != nil {
		t.Fatal(err)
	}
	// The file form alone, and a manifest with the setup phase alone.
	c.Manifest = &Manifest{File: true}
	if err := c.ValidateSave(); err != nil {
		t.Fatal(err)
	}
	c.Manifest = &Manifest{Setup: []Check{{ID: "deps", Argv: []string{"npm", "ci"}}}}
	if err := c.ValidateSave(); err != nil {
		t.Fatal(err)
	}
}

// The validation table: every rule of a crew's project manifest, each case
// refused with words that name it (web/app/utils/crews.test.ts holds the
// same cases for manifestProblems).
func TestAProjectManifestIsHeldToItsRules(t *testing.T) {
	big := strings.Repeat("x", maxArg)
	cases := []struct {
		name  string
		edit  func(c *Crew)
		words string
	}{
		{"a manifest without tasks", func(c *Crew) {
			c.Tasks = nil
			c.Members[0].Start, c.Members[1].Start = Start{When: StartImmediately}, Start{When: StartImmediately}
		}, "a project manifest is only set with tasks"},
		{"setup beside the manifest", func(c *Crew) { c.Setup = []Check{{ID: "old", Argv: []string{"true"}}} }, "setup is the project manifest's setup phase: put it there"},
		{"schema in a crew's manifest", func(c *Crew) { c.Manifest.Schema = 1 }, "schema is only set in conductor.manifest.json"},
		{"file beside a phase", func(c *Crew) { c.Manifest.File = true }, "with file, the manifest is conductor.manifest.json at each tree's commit: set nothing else"},
		{"too many prerequisites", func(c *Crew) { c.Manifest.Prerequisites = steps("p", maxManifestSteps+1) }, "too many prerequisites (at most 8)"},
		{"too many setup steps", func(c *Crew) { c.Manifest.Setup = steps("s", maxManifestSteps+1) }, "too many setup steps (at most 8)"},
		{"too many services", func(c *Crew) { c.Manifest.Services = steps("svc", maxManifestServices+1) }, "too many services (at most 4)"},
		{"too many ready probes", func(c *Crew) {
			for i := range maxManifestSteps - 1 {
				c.Manifest.Ready = append(c.Manifest.Ready, Probe{ID: fmt.Sprintf("r%d", i), Argv: []string{"true"}})
			}
		}, "too many ready probes (at most 8)"},
		{"too many verify steps", func(c *Crew) { c.Manifest.Verify = steps("v", maxManifestSteps+1) }, "too many verify steps (at most 8)"},
		{"too many cleanup steps", func(c *Crew) { c.Manifest.Cleanup = steps("c", maxManifestSteps+1) }, "too many cleanup steps (at most 8)"},
		{"too many env names", func(c *Crew) {
			for i := range maxManifestEnv {
				c.Manifest.Env = append(c.Manifest.Env, fmt.Sprintf("VAR_%d", i))
			}
		}, "too many env names (at most 32)"},
		{"too many ports", func(c *Crew) { c.Manifest.Ports = append(c.Manifest.Ports, "a", "b", "c", "d", "e", "f", "g") }, "too many ports (at most 8)"},
		{"a step with a bad id", func(c *Crew) { c.Manifest.Setup[0].ID = "Deps" }, "project manifest: setup[0]: id \"Deps\" must match"},
		{"a step with no program", func(c *Crew) { c.Manifest.Cleanup[0].Argv = nil }, "cleanup[0]: argv must name a program"},
		{"a step's argv past its bound", func(c *Crew) { c.Manifest.Verify[0].Argv = []string{"x", big, big} }, "verify[0]: argv must be at most 8192 bytes in all"},
		{"a step's timeout past the bound", func(c *Crew) { c.Manifest.Prerequisites[0].TimeoutSeconds = maxCheckTimeout + 1 }, "prerequisites[0]: timeoutSeconds must be between 1 and 3600"},
		{"a service with a timeout", func(c *Crew) { c.Manifest.Services[0].TimeoutSeconds = 60 }, "services[0]: a service has no timeoutSeconds: it runs as long as its tree"},
		{"an id in two lists", func(c *Crew) { c.Manifest.Cleanup[0].ID = "deps" }, "cleanup[0]: id \"deps\" is used twice in the manifest"},
		{"a probe's id taken by a step", func(c *Crew) { c.Manifest.Ready[0].ID = "webserver" }, "ready[0]: id \"webserver\" is used twice in the manifest"},
		{"an id of a task's check", func(c *Crew) { c.Manifest.Setup[0].ID = "unit" }, "setup[0]: id \"unit\" is also a check of task \"build\""},
		{"a bad env name", func(c *Crew) { c.Manifest.Env = []string{"NPM-TOKEN"} }, "env[0]: \"NPM-TOKEN\" is not a variable name"},
		{"an env name starting with a digit", func(c *Crew) { c.Manifest.Env = []string{"1PASSWORD"} }, "env[0]: \"1PASSWORD\" is not a variable name"},
		{"a CONDUCTOR_ env name", func(c *Crew) { c.Manifest.Env = []string{"CONDUCTOR_NOTIFY_TOKEN"} }, "env[0]: \"CONDUCTOR_NOTIFY_TOKEN\" is Conductor's own"},
		{"a loader", func(c *Crew) { c.Manifest.Env = []string{"PATH", "LD_PRELOAD"} }, "env[1]: \"LD_PRELOAD\" is a loader's variable"},
		{"an env name twice", func(c *Crew) { c.Manifest.Env = []string{"NPM_TOKEN", "NPM_TOKEN"} }, "env[1]: \"NPM_TOKEN\" is named twice"},
		{"a port name in capitals", func(c *Crew) { c.Manifest.Ports = []string{"Web"} }, "ports[0]: \"Web\" must match"},
		{"a port name past 16", func(c *Crew) { c.Manifest.Ports = []string{strings.Repeat("p", 17)} }, "ports[0]: \"ppppppppppppppppp\" must match"},
		{"a port name twice", func(c *Crew) { c.Manifest.Ports = []string{"web", "db", "web"} }, "ports[2]: \"web\" is named twice"},
		{"a placeholder naming no port", func(c *Crew) { c.Manifest.Setup[0].Argv = []string{"npm", "ci", "--port=${CONDUCTOR_PORT_API}"} }, "setup[0]: argv[2]: \"${CONDUCTOR_PORT_API}\" names no port of the manifest"},
		{"a port placeholder in lower case", func(c *Crew) { c.Manifest.Services[1].Argv[5] = "${CONDUCTOR_PORT_web}" }, "services[1]: argv[5]: \"${CONDUCTOR_PORT_web}\" names no port of the manifest"},
		{"a placeholder naming no port in a probe's URL", func(c *Crew) { c.Manifest.Ready[1].URL = "http://127.0.0.1:${CONDUCTOR_PORT_API}/" }, "ready[1]: url: \"${CONDUCTOR_PORT_API}\" names no port of the manifest"},
		{"a placeholder naming no port in the preview", func(c *Crew) { c.Manifest.Preview = "http://localhost:${CONDUCTOR_PORT_UI}/" }, "preview: \"${CONDUCTOR_PORT_UI}\" names no port of the manifest"},
		{"a port placeholder in a prerequisite", func(c *Crew) {
			c.Manifest.Prerequisites[0].Argv = []string{"nc", "-z", "127.0.0.1", "${CONDUCTOR_PORT_DB}"}
		}, "prerequisites[0]: argv[3]: a prerequisite runs before any tree: no placeholders"},
		{"the state directory in a prerequisite", func(c *Crew) { c.Manifest.Prerequisites[0].Argv = []string{"mkdir", "${CONDUCTOR_STATE_DIR}/cache"} }, "prerequisites[0]: argv[1]: a prerequisite runs before any tree: no placeholders"},
		{"a probe with argv and url", func(c *Crew) { c.Manifest.Ready[0].URL = "http://127.0.0.1/" }, "ready[0]: a probe has argv or url, not both"},
		{"a probe with neither", func(c *Crew) { c.Manifest.Ready[0].Argv = nil }, "ready[0]: a probe needs argv or url"},
		{"a probe with a bad id", func(c *Crew) { c.Manifest.Ready[0].ID = "db.ready" }, "ready[0]: id \"db.ready\" must match"},
		{"a probe's argv with no program", func(c *Crew) { c.Manifest.Ready[0].Argv = []string{""} }, "ready[0]: argv must name a program"},
		{"a probe's URL off the loopback", func(c *Crew) { c.Manifest.Ready[1].URL = "http://example.com/health" }, "ready[1]: url \"http://example.com/health\" must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a probe's URL on a host that starts as the loopback", func(c *Crew) { c.Manifest.Ready[1].URL = "http://127.0.0.1.example.com/" }, "must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a probe's URL with a user", func(c *Crew) { c.Manifest.Ready[1].URL = "http://localhost@example.com/" }, "must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a probe's URL of another scheme", func(c *Crew) { c.Manifest.Ready[1].URL = "ftp://127.0.0.1/" }, "must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a probe's URL with a space", func(c *Crew) { c.Manifest.Ready[1].URL = "http://127.0.0.1/a b" }, "must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a probe's URL past 2048", func(c *Crew) { c.Manifest.Ready[1].URL = "http://127.0.0.1/" + strings.Repeat("x", maxProbeURL) }, "ready[1]: url is longer than 2048 bytes"},
		{"a probe's port of 0", func(c *Crew) { c.Manifest.Ready[1].URL = "http://127.0.0.1:0/" }, "the port must be 1 to 65535"},
		{"a probe's port past 65535", func(c *Crew) { c.Manifest.Ready[1].URL = "http://[::1]:65536/" }, "the port must be 1 to 65535"},
		{"a probe's timeout past the bound", func(c *Crew) { c.Manifest.Ready[1].TimeoutSeconds = maxProbeTimeout + 1 }, "ready[1]: timeoutSeconds must be between 1 and 3600 (60 when left out)"},
		{"a probe's negative timeout", func(c *Crew) { c.Manifest.Ready[1].TimeoutSeconds = -1 }, "ready[1]: timeoutSeconds must be between 1 and 3600"},
		{"a probe's interval past the bound", func(c *Crew) { c.Manifest.Ready[1].IntervalSeconds = maxProbeInterval + 1 }, "ready[1]: intervalSeconds must be between 1 and 60 (2 when left out)"},
		{"a preview off the loopback", func(c *Crew) { c.Manifest.Preview = "https://preview.example.com/" }, "preview \"https://preview.example.com/\" must be http or https on 127.0.0.1, localhost or [::1]"},
		{"a preview past 512", func(c *Crew) { c.Manifest.Preview = "http://localhost/" + strings.Repeat("x", maxPreview) }, "preview is longer than 512 bytes"},
		{"a manifest past 64 KiB as JSON", func(c *Crew) {
			c.Manifest.Setup = nil
			for i := range maxManifestSteps {
				c.Manifest.Setup = append(c.Manifest.Setup, Check{ID: fmt.Sprintf("s%d", i), Argv: []string{big, big}})
			}
		}, "bytes as JSON, more than 65536"},
	}
	for _, tc := range cases {
		c := manifestCrew()
		tc.edit(&c)
		err := c.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words) {
			t.Errorf("%s: %v (want %q)", tc.name, err, tc.words)
		}
	}
}

// The 64 KiB bound counts a manifest as encodeManifest writes it: U+2028 and
// U+2029 escaped, six bytes each, and a lone surrogate, which the web's
// JSON.stringify sends as an escape, decoded as U+FFFD, three bytes.
// web/app/utils/crews.test.ts holds the same cases for manifestProblems.
func TestAManifestsJSONBoundCountsItAsTheServerWritesIt(t *testing.T) {
	// sized is manifestCrew with eight setup steps of two arguments for its
	// manifest, the last step's second one last: 61,457 bytes as JSON
	// before it.
	sized := func(last string) Crew {
		c := manifestCrew()
		c.Manifest = &Manifest{}
		for i := range maxManifestSteps {
			arg := strings.Repeat("y", 4066)
			if i == maxManifestSteps-1 {
				arg = last
			}
			c.Manifest.Setup = append(c.Manifest.Setup, Check{ID: fmt.Sprintf("s%d", i), Argv: []string{strings.Repeat("x", maxArg), arg}})
		}
		return c
	}
	// held checks that c's manifest is size bytes as JSON, and that it is
	// refused for it exactly when that is past the bound.
	held := func(name string, c Crew, size int) {
		t.Helper()
		if b, err := encodeManifest(*c.Manifest); err != nil || len(b) != size {
			t.Fatalf("%s: %d bytes as JSON, %v (want %d)", name, len(b), err, size)
		}
		err := c.Validate()
		if size <= maxManifestJSON {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			return
		}
		want := fmt.Sprintf("the manifest is %d bytes as JSON, more than %d", size, maxManifestJSON)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", name, err, want)
		}
	}
	ls, ps := string(rune(0x2028)), string(rune(0x2029))
	held("ASCII at the bound", sized(strings.Repeat("y", 4079)), maxManifestJSON)
	held("ASCII past it", sized(strings.Repeat("y", 4080)), maxManifestJSON+1)
	held("U+2028 at the bound", sized(strings.Repeat("y", 4073)+ls), maxManifestJSON)
	held("U+2028 past it", sized(strings.Repeat("y", 4074)+ls), maxManifestJSON+1)
	held("U+2029 past it", sized(strings.Repeat("y", 4074)+ps), maxManifestJSON+1)
	// A lone surrogate comes as the web sends it, an escape in the body,
	// and is decoded as U+FFFD.
	for _, tc := range []struct{ ys, size int }{{4076, maxManifestJSON}, {4077, maxManifestJSON + 1}} {
		body, err := json.Marshal(sized(strings.Repeat("y", tc.ys) + "@").Manifest)
		if err != nil {
			t.Fatal(err)
		}
		var rec Manifest
		if err := json.Unmarshal([]byte(strings.Replace(string(body), `@"`, `\`+`ud800"`, 1)), &rec); err != nil {
			t.Fatal(err)
		}
		if got := rec.Setup[maxManifestSteps-1].Argv[1]; got != strings.Repeat("y", tc.ys)+string(rune(0xfffd)) {
			t.Fatalf("the escape decoded as %q", got[tc.ys:])
		}
		c := manifestCrew()
		c.Manifest = &rec
		held(fmt.Sprintf("a lone surrogate after %d bytes", tc.ys), c, tc.size)
	}
}

// A legacy setup keeps main's rule: its ids unique within setup, and one
// may be a task's check id too, so no stored crew becomes invalid.
func TestALegacySetupKeepsItsRules(t *testing.T) {
	c := managedCrew()
	c.Setup = []Check{{ID: "unit", Argv: []string{"npm", "ci"}}}
	if err := c.ValidateSave(); err != nil {
		t.Fatalf("a legacy setup sharing a check's id: %v", err)
	}
	c.Setup = append(c.Setup, Check{ID: "unit", Argv: []string{"true"}})
	if err := c.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "setup[1]: id \"unit\" is used twice") {
		t.Fatalf("a setup id twice: %v", err)
	}
	// An empty manifest is none: beside a setup, and without tasks.
	c = managedCrew()
	c.Manifest = &Manifest{}
	if err := c.ValidateSave(); err != nil {
		t.Fatalf("an empty manifest beside a setup: %v", err)
	}
	c.Tasks, c.Setup = nil, nil
	c.Members[0].Start, c.Members[1].Start = Start{When: StartImmediately}, Start{When: StartImmediately}
	if err := c.ValidateSave(); err != nil {
		t.Fatalf("an empty manifest without tasks: %v", err)
	}
}

// effectiveManifest is the crew's manifest, or its legacy setup lifted into the
// setup phase, or empty; a copy either way.
func TestTheEffectiveManifestLiftsALegacySetup(t *testing.T) {
	c := managedCrew()
	got := c.effectiveManifest()
	if !slices.EqualFunc(got.Setup, c.Setup, func(a, b Check) bool { return a.ID == b.ID && slices.Equal(a.Argv, b.Argv) }) || len(got.Setup) != 1 {
		t.Fatalf("the lifted setup: %+v", got)
	}
	rest := got
	rest.Setup = nil
	if !rest.empty() {
		t.Fatalf("a lifted setup has more than its setup phase: %+v", got)
	}
	got.Setup[0].Argv[0] = "changed"
	if c.Setup[0].Argv[0] != "npm" {
		t.Fatal("the lifted setup shares the crew's argv")
	}
	r := manifestCrew()
	if eff := r.effectiveManifest(); !sameManifest(eff, *r.Manifest) {
		t.Fatalf("the manifest: %+v", eff)
	}
	if eff := testCrew(immediate("lead", "x")).effectiveManifest(); !eff.empty() {
		t.Fatalf("a crew without either: %+v", eff)
	}
	// The run reads the setup phase from it: a legacy setup and a manifest's
	// setup alike.
	e, _ := newEngine(t)
	for _, c := range []Crew{managedCrew(), func() Crew { c := managedCrew(); c.Manifest, c.Setup = &Manifest{Setup: c.Setup}, nil; return c }()} {
		r := e.add(c, prepared{}, "")
		e.launched(r)
		if len(r.manifest.Setup) != 1 || r.manifest.Setup[0].ID != "deps" || !r.hasManifest() {
			t.Fatalf("the run's setup: %+v", r.manifest.Setup)
		}
	}
}

// expand puts each placeholder in literally, inside one argv element, a
// probe's URL or the preview: never splitting the element, never expanding
// what it put in, leaving a placeholder that names no port as it is.
func TestExpandPutsThePlaceholdersInLiterally(t *testing.T) {
	ports := map[string]int{"web": 41234, "db_2": 5433}
	state := "/tmp/a dir/with $1 and ${CONDUCTOR_PORT_WEB}/.conductor/state"
	argv := []string{"server", "--port=${CONDUCTOR_PORT_WEB}", "--data", "${CONDUCTOR_STATE_DIR}/db", "${CONDUCTOR_PORT_DB_2}:${CONDUCTOR_PORT_WEB}"}
	var got []string
	for _, a := range argv {
		got = append(got, expand(a, ports, state))
	}
	want := []string{"server", "--port=41234", "--data", state + "/db", "5433:41234"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv: %q", got)
	}
	if u := expand("http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health?db=${CONDUCTOR_PORT_DB_2}", ports, state); u != "http://127.0.0.1:41234/health?db=5433" {
		t.Fatalf("url: %s", u)
	}
	if p := expand("http://localhost:${CONDUCTOR_PORT_WEB}/", ports, state); p != "http://localhost:41234/" {
		t.Fatalf("preview: %s", p)
	}
	for _, kept := range []string{"${CONDUCTOR_PORT_API}", "${CONDUCTOR_PORT_web}", "${CONDUCTOR_TASK}", "$CONDUCTOR_PORT_WEB", "${CONDUCTOR_STATE_DIRX}", "no placeholder"} {
		if got := expand(kept, ports, state); got != kept {
			t.Errorf("%s expanded to %s", kept, got)
		}
	}
}

// expandManifest puts a tree's ports and state directory in every argv,
// probe URL and preview of a manifest, and holds the expanded form to the
// bounds the manifest's own is (EN1 PR-3): twenty ${CONDUCTOR_STATE_DIR}
// in a preview of 457 bytes, which validation accepts, expand past 512
// with any real state directory (a temporary one is near 100 bytes) and
// are refused with the size they expand to; the same for an argv element
// past 4,096, an argv past 8 KiB in all and a probe's URL past 2,048, in
// every phase. The prerequisites are left as they are, and the manifest
// given is not changed.
func TestTheExpandedManifestIsHeldToItsBounds(t *testing.T) {
	state := filepath.Join(t.TempDir(), ".conductor", "state")
	ports := map[string]int{"web": 41234}
	fits := Manifest{Ports: []string{"web"}, Prerequisites: []Check{{ID: "node", Argv: []string{"node", "--version"}}},
		Setup:    []Check{{ID: "cache", Argv: []string{"mkdir", "-p", "${CONDUCTOR_STATE_DIR}/cache"}}},
		Services: []Check{{ID: "web", Argv: []string{"serve", "--port", "${CONDUCTOR_PORT_WEB}"}}},
		Ready:    []Probe{{ID: "up", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health"}, {ID: "file", Argv: []string{"test", "-e", "${CONDUCTOR_STATE_DIR}/up"}}},
		Preview:  "http://localhost:${CONDUCTOR_PORT_WEB}/", Cleanup: []Check{{ID: "rm", Argv: []string{"rm", "-rf", "${CONDUCTOR_STATE_DIR}/tmp"}}}}
	got, err := expandManifest(fits, ports, state)
	if err != nil {
		t.Fatal(err)
	}
	if got.Setup[0].Argv[2] != state+"/cache" || got.Services[0].Argv[2] != "41234" || got.Ready[0].URL != "http://127.0.0.1:41234/health" || got.Ready[1].Argv[2] != state+"/up" ||
		got.Preview != "http://localhost:41234/" || got.Cleanup[0].Argv[2] != state+"/tmp" || !reflect.DeepEqual(got.Prerequisites, fits.Prerequisites) {
		t.Fatalf("expanded: %+v", got)
	}
	if fits.Setup[0].Argv[2] != "${CONDUCTOR_STATE_DIR}/cache" || fits.Ready[0].URL != "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health" || fits.Preview != "http://localhost:${CONDUCTOR_PORT_WEB}/" {
		t.Fatalf("the manifest given was changed: %+v", fits)
	}
	url := "http://127.0.0.1/"
	preview := url + strings.Repeat(stateDirPlaceholder, 20)
	element := strings.Repeat(stateDirPlaceholder, 180)
	k := maxArg / len(state) // placeholders in a part that expands within maxArg
	part := strings.Repeat(stateDirPlaceholder, k)
	if len(preview) != 457 || len(element) > maxArg {
		t.Fatalf("the preview is %d bytes, the element %d", len(preview), len(element))
	}
	for _, tc := range []struct {
		name  string
		rec   Manifest
		words string
	}{
		{"the preview", Manifest{Preview: preview}, fmt.Sprintf("preview expands to %s bytes, more than 512", byteCount(len(url)+20*len(state)))},
		{"a setup step's argv element", Manifest{Setup: []Check{{ID: "cache", Argv: []string{"mkdir", "-p", element}}}}, fmt.Sprintf("setup cache: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))},
		{"a service's argv element", Manifest{Services: []Check{{ID: "db", Argv: []string{"postgres", "-D", element}}}}, fmt.Sprintf("service db: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))},
		{"a probe's argv element", Manifest{Ready: []Probe{{ID: "file", Argv: []string{"test", "-e", element}}}}, fmt.Sprintf("ready file: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))},
		{"a cleanup step's argv element", Manifest{Cleanup: []Check{{ID: "rm", Argv: []string{"rm", "-rf", element}}}}, fmt.Sprintf("cleanup rm: argv[2] expands to %s bytes, more than 4,096", byteCount(180*len(state)))},
		{"a verify step's argv in all", Manifest{Verify: []Check{{ID: "e2e", Argv: []string{"run", part, part, part}}}}, fmt.Sprintf("verify e2e: its argv expands to %s bytes in all, more than 8,192", byteCount(3+3*k*len(state)))},
		{"a probe's url", Manifest{Ready: []Probe{{ID: "up", URL: url + strings.Repeat(stateDirPlaceholder, 90)}}}, fmt.Sprintf("ready up: its url expands to %s bytes, more than 2,048", byteCount(len(url)+90*len(state)))},
	} {
		c := managedCrew()
		c.Setup, c.Manifest = nil, &tc.rec
		if err := c.ValidateSave(); err != nil {
			t.Fatalf("%s: the manifest is not valid: %v", tc.name, err)
		}
		if _, err := expandManifest(tc.rec, nil, state); !errors.Is(err, ErrInvalid) || err.Error() != tc.words {
			t.Errorf("%s: %v\nwant %s", tc.name, err, tc.words)
		}
	}
	for n, want := range map[int]string{0: "0", 512: "512", 4096: "4,096", 61440: "61,440", 1234567: "1,234,567"} {
		if got := byteCount(n); got != want {
			t.Errorf("byteCount(%d) = %s", n, got)
		}
	}
}

// A manifest of every phase launches (EN1 PR-5 lifted the last refusal):
// its prerequisites, env names, ports and placeholders, setup steps,
// services, ready probes and preview, verify and cleanup steps; and so do
// the file form and a legacy setup, whatever its argv holds.
func TestALaunchTakesAManifestOfEveryPhase(t *testing.T) {
	t.Setenv("EN1_EVERY_PHASE", "set")
	for name, c := range map[string]func(dir string) Crew{
		"every phase": func(dir string) Crew {
			return repoManifestCrew(dir, Manifest{Prerequisites: []Check{{ID: "tools", Argv: []string{"true"}}}, Env: []string{"EN1_EVERY_PHASE"}, Ports: []string{"web"},
				Setup:    []Check{{ID: "cache", Argv: []string{"mkdir", "-p", "${CONDUCTOR_STATE_DIR}/cache/${CONDUCTOR_PORT_WEB}"}}},
				Services: []Check{serviceLoop("web", "web.pid", false)}, Ready: []Probe{byFile("up", "web.pid")}, Preview: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/",
				Verify: []Check{{ID: "e2e", Argv: []string{"true"}}}, Cleanup: []Check{{ID: "rm", Argv: []string{"rm", "-rf", "${CONDUCTOR_STATE_DIR}/cache"}}}}, work("build", "lead"))
		},
		"a legacy setup": func(dir string) Crew {
			c := repoCrew(dir, work("build", "lead"))
			c.Setup = []Check{{ID: "cache", Argv: []string{"mkdir", "-p", "${CONDUCTOR_STATE_DIR}/cache"}}}
			return c
		},
		"the file form": func(dir string) Crew {
			commitManifestFile(t, dir, `{"schema": 1, "verify": [{"id": "e2e", "argv": ["true"]}], "cleanup": [{"id": "rm", "argv": ["true"]}]}`)
			return fileCrew(dir, work("build", "lead"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, _ := managedEngine(t)
			e.SetEnvPassthrough([]string{"EN1_EVERY_PHASE"})
			crew := c(dir)
			if err := crew.ValidateSave(); err != nil {
				t.Fatalf("the crew is not valid: %v", err)
			}
			run := launchManaged(t, e, crew)
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			stopRun(t, e, run.ID)
		})
	}
}

// A crew's manifest with its setup phase alone runs as a legacy setup runs:
// in every new attempt worktree, and in the checks' checkout.
func TestAManifestsSetupRunsAsALegacySetupDoes(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "dep", Argv: []string{"sh", "-c", "test -f dep.txt"}}}
	c := repoCrew(dir, build)
	c.Manifest = &Manifest{Setup: []Check{{ID: "deps", Argv: []string{"sh", "-c", "echo dep > dep.txt"}}}}
	run := launchManaged(t, e, c)
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if _, err := os.Stat(filepath.Join(a.Attempt.Worktree, "dep.txt")); err != nil {
		t.Fatalf("the manifest's setup did not run in the worktree: %v", err)
	}
	if rows := l.stepRows(run.ID, "build", 1); len(rows) != 1 || rows[0].Phase != ledger.PhaseSetup || rows[0].K != 1 || rows[0].Dir != a.Attempt.Worktree || rows[0].Manifest != "crew:0" || rows[0].State != ledger.CheckFinished {
		t.Fatalf("the worktree's setup receipt:\n%s", rowWords(rows))
	}
	writeFile(t, filepath.Join(a.Attempt.Worktree, "built.txt"), "yes\n")
	commitAll(t, a.Attempt.Worktree, "built")
	submit(t, e, a.Attempt.SessionID, Result{Summary: "built"})
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	// The checkout's setup is the attempt's second row of deps, then the check.
	rows := l.stepRows(run.ID, "build", 1)
	if len(rows) != 3 || rows[1].ID != "deps" || rows[1].K != 2 || rows[1].Dir != a.Attempt.Worktree+".check" || rows[2].Phase != ledger.PhaseCheck || rows[2].ID != "dep" || rows[2].K != 1 {
		t.Fatalf("the receipts:\n%s", rowWords(rows))
	}
	// The run's row keeps the crew as launched, manifest and all.
	var launched Crew
	l.mu.Lock()
	err := json.Unmarshal([]byte(l.runs[run.ID].Crew), &launched)
	l.mu.Unlock()
	if err != nil || launched.Manifest == nil || len(launched.Manifest.Setup) != 1 || len(launched.Setup) != 0 {
		t.Fatalf("the crew as launched: %+v %v", launched, err)
	}
	stopRun(t, e, run.ID)
}

// manifestRepo is a repository whose crew directory is sub/, with a commit
// holding sub/conductor.manifest.json as content, and that commit's id.
func manifestRepo(t *testing.T, content string) (dir, commit string) {
	t.Helper()
	dir = newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", manifestFile), content)
	commitAll(t, dir, "the manifest")
	return dir, runGit(t, dir, "rev-parse", "HEAD")
}

// The file form is read at a commit, its blob id its identity, decoded
// strictly and held to a manifest's rules.
func TestTheFileFormIsReadAtACommit(t *testing.T) {
	dir, commit := manifestRepo(t, `{
  "schema": 1,
  "setup": [{"id": "deps", "argv": ["npm", "ci"]}],
  "ports": ["web"],
  "services": [{"id": "web", "argv": ["npm", "run", "dev", "--", "--port", "${CONDUCTOR_PORT_WEB}"]}],
  "ready": [{"id": "up", "url": "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}],
  "preview": "http://localhost:${CONDUCTOR_PORT_WEB}/"
}
`)
	ctx := t.Context()
	sub := filepath.Join(dir, "sub")
	rec, blob, err := resolveManifest(ctx, sub, commit, "sub/", managedCrew().Tasks)
	if err != nil {
		t.Fatal(err)
	}
	if want := runGit(t, dir, "rev-parse", commit+":sub/"+manifestFile); blob != want {
		t.Fatalf("the blob is %s, not %s", blob, want)
	}
	if rec.Schema != 1 || len(rec.Setup) != 1 || rec.Setup[0].Argv[1] != "ci" || rec.Services[0].ID != "web" || rec.Ready[0].URL == "" || rec.Preview != "http://localhost:${CONDUCTOR_PORT_WEB}/" {
		t.Fatalf("the manifest: %+v", rec)
	}
	// Read at the commit, not from the working tree: a change not
	// committed is not the manifest, and a later commit's is its own.
	writeFile(t, filepath.Join(sub, manifestFile), `{"schema": 1, "setup": [{"id": "other", "argv": ["true"]}]}`)
	if again, blob2, err := resolveManifest(ctx, sub, commit, "sub/", nil); err != nil || blob2 != blob || again.Setup[0].ID != "deps" {
		t.Fatalf("read again: %+v %s %v", again, blob2, err)
	}
	commitAll(t, dir, "another manifest")
	next := runGit(t, dir, "rev-parse", "HEAD")
	if later, blob3, err := resolveManifest(ctx, sub, next, "sub/", nil); err != nil || blob3 == blob || later.Setup[0].ID != "other" {
		t.Fatalf("the later commit's: %+v %s %v", later, blob3, err)
	}
	// From the top of the repository too: the path is the prefix's.
	if _, _, err := resolveManifest(ctx, dir, commit, "sub/", nil); err != nil {
		t.Fatalf("from the top: %v", err)
	}
	if _, _, err := resolveManifest(ctx, dir, commit, "", nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "does not hold conductor.manifest.json") {
		t.Fatalf("a commit without the file: %v", err)
	}
	if _, _, err := resolveManifest(ctx, dir, "HEAD", "sub/", nil); err == nil || !strings.Contains(err.Error(), "is not a commit id") {
		t.Fatalf("a revision that is not an id: %v", err)
	}
}

func TestTheFileFormRefusesWhatIsNotAManifest(t *testing.T) {
	big := `{"schema": 1, "setup": [{"id": "deps", "argv": ["true"]}]}` + strings.Repeat(" ", maxManifestFile)
	cases := []struct {
		name, content, words string
	}{
		{"schema 2", `{"schema": 2, "setup": [{"id": "deps", "argv": ["true"]}]}`, "schema 2 needs a newer Conductor (this one reads schema 1)"},
		{"no schema", `{"setup": [{"id": "deps", "argv": ["true"]}]}`, "schema must be 1"},
		{"file in the file", `{"schema": 1, "file": true}`, "file is set in a crew's manifest, not in conductor.manifest.json"},
		{"an unknown field", `{"schema": 1, "setups": []}`, `unknown field "setups"`},
		{"more than one value", `{"schema": 1} {"schema": 1}`, "more than one JSON value"},
		{"not JSON", `schema: 1`, "invalid character"},
		{"a file past 256 KiB", big, fmt.Sprintf("is %d bytes, more than %d", len(big), maxManifestFile)},
		{"a step id of a task's check", `{"schema": 1, "setup": [{"id": "unit", "argv": ["true"]}]}`, `setup[0]: id "unit" is also a check of task "build"`},
		{"a placeholder naming no port", `{"schema": 1, "setup": [{"id": "deps", "argv": ["x", "${CONDUCTOR_PORT_WEB}"]}]}`, `names no port of the manifest`},
	}
	for _, tc := range cases {
		dir, commit := manifestRepo(t, tc.content)
		_, _, err := resolveManifest(t.Context(), dir, commit, "sub/", managedCrew().Tasks)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.words) || !strings.Contains(err.Error(), "sub/conductor.manifest.json at "+short(commit)) {
			t.Errorf("%s: %v (want %q)", tc.name, err, tc.words)
		}
	}
	// A link, a directory and a submodule in the file's place are named,
	// never decoded.
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, "elsewhere.json"), `{"schema": 1}`)
	if err := os.Symlink("elsewhere.json", filepath.Join(dir, manifestFile)); err != nil {
		t.Fatal(err)
	}
	commitAll(t, dir, "a link")
	link := runGit(t, dir, "rev-parse", "HEAD")
	if _, _, err := resolveManifest(t.Context(), dir, link, "", nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Errorf("a link: %v", err)
	}
	runGit(t, dir, "rm", "-q", manifestFile)
	if err := os.MkdirAll(filepath.Join(dir, manifestFile), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, manifestFile, "x.json"), "{}")
	commitAll(t, dir, "a directory")
	tree := runGit(t, dir, "rev-parse", "HEAD")
	if _, _, err := resolveManifest(t.Context(), dir, tree, "", nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("a directory: %v", err)
	}
	runGit(t, dir, "rm", "-rq", manifestFile)
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+link+","+manifestFile)
	runGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "a submodule")
	gitlink := runGit(t, dir, "rev-parse", "HEAD")
	if _, _, err := resolveManifest(t.Context(), dir, gitlink, "", nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "is a submodule") {
		t.Errorf("a submodule: %v", err)
	}
}
