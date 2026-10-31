package ext_test

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// fake implements every interface of ext, as one value: a pointer, so two
// registrations of it are one value.
type fake struct{ name string }

func (f *fake) Name() string { return f.name }
func (f *fake) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (f *fake) Route(context.Context, ext.Decision) (ext.Route, error) { return ext.Route{}, nil }
func (f *fake) Resolved(context.Context, ext.Resolution)               {}
func (f *fake) NoteTouch(context.Context, ext.TouchedFile)             {}
func (f *fake) Context(context.Context, ext.ContextQuery) ([]ext.ContextBlock, error) {
	return nil, nil
}
func (f *fake) Events() []string                               { return nil }
func (f *fake) Notify(context.Context, ext.Notification) error { return nil }
func (f *fake) Saved(context.Context, ext.Doc) error           { return nil }
func (f *fake) Deleted(context.Context, ext.Doc) error         { return nil }
func (f *fake) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}
func (f *fake) Restore(context.Context, ext.RestoreRequest, io.Writer) error {
	return ext.ErrNothingToRestore
}
func (f *fake) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return nil, nil
}
func (f *fake) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, nil
}
func (f *fake) Where() ext.Where { return ext.WhereNative }

// incomparable is a provider value whose dynamic type == cannot compare (it
// holds a slice): never the same value as anything, itself included.
type incomparable struct {
	name string
	tags []string
}

func (i incomparable) Name() string { return i.name }
func (i incomparable) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (i incomparable) Route(context.Context, ext.Decision) (ext.Route, error) {
	return ext.Route{}, nil
}
func (i incomparable) Resolved(context.Context, ext.Resolution) {}

// nilSafe is a provider whose methods never read their receiver: a nil
// *nilSafe answers a name, and is still no provider.
type nilSafe struct{}

func (*nilSafe) Name() string     { return "constant" }
func (*nilSafe) Where() ext.Where { return ext.WhereNative }

// registers is each Register function by its kind, for a fake.
var registers = []struct {
	kind string
	reg  func(*fake)
	list bool
}{
	{ext.KindIdentity, func(f *fake) { ext.RegisterIdentity(f) }, false},
	{ext.KindDecisions, func(f *fake) { ext.RegisterRouter(f) }, false},
	{ext.KindContext, func(f *fake) { ext.RegisterContextProvider(f) }, true},
	{ext.KindNotifications, func(f *fake) { ext.RegisterNotificationSink(f) }, true},
	{ext.KindSync, func(f *fake) { ext.RegisterContinuity(f) }, false},
	{ext.KindEntitlements, func(f *fake) { ext.RegisterEntitlements(f) }, false},
	{ext.KindMedia, func(f *fake) { ext.RegisterMedia(f) }, false},
	{ext.KindRuntime, func(f *fake) { ext.RegisterRuntime(f) }, false},
}

// nilRegisters registers a nil interface of each kind.
var nilRegisters = map[string]func(){
	ext.KindIdentity:      func() { ext.RegisterIdentity(nil) },
	ext.KindDecisions:     func() { ext.RegisterRouter(nil) },
	ext.KindContext:       func() { ext.RegisterContextProvider(nil) },
	ext.KindNotifications: func() { ext.RegisterNotificationSink(nil) },
	ext.KindSync:          func() { ext.RegisterContinuity(nil) },
	ext.KindEntitlements:  func() { ext.RegisterEntitlements(nil) },
	ext.KindMedia:         func() { ext.RegisterMedia(nil) },
	ext.KindRuntime:       func() { ext.RegisterRuntime(nil) },
}

// fresh empties the registry for one test and again after it.
func fresh(t *testing.T) {
	t.Helper()
	ext.ResetRegistry()
	t.Cleanup(ext.ResetRegistry)
}

// panics runs fn and answers its panic's words, or fails when it does not
// panic.
func panics(t *testing.T, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("no panic")
		}
		msg = fmt.Sprint(r)
	}()
	fn()
	return ""
}

// refuses wants fn to panic naming the slot kind, and the registry to hold
// what it held before.
func refuses(t *testing.T, kind string, fn func()) {
	t.Helper()
	before := ext.Registered()
	msg := panics(t, fn)
	if !strings.HasPrefix(msg, "ext: register "+kind) {
		t.Fatalf("panic %q does not name the slot %q", msg, kind)
	}
	if after := ext.Registered(); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused register changed the registry: %v, then %v", before, after)
	}
}

func TestRegisterRefuses(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		fresh(t)
		for kind, fn := range nilRegisters {
			refuses(t, kind, fn)
		}
	})
	t.Run("typed nil", func(t *testing.T) {
		fresh(t)
		for _, r := range registers {
			refuses(t, r.kind, func() { r.reg((*fake)(nil)) }) // a Name that reads its receiver
		}
		refuses(t, ext.KindIdentity, func() { ext.RegisterIdentity((*incomparable)(nil)) }) // a Name of a value receiver
		refuses(t, ext.KindRuntime, func() { ext.RegisterRuntime((*nilSafe)(nil)) })        // a Name that answers from a nil pointer
	})
	t.Run("bad name", func(t *testing.T) {
		for _, bad := range []string{"", "Cloud", "-cloud", "cloud_push", "cloud push", "clöud", strings.Repeat("a", ext.MaxProviderName+1)} {
			for _, r := range registers {
				fresh(t)
				refuses(t, r.kind, func() { r.reg(&fake{name: bad}) })
			}
		}
		fresh(t)
		ext.RegisterIdentity(&fake{name: strings.Repeat("a", ext.MaxProviderName)}) // the bound itself is taken
		ext.RegisterRouter(&fake{name: "9-lives"})                                  // a digit may begin it
	})
	t.Run("a second in a one-value slot", func(t *testing.T) {
		for _, r := range registers {
			if r.list {
				continue
			}
			fresh(t)
			r.reg(&fake{name: "first"})
			refuses(t, r.kind, func() { r.reg(&fake{name: "second"}) })
			first := &fake{name: "again"}
			fresh(t)
			r.reg(first)
			refuses(t, r.kind, func() { r.reg(first) }) // the same value twice in one slot too
		}
	})
	t.Run("a repeated name in a list", func(t *testing.T) {
		for _, r := range registers {
			if !r.list {
				continue
			}
			fresh(t)
			f := &fake{name: "push"}
			r.reg(f)
			refuses(t, r.kind, func() { r.reg(f) })
			refuses(t, r.kind, func() { r.reg(&fake{name: "push"}) })
		}
	})
	t.Run("a list past its bound", func(t *testing.T) {
		for _, r := range registers {
			if !r.list {
				continue
			}
			fresh(t)
			for i := range ext.MaxPerList {
				r.reg(&fake{name: fmt.Sprintf("p%d", i)})
			}
			refuses(t, r.kind, func() { r.reg(&fake{name: "one-more"}) })
		}
	})
	t.Run("a name another slot holds", func(t *testing.T) {
		fresh(t)
		ext.RegisterIdentity(&fake{name: "cloud"})
		refuses(t, ext.KindDecisions, func() { ext.RegisterRouter(&fake{name: "cloud"}) })
		refuses(t, ext.KindNotifications, func() { ext.RegisterNotificationSink(&fake{name: "cloud"}) })

		fresh(t)
		v := incomparable{name: "tagged", tags: []string{"a"}}
		ext.RegisterIdentity(v) // an incomparable value registers once
		refuses(t, ext.KindDecisions, func() { ext.RegisterRouter(v) })
	})
	t.Run("after Freeze", func(t *testing.T) {
		fresh(t)
		ext.Freeze()
		for _, r := range registers {
			refuses(t, r.kind, func() { r.reg(&fake{name: "late"}) })
		}
	})
	t.Run("one value in several slots", func(t *testing.T) {
		fresh(t)
		f := &fake{name: "premium"}
		for _, r := range registers {
			r.reg(f)
		}
		if got := len(ext.Registered()); got != len(registers) {
			t.Fatalf("%d registrations, want %d", got, len(registers))
		}
		p := ext.Freeze()
		if p.Identity != f || p.Router != f || p.Continuity != f || p.Media != f || p.Runtime != f ||
			len(p.Context) != 1 || p.Context[0] != f || len(p.Notifications) != 1 || p.Notifications[0] != f {
			t.Fatalf("the value is not in every slot: %+v", p)
		}
	})
}

func TestRegisteredOrder(t *testing.T) {
	fresh(t)
	// Registered out of the kinds' order, as inits of different packages
	// run in an order no provider controls.
	ext.RegisterRuntime(&fake{name: "vm"})
	ext.RegisterNotificationSink(&fake{name: "push"})
	ext.RegisterMedia(&fake{name: "turn"})
	ext.RegisterContextProvider(&fake{name: "graph-b"})
	ext.RegisterEntitlements(&fake{name: "billing"})
	ext.RegisterNotificationSink(&fake{name: "mail"})
	ext.RegisterContinuity(&fake{name: "sync"})
	ext.RegisterContextProvider(&fake{name: "graph-a"})
	ext.RegisterRouter(&fake{name: "switchman"})
	ext.RegisterIdentity(&fake{name: "cloud"})

	want := []ext.Registration{
		{Kind: ext.KindIdentity, Name: "cloud"},
		{Kind: ext.KindDecisions, Name: "switchman"},
		{Kind: ext.KindContext, Name: "graph-b"},
		{Kind: ext.KindContext, Name: "graph-a"},
		{Kind: ext.KindNotifications, Name: "push"},
		{Kind: ext.KindNotifications, Name: "mail"},
		{Kind: ext.KindSync, Name: "sync"},
		{Kind: ext.KindEntitlements, Name: "billing"},
		{Kind: ext.KindMedia, Name: "turn"},
		{Kind: ext.KindRuntime, Name: "vm"},
	}
	if got := ext.Registered(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Registered() =\n%v\nwant\n%v", got, want)
	}
	var names []string
	for _, p := range ext.ContextProviders() {
		names = append(names, p.Name())
	}
	for _, s := range ext.NotificationSinks() {
		names = append(names, s.Name())
	}
	if got := strings.Join(names, ","); got != "graph-b,graph-a,push,mail" {
		t.Fatalf("the lists = %s, want them in the order of registration", got)
	}
	var all []ext.Registration
	for _, e := range ext.Freeze().All {
		all = append(all, ext.Registration{Kind: e.Kind, Name: e.Name})
		if e.Value.Name() != e.Name {
			t.Fatalf("entry %s %s holds %q", e.Kind, e.Name, e.Value.Name())
		}
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("Freeze().All =\n%v\nwant Registered's order\n%v", all, want)
	}
}

func TestFreezeOnceAndCopies(t *testing.T) {
	fresh(t)
	id := &fake{name: "cloud"}
	ext.RegisterIdentity(id)
	ext.RegisterContextProvider(&fake{name: "graph"})
	ext.RegisterNotificationSink(&fake{name: "push"})

	first := ext.Freeze()
	// What a caller does to what it was given never reaches the registry.
	first.Context[0] = &fake{name: "changed"}
	first.Notifications[0] = &fake{name: "changed"}
	first.All[0].Name = "changed"
	first.Identity = nil
	ext.ContextProviders()[0] = &fake{name: "changed"}
	ext.NotificationSinks()[0] = &fake{name: "changed"}
	ext.Registered()[0].Name = "changed"

	second := ext.Freeze()
	if second.Identity != id || second.Context[0].Name() != "graph" || len(second.Notifications) != 1 ||
		second.Notifications[0].Name() != "push" || second.All[0].Name != "cloud" || len(second.All) != 3 {
		t.Fatalf("the second Freeze answers %+v, not what the registry held", second)
	}
	if got := ext.ContextProviders()[0].Name(); got != "graph" {
		t.Fatalf("ContextProviders()[0] = %q after a caller changed its copy", got)
	}
	if got := ext.NotificationSinks()[0].Name(); got != "push" {
		t.Fatalf("NotificationSinks()[0] = %q after a caller changed its copy", got)
	}
	if got := ext.Registered()[0].Name; got != "cloud" {
		t.Fatalf("Registered()[0] = %q after a caller changed its copy", got)
	}
	if got, ok := ext.Identity(); !ok || got != id {
		t.Fatalf("Identity() = %v, %v after Freeze", got, ok)
	}
	refuses(t, ext.KindMedia, func() { ext.RegisterMedia(&fake{name: "turn"}) })
	if third := ext.Freeze(); len(third.All) != 3 || third.Media != nil {
		t.Fatalf("a refused register after Freeze reached it: %+v", third)
	}

	// An empty registry freezes to nothing at all.
	fresh(t)
	if p := ext.Freeze(); !reflect.DeepEqual(p, ext.Providers{}) {
		t.Fatalf("Freeze() of an empty registry = %+v", p)
	}
}

func TestAllListsEverySlot(t *testing.T) {
	// A provider registered only as the entitlement source has no typed
	// field in Providers, and is in All and Registered all the same.
	fresh(t)
	billing := &fake{name: "billing"}
	ext.RegisterEntitlements(billing)
	want := []ext.Entry{{Kind: ext.KindEntitlements, Name: "billing", Value: billing}}
	if got := ext.Registered(); !reflect.DeepEqual(got, []ext.Registration{{Kind: ext.KindEntitlements, Name: "billing"}}) {
		t.Fatalf("Registered() = %v", got)
	}
	if got, ok := ext.Entitlements(); !ok || got != billing {
		t.Fatalf("Entitlements() = %v, %v", got, ok)
	}
	p := ext.Freeze()
	if !reflect.DeepEqual(p.All, want) {
		t.Fatalf("Freeze().All = %v, want %v", p.All, want)
	}
	if _, ok := p.All[0].Value.(ext.EntitlementSource); !ok {
		t.Fatal("the entry's value is not the entitlement source")
	}

	// Every slot filled: each is in All once, in the kinds' order.
	fresh(t)
	for i, r := range registers {
		r.reg(&fake{name: fmt.Sprintf("p%d", i)})
	}
	p = ext.Freeze()
	if len(p.All) != len(registers) {
		t.Fatalf("All has %d entries, want %d", len(p.All), len(registers))
	}
	for i, e := range p.All {
		if e.Kind != registers[i].kind || e.Name != fmt.Sprintf("p%d", i) {
			t.Fatalf("All[%d] = %s %s, want %s p%d", i, e.Kind, e.Name, registers[i].kind, i)
		}
	}
}

func TestRegistryConcurrent(t *testing.T) {
	fresh(t)
	const goroutines = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range goroutines {
		wg.Go(func() {
			<-start
			f := &fake{name: fmt.Sprintf("p%d", i)}
			if i%2 == 0 {
				ext.RegisterContextProvider(f)
			} else {
				ext.RegisterNotificationSink(f)
			}
			// Reads beside the writes, for the race detector.
			_ = ext.Registered()
			_ = ext.ContextProviders()
			_ = ext.NotificationSinks()
			_, _ = ext.Identity()
		})
	}
	close(start)
	wg.Wait()
	if c, n := len(ext.ContextProviders()), len(ext.NotificationSinks()); c != goroutines/2 || n != goroutines/2 {
		t.Fatalf("%d context providers and %d sinks, want %d each", c, n, goroutines/2)
	}

	// Freeze from every goroutine at once: one answer, whoever is first.
	answers := make([]ext.Providers, goroutines)
	for i := range goroutines {
		wg.Go(func() { answers[i] = ext.Freeze() })
	}
	wg.Wait()
	for i := range answers {
		if !reflect.DeepEqual(answers[i], answers[0]) {
			t.Fatalf("Freeze answered differently in goroutine %d", i)
		}
	}
	if len(answers[0].All) != goroutines {
		t.Fatalf("Freeze().All has %d entries, want %d", len(answers[0].All), goroutines)
	}
}
