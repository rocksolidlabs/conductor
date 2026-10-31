// Package ext is Conductor's extension API: the eight interfaces through
// which code outside this repository extends the app, the records they
// trade, their bounds and waits, and the registry that holds them.
//
// # Providers
//
// A provider is code in another module that implements one or more of the
// interfaces below and registers itself:
//
//   - IdentityProvider: who presents a token the server's own chain (the
//     workbench token, the host tokens, the share links) does not know.
//   - DecisionRouter: whom a question a session asks goes to, and an answer
//     the router already holds (Switchman's team routing).
//   - ContextProvider: context an attempt of a managed run reads before it
//     starts, and the files the agents touched.
//   - NotificationSink: the sessions' activity, delivered elsewhere (mobile
//     push, for one).
//   - ContinuityStore: every document of the data directory after its local
//     commit, and a copy to restore on another device (sync; "continuity"
//     is the word for a run continued after a restart, so the feature and
//     its kind are called sync).
//   - EntitlementSource: what the signed-in workspace may use of a paid
//     feature. Only providers ask it; no code of this repository does.
//   - MediaRoomProvider: ICE servers beside the configured ones, and rooms.
//   - Runtime: where an attempt and its checks run (its methods come with
//     the Runtime seam; until then it names its Where only).
//
// None of them is implemented in this repository. Each has a free default
// (package ext/local) that does what Conductor does without it, and a plain
// build of this repository registers nothing, so every seam runs its
// default. No configuration key, flag, environment variable or build tag
// turns a provider on: a provider exists only when another module's main
// compiles it in.
//
// # Registering
//
// A provider registers in its package's init, with the Register function of
// each interface it implements, and does nothing else there: no goroutine,
// no file, no network. That module's main blank-imports the package and
// calls app.Main:
//
//	package main
//
//	import (
//		"github.com/rocksolidlabs/conductor/app"
//		_ "example.com/provider/register"
//	)
//
//	func main() { app.Main() }
//
// Every provider has a name (ValidName: 1 to 40 of a-z, 0-9 and '-'), which
// the logs, the server's notes and GET /api/capabilities give it. One value
// may register in several slots under one name (a router that is also a
// sink); one name is always one value. A Register panics on a nil value, a
// bad name, a second provider in a one-value slot, a repeated name in a
// list, a list past MaxPerList, a name another slot holds with a different
// value, and after Freeze. The server calls Freeze once, at start, and reads
// what the registry holds then; a provider never appears mid-run.
//
// # Starting
//
// A provider that needs the server implements Starter. Start is called
// after the server is made and before it serves, with a Host: the
// provider's one door into the app (launch a crew's run, type into a
// session or answer its question, send a notice, a directory of its own, a
// logger). Start returns at once; reaching the provider's service is the
// work of goroutines it begins, which end with Start's context. A Start
// that errs, panics or has not returned within StartWait is tried again
// later, with backoff, never while an earlier try is still running.
//
// # The guard, from the provider's side
//
// Every method answers from what the provider holds on this machine, never
// with a round trip to its service on the call's path: the provider keeps
// what it holds current on its own goroutines. The one exception is
// ContinuityStore.Restore, which only `conductor restore --provider` calls,
// at a person's word, with no server running.
//
// The server calls every provider through a guard. Each call has a wait
// (IdentityWait, RouteWait, ContextWait, ICEWait, DeliveryWait, StartWait),
// a bound of a hang and never what a normal answer takes; a call past it,
// one that errs or panics, and an answer its record's Valid refuses are
// answered with the free default instead, call by call, with nothing
// remembered about the failure, so a provider whose service comes back is
// used again at its next call. Calls fed from queues (NoteTouch, Notify)
// never hold the code that queues them: a full queue drops its oldest.
// While a provider has not started, or its service is unreachable,
// Conductor is the open-source build, mid-session, with no restart.
//
// # Records
//
// The records carry JSON tags in the style of Conductor's protocol
// (camelCase, omitempty on what may be absent, omitzero on times), so a
// hosted service can take them as its wire records. Every bound and wait
// is a constant of this package, and every record a provider hands the
// server has a Valid method the guard applies and the provider can call
// itself. Two returned records have none, since no code of this
// repository takes them: RoomJoin (Room has no caller until Program item
// CL1's rooms, which add its check with it) and Entitlement (the private
// repositories' providers ask the entitlement source; this repository
// never does).
//
// # Stability
//
// ext, ext/local, ext/exttest and app are this module's public API; every
// other package is internal. A change that removes or renames a field, or
// changes a method, is a breaking change of that API and is named in its
// pull request's title. Package ext imports the standard library only.
package ext
