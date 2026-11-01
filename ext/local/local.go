// Package local holds the free defaults of package ext's interfaces, and
// Fill, which puts them in every slot no provider is registered in.
//
// Each default does what Conductor does without a provider, which is what
// a plain build of this repository does: it recognises no token, routes no
// question, keeps no copy of the data directory, allows every entitlement,
// and adds no ICE server and no room. Context providers and notification
// sinks are lists, empty by default, so they need no default; the Runtime's
// native default comes with the Runtime seam (docs/plans/runtime.md).
//
// Every default is a value of an empty struct: it holds nothing, so it is
// safe for concurrent use, and its name is Name ("local").
//
// Fill is the one place any code asks whether a provider is registered.
// The composition root calls it once, at start, on what ext.Freeze
// answers, and every seam then calls its interface without a condition.
// Nothing in this package checks a licence, a plan, a tier or an account.
//
// Package local imports the standard library and ext only.
package local

import "github.com/rocksolidlabs/conductor/ext"

// Name is the name of every free default.
const Name = "local"

// Fill answers p with the free default in every slot nothing is registered
// in: the identity provider, the decision router, the sync store and the
// media provider. A registered provider is kept as it is; so are the lists
// (context providers and notification sinks, empty when none is
// registered), the runtime (whose native default is the Runtime seam's) and
// All, which lists what is registered and so never a default. It is the one
// place any code asks whether a provider is registered.
func Fill(p ext.Providers) ext.Providers {
	if p.Identity == nil {
		p.Identity = Identity{}
	}
	if p.Router == nil {
		p.Router = Router{}
	}
	if p.Continuity == nil {
		p.Continuity = Continuity{}
	}
	if p.Media == nil {
		p.Media = Media{}
	}
	return p
}
