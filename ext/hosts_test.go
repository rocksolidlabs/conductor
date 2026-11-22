package ext_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// hostSummary is a host summary of every field, two rows.
func hostSummary() ext.HostSummary {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return ext.HostSummary{
		Platform: "linux/amd64", Version: "v0.7.0-rc.23-5-gabc1234-dirty",
		Sessions: ext.HostSessions{Running: 3, Waiting: 1},
		Runs:     ext.HostRuns{Held: 2, Waiting: 1},
		Hosts: []ext.HostSummaryRow{
			{Name: "studio", Platform: "darwin/arm64", Version: "v0.7.0", Availability: ext.HostConnected, Since: at, Sessions: ext.HostSessions{Running: 2, Waiting: 1}},
			{Name: "Ana's desk", Platform: "linux/amd64+wsl", Version: "dev", Availability: ext.HostAway, Since: at, Sessions: ext.HostSessions{Running: 1}},
		},
	}
}

// TestHostSummaryValid holds each field of a host summary to its form and
// bound: at its bound it is taken, past it or out of its form it is
// refused, and a session's id, a path, a login or an address in a host's
// name is refused by its shape.
func TestHostSummaryValid(t *testing.T) {
	if err := hostSummary().Valid(); err != nil {
		t.Fatalf("the sample: %v", err)
	}
	long := func(n int) string { return strings.Repeat("a", n) }
	platformAt := "abcdefghijklmno/abcdefghijkl+wsl" // 32 bytes
	if len(platformAt) != ext.MaxHostPlatform {
		t.Fatalf("the platform at its bound is %d bytes", len(platformAt))
	}
	row := func(f func(*ext.HostSummaryRow)) func(*ext.HostSummary) {
		return func(s *ext.HostSummary) { f(&s.Hosts[0]) }
	}
	rows := func(n int) func(*ext.HostSummary) {
		return func(s *ext.HostSummary) {
			s.Hosts = nil
			for range n {
				s.Hosts = append(s.Hosts, ext.HostSummaryRow{Name: "studio", Availability: ext.HostConnected})
			}
		}
	}
	taken := map[string]func(*ext.HostSummary){
		"platform at its bound": func(s *ext.HostSummary) { s.Platform = platformAt },
		"version at its bound":  func(s *ext.HostSummary) { s.Version = long(ext.MaxHostVersion) },
		"counts at their bound": func(s *ext.HostSummary) {
			s.Sessions, s.Runs = ext.HostSessions{ext.MaxHostCount, ext.MaxHostCount}, ext.HostRuns{ext.MaxHostCount, ext.MaxHostCount}
		},
		"counts of none":                   func(s *ext.HostSummary) { s.Sessions, s.Runs = ext.HostSessions{}, ext.HostRuns{} },
		"no hosts":                         func(s *ext.HostSummary) { s.Hosts = nil },
		"hosts at their bound":             rows(ext.MaxHostSummaryRows),
		"a host's name at its bound":       row(func(h *ext.HostSummaryRow) { h.Name = strings.Repeat("n", ext.MaxHostName) }),
		"a host's name of a machine label": row(func(h *ext.HostSummaryRow) { h.Name = "Ana’s MacBook Pro (2024)" }),
		"a host's name of a domain":        row(func(h *ext.HostSummaryRow) { h.Name = "studio.local" }),
		"a host's short hex name":          row(func(h *ext.HostSummaryRow) { h.Name = "cafe" }),
		"a host's platform at its bound":   row(func(h *ext.HostSummaryRow) { h.Platform = platformAt }),
		"a host with no platform":          row(func(h *ext.HostSummaryRow) { h.Platform = "" }),
		"a host's version at its bound":    row(func(h *ext.HostSummaryRow) { h.Version = long(ext.MaxHostVersion) }),
		"a host with no version":           row(func(h *ext.HostSummaryRow) { h.Version = "" }),
		"a host connected since no time":   row(func(h *ext.HostSummaryRow) { h.Since = time.Time{} }),
		"a host seen":                      row(func(h *ext.HostSummaryRow) { h.Availability = ext.HostSeen }),
		"a host's counts at their bound":   row(func(h *ext.HostSummaryRow) { h.Sessions = ext.HostSessions{ext.MaxHostCount, ext.MaxHostCount} }),
	}
	for name, mutate := range taken {
		s := hostSummary()
		mutate(&s)
		if err := s.Valid(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	refused := map[string]func(*ext.HostSummary){
		// This server's platform and version.
		"no platform":                   func(s *ext.HostSummary) { s.Platform = "" },
		"a platform past its bound":     func(s *ext.HostSummary) { s.Platform = "abcdefghijklmno/abcdefghijklm+wsl" },
		"a platform of no GOARCH":       func(s *ext.HostSummary) { s.Platform = "linux" },
		"a platform in capitals":        func(s *ext.HostSummary) { s.Platform = "Linux/amd64" },
		"a platform that is a path":     func(s *ext.HostSummary) { s.Platform = "/home/ana" },
		"no version":                    func(s *ext.HostSummary) { s.Version = "" },
		"a version past its bound":      func(s *ext.HostSummary) { s.Version = long(ext.MaxHostVersion + 1) },
		"a version with a space":        func(s *ext.HostSummary) { s.Version = "v1 0" },
		"a version that is a path":      func(s *ext.HostSummary) { s.Version = "v1/../../etc" },
		"a version starting with a dot": func(s *ext.HostSummary) { s.Version = ".v1" },
		// This server's counts.
		"sessions running below 0":          func(s *ext.HostSummary) { s.Sessions.Running = -1 },
		"sessions running past their bound": func(s *ext.HostSummary) { s.Sessions.Running = ext.MaxHostCount + 1 },
		"sessions waiting below 0":          func(s *ext.HostSummary) { s.Sessions.Waiting = -1 },
		"sessions waiting past their bound": func(s *ext.HostSummary) { s.Sessions.Waiting = ext.MaxHostCount + 1 },
		"runs held below 0":                 func(s *ext.HostSummary) { s.Runs.Held = -1 },
		"runs held past their bound":        func(s *ext.HostSummary) { s.Runs.Held = ext.MaxHostCount + 1 },
		"runs waiting below 0":              func(s *ext.HostSummary) { s.Runs.Waiting = -1 },
		"runs waiting past their bound":     func(s *ext.HostSummary) { s.Runs.Waiting = ext.MaxHostCount + 1 },
		// The rows.
		"hosts past their bound": rows(ext.MaxHostSummaryRows + 1),
		// A host's name.
		"a host with no name":                   row(func(h *ext.HostSummaryRow) { h.Name = "" }),
		"a host's blank name":                   row(func(h *ext.HostSummaryRow) { h.Name = "   " }),
		"a host's name past its bound":          row(func(h *ext.HostSummaryRow) { h.Name = strings.Repeat("n", ext.MaxHostName+1) }),
		"a host's name of two lines":            row(func(h *ext.HostSummaryRow) { h.Name = "studio\nrm -rf" }),
		"a host's name with an escape":          row(func(h *ext.HostSummaryRow) { h.Name = "studio\x1b[2J" }),
		"a host's name not UTF-8":               row(func(h *ext.HostSummaryRow) { h.Name = "studio\xff" }),
		"a host's name that is a path":          row(func(h *ext.HostSummaryRow) { h.Name = "/home/ana/src/app" }),
		"a host's name that is a home path":     row(func(h *ext.HostSummaryRow) { h.Name = "~/src" }),
		"a host's name that is a Windows path":  row(func(h *ext.HostSummaryRow) { h.Name = `C:\Users\ana` }),
		"a host's name that is a login":         row(func(h *ext.HostSummaryRow) { h.Name = "ana@studio" }),
		"a host's name with a port":             row(func(h *ext.HostSummaryRow) { h.Name = "studio:22" }),
		"a host's name that is an IPv4 address": row(func(h *ext.HostSummaryRow) { h.Name = "192.0.2.10" }),
		"a host's name that is an IPv6 address": row(func(h *ext.HostSummaryRow) { h.Name = "2001:db8::1" }),
		"a host's name that is a session's id":  row(func(h *ext.HostSummaryRow) { h.Name = "0123456789abcdef" }),
		"a host's name that is a machine's id":  row(func(h *ext.HostSummaryRow) { h.Name = strings.Repeat("ab", 16) }),
		"a host's name that is a key":           row(func(h *ext.HostSummaryRow) { h.Name = strings.Repeat("C0", 32) }),
		// A host's platform and version.
		"a host's platform past its bound":  row(func(h *ext.HostSummaryRow) { h.Platform = "abcdefghijklmno/abcdefghijklm+wsl" }),
		"a host's platform out of its form": row(func(h *ext.HostSummaryRow) { h.Platform = "darwin arm64" }),
		"a host's version past its bound":   row(func(h *ext.HostSummaryRow) { h.Version = long(ext.MaxHostVersion + 1) }),
		"a host's version out of its form":  row(func(h *ext.HostSummaryRow) { h.Version = "v1\n" }),
		// A host's availability and since.
		"a host online":                 row(func(h *ext.HostSummaryRow) { h.Availability = "online" }),
		"a host of no availability":     row(func(h *ext.HostSummaryRow) { h.Availability = "" }),
		"a host away since no time":     row(func(h *ext.HostSummaryRow) { h.Availability, h.Since = ext.HostAway, time.Time{} }),
		"a host seen since no time":     row(func(h *ext.HostSummaryRow) { h.Availability, h.Since = ext.HostSeen, time.Time{} }),
		"a host since a time past 9999": row(func(h *ext.HostSummaryRow) { h.Since = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }),
		// A host's counts.
		"a host's sessions running below 0":          row(func(h *ext.HostSummaryRow) { h.Sessions.Running = -1 }),
		"a host's sessions running past their bound": row(func(h *ext.HostSummaryRow) { h.Sessions.Running = ext.MaxHostCount + 1 }),
		"a host's sessions waiting below 0":          row(func(h *ext.HostSummaryRow) { h.Sessions.Waiting = -1 }),
		"a host's sessions waiting past their bound": row(func(h *ext.HostSummaryRow) { h.Sessions.Waiting = ext.MaxHostCount + 1 }),
		// The whole, encoded: 32 names of 120 bytes that encode at 6 bytes
		// each are past 16 KiB.
		"a summary past its bound encoded": func(s *ext.HostSummary) {
			rows(ext.MaxHostSummaryRows)(s)
			for i := range s.Hosts {
				s.Hosts[i].Name = strings.Repeat("<", ext.MaxHostName)
			}
		},
	}
	for name, mutate := range refused {
		s := hostSummary()
		mutate(&s)
		if err := s.Valid(); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

// encode is s as JSON.
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseHostSummary decodes a summary strictly: an unknown field at any
// depth, a field's name in other letters or given twice, anything after
// the object, a body not an object or past MaxHostSummary bytes are each
// refused whole, never trimmed, and the refusal holds none of the body.
func TestParseHostSummary(t *testing.T) {
	good := encode(t, hostSummary())
	s, err := ext.ParseHostSummary(good)
	if err != nil {
		t.Fatalf("the sample: %v", err)
	}
	if !reflect.DeepEqual(s, hostSummary()) {
		t.Fatalf("the sample decoded = %+v", s)
	}
	const secret = "tok-SECRET-0001"
	at := `"since":"2026-11-20T09:00:00Z"`
	cases := map[string]string{
		"an unknown field":               `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[],"token":"` + secret + `"}`,
		"a host's unknown field":         `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","availability":"connected","sessions":{"running":0,"waiting":0},"user":"` + secret + `"}]}`,
		"a host's cwd":                   `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","availability":"seen",` + at + `,"sessions":{"running":0,"waiting":0},"cwd":"/home/` + secret + `"}]}`,
		"a count's unknown field":        `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0,"ids":["` + secret + `"]},"runs":{"held":0,"waiting":0},"hosts":[]}`,
		"a host's field of another part": `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","availability":"connected","sessions":{"running":0,"waiting":0},"held":1}]}`,
		"a field in capitals":            `{"Platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[]}`,
		"a host's field in capitals":     `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"NAME":"studio","availability":"connected","sessions":{"running":0,"waiting":0}}]}`,
		"a field given twice":            `{"platform":"linux/amd64","version":"v1","version":"v2","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[]}`,
		"a host's field given twice":     `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","name":"desk","availability":"connected","sessions":{"running":0,"waiting":0}}]}`,
		"something after the object":     string(good) + `{"platform":"` + secret + `"}`,
		"an array":                       `[` + string(good) + `]`,
		"null":                           `null`,
		"a string":                       `"` + secret + `"`,
		"an empty body":                  ``,
		"a field of another type":        `{"platform":"linux/amd64","version":"v1","sessions":{"running":"` + secret + `","waiting":0},"runs":{"held":0,"waiting":0},"hosts":[]}`,
		"a count with a fraction":        `{"platform":"linux/amd64","version":"v1","sessions":{"running":1.5,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[]}`,
		"a since that is not a time":     `{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","availability":"seen","since":"` + secret + `","sessions":{"running":0,"waiting":0}}]}`,
		"a summary as OS-4 kept them":    `{"hosts":[]}`,
		"a body past its bound":          string(good[:len(good)-1]) + strings.Repeat(" ", ext.MaxHostSummary) + `}`,
	}
	for name, body := range cases {
		_, err := ext.ParseHostSummary([]byte(body))
		if !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%s: %v, want a refusal", name, err)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the refusal holds the body: %v", name, err)
		}
	}
	// A body within its bound only by its spaces is taken: the bound is
	// the bytes handed in.
	padded := append(slices.Clone(good[:len(good)-1]), []byte(strings.Repeat(" ", ext.MaxHostSummary-len(good))+"}")...)
	if len(padded) != ext.MaxHostSummary {
		t.Fatalf("the padded body is %d bytes", len(padded))
	}
	if _, err := ext.ParseHostSummary(padded); err != nil {
		t.Errorf("a body at its bound: %v", err)
	}
}

// TestHostsKindValidIsTheSummarys: a hosts record whose body fails
// HostSummary.Valid, or does not decode as ParseHostSummary takes one, is
// refused by the kind's Valid with the summary's own refusal; a record of
// a valid summary is taken.
func TestHostsKindValidIsTheSummarys(t *testing.T) {
	record := func(body []byte) ext.Remote {
		return ext.Remote{Kind: ext.RemoteHosts, Device: "dev-1", DeviceName: "studio", ID: "summary", Rev: 1,
			At: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Data: body}
	}
	if err := record(encode(t, hostSummary())).Valid(); err != nil {
		t.Fatalf("a valid summary: %v", err)
	}
	pathName := hostSummary()
	pathName.Hosts[0].Name = "/home/ana"
	tooMany := hostSummary()
	tooMany.Hosts = slices.Repeat(tooMany.Hosts[:1], ext.MaxHostSummaryRows+1)
	for name, body := range map[string][]byte{
		"a host named by a path": encode(t, pathName),
		"too many hosts":         encode(t, tooMany),
		"an unknown field":       []byte(`{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[],"key":"k"}`),
		"OS-4's unjudged body":   []byte(`{"hosts":[]}`),
		"another object":         []byte(`{"x":1}`),
		"a host with no since":   []byte(`{"platform":"linux/amd64","version":"v1","sessions":{"running":0,"waiting":0},"runs":{"held":0,"waiting":0},"hosts":[{"name":"studio","availability":"away","sessions":{"running":0,"waiting":0}}]}`),
	} {
		_, want := ext.ParseHostSummary(body)
		if want == nil {
			t.Fatalf("%s: the summary's own check takes it", name)
		}
		err := record(body).Valid()
		if !errors.Is(err, ext.ErrRefused) || err.Error() != want.Error() {
			t.Errorf("%s: the kind's Valid = %v, want the summary's %v", name, err, want)
		}
	}
}

// TestHostSummaryFieldsAreThePlans: a host summary carries exactly the
// fields docs/plans/hosts.md gives it (this server's platform, version and
// counts; each host's name, platform, version, availability, since and
// counts) and no other, so nothing is added to what leaves the machine
// without this test changing with it.
func TestHostSummaryFieldsAreThePlans(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			out = append(out, tag)
		}
		return out
	}
	for _, c := range []struct {
		v    any
		want []string
	}{
		{ext.HostSummary{}, []string{"platform", "version", "sessions", "runs", "hosts"}},
		{ext.HostSummaryRow{}, []string{"name", "platform", "version", "availability", "since", "sessions"}},
		{ext.HostSessions{}, []string{"running", "waiting"}},
		{ext.HostRuns{}, []string{"held", "waiting"}},
	} {
		if got := fields(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%T's fields = %v, want %v", c.v, got, c.want)
		}
	}
}
