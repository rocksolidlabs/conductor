package api

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// hostsSeen keeps the machines whose last session left this server, for
// the host overview's Last seen rows (docs/plans/hosts.md, PR-2): in
// memory only, at most hostsSeenMax machines, each for hostsSeenFor, the
// oldest dropped first. The registry decides, never the hub: the removal
// hook, which every removal of a session reaches (the hub's Expire, a
// DELETE, the retention sweep), records a machine only when the registry
// then lists no session of its key, and a change of a session of the key
// drops the entry; both under the table's lock, so of a register and a
// last removal the second decides.
type hostsSeen struct {
	mu      sync.Mutex
	entries map[string]seenHost
	// now is the table's clock: time.Now but in tests.
	now func() time.Time
	// removing, when a test sets it, is called inside the removal hook,
	// under the table's lock, before the registry is read.
	removing func(key string)
}

// seenHost is one machine whose last session left.
type seenHost struct {
	name, platform, version, keyFrom string
	at                               time.Time
}

func newHostsSeen() *hostsSeen {
	return &hostsSeen{entries: map[string]seenHost{}, now: time.Now}
}

// clock is the table's time, in UTC.
func (h *hostsSeen) clock() time.Time { return h.now().UTC() }

// removed is the registry's removal hook for a hosted session: the
// machine is recorded when list, read under the table's lock, holds no
// session of its key. own is this server's own machine key, never
// recorded: its sessions are this server's.
func (h *hostsSeen) removed(info session.Info, own string, list func() []session.Info) {
	if info.Kind != session.KindHosted || info.HostKey == "" || info.HostKey == own {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removing != nil {
		h.removing(info.HostKey)
	}
	for _, other := range list() {
		if other.HostKey == info.HostKey {
			return
		}
	}
	h.entries[info.HostKey] = seenHost{name: info.HostName, platform: info.HostPlatform, version: info.HostVersion,
		keyFrom: info.HostKeyFrom, at: h.clock()}
	h.trimLocked()
}

// changed is the hub's change hook: a session of the key is listed, so
// its machine is no longer only seen.
func (h *hostsSeen) changed(info session.Info) {
	if info.HostKey == "" {
		return
	}
	h.mu.Lock()
	delete(h.entries, info.HostKey)
	h.mu.Unlock()
}

// trimLocked drops the entries past hostsSeenFor and, past hostsSeenMax,
// the oldest.
func (h *hostsSeen) trimLocked() {
	now := h.clock()
	for k, e := range h.entries {
		if now.Sub(e.at) >= hostsSeenFor {
			delete(h.entries, k)
		}
	}
	for len(h.entries) > hostsSeenMax {
		oldest := ""
		for k, e := range h.entries {
			if oldest == "" || e.at.Before(h.entries[oldest].at) || e.at.Equal(h.entries[oldest].at) && k < oldest {
				oldest = k
			}
		}
		delete(h.entries, oldest)
	}
}

// list is the machines seen, newest first, without those whose key has a
// listed session (listed).
func (h *hostsSeen) list(listed map[string]bool) []HostRow {
	h.mu.Lock()
	h.trimLocked()
	out := make([]HostRow, 0, len(h.entries))
	for k, e := range h.entries {
		if listed[k] {
			continue
		}
		out = append(out, HostRow{Key: k, Kind: HostKindHost, Name: cutBytes(e.name, hostsNameMax),
			Platform: cutBytes(e.platform, hostsPlatformMax), Version: cutBytes(e.version, hostsVersionMax),
			Availability: HostSeen, Since: e.at, SessionIDs: []string{}, KeyFrom: e.keyFrom})
	}
	h.mu.Unlock()
	slices.SortFunc(out, func(a, b HostRow) int {
		if c := b.Since.Compare(a.Since); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}
