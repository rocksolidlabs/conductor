package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// handleExport streams the data directory as an export archive
// (docs/plans/ext.md, C6; docs/protocol.md, GET /api/export): the admin's,
// a switchyard's too, the archive written as it is made, nothing buffered
// whole. The databases are snapshotted on the server's own connections,
// consistent while it writes; the catalog is resolved against the
// configured one, so the archive holds saved agents' env values in full:
// it is the person's own file.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no_data_dir", "the server keeps no data directory to export")
		return
	}
	name := "conductor-export-" + time.Now().UTC().Format("20060102T150405Z") + ".zip"
	cw := &countingWriter{w: w, header: func() {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}}
	m, err := export.Write(r.Context(), cw, s.exportSource())
	if err != nil {
		if cw.n == 0 {
			s.log.Warn("export refused", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "the export could not be made: "+err.Error())
			return
		}
		// The archive is cut short, with no manifest: no restore takes it.
		s.log.Warn("export cut short", "err", err, "bytes", cw.n)
		return
	}
	s.log.Info("data directory exported", "files", len(m.Files), "left", len(m.Left), "bytes", cw.n)
}

// exportSource is the export's view of this server's data directory: its
// own ledger and Dispatch connections for the snapshots (a database it
// has none open on, through a read-only one of the export's), and the
// catalog it runs with.
func (s *Server) exportSource() export.Source {
	dir := s.store.Dir()
	return export.Source{
		Dir: dir,
		Snapshot: func(ctx context.Context, rel, dst string) error {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			switch rel {
			case export.LedgerPath:
				if s.ledger != nil {
					err := s.ledger.Snapshot(ctx, dst)
					if !errors.Is(err, ledger.ErrUnavailable) {
						return err
					}
				}
				return ledger.SnapshotFile(ctx, path, dst)
			case export.DispatchPath:
				if s.dispatch != nil {
					err := s.dispatch.Snapshot(ctx, dst)
					if !errors.Is(err, dispatch.ErrUnavailable) {
						return err
					}
				}
				return dispatch.SnapshotFile(ctx, path, dst)
			}
			return fmt.Errorf("no database at %s", rel)
		},
		Catalog: func() ([]byte, error) {
			s.catalogMu.Lock()
			ov, base := s.overlay, s.base
			s.catalogMu.Unlock()
			return export.ResolveCatalog(ov, base)
		},
	}
}

// countingWriter is the response as the archive is written to it: the
// headers set before its first byte, and the bytes counted, so a failure
// before any is answered as an error.
type countingWriter struct {
	w      http.ResponseWriter
	header func()
	n      int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.n == 0 && c.header != nil {
		c.header()
		c.header = nil
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
