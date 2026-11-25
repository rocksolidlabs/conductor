package exttest

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// countedPipes is the native Runtime with its StartPipes counted, and,
// as its fields say, one rule of the pipes broken.
type countedPipes struct {
	ext.Runtime
	n         *atomic.Int32
	extraEnv  bool // StartPipes adds a variable
	ignoreCtx bool // StartPipes starts under any context
}

func (c countedPipes) StartPipes(ctx context.Context, s ext.PipeSpec) (ext.Pipes, error) {
	c.n.Add(1)
	if c.extraEnv {
		s.Env = append(s.Env, "EXTTEST_EXTRA=1")
	}
	if c.ignoreCtx {
		ctx = context.Background()
	}
	return c.Runtime.(ext.PipeStarter).StartPipes(ctx, s)
}

// RunAll runs the pipes' checks on a Runtime that is an ext.PipeStarter,
// and the native one passes them; a Runtime that breaks one of their
// rules fails them, naming StartPipes.
func TestRuntimeExecPipes(t *testing.T) {
	rc := helperCase(t)
	n := &atomic.Int32{}
	RunAll(t, nil, local.Fill(ext.Providers{Runtime: countedPipes{Runtime: local.Native(), n: n}}), WithRuntimeCase(rc))
	if n.Load() == 0 {
		t.Fatal("RunAll ran no check of StartPipes")
	}
	for name, c := range map[string]struct {
		r    countedPipes
		said []string
	}{
		"a variable added":    {countedPipes{extraEnv: true}, []string{"StartPipes did not give the helper exactly its Env"}},
		"the context ignored": {countedPipes{ignoreCtx: true}, []string{"StartPipes under a context ended before the call returned Pipes"}},
	} {
		t.Run(name, func(t *testing.T) {
			c.r.Runtime, c.r.n = local.Native(), &atomic.Int32{}
			errs := record(t, func(t T) { RuntimeExec(t, c.r, rc) })
			if !said(errs, c.said...) {
				t.Fatalf("the checks did not say %q: %q", c.said, errs)
			}
		})
	}
}
