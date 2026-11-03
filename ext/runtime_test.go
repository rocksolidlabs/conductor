package ext_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// whereRuntime is a fake whose Where is set.
type whereRuntime struct {
	*fake
	where ext.Where
}

func (w whereRuntime) Where() ext.Where { return w.where }

// RegisterRuntime refuses a Where that Where.Valid refuses, and the native
// one, which only the free default reports, each panicking with the slot's
// name and leaving the registry as it was; a valid Where of a provider's
// own registers.
func TestRegisterRuntimeRefusesItsWhere(t *testing.T) {
	for _, bad := range []ext.Where{"", "VM", "9vm", "-vm", "vm_1", "vm 1", ext.Where(strings.Repeat("a", ext.MaxWhere+1)), ext.WhereNative} {
		fresh(t)
		msg := panics(t, func() { ext.RegisterRuntime(whereRuntime{&fake{name: "vm"}, bad}) })
		if !strings.HasPrefix(msg, "ext: register "+ext.KindRuntime) || !strings.Contains(msg, "where") {
			t.Errorf("Where %q: the panic %q does not name the slot and its where", bad, msg)
		}
		if got := ext.Registered(); len(got) != 0 {
			t.Errorf("Where %q: a refused register changed the registry: %v", bad, got)
		}
		if _, ok := ext.RuntimeProvider(); ok {
			t.Errorf("Where %q: a runtime is registered", bad)
		}
	}
	if msg := panics(t, func() { ext.RegisterRuntime(whereRuntime{&fake{name: "vm"}, ext.WhereNative}) }); !strings.Contains(msg, "only the free default") {
		t.Errorf("the native Where's refusal: %q", msg)
	}
	fresh(t)
	ext.RegisterRuntime(whereRuntime{&fake{name: "vm"}, ext.Where(strings.Repeat("a", ext.MaxWhere))})
	if r, ok := ext.RuntimeProvider(); !ok || r.Name() != "vm" {
		t.Fatalf("a runtime with a valid Where did not register: %v %v", r, ok)
	}
}

// starterRuntime is a Runtime that needs the server: it is a Starter too.
type starterRuntime struct {
	*fake
	where ext.Where
}

func (s starterRuntime) Where() ext.Where                      { return s.where }
func (s starterRuntime) Start(context.Context, ext.Host) error { return nil }

var (
	_ ext.Runtime = starterRuntime{}
	_ ext.Starter = starterRuntime{}
)

// One type with StartTerminal and Start(ctx, Host) is both a Runtime and a
// Starter (the terminal's launch is not named Start for this), and
// registers as a Runtime, whose value the registry answers as a Starter
// too.
func TestARuntimeMayBeAStarter(t *testing.T) {
	fresh(t)
	r := starterRuntime{&fake{name: "boxcar"}, "vm"}
	ext.RegisterRuntime(r)
	got, ok := ext.RuntimeProvider()
	if !ok || got.Name() != "boxcar" || got.Where() != "vm" {
		t.Fatalf("RuntimeProvider = %v %v", got, ok)
	}
	if _, ok := got.(ext.Starter); !ok {
		t.Fatalf("the registered runtime is no Starter: %T", got)
	}
	p := ext.Freeze()
	if p.Runtime == nil || len(p.All) != 1 || p.All[0].Kind != ext.KindRuntime {
		t.Fatalf("Freeze: %+v", p)
	}
	if _, ok := p.All[0].Value.(ext.Starter); !ok {
		t.Fatalf("All's entry is no Starter: %T", p.All[0].Value)
	}
}
