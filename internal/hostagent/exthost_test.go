package hostagent

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// conductor host keeps no ledger: its provider's Host answers a decision's
// read and the open decisions' refs ErrUnsupported, then context.Canceled
// once its try has ended.
func TestHostExtHostKeepsNoLedger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &hostExtHost{ctx: ctx, name: "cloud", dir: t.TempDir(), log: slog.New(slog.DiscardHandler)}
	if _, err := h.Decision(t.Context(), "dr-1"); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("Decision: %v", err)
	}
	if refs, next, err := h.OpenDecisionRefs(t.Context(), ""); !errors.Is(err, ext.ErrUnsupported) || refs != nil || next != "" {
		t.Fatalf("OpenDecisionRefs: %v %q %v", refs, next, err)
	}
	cancel()
	if _, err := h.Decision(t.Context(), "dr-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Decision once the try ended: %v", err)
	}
	if refs, next, err := h.OpenDecisionRefs(t.Context(), "decision:dr-1"); !errors.Is(err, context.Canceled) || refs != nil || next != "" {
		t.Fatalf("OpenDecisionRefs once the try ended: %v %q %v", refs, next, err)
	}
}
