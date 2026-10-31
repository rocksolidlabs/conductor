package crew

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A planned launch (docs/plans/dispatch-evidence.md, Program item DS2): a
// managed run launched from a Dispatch ticket's approved plan revision,
// under the run id Dispatch minted when it recorded the launch, at the
// revision's base, never HEAD read again, carrying the launch package byte
// for byte. Dispatch records the launch before the run starts, so the id is
// fixed first: the run's first ledger transaction writes its row with an
// insert that refuses an id the ledger has (ledger.Tx.CreateRun), and so
// does every new run's, an ordinary launch's and a continuation's, so no
// launch takes another's id or overwrites its row.

// ErrRunExists refuses a run id that is another run's: the engine holds a
// run of it, or the ledger has its row (ledger.ErrRunExists, which it is).
// The run there is untouched.
var ErrRunExists = ledger.ErrRunExists

// maxMints is how many ids an ordinary launch or a continuation mints
// before it gives up with ErrRunExists: each minted id the ledger has
// already (a planned run's, after a restart that left the engine without
// it) is minted again, nothing having been made for it.
const maxMints = 3

// Planned is what a planned launch is given: the run id Dispatch minted,
// the revision's base (a full commit id of the crew's repository), and the
// package with its SHA-256 (hex), which the engine checks.
type Planned struct {
	RunID   string
	Base    string
	Digest  string
	Package []byte
}

// LaunchPlanned launches c, a crew with tasks, as LaunchNamed does, under
// p's run id, at p's base, with p's package: the id must be one of c's
// (ValidRunID, c's id before its last dash) and the package one whose
// SHA-256 is p.Digest, whose launch names the id and whose plan names the
// base, else ErrInvalid; the base must be a commit of c's repository,
// checked before anything is read at it (ErrInvalid). An id the engine
// holds, or one whose row the ledger has (found by the run's first
// transaction, the insert being the check), is refused with ErrRunExists
// before any attempt is made, and the run there is left as it was: a
// planned id is never minted again. The run's row keeps the ticket's id
// and the package, its run.created names the ticket's key and the digest,
// and its read says which ticket and revision it was launched from
// (Run.Ticket).
func (e *Engine) LaunchPlanned(ctx context.Context, c Crew, label string, p Planned) (*Run, func(), error) {
	noop := func() {}
	if err := ValidateLabel(label); err != nil {
		return nil, noop, err
	}
	if !c.Managed() {
		return nil, noop, invalidf("a planned launch is of a crew with tasks")
	}
	if !ValidRunID(p.RunID) || p.RunID[:strings.LastIndexByte(p.RunID, '-')] != c.ID {
		return nil, noop, invalidf("the run id %s is not one of crew %s's", quote(p.RunID), quote(c.ID))
	}
	if !fullCommit(p.Base) {
		return nil, noop, invalidf("the base %s is not a full commit id", quote(p.Base))
	}
	if len(p.Package) > ledger.MaxPackage {
		return nil, noop, invalidf("the package is %d bytes, more than %d", len(p.Package), ledger.MaxPackage)
	}
	if got := packageDigest(p.Package); got != p.Digest {
		return nil, noop, invalidf("the package's SHA-256 is %s, not the digest given (%s)", got, quote(p.Digest))
	}
	pkg, err := readPackage(p.Package)
	if err != nil {
		return nil, noop, err
	}
	if pkg.p.Launch.Run != p.RunID {
		return nil, noop, invalidf("the package is the launch of run %s, not of %s", quote(pkg.p.Launch.Run), quote(p.RunID))
	}
	// The base is the revision's: the one its package was built at, its
	// references' objects resolved there.
	if pkg.p.Plan.Base != p.Base {
		return nil, noop, invalidf("the package's plan was approved at the base %s, not at %s", quote(pkg.p.Plan.Base), quote(p.Base))
	}
	// A commit of the repository before anything is read at it (prepare
	// reads a file-form manifest there, whose failure is git's). A cwd
	// that is no absolute path is prepare's to refuse.
	if filepath.IsAbs(c.Cwd) {
		if err := checkCommit(ctx, c.Cwd, p.Base); err != nil {
			return nil, noop, err
		}
	}
	prep, err := e.prepare(ctx, c, p.Base)
	if err != nil {
		return nil, noop, err
	}
	r, err := e.addRun(c, prep, label, p.RunID, pkg)
	if err != nil {
		return nil, noop, err
	}
	release := sync.OnceFunc(func() { e.launched(r) })
	if e.afterAdd != nil {
		e.afterAdd(r.id)
	}
	run, err := e.startImmediate(ctx, r)
	return run, release, err
}

// checkCommit refuses a base that is not a commit of the repository dir
// is in (ErrInvalid), with git's words.
func checkCommit(ctx context.Context, dir, commit string) error {
	// commit is a full commit id (fullCommit), never an option.
	out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
	if err != nil || strings.TrimSpace(out) != commit {
		return invalidf("the base %s is not a commit of the repository at %s", short(commit), dir)
	}
	return nil
}

// mintRunID gives a new run of the crew its id: the crew's id, a dash and
// eight hex digits, through the newRunID seam when a test sets it. The
// caller holds e.mu.
func (e *Engine) mintRunID(crewID string) string {
	if e.newRunID != nil {
		return e.newRunID(crewID)
	}
	return crewID + "-" + session.NewID()[:8]
}

// minted reports whether a launch's or a continuation's error is the
// ledger's refusal of the id it minted (ErrRunExists from the run's first
// transaction), which a new id answers: nothing was made for that one.
func minted(err error, try int) bool {
	return errors.Is(err, ErrRunExists) && try < maxMints
}

// ticketOfRow is the binding of the run a ledger row keeps (Run.Ticket),
// nil for a run no ticket launched or a package that cannot be read: what
// a run's record says when it is rebuilt from the ledger.
func ticketOfRow(row ledger.RunRow) *RunTicket {
	rp, err := packageOfRow(row)
	if err != nil {
		return nil
	}
	return rp.ticket()
}
