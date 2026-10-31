package ext

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"
)

// ContinuityStore keeps a copy of the data directory's documents elsewhere
// (sync), and restores this device's latest copy on another. Its free
// default keeps nothing: the local data directory is the one copy and
// stays authoritative.
type ContinuityStore interface {
	Provider
	// Saved and Deleted are told, after its local commit, of a document the
	// data directory now holds or no longer holds, with its bytes as they
	// are when it is delivered; each epoch begins with a replay of the whole
	// state. Called in order, from one goroutine of the store's own, with a
	// context that ends after DeliveryWait. A nil answer acknowledges the
	// doc; an error, a panic or the context's end has the document
	// delivered again, whole, as it is then.
	Saved(ctx context.Context, d Doc) error
	Deleted(ctx context.Context, d Doc) error
	// Restore writes this device's latest copy as an archive of `conductor
	// export`'s format, holding any part of data/ that the archive's checks
	// accept; ErrNothingToRestore when it keeps none. Called only by
	// `conductor restore --provider`, with no server running and no Start:
	// r gives the provider its own directory and a logger, all it has then.
	// The one method not bound to answer from what the provider holds: it
	// may fetch the archive from its service on this call, streaming it
	// into w, and must end when ctx does (the person's Ctrl-C), returning
	// ctx's error; whatever it wrote is then thrown away.
	Restore(ctx context.Context, r RestoreRequest, w io.Writer) error
}

// RestoreRequest is what a provider's Restore is given on a machine where
// no server has started it.
type RestoreRequest struct {
	Dir string       // <dataDir>/ext/<name>, made 0700, empty on a fresh machine: the provider's sign-in goes there
	Log *slog.Logger // naming the provider
}

// Doc is one document's state after a local commit, or a replay's marker.
type Doc struct {
	Store  string    `json:"store"`         // data | ledger | dispatch, or the markers replay | replayed
	Path   string    `json:"path"`          // a document's path in the data directory, a run's id, a Dispatch entity's kind/id or "log"
	Epoch  string    `json:"epoch"`         // 32 hex characters, new at every replay: Rev orders docs of one epoch only
	Rev    int64     `json:"rev,omitempty"` // data: the data directory's commit sequence in this epoch; ledger: the run's revision; dispatch: the last change's position in its log
	At     time.Time `json:"at"`
	Size   int64     `json:"size,omitempty"`   // the whole document's bytes
	Offset int64     `json:"offset,omitempty"` // where Data begins in it: a document past MaxDocData comes in parts
	Data   []byte    `json:"data,omitempty"`   // at most MaxDocData bytes of the document as it is when delivered
}

// ErrNothingToRestore is Restore's answer when the store keeps no copy.
var ErrNothingToRestore = errors.New("ext: nothing to restore")

const (
	MaxDocData  = 1 << 20
	SyncPending = 65_536 // documents waiting to be delivered; past it, a replay
)
