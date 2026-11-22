package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// The host summary (docs/plans/hosts.md, PR-5): the first half of GET
// /api/hosts reduced to what may leave the machine (ext.HostSummary),
// handed to a registered sync store as the computed document hosts/summary
// so that the person's other servers can show it. Never on a switchyard,
// whose hosts are other people's servers.

// The host summary's store and its one document.
const (
	syncHosts       = "hosts"
	syncHostsPath   = "summary"
	hostsCoalesce   = 30 * time.Second
	hostsCheckEvery = 5 * time.Minute
)

// hostsSync is the host summary's source: what marks it (the event hub's
// sends, through changed), the 30 s coalescing and the 5-minute check, and
// the hash gate that marks it pending only when it differs from what was
// last delivered.
type hostsSync struct {
	s     *Server
	a     *syncAdapter
	src   *computedSource
	marks chan struct{}

	mu sync.Mutex
	// after is the source's clock (time.After but in tests); checked, when
	// a test sets it, is told each gate's outcome (marked or not).
	after   func(time.Duration) <-chan time.Time
	checked func(marked bool)
}

// registerHostSummary makes the host summary a computed source of the sync
// adapter, marked by the event hub's sends: only with a sync store
// registered, never on a switchyard.
func (s *Server) registerHostSummary() {
	if s.cfg.Switchyard.Enabled || s.syncName() == "" || s.syncs == nil {
		return
	}
	h := &hostsSync{s: s, a: s.syncs, marks: make(chan struct{}, 1), after: time.After}
	h.src = &computedSource{store: syncHosts,
		paths: func() []string { return []string{syncHostsPath} },
		build: func(ctx context.Context, _ string) ([]byte, bool, error) {
			b, ok := s.hostSummaryBytes(ctx)
			if !ok {
				return nil, false, errNotHanded
			}
			return b, false, nil
		},
		start: h.run}
	s.syncs.computed(h.src)
	s.hostsSync = h
	f := h.changed
	s.events.onChange.Store(&f)
}

// changed is told every change the page would show (the event hub's
// session, removed, run and decision sends). It only arms the coalescing,
// never builds or waits.
func (h *hostsSync) changed() {
	select {
	case h.marks <- struct{}{}:
	default:
	}
}

// clock is the source's after.
func (h *hostsSync) clock(d time.Duration) <-chan time.Time {
	h.mu.Lock()
	after := h.after
	h.mu.Unlock()
	return after(d)
}

// run coalesces the marks, hostsCoalesce from the first, and checks every
// hostsCheckEvery for a change no event tells (a recently seen machine
// passing its 24 hours, another process taking the ledger's lock).
func (h *hostsSync) run(ctx context.Context) {
	var coalesce <-chan time.Time
	check := h.clock(hostsCheckEvery)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.marks:
			if coalesce == nil {
				coalesce = h.clock(hostsCoalesce)
			}
		case <-coalesce:
			coalesce = nil
			h.gate(ctx)
		case <-check:
			check = h.clock(hostsCheckEvery)
			h.gate(ctx)
		}
	}
}

// gate builds the summary and marks it pending when its bytes differ from
// those last delivered.
func (h *hostsSync) gate(ctx context.Context) {
	b, ok := h.s.hostSummaryBytes(ctx)
	marked := ok && docHash(b) != h.src.delivered(syncHostsPath)
	if marked {
		h.a.computedChanged(syncHosts, syncHostsPath)
	}
	h.mu.Lock()
	checked := h.checked
	h.mu.Unlock()
	if checked != nil {
		checked(marked)
	}
}

// hostSummaryBytes is the host summary encoded; false when there is none
// it may hand on (one ext.HostSummary.Valid refuses, as for a server that
// cannot say its platform).
func (s *Server) hostSummaryBytes(ctx context.Context) ([]byte, bool) {
	sum := s.hostSummary(ctx)
	if err := sum.Valid(); err != nil {
		s.log.Warn("sync: the host summary is not handed on", "err", err)
		return nil, false
	}
	b, err := json.Marshal(sum)
	if err != nil {
		return nil, false
	}
	return b, true
}

// hostSummary is the first half of the host overview reduced to what may
// leave the machine: this server's platform, version and counts (its own
// sessions and those hosted from its machine; the open runs it holds and
// how many of them wait for a person), and each host row's name,
// platform, version, availability, since and counts, in the page's order:
// at most ext.MaxHostSummaryRows, the encoded whole within
// ext.MaxHostSummary, a row whose name the summary refuses left out, and
// every host not in the list counted in More. Never a session's id, a
// command, a folder, a branch, a run's name, an OS user, the machine id or
// a machine key.
func (s *Server) hostSummary(ctx context.Context) ext.HostSummary {
	r := s.hostsFirstHalf(ctx, false)
	sum := ext.HostSummary{Platform: r.This.Platform, Version: r.This.Version,
		Sessions: ext.HostSessions{Running: r.This.Sessions.Running, Waiting: r.This.Sessions.Waiting}, Hosts: []ext.HostSummaryRow{}}
	if r.This.Hosted != nil {
		sum.Sessions.Running += r.This.Hosted.Sessions.Running
		sum.Sessions.Waiting += r.This.Hosted.Sessions.Waiting
	}
	if r.Ledger.Holder == HolderThis {
		sum.Runs.Held = len(r.Runs) + r.More.Runs
		for _, run := range r.Runs {
			if run.Waiting != nil && *run.Waiting > 0 {
				sum.Runs.Waiting++
			}
		}
	}
	sum.More = r.More.Hosts
	for _, h := range r.Hosts {
		row := ext.HostSummaryRow{Name: h.Name, Platform: h.Platform, Version: h.Version, Availability: h.Availability, Since: h.Since,
			Sessions: ext.HostSessions{Running: h.Sessions.Running, Waiting: h.Sessions.Waiting}}
		if row.Availability == ext.HostConnected {
			row.Since = time.Time{}
		}
		if len(sum.Hosts) == ext.MaxHostSummaryRows || (ext.HostSummary{Platform: "linux/amd64", Version: "v1", Hosts: []ext.HostSummaryRow{row}}).Valid() != nil {
			sum.More++
			continue
		}
		sum.Hosts = append(sum.Hosts, row)
	}
	// Fitted to the summary's bound, dropping from the end of the order.
	for len(sum.Hosts) > 0 {
		b, err := json.Marshal(sum)
		if err == nil && len(b) <= ext.MaxHostSummary {
			break
		}
		sum.Hosts = sum.Hosts[:len(sum.Hosts)-1]
		sum.More++
	}
	return sum
}
