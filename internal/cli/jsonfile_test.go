package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// A request file, padded with white space after its value to n bytes.
func padded(value string, n int) string {
	return value + strings.Repeat(" ", n-len(value))
}

// growing is a file that grows while it is read: each Read gives the
// next part, as if it had just been written.
type growing struct{ parts []string }

func (g *growing) Read(p []byte) (int, error) {
	if len(g.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, g.parts[0])
	if g.parts[0] = g.parts[0][n:]; g.parts[0] == "" {
		g.parts = g.parts[1:]
	}
	return n, nil
}

// readOneJSON's rule at its edges, for a request file and for stdin: a
// file of exactly the limit is read, one byte more is refused (white
// space included, so a second value past the limit is never cut off and
// missed); exactly one value, white space after it taken, a second value
// or anything else after it refused; a value cut short, an empty file
// and an unknown field refused; a file that grows while it is read is
// judged by all it gave, past the limit or with a second value (#689,
// review round 2).
func TestReadOneJSONAtItsEdges(t *testing.T) {
	const value = `{"type":"clarification","question":"Which schema?"}`
	limit := maxResultFile
	dir := t.TempDir()
	for _, c := range []struct {
		name, body string
		ok         bool
		words      string
	}{
		{"exactly the limit", padded(value, limit), true, ""},
		{"white space after it", value + " \n\t\r\n", true, ""},
		{"one byte over", padded(value, limit+1), false, "larger than 1048576 bytes"},
		{"a second value after white space to the limit", padded(value, limit) + `{"type":"approval"}`, false, "larger than 1048576 bytes"},
		{"a second value", value + ` {"type":"approval"}`, false, "more after its one JSON value"},
		{"a second value without a space", value + `{}`, false, "more after its one JSON value"},
		{"anything else after it", value + ` x`, false, "more after its one JSON value"},
		{"a stray bracket after it", value + `]`, false, "more after its one JSON value"},
		{"a value cut short", value[:len(value)-1], false, "unexpected EOF"},
		{"an empty file", "", false, "no JSON value"},
		{"only white space", "  \n", false, "no JSON value"},
		{"an unknown field", `{"type":"clarification","question":"Q","nope":1}`, false, `unknown field "nope"`},
	} {
		file := filepath.Join(dir, "request.json")
		if err := os.WriteFile(file, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{file, "-"} {
			var req crew.DecisionRequest
			err := readOneJSON(name, strings.NewReader(c.body), limit, &req)
			if c.ok && (err != nil || req.Question != "Which schema?") {
				t.Errorf("%s (%s): %v %+v", c.name, name, err, req)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), c.words) || !strings.HasPrefix(err.Error(), name+": ")) {
				t.Errorf("%s (%s): %v, want %q", c.name, name, err, c.words)
			}
		}
	}
	for _, c := range []struct {
		name  string
		parts []string
		words string
	}{
		{"grows past the limit", []string{value, strings.Repeat(" ", limit-len(value)), " "}, "larger than"},
		{"grows a second value", []string{value, "\n", `{"type":"approval"}`}, "more after its one JSON value"},
	} {
		var req crew.DecisionRequest
		if err := readOneJSON("-", &growing{parts: c.parts}, limit, &req); err == nil || !strings.Contains(err.Error(), c.words) {
			t.Errorf("%s: %v, want %q", c.name, err, c.words)
		}
	}
}

// Every command that reads a request file goes by readOneJSON's rule:
// task decide --file and task result --file, with a request followed by
// white space through the limit and then a second value, exit 2 and send
// nothing (before, the limit cut the file and the first value was sent),
// and send a file of exactly the limit; crew create refuses a second
// value too (#689, review round 2).
func TestEveryRequestFileGoesByTheRule(t *testing.T) {
	var sent atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		switch r.URL.Path {
		case "/api/sessions/s1/decisions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"decision":{"id":"dr-1","state":"open"}}`))
		case "/api/sessions/s1/task/result":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":{}}`))
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	defer hs.Close()
	inSession(t, hs)
	dir := t.TempDir()
	write := func(name, body string) string {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	decide := `{"type":"clarification","question":"Which schema?"}`
	result := `{"summary":"done","commit":"0123456789abcdef0123456789abcdef01234567"}`
	for _, c := range []struct{ cmd, value string }{{"decide", decide}, {"result", result}} {
		over := write(c.cmd+"-over.json", padded(c.value, maxResultFile)+`{"summary":"another"}`)
		before := sent.Load()
		if code, out, err := task(t, "", c.cmd, "--file", over); code != 2 || err == nil || out != "" || sent.Load() != before {
			t.Errorf("%s --file with a second value past the limit: exit %d %q %v, %d sent", c.cmd, code, out, err, sent.Load()-before)
		}
		if code, _, err := task(t, padded(c.value, maxResultFile+1), c.cmd, "--file", "-"); code != 2 || err == nil || sent.Load() != before {
			t.Errorf("%s --file - one byte over: exit %d %v", c.cmd, code, err)
		}
		at := write(c.cmd+"-at.json", padded(c.value, maxResultFile))
		if code, _, err := task(t, "", c.cmd, "--file", at); code != 0 || err != nil || sent.Load() != before+1 {
			t.Errorf("%s --file at the limit: exit %d %v, %d sent", c.cmd, code, err, sent.Load()-before)
		}
	}
	before := sent.Load()
	if code, _, _, err := runCrewCmd(t, `{"name":"c","self":"me","members":[]} {"name":"d"}`, "create", "-"); code != 2 || err == nil || !strings.Contains(err.Error(), "more after its one JSON value") || sent.Load() != before {
		t.Errorf("crew create with a second value: exit %d %v", code, err)
	}
}
