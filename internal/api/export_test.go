package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/export"
)

// getExport asks e's server for its archive with token, and returns the
// response and its body.
func getExport(t *testing.T, e *testEnv, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", e.http.URL+"/api/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// exportMatches checks the archive b against the data directory dir: the
// manifest last, every entry listed with its size and hash, every file but
// the two databases (snapshots) and the catalog (resolved) byte for byte
// the directory's, and every regular file of the directory in the archive
// or listed as left out. It returns the archive's paths.
func exportMatches(t *testing.T, b []byte, dir string) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	last := zr.File[len(zr.File)-1]
	if last.Name != export.ManifestName {
		t.Fatalf("the last entry is %s", last.Name)
	}
	rc, _ := last.Open()
	var m export.Manifest
	err = json.NewDecoder(rc).Decode(&m)
	rc.Close()
	if err != nil || m.Format != export.Format || len(m.Files) != len(zr.File)-1 {
		t.Fatalf("the manifest: %+v %v", m, err)
	}
	paths := map[string]bool{}
	for i, f := range zr.File[:len(zr.File)-1] {
		listed := m.Files[i]
		if f.Name != export.DataDir+listed.Path {
			t.Fatalf("entry %d is %s, the manifest's %s", i, f.Name, listed.Path)
		}
		rc, _ := f.Open()
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != listed.Size || hex.EncodeToString(sum[:]) != listed.SHA256 {
			t.Fatalf("%s: its size or hash is not the manifest's", f.Name)
		}
		paths[listed.Path] = true
		switch listed.Path {
		case export.LedgerPath, export.DispatchPath, "catalog.json":
			continue
		}
		disk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(listed.Path)))
		if err != nil || !bytes.Equal(disk, data) {
			t.Fatalf("%s: not the data directory's (%v)", listed.Path, err)
		}
	}
	left := map[string]bool{}
	for _, l := range m.Left {
		left[l.Path] = true
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if paths[rel] || left[rel] {
			return nil
		}
		for l := range left {
			if strings.HasSuffix(l, "/") && strings.HasPrefix(rel, l) {
				return nil
			}
		}
		if strings.HasPrefix(rel, "dispatch/attachments/") {
			return nil // orphans: no row names them
		}
		t.Errorf("%s: neither in the archive nor left out", rel)
		return nil
	})
	return paths
}

// GET /api/export is the admin's alone: without a token, with a host's,
// with a share link's, it is refused; the admin gets a zip, named for its
// time, whose manifest and files match the data directory. A switchyard's
// admin gets its kept links in it, which hold the links' hashes, never
// their tokens.
func TestExportRoute(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	link, _ := out["url"].(string)
	linkToken := link[strings.LastIndex(link, "/")+1:]
	for _, tok := range []string{"", "test-host-token", linkToken, "wrong"} {
		if resp, _ := getExport(t, e, tok); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", tok, resp.StatusCode)
		}
	}
	crews, err := e.srv.store.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	if err := crews.Save("team.json", map[string]string{"id": "team", "name": "Team"}); err != nil {
		t.Fatal(err)
	}
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("the admin's export: %d %s: %.200s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	if cd := resp.Header.Get("Content-Disposition"); !regexp.MustCompile(`^attachment; filename="conductor-export-\d{8}T\d{6}Z\.zip"$`).MatchString(cd) {
		t.Fatalf("Content-Disposition: %q", cd)
	}
	paths := exportMatches(t, b, e.srv.store.Dir())
	for _, want := range []string{"crews/team.json", export.DispatchPath} {
		if !paths[want] {
			t.Fatalf("the archive lacks %s: %v", want, paths)
		}
	}

	dir := filepath.Join(t.TempDir(), "yard")
	y := keptSwitchyard(t, dir, nil)
	hi, hs := instanceHost(y, "instance-secret-0123456789", "1")
	host, _ := dialFakeHostWith(t, y, hi, hs, "")
	linkID, yardToken := mintHostLink(t, host, "r1")
	if resp, _ := getExport(t, y, "test-host-token"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a host token at the switchyard: %d", resp.StatusCode)
	}
	resp, b = getExport(t, y, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the switchyard's export: %d %.200s", resp.StatusCode, b)
	}
	paths = exportMatches(t, b, dir)
	if !paths["links/"+linkID+".json"] {
		t.Fatalf("the switchyard's archive lacks its kept link: %v", paths)
	}
	if bytes.Contains(b, []byte(yardToken)) {
		t.Fatal("the archive holds a link's token")
	}
	host.c.Close(websocket.StatusNormalClosure, "")
	<-host.done
}
