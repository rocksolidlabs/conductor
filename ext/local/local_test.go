package local_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// callers is how many goroutines call each default at once.
const callers = 32

// TestDefaults calls every default from 32 goroutines at once, under -race,
// and wants main's answer from each: no principal, the empty route, nothing
// kept, ErrNothingToRestore, allowed, no ICE server, no room.
func TestDefaults(t *testing.T) {
	ctx := context.Background()
	decision := ext.Decision{
		ID:      "q1",
		Session: ext.SessionRef{ID: "s1"},
		Message: "Allow this?",
		Options: []ext.DecisionOption{{Label: "Yes", Input: "1"}, {Label: "No", Input: "2"}},
		Since:   time.Unix(1_800_000_000, 0),
	}
	doc := ext.Doc{Store: "data", Path: "catalog.json", Epoch: "0123456789abcdef0123456789abcdef", Rev: 1, Data: []byte("{}")}
	providers := []ext.Provider{local.Identity{}, local.Router{}, local.Continuity{}, local.AllowAll{}, local.Media{}}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			for _, p := range providers {
				if got := p.Name(); got != local.Name || !ext.ValidName(got) {
					t.Errorf("%T.Name() = %q, want %q", p, got, local.Name)
				}
			}

			// Identity: no token is the provider's, the workbench token's
			// shape and a cloud token's alike.
			for _, tok := range []string{"", "wb-0123456789", "eyJhbGciOiJFZERTQSJ9.e30.sig"} {
				p, ok, err := local.Identity{}.Authenticate(ctx, tok)
				if ok || err != nil || !reflect.DeepEqual(p, ext.Principal{}) {
					t.Errorf("Identity.Authenticate(%q) = %+v, %v, %v; want no principal", tok, p, ok, err)
				}
			}

			// Router: the empty route, valid for its decision, and nothing
			// it is told changes the next.
			r, err := local.Router{}.Route(ctx, decision)
			if err != nil || !reflect.DeepEqual(r, ext.Route{}) {
				t.Errorf("Router.Route = %+v, %v; want the empty route", r, err)
			}
			if err := r.Valid(decision); err != nil {
				t.Errorf("the empty route is not valid for its decision: %v", err)
			}
			local.Router{}.Resolved(ctx, ext.Resolution{ID: decision.ID, Session: decision.Session, At: decision.Since})
			if r, _ := (local.Router{}).Route(ctx, decision); !reflect.DeepEqual(r, ext.Route{}) {
				t.Errorf("Router.Route after Resolved = %+v; want the empty route", r)
			}

			// Continuity: everything acknowledged, nothing kept, so nothing
			// to restore and nothing written.
			c := local.Continuity{}
			if err := c.Saved(ctx, doc); err != nil {
				t.Errorf("Continuity.Saved = %v; want nil", err)
			}
			if err := c.Deleted(ctx, doc); err != nil {
				t.Errorf("Continuity.Deleted = %v; want nil", err)
			}
			if err := c.Saved(ctx, doc); err != nil {
				t.Errorf("Continuity.Saved again = %v; want nil", err)
			}
			var w bytes.Buffer
			if err := c.Restore(ctx, ext.RestoreRequest{Dir: t.TempDir()}, &w); !errors.Is(err, ext.ErrNothingToRestore) {
				t.Errorf("Continuity.Restore = %v; want ErrNothingToRestore", err)
			}
			if w.Len() != 0 {
				t.Errorf("Continuity.Restore wrote %d bytes; want none", w.Len())
			}

			// Entitlements: every feature allowed, no limit, no end.
			for _, feature := range []string{"", "push", "sync", "seats"} {
				e, err := local.AllowAll{}.Entitled(ctx, feature)
				want := ext.Entitlement{Allowed: true, Why: "allowed: no entitlement source is registered"}
				if err != nil || !reflect.DeepEqual(e, want) {
					t.Errorf("AllowAll.Entitled(%q) = %+v, %v; want %+v", feature, e, err, want)
				}
			}

			// Media: no ICE server beside the configured ones, no room.
			for _, purpose := range []string{"viewer", "host", "paste"} {
				servers, err := local.Media{}.ICEServers(ctx, ext.ICERequest{Purpose: purpose, Session: "s1"})
				if err != nil || len(servers) != 0 {
					t.Errorf("Media.ICEServers(%s) = %v, %v; want none", purpose, servers, err)
				}
			}
			join, ok, err := local.Media{}.Room(ctx, ext.RoomRequest{Kind: "run", ID: "r1"})
			if ok || err != nil || !reflect.DeepEqual(join, ext.RoomJoin{}) {
				t.Errorf("Media.Room = %+v, %v, %v; want no room", join, ok, err)
			}
		}()
	}
	close(start)
	wg.Wait()
}

// fake is a registered provider in every slot Fill fills, and a list
// member: a pointer, so the test can tell it is the same value.
type fake struct{ name string }

func (f *fake) Name() string { return f.name }
func (f *fake) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{Subject: "u1", Name: "U", Grants: []ext.Grant{ext.GrantAdmin}}, true, nil
}
func (f *fake) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{Rule: "fake"}, nil
}
func (f *fake) Resolved(context.Context, ext.Resolution)   {}
func (f *fake) NoteTouch(context.Context, ext.TouchedFile) {}
func (f *fake) Context(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	return nil, nil
}
func (f *fake) Events() []string                               { return nil }
func (f *fake) Notify(context.Context, ext.Notification) error { return nil }
func (f *fake) Saved(context.Context, ext.Doc) error           { return nil }
func (f *fake) Deleted(context.Context, ext.Doc) error         { return nil }
func (f *fake) Restore(context.Context, ext.RestoreRequest, io.Writer) error {
	return nil
}
func (f *fake) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return []ext.ICEServer{{URLs: []string{"stun:stun.example.org:3478"}}}, nil
}
func (f *fake) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{URL: "https://room.example.org/r1"}, true, nil
}
func (f *fake) Where() ext.Where { return "fake" }

// TestFill wants every empty slot to get its default, and a registered
// provider, the lists, the runtime and All kept as they are.
func TestFill(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := local.Fill(ext.Providers{})
		want := ext.Providers{
			Identity:   local.Identity{},
			Router:     local.Router{},
			Continuity: local.Continuity{},
			Media:      local.Media{},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Fill(empty) = %#v\nwant %#v", got, want)
		}
		if got.Context != nil || got.Notifications != nil || got.Runtime != nil || got.All != nil {
			t.Errorf("Fill(empty) put something in a list, the runtime or All: %#v", got)
		}
		if again := local.Fill(got); !reflect.DeepEqual(again, got) {
			t.Errorf("Fill(Fill(empty)) = %#v; want what Fill answered", again)
		}
	})

	t.Run("registered", func(t *testing.T) {
		f := &fake{name: "cloud"}
		c1, c2 := &fake{name: "ctx-a"}, &fake{name: "ctx-b"}
		s1 := &fake{name: "push"}
		all := []ext.Entry{
			{Kind: ext.KindIdentity, Name: f.name, Value: f},
			{Kind: ext.KindDecisions, Name: f.name, Value: f},
			{Kind: ext.KindContext, Name: c1.name, Value: c1},
			{Kind: ext.KindContext, Name: c2.name, Value: c2},
			{Kind: ext.KindNotifications, Name: s1.name, Value: s1},
			{Kind: ext.KindSync, Name: f.name, Value: f},
			{Kind: ext.KindEntitlements, Name: f.name, Value: f},
			{Kind: ext.KindMedia, Name: f.name, Value: f},
			{Kind: ext.KindRuntime, Name: f.name, Value: f},
		}
		in := ext.Providers{
			Identity:      f,
			Router:        f,
			Context:       []ext.ContextProvider{c1, c2},
			Notifications: []ext.NotificationSink{s1},
			Continuity:    f,
			Media:         f,
			Runtime:       f,
			All:           all,
		}
		got := local.Fill(in)
		if got.Identity != ext.IdentityProvider(f) || got.Router != ext.DecisionRouter(f) ||
			got.Continuity != ext.ContinuityStore(f) || got.Media != ext.MediaRoomProvider(f) ||
			got.Runtime != ext.Runtime(f) {
			t.Errorf("Fill replaced a registered provider: %#v", got)
		}
		if len(got.Context) != 2 || got.Context[0] != ext.ContextProvider(c1) || got.Context[1] != ext.ContextProvider(c2) {
			t.Errorf("Fill changed the context providers: %#v", got.Context)
		}
		if len(got.Notifications) != 1 || got.Notifications[0] != ext.NotificationSink(s1) {
			t.Errorf("Fill changed the notification sinks: %#v", got.Notifications)
		}
		if !reflect.DeepEqual(got.All, all) {
			t.Errorf("Fill changed All: %#v", got.All)
		}
	})

	t.Run("some", func(t *testing.T) {
		f := &fake{name: "turn"}
		in := ext.Providers{
			Media: f,
			All:   []ext.Entry{{Kind: ext.KindMedia, Name: f.name, Value: f}},
		}
		got := local.Fill(in)
		if got.Media != ext.MediaRoomProvider(f) {
			t.Errorf("Fill replaced the registered media provider with %#v", got.Media)
		}
		if got.Identity != (local.Identity{}) || got.Router != (local.Router{}) || got.Continuity != (local.Continuity{}) {
			t.Errorf("Fill left an empty slot without its default: %#v", got)
		}
		if len(got.All) != 1 || got.All[0].Value != ext.Provider(f) {
			t.Errorf("Fill changed All, or listed a default in it: %#v", got.All)
		}
		if in.Identity != nil || in.Router != nil || in.Continuity != nil {
			t.Errorf("Fill changed its argument: %#v", in)
		}
	})
}
