package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The workspace's other machines (docs/plans/hosts.md, PR-5): the second
// half of GET /api/hosts, built from the remote records a registered sync
// store wrote (internal/api/remote.go: the kinds device, hosts, pointer
// and decision), shown by one rule, otherMachinesShown.

// othersBehindShown is how long the sync status may say behind with other
// machines still shown: a shorter backlog of uploads counts as current.
const othersBehindShown = 10 * time.Minute

// othersView is what otherMachinesShown says: present, the section is in
// the reply at all; shown, its machines are; state and since the sync
// status's.
type othersView struct {
	present, shown bool
	state          string
	since          time.Time
}

// othersSeen keeps, in memory, that the sync status has been current at
// least once in this server's life.
type othersSeen struct {
	mu      sync.Mutex
	current bool
}

// otherMachinesShown is the one rule of when other machines are shown, on
// every page that shows them: present while the sync store is registered
// and ready (never while it starts), its status has been current at least
// once in this server's life, and it is not signed out; shown when,
// besides, the status is current, or behind for less than
// othersBehindShown. It reads the store's report once, from memory
// through the guard (ext.StatusWait).
func (s *Server) otherMachinesShown(ctx context.Context) othersView {
	if s.syncName() == "" {
		return othersView{}
	}
	rep, ok := s.syncReport(ctx)
	if !ok {
		return othersView{}
	}
	s.others.mu.Lock()
	if rep.State == ext.SyncCurrent {
		s.others.current = true
	}
	ever := s.others.current
	s.others.mu.Unlock()
	if !ever || rep.State == ext.SyncSignedOut {
		return othersView{}
	}
	v := othersView{present: true, state: rep.State, since: rep.Since.UTC()}
	switch rep.State {
	case ext.SyncCurrent:
		v.shown = true
	case ext.SyncBehind:
		v.shown = !rep.Since.IsZero() && s.seen.clock().Sub(rep.Since) < othersBehindShown
	}
	return v
}

// remoteDeviceBody is a device record's body as the reply reads it.
type remoteDeviceBody struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Platform   string    `json:"platform"`
	Version    string    `json:"version"`
	LastReport time.Time `json:"lastReport"`
	Revoked    bool      `json:"revoked"`
}

// remotePointerBody is a pointer record's body as the reply reads it.
type remotePointerBody struct {
	ID          string    `json:"id"`
	Crew        string    `json:"crew"`
	State       string    `json:"state"`
	ContinuedBy string    `json:"continuedBy"`
	ClosedAt    time.Time `json:"closedAt"`
}

// hostsOther is the second half of the reply, nil when the rule says it
// is not present; machines empty when it says they are not shown. A
// device record of this server's own device (the status's) is left out,
// and so is a pointer of a run this ledger holds a row of.
func (s *Server) hostsOther(ctx context.Context) *HostsOther {
	v := s.otherMachinesShown(ctx)
	if !v.present {
		return nil
	}
	out := &HostsOther{State: v.state, Since: v.since, Machines: []HostsMachine{}}
	if !v.shown {
		return out
	}
	rep, _ := s.syncReport(ctx)
	self := rep.Device
	paths, err := s.remoteNames()
	if err != nil {
		s.log.Warn("hosts: other machines' records", "err", err)
		return out
	}
	type machineParts struct {
		device   remoteDeviceBody
		summary  *ext.HostSummary
		pointers []struct {
			p      remotePointerBody
			at     time.Time
			byName string
		}
		decisions map[string]int // open decisions by the run that holds each now
		open      int
	}
	devices := map[string]*machineParts{}
	var deviceOrder []string
	var rest []remoteRecord
	for _, path := range paths {
		kind := remoteKindOf(path)
		if kind != ext.RemoteDevice && kind != ext.RemoteHosts && kind != ext.RemotePointer && kind != ext.RemoteDecision {
			continue
		}
		rec, ok := s.readRemote(path)
		if !ok || rec.Kind != kind {
			continue
		}
		if kind != ext.RemoteDevice {
			// What this server's own device holds is this server's.
			if rec.Device != self {
				rest = append(rest, rec)
			}
			continue
		}
		var d remoteDeviceBody
		if json.Unmarshal(rec.Data, &d) != nil || d.ID != rec.ID || d.ID == self || d.Revoked || d.Kind == "host" {
			continue
		}
		if _, ok := devices[d.ID]; !ok {
			deviceOrder = append(deviceOrder, d.ID)
		}
		devices[d.ID] = &machineParts{device: d, decisions: map[string]int{}}
	}
	names := map[string]string{}
	for id, m := range devices {
		names[id] = m.device.Name
	}
	for _, rec := range rest {
		// A pointer is an open run while it is not closed, not final and not
		// continued, as this server's own list decides.
		// A record of a device that is not shown (this one, revoked, or
		// no device record) is of no machine on the page.
		m := devices[rec.Device]
		if m == nil {
			continue
		}
		switch rec.Kind {
		case ext.RemoteHosts:
			if sum, err := ext.ParseHostSummary(rec.Data); err == nil {
				m.summary = &sum
			}
		case ext.RemotePointer:
			var p remotePointerBody
			if json.Unmarshal(rec.Data, &p) != nil || p.ID == "" || !p.ClosedAt.IsZero() || p.ContinuedBy != "" || s.ledgerHolds(ctx, p.ID) ||
				p.State == ledger.RunFinished || p.State == ledger.RunStopped {
				continue
			}
			m.pointers = append(m.pointers, struct {
				p      remotePointerBody
				at     time.Time
				byName string
			}{p, rec.At, rec.DeviceName})
		case ext.RemoteDecision:
			var d ext.DecisionDoc
			if json.Unmarshal(rec.Data, &d) != nil || d.State != ledger.DecisionOpen {
				continue
			}
			m.decisions[d.Run]++
			m.open++
		}
	}
	// Newest report first, then by name.
	slices.SortFunc(deviceOrder, func(a, b string) int {
		if c := devices[b].device.LastReport.Compare(devices[a].device.LastReport); c != 0 {
			return c
		}
		return strings.Compare(devices[a].device.Name, devices[b].device.Name)
	})
	used := 0
	for _, id := range deviceOrder {
		m := devices[id]
		row := HostsMachine{Device: cutBytes(id, ext.MaxHeldID), Name: cutBytes(m.device.Name, hostsNameMax),
			Platform: cutBytes(m.device.Platform, hostsPlatformMax), Version: cutBytes(m.device.Version, hostsVersionMax),
			LastReport: m.device.LastReport.UTC(), Own: m.summary != nil, Runs: []HostsRemoteRun{},
			Decisions: HostsDecisions{Open: m.open}}
		if m.summary != nil {
			row.Summary = remoteSummary(*m.summary)
		}
		slices.SortFunc(m.pointers, func(a, b struct {
			p      remotePointerBody
			at     time.Time
			byName string
		}) int {
			return b.at.Compare(a.at)
		})
		for _, p := range m.pointers {
			if len(row.Runs) == hostsMaxRemoteRuns {
				out.More.RemoteRuns++
				continue
			}
			row.Runs = append(row.Runs, HostsRemoteRun{ID: cutBytes(p.p.ID, ext.MaxHeldID), Crew: cutBytes(p.p.Crew, hostsNameMax),
				State: cutBytes(p.p.State, 32), Waiting: m.decisions[p.p.ID], ReportedBy: cutBytes(names[id], hostsNameMax), ReportedAt: p.at.UTC()})
		}
		n := hostsEncodedLen(row) + 1
		if len(out.Machines) == hostsMaxMachines || used+n > hostsOtherBudget {
			// A machine past the budget: once more with no runs, else left
			// out whole.
			cut := row
			cut.Runs = []HostsRemoteRun{}
			if m := hostsEncodedLen(cut) + 1; len(out.Machines) < hostsMaxMachines && used+m <= hostsOtherBudget {
				out.More.RemoteRuns += len(row.Runs)
				out.Machines = append(out.Machines, cut)
				used += m
				continue
			}
			out.More.Machines++
			out.More.RemoteRuns += len(row.Runs)
			continue
		}
		out.Machines = append(out.Machines, row)
		used += n
	}
	return out
}

// remoteSummary is another server's host summary as the reply carries it:
// at most hostsSummaryRows of its host rows, the rest counted with those
// its own list left out.
func remoteSummary(sum ext.HostSummary) *HostsRemoteSummary {
	out := &HostsRemoteSummary{Platform: sum.Platform, Version: sum.Version,
		Sessions: HostsSessions{Running: sum.Sessions.Running, Waiting: sum.Sessions.Waiting},
		Runs:     HostsRemoteRuns{Held: sum.Runs.Held, Waiting: sum.Runs.Waiting}, Hosts: []HostsSummaryHost{}, MoreHosts: sum.More}
	for _, h := range sum.Hosts {
		if len(out.Hosts) == hostsSummaryRows {
			out.MoreHosts++
			continue
		}
		out.Hosts = append(out.Hosts, HostsSummaryHost{Name: h.Name, Platform: h.Platform, Version: h.Version, Availability: h.Availability,
			Since: h.Since, Sessions: HostsSessions{Running: h.Sessions.Running, Waiting: h.Sessions.Waiting}})
	}
	return out
}

// ledgerHolds reports whether this server's ledger holds a row of run id.
func (s *Server) ledgerHolds(ctx context.Context, id string) bool {
	if s.ledger == nil {
		return false
	}
	_, ok, err := s.ledger.Run(ctx, id)
	return err == nil && ok
}
