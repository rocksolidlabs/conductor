// Package tracker is what an external tracker is to Conductor
// (docs/plans/tracker-linking.md, Program item IN1a): the adapter contract
// a system's adapter implements (Adapter), the records a read gives (Item,
// CheckSet, Target, Comment, Account), the credential an adapter alone
// opens (Credential), the typed errors the API maps to its answers, and
// the rules shared by every adapter: a reference's parsing (ref.go), an
// item's steps to reproduce (repro.go) and the hosts an image or a job's
// log may be fetched from (images.go). It is system-neutral, so another
// system can be an adapter later (Program item IN1b) without a change to
// Dispatch or the API; GitHub, in internal/tracker/github, is the only one
// here. It holds no HTTP server and no store, imports the standard library
// alone, and is imported by the API (which joins it to Dispatch's store)
// and by its adapters.
package tracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// Kinds of item: an issue, or a change (a pull request; a merge request
// later).
const (
	KindIssue  = "issue"
	KindChange = "change"
)

// States of an item, as the system says them.
const (
	StateOpen   = "open"
	StateClosed = "closed"
	StateMerged = "merged"
)

// The bounds of what a read gives. An adapter cuts to them (saying so
// where the record has a word for it) and never hands back more.
const (
	MaxSpace         = 200
	MaxID            = 20
	MaxNodeID        = 100
	MaxTitle         = 1024
	MaxBody          = 256 << 10
	MaxLabels        = 50
	MaxAssignees     = 20
	MaxImages        = 32
	MaxChecks        = 100
	MaxIncomplete    = 8
	MaxIncompleteLen = 120
	MaxRemoteID      = 64
	MaxMarker        = 128
)

// Ref is where an item is: System its system (github), Host the system's
// host (github.com, or a GitHub Enterprise Server host name, lower case,
// with a port only in tests), Space its repository's owner/name (at most
// MaxSpace bytes), Kind issue or change, ID its number as text (at most
// MaxID bytes).
type Ref struct {
	System string `json:"system"`
	Host   string `json:"host"`
	Space  string `json:"space"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

// String is the reference as a person writes it: owner/name#12.
func (r Ref) String() string { return r.Space + "#" + r.ID }

// Key is the reference as one string that names its system, host and
// kind: github:<host>/<space>/<kind>/<id>.
func (r Ref) Key() string {
	return r.System + ":" + r.Host + "/" + r.Space + "/" + r.Kind + "/" + r.ID
}

// RemoteID is an identity the remote system gives (an account's, an
// item's database id, a repository's, a comment's): an opaque string of 1
// to MaxRemoteID bytes of visible ASCII, GitHub's numbers written in
// decimal. Conductor compares one and never parses it, orders by it or
// does arithmetic on it, so a system whose ids are UUIDs keeps them as
// they are.
type RemoteID string

// CheckRemoteID refuses an id that is empty, longer than MaxRemoteID bytes
// or holds a byte outside visible ASCII.
func CheckRemoteID(id RemoteID) error {
	if id == "" || len(id) > MaxRemoteID {
		return fmt.Errorf("tracker: a remote id is 1 to %d bytes, got %d", MaxRemoteID, len(id))
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return errors.New("tracker: a remote id is visible ASCII")
		}
	}
	return nil
}

// Validators are what the caller holds of its last read, sent so an
// unchanged item is answered "not modified": the issue's ETag and, for a
// change, the pull request's; NodeID is the item's node id the caller
// holds (” on a first read), compared when a read is answered from a
// redirect.
type Validators struct {
	Issue  string `json:"issue,omitempty"`
	Pull   string `json:"pull,omitempty"`
	NodeID string `json:"nodeId,omitempty"`
}

// Item is what a read gives.
type Item struct {
	Ref           Ref        `json:"ref"`
	NodeID        string     `json:"nodeId"`
	DatabaseID    RemoteID   `json:"databaseId"`
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	BodyTruncated bool       `json:"bodyTruncated,omitempty"`
	State         string     `json:"state"`
	StateReason   string     `json:"stateReason,omitempty"`
	Labels        []string   `json:"labels,omitempty"`
	Assignees     []string   `json:"assignees,omitempty"`
	Milestone     string     `json:"milestone,omitempty"`
	Author        string     `json:"author,omitempty"`
	AuthorID      RemoteID   `json:"authorId,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	ClosedAt      time.Time  `json:"closedAt,omitzero"`
	Merged        bool       `json:"merged,omitempty"`
	MergedAt      time.Time  `json:"mergedAt,omitzero"`
	MergeSHA      string     `json:"mergeSha,omitempty"`
	HeadSHA       string     `json:"headSha,omitempty"`
	BaseRef       string     `json:"baseRef,omitempty"`
	Images        []ImageRef `json:"images,omitempty"`
	// ImagesLeftOut counts the body's images Images does not hold, by
	// reason, so a caller can say "N images not imported".
	ImagesLeftOut ImagesLeftOut `json:"imagesLeftOut,omitzero"`
	Validators    Validators    `json:"validators"`
}

// ImagesLeftOut counts an item's images a read left out: Unlisted those on
// a host the image rule does not admit, PastBound those past MaxImages.
type ImagesLeftOut struct {
	Unlisted  int `json:"unlisted,omitempty"`
	PastBound int `json:"pastBound,omitempty"`
}

// LogTail is the end of a check's log: Bytes its last bytes (at most the
// tail asked for), Cut true when the log is longer than Bytes (its start
// was not asked for or not kept), Size the log's length in bytes when the
// system said it (0 when it did not).
type LogTail struct {
	Bytes []byte `json:"bytes"`
	Cut   bool   `json:"cut,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

// Check is one CI job on a change at a commit. ID names its kind and the
// system's id (GitHub's status:<id>, job:<id> or run:<id>); HasLog says
// CheckLog can read its log.
type Check struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion,omitempty"`
	URL         string    `json:"url,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	CompletedAt time.Time `json:"completedAt,omitzero"`
	HeadSHA     string    `json:"headSha,omitempty"`
	App         string    `json:"app,omitempty"`
	HasLog      bool      `json:"hasLog,omitempty"`
}

// Failed reports a check that ended in failure (a failure, a cancel, a
// timeout, an action required of a person or a start that failed).
func (c Check) Failed() bool {
	switch c.Conclusion {
	case "failure", "cancelled", "timed_out", "action_required", "startup_failure", "error":
		return true
	}
	return false
}

// Pending reports a check that has not finished.
func (c Check) Pending() bool { return c.Status != "completed" }

// CheckSet is the checks of a change at a commit: at most MaxChecks;
// Incomplete the words of every reason the set is not every check the
// system has at the commit (at most MaxIncomplete entries of at most
// MaxIncompleteLen bytes; empty only when every listing was read to its
// end and nothing was cut), Omitted the checks the system counted that
// the set does not hold, where its totals say. A set with Incomplete is
// never "all passed" (Passed).
type CheckSet struct {
	Checks     []Check  `json:"checks"`
	Incomplete []string `json:"incomplete,omitempty"`
	Omitted    int      `json:"omitted,omitempty"`
}

// Passed reports a set that is whole, not empty, and every check of it
// completed with a conclusion that is a pass: success, neutral or skipped.
// Any other (a failure, stale, one not known) is not.
func (s CheckSet) Passed() bool {
	if len(s.Incomplete) > 0 || len(s.Checks) == 0 {
		return false
	}
	for _, c := range s.Checks {
		if c.Pending() || !c.Succeeded() {
			return false
		}
	}
	return true
}

// Succeeded reports a check that completed with a conclusion that is a
// pass: success, neutral or skipped.
func (c Check) Succeeded() bool {
	if c.Status != "completed" {
		return false
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

// AddIncomplete adds a reason the set is cut, cut to MaxIncompleteLen
// bytes at a rune boundary; past MaxIncomplete reasons the last one says
// that more were left out.
func (s *CheckSet) AddIncomplete(words string) {
	if len(s.Incomplete) < MaxIncomplete {
		s.Incomplete = append(s.Incomplete, cutBytes(words, MaxIncompleteLen))
		return
	}
	s.Incomplete[MaxIncomplete-1] = "more reasons than Conductor keeps; see the checks on the tracker"
}

// Target is where a write goes, read by the system's own id: NodeID the
// item's stable id, Kind issue or change as the system types the item
// (never as a URL's path says), SpaceID the stable id of the repository
// holding it now (one a rename keeps and a new repository of a reused name
// does not have), Ref and URL its place now, State open, closed or merged.
type Target struct {
	NodeID  string   `json:"nodeId"`
	Kind    string   `json:"kind"`
	SpaceID RemoteID `json:"spaceId"`
	Ref     Ref      `json:"ref"`
	URL     string   `json:"url"`
	State   string   `json:"state"`
}

// Comment is a comment on an item.
type Comment struct {
	ID        RemoteID  `json:"id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
	AuthorID  RemoteID  `json:"authorId"`
	Author    string    `json:"author"`
}

// Check refuses a comment whose ids are not RemoteIDs.
func (c Comment) Check() error {
	if err := CheckRemoteID(c.ID); err != nil {
		return err
	}
	return CheckRemoteID(c.AuthorID)
}

// Account is the account a credential belongs to; Scopes are a classic
// token's scopes (none for a fine-grained token, which has permissions).
type Account struct {
	Login  string   `json:"login"`
	ID     RemoteID `json:"id"`
	Scopes []string `json:"scopes,omitempty"`
}

// Check refuses an account whose id is not a RemoteID.
func (a Account) Check() error { return CheckRemoteID(a.ID) }

// Match is what FindComment looks for: a comment whose body holds Marker
// and whose normalised body (BodyDigest) has the SHA-256 BodySha256, and,
// when AuthorID is not ”, whose author is AuthorID.
type Match struct {
	Marker     string   `json:"marker"`
	BodySha256 string   `json:"bodySha256"`
	AuthorID   RemoteID `json:"authorId,omitempty"`
}

// Check refuses a match that could match nothing in particular.
func (m Match) Check() error {
	if m.Marker == "" || len(m.Marker) > MaxMarker {
		return fmt.Errorf("tracker: a marker is 1 to %d bytes", MaxMarker)
	}
	if b, err := hex.DecodeString(m.BodySha256); err != nil || len(b) != sha256.Size || strings.ToLower(m.BodySha256) != m.BodySha256 {
		return errors.New("tracker: a match's body digest is a SHA-256 in lower-case hex")
	}
	if m.AuthorID != "" {
		return CheckRemoteID(m.AuthorID)
	}
	return nil
}

// Matches reports whether a comment with this body and author is m's.
func (m Match) Matches(body string, author RemoteID) bool {
	if m.AuthorID != "" && author != m.AuthorID {
		return false
	}
	return m.MatchesBody(body)
}

// MatchesBody reports a body that holds m's marker and whose normalised
// bytes have m's digest, whoever wrote it.
func (m Match) MatchesBody(body string) bool {
	return m.Marker != "" && strings.Contains(body, m.Marker) && BodyDigest(body) == m.BodySha256
}

// BodyDigest is the SHA-256, in hex, of a comment's body normalised as a
// system may return it: "\r\n" made "\n" and trailing white space trimmed.
func BodyDigest(body string) string {
	body = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\r\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// Found is what FindComment says of its window.
type Found string

const (
	// FoundYes: a comment in the window matches.
	FoundYes Found = "yes"
	// FoundNo: the whole window was read and no comment in it matches.
	FoundNo Found = "no"
	// FoundUnknown: the window was not read whole (a page past the
	// bound, a failure).
	FoundUnknown Found = "unknown"
)

// Close reasons.
const (
	ReasonCompleted  = "completed"
	ReasonNotPlanned = "not_planned"
)

// Adapter is a system's adapter. Every method is safe for concurrent use.
// The two writes (Comment, Close) take a Target, never a Ref, so no
// repository path, renamed or reused, can turn a write to another item;
// both refuse a credential not tagged for them (ForWrite) with
// ErrUnsupported before any request, and Close refuses a Target of kind
// change. An adapter never retries a request by itself.
type Adapter interface {
	// System is the adapter's system name, its key in Adapters.
	System() string
	// Parse reads an item's reference: a URL, or a short form with the
	// repository taken from defaultSpace (owner/name, a repository's URL
	// or a git remote).
	Parse(raw string, defaultSpace string) (Ref, error)
	// Whoami reads the account the credential belongs to.
	Whoami(ctx context.Context, c Credential) (Account, error)
	// Read reads an item; the bool is true when every request it took
	// was answered "not modified" for v, and the Item is then empty.
	Read(ctx context.Context, c Credential, r Ref, v Validators) (Item, bool, error)
	// Image downloads one of a read's images, at most max bytes, with
	// its media type.
	Image(ctx context.Context, c Credential, img ImageRef, max int64) (io.ReadCloser, string, error)
	// Checks reads a change's checks at a commit.
	Checks(ctx context.Context, c Credential, r Ref, sha string) (CheckSet, error)
	// CheckLog reads the last tail bytes of a check's log, saying when the
	// log is longer than what it gives.
	CheckLog(ctx context.Context, c Credential, r Ref, check Check, tail int) (LogTail, error)
	// Resolve reads where an item is now, by its node id.
	Resolve(ctx context.Context, c Credential, nodeID string) (Target, error)
	// Comment posts a comment on the item.
	Comment(ctx context.Context, c Credential, t Target, body string) (Comment, error)
	// FindComment looks for a comment matching m posted at or after
	// since on the item.
	FindComment(ctx context.Context, c Credential, t Target, since time.Time, m Match) (Comment, Found, error)
	// Close closes an issue with a reason (ReasonCompleted,
	// ReasonNotPlanned).
	Close(ctx context.Context, c Credential, t Target, reason string) error
	// Marker is the text that ends a publication's posted bytes and that
	// a Match looks for: it holds id and is at most MaxMarker bytes; ''
	// for an id the adapter cannot mark.
	Marker(id string) string
}

// Adapters is the registry the API makes, keyed by each adapter's
// System(). Nothing registers itself.
type Adapters map[string]Adapter

// Systems lists the registry's systems, sorted, refusing a name past the
// shape (CheckSystem) or a key that is not its adapter's System().
func (a Adapters) Systems() ([]string, error) {
	out := make([]string, 0, len(a))
	for name, ad := range a {
		if err := CheckSystem(name); err != nil {
			return nil, err
		}
		if ad == nil || ad.System() != name {
			return nil, fmt.Errorf("tracker: the adapter registered as %q is not that system's", name)
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

// CheckSystem refuses a system name that is not 1 to 16 bytes of
// [a-z0-9-] starting with a letter, or that is native or dispatch
// (Dispatch's own names for where a ticket's fields come from).
func CheckSystem(name string) error {
	if name == "" || len(name) > 16 || name[0] < 'a' || name[0] > 'z' {
		return fmt.Errorf("tracker: a system name is 1 to 16 bytes starting with a letter: %q", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return fmt.Errorf("tracker: a system name is [a-z0-9-]: %q", name)
		}
	}
	if name == "native" || name == "dispatch" {
		return fmt.Errorf("tracker: %q is Dispatch's own name, not a system's", name)
	}
	return nil
}

// cutBytes cuts s to at most n bytes at a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !runeStart(s[i]) {
		i--
	}
	return s[:i]
}

func runeStart(b byte) bool { return b&0xc0 != 0x80 }

// CutBytes cuts s to at most n bytes at a rune boundary, reporting a cut.
func CutBytes(s string, n int) (string, bool) {
	out := cutBytes(s, n)
	return out, len(out) < len(s)
}
