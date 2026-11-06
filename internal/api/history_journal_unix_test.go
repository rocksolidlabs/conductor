//go:build unix

package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/journal"
)

// Codex round 4, finding 2: a server session whose record's first write
// failed (its folder read-only for the moment) keeps its recorder: once
// the disk answers again the record is written, and the session's end
// reaches it, so the record is ended and forgettable, never left open.
func TestARecordWhoseFirstWriteFailedFollowsItsSession(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder's mode does not stop root")
	}
	e := newTestEnv(t, nil)
	sessions := filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir)
	if err := os.Chmod(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o700) })
	id := e.createSession("cat")
	if err := os.Chmod(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sessions, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("the record's first write did not fail: %v", err)
	}
	e.stopAndWait(id)
	if r := endedRecord(t, e, id); r.Ending == "" || !r.LostAt.IsZero() {
		t.Fatalf("the record once the disk answered: %+v", r)
	}
	if resp, _ := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgetting it: %d", resp.StatusCode)
	}
}
