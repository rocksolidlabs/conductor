// Package local is a fixture of internal/opencore's test: one planted
// import of this module's internals in ext/local, beside the runners it
// may import.
package local

import (
	"context"

	_ "github.com/rocksolidlabs/conductor/internal/session"
)

var _ = context.Background
