package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// The host overview, GET /api/hosts (docs/plans/hosts.md, PR-2, PR-4 and
// PR-5): every machine this server knows, what runs and waits on each,
// and who holds this data directory's managed runs. The reply is built
// from what the server already holds, read once per request; nothing of
// it is persisted, and nothing in it is a token, a key a host sent, a
// folder, an address beyond the switchyard's shown URL, or anything a
// terminal printed.

// HostsReply is the answer of GET /api/hosts.
type HostsReply struct {
	At      time.Time `json:"at"`      // when the reply was built
	Started time.Time `json:"started"` // when this server started: the recently seen machines are those since then
	This    HostsThis `json:"this"`
	Hosts   []HostRow `json:"hosts"` // this server's hosts by availability, then newest; never nil
	// Switchyard is present while this server publishes to one.
	Switchyard *HostsSwitchyard `json:"switchyard,omitempty"`
	Runs       []HostsRun       `json:"runs"` // the data directory's open managed runs, newest first; never nil
	Ledger     HostsLedger      `json:"ledger"`
	More       HostsMore        `json:"more"`
	// Other is present only while a registered sync store has been current
	// in this server's life and is not signed out (PR-5).
	Other *HostsOther `json:"other,omitempty"`
}

// HostsThis is this server's own row.
type HostsThis struct {
	Key          string        `json:"key"`  // its machine's group key, the one a host of its machine is kept under
	Kind         string        `json:"kind"` // HostKindServer
	Name         string        `json:"name"`
	Platform     string        `json:"platform"`
	Version      string        `json:"version"`
	Availability string        `json:"availability"` // HostHere
	Sessions     HostsSessions `json:"sessions"`     // this server's own sessions
	// Hosted are the sessions hosted from this server's own machine (a host
	// whose key is this server's): no card of their own.
	Hosted *HostsHosted `json:"hosted,omitempty"`
	// Decisions are the open decision records of the runs this server
	// holds, by the run that holds each now (PR-4); null for every holder
	// but this server.
	Decisions *HostsDecisions `json:"decisions"`
}

// HostsSessions counts sessions: those starting, running or away, and
// those waiting for a person (waitsForPerson). WaitingRef names the one
// thing waiting when exactly one does (PR-4).
type HostsSessions struct {
	Running    int    `json:"running"`
	Waiting    int    `json:"waiting"`
	WaitingRef string `json:"waitingRef,omitempty"`
}

// HostsHosted are the sessions hosted from this server's own machine.
type HostsHosted struct {
	Sessions   HostsSessions `json:"sessions"`
	SessionIDs []string      `json:"sessionIds"`               // at most hostsMaxSessionIDs, newest first
	MoreIDs    int           `json:"moreSessionIds,omitempty"` // the ids left out
	Where      []string      `json:"where,omitempty"`          // the Runtimes its sessions run in, as their host reported them
}

// HostsDecisions counts open decision records.
type HostsDecisions struct {
	Open       int    `json:"open"`
	WaitingRef string `json:"waitingRef,omitempty"`
}

// HostRow is one machine hosting sessions here, or seen doing so since
// this server started.
type HostRow struct {
	Key          string        `json:"key"`             // the sessions' hostKey: a hash, never what the host sent
	Kind         string        `json:"kind"`            // HostKindHost
	Name         string        `json:"name"`            // the newest of its sessions' host names
	Names        int           `json:"names,omitempty"` // how many names its sessions report, when more than one
	Platform     string        `json:"platform,omitempty"`
	Version      string        `json:"version,omitempty"`
	User         string        `json:"user,omitempty"`
	Availability string        `json:"availability"` // HostConnected, HostAway or HostSeen
	Since        time.Time     `json:"since,omitzero"`
	Sessions     HostsSessions `json:"sessions"`
	SessionIDs   []string      `json:"sessionIds"`               // at most hostsMaxSessionIDs, newest first; empty when seen
	MoreIDs      int           `json:"moreSessionIds,omitempty"` // the ids left out
	Where        []string      `json:"where,omitempty"`          // the Runtimes its sessions run in, as their host reported them
	KeyFrom      string        `json:"keyFrom"`                  // session.HostKeyMachine, HostKeyProcess or HostKeySession
}

// HostsSwitchyard is the switchyard this server publishes to.
type HostsSwitchyard struct {
	URL        string `json:"url"` // scheme, host and path only
	Publishing int    `json:"publishing"`
	Connected  int    `json:"connected"`
}

// HostsRun is one open managed run of this data directory.
type HostsRun struct {
	ID     string `json:"id"`
	Name   string `json:"name"` // the crew's name, as the run's row holds it
	Crew   string `json:"crew"` // the crew's id
	State  string `json:"state"`
	Paused bool   `json:"paused"`
	// Waiting counts what waits for a person on the run; null when another
	// process holds the run, whose attention only it knows.
	Waiting    *int   `json:"waiting"`
	WaitingRef string `json:"waitingRef,omitempty"` // PR-4
	HeldBy     string `json:"heldBy"`               // the ledger's holder
}

// HostsLedger says who holds this data directory's managed runs, decided
// for this reply.
type HostsLedger struct {
	Holder string `json:"holder"` // HolderThis, HolderThisReadOnly, HolderOther, HolderNone or HolderUnknown
	Note   string `json:"note"`
}

// HostsMore counts the rows left out of the first half by its bounds.
type HostsMore struct {
	Hosts int `json:"hosts"`
	Runs  int `json:"runs"`
}

// HostsOther is the workspace's other machines, as a registered sync
// store last handed them in (PR-5).
type HostsOther struct {
	State    string         `json:"state"` // the sync status's: current, behind, unreachable or lapsed
	Since    time.Time      `json:"since,omitzero"`
	Machines []HostsMachine `json:"machines"` // empty while not shown; never nil
	More     HostsOtherMore `json:"more"`
}

// HostsMachine is one other server device of the workspace.
type HostsMachine struct {
	Device     string    `json:"device"`
	Name       string    `json:"name"`
	Platform   string    `json:"platform,omitempty"`
	Version    string    `json:"version,omitempty"`
	LastReport time.Time `json:"lastReport,omitzero"`
	Own        bool      `json:"own"` // one of the person's own servers: its summary is shown
	// Summary is its host summary, present only for the person's own servers.
	Summary   *HostsRemoteSummary `json:"summary,omitempty"`
	Runs      []HostsRemoteRun    `json:"runs"`
	Decisions HostsDecisions      `json:"decisions"` // its open decisions; never a link
}

// HostsRemoteSummary is another server's host summary as the reply carries
// it: at most hostsSummaryRows of its host rows, the rest counted.
type HostsRemoteSummary struct {
	Platform  string             `json:"platform"`
	Version   string             `json:"version"`
	Sessions  HostsSessions      `json:"sessions"`
	Runs      HostsRemoteRuns    `json:"runs"`
	Hosts     []HostsSummaryHost `json:"hosts"`
	MoreHosts int                `json:"moreHosts,omitempty"`
}

// HostsRemoteRuns counts the open runs another server reported holding.
type HostsRemoteRuns struct {
	Held    int `json:"held"`
	Waiting int `json:"waiting"`
}

// HostsSummaryHost is one host row of another server's summary.
type HostsSummaryHost struct {
	Name         string        `json:"name"`
	Platform     string        `json:"platform,omitempty"`
	Version      string        `json:"version,omitempty"`
	Availability string        `json:"availability"`
	Since        time.Time     `json:"since,omitzero"`
	Sessions     HostsSessions `json:"sessions"`
}

// HostsRemoteRun is a run another device reported: who reported it and
// when, never a holder.
type HostsRemoteRun struct {
	ID         string    `json:"id"`
	Crew       string    `json:"crew"`
	State      string    `json:"state"`
	Waiting    int       `json:"waiting"`
	ReportedBy string    `json:"reportedBy"`
	ReportedAt time.Time `json:"reportedAt,omitzero"`
}

// HostsOtherMore counts the rows left out of the second half.
type HostsOtherMore struct {
	Machines   int `json:"machines"`
	RemoteRuns int `json:"remoteRuns"`
}

// The words of the reply.
const (
	HostKindServer = "server"
	HostKindHost   = "host"

	HostHere      = "here"
	HostConnected = "connected"
	HostAway      = "away"
	HostSeen      = "seen"

	HolderThis         = "this"
	HolderThisReadOnly = "this_read_only"
	HolderOther        = "other"
	HolderNone         = "none"
	HolderUnknown      = "unknown"
)

// The reply's bounds (docs/protocol.md, GET /api/hosts).
const (
	hostsReplyMax      = 256 << 10 // the whole reply, encoded
	hostsFirstBudget   = 192 << 10 // this server, its hosts, the switchyard and the runs
	hostsOtherBudget   = 56 << 10  // other
	hostsMaxHostRows   = 256
	hostsMaxRuns       = 500
	hostsMaxSessionIDs = 64
	hostsMaxWhere      = 8
	hostsNameMax       = 120 // a host's or this server's name, a run's name and crew
	hostsPlatformMax   = 32
	hostsVersionMax    = 64
	hostsUserMax       = 64
	hostsNoteMax       = 200
	hostsRefMax        = 512 // a waitingRef: MB0's bound for an item's ref
	hostsMaxMachines   = 64
	hostsMaxRemoteRuns = 200 // per machine
	hostsSummaryRows   = 8   // a summary's host rows per machine
	hostsSeenMax       = 64
	hostsSeenFor       = 24 * time.Hour
	hostsURLMax        = 2048 // the switchyard's URL; a longer one is not shown
)

// handleHosts answers GET /api/hosts: admin only, no query, and nothing
// on a switchyard, which serves no workbench page.
func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "a switchyard has no host overview")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "GET /api/hosts takes no query")
		return
	}
	writeJSON(w, http.StatusOK, s.hostsReply(r.Context()))
}

// hostsReply builds the host overview from what the server holds now,
// each holder read under its own lock, once: the first half, then the
// workspace's other machines when the rule says so.
func (s *Server) hostsReply(ctx context.Context) HostsReply {
	reply := s.hostsFirstHalf(ctx)
	reply.Other = s.hostsOther(ctx)
	return reply
}

// hostsFirstHalf is the reply without other machines: this server, its
// hosts, the switchyard and the runs, within hostsFirstBudget.
func (s *Server) hostsFirstHalf(ctx context.Context) HostsReply {
	now := s.seen.clock()
	infos := s.registry.List()
	own := s.OwnHostKey()
	reply := HostsReply{At: now, Started: s.started.UTC(), Hosts: []HostRow{}, Runs: []HostsRun{}}
	reply.This = HostsThis{Key: own, Kind: HostKindServer, Name: s.hostsName(), Platform: cutBytes(machine.Platform(), hostsPlatformMax),
		Version: cutBytes(version.Version, hostsVersionMax), Availability: HostHere}
	byID := make(map[string]session.Info, len(infos))
	groups := map[string][]session.Info{}
	var order []string
	var hosted []session.Info
	for _, info := range infos {
		byID[info.ID] = info
		if info.Kind != session.KindHosted {
			countSession(&reply.This.Sessions, info)
			continue
		}
		if own != "" && info.HostKey == own {
			hosted = append(hosted, info)
			continue
		}
		key := info.HostKey
		if key == "" {
			// A session whose host the hub keyed by nothing is a machine of its own.
			key = "session:" + info.ID
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], info)
	}
	if len(hosted) > 0 {
		h := &HostsHosted{}
		for _, info := range hosted {
			countSession(&h.Sessions, info)
		}
		h.SessionIDs, h.MoreIDs = sessionIDs(hosted)
		h.Where = s.hostedWhere(hosted)
		reply.This.Hosted = h
	}
	rows := make([]HostRow, 0, len(groups))
	listed := make(map[string]bool, len(groups)+1)
	for _, key := range order {
		listed[key] = true
		rows = append(rows, s.hostRow(key, groups[key]))
	}
	if own != "" {
		listed[own] = true
	}
	sortHostRows(rows)
	rows = append(rows, s.seen.list(listed)...)
	reply.Switchyard = s.hostsSwitchyard()
	runs, moreRuns, led, decisions := s.hostsRuns(ctx, byID)
	reply.Ledger, reply.This.Decisions = led, decisions

	// The first half within its budget, row by row as encoded.
	used := hostsEncodedLen(reply.This) + hostsEncodedLen(reply.Switchyard) + hostsEncodedLen(reply.Ledger)
	for _, row := range rows {
		n := hostsEncodedLen(row) + 1
		if len(reply.Hosts) >= hostsMaxHostRows || used+n > hostsFirstBudget {
			reply.More.Hosts++
			continue
		}
		reply.Hosts = append(reply.Hosts, row)
		used += n
	}
	reply.More.Runs = moreRuns
	for _, run := range runs {
		n := hostsEncodedLen(run) + 1
		if len(reply.Runs) >= hostsMaxRuns || used+n > hostsFirstBudget {
			reply.More.Runs++
			continue
		}
		reply.Runs = append(reply.Runs, run)
		used += n
	}
	return reply
}

// hostsEncodedLen is v's length as json.Marshal writes it.
func hostsEncodedLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// hostsName is this server's machine's name, at most hostsNameMax bytes.
func (s *Server) hostsName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return cutBytes(name, hostsNameMax)
}

// waitsForPerson is the one predicate by which the host overview, its
// links and the host summary count a session as waiting for a person:
// not ended, its question shown, not answered by keys typed in its
// terminal (a question so answered keeps needs_input until the screen
// shows it gone, and waits for no one), and mirroring no decision: a
// decision's mirror is counted as its decision, from its record, while
// that is open (Attention.Decision).
func waitsForPerson(info session.Info) bool {
	a := info.Attention
	return !info.Status.Ended() && a.State == session.AttentionNeedsInput && a.Answered == "" && a.Decision == ""
}

// countSession counts info into c: running when starting, running or away
// (its host gone, in its grace); waiting by waitsForPerson, with the
// question's ref while it is the only thing waiting (waitingOne).
func countSession(c *HostsSessions, info session.Info) {
	switch info.Status {
	case session.StatusStarting, session.StatusRunning, session.StatusHostDisconnected:
		c.Running++
	}
	if waitsForPerson(info) {
		c.WaitingRef = waitingOne(c.Waiting, c.WaitingRef, sessionQuestionRef(info))
		c.Waiting++
	}
}

// sessionQuestionRef is the item ref of info's question shown, "" when it
// has no id or past hostsRefMax.
func sessionQuestionRef(info session.Info) string {
	if info.Attention.ID == "" {
		return ""
	}
	return boundedRef(questionRef(info.ID, info.Attention.ID))
}

// boundedRef is ref within hostsRefMax, else "": a cut ref names nothing.
func boundedRef(ref string) string {
	if len(ref) > hostsRefMax {
		return ""
	}
	return ref
}

// waitingOne is the ref a count links to once one more thing waits, given
// how many waited before and their ref: that thing's ref when it is the
// only one, "" past one.
func waitingOne(before int, _, ref string) string {
	if before == 0 {
		return ref
	}
	return ""
}

// sessionIDs are the ids of list (newest first), at most
// hostsMaxSessionIDs, and how many were left out.
func sessionIDs(list []session.Info) ([]string, int) {
	ids := make([]string, 0, min(len(list), hostsMaxSessionIDs))
	for _, info := range list {
		if len(ids) == hostsMaxSessionIDs {
			break
		}
		ids = append(ids, info.ID)
	}
	return ids, len(list) - len(ids)
}

// hostedWhere is the Runtimes the hosted sessions of list run in, as
// their hosts reported them (proto.HostReport.Where), each once, sorted,
// at most hostsMaxWhere.
func (s *Server) hostedWhere(list []session.Info) []string {
	var out []string
	for _, info := range list {
		d, ok := s.registry.Get(info.ID)
		if !ok {
			continue
		}
		hs, ok := d.(*signal.HostedSession)
		if !ok {
			continue
		}
		if w := hs.Report().Where; w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	slices.Sort(out)
	if len(out) > hostsMaxWhere {
		out = out[:hostsMaxWhere]
	}
	return out
}

// hostRow is the row of one machine's listed sessions (newest first).
func (s *Server) hostRow(key string, list []session.Info) HostRow {
	newest := list[0]
	row := HostRow{Key: newest.HostKey, Kind: HostKindHost, Name: cutBytes(newest.HostName, hostsNameMax),
		Platform: cutBytes(newest.HostPlatform, hostsPlatformMax), Version: cutBytes(newest.HostVersion, hostsVersionMax),
		User: cutBytes(newest.HostUser, hostsUserMax), KeyFrom: newest.HostKeyFrom}
	if row.KeyFrom == "" {
		row.KeyFrom = session.HostKeySession
	}
	names := map[string]bool{}
	up, down := false, false
	var away time.Time
	for _, info := range list {
		names[info.HostName] = true
		countSession(&row.Sessions, info)
		switch {
		case info.HostLink == session.HostLinkUp:
			up = true
		case info.HostLink == session.HostLinkDown:
			down = true
			if info.HostAwaySince != nil && info.HostAwaySince.After(away) {
				away = *info.HostAwaySince
			}
		case info.Status == session.StatusHostDisconnected:
			// A link the hub does not say: the status alone.
			down = true
		default:
			up = true
		}
	}
	if len(names) > 1 {
		row.Names = len(names)
	}
	switch {
	case up:
		row.Availability = HostConnected
	case down:
		row.Availability, row.Since = HostAway, away.UTC()
	default:
		row.Availability = HostConnected
	}
	row.SessionIDs, row.MoreIDs = sessionIDs(list)
	row.Where = s.hostedWhere(list)
	_ = key
	return row
}

// availabilityRank orders the rows: connected, away, then seen.
func availabilityRank(a string) int {
	switch a {
	case HostConnected:
		return 0
	case HostAway:
		return 1
	}
	return 2
}

// sortHostRows orders the listed machines by availability, then newest
// first: an away machine by when its last link dropped, a connected one
// by its newest session (the first of its ids, as the registry lists
// newest first, is kept in the row's order of the reply's list).
func sortHostRows(rows []HostRow) {
	slices.SortStableFunc(rows, func(a, b HostRow) int {
		if c := availabilityRank(a.Availability) - availabilityRank(b.Availability); c != 0 {
			return c
		}
		return b.Since.Compare(a.Since)
	})
}

// hostsSwitchyard is the switchyard this server publishes to, nil when it
// publishes to none.
func (s *Server) hostsSwitchyard() *HostsSwitchyard {
	if s.cfg.RendezvousServer() == "" {
		return nil
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.publisher == nil {
		return nil
	}
	sw := &HostsSwitchyard{URL: s.publisher.Server(), Publishing: len(s.published)}
	if len(sw.URL) > hostsURLMax {
		sw.URL = ""
	}
	for _, p := range s.published {
		if c, ok := p.(interface{ Connected() bool }); ok && c.Connected() {
			sw.Connected++
		}
	}
	return sw
}

// hostsRuns lists the data directory's open managed runs and says who
// holds them, decided now (docs/plans/hosts.md, the lock's states): the
// ledger's open rows for every holder, the engine's snapshot standing in
// for the rows it holds when this server holds them. A run this server
// holds counts what waits on it as distinct items: its open decisions, by
// the run that holds each now (current_run), and its members' questions
// that mirror no decision; the decisions of every such run are this
// server's decisions, nil under any other holder.
func (s *Server) hostsRuns(ctx context.Context, byID map[string]session.Info) ([]HostsRun, int, HostsLedger, *HostsDecisions) {
	led := s.hostsHolder()
	runs := []HostsRun{}
	if s.ledger == nil {
		return runs, 0, led, nil
	}
	rows, more, err := s.ledger.OpenRuns(ctx, hostsMaxRuns)
	if err != nil && !errors.Is(err, ledger.ErrUnavailable) {
		s.log.Warn("hosts: open runs", "err", err)
	}
	if led.Holder != HolderThis {
		for _, r := range rows {
			runs = append(runs, HostsRun{ID: r.ID, Name: cutBytes(r.CrewName, hostsNameMax), Crew: cutBytes(r.CrewID, hostsNameMax),
				State: r.State, Paused: r.Paused, HeldBy: led.Holder})
		}
		return runs, more, led, nil
	}
	held := map[string]crew.Run{}
	var fresh []HostsRun
	inRows := make(map[string]bool, len(rows))
	for _, r := range rows {
		inRows[r.ID] = true
	}
	decisions := &HostsDecisions{}
	for _, r := range s.runs.List() {
		if r.Managed == nil {
			continue
		}
		held[r.ID] = r
		if !inRows[r.ID] && !runFinal(r.State) {
			fresh = append(fresh, s.heldRun(ctx, r.ID, r.Name, r.CrewID, r.State, r.Paused, r.Members, byID, decisions))
		}
	}
	runs = append(runs, fresh...)
	for _, row := range rows {
		if r, ok := held[row.ID]; ok {
			if runFinal(r.State) {
				continue
			}
			runs = append(runs, s.heldRun(ctx, r.ID, r.Name, r.CrewID, r.State, r.Paused, r.Members, byID, decisions))
			continue
		}
		// A run only the ledger has (interrupted, or recovered after a
		// restart or a late lock): none of its attempts runs in any
		// process, so only its open decisions wait on it.
		runs = append(runs, s.heldRun(ctx, row.ID, row.CrewName, row.CrewID, row.State, row.Paused, nil, byID, decisions))
	}
	return runs, more, led, decisions
}

// runFinal reports whether an engine's run has ended.
func runFinal(state string) bool { return state == crew.RunFinished || state == crew.RunStopped }

// heldRun is a run this server holds: its members' questions that wait by
// waitsForPerson and its open decisions by current_run, counted with the
// lone item's ref in the same pass, the decisions added to all.
func (s *Server) heldRun(ctx context.Context, id, name, crewID, state string, paused bool, members []crew.MemberState,
	byID map[string]session.Info, all *HostsDecisions) HostsRun {
	waiting, ref := 0, ""
	for _, m := range members {
		if info, ok := byID[m.SessionID]; ok && m.SessionID != "" && waitsForPerson(info) {
			ref = waitingOne(waiting, ref, sessionQuestionRef(info))
			waiting++
		}
	}
	ds, err := s.ledger.Decisions(ctx, ledger.DecisionFilter{States: []string{ledger.DecisionOpen}, CurrentRun: id, Limit: ledger.MaxDecisionsPage})
	if err != nil && !errors.Is(err, ledger.ErrUnavailable) {
		s.log.Warn("hosts: a run's decisions", "run", id, "err", err)
	}
	for _, d := range ds {
		dref := boundedRef("decision:" + d.ID())
		ref = waitingOne(waiting, ref, dref)
		waiting++
		all.WaitingRef = waitingOne(all.Open, all.WaitingRef, dref)
		all.Open++
	}
	return HostsRun{ID: id, Name: cutBytes(name, hostsNameMax), Crew: cutBytes(crewID, hostsNameMax), State: state, Paused: paused,
		Waiting: &waiting, WaitingRef: ref, HeldBy: HolderThis}
}

// lockLine is a lock file's first line in the form the ledger writes it.
var lockLine = regexp.MustCompile(`^pid ([0-9]{1,10}) since (\S+)$`)

// hostsHolder says who holds the data directory's managed runs, read for
// this reply: the ledger's own view while it owns the lock, a probe of
// the lock otherwise. Every note is made here from structured facts of
// that moment, never from words the ledger or the lock kept.
func (s *Server) hostsHolder() HostsLedger {
	if s.store == nil {
		return HostsLedger{Holder: HolderNone, Note: "this server keeps no data directory"}
	}
	var lv ledger.LockView
	if s.ledger != nil {
		lv = s.ledger.Lock()
		if lv.Owned && !lv.ReadOnly {
			return HostsLedger{Holder: HolderThis}
		}
		if lv.Owned {
			return HostsLedger{Holder: HolderThisReadOnly, Note: cutBytes(lv.Why, hostsNoteMax)}
		}
	}
	probe := s.probeLock
	if probe == nil {
		probe = ledger.Probe
	}
	pr, err := probe(filepath.Join(s.store.Dir(), "runs"))
	if err != nil {
		class := "other"
		switch {
		case errors.Is(err, fs.ErrPermission):
			class = "permission"
		case errors.Is(err, fs.ErrNotExist):
			class = "missing"
		}
		return HostsLedger{Holder: HolderUnknown, Note: "the lock file could not be read (" + class + ")"}
	}
	switch pr.State {
	case ledger.ProbeHeld:
		return HostsLedger{Holder: HolderOther, Note: holderLine(pr.Line)}
	case ledger.ProbeNoLocks:
		return HostsLedger{Holder: HolderNone, Note: "the data directory's filesystem has no locks"}
	}
	note := ""
	switch {
	case lv.NoLocks:
		note = "the data directory's filesystem has no locks"
	case !lv.GaveUpAt.IsZero():
		note = "this server stopped waiting for the lock at " + lv.GaveUpAt.UTC().Format(time.RFC3339)
	case !lv.WaitingSince.IsZero():
		note = "this server is taking the lock, waiting since " + lv.WaitingSince.UTC().Format(time.RFC3339)
	case lv.Closed:
		note = "this server's ledger is closed"
	default:
		note = "this server could not open its ledger"
	}
	return HostsLedger{Holder: HolderNone, Note: note}
}

// holderLine is the note of a lock another process holds: its pid and
// time as it wrote them, when they are in the ledger's form.
func holderLine(line string) string {
	m := lockLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "its line is not readable"
	}
	at, err := time.Parse(time.RFC3339, m[2])
	if err != nil {
		return "its line is not readable"
	}
	return cutBytes("pid "+m[1]+" since "+at.UTC().Format(time.RFC3339), hostsNoteMax)
}
