package ext

import "time"

// UsageObserver is an identity provider that is told what the host
// connections it admitted used on a switchyard, so that a hosted service
// can bound the relay it runs for the hosts it admits. It is asserted on
// the value in the identity slot, as UplinkIdentity and IdentityDetail
// are, and asked nothing else.
//
// The server calls Used only in switchyard mode, and only for a host
// connection admitted through the provider's principal (a credential none
// of the server's own, with the host or the admin grant): never for the
// workbench token, a host token, an open host or a share link, and never
// on a server that is no switchyard. It hands one Usage for each such
// connection every UsageEvery, one at each change of the connection's
// credential, and one at its close (Final).
//
// Used is called from a queue of UsageQueue entries, one goroutine at a
// time, in the order the reports were made: a full queue drops its oldest
// report and counts the drop, so a provider that is slow, blocks or
// panics never holds the relay, and loses reports instead. Used answers
// from what it holds (a count it keeps, a queue of its own to its
// service), never with a round trip on the call's path. It gates nothing:
// what it is told changes nothing the server does.
type UsageObserver interface {
	Used(u Usage)
}

// Usage is what one host connection used over From..To, as the server's
// own relay counted it, under one credential.
//
// A renewal of the connection's credential (the host's host_token) that
// presents another credential ends a stretch: the report up to it is made
// under the old credential's hash before the new one takes effect, and the
// counts start again; a renewal with the same credential ends nothing. So
// every report's counts belong to the one credential it names. The
// reports of a connection tile its life: each From is the previous To.
//
// What is still queued when the server shuts down is not delivered: a
// connection whose Final never came ended at its last report's To, or
// later, before the server stopped.
type Usage struct {
	// Connection is an opaque id the server makes for each host
	// connection, UsageConnectionLen lower-case hex characters: the same
	// in every report of the connection and in its Final, never that of
	// another (a reconnect is a new connection).
	Connection string `json:"connection"`
	// Credential is the SHA-256 of the credential that admitted the
	// connection for the whole of From..To, as the host presented it: the
	// bytes the provider judged. The credential itself is in no field.
	Credential [32]byte `json:"credential"`
	// Subject is the principal's subject, at most MaxSubject bytes.
	Subject string `json:"subject"`
	// Sessions is 1 for the connection of a hosted session and 0 for a
	// run's desk, which is a host connection and no session.
	Sessions int `json:"sessions"`
	// SessionSeconds is the whole seconds of From..To the session was
	// registered on the connection, the fraction carried to the next
	// report, so the reports of a connection add up to its time; 0 for a
	// desk.
	SessionSeconds int64 `json:"sessionSeconds"`
	// RelayedBytes is what the server's relay counted for the connection
	// over From..To, both ways: what the host relayed to its viewers and
	// what its viewers relayed to it.
	RelayedBytes int64 `json:"relayedBytes"`
	// Final marks the connection's last report, made as it closed.
	Final bool      `json:"final,omitempty"`
	From  time.Time `json:"from,omitzero"`
	To    time.Time `json:"to,omitzero"`
}

const (
	// UsageEvery is how often a connection's report is made between its
	// credential's changes.
	UsageEvery = 60 * time.Second
	// UsageQueue is how many reports wait for Used, at most.
	UsageQueue = 256
	// UsageConnectionLen is the length of a Usage's Connection.
	UsageConnectionLen = 32
)
