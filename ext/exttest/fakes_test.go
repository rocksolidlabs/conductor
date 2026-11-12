package exttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// testWait bounds what a test waits for a step a fake takes at once; it is
// no product's bound, only what keeps a broken fake from hanging the test.
const testWait = 30 * time.Second

func waitCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	t.Cleanup(cancel)
	return ctx
}

var ada = ext.Principal{Subject: "ada", Name: "Ada", Grants: []ext.Grant{ext.GrantAdmin}}

// logT is a T that logs and takes cleanups, as *testing.T does.
type logT struct {
	*testing.T
	mu    sync.Mutex
	lines []string
}

func (l *logT) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logT) logged() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func TestFakes(t *testing.T) {
	t.Run("FakeIdentity", func(t *testing.T) {
		f := NewFakeIdentity("fake-identity")
		f.Add("tok", ada)
		f.Add("", ada) // never admitted
		ctx := context.Background()
		p, ok, err := f.Authenticate(ctx, "tok")
		if !ok || err != nil || !reflect.DeepEqual(p, ada) {
			t.Fatalf("Authenticate(tok) = %+v, %v, %v; want ada", p, ok, err)
		}
		p.Grants[0] = ext.GrantHost
		if p, _, _ := f.Authenticate(ctx, "tok"); p.Grants[0] != ext.GrantAdmin {
			t.Errorf("a caller's change of an answer reached the fake")
		}
		for _, tok := range []string{"", "other"} {
			if _, ok, err := f.Authenticate(ctx, tok); ok || err != nil {
				t.Errorf("Authenticate(%q) = %v, %v; want not ok", tok, ok, err)
			}
		}
		f.Remove("tok")
		if _, ok, _ := f.Authenticate(ctx, "tok"); ok {
			t.Errorf("a removed token is still admitted")
		}
		boom := errors.New("keys out of reach")
		f.Add("tok", ada)
		f.SetErr(boom)
		if _, ok, err := f.Authenticate(ctx, "tok"); ok || !errors.Is(err, boom) {
			t.Errorf("with an error set: %v, %v", ok, err)
		}
		if f.Calls() != 6 || f.Name() != "fake-identity" {
			t.Errorf("Calls %d, Name %q", f.Calls(), f.Name())
		}
	})

	t.Run("FakeRouter", func(t *testing.T) {
		f := NewFakeRouter("fake-router")
		d := SampleDecision()
		if r, err := f.Route(context.Background(), d); err != nil || !reflect.DeepEqual(r, ext.Route{}) {
			t.Fatalf("an unscripted Route = %+v, %v; want the empty route", r, err)
		}
		want := ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}, Rule: "owns main.go", Answer: &ext.Answer{Input: "1", By: ada}}
		f.SetRoute(d.ID, want)
		r, err := f.Route(context.Background(), d)
		if err != nil || !reflect.DeepEqual(r, want) || r.Valid(d) != nil {
			t.Fatalf("Route = %+v, %v; want %+v", r, err, want)
		}
		r.Answer.Input = "2"
		if r, _ := f.Route(context.Background(), d); r.Answer.Input != "1" {
			t.Errorf("a caller's change of an answer reached the fake")
		}
		f.Resolved(context.Background(), ext.Resolution{ID: d.ID, Session: d.Session})
		if got := f.Decisions(); len(got) != 3 || got[0].ID != d.ID {
			t.Errorf("Decisions = %+v", got)
		}
		if got := f.Resolutions(); len(got) != 1 || got[0].ID != d.ID {
			t.Errorf("Resolutions = %+v", got)
		}
		// A durable decision's record is recorded with its Request, and
		// so is its resolution.
		rec := SampleDecisionRequest()
		f.Route(context.Background(), rec)
		f.Resolved(context.Background(), ext.Resolution{ID: rec.ID, Session: rec.Session, Request: rec.Request})
		if got := f.Decisions(); len(got) != 4 || got[3].Request != "dr-7" || got[0].Request != "" {
			t.Errorf("Decisions = %+v", got)
		}
		if got := f.Resolutions(); len(got) != 2 || got[1].Request != "dr-7" || got[0].Request != "" {
			t.Errorf("Resolutions = %+v", got)
		}
		boom := errors.New("down")
		f.SetErr(boom)
		if _, err := f.Route(context.Background(), d); !errors.Is(err, boom) {
			t.Errorf("with an error set: %v", err)
		}
	})

	t.Run("FakeContext", func(t *testing.T) {
		f := NewFakeContext("fake-context")
		q := SampleQuery(t.TempDir())
		if bs, err := f.Context(context.Background(), q); bs != nil || err != nil {
			t.Fatalf("an unscripted Context = %v, %v", bs, err)
		}
		blocks := []ext.ContextBlock{{Title: "Callers of Build", Ref: "main.go", Body: "main calls Build."}}
		f.SetBlocks(blocks)
		bs, _ := f.Context(context.Background(), q)
		if !reflect.DeepEqual(bs, blocks) {
			t.Fatalf("Context = %+v", bs)
		}
		bs[0].Title = "changed"
		if bs, _ := f.Context(context.Background(), q); bs[0].Title != blocks[0].Title {
			t.Errorf("a caller's change of an answer reached the fake")
		}
		touch := ext.TouchedFile{Session: ext.SessionRef{ID: "s1"}, Path: "/w/main.go", Op: "edit"}
		f.NoteTouch(context.Background(), touch)
		if got := f.Touches(); !reflect.DeepEqual(got, []ext.TouchedFile{touch}) {
			t.Errorf("Touches = %+v", got)
		}
		if got := f.Queries(); len(got) != 3 || !reflect.DeepEqual(got[0], q) {
			t.Errorf("Queries = %+v", got)
		}
		f.SetErr(errors.New("down"))
		if bs, err := f.Context(context.Background(), q); bs != nil || err == nil {
			t.Errorf("with an error set: %v, %v", bs, err)
		}
	})

	t.Run("FakeSink", func(t *testing.T) {
		if evs := NewFakeSink("all", nil).Events(); evs != nil {
			t.Errorf("a sink made with nil takes %q, not every entry", evs)
		}
		if evs := NewFakeSink("none", []string{}).Events(); evs == nil || len(evs) != 0 {
			t.Errorf("a sink made with an empty list takes %v, not nothing", evs)
		}
		f := NewFakeSink("fake-sink", []string{"needs_input"})
		evs := f.Events()
		evs[0] = "changed"
		if got := f.Events(); !reflect.DeepEqual(got, []string{"needs_input"}) {
			t.Errorf("Events = %q after a caller's change", got)
		}
		n1 := ext.Notification{Event: "needs_input", Message: "one"}
		if err := f.Notify(context.Background(), n1); err != nil {
			t.Fatal(err)
		}

		// Blocked: a Notify waits, recorded, until it is let go.
		f.Block()
		n2 := ext.Notification{Event: "needs_input", Message: "two"}
		done := make(chan error, 1)
		go func() { done <- f.Notify(context.Background(), n2) }()
		if err := Await(waitCtx(t), f, func() bool { return f.Waiting() == 1 }); err != nil {
			t.Fatalf("the blocked Notify never waited: %v", err)
		}
		if got := f.Notifications(); !reflect.DeepEqual(got, []ext.Notification{n1, n2}) {
			t.Errorf("Notifications = %+v", got)
		}
		f.Unblock()
		if err := <-done; err != nil {
			t.Errorf("a Notify let go answered %v", err)
		}

		// Blocked, its context ending: its error.
		f.Block()
		ctx, cancel := context.WithCancel(context.Background())
		go func() { done <- f.Notify(ctx, n2) }()
		if err := Await(waitCtx(t), f, func() bool { return f.Waiting() == 1 }); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("a blocked Notify whose context ended answered %v", err)
		}
		f.Unblock()
		boom := errors.New("push service down")
		f.SetErr(boom)
		if err := f.Notify(context.Background(), n1); !errors.Is(err, boom) {
			t.Errorf("with an error set: %v", err)
		}
	})

	t.Run("FakeContinuity", func(t *testing.T) {
		f := NewFakeContinuity("fake-sync")
		data := []byte(`{"a":1}`)
		d := ext.Doc{Store: "data", Path: "catalog.json", Rev: 1, Data: data}
		if err := f.Saved(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		data[0] = 'x'
		if err := f.Deleted(context.Background(), ext.Doc{Store: "data", Path: "crews/c.json"}); err != nil {
			t.Fatal(err)
		}
		docs := f.Docs()
		if len(docs) != 2 || docs[0].Deleted || string(docs[0].Doc.Data) != `{"a":1}` || !docs[1].Deleted || docs[1].Doc.Path != "crews/c.json" {
			t.Errorf("Docs = %+v", docs)
		}
		var w bytes.Buffer
		r := ext.RestoreRequest{Dir: t.TempDir()}
		if err := f.Restore(context.Background(), r, &w); !errors.Is(err, ext.ErrNothingToRestore) || w.Len() != 0 {
			t.Errorf("an unscripted Restore = %v, %d bytes; want ErrNothingToRestore, none", err, w.Len())
		}
		f.SetArchive([]byte{})
		if err := f.Restore(context.Background(), r, &w); err != nil {
			t.Errorf("an empty archive's Restore = %v; want nil", err)
		}
		f.SetArchive([]byte("PK archive"))
		if err := f.Restore(context.Background(), r, &w); err != nil || w.String() != "PK archive" {
			t.Errorf("Restore = %v, %q", err, w.String())
		}
		if got := f.Restores(); len(got) != 3 || got[0].Dir != r.Dir {
			t.Errorf("Restores = %+v", got)
		}
		boom := errors.New("down")
		f.SetErr(boom)
		if err := f.Saved(context.Background(), d); !errors.Is(err, boom) {
			t.Errorf("with an error set: %v", err)
		}
	})

	t.Run("FakeEntitlements", func(t *testing.T) {
		f := NewFakeEntitlements("fake-plans")
		if e, err := f.Entitled(context.Background(), "push"); err != nil || e.Allowed || e.Why != NotSetWhy {
			t.Errorf("an unset feature = %+v, %v", e, err)
		}
		want := ext.Entitlement{Allowed: true, Limit: 3, Why: "team plan"}
		f.Set("push", want)
		if e, _ := f.Entitled(context.Background(), "push"); e != want {
			t.Errorf("Entitled = %+v", e)
		}
		if got := f.Asked(); !reflect.DeepEqual(got, []string{"push", "push"}) {
			t.Errorf("Asked = %q", got)
		}
		f.SetErr(errors.New("down"))
		if _, err := f.Entitled(context.Background(), "push"); err == nil {
			t.Errorf("with an error set: nil")
		}
	})

	t.Run("FakeMedia", func(t *testing.T) {
		f := NewFakeMedia("fake-media")
		if ss, err := f.ICEServers(context.Background(), ext.ICERequest{Purpose: "viewer"}); ss != nil || err != nil {
			t.Errorf("an unscripted ICEServers = %v, %v", ss, err)
		}
		if _, ok, err := f.Room(context.Background(), ext.RoomRequest{Kind: "run"}); ok || err != nil {
			t.Errorf("an unscripted Room = %v, %v", ok, err)
		}
		servers := []ext.ICEServer{{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "c"}}
		f.SetServers(servers)
		ss, _ := f.ICEServers(context.Background(), ext.ICERequest{Purpose: "host", Session: "s1"})
		if !reflect.DeepEqual(ss, servers) {
			t.Errorf("ICEServers = %+v", ss)
		}
		ss[0].URLs[0] = "changed"
		if ss, _ := f.ICEServers(context.Background(), ext.ICERequest{}); ss[0].URLs[0] != "turn:turn.example.com:3478" {
			t.Errorf("a caller's change of an answer reached the fake")
		}
		f.SetRoom(ext.RoomJoin{URL: "https://rooms.example.com/r1"}, true)
		if j, ok, _ := f.Room(context.Background(), ext.RoomRequest{Kind: "run", ID: "r1", By: ada}); !ok || j.URL != "https://rooms.example.com/r1" {
			t.Errorf("Room = %+v, %v", j, ok)
		}
		if got := f.ICERequests(); len(got) != 3 || got[1].Purpose != "host" {
			t.Errorf("ICERequests = %+v", got)
		}
		if got := f.RoomRequests(); len(got) != 2 || got[1].ID != "r1" {
			t.Errorf("RoomRequests = %+v", got)
		}
		f.SetErr(errors.New("down"))
		if ss, err := f.ICEServers(context.Background(), ext.ICERequest{}); ss != nil || err == nil {
			t.Errorf("with an error set: %v, %v", ss, err)
		}
	})

	t.Run("FakeRuntime", func(t *testing.T) {
		f := NewFakeRuntime("fake-runtime", ext.WhereNative)
		if f.Name() != "fake-runtime" || f.Where() != ext.WhereNative {
			t.Errorf("Name %q, Where %q", f.Name(), f.Where())
		}
		f.SetWhere("boxcar")
		if f.Where() != "boxcar" {
			t.Errorf("Where %q after SetWhere", f.Where())
		}
	})

	t.Run("FakeHost", func(t *testing.T) {
		lt := &logT{T: t}
		h := NewFakeHost(lt)
		ctx := context.Background()
		run, err := h.Launch(ctx, ext.LaunchRequest{Crew: "web", Label: "nightly", By: ada})
		if err != nil || run.ID != "run-1" || run.Crew != "web" {
			t.Fatalf("Launch = %+v, %v", run, err)
		}
		if _, err := h.Launch(ctx, ext.LaunchRequest{Crew: "Not A Crew", By: ada}); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("an invalid launch answered %v, not ErrRefused", err)
		}
		if res, err := h.Submit(ctx, ext.SubmitRequest{Session: "s1", Text: "go on", By: ada}); err != nil || res.Status != "entered" {
			t.Errorf("Submit = %+v, %v", res, err)
		}
		if _, err := h.Submit(ctx, ext.SubmitRequest{Session: "s1", Input: "1", By: ada}); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("a choice with no question answered %v, not ErrRefused", err)
		}
		if err := h.Notify(ctx, ext.Notification{Message: "deployed"}); err != nil {
			t.Errorf("Notify = %v", err)
		}
		if err := h.Notify(ctx, ext.Notification{}); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("a notice with no message answered %v, not ErrRefused", err)
		}
		h.OnLaunch(func(r ext.LaunchRequest) (ext.RunRef, error) { return ext.RunRef{ID: "scripted", Crew: r.Crew}, nil })
		h.OnSubmit(func(ext.SubmitRequest) (ext.SubmitResult, error) { return ext.SubmitResult{Status: "pending"}, nil })
		h.OnNotify(func(ext.Notification) error { return ext.ErrUnsupported })
		if run, _ := h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada}); run.ID != "scripted" {
			t.Errorf("a scripted Launch = %+v", run)
		}
		if res, _ := h.Submit(ctx, ext.SubmitRequest{Session: "s1", Text: "x", By: ada}); res.Status != "pending" {
			t.Errorf("a scripted Submit = %+v", res)
		}
		if err := h.Notify(ctx, ext.Notification{Message: "m"}); !errors.Is(err, ext.ErrUnsupported) {
			t.Errorf("a scripted Notify = %v", err)
		}
		if n := len(h.Launches()); n != 3 {
			t.Errorf("%d launches recorded, want 3 (the refused one among them)", n)
		}
		if n := len(h.Submits()); n != 3 {
			t.Errorf("%d submits recorded, want 3", n)
		}
		if n := len(h.Notices()); n != 3 {
			t.Errorf("%d notices recorded, want 3", n)
		}
		dir, err := h.DataDir()
		if err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Errorf("DataDir %q: %v", dir, err)
		}
		if h.HostToken() != "" || h.Mode() != ext.ModeServer {
			t.Errorf("HostToken %q, Mode %q by default", h.HostToken(), h.Mode())
		}
		h.SetHostToken("code-1")
		h.SetMode(ext.ModeHost)
		if h.HostToken() != "code-1" || h.Mode() != ext.ModeHost {
			t.Errorf("HostToken %q, Mode %q once set", h.HostToken(), h.Mode())
		}
		h.Log().Info("provider started", "provider", "fake")
		if got := lt.logged(); len(got) != 1 || !strings.Contains(got[0], "provider started") {
			t.Errorf("the host's log reached the test's as %q", got)
		}

		// Blocked: a call waits until it is let go.
		h.Block()
		done := make(chan error, 1)
		go func() {
			_, err := h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada})
			done <- err
		}()
		if err := Await(waitCtx(t), h, func() bool { return h.Waiting() == 1 }); err != nil {
			t.Fatalf("the blocked Launch never waited: %v", err)
		}
		if n := len(h.Launches()); n != 3 {
			t.Errorf("a held Launch was recorded before it was let go")
		}
		h.Unblock()
		if err := <-done; err != nil {
			t.Errorf("a Launch let go answered %v", err)
		}

		// Blocked, its context ending: its error.
		h.Block()
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := h.Submit(cctx, ext.SubmitRequest{Session: "s1", Text: "x", By: ada})
			done <- err
		}()
		if err := Await(waitCtx(t), h, func() bool { return h.Waiting() == 1 }); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("a blocked Submit whose context ended answered %v", err)
		}

		// Blocked, then closed: context.Canceled, and so every call after.
		go func() { done <- h.Notify(ctx, ext.Notification{Message: "m"}) }()
		if err := Await(waitCtx(t), h, func() bool { return h.Waiting() == 1 }); err != nil {
			t.Fatal(err)
		}
		h.Close()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("a blocked Notify the host's Close let go answered %v", err)
		}
		h.Unblock()
		h.Close() // again: nothing
		if _, err := h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada}); !errors.Is(err, context.Canceled) {
			t.Errorf("a closed host's Launch answered %v", err)
		}
		if _, err := h.Submit(ctx, ext.SubmitRequest{Session: "s1", Text: "x", By: ada}); !errors.Is(err, context.Canceled) {
			t.Errorf("a closed host's Submit answered %v", err)
		}
		if err := h.Notify(ctx, ext.Notification{Message: "m"}); !errors.Is(err, context.Canceled) {
			t.Errorf("a closed host's Notify answered %v", err)
		}
		if _, err := h.DataDir(); !errors.Is(err, context.Canceled) {
			t.Errorf("a closed host's DataDir answered %v", err)
		}
		h.Log().Info("after the close")
		if got := lt.logged(); len(got) != 1 {
			t.Errorf("a closed host still logs into the test: %q", got)
		}
	})

	t.Run("accessors answer copies", func(t *testing.T) {
		// A change to what an accessor answered, nested slices too, reaches
		// nothing the fake holds.
		ctx := context.Background()
		rt := NewFakeRouter("r")
		rt.Route(ctx, SampleDecision())
		rt.Decisions()[0].Options[0].Label = "changed"
		cx := NewFakeContext("c")
		cx.Context(ctx, SampleQuery(t.TempDir()))
		cx.Queries()[0].Inputs[0].Task = "changed"
		cs := NewFakeContinuity("y")
		cs.Saved(ctx, ext.Doc{Store: "data", Path: "a.json", Data: []byte("{}")})
		cs.Docs()[0].Doc.Data[0] = 'X'
		md := NewFakeMedia("m")
		md.Room(ctx, ext.RoomRequest{Kind: "run", ID: "r1", By: ada})
		md.RoomRequests()[0].By.Grants[0] = ext.GrantHost
		h := NewFakeHost(t)
		h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada})
		h.Launches()[0].By.Grants[0] = ext.GrantHost
		h.Submit(ctx, ext.SubmitRequest{Session: "s1", Text: "x", By: ada})
		h.Submits()[0].By.Grants[0] = ext.GrantHost
		if got := rt.Decisions()[0].Options[0].Label; got != "Yes" {
			t.Errorf("Decisions' option changed to %q", got)
		}
		if got := cx.Queries()[0].Inputs[0].Task; got != "plan" {
			t.Errorf("Queries' input changed to %q", got)
		}
		if got := string(cs.Docs()[0].Doc.Data); got != "{}" {
			t.Errorf("Docs' data changed to %q", got)
		}
		if got := md.RoomRequests()[0].By.Grants[0]; got != ext.GrantAdmin {
			t.Errorf("RoomRequests' grant changed to %q", got)
		}
		if got := h.Launches()[0].By.Grants[0]; got != ext.GrantAdmin {
			t.Errorf("Launches' grant changed to %q", got)
		}
		if got := h.Submits()[0].By.Grants[0]; got != ext.GrantAdmin {
			t.Errorf("Submits' grant changed to %q", got)
		}
		if ada.Grants[0] != ext.GrantAdmin {
			t.Fatalf("the test's own principal changed")
		}
	})

	t.Run("callbacks hold copies of their own", func(t *testing.T) {
		// A callback that changes the request it is handed changes neither
		// the record nor the caller's, while the accessors read the record
		// from other goroutines (under -race).
		ctx := context.Background()
		h := NewFakeHost(t)
		h.OnLaunch(func(r ext.LaunchRequest) (ext.RunRef, error) {
			r.By.Grants[0] = ext.GrantHost
			return ext.RunRef{ID: "run-x", Crew: r.Crew}, nil
		})
		h.OnSubmit(func(r ext.SubmitRequest) (ext.SubmitResult, error) {
			r.By.Grants[0] = ext.GrantHost
			return ext.SubmitResult{Status: "entered"}, nil
		})
		together(func() {
			h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada})
			h.Submit(ctx, ext.SubmitRequest{Session: "s1", Text: "x", By: ada})
			h.Launches()
			h.Submits()
		})
		for i, r := range h.Launches() {
			if got := r.By.Grants[0]; got != ext.GrantAdmin {
				t.Fatalf("Launch %d's record holds the grant %q its callback set", i, got)
			}
		}
		for i, r := range h.Submits() {
			if got := r.By.Grants[0]; got != ext.GrantAdmin {
				t.Fatalf("Submit %d's record holds the grant %q its callback set", i, got)
			}
		}
		if ada.Grants[0] != ext.GrantAdmin {
			t.Fatalf("the caller's principal changed")
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		// Every fake from Goroutines goroutines at once, under -race.
		id, rt, cx, sk := NewFakeIdentity("i"), NewFakeRouter("r"), NewFakeContext("c"), NewFakeSink("s", nil)
		cs, en, md, ru := NewFakeContinuity("y"), NewFakeEntitlements("e"), NewFakeMedia("m"), NewFakeRuntime("u", ext.WhereNative)
		h := NewFakeHost(t)
		ctx := context.Background()
		together(func() {
			id.Add("tok", ada)
			id.Authenticate(ctx, "tok")
			rt.SetRoute("q", ext.Route{})
			rt.Route(ctx, SampleDecision())
			rt.Resolved(ctx, ext.Resolution{})
			cx.NoteTouch(ctx, ext.TouchedFile{})
			cx.Context(ctx, ext.ContextQuery{})
			sk.Notify(ctx, ext.Notification{})
			cs.Saved(ctx, ext.Doc{})
			cs.SetArchive([]byte("x"))
			cs.Restore(ctx, ext.RestoreRequest{}, &bytes.Buffer{})
			en.Entitled(ctx, "f")
			md.ICEServers(ctx, ext.ICERequest{})
			ru.SetWhere(ext.WhereNative)
			ru.Where()
			h.Launch(ctx, ext.LaunchRequest{Crew: "web", By: ada})
			h.Submit(ctx, ext.SubmitRequest{Session: "s", Text: "x", By: ada})
			h.Notify(ctx, ext.Notification{Message: "m"})
		})
		if id.Calls() != Goroutines || len(h.Launches()) != Goroutines || len(sk.Notifications()) != Goroutines {
			t.Errorf("calls lost: %d, %d, %d", id.Calls(), len(h.Launches()), len(sk.Notifications()))
		}
	})
}
