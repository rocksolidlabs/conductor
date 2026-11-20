package export_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// The context an attempt was given (docs/plans/context-seams.md, CT-3)
// goes with an export: the ledger's row and the data directory's text
// both, so that a restored server shows the text whose digest is its
// row's. A write the server had not finished (its .tmp) is left out.
func TestDeliveredContextExported(t *testing.T) {
	f := newFixture(t)
	const id = "team-00000001"
	f.run(t, id, ledger.RunRunning)
	const body = "main calls Build."
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	if _, err := f.l.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: "work", State: ledger.TaskRunning, Attempts: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptRunning, ReservedAt: now}),
			tx.AddAttemptContext([]ledger.AttemptContextRow{{RunID: id, TaskID: "build", N: 1, Seq: 1, Phase: ledger.PhaseTask, Source: "graph", Title: "Callers", SHA256: digest, Bytes: len(body)}}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	file, _ := json.Marshal(map[string]any{"v": 1, "blocks": []map[string]any{{"seq": 1, "phase": "task", "source": "graph", "title": "Callers", "body": body}}})
	rel := "runs/context/" + id + "/build.1.json"
	put(t, f.dir, rel, string(file))
	put(t, f.dir, rel+".tmp", "a write not finished")

	b, _ := archive(t, f.source())
	_, files := entries(t, b)
	if _, ok := files[export.DataDir+rel]; !ok {
		t.Fatal("the context's text is left out")
	}
	if _, ok := files[export.DataDir+rel+".tmp"]; ok {
		t.Fatal("an unfinished write is exported")
	}
	to := filepath.Join(t.TempDir(), "restored")
	if _, err := restore(b, to); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(to, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	rows, err := l.AttemptContext(context.Background(), id, "build")
	if err != nil || len(rows[1]) != 1 || rows[1][0].SHA256 != digest {
		t.Fatalf("the restored rows: %v %v", rows, err)
	}
	cf, err := crew.OpenContextFiles(filepath.Join(to, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	if got, ok, err := cf.Body(id, "build", 1, 1, digest); err != nil || !ok || got != body {
		t.Fatalf("the restored text: %q %v %v", got, ok, err)
	}
}
