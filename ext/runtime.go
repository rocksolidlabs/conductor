package ext

// Runtime is where an attempt's process and its checks run. Today it only
// names its Where; its methods (an attempt's process, a check where its
// attempt ran) and its native default come with the Runtime seam
// (docs/plans/runtime.md), before anything implements it. Until then the
// one runtime is native: processes on the machine that runs the session,
// as the user.
type Runtime interface {
	Provider
	Where() Where
}

// Where names a runtime, as the ledger's evidence rows will record it.
type Where string

// WhereNative is the native runtime: a PTY for the attempt and
// internal/proc for its checks, on the machine that runs the session.
const WhereNative Where = "native"

// MaxWhere bounds a Where, in bytes.
const MaxWhere = 32

// Valid checks w: 1 to MaxWhere of a-z, 0-9 and '-', beginning with a
// letter.
func (w Where) Valid() error {
	if !slug(string(w), MaxWhere, true) {
		return invalid("where", "%q is not 1 to %d of a-z, 0-9 and '-' beginning with a letter", string(w), MaxWhere)
	}
	return nil
}
