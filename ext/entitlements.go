package ext

import (
	"context"
	"time"
)

// EntitlementSource answers what the signed-in workspace may use of a paid
// feature. The providers of the private repositories ask it; no code of
// this repository does (CONTRIBUTING.md, Rules for code). Its free default
// allows everything, and the configuration keeps its own limits.
type EntitlementSource interface {
	Provider
	Entitled(ctx context.Context, feature string) (Entitlement, error)
}

// Entitlement is what a workspace may use of one feature.
type Entitlement struct {
	Allowed bool      `json:"allowed"`
	Limit   int64     `json:"limit,omitempty"` // 0: none
	Until   time.Time `json:"until,omitzero"`
	Why     string    `json:"why,omitempty"`
}
