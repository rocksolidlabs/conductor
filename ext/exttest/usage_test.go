package exttest

import (
	"context"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// TestFakeUsageObserverRecords: the fake records every report in order,
// answers copies, tells Changed at each, and is still the identity
// provider it wraps.
func TestFakeUsageObserverRecords(t *testing.T) {
	f := NewFakeUsageObserver("fake-identity")
	var _ ext.IdentityProvider = f
	f.Add("tok", ada)
	if p, ok, _ := f.Authenticate(context.Background(), "tok"); !ok || p.Subject != ada.Subject {
		t.Fatalf("Authenticate(tok) = %+v, %v; want ada", p, ok)
	}

	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { f.Used(ext.Usage{Subject: "ada", RelayedBytes: int64(i)}) })
	}
	if err := Await(waitCtx(t), f, func() bool { return len(f.Usages()) == n }); err != nil {
		t.Fatalf("the fake holds %d reports of %d: %v", len(f.Usages()), n, err)
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, u := range f.Usages() {
		seen[u.RelayedBytes] = true
	}
	if len(seen) != n {
		t.Fatalf("the fake holds %d distinct reports, want %d", len(seen), n)
	}

	got := f.Usages()
	got[0].Subject = "changed"
	if f.Usages()[0].Subject != "ada" {
		t.Fatal("Usages answers what the fake holds, not a copy")
	}

	f.Used(ext.Usage{Subject: "ada", Final: true})
	if u := f.Usages(); !u[len(u)-1].Final {
		t.Fatalf("the last report is not the one handed last: %+v", u[len(u)-1])
	}
}
