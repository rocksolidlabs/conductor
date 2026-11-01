package api

import (
	"context"
	"testing"

	. "github.com/rocksolidlabs/conductor/ext/local"

	"github.com/rocksolidlabs/conductor/ext"
)

// plans is a test's entitlement-only provider: a test file is no part of
// the binary, so it may name the entitlement source.
type plans struct{}

func (plans) Name() string { return "plans" }

func (plans) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}

func TestStartsAnEntitlementOnlyProvider(t *testing.T) {
	var _ ext.EntitlementSource = plans{}
	if got, _ := (AllowAll{}).Entitled(t.Context(), "x"); !got.Allowed {
		t.Fatal("refused")
	}
}
