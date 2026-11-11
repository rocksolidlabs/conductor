package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// The value registered in the identity slot is handed the host token; in
// every other slot it holds, nothing it says leaves its guard: no returned
// error, no result's words, no log line. The walk registers one value in
// every slot, makes each method of each fail with the token (an error,
// then a panic) and looks at what left the guard.

const walkToken = "code-secret-1"

// walker is one value in every slot of the registry under the identity
// slot's name. Each method records its call and, unless it is to succeed
// (a terminal or a service the walk starts to reach their methods), fails
// with the token it was handed: an error, or a panic when panics is set
// and the method is panicOn ("" for every one).
type walker struct {
	panics  bool
	panicOn string
	start   bool // StartTerminal and Serve succeed
	done    chan struct{}

	mu     sync.Mutex
	tok    string
	called map[string]int
}

func newWalker(panics bool, panicOn string) *walker {
	return &walker{panics: panics, panicOn: panicOn, done: make(chan struct{}), called: map[string]int{}}
}

func (w *walker) fail(method string) error {
	w.mu.Lock()
	w.called[method]++
	tok := w.tok
	w.mu.Unlock()
	words := method + " cannot go on with " + tok
	if w.panics && (w.panicOn == "" || w.panicOn == method) {
		panic(words)
	}
	return errors.New(words)
}

func (w *walker) calls(method string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.called[method]
}

func (*walker) Name() string { return "cloud" }
func (w *walker) Start(_ context.Context, h ext.Host) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tok = h.HostToken()
	return nil
}
func (w *walker) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, w.fail("Authenticate")
}
func (w *walker) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{}, w.fail("Route")
}
func (w *walker) Resolved(context.Context, ext.Resolution)   { _ = w.fail("Resolved") }
func (w *walker) NoteTouch(context.Context, ext.TouchedFile) { _ = w.fail("NoteTouch") }
func (w *walker) Context(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	return nil, w.fail("Context")
}
func (w *walker) Events() []string {
	if w.panicOn == "Events" {
		_ = w.fail("Events")
	}
	return nil // every event, so Notify is delivered
}
func (w *walker) Notify(context.Context, ext.Notification) error { return w.fail("Notify") }
func (w *walker) Saved(context.Context, ext.Doc) error           { return w.fail("Saved") }
func (w *walker) Deleted(context.Context, ext.Doc) error         { return w.fail("Deleted") }
func (w *walker) Restore(context.Context, ext.RestoreRequest, io.Writer) error {
	return w.fail("Restore")
}
func (w *walker) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return nil, w.fail("ICEServers")
}
func (w *walker) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, w.fail("Room")
}
func (*walker) Where() ext.Where { return "walk" }
func (w *walker) Takes(ext.ProcessRef) bool {
	_ = w.fail("Takes")
	return false
}
func (w *walker) StartTerminal(context.Context, ext.TerminalSpec) (ext.Terminal, error) {
	if w.start {
		return walkTerminal{w}, nil
	}
	return nil, w.fail("StartTerminal")
}
func (w *walker) Run(context.Context, ext.CommandSpec) ext.CommandResult {
	err := w.fail("Run")
	return ext.CommandResult{Exit: 1, Err: err.Error(), LogErr: err.Error()}
}
func (w *walker) Serve(context.Context, ext.CommandSpec) (ext.ServiceProcess, error) {
	if w.start {
		return walkService{w}, nil
	}
	return nil, w.fail("Serve")
}
func (w *walker) Probe(context.Context, ext.ProbeSpec) (int, error) { return 0, w.fail("Probe") }
func (w *walker) Port(context.Context, ext.ProcessRef) (int, error) { return 0, w.fail("Port") }

// walkTerminal and walkService are the walker's processes.
type walkTerminal struct{ w *walker }

func (t walkTerminal) Read([]byte) (int, error)  { return 0, t.w.fail("Read") }
func (t walkTerminal) Write([]byte) (int, error) { return 0, t.w.fail("Write") }
func (t walkTerminal) Resize(uint16, uint16) error {
	return t.w.fail("Resize")
}
func (t walkTerminal) Done() <-chan struct{} {
	if t.w.panicOn == "Done" {
		_ = t.w.fail("Done")
	}
	return t.w.done
}
func (t walkTerminal) Exit() ext.ExitStatus {
	return ext.ExitStatus{Code: 1, Err: t.w.fail("Exit").Error()}
}
func (t walkTerminal) Stop(context.Context, time.Duration) error {
	return t.w.fail("Stop")
}
func (t walkTerminal) Process() ext.ProcessInfo {
	if t.w.panicOn == "Process" {
		_ = t.w.fail("Process")
	}
	return ext.ProcessInfo{}
}

type walkService struct{ w *walker }

func (s walkService) Done() <-chan struct{} {
	if s.w.panicOn == "Done" {
		_ = s.w.fail("Done")
	}
	return s.w.done
}
func (s walkService) Result() ext.CommandResult {
	err := s.w.fail("Result")
	return ext.CommandResult{Exit: 1, Err: err.Error(), LogErr: err.Error()}
}
func (s walkService) Stop(context.Context) error { return s.w.fail("Stop") }

// walkRow is one method of one slot: call makes it fail through the
// guard and answers what left the guard; queued says its call reaches the
// provider later, from a queue's drain or the guard's own goroutine. A
// row with skip is not walked, for the reason it gives.
type walkRow struct {
	call   func(ctx context.Context, e *testEnv) []any
	queued bool
	skip   string
}

func terminalOf(ctx context.Context, e *testEnv) ext.Terminal {
	t, _ := e.srv.offered.StartTerminal(ctx, ext.TerminalSpec{})
	return t
}

func serviceOf(ctx context.Context, e *testEnv) ext.ServiceProcess {
	s, _ := e.srv.offered.Serve(ctx, ext.CommandSpec{})
	return s
}

const notWalkedName = "read once, by the registry, when the value registered"

// walkSlots is every slot the server holds a provider in, by its field
// of ext.Providers (and the Runtime's processes), and every method of its
// interface: TestIdentityValueQuietInEverySlot checks the table against
// the types themselves, so a slot or a method added later fails it until
// it is walked here.
var walkSlots = map[string]map[string]walkRow{
	"Identity": {
		"Name": {skip: notWalkedName},
		"Authenticate": {call: func(ctx context.Context, e *testEnv) []any {
			p, ok, err := e.srv.exts.identity.Authenticate(ctx, "unknown-token")
			return []any{p, ok, err}
		}},
	},
	"Router": {
		"Name": {skip: notWalkedName},
		"Route": {call: func(ctx context.Context, e *testEnv) []any {
			r, err := e.srv.exts.router.Route(ctx, ext.Decision{})
			return []any{r, err}
		}},
		"Resolved": {call: func(ctx context.Context, e *testEnv) []any {
			e.srv.exts.router.Resolved(ctx, ext.Resolution{})
			return nil
		}},
	},
	"Context": {
		"Name": {skip: notWalkedName},
		"NoteTouch": {queued: true, call: func(ctx context.Context, e *testEnv) []any {
			e.srv.exts.context[0].NoteTouch(ctx, ext.TouchedFile{Path: "/x", Op: "read", At: time.Now()})
			return nil
		}},
		"Context": {call: func(ctx context.Context, e *testEnv) []any {
			bs, err := e.srv.exts.context[0].Context(ctx, ext.ContextQuery{})
			return []any{bs, err}
		}},
	},
	"Notifications": {
		"Name": {skip: notWalkedName},
		"Events": {call: func(ctx context.Context, e *testEnv) []any {
			return []any{e.srv.exts.sinks[0].Events()}
		}},
		"Notify": {queued: true, call: func(ctx context.Context, e *testEnv) []any {
			return []any{e.srv.exts.sinks[0].Notify(ctx, ext.Notification{Type: "attention", At: time.Now()})}
		}},
	},
	"Continuity": {
		"Name":    {skip: notWalkedName},
		"Saved":   {call: func(ctx context.Context, e *testEnv) []any { return []any{e.srv.exts.sync.Saved(ctx, ext.Doc{})} }},
		"Deleted": {call: func(ctx context.Context, e *testEnv) []any { return []any{e.srv.exts.sync.Deleted(ctx, ext.Doc{})} }},
		"Restore": {call: func(ctx context.Context, e *testEnv) []any {
			return []any{e.srv.exts.sync.Restore(ctx, ext.RestoreRequest{}, io.Discard)}
		}},
	},
	"Media": {
		"Name": {skip: notWalkedName},
		"ICEServers": {call: func(ctx context.Context, e *testEnv) []any {
			ss, err := e.srv.exts.media.ICEServers(ctx, ext.ICERequest{Purpose: "viewer"})
			return []any{ss, err}
		}},
		"Room": {call: func(ctx context.Context, e *testEnv) []any {
			j, ok, err := e.srv.exts.media.Room(ctx, ext.RoomRequest{})
			return []any{j, ok, err}
		}},
	},
	"Runtime": {
		"Name":  {skip: notWalkedName},
		"Where": {skip: notWalkedName + " (a Where that panics fails New); it answers no error"},
		"Takes": {call: func(ctx context.Context, e *testEnv) []any { return []any{e.srv.offered.Takes(ext.ProcessRef{})} }},
		"StartTerminal": {call: func(ctx context.Context, e *testEnv) []any {
			t, err := e.srv.offered.StartTerminal(ctx, ext.TerminalSpec{})
			return []any{t == nil, err}
		}},
		"Run": {call: func(ctx context.Context, e *testEnv) []any { return []any{e.srv.offered.Run(ctx, ext.CommandSpec{})} }},
		"Serve": {call: func(ctx context.Context, e *testEnv) []any {
			s, err := e.srv.offered.Serve(ctx, ext.CommandSpec{})
			return []any{s == nil, err}
		}},
		"Probe": {call: func(ctx context.Context, e *testEnv) []any {
			n, err := e.srv.offered.Probe(ctx, ext.ProbeSpec{})
			return []any{n, err}
		}},
		"Port": {call: func(ctx context.Context, e *testEnv) []any {
			n, err := e.srv.offered.Port(ctx, ext.ProcessRef{})
			return []any{n, err}
		}},
	},
	"Runtime.Terminal": {
		"Read": {call: func(ctx context.Context, e *testEnv) []any {
			n, err := terminalOf(ctx, e).Read(make([]byte, 8))
			return []any{n, err}
		}},
		"Write": {call: func(ctx context.Context, e *testEnv) []any {
			n, err := terminalOf(ctx, e).Write([]byte("x"))
			return []any{n, err}
		}},
		"Resize": {call: func(ctx context.Context, e *testEnv) []any { return []any{terminalOf(ctx, e).Resize(80, 24)} }},
		"Done": {queued: true, call: func(ctx context.Context, e *testEnv) []any {
			return []any{terminalOf(ctx, e).Exit()} // the guard reads Done on its watch
		}},
		"Exit": {call: func(ctx context.Context, e *testEnv) []any { return []any{terminalOf(ctx, e).Exit()} }},
		"Stop": {call: func(ctx context.Context, e *testEnv) []any { return []any{terminalOf(ctx, e).Stop(ctx, 0)} }},
		"Process": {call: func(ctx context.Context, e *testEnv) []any {
			t := terminalOf(ctx, e)
			return []any{t.Process(), t.Exit()}
		}},
	},
	"Runtime.ServiceProcess": {
		"Done": {queued: true, call: func(ctx context.Context, e *testEnv) []any {
			return []any{serviceOf(ctx, e).Result()}
		}},
		"Result": {call: func(ctx context.Context, e *testEnv) []any { return []any{serviceOf(ctx, e).Result()} }},
		"Stop":   {call: func(ctx context.Context, e *testEnv) []any { return []any{serviceOf(ctx, e).Stop(ctx)} }},
	},
}

// walkTypes is the interface of every slot of walkSlots.
func walkTypes() map[string]reflect.Type {
	types := map[string]reflect.Type{
		"Runtime.Terminal":       reflect.TypeFor[ext.Terminal](),
		"Runtime.ServiceProcess": reflect.TypeFor[ext.ServiceProcess](),
	}
	pt := reflect.TypeFor[ext.Providers]()
	for i := range pt.NumField() {
		f := pt.Field(i)
		if f.Name == "All" {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		types[f.Name] = ft
	}
	return types
}

// TestIdentityValueQuietInEverySlot is the walk: one value in the identity
// slot and every other, handed the host token; each method of each slot
// fails with it, by an error and by a panic, through the guard, and the
// token is in nothing that left the guard and in no log line.
func TestIdentityValueQuietInEverySlot(t *testing.T) {
	types := walkTypes()
	for slot, typ := range types {
		rows, ok := walkSlots[slot]
		if !ok {
			t.Errorf("the slot %s is not walked", slot)
			continue
		}
		for i := range typ.NumMethod() {
			if _, ok := rows[typ.Method(i).Name]; !ok {
				t.Errorf("%s.%s is not walked", slot, typ.Method(i).Name)
			}
		}
		if len(rows) != typ.NumMethod() {
			t.Errorf("%s: %d rows for %d methods", slot, len(rows), typ.NumMethod())
		}
	}
	for slot, rows := range walkSlots {
		if _, ok := types[slot]; !ok {
			t.Errorf("the walk has a slot %s the registry does not", slot)
		}
		for method, row := range rows {
			if row.skip != "" {
				continue
			}
			for _, panics := range []bool{false, true} {
				w := newWalker(panics, method)
				w.start = strings.HasPrefix(slot, "Runtime.")
				logs := &syncLog{}
				e := newTestEnvAgents(t, func(c *config.Config) {
					c.Rendezvous.Server, c.Rendezvous.Token = "https://yard.invalid", walkToken
				}, slog.New(slog.NewTextHandler(logs, nil)), nil, WithExtensions(local.Fill(ext.Providers{
					Identity: w, Router: w, Context: []ext.ContextProvider{w}, Notifications: []ext.NotificationSink{w},
					Continuity: w, Media: w, Runtime: w,
					All: []ext.Entry{{Kind: ext.KindIdentity, Name: "cloud", Value: w}, {Kind: ext.KindDecisions, Name: "cloud", Value: w},
						{Kind: ext.KindContext, Name: "cloud", Value: w}, {Kind: ext.KindNotifications, Name: "cloud", Value: w},
						{Kind: ext.KindSync, Name: "cloud", Value: w}, {Kind: ext.KindMedia, Name: "cloud", Value: w},
						{Kind: ext.KindRuntime, Name: "cloud", Value: w}}})))
				e.srv.StartExtensions(t.Context())
				if !e.srv.ProviderStarted(t.Context(), "cloud") {
					t.Fatal("the walker did not start")
				}
				w.mu.Lock()
				handed := w.tok
				w.mu.Unlock()
				if handed != walkToken {
					t.Fatalf("the walker was handed %q", handed)
				}
				left := fmt.Sprintf("%+v", row.call(t.Context(), e))
				if row.queued {
					waitFor(testWait, func() bool { return w.calls(method) > 0 })
				}
				if w.calls(method) == 0 {
					t.Errorf("%s.%s (panics %v): the walker was never called", slot, method, panics)
				}
				e.srv.stopExtensions()
				close(w.done) // a process the walk started ends
				if strings.Contains(left, walkToken) {
					t.Errorf("%s.%s (panics %v): what left the guard holds the token: %s", slot, method, panics, left)
				}
				if strings.Contains(logs.String(), walkToken) {
					t.Errorf("%s.%s (panics %v): the log holds the token:\n%s", slot, method, panics, logs.String())
				}
			}
		}
	}
}
