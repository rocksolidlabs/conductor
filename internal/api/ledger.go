package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// openLedger opens the managed runs' ledger under the data directory's
// runs/ (beside the records) and concludes what the last owner left open
// (ledger.Recover). A ledger that opens read-only (another server holds
// the owner lock, the filesystem has no locks, the database is damaged or
// newer) is logged, and managed launches answer 503 from then on; the
// legacy runs are untouched by it. observe is given the ledger before its
// recovery, so the hooks it installs (the sync seam's) are told of every
// commit.
func openLedger(ctx context.Context, st *store.Store, records *crew.Records, log logger, observe func(*ledger.Ledger)) *ledger.Ledger {
	ld, err := ledger.Open(filepath.Join(st.Dir(), "runs"))
	if err != nil {
		// The directory or the file itself: the server runs its legacy
		// crews all the same, with no ledger.
		log.Warn("managed runs are off: the ledger could not be opened", "err", err)
		return nil
	}
	if observe != nil {
		observe(ld)
	}
	if ro, why := ld.ReadOnly(); ro {
		// Held by another server, this one serves meanwhile and takes the
		// lock as it is let go (the server it replaces shutting down):
		// then it concludes what that one left open.
		log.Warn("managed runs are off: the ledger is read-only", "why", why)
		ld.OnOwned(func() {
			log.Info("the ledger's owner lock was taken: managed runs are on")
			recoverLedger(context.Background(), ld, records, log)
		})
		return ld
	}
	recoverLedger(ctx, ld, records, log)
	return ld
}

// recoverLedger concludes what the last owner left open (ledger.Recover),
// logs each run touched, and writes each one's record as the ledger now
// has it (crew.Recovered): interrupted, its attempts concluded, so the
// run reads so until it is continued.
func recoverLedger(ctx context.Context, ld *ledger.Ledger, records *crew.Records, log logger) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	recovered, err := ld.Recover(rctx)
	if err != nil {
		log.Warn("the ledger's recovery failed", "err", err)
	}
	for _, r := range recovered {
		log.Info("a managed run was left open by the last server's end", "run", r.RunID, "interrupted", r.Interrupted, "attempts", r.Attempts, "checks", r.Checks,
			"prerequisites", r.Prerequisites, "trees", r.Trees, "commands", r.Commands, "services", len(r.Services))
		// A service the last server started may have outlived it: it is
		// named here, with what tells it from a later process of the same
		// pid, and never signalled (a pid after a restart is nobody's).
		for _, sv := range r.Services {
			if sv.Confirmed {
				log.Warn("a service the last server started may still be running: it is reported, never signalled", "run", r.RunID, "task", sv.Task, "attempt", sv.N,
					"tree", sv.Tree, "service", sv.ID, "pid", sv.Pid, "group", sv.Group, "startedAt", sv.StartedAt)
				continue
			}
			log.Warn("a service the last server was starting may be running: its process was never recorded, so it cannot be named", "run", r.RunID, "task", sv.Task,
				"attempt", sv.N, "tree", sv.Tree, "service", sv.ID)
		}
	}
	syncRecords(rctx, ld, records, log)
}

// syncRecords writes the record of every managed run the ledger has whose
// record lags the ledger (a snapshot cut short by the last server's end,
// or none at all), or says something of the run is under way (a tree
// being ended, a prerequisite running: crew.UnderWay), which nothing is
// after the server's end, as the ledger has it (crew.Recovered): what the
// API answers for a run it no longer holds is then the ledger's state.
func syncRecords(ctx context.Context, ld *ledger.Ledger, records *crew.Records, log logger) {
	if records == nil {
		return
	}
	rows, err := ld.Runs(ctx)
	if err != nil {
		log.Warn("the managed runs could not be listed for their records", "err", err)
		return
	}
	for _, row := range rows {
		rec, ok, err := records.Get(row.ID)
		if err != nil {
			continue
		}
		if ok && rec.Managed != nil && rec.Managed.Revision >= row.Revision && !crew.UnderWay(rec) {
			continue
		}
		from, found, err := crew.SQLLedger(ld).Load(ctx, row.ID)
		if err != nil || !found {
			log.Warn("a managed run's record stays as it was", "run", row.ID, "err", err)
			continue
		}
		if !ok {
			rec = crew.Run{}
		}
		if err := records.Save(crew.Recovered(rec, from)); err != nil {
			log.Warn("a managed run's record could not be written", "run", row.ID, "err", err)
		}
	}
}

// logger is what openLedger logs with.
type logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// commandIDPattern bounds a command id: what a client may mint
// (docs/plans/agent-harness-order.md, decision 10).
var commandIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,32}$`)

// adminActor is the actor a workbench token's commands are kept under; an
// identity provider's principal's are kept under its subject (actorOf).
const adminActor = "admin"

// handleGetCommand answers a command's receipt: 200 {command}, 404 when
// the run or the caller's command with that id is unknown (a receipt is
// found by the actor that made it, actorOf). It never acts:
// a client that lost a reply reads here before it sends again.
func (s *Server) handleGetCommand(w http.ResponseWriter, r *http.Request) {
	runID, id := r.PathValue("run"), r.PathValue("command")
	if !crew.ValidRunID(runID) || !commandIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id and a command id of 1 to 32 letters, digits, '.', '_', '~' or '-'")
		return
	}
	if s.ledger == nil {
		// No ledger opened: the receipt may exist and cannot be read, which
		// is not its absence.
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
		return
	}
	c, ok, err := s.ledger.Receipt(r.Context(), runID, actorOf(r), id)
	if errors.Is(err, ledger.ErrUnavailable) || errors.Is(err, ledger.ErrClosed) {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
		return
	}
	if err != nil {
		s.log.Warn("command receipt", "run", runID, "command", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "the receipt could not be read")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such command")
		return
	}
	var receipt json.RawMessage
	if c.Receipt != "" {
		receipt = json.RawMessage(c.Receipt)
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": map[string]any{
		"id": c.ID, "intent": c.Intent, "status": c.Status, "receipt": receipt, "createdAt": c.CreatedAt,
	}})
}
