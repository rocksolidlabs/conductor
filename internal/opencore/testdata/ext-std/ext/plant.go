// Package ext is a fixture of internal/opencore's test: one planted import
// of this module's internals under ext/, beside the imports the rule lets
// through.
package ext

import (
	"context"

	_ "github.com/rocksolidlabs/conductor/internal/session"
)

var _ = context.Background
