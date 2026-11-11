package tracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

const secret = "ghp_FixtureSecretNeverPrinted0123456789"

// token is a token's credential for a host Token admits.
func token(t *testing.T, host string) Credential {
	t.Helper()
	c, err := Token(host, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCredentialNeverPrinted(t *testing.T) {
	plain := token(t, "github.com")
	tagged := plain.ForWrite(Write{Kind: WriteComment, Publication: "pub1", Try: 2, PostSha256: strings.Repeat("a", 64)})
	type holder struct {
		C     Credential
		inner Credential
		P     *Credential
	}
	for _, c := range []Credential{plain, tagged} {
		var out []string
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			out = append(out, fmt.Sprintf(f, c), fmt.Sprintf(f, holder{c, c, &c}), fmt.Sprintf(f, []Credential{c}))
		}
		out = append(out, c.String(), fmt.Sprint(c.Transport(nil)), fmt.Sprintf("%+v", c.Transport(nil)), fmt.Sprintf("%#v", c.Transport(nil)))
		b, err := json.Marshal(holder{C: c, P: &c})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
		for _, h := range []func(*bytes.Buffer) slog.Handler{
			func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
			func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		} {
			var buf bytes.Buffer
			slog.New(h(&buf)).Info("x", "cred", c, "held", holder{C: c}, "ptr", &c)
			out = append(out, buf.String())
		}
		for _, s := range out {
			if strings.Contains(s, secret) || strings.Contains(s, "FixtureSecret") {
				t.Fatalf("the secret printed: %s", s)
			}
		}
		if !strings.Contains(c.String(), c.Fingerprint()) || strings.Contains(secret, c.Fingerprint()) {
			t.Fatalf("String %q, fingerprint %q", c.String(), c.Fingerprint())
		}
	}
	if (Credential{}).String() != "token(none)" || !(Credential{}).IsZero() {
		t.Fatal("the zero credential")
	}
}

type recordRT struct{ got []*http.Request }

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = append(r.got, req)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func TestCredentialTransport(t *testing.T) {
	base := &recordRT{}
	c := token(t, "github.com")
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if _, err := c.Transport(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(base.got) != 1 {
		t.Fatalf("%d requests", len(base.got))
	}
	sent := base.got[0]
	if sent.Header.Get("Authorization") != "Bearer "+secret {
		t.Fatalf("Authorization %q", sent.Header.Get("Authorization"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("the caller's request was changed")
	}
	if len(sent.Header) != 2 || sent.Header.Get("Accept") != "application/vnd.github+json" || sent.URL.String() != req.URL.String() || sent.Method != req.Method {
		t.Fatalf("the transport did more than set Authorization: %v", sent.Header)
	}
	if _, ok := c.Write(); ok {
		t.Fatal("an untagged credential carries a write")
	}
	w := Write{Kind: WriteClose, Publication: "pub9", Try: 3, Comment: "123"}
	tagged := c.ForWrite(w)
	if got, ok := tagged.Write(); !ok || got != w {
		t.Fatalf("tag %+v %v", got, ok)
	}
	if _, ok := c.Write(); ok {
		t.Fatal("ForWrite changed the credential it was called on")
	}
	if tagged.Host() != "github.com" || tagged.Fingerprint() != c.Fingerprint() {
		t.Fatal("the tag changed the credential")
	}
	base.got = nil
	if _, err := tagged.Transport(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(base.got[0].Header) != 2 {
		t.Fatalf("a tagged credential's transport sent %v", base.got[0].Header)
	}
	// The zero credential's transport is the base, adding nothing.
	base.got = nil
	_, _ = (Credential{}).Transport(base).RoundTrip(req)
	if base.got[0].Header.Get("Authorization") != "" {
		t.Fatal("the zero credential sent Authorization")
	}
}

// The token travels only on a request whose URL is the credential's API
// host, checked by the credential's transport itself at every hop.
func TestCredentialTransportChecksEveryHop(t *testing.T) {
	base := &recordRT{}
	tr := token(t, "github.com").Transport(base)
	for _, u := range []string{"https://collector.example/x", "https://api.github.com.collector.example/x", "http://api.github.com/x", "https://user@api.github.com/x", "https://api.github.com:443/x", "https://API.github.com/x"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if _, err := tr.RoundTrip(req); !errors.Is(err, ErrOffHost) || !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", u, err)
		}
	}
	// A Host header set apart from the URL is refused too.
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	req.Host = "collector.example"
	if _, err := tr.RoundTrip(req); !errors.Is(err, ErrOffHost) {
		t.Errorf("a Host apart from the URL: %v", err)
	}
	if len(base.got) != 0 {
		t.Fatalf("%d requests reached the base", len(base.got))
	}
	req, _ = http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if _, err := tr.RoundTrip(req); err != nil || len(base.got) != 1 || base.got[0].Header.Get("Authorization") != "Bearer "+secret {
		t.Fatalf("the API host's request: %v", err)
	}
}

// A credential carries its own destination: it is made for one API address
// (https and its own admitted host; plain http only to a loopback form,
// made in loopback mode), a host not admitted makes no credential, and its
// transport sends to that address alone, whatever request it is handed.
func TestCredentialCarriesItsDestination(t *testing.T) {
	for _, c := range []struct {
		host     string
		loopback bool
		api      string
	}{
		{"github.com", false, "https://api.github.com"},
		{"github.com", true, "https://api.github.com"},
		{"ghe.example.test", false, "https://ghe.example.test"},
		{"localhost:8123", true, "http://localhost:8123"},
		{"127.0.0.1:8123", true, "http://127.0.0.1:8123"},
		{"[::1]:8123", true, "http://[::1]:8123"},
	} {
		cred, err := Token(c.host, secret, c.loopback)
		if err != nil {
			t.Fatalf("%s: %v", c.host, err)
		}
		if scheme, host := cred.API(); scheme+"://"+host != c.api || cred.Host() != c.host {
			t.Errorf("%s: API %s://%s", c.host, scheme, host)
		}
		base := &recordRT{}
		tr := cred.Transport(base)
		for _, u := range []string{"http://collector.example/x", "https://collector.example/x", "http://169.254.169.254/x", "http://api.github.com/x", "https://localhost:8123/x", "http://ghe.example.test/x"} {
			if u == c.api+"/x" {
				continue
			}
			req, _ := http.NewRequest(http.MethodGet, u, nil)
			if _, err := tr.RoundTrip(req); !errors.Is(err, ErrOffHost) {
				t.Errorf("a token for %s: %s: %v", c.host, u, err)
			}
		}
		if len(base.got) != 0 {
			t.Errorf("a token for %s left its API address: %d requests", c.host, len(base.got))
		}
		req, _ := http.NewRequest(http.MethodGet, c.api+"/x", nil)
		req.Header.Set("Referer", "https://private-user-images.githubusercontent.com/1/x.png?jwt=J")
		if _, err := tr.RoundTrip(req); err != nil || len(base.got) != 1 || base.got[0].Header.Get("Referer") != "" {
			t.Errorf("a token for %s to its API: %v", c.host, err)
		}
	}
	for _, c := range []struct {
		host     string
		loopback bool
	}{
		{"localhost:8123", false}, {"127.0.0.1:8123", false}, {"127.0.0.1", true}, {"localhost", true}, {"169.254.169.254", false},
		{"2130706433", false}, {"0x7f000001", false}, {"ghe.example.test@collector.example", false}, {"GitHub.com", false}, {"", false},
	} {
		if _, err := Token(c.host, secret, c.loopback); !errors.Is(err, ErrHost) {
			t.Errorf("Token(%q, loopback %v): %v", c.host, c.loopback, err)
		}
	}
}
