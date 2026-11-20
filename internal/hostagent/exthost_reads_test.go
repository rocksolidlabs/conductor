package hostagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// conductor host keeps no run's repository: its Host answers every read
// ErrUnsupported, and context.Canceled once its try has ended.
func TestHostReadsNoRepository(t *testing.T) {
	c := strings.Repeat("ab", 20)
	h := &hostExtHost{ctx: t.Context(), name: "graph"}
	if _, err := h.ReadTree(t.Context(), ext.TreeRequest{Run: "reads-0000aaaa", Commit: c}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("ReadTree: %v", err)
	}
	if _, err := h.ReadBlob(t.Context(), ext.BlobRequest{Run: "reads-0000aaaa", Commit: c, Path: "README"}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("ReadBlob: %v", err)
	}
	if _, err := h.Denied(t.Context(), ext.DeniedRequest{Run: "reads-0000aaaa", Paths: []string{"README"}}); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("Denied: %v", err)
	}
	ended, end := context.WithCancel(t.Context())
	end()
	if _, err := (&hostExtHost{ctx: ended, name: "graph"}).ReadTree(t.Context(), ext.TreeRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended try: %v", err)
	}
}
