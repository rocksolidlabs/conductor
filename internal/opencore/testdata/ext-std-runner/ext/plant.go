// Package ext is a fixture of internal/opencore's test: one planted import
// of a runner under ext/, which only ext/local may import.
package ext

import (
	"context"

	_ "github.com/rocksolidlabs/conductor/internal/pty"
)

var _ = context.Background
