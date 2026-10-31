package main

import (
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// TestPlainBuildRegistersNothing: this test's binary links every package
// main links, and every init of them has run before it starts; a plain
// build of this repository registers no provider, so every extension seam
// runs its free default (the open-source build is the whole product).
func TestPlainBuildRegistersNothing(t *testing.T) {
	if got := ext.Registered(); len(got) != 0 {
		t.Fatalf("ext.Registered() = %v, want nothing registered in the free build", got)
	}
	if _, ok := ext.Identity(); ok {
		t.Error("an identity provider is registered")
	}
	if _, ok := ext.Router(); ok {
		t.Error("a decision router is registered")
	}
	if _, ok := ext.Continuity(); ok {
		t.Error("a sync store is registered")
	}
	if _, ok := ext.Entitlements(); ok {
		t.Error("an entitlement source is registered")
	}
	if _, ok := ext.Media(); ok {
		t.Error("a media room provider is registered")
	}
	if _, ok := ext.RuntimeProvider(); ok {
		t.Error("a runtime is registered")
	}
	if got := ext.ContextProviders(); len(got) != 0 {
		t.Errorf("context providers registered: %d", len(got))
	}
	if got := ext.NotificationSinks(); len(got) != 0 {
		t.Errorf("notification sinks registered: %d", len(got))
	}
}
