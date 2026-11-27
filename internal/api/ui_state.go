package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The app's screens' state in the data directory (docs/plans/sync-seams.md,
// OS-2): three documents under ui/, each a JSON object with v (1), at (when
// its content last changed, by the server's clock) and by (this data
// directory's writer id, ui/writer), at most maxUIDoc bytes:
//
//   - layout: the editor's layout and splits, the sidebar's mode, width
//     and folds, the crew graph's switches, the run and project pages'
//     views and the run page's last layout of its tiles: enums, booleans,
//     fractions and bounded numbers only;
//   - palette: the palette's recents, by row key, at most uiMaxRecents;
//   - pins: the pinned sessions, at most uiMaxPins.
//
// GET /api/ui/{name} answers the document ({} when there is none); PUT
// /api/ui/{name} takes it whole without at and by, unknown fields refused,
// and sets at and by only when the content changed, at to the later of
// the clock and one millisecond past the held document's at, so an edit
// made after a later-dated document came from a machine whose clock runs
// ahead is still the newest. The sync store writes layout and palette as
// another machine left them (SetUIState), never pins: another machine's
// pins are its own.

// The UI documents' names.
const (
	uiLayout  = "layout"
	uiPalette = "palette"
	uiPins    = "pins"
)

// The bounds of the UI documents.
const (
	maxUIDoc        = 64 << 10
	uiMaxRecents    = 200
	uiMaxRecentKey  = 300
	uiMaxPins       = 20
	uiMaxPinName    = 200
	uiMaxPinSession = 64
	uiMaxPinAgent   = 64
	uiMaxSidebarRem = 100
	uiWriterFile    = "writer"
)

// uiWriterPattern is a writer id: 32 hex characters.
var uiWriterPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// uiPinSessionPattern and uiPinAgentPattern are what a pin's session id
// and agent are made of.
var (
	uiPinSessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	uiPinAgentPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
)

// uiStamp is a document's version: when its content last changed and the
// data directory that changed it.
type uiStamp struct {
	At time.Time `json:"at"`
	By string    `json:"by"`
}

// newer reports whether a is newer than b: a later at, or the same at and
// a greater by.
func (a uiStamp) newer(b uiStamp) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.By > b.By
}

// uiLayoutDoc is the layout document's content.
type uiLayoutDoc struct {
	V           int          `json:"v"`
	Editor      *uiEditor    `json:"editor,omitempty"`
	Sidebar     *uiSidebar   `json:"sidebar,omitempty"`
	CrewGraph   *uiCrewGraph `json:"crewGraph,omitempty"`
	RunTiles    *uiRunTiles  `json:"runTiles,omitempty"`
	RunView     string       `json:"runView,omitempty"`
	ProjectView string       `json:"projectView,omitempty"`
}

type uiEditor struct {
	Layout    string   `json:"layout,omitempty"`    // stacked | side
	Split     *float64 `json:"split,omitempty"`     // the editor's share of the height stacked
	SideSplit *float64 `json:"sideSplit,omitempty"` // and of the width side by side
}

type uiSidebar struct {
	Mode  string          `json:"mode,omitempty"`  // full | rail
	Width *float64        `json:"width,omitempty"` // rem
	Folds map[string]bool `json:"folds,omitempty"` // by section
}

type uiCrewGraph struct {
	Direction string `json:"direction,omitempty"` // LR | TB
	Handoffs  *bool  `json:"handoffs,omitempty"`
	Members   *bool  `json:"members,omitempty"`
}

// uiRunTiles is the run page's last choice for its members' tiles, which
// a crew opened for the first time takes: their layout and whether the
// Activity column is beside them.
type uiRunTiles struct {
	Layout   string `json:"layout,omitempty"` // grid | columns | rows | large
	Activity *bool  `json:"activity,omitempty"`
}

// uiPaletteDoc is the palette document's content: each row key's uses.
type uiPaletteDoc struct {
	V       int                    `json:"v"`
	Recents map[string]uiRecentUse `json:"recents"`
}

type uiRecentUse struct {
	Count int64 `json:"count"`
	Last  int64 `json:"last"` // milliseconds since the epoch
}

// uiPinsDoc is the pins document's content.
type uiPinsDoc struct {
	V    int     `json:"v"`
	Pins []uiPin `json:"pins"`
}

// uiPin is a pinned session: its id, and its name and agent and the time
// (milliseconds since the epoch) when it was pinned.
type uiPin struct {
	Session string `json:"session"`
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	At      int64  `json:"at"`
	// NameVerdict is the copied-output check's verdict of the name, made
	// when the pin is kept, which the server sets and a put may not carry
	// (docs/plans/sync-seams.md, OS-1c): only a clean name leaves the
	// machine.
	NameVerdict string `json:"nameVerdict,omitempty"`
}

// The sections a sidebar folds, the views of the two pages and the
// editor's layouts.
var (
	uiSections     = map[string]bool{"needs": true, "finished": true, "working": true, "shared": true, "exited": true, "pinned": true}
	uiRunViews     = map[string]bool{"grid": true, "graph": true, "timeline": true, "tasks": true}
	uiProjectViews = map[string]bool{"list": true, "board": true, "roadmap": true}
	uiTileLayouts  = map[string]bool{"grid": true, "columns": true, "rows": true, "large": true}
)

// fraction reports whether v is absent or a share in [0, 1].
func fraction(v *float64) bool { return v == nil || (*v >= 0 && *v <= 1) }

// check refuses a layout past its schema.
func (d uiLayoutDoc) check() error {
	if d.V != 1 {
		return errors.New("v is 1")
	}
	if e := d.Editor; e != nil {
		if e.Layout != "" && e.Layout != "stacked" && e.Layout != "side" {
			return errors.New("editor.layout is stacked or side")
		}
		if !fraction(e.Split) || !fraction(e.SideSplit) {
			return errors.New("editor's splits are fractions from 0 to 1")
		}
	}
	if sb := d.Sidebar; sb != nil {
		if sb.Mode != "" && sb.Mode != "full" && sb.Mode != "rail" {
			return errors.New("sidebar.mode is full or rail")
		}
		if sb.Width != nil && (*sb.Width <= 0 || *sb.Width > uiMaxSidebarRem) {
			return fmt.Errorf("sidebar.width is above 0 and at most %d rem", uiMaxSidebarRem)
		}
		for k := range sb.Folds {
			if !uiSections[k] {
				return fmt.Errorf("sidebar.folds names no section %q", k)
			}
		}
	}
	if g := d.CrewGraph; g != nil && g.Direction != "" && g.Direction != "LR" && g.Direction != "TB" {
		return errors.New("crewGraph.direction is LR or TB")
	}
	if t := d.RunTiles; t != nil && t.Layout != "" && !uiTileLayouts[t.Layout] {
		return errors.New("runTiles.layout is grid, columns, rows or large")
	}
	if d.RunView != "" && !uiRunViews[d.RunView] {
		return errors.New("runView is grid, graph, timeline or tasks")
	}
	if d.ProjectView != "" && !uiProjectViews[d.ProjectView] {
		return errors.New("projectView is list, board or roadmap")
	}
	return nil
}

// check refuses recents past their bounds.
func (d uiPaletteDoc) check() error {
	if d.V != 1 {
		return errors.New("v is 1")
	}
	if len(d.Recents) > uiMaxRecents {
		return fmt.Errorf("%d recents, more than %d", len(d.Recents), uiMaxRecents)
	}
	for k, u := range d.Recents {
		if k == "" || len(k) > uiMaxRecentKey || !utf8.ValidString(k) || hasControlChar(k) {
			return fmt.Errorf("a recent's key is 1 to %d bytes of one line", uiMaxRecentKey)
		}
		if u.Count < 1 || u.Count > 1e9 || u.Last < 0 {
			return fmt.Errorf("the recent %q's count is 1 or more and its last a time", k)
		}
	}
	return nil
}

// check refuses pins past their bounds.
func (d uiPinsDoc) check() error {
	if d.V != 1 {
		return errors.New("v is 1")
	}
	if len(d.Pins) > uiMaxPins {
		return fmt.Errorf("%d pins, more than %d", len(d.Pins), uiMaxPins)
	}
	seen := map[string]bool{}
	for _, p := range d.Pins {
		switch {
		case !uiPinSessionPattern.MatchString(p.Session):
			return fmt.Errorf("a pin's session is 1 to %d letters, digits, '_' or '-'", uiMaxPinSession)
		case seen[p.Session]:
			return fmt.Errorf("the session %s is pinned twice", p.Session)
		case len(p.Name) > uiMaxPinName || !utf8.ValidString(p.Name) || hasControlChar(p.Name):
			return fmt.Errorf("a pin's name is at most %d bytes of one line", uiMaxPinName)
		case !uiPinAgentPattern.MatchString(p.Agent):
			return fmt.Errorf("a pin's agent is at most %d letters, digits, '.', '_' or '-'", uiMaxPinAgent)
		case p.At < 0:
			return errors.New("a pin's at is a time")
		}
		seen[p.Session] = true
	}
	return nil
}

// hasControlChar reports whether s holds a control character.
func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// uiContent decodes b, the content of the document name (without at and
// by), against its schema, unknown fields refused, and answers it encoded
// as the store writes it.
func uiContent(name string, b []byte) ([]byte, error) { return uiContentOf(name, b, false) }

// uiContentOf is uiContent; kept says b is a document as the server kept
// it, which may carry what the server sets (a pin's nameVerdict).
func uiContentOf(name string, b []byte, kept bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var v interface{ check() error }
	switch name {
	case uiLayout:
		var d uiLayoutDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		v = d
	case uiPalette:
		var d uiPaletteDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		if d.Recents == nil {
			d.Recents = map[string]uiRecentUse{}
		}
		v = d
	case uiPins:
		var d uiPinsDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		if d.Pins == nil {
			d.Pins = []uiPin{}
		}
		for _, p := range d.Pins {
			if !kept && p.NameVerdict != "" {
				return nil, errors.New("a pin's nameVerdict is the server's to set")
			}
		}
		v = d
	default:
		return nil, fmt.Errorf("no UI document is called %q", name)
	}
	if dec.More() {
		return nil, errors.New("one JSON object")
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// uiDoc is a document as kept: its content's members with at and by.
type uiDoc struct {
	content []byte // the content, encoded, without at and by
	stamp   uiStamp
}

// encode is the document as the store writes it and the routes answer it.
func (d uiDoc) encode() ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(d.content, &m); err != nil {
		return nil, err
	}
	at, _ := json.Marshal(d.stamp.At)
	by, _ := json.Marshal(d.stamp.By)
	m["at"], m["by"] = at, by
	return store.Encode(m)
}

// splitUIDoc is b, a document as kept or as another machine left it,
// taken apart: its content checked against name's schema, and its at and
// by, each required.
func splitUIDoc(name string, b []byte) (uiDoc, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return uiDoc{}, err
	}
	var st uiStamp
	if err := json.Unmarshal(m["at"], &st.At); err != nil || st.At.IsZero() {
		return uiDoc{}, errors.New("a document's at is a time")
	}
	if err := json.Unmarshal(m["by"], &st.By); err != nil || !uiWriterPattern.MatchString(st.By) {
		return uiDoc{}, errors.New("a document's by is a writer's id")
	}
	delete(m, "at")
	delete(m, "by")
	raw, err := json.Marshal(m)
	if err != nil {
		return uiDoc{}, err
	}
	content, err := uiContentOf(name, raw, true)
	if err != nil {
		return uiDoc{}, err
	}
	return uiDoc{content: content, stamp: st}, nil
}

// uiNames are the documents' names.
var uiNames = map[string]bool{uiLayout: true, uiPalette: true, uiPins: true}

// uiStore is the data directory's ui/ store; nil, with why, without one.
func (s *Server) uiStore() (*store.Store, error) {
	if s.store == nil {
		return nil, errors.New("no data directory is configured")
	}
	return s.store.Sub("ui")
}

// maxUIStored bounds a UI document as its file holds it: the bound both
// writers keep (putUI, setUIFromElsewhere) and the read reads under.
const maxUIStored = maxUIDoc + 1024

// errUIStoredBound is a document whose stored form is past maxUIStored.
var errUIStoredBound = fmt.Errorf("a UI document is stored in at most %d KiB", maxUIStored>>10)

// uiStoredSize is the bytes the document b takes in its file, as Save
// writes it.
func uiStoredSize(b []byte) (int, error) {
	enc, err := store.Encode(json.RawMessage(b))
	return len(enc), err
}

// uiHeld reads the document name as kept: ok false for none. A file past
// its bound (a build before the bound was kept at the writers) is set
// aside, as name.json.aside, and read as none, so the next write keeps
// the person's.
func uiHeld(st *store.Store, name string) (uiDoc, bool, error) {
	path := filepath.Join(st.Dir(), name+".json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return uiDoc{}, false, nil
	}
	if err != nil {
		return uiDoc{}, false, err
	}
	if len(b) > maxUIStored {
		if err := os.Rename(path, path+".aside"); err != nil {
			return uiDoc{}, false, err
		}
		return uiDoc{}, false, nil
	}
	d, err := splitUIDoc(name, b)
	if err != nil {
		return uiDoc{}, false, fmt.Errorf("ui/%s.json: %w", name, err)
	}
	return d, true, nil
}

// uiWriter is this data directory's writer id, made once in ui/writer
// (left out of an export: each machine restored from one export makes its
// own).
func uiWriter(st *store.Store) (string, error) {
	b, err := os.ReadFile(filepath.Join(st.Dir(), uiWriterFile))
	if err == nil && uiWriterPattern.Match(bytes.TrimSpace(b)) {
		return string(bytes.TrimSpace(b)), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	w := hex.EncodeToString(id[:])
	if err := st.WriteFile(uiWriterFile, []byte(w+"\n")); err != nil {
		return "", err
	}
	return w, nil
}

// putUI keeps content as the document name: unchanged content keeps the
// held document as it is (no write), and a change is stamped now, past
// the held document's at, by this directory's writer. It answers the
// document as kept.
func (s *Server) putUI(name string, content []byte) ([]byte, error) {
	st, err := s.uiStore()
	if err != nil {
		return nil, err
	}
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	held, ok, err := uiHeld(st, name)
	if err != nil {
		// A document that does not read is written over by the person's.
		ok = false
	}
	if name == uiPins {
		var keptDoc *uiDoc
		if ok {
			keptDoc = &held
		}
		if content, err = s.judgePins(content, keptDoc); err != nil {
			return nil, err
		}
	}
	if ok && bytes.Equal(held.content, content) {
		return held.encode()
	}
	by, err := uiWriter(st)
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	if ok && !at.After(held.stamp.At) {
		at = held.stamp.At.Add(time.Millisecond)
	}
	d := uiDoc{content: content, stamp: uiStamp{At: at, By: by}}
	b, err := d.encode()
	if err != nil {
		return nil, err
	}
	if n, err := uiStoredSize(b); err != nil {
		return nil, err
	} else if n > maxUIStored {
		return nil, errUIStoredBound
	}
	if err := st.Save(name+".json", json.RawMessage(b)); err != nil {
		return nil, err
	}
	return b, nil
}

// setUIFromElsewhere keeps the document name as another machine left it
// (b, with its at and by): refused, ext.ErrRefused, for one not newer than
// the one held; equal content with an older stamp writes nothing.
func (s *Server) setUIFromElsewhere(name string, b []byte) error {
	if name != uiLayout && name != uiPalette {
		return fmt.Errorf("%w: the UI document %q is not one another machine writes here", ext.ErrRefused, name)
	}
	if len(b) > maxUIDoc {
		return fmt.Errorf("%w: the UI document is past %d bytes", ext.ErrRefused, maxUIDoc)
	}
	d, err := splitUIDoc(name, b)
	if err != nil {
		return fmt.Errorf("%w: %v", ext.ErrRefused, err)
	}
	st, err := s.uiStore()
	if err != nil {
		return fmt.Errorf("%w: %v", ext.ErrUnsupported, err)
	}
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	if held, ok, err := uiHeld(st, name); err == nil && ok && !d.stamp.newer(held.stamp) {
		return fmt.Errorf("%w: an older document: ui/%s.json holds one of %s by %s", ext.ErrRefused, name, held.stamp.At.Format(time.RFC3339Nano), held.stamp.By)
	}
	out, err := d.encode()
	if err != nil {
		return err
	}
	if n, err := uiStoredSize(out); err != nil {
		return err
	} else if n > maxUIStored {
		return fmt.Errorf("%w: %v", ext.ErrRefused, errUIStoredBound)
	}
	return st.Save(name+".json", json.RawMessage(out))
}

// handleUIGet answers the UI document {name}: {} when there is none.
func (s *Server) handleUIGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !uiNames[name] {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("no UI document is called %q: layout, palette or pins", name))
		return
	}
	st, err := s.uiStore()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", err.Error())
		return
	}
	held, ok, err := uiHeld(st, name)
	switch {
	case err != nil:
		s.log.Warn("ui: a document does not read", "name", name, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "the document does not read")
		return
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	b, err := held.encode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_failed", "the document does not read")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

// handleUIPut keeps the UI document {name}, whole without at and by, at
// most maxUIDoc bytes, and at most maxUIStored as its file holds it: 400
// invalid_request past its schema, 413 bound past either bound; the
// document as kept.
func (s *Server) handleUIPut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !uiNames[name] {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("no UI document is called %q: layout, palette or pins", name))
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	var raw json.RawMessage
	if err := decodeJSONLimit(w, r, &raw, maxUIDoc); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("a UI document is at most %d KiB", maxUIDoc>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	content, err := uiContent(name, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	b, err := s.putUI(name, content)
	if errors.Is(err, errUIStoredBound) {
		writeError(w, http.StatusRequestEntityTooLarge, "bound", err.Error())
		return
	}
	if err != nil {
		s.log.Warn("ui: a document could not be kept", "name", name, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "could not keep the document")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

// judgePins gives each pin of content its name's verdict: the held pin's
// when the same session has the same name there, else the check's now (a
// person's words: the person pinned it). Without the check, pending.
func (s *Server) judgePins(content []byte, held *uiDoc) ([]byte, error) {
	var d uiPinsDoc
	if err := json.Unmarshal(content, &d); err != nil {
		return nil, err
	}
	before := map[string]uiPin{}
	if held != nil {
		var h uiPinsDoc
		if json.Unmarshal(held.content, &h) == nil {
			for _, p := range h.Pins {
				before[p.Session] = p
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*indexCatchUp)
	defer cancel()
	for i, p := range d.Pins {
		if b, ok := before[p.Session]; ok && b.Name == p.Name && b.NameVerdict != "" && b.NameVerdict != verdictPending {
			d.Pins[i].NameVerdict = b.NameVerdict
			continue
		}
		d.Pins[i].NameVerdict = s.copyCheck.Judge(ctx, p.Name, false)
	}
	return json.Marshal(d)
}

// The kinds of palette recents that leave this machine (a page, a ticket,
// a crew, a run, a built-in action): every other kind names a path, a
// link, a session or a person here, and stays (TestPaletteDeliveredWithoutPaths).
var uiDeliveredRecents = []string{"go:", "ticket:", "crew:", "run:", "action:"}

// deliveredUIDoc is the UI document at path (ui/<name>.json) as a sync
// store is given it: the layout whole, the palette with the recents of
// uiDeliveredRecents's kinds alone, the pins with each pin's session, agent
// and time and no name (a name waits for the copied-output check, OS-1c);
// nil for bytes that do not read.
func deliveredUIDoc(path string, b []byte) []byte {
	name := strings.TrimSuffix(strings.TrimPrefix(path, "ui/"), ".json")
	d, err := splitUIDoc(name, b)
	if err != nil {
		return nil
	}
	switch name {
	case uiPalette:
		var p uiPaletteDoc
		if json.Unmarshal(d.content, &p) != nil {
			return nil
		}
		for k := range p.Recents {
			if !hasAnyPrefix(k, uiDeliveredRecents) {
				delete(p.Recents, k)
			}
		}
		d.content, _ = json.Marshal(p)
	case uiPins:
		var p uiPinsDoc
		if json.Unmarshal(d.content, &p) != nil {
			return nil
		}
		for i := range p.Pins {
			if p.Pins[i].NameVerdict != verdictClean {
				p.Pins[i].Name = ""
			}
			p.Pins[i].NameVerdict = ""
		}
		d.content, _ = json.Marshal(p)
	}
	out, err := d.encode()
	if err != nil {
		return nil
	}
	return out
}

// hasAnyPrefix reports whether s begins with one of prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
