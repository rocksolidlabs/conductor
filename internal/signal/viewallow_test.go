package signal

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// protoConstants is every constant internal/proto declares whose name
// begins with prefix, by name, read from its source: a constant a later
// release adds joins the list by itself.
func protoConstants(t *testing.T, prefix string) map[string]ast.Expr {
	t.Helper()
	dir := filepath.Join("..", "proto")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ast.Expr{}
	fset := token.NewFileSet()
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, f.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if strings.HasPrefix(name.Name, prefix) && i < len(vs.Values) {
						out[name.Name] = vs.Values[i]
					}
				}
			}
		}
	}
	return out
}

// I15: a relayed viewer whose role is not control passes the host CONTROL
// whose t is hello, ping, chat, file_get or transcript_get, and nothing
// else: every other string proto declares under any name (read from its
// own source, so one a later release adds is refused until this list names
// it, whatever its constant is called), a t
// nobody knows, a control that does not parse, and every other frame type
// are refused read_only, nothing of them sent. A control viewer's pass.
func TestRelayAllowListForViewRoles(t *testing.T) {
	allowed := map[string]bool{proto.CtlHello: true, proto.CtlPing: true, proto.CtlChat: true, proto.CtlFileGet: true, proto.CtlTranscriptGet: true}
	// Every string proto declares, whatever its constant's name: a t the
	// list might take, under any prefix (a host message's, a code's), is
	// among them, so a name the list gains is seen here however it is
	// declared. The controls (Ctl*) are each a literal, and all are read.
	var ts []string
	ctls, seen := 0, map[string]bool{}
	for name, v := range protoConstants(t, "") {
		lit, ok := v.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			if strings.HasPrefix(name, "Ctl") {
				t.Fatalf("proto.%s is not a string literal", name)
			}
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "Ctl") {
			ctls++
		}
		if !seen[s] {
			seen[s] = true
			ts = append(ts, s)
		}
	}
	sort.Strings(ts)
	if ctls < 30 || !contains(ts, proto.CtlInterrupt) || !contains(ts, proto.CtlTranscriptGet) || !contains(ts, proto.HostAnswerAs) {
		t.Fatalf("proto's strings were not all read: %d controls, %v", ctls, ts)
	}
	ts = append(ts, "a_control_a_later_release_adds")
	var types []byte
	for name, v := range protoConstants(t, "Type") {
		lit, ok := v.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			t.Fatalf("proto.%s is not a number", name)
		}
		n, err := strconv.ParseUint(lit.Value, 0, 8)
		if err != nil {
			t.Fatal(err)
		}
		if byte(n) != proto.TypeControl {
			types = append(types, byte(n))
		}
	}
	if len(types) < 11 {
		t.Fatalf("proto's frame types were not all read: %v", types)
	}

	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	for _, role := range []session.Role{session.RoleView, session.Role("")} {
		v, err := hs.AddViewer("0123456789abcdef", role, "l1", "")
		if err != nil {
			t.Fatal(err)
		}
		drainText(t, conn, proto.HostViewerJoin)
		if err := hs.StartRelay(v); err != nil {
			t.Fatal(err)
		}
		drainText(t, conn, proto.HostRelayStart)
		control := func(payload string) proto.Frame {
			return proto.Frame{Type: proto.TypeControl, Payload: []byte(payload)}
		}
		var refused []proto.Frame
		for _, tt := range ts {
			f := control(`{"t":` + strconv.Quote(tt) + `,"reqId":"r1"}`)
			if allowed[tt] {
				if err := hs.RelayToHost(v, f); err != nil {
					t.Fatalf("role %q: %s was refused: %v", role, tt, err)
				}
				relayedAs(t, conn, proto.TypeControl, tt)
				continue
			}
			refused = append(refused, f)
		}
		refused = append(refused, control(`{"reqId":"r1"}`), control(`{"t":"ping"`), control(`not json`), control(``))
		for _, typ := range types {
			refused = append(refused, proto.Frame{Type: typ, Payload: []byte(`{"t":"ping"}`)})
		}
		for _, f := range refused {
			if err := hs.RelayToHost(v, f); !errors.Is(err, session.ErrReadOnly) {
				t.Fatalf("role %q: 0x%02x %q passed: %v", role, f.Type, f.Payload, err)
			}
		}
		// Nothing of them reached the host: the next it is sent is a ping sent after them.
		if err := hs.RelayToHost(v, control(`{"t":"ping","ts":7}`)); err != nil {
			t.Fatal(err)
		}
		relayedAs(t, conn, proto.TypeControl, proto.CtlPing)
		hs.RemoveViewer(v)
		drainText(t, conn, proto.HostViewerLeave)
	}

	ctl, err := hs.AddViewer("fedcba9876543210", session.RoleControl, "l2", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	if err := hs.StartRelay(ctl); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostRelayStart)
	for _, tt := range []string{proto.CtlInterrupt, proto.CtlSubmit, proto.CtlTranscriptGet, "a_control_a_later_release_adds"} {
		if err := hs.RelayToHost(ctl, proto.Frame{Type: proto.TypeControl, Payload: []byte(`{"t":"` + tt + `","reqId":"r1"}`)}); err != nil {
			t.Fatalf("a control viewer's %s: %v", tt, err)
		}
		relayedAs(t, conn, proto.TypeControl, tt)
	}
	if err := hs.RelayToHost(ctl, proto.Frame{Type: proto.TypeInput, Payload: []byte("ls\r")}); err != nil {
		t.Fatalf("a control viewer's keys: %v", err)
	}
	relayedAs(t, conn, proto.TypeInput, "")
}

// relayedAs fails t unless the next message the host is sent is a relay
// envelope of a frame of typ (a control of t, when t is set).
func relayedAs(t *testing.T, conn *HostConn, typ byte, tt string) {
	t.Helper()
	o, ok := takeWithin(conn, 10*time.Second)
	if !ok || o.Binary == nil {
		t.Fatalf("the host was not sent the %s", tt)
	}
	env, err := proto.Decode(o.Binary)
	if err != nil {
		t.Fatal(err)
	}
	_, inner, err := proto.DecodeRelay(env.Payload)
	if err != nil || inner.Type != typ {
		t.Fatalf("relayed %+v %v, want 0x%02x", inner, err, typ)
	}
	if tt != "" {
		if got, _ := proto.ParseHeader(inner.Payload); got != tt {
			t.Fatalf("relayed %q, want %q", got, tt)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
