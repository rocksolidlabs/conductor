package ext

import (
	"fmt"
	"reflect"
	"sync"
)

// Provider is what every interface embeds: the name the logs, the guard's
// notes and GET /api/capabilities give the provider: 1 to MaxProviderName of
// a-z, 0-9 and '-', beginning with a letter or a digit (ValidName).
type Provider interface{ Name() string }

// MaxProviderName bounds a provider's name, in bytes.
const MaxProviderName = 40

// MaxPerList bounds the providers of a list slot (context providers,
// notification sinks): it bounds the capabilities reply and the event hub's
// fan-out.
const MaxPerList = 16

// The kinds, as Registration and GET /api/capabilities name them, in the
// order Registered lists them.
const (
	KindIdentity      = "identity"
	KindDecisions     = "decisions"
	KindContext       = "context"
	KindNotifications = "notifications"
	KindSync          = "sync" // ContinuityStore: "continuity" is a run continued after a restart
	KindEntitlements  = "entitlements"
	KindMedia         = "media"
	KindRuntime       = "runtime"
)

// ValidName reports whether name can be a provider's: 1 to MaxProviderName
// of a-z, 0-9 and '-', beginning with a letter or a digit.
func ValidName(name string) bool {
	return slug(name, MaxProviderName, false)
}

// Registration is one registered provider: its kind and its name.
type Registration struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Providers is what the registry holds, as the composition root reads it.
// The entitlement source has no typed field: no code of this repository
// asks it. It is in All, as every registered provider is.
type Providers struct {
	Identity      IdentityProvider
	Router        DecisionRouter
	Context       []ContextProvider
	Notifications []NotificationSink
	Continuity    ContinuityStore
	Media         MediaRoomProvider
	Runtime       Runtime
	// All is every registered provider, the entitlement source's slot
	// among them, in Registered's order: what the server starts and GET
	// /api/capabilities lists. A value is typed only as a Provider, so a
	// caller learns its kind and name, and whether it is a Starter, and
	// nothing it could gate a feature on.
	All []Entry
}

// Entry is one registered provider in one slot.
type Entry struct {
	Kind  string
	Name  string
	Value Provider
}

// kind is a slot of the registry, in the order of the kinds above.
type kind int

const (
	kindIdentity kind = iota
	kindDecisions
	kindContext
	kindNotifications
	kindSync
	kindEntitlements
	kindMedia
	kindRuntime
	kindCount
)

var kindNames = [kindCount]string{
	KindIdentity, KindDecisions, KindContext, KindNotifications,
	KindSync, KindEntitlements, KindMedia, KindRuntime,
}

// isList reports whether k holds a list rather than one value.
func (k kind) isList() bool { return k == kindContext || k == kindNotifications }

// registry is the process's one registry. Register functions run in init,
// before main; the composition root reads it once through Freeze.
type registry struct {
	mu     sync.Mutex
	frozen bool
	held   Providers // what Freeze answered, once frozen
	slots  [kindCount][]Entry
	names  map[string]Provider
}

var reg = &registry{}

// RegisterIdentity registers the identity provider. One value; it panics on
// a nil provider (a nil interface, or one holding a nil pointer), a bad
// name, a second identity provider, a name another slot holds for a
// different value, and after Freeze.
func RegisterIdentity(p IdentityProvider) { register(kindIdentity, p) }

// RegisterRouter registers the decision router; one value, as
// RegisterIdentity.
func RegisterRouter(r DecisionRouter) { register(kindDecisions, r) }

// RegisterContinuity registers the sync store; one value, as
// RegisterIdentity.
func RegisterContinuity(c ContinuityStore) { register(kindSync, c) }

// RegisterEntitlements registers the entitlement source; one value, as
// RegisterIdentity.
func RegisterEntitlements(e EntitlementSource) { register(kindEntitlements, e) }

// RegisterMedia registers the media room provider; one value, as
// RegisterIdentity.
func RegisterMedia(m MediaRoomProvider) { register(kindMedia, m) }

// RegisterRuntime registers the runtime; one value, as RegisterIdentity.
func RegisterRuntime(r Runtime) { register(kindRuntime, r) }

// RegisterContextProvider adds a context provider to its list, after those
// registered before it. It panics on a nil provider, a bad name, a name the
// list already holds, a list of MaxPerList already, a name another slot
// holds for a different value, and after Freeze.
func RegisterContextProvider(p ContextProvider) { register(kindContext, p) }

// RegisterNotificationSink adds a notification sink to its list, as
// RegisterContextProvider.
func RegisterNotificationSink(s NotificationSink) { register(kindNotifications, s) }

// Identity answers the registered identity provider; ok false: none.
func Identity() (IdentityProvider, bool) {
	v, ok := one(kindIdentity)
	if !ok {
		return nil, false
	}
	return v.(IdentityProvider), true
}

// Router answers the registered decision router; ok false: none.
func Router() (DecisionRouter, bool) {
	v, ok := one(kindDecisions)
	if !ok {
		return nil, false
	}
	return v.(DecisionRouter), true
}

// Continuity answers the registered sync store; ok false: none.
func Continuity() (ContinuityStore, bool) {
	v, ok := one(kindSync)
	if !ok {
		return nil, false
	}
	return v.(ContinuityStore), true
}

// Entitlements answers the registered entitlement source; ok false: none.
// Providers ask it; no code of this repository does.
func Entitlements() (EntitlementSource, bool) {
	v, ok := one(kindEntitlements)
	if !ok {
		return nil, false
	}
	return v.(EntitlementSource), true
}

// Media answers the registered media room provider; ok false: none.
func Media() (MediaRoomProvider, bool) {
	v, ok := one(kindMedia)
	if !ok {
		return nil, false
	}
	return v.(MediaRoomProvider), true
}

// RuntimeProvider answers the registered runtime; ok false: none. (A type
// named Runtime takes the shorter name.)
func RuntimeProvider() (Runtime, bool) {
	v, ok := one(kindRuntime)
	if !ok {
		return nil, false
	}
	return v.(Runtime), true
}

// ContextProviders answers the registered context providers, in the order
// of registration: a copy.
func ContextProviders() []ContextProvider {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return contextList(reg.slots[kindContext])
}

// NotificationSinks answers the registered notification sinks, in the
// order of registration: a copy.
func NotificationSinks() []NotificationSink {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return sinkList(reg.slots[kindNotifications])
}

// Registered lists every registered provider, in the order of the kinds
// and, within a list, of registration. A value registered in two slots is
// listed in each.
func Registered() []Registration {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var out []Registration
	for k := range kindCount {
		for _, e := range reg.slots[k] {
			out = append(out, Registration{Kind: e.Kind, Name: e.Name})
		}
	}
	return out
}

// Freeze closes the registry and returns what it holds. Every call after
// the first returns the same; a Register after it panics.
func Freeze() Providers {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if !reg.frozen {
		reg.frozen = true
		reg.held = reg.providersLocked()
	}
	return copyProviders(reg.held)
}

// register adds p to slot k, or panics with the slot and the reason.
func register(k kind, p Provider) {
	slot := kindNames[k]
	if p == nil || nilValue(p) {
		panic(fmt.Sprintf("ext: register %s: a nil provider", slot))
	}
	name := p.Name() // before the lock: a Name that reads the registry must not deadlock
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.frozen {
		panic(fmt.Sprintf("ext: register %s %q: the registry is frozen (register in init only)", slot, name))
	}
	if !ValidName(name) {
		panic(fmt.Sprintf("ext: register %s: provider name %q is not 1 to %d of a-z, 0-9 and '-' beginning with a letter or digit", slot, name, MaxProviderName))
	}
	held := reg.slots[k]
	if !k.isList() && len(held) > 0 {
		panic(fmt.Sprintf("ext: register %s %q: %q is registered already, and the slot holds one", slot, name, held[0].Name))
	}
	for _, e := range held {
		if e.Name == name {
			panic(fmt.Sprintf("ext: register %s %q: the list holds that name already", slot, name))
		}
	}
	if len(held) >= MaxPerList {
		panic(fmt.Sprintf("ext: register %s %q: the list holds %d already, its most", slot, name, MaxPerList))
	}
	if other, ok := reg.names[name]; ok && !sameValue(other, p) {
		panic(fmt.Sprintf("ext: register %s %q: another provider holds that name (one name is one value)", slot, name))
	}
	if reg.names == nil {
		reg.names = make(map[string]Provider)
	}
	reg.names[name] = p
	reg.slots[k] = append(held, Entry{Kind: slot, Name: name, Value: p})
}

// nilValue reports whether p, an interface that is not nil, holds a nil
// pointer, map, slice, func or channel: a provider that is not there, whose
// Name would panic without naming the slot or answer for a value that is
// none.
func nilValue(p Provider) bool {
	switch v := reflect.ValueOf(p); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return v.IsNil()
	}
	return false
}

// sameValue reports whether a and b are one value: of one comparable
// dynamic type and ==, as a pointer is. An incomparable value (a struct
// holding a slice, say) is never the same as anything.
func sameValue(a, b Provider) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// one answers the value of a one-value slot.
func one(k kind) (Provider, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if len(reg.slots[k]) == 0 {
		return nil, false
	}
	return reg.slots[k][0].Value, true
}

// providersLocked builds what the registry holds; reg.mu is held.
func (r *registry) providersLocked() Providers {
	var p Providers
	if e := r.slots[kindIdentity]; len(e) > 0 {
		p.Identity = e[0].Value.(IdentityProvider)
	}
	if e := r.slots[kindDecisions]; len(e) > 0 {
		p.Router = e[0].Value.(DecisionRouter)
	}
	if e := r.slots[kindSync]; len(e) > 0 {
		p.Continuity = e[0].Value.(ContinuityStore)
	}
	if e := r.slots[kindMedia]; len(e) > 0 {
		p.Media = e[0].Value.(MediaRoomProvider)
	}
	if e := r.slots[kindRuntime]; len(e) > 0 {
		p.Runtime = e[0].Value.(Runtime)
	}
	p.Context = contextList(r.slots[kindContext])
	p.Notifications = sinkList(r.slots[kindNotifications])
	for k := range kindCount {
		p.All = append(p.All, r.slots[k]...)
	}
	return p
}

func contextList(es []Entry) []ContextProvider {
	if len(es) == 0 {
		return nil
	}
	out := make([]ContextProvider, len(es))
	for i, e := range es {
		out[i] = e.Value.(ContextProvider)
	}
	return out
}

func sinkList(es []Entry) []NotificationSink {
	if len(es) == 0 {
		return nil
	}
	out := make([]NotificationSink, len(es))
	for i, e := range es {
		out[i] = e.Value.(NotificationSink)
	}
	return out
}

// copyProviders answers p with slices of its own, so a caller's changes
// never reach the registry.
func copyProviders(p Providers) Providers {
	p.Context = append([]ContextProvider(nil), p.Context...)
	p.Notifications = append([]NotificationSink(nil), p.Notifications...)
	p.All = append([]Entry(nil), p.All...)
	return p
}
