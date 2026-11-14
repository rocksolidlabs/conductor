package api

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// fileDeny returns what no file read of a server session may reach, even
// inside its working directory (config.Config.FileDeny): the data directory,
// and the one st writes to should the two ever differ, the config file and
// the catalog file, each of the two files with the copies an editor leaves
// beside it; and what the other Conductors on the machine keep: ~/.conductor,
// the desktop app's directories and settings, and the directory the app
// names for the server it starts (CONDUCTOR_DESKTOP_DIR); and each folder
// the accounts document of each of those registers (Capacity). The list is looked up again at
// each request and keeps what it named before (config.Config.FileDenyFunc),
// so a data directory the desktop app's settings move to while the server
// runs, or an account added, is refused from then on. While the accounts
// document is there and cannot be read the list refuses every file, and the
// log says so once as that begins and once as it ends, in fixed words.
func fileDeny(cfg *config.Config, st *store.Store, log *slog.Logger) func() []string {
	var dirs []string
	if st != nil {
		dirs = []string{st.Dir()}
	}
	deny := cfg.FileDenyStateFunc(dirs...)
	var mu sync.Mutex
	refusing := false
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		list, err := deny()
		switch {
		case err != nil && !refusing:
			log.Warn(denyAllWords)
		case err == nil && refusing:
			log.Info(denyEndWords)
		}
		refusing = err != nil
		return list
	}
}

// The log's words as the deny list starts and stops refusing every file.
const (
	denyAllWords = "capacity/accounts.json cannot be read: every file of every session is refused until it can be"
	denyEndWords = "capacity/accounts.json can be read again: the files of sessions are served as before"
)

// handleGetFile reads a file from a server-hosted session's working directory
// under the same rules as the in-band file_get message.
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.authenticate(w, r, true)
	if !ok {
		return
	}
	role := p.role(id)
	if role == "" {
		if !s.limiter.allow(clientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "no access to this session")
		return
	}
	switch s.cfg.FileView {
	case config.FileViewOff:
		writeError(w, http.StatusForbidden, "file_denied", "file viewing is disabled")
		return
	case config.FileViewControl:
		if role != session.RoleControl {
			writeError(w, http.StatusForbidden, "file_denied", "file viewing requires the control role")
			return
		}
	}
	d, ok := s.registry.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	local, ok := d.(*session.Local)
	if !ok || d.Info().Kind != session.KindServer {
		writeError(w, http.StatusBadRequest, "unsupported", "files of hosted sessions are read through the terminal connection")
		return
	}
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	path := q.Get("path")
	if len(path) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_request", "path too long")
		return
	}
	// Through the folder the session holds, as the in-band file_get reads.
	h, body := local.ReadPath(path, q.Get("stat") == "1", s.fileDeny(), role)
	status := http.StatusOK
	if h.Kind == "error" {
		switch h.Error.Code {
		case "not_found":
			status = http.StatusNotFound
		case "denied", proto.ErrCodeFileDenied:
			status = http.StatusForbidden
		default:
			status = http.StatusBadRequest
		}
	}
	if q.Get("raw") == "1" && h.Kind == "file" && !h.Binary {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	writeJSON(w, status, map[string]any{"file": h, "content": string(body)})
}
