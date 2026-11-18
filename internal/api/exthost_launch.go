package api

import (
	"context"
	"fmt"

	"github.com/rocksolidlabs/conductor/ext"
)

// Run reads a run this provider launched. Not built yet: every call
// answers ext.ErrUnsupported.
func (h *extHost) Run(context.Context, string) (ext.RunStatus, error) {
	if err := h.gone(); err != nil {
		return ext.RunStatus{}, err
	}
	return ext.RunStatus{}, fmt.Errorf("%w: this server does not read a provider's runs yet", ext.ErrUnsupported)
}

// RunByOp finds the run a launch with an operation id made. Not built yet:
// every call answers ext.ErrUnsupported.
func (h *extHost) RunByOp(context.Context, string) (ext.RunStatus, error) {
	if err := h.gone(); err != nil {
		return ext.RunStatus{}, err
	}
	return ext.RunStatus{}, fmt.Errorf("%w: this server does not read a provider's runs yet", ext.ErrUnsupported)
}

// StopRun stops a run this provider launched. Not built yet: every call
// answers ext.ErrUnsupported.
func (h *extHost) StopRun(context.Context, ext.StopRequest) error {
	if err := h.gone(); err != nil {
		return err
	}
	return fmt.Errorf("%w: this server does not stop a provider's runs yet", ext.ErrUnsupported)
}

// ResolveLaunch answers what a launch of a saved crew would run now. Not
// built yet: every call answers ext.ErrUnsupported.
func (h *extHost) ResolveLaunch(context.Context, ext.ResolveRequest) (ext.ResolvedLaunch, error) {
	if err := h.gone(); err != nil {
		return ext.ResolvedLaunch{}, err
	}
	return ext.ResolvedLaunch{}, fmt.Errorf("%w: this server does not resolve a launch yet", ext.ErrUnsupported)
}
