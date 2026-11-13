package crew

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// updateUnchangedGoldens rewrites testdata/crew-unchanged: `go test
// ./internal/crew -run TestACrewWithoutCompareIsUnchanged
// -update-crew-goldens`. They were written by main's code before a crew
// could carry a compare block, so rewrite them only for a change of the
// crew file meant to show in every crew.
var updateUnchangedGoldens = flag.Bool("update-crew-goldens", false, "rewrite testdata/crew-unchanged")

// A crew without a compare block encodes byte for byte as before the block
// existed, as its file (store.Encode) and as a run's crew (crewAsLaunched):
// every example crew and the managed fixture, against the goldens.
func TestACrewWithoutCompareIsUnchanged(t *testing.T) {
	dir := filepath.Join("testdata", "crew-unchanged")
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	crews := Examples("/srv/work", at)
	fixture := managedCrew()
	fixture.Manifest, fixture.Setup = &Manifest{Verify: []Check{{ID: "lint", Argv: []string{"make", "lint"}}}}, nil
	crews = append(crews, fixture)
	if len(crews) < 6 {
		t.Fatalf("%d crews", len(crews))
	}
	for _, c := range crews {
		file, err := store.Encode(c)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]byte{c.ID + ".json": file, c.ID + ".run.json": []byte(crewAsLaunched(c))}
		for name, b := range got {
			path := filepath.Join(dir, name)
			if *updateUnchangedGoldens {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, b, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !bytes.Equal(b, want) {
				t.Errorf("%s differs from its golden:\n got: %s\nwant: %s", name, b, want)
			}
		}
	}
}
