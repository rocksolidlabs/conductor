package api

import "time"

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
	Key          string        `json:"key"`  // the sessions' hostKey: a hash, never what the host sent
	Kind         string        `json:"kind"` // HostKindHost
	Name         string        `json:"name"` // the newest of its sessions' host names
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
)
