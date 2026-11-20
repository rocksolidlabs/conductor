package exttest

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// looker is a context provider that answers lookups: a valid answer, or,
// with dial set, after a round trip to its service under the call's
// context the first time it is asked.
type looker struct {
	*FakeContext
	n     *Network
	addr  string
	dial  bool
	asked atomic.Int32
}

func (p *looker) Lookup(ctx context.Context, r ext.LookupRequest) (ext.LookupAnswer, error) {
	if p.asked.Add(1) == 1 && p.dial {
		if conn, err := p.n.DialContext(ctx, "tcp", p.addr); err == nil {
			conn.Close()
		}
	}
	return ext.LookupAnswer{Items: []ext.LookupItem{{Kind: ext.ItemSymbol, Symbol: r.Symbol, Mark: ext.MarkInferred}},
		Coverage: ext.LookupCoverage{Commit: strings.Repeat("0a", 20), Files: 1}}, nil
}

// The Context check asks a provider that answers lookups, and finds one
// that reaches its service on the lookup's path; one that answers from
// what it holds passes, and is asked.
func TestLookupCheckBlocksTheNetwork(t *testing.T) {
	n := NewNetwork()
	echo := echoServer(t)
	errs := record(t, func(rt T) {
		Context(rt, n, &looker{FakeContext: NewFakeContext("fetcher"), n: n, addr: echo, dial: true}, SampleQuery(t.TempDir()))
	})
	if !said(errs, `exttest: Context "fetcher": Lookup dialled tcp`, "article 8") {
		t.Fatalf("a lookup that fetches: %v", errs)
	}
	quiet := &looker{FakeContext: NewFakeContext("holder")}
	if errs := record(t, func(rt T) { Context(rt, n, quiet, SampleQuery(t.TempDir())) }); len(errs) != 0 {
		t.Fatalf("a lookup from what it holds: %v", errs)
	}
	if quiet.asked.Load() < 2 {
		t.Fatalf("Lookup asked %d times", quiet.asked.Load())
	}
}
