package export

import (
	"slices"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// ResolveCatalog is catalog.json for an archive: the overlay ov, saved over
// the configured catalog base, with every agent it saves resolved against
// base as the running server resolves it (catalog.Catalog.ApplyOverlay):
// each env value stored as catalog.RedactedValue (the configured agent's
// value) replaced by that value, and each field it leaves to the
// configured agent (adapter, signal, site, yolo, trust, questions,
// session, probe) filled from it. The archive holds no configuration, so
// its agents then launch as the source's did with none; a destination
// whose config holds the same agent and values masks them again at its
// start. An id the overlay repeats is one agent, the one that wins at
// start, in the place of its first; a hidden one stays as saved (no
// catalog holds it), and the hidden list is kept. The bytes are in
// catalog.json's format.
func ResolveCatalog(ov catalog.Overlay, base catalog.Catalog) ([]byte, error) {
	effective := base.Clone()
	if err := effective.ApplyOverlay(ov); err != nil {
		return nil, err
	}
	out := catalog.Overlay{Agents: make([]catalog.Agent, 0, len(ov.Agents)), Hidden: slices.Clone(ov.Hidden)}
	var seen []string
	for i, a := range ov.Agents {
		if slices.Contains(seen, a.ID) {
			continue
		}
		seen = append(seen, a.ID)
		if r, ok := effective.Get(a.ID); ok {
			out.Agents = append(out.Agents, r.Copy())
			continue
		}
		// Hidden: as the last save of its id has it.
		for j := len(ov.Agents) - 1; j >= i; j-- {
			if ov.Agents[j].ID == a.ID {
				out.Agents = append(out.Agents, ov.Agents[j].Copy())
				break
			}
		}
	}
	return store.Encode(out)
}
