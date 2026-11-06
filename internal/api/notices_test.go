package api

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// A text a main handed in (WithNotices) is what /third-party-notices.txt
// serves, by a workbench server and by a switchyard, with the headers the
// embedded text is served with; WithNotices("") and no option at all serve
// the embedded text, byte for byte.
func TestNoticesServeTheTextAMainHandedIn(t *testing.T) {
	const own = "THIRD-PARTY NOTICES of a build that registers providers\n\n  example.com/provider v1.2.3 (Apache-2.0)\n"
	for _, sy := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			opts []Option
			want string
		}{
			{"none", nil, conductor.ThirdPartyNotices},
			{"empty", []Option{WithNotices("")}, conductor.ThirdPartyNotices},
			{"own", []Option{WithNotices(own)}, own},
		} {
			e := newTestEnvAgents(t, func(c *config.Config) {
				if sy {
					relay := true
					c.Switchyard.Enabled = true
					c.Switchyard.Relay = &relay
				}
			}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, tc.opts...)
			resp, err := http.Get(e.http.URL + "/third-party-notices.txt")
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || string(b) != tc.want {
				t.Fatalf("switchyard %v, %s: %d, %d bytes (%.80q), want %d bytes", sy, tc.name, resp.StatusCode, len(b), b, len(tc.want))
			}
			for h, want := range map[string]string{
				"Content-Type":           "text/plain; charset=utf-8",
				"X-Content-Type-Options": "nosniff",
				"Cache-Control":          "no-cache",
			} {
				if got := resp.Header.Get(h); got != want {
					t.Errorf("switchyard %v, %s: %s %q, want %q", sy, tc.name, h, got, want)
				}
			}
		}
	}
}
