package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// What other machines hold (docs/plans/sync-seams.md, OS-4): records the
// sync store writes and removes (PutRemote, DeleteRemote, ClearRemote),
// kept in the data directory under remote/<device>/<kind>-<id>.json (the
// device and the id hex-encoded, as the store's names are), at most
// ext.MaxRemoteFiles, left out of an export and never on the sync
// allowlist. The server reads them (GET /api/remote) and never acts on
// them: no route takes a remote record's id as the target of an action,
// and the app's screens draw each as another machine's, read only.

// remoteRecord is a remote record as the data directory keeps it and GET
// /api/remote answers it: its body JSON as handed in.
type remoteRecord struct {
	Kind       string          `json:"kind"`
	Device     string          `json:"device"`
	DeviceName string          `json:"deviceName"`
	ID         string          `json:"id"`
	Rev        int64           `json:"rev"`
	At         time.Time       `json:"at"`
	Data       json.RawMessage `json:"data"`
}

// The bounds of GET /api/remote's reply.
const (
	remotePage      = 200
	maxRemoteReply  = 16 << 20
	remoteReplyRoom = 64 << 10 // kept for the reply's own fields
)

// remoteStore is the data directory's remote/ store.
func (s *Server) remoteStore() (*store.Store, error) {
	if s.store == nil {
		return nil, errors.New("no data directory is configured")
	}
	return s.store.Sub("remote")
}

// remoteDir and remoteFile are a record's directory (its device) and file
// name (its kind and id), hex-encoded into the store's names.
func remoteDir(device string) string { return hex.EncodeToString([]byte(device)) }
func remoteFile(kind, id string) string {
	return kind + "-" + hex.EncodeToString([]byte(id)) + ".json"
}

// remoteFinal says a record is of something over: a decision final, a run
// closed. Past the bound of files these go first, oldest first.
func remoteFinal(r remoteRecord) bool {
	switch r.Kind {
	case ext.RemoteDecision:
		var d struct {
			State string `json:"state"`
		}
		return json.Unmarshal(r.Data, &d) == nil && (ledger.DecisionRow{State: d.State}).Final()
	case ext.RemotePointer:
		var p struct {
			ClosedAt time.Time `json:"closedAt"`
		}
		return json.Unmarshal(r.Data, &p) == nil && !p.ClosedAt.IsZero()
	}
	return false
}

// remoteRoot is remote/'s path, for a read: nothing is made or probed, so
// a read of a server with no sync store writes nothing in its data
// directory.
func (s *Server) remoteRoot() (string, error) {
	if s.store == nil {
		return "", errors.New("no data directory is configured")
	}
	return filepath.Join(s.store.Dir(), "remote"), nil
}

// remoteNames lists the records remote/ holds by their paths
// (<device>/<kind>-<id>.json), in path order, from the folders' names
// alone: no record is opened. No remote/ holds none.
func (s *Server) remoteNames() ([]string, error) {
	root, err := s.remoteRoot()
	if err != nil {
		return nil, err
	}
	devs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, dev := range devs {
		if !dev.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, dev.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
				paths = append(paths, dev.Name()+"/"+f.Name())
			}
		}
	}
	return paths, nil
}

// remoteRead, when a test sets it, is told the path of every record whose
// body remote/ is read for; nil outside tests.
var remoteRead atomic.Pointer[func(path string)]

// readRemote reads the record at path (remoteNames'); false for one that
// does not read or parse, which is passed over.
func (s *Server) readRemote(path string) (remoteRecord, bool) {
	root, err := s.remoteRoot()
	if err != nil {
		return remoteRecord{}, false
	}
	if f := remoteRead.Load(); f != nil {
		(*f)(path)
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return remoteRecord{}, false
	}
	var r remoteRecord
	if json.Unmarshal(b, &r) != nil {
		return remoteRecord{}, false
	}
	return r, true
}

// remoteKindOf is the kind a record's path names (its file's name begins
// with it), "" for none.
func remoteKindOf(path string) string {
	base := path[strings.LastIndexByte(path, '/')+1:]
	kind, _, ok := strings.Cut(base, "-")
	if !ok || !remoteKinds[kind] {
		return ""
	}
	return kind
}

// putRemote keeps r, unless the record held is newer. A new record past
// the bound of files drops the oldest final ones first; with none to
// drop it is refused.
func (s *Server) putRemote(r ext.Remote) error {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	st, err := s.remoteStore()
	if err != nil {
		return err
	}
	dev, err := st.Sub(remoteDir(r.Device))
	if err != nil {
		return err
	}
	name := remoteFile(r.Kind, r.ID)
	var held remoteRecord
	ok, err := dev.Load(name, &held)
	if err != nil {
		return err
	}
	if ok && held.Rev > r.Rev {
		return nil
	}
	if !ok {
		// Counted by their names; the records are read only past the
		// bound, to judge which are over and how old.
		paths, err := s.remoteNames()
		if err != nil {
			return err
		}
		limit := ext.MaxRemoteFiles
		if s.remoteMax > 0 {
			limit = s.remoteMax
		}
		if over := len(paths) + 1 - limit; over > 0 {
			type final struct {
				path string
				at   time.Time
			}
			var finals []final
			for _, p := range paths {
				if rec, ok := s.readRemote(p); ok && remoteFinal(rec) {
					finals = append(finals, final{p, rec.At})
				}
			}
			slices.SortFunc(finals, func(a, b final) int { return a.at.Compare(b.at) })
			if len(finals) < over {
				return fmt.Errorf("%w: other machines' records fill their %d files", ext.ErrRefused, limit)
			}
			for _, f := range finals[:over] {
				if err := os.Remove(filepath.Join(st.Dir(), filepath.FromSlash(f.path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
	}
	return dev.Save(name, remoteRecord{Kind: r.Kind, Device: r.Device, DeviceName: r.DeviceName, ID: r.ID, Rev: r.Rev, At: r.At.UTC(), Data: json.RawMessage(r.Data)})
}

// PutRemote keeps a record another machine holds (ext.Host).
func (h *extHost) PutRemote(ctx context.Context, r ext.Remote) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	if err := r.Valid(); err != nil {
		return err
	}
	if !remoteShown(r.Kind, r.Data) {
		return fmt.Errorf("%w: a remote %s record is not of the form this server shows", ext.ErrRefused, r.Kind)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.s.putRemote(r)
}

// remoteShown says a record's body is of the form its kind is shown in:
// another machine's pins a pins document as this server keeps its own
// (its version, at most its count of pins, each session once, unknown
// fields refused); a host summary one ext.ParseHostSummary takes; every
// other kind as its check took it.
func remoteShown(kind string, data []byte) bool {
	switch kind {
	case ext.RemotePins:
		_, err := uiContentOf(uiPins, data, true)
		return err == nil
	case ext.RemoteHosts:
		_, err := ext.ParseHostSummary(data)
		return err == nil
	}
	return true
}

// DeleteRemote removes a record another machine held (ext.Host); one not
// held is no error.
func (h *extHost) DeleteRemote(ctx context.Context, kind, device, id string) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	if !remoteKinds[kind] || !remoteIDShape.MatchString(device) || !remoteRecordID(kind, id) {
		return fmt.Errorf("%w: a remote record is named by its kind, device and id", ext.ErrRefused)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.s.remoteMu.Lock()
	defer h.s.remoteMu.Unlock()
	st, err := h.s.remoteStore()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(st.Dir(), remoteDir(device), remoteFile(kind, id)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ClearRemote removes every record other machines held (ext.Host).
func (h *extHost) ClearRemote(ctx context.Context) error {
	if err := h.syncHost(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.s.remoteMu.Lock()
	defer h.s.remoteMu.Unlock()
	st, err := h.s.remoteStore()
	if err != nil {
		return err
	}
	return os.RemoveAll(st.Dir())
}

// remoteKinds are the kinds a remote record has.
var remoteKinds = map[string]bool{ext.RemoteDecision: true, ext.RemotePointer: true, ext.RemotePins: true, ext.RemoteDevice: true, ext.RemoteHosts: true}

// remoteIDShape is a remote record's device's and id's shape, and
// remoteCursor a page's cursor (the last record's path: the device's hex,
// at most remoteIDShape's 64 bytes, and the id's, at most a run's id).
var (
	remoteIDShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	remoteCursor  = regexp.MustCompile(fmt.Sprintf(`^[0-9a-f]{2,128}/[a-z]+-[0-9a-f]{2,%d}\.json$`, 2*ext.MaxRunID))
)

// remoteRecordID says id can name a record of kind, as ext.Remote's Valid
// judges it: remoteIDShape, or for a pointer, named by its run, a run's id
// (ext.ValidRunID, at most ext.MaxRunID bytes).
func remoteRecordID(kind, id string) bool {
	return remoteIDShape.MatchString(id) || kind == ext.RemotePointer && ext.ValidRunID(id)
}

// remoteTicketMatch is what a ticket filter matches: a decision's document
// naming the ticket, a pointer naming it or naming a run the ticket links.
type remoteTicketMatch struct {
	id   string
	runs map[string]bool
}

func (m remoteTicketMatch) matches(r remoteRecord) bool {
	var body struct {
		ID     string `json:"id"`
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(r.Data, &body) != nil {
		return false
	}
	switch r.Kind {
	case ext.RemoteDecision:
		return body.Ticket == m.id
	case ext.RemotePointer:
		return body.Ticket == m.id || m.runs[body.ID]
	}
	return false
}

// handleRemote is GET /api/remote?kind=&ticket=&after=: the records other
// machines hold, in pages of at most remotePage within maxRemoteReply
// bytes, {records, next?}; kind one of the five, ticket a ticket's key
// (its decisions and its runs' pointers), after a page's next. With no
// sync store registered, none.
func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key := range q {
		if key != "kind" && key != "ticket" && key != "after" {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("this route takes kind, ticket and after, not %q", key))
			return
		}
	}
	kind, ticket, after := q.Get("kind"), q.Get("ticket"), q.Get("after")
	switch {
	case len(q["kind"]) > 1 || len(q["ticket"]) > 1 || len(q["after"]) > 1:
		writeError(w, http.StatusBadRequest, "invalid_request", "each of kind, ticket and after is given once")
		return
	case kind != "" && !remoteKinds[kind]:
		writeError(w, http.StatusBadRequest, "invalid_request", "kind is decision, pointer, pins, device or hosts")
		return
	case ticket != "" && !validTicketKey(ticket):
		writeError(w, http.StatusBadRequest, "invalid_request", "ticket is a ticket's key")
		return
	case after != "" && !remoteCursor.MatchString(after):
		writeError(w, http.StatusBadRequest, "invalid_request", "after is a page's next")
		return
	}
	if s.syncName() == "" {
		// No sync store: this machine holds nothing of other machines', as
		// GET /api/sync answers {}, whatever a store that was registered
		// once left in remote/.
		writeJSON(w, http.StatusOK, map[string]any{"records": []remoteRecord{}})
		return
	}
	var match *remoteTicketMatch
	if ticket != "" {
		m, ok, err := s.remoteTicket(r.Context(), ticket)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_failed", "the ticket does not read")
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"records": []remoteRecord{}})
			return
		}
		match = &m
	}
	paths, err := s.remoteNames()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_failed", "other machines' records do not read")
		return
	}
	// Filtered by name (the cursor, the kind) before a record is opened;
	// a page full is ended at the next record it would hold.
	out := []remoteRecord{}
	size, next, last := 0, "", ""
	for _, path := range paths {
		if after != "" && path <= after || kind != "" && remoteKindOf(path) != kind {
			continue
		}
		rec, ok := s.readRemote(path)
		if !ok || kind != "" && rec.Kind != kind || !remoteShown(rec.Kind, rec.Data) || match != nil && !match.matches(rec) {
			continue
		}
		b, _ := json.Marshal(rec)
		if len(out) == remotePage || size+len(b) > maxRemoteReply-remoteReplyRoom {
			next = last
			break
		}
		size += len(b) + 1
		out = append(out, rec)
		last = path
	}
	reply := map[string]any{"records": out}
	if next != "" {
		reply["next"] = next
	}
	writeJSON(w, http.StatusOK, reply)
}

// remoteTicket is the filter of the ticket with key: its id and the runs
// it links; false for a ticket the store does not hold.
func (s *Server) remoteTicket(ctx context.Context, key string) (remoteTicketMatch, bool, error) {
	if s.dispatch == nil {
		return remoteTicketMatch{}, false, nil
	}
	tk, err := s.dispatch.Ticket(ctx, key)
	if errors.Is(err, dispatch.ErrNotFound) {
		return remoteTicketMatch{}, false, nil
	}
	if err != nil {
		return remoteTicketMatch{}, false, err
	}
	links, err := s.dispatch.Links(ctx, key)
	if err != nil {
		return remoteTicketMatch{}, false, err
	}
	m := remoteTicketMatch{id: tk.ID, runs: map[string]bool{}}
	for _, l := range links {
		if l.Kind == dispatch.LinkRun && l.DeletedAt.IsZero() {
			m.runs[l.Ref] = true
		}
	}
	return m, true, nil
}

// validTicketKey says key is a ticket's key (CND-12).
func validTicketKey(key string) bool {
	_, _, ok := dispatch.ParseKey(key)
	return ok
}
