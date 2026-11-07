package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

// proxied is the path a proxy that normalises sends on (nginx with a path
// in proxy_pass, say): decoded, its dot steps and doubled slashes
// resolved, escaped again.
func proxied(p string) string {
	dec, err := url.PathUnescape(p)
	if err != nil {
		return p
	}
	return (&url.URL{Path: path.Clean(dec)}).EscapedPath()
}

// A person's page is the app's for every name: each name of the testdata
// the app's links and the people route share (internal/api's
// person-segments.json, ".", "..", a slash that climbs to the brand's
// image, a percent sign, a space, letters past ASCII, the longest names),
// asked at its segment, as written and as a normalising proxy sends it
// on, is answered with the app's page, never with a file of the build.
func TestAPersonsPageIsTheApps(t *testing.T) {
	b, err := os.ReadFile("../api/testdata/person-segments.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct{ Name, Segment string } `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("the testdata: %v", err)
	}
	h := handler(fstest.MapFS{
		"index.html":                  {Data: []byte("the app")},
		"brand/conductor-favicon.svg": {Data: []byte("<svg/>")},
		"people.json":                 {Data: []byte("{}")},
	})
	for _, c := range f.Cases {
		p := "/people/" + c.Segment
		for _, asked := range []string{p, proxied(p)} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, asked, nil))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK || !strings.Contains(string(body), "the app") {
				t.Fatalf("%q at %s: %d %q", c.Name, asked, rec.Code, body)
			}
		}
	}
}
