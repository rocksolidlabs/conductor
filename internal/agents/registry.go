package agents

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// registry lists every adapter in the order the Events page shows them.
var registry = []Adapter{
	claudeAdapter(),
	codexAdapter(),
	agyAdapter(),
	copilotAdapter(),
	cursorAdapter(),
	opencodeAdapter(),
	piAdapter(),
	ompAdapter(),
	aiderAdapter(),
	gooseAdapter(),
	ampAdapter(),
	dshAdapter(),
}

// Get returns the adapter with the given ID.
func Get(id string) (Adapter, bool) {
	for _, a := range registry {
		if a.ID == id {
			return a.clone(), true
		}
	}
	return Adapter{}, false
}

// CheckAdapter reports an error when id names no adapter Conductor has; ""
// (no adapter) passes. The error lists the adapters there are. The catalog
// cannot check this itself, since the adapters import it: the config's agents
// are checked when it is loaded (config.LoadCatalog), saved ones when they are
// saved and at startup (internal/api).
func CheckAdapter(id string) error {
	if id == "" {
		return nil
	}
	if _, ok := Get(id); ok {
		return nil
	}
	ids := make([]string, 0, len(registry))
	for _, a := range registry {
		ids = append(ids, a.ID)
	}
	return fmt.Errorf("unknown adapter %q (one of %s)", id, strings.Join(ids, ", "))
}

// All returns every adapter, in a stable order.
func All() []Adapter {
	out := make([]Adapter, 0, len(registry))
	for _, a := range registry {
		out = append(out, a.clone())
	}
	return out
}

// TrustArgsFor returns the arguments that trust the directories dirs gives
// for one launch of the adapter agentID (Adapter.TrustArgs), nil when it has
// none or there are no directories. dirs is called only for an adapter that
// trusts per launch: it asks git.
func TrustArgsFor(agentID string, dirs func() []string) []string {
	a, ok := Get(agentID)
	if !ok || a.TrustArgs == nil {
		return nil
	}
	if d := dirs(); len(d) > 0 {
		return a.TrustArgs(d)
	}
	return nil
}

// ConfirmsSubmit reports whether the adapter agentID reports taking a prompt
// with the agent's signal (Adapter.ConfirmsSubmit, through its hooks).
func ConfirmsSubmit(agentID string, sig catalog.Signal) bool {
	a, ok := Get(agentID)
	return ok && a.ConfirmsSubmit && sig.Kind == catalog.SignalHook
}

// clone returns a copy of a that shares no map or slice with the registry.
func (a Adapter) clone() Adapter {
	a.Assets = maps.Clone(a.Assets)
	a.Events = slices.Clone(a.Events)
	if a.Usage != nil {
		u := *a.Usage
		a.Usage = &u
	}
	if a.Probe != nil {
		p := *a.Probe
		a.Probe = &p
	}
	return a
}

// MCPFor returns the arguments that register Conductor's MCP server with
// the agent of adapter agentID for one launch: nil for an adapter without a
// way to at launch, an unknown adapter, or a hooks dir that is not absolute
// (the arguments name a file in it).
func MCPFor(agentID, hooksDir string) []string {
	if !filepath.IsAbs(hooksDir) {
		return nil
	}
	a, ok := Get(agentID)
	if !ok || a.MCP == nil {
		return nil
	}
	return a.MCP(hooksDir)
}

// InjectFor returns the flags to append to an agent's command and the
// environment to add to its session, for the adapter agentID and the agent's
// signal. Only the "hook" signal is wired at launch, except with yolo (the
// agent's yolo recipe applied) for an adapter with YoloInject, which takes
// Inject's place whatever the signal. Any other signal, an unknown or empty
// adapter, an adapter with no launch route and a hooks dir that is not
// absolute (the flags name files in it, and a relative path would be read
// from the session's working directory) all give nil, nil.
func InjectFor(agentID string, hooksDir string, sig catalog.Signal, yolo bool) ([]string, map[string]string) {
	if !filepath.IsAbs(hooksDir) {
		return nil, nil
	}
	for _, a := range registry {
		if a.ID != agentID {
			continue
		}
		inject := a.Inject
		switch {
		case yolo && a.YoloInject != nil:
			inject = a.YoloInject
		case sig.Kind != catalog.SignalHook:
			return nil, nil
		}
		if inject == nil {
			return nil, nil
		}
		argv, env := inject(hooksDir, sig)
		if len(argv) == 0 {
			argv = nil
		}
		if len(env) == 0 {
			env = nil
		}
		return argv, env
	}
	return nil, nil
}
