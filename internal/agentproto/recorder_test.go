package agentproto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// recorder is the session's side of a driver in a test: each event it is
// handed, in order.
type recorder struct {
	mu     sync.Mutex
	events []event
	// after, when set, runs after each event is recorded.
	after func(event)
}

type event struct {
	kind string
	ref  string
	text string
	fin  bool
	tool session.ToolStep
	res  session.ToolResult
	file session.FileChange
	bg   bool
	out  string
	use  session.Usage
	code string
	perm session.PermissionRequest
}

func (r *recorder) add(e event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	after := r.after
	r.mu.Unlock()
	if after != nil {
		after(e)
	}
}

func (r *recorder) all() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

func (r *recorder) kinds(kind string) []event {
	var out []event
	for _, e := range r.all() {
		if e.kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) Conversation(id string) { r.add(event{kind: "conversation", ref: id}) }
func (r *recorder) Accepted(ref string)    { r.add(event{kind: "accepted", ref: ref}) }
func (r *recorder) TurnStarted(string)     { r.add(event{kind: "turn_started"}) }
func (r *recorder) Text(ref, delta string, final bool) {
	r.add(event{kind: "text", ref: ref, text: delta, fin: final})
}
func (r *recorder) ToolStarted(t session.ToolStep) {
	r.add(event{kind: "tool_started", ref: t.Ref, tool: t})
}
func (r *recorder) ToolEnded(ref string, res session.ToolResult) {
	r.add(event{kind: "tool_ended", ref: ref, res: res})
}
func (r *recorder) FileChanged(f session.FileChange) {
	r.add(event{kind: "file_changed", ref: f.Tool, file: f})
}
func (r *recorder) Permission(p session.PermissionRequest) {
	r.add(event{kind: "permission", ref: p.Ref, perm: p})
}
func (r *recorder) PermissionWithdrawn(ref string) {
	r.add(event{kind: "permission_withdrawn", ref: ref})
}
func (r *recorder) Background(running bool) { r.add(event{kind: "background", bg: running}) }
func (r *recorder) TurnEnded(outcome string, u session.Usage) {
	r.add(event{kind: "turn_ended", out: outcome, use: u})
}
func (r *recorder) Notice(code, words string) { r.add(event{kind: "notice", code: code, text: words}) }

// render is the events as lines, a text's deltas joined into one line per
// run of the same ref.
func (r *recorder) render() string {
	var b strings.Builder
	evs := r.all()
	for i := 0; i < len(evs); i++ {
		e := evs[i]
		switch e.kind {
		case "text":
			text, fin, n := e.text, e.fin, 1
			for i+1 < len(evs) && evs[i+1].kind == "text" && evs[i+1].ref == e.ref && !fin {
				i++
				text += evs[i].text
				fin = evs[i].fin
				n++
			}
			fmt.Fprintf(&b, "text %s %d call(s) final=%v %s\n", e.ref, n, fin, strconv.Quote(text))
		case "tool_started":
			fmt.Fprintf(&b, "tool_started %s parent=%q %s summary=%q input=%s\n", e.ref, e.tool.Parent, e.tool.Name, e.tool.Summary, compact(e.tool.Input))
		case "tool_ended":
			exit := "none"
			if e.res.Exit != nil {
				exit = strconv.Itoa(*e.res.Exit)
			}
			fmt.Fprintf(&b, "tool_ended %s %s exit=%s cut=%v output=%s\n", e.ref, e.res.Status, exit, e.res.Cut, strconv.Quote(cutText(string(e.res.Output), 160)))
		case "file_changed":
			fmt.Fprintf(&b, "file_changed tool=%s %s %s +%d -%d diff=%s\n", e.file.Tool, e.file.Path, e.file.Op, e.file.Added, e.file.Removed, strconv.Quote(string(e.file.Diff)))
		case "background":
			fmt.Fprintf(&b, "background %v\n", e.bg)
		case "turn_ended":
			fmt.Fprintf(&b, "turn_ended %s in=%d out=%d cost=%.8f\n", e.out, e.use.InputTokens, e.use.OutputTokens, e.use.CostUSD)
		case "notice":
			fmt.Fprintf(&b, "notice %s %q\n", e.code, e.text)
		case "turn_started":
			b.WriteString("turn_started\n")
		case "permission":
			fmt.Fprintf(&b, "permission %s call=%s %s cwd=%q input=%s\n", e.ref, e.perm.Call, e.perm.Tool, e.perm.Cwd, compact(e.perm.Input))
		default:
			fmt.Fprintf(&b, "%s %s\n", e.kind, e.ref)
		}
	}
	return b.String()
}

// compact is raw with its keys sorted, cut.
func compact(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return strconv.Quote(string(raw))
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return cutText(strings.TrimSpace(b.String()), 200)
}

func cutText(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// discard is a child's input that takes everything.
type discard struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (d *discard) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.Write(p)
}
func (d *discard) Close() error { return nil }
func (d *discard) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.String()
}

// fixtureSends are the messages the recorded run wrote, by the uuid it
// gave them, under refs the tests name m<N>.
var fixtureSends = func() []pendingSend {
	var out []pendingSend
	for _, n := range []int{1, 2, 3, 7, 4, 5, 6, 101, 201} {
		out = append(out, pendingSend{uuid: fmt.Sprintf("c0ffee00-0000-4000-8000-%012d", n), ref: "m" + strconv.Itoa(n)})
	}
	return out
}

// fixture is the path of a fixture.
func fixture(name string) string { return filepath.Join("testdata", "claude", name) }

// replay feeds the fixture's lines to a driver's Read, as if the run's
// messages had been written by it, and returns what the session was told
// and how Read ended.
func replay(t *testing.T, out io.Reader) (*recorder, *Claude, error) {
	t.Helper()
	c := NewClaude(&discard{}, out, ClaudeOptions{})
	c.sends = fixtureSends()
	rec := &recorder{}
	err := c.Read(context.Background(), rec)
	return rec, c, err
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
