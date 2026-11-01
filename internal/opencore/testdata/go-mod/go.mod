module github.com/rocksolidlabs/conductor

go 1.26.0

// A fixture of internal/opencore's test: one planted requirement of a
// private module of the organization, beside one the rule lets through.
require (
	github.com/coder/websocket v1.8.15
	github.com/rocksolidlabs/conductor-cloud v0.1.0
)

replace github.com/rocksolidlabs/conductor => ./
