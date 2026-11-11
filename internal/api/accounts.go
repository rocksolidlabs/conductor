package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// The accounts routes (docs/plans/capacity.md, PR-3): the account
// references of capacity/accounts.json, the admin's. Every write names the
// rev it read; only the server that owns capacity/ writes (409
// accounts_not_owner elsewhere). The routes answer the admin alone, and a
// refusal's words never reach a log.

// capacityFacts is what internal/capacity asks of the server. Nothing
// binds an account before PR-4, so Bound names nothing: a remove is
// refused only while a reservation holds the account.
type capacityFacts struct{}

func (capacityFacts) ConfigDirEnv(adapter string) (string, bool) {
	ad, ok := agents.Get(adapter)
	return ad.ConfigDirEnv, ok
}

func (capacityFacts) CheckAccountDir(dir string) error { return checkAccountDir(dir) }

func (capacityFacts) Bound(string) []string { return nil }

// accountsAdapter is an adapter with a ConfigDirEnv as GET /api/accounts
// gives it: its implicit default login, its default account and that
// login's last reading.
type accountsAdapter struct {
	Adapter string          `json:"adapter"`
	Name    string          `json:"name"`
	Default string          `json:"default"`
	Reading *accountReading `json:"reading,omitempty"`
}

// listedAccount is an account with its last reading.
type listedAccount struct {
	capacity.Account
	Reading *accountReading `json:"reading,omitempty"`
}

// accountsReply is GET /api/accounts: the document (its removed ids
// left out: they only keep ids from being minted again), the adapters
// with their defaults, and whether this server owns capacity/, and why
// not (at most 300 bytes).
type accountsReply struct {
	Owner    bool              `json:"owner"`
	Why      string            `json:"why,omitempty"`
	Rev      int64             `json:"rev"`
	Accounts []listedAccount   `json:"accounts"`
	Adapters []accountsAdapter `json:"adapters"`
}

// ownerWhy holds a reason shown beside the document to 300 bytes, cut on
// a character's boundary.
func ownerWhy(s string) string {
	for len(s) > 300 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.capacityAvailable(w) {
		return
	}
	d := s.capacity
	doc, err := d.st.Accounts()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "capacity_unavailable", err.Error())
		return
	}
	owned, why := d.ownership()
	out := accountsReply{Owner: owned, Why: ownerWhy(why), Rev: doc.Rev, Accounts: []listedAccount{}, Adapters: []accountsAdapter{}}
	d.mu.Lock()
	reading := func(key string) *accountReading {
		if r, ok := d.lastRead[key]; ok {
			return &r
		}
		return nil
	}
	for _, a := range doc.Accounts {
		out.Accounts = append(out.Accounts, listedAccount{Account: a, Reading: reading(a.ID)})
	}
	for _, ad := range agents.All() {
		if ad.ConfigDirEnv != "" {
			out.Adapters = append(out.Adapters, accountsAdapter{Adapter: ad.ID, Name: ad.Name, Default: capacity.DefaultOf(doc, ad.ID), Reading: reading(ad.ID + ":" + capacity.DefaultID)})
		}
	}
	d.mu.Unlock()
	writeCapacityJSON(w, http.StatusOK, out, maxAccountsReply)
}

// writeCapacityError answers a refusal of internal/capacity with its code,
// its field and what holds the account; anything else is the store's,
// logged without its words (they may name a folder).
func (s *Server) writeCapacityError(w http.ResponseWriter, err error) {
	var ce *capacity.Error
	if !errors.As(err, &ce) {
		s.log.Warn("capacity/accounts.json could not be read or written")
		writeError(w, http.StatusInternalServerError, "store_failed", "capacity/accounts.json could not be read or written")
		return
	}
	status := http.StatusConflict
	switch ce.Code {
	case capacity.CodeInvalidAccount:
		status = http.StatusBadRequest
	case capacity.CodeNotKept:
		status = http.StatusServiceUnavailable
	}
	writeAccountError(w, status, ce.Code, ce.Field, ce.Words, ce.Names)
}

// accountError is the error envelope of the accounts' routes: the API's,
// with the field a refusal is about and what holds the account.
type accountError struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Field   string   `json:"field,omitempty"`
	Names   []string `json:"names,omitempty"`
}

func writeAccountError(w http.ResponseWriter, status int, code, field, words string, names []string) {
	writeJSON(w, status, map[string]accountError{"error": {Code: code, Message: words, Field: field, Names: names}})
}

// The bodies of the accounts' writes. A write names the rev it read.
type (
	addAccountBody struct {
		Adapter     string `json:"adapter"`
		Label       string `json:"label"`
		Folder      string `json:"folder"`
		ExpectedRev *int64 `json:"expectedRev"`
	}
	renameAccountBody struct {
		Label       string `json:"label"`
		ExpectedRev *int64 `json:"expectedRev"`
	}
	accountRevBody struct {
		ExpectedRev *int64 `json:"expectedRev"`
	}
	accountDefaultBody struct {
		Account     string `json:"account"`
		ExpectedRev *int64 `json:"expectedRev"`
	}
)

// decodeAccountWrite decodes a write's body into v, whose expectedRev
// rev points to, answering false having answered the request: an id (the
// server mints one) and a folder where the route takes none (an
// account's folder never changes) are refused with their words.
func (s *Server) decodeAccountWrite(w http.ResponseWriter, r *http.Request, v any, rev func() *int64) bool {
	if !s.capacityAvailable(w) {
		return false
	}
	if err := decodeJSONLimit(w, r, v, maxAccountBody); err != nil {
		switch msg := err.Error(); {
		case strings.Contains(msg, `unknown field "id"`):
			writeAccountError(w, http.StatusBadRequest, capacity.CodeInvalidAccount, "id", "the server mints an account's id", nil)
		case strings.Contains(msg, `unknown field "folder"`):
			writeAccountError(w, http.StatusBadRequest, capacity.CodeInvalidAccount, "folder", "an account's folder never changes: add another account for another folder", nil)
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "a JSON object of the route's fields")
		}
		return false
	}
	if rev() == nil {
		writeAccountError(w, http.StatusBadRequest, capacity.CodeInvalidAccount, "expectedRev", "the rev of the accounts as read", nil)
		return false
	}
	return true
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var b addAccountBody
	if !s.decodeAccountWrite(w, r, &b, func() *int64 { return b.ExpectedRev }) {
		return
	}
	a, rev, err := s.capacity.st.Add(capacityFacts{}, b.Adapter, b.Label, b.Folder, *b.ExpectedRev)
	if err != nil {
		s.writeCapacityError(w, err)
		return
	}
	writeCapacityJSON(w, http.StatusCreated, map[string]any{"account": a, "rev": rev}, maxAccountsReply)
}

func (s *Server) handleRenameAccount(w http.ResponseWriter, r *http.Request) {
	var b renameAccountBody
	if !s.decodeAccountWrite(w, r, &b, func() *int64 { return b.ExpectedRev }) {
		return
	}
	a, rev, err := s.capacity.st.Rename(r.PathValue("id"), b.Label, *b.ExpectedRev)
	if err != nil {
		s.writeCapacityError(w, err)
		return
	}
	writeCapacityJSON(w, http.StatusOK, map[string]any{"account": a, "rev": rev}, maxAccountsReply)
}

func (s *Server) handleRetireAccount(w http.ResponseWriter, r *http.Request) {
	var b accountRevBody
	if !s.decodeAccountWrite(w, r, &b, func() *int64 { return b.ExpectedRev }) {
		return
	}
	a, rev, err := s.capacity.st.Retire(r.PathValue("id"), *b.ExpectedRev)
	if err != nil {
		s.writeCapacityError(w, err)
		return
	}
	writeCapacityJSON(w, http.StatusOK, map[string]any{"account": a, "rev": rev}, maxAccountsReply)
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if !s.capacityAvailable(w) {
		return
	}
	rev, err := strconv.ParseInt(r.URL.Query().Get("expectedRev"), 10, 64)
	if err != nil {
		writeAccountError(w, http.StatusBadRequest, capacity.CodeInvalidAccount, "expectedRev", "?expectedRev= the rev of the accounts as read", nil)
		return
	}
	id := r.PathValue("id")
	if _, err := s.capacity.st.Remove(capacityFacts{}, id, rev); err != nil {
		s.writeCapacityError(w, err)
		return
	}
	s.capacity.mu.Lock()
	delete(s.capacity.lastRead, id)
	s.capacity.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAccountDefault(w http.ResponseWriter, r *http.Request) {
	var b accountDefaultBody
	if !s.decodeAccountWrite(w, r, &b, func() *int64 { return b.ExpectedRev }) {
		return
	}
	adapter := r.PathValue("adapter")
	if env, _ := (capacityFacts{}).ConfigDirEnv(adapter); env == "" {
		writeAccountError(w, http.StatusBadRequest, capacity.CodeInvalidAccount, "adapter", "an adapter that can move its login folder: "+adapter+" has its default login only", nil)
		return
	}
	defaults, rev, err := s.capacity.st.SetDefault(adapter, b.Account, *b.ExpectedRev)
	if err != nil {
		s.writeCapacityError(w, err)
		return
	}
	if defaults == nil {
		defaults = map[string]string{}
	}
	writeCapacityJSON(w, http.StatusOK, map[string]any{"defaults": defaults, "rev": rev}, maxAccountsReply)
}
