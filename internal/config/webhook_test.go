package config

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLookup makes LookupWebhookHost answer from answers, by host name, for
// the rest of the test, and counts the lookups, which may run at once. A name
// it does not know fails to resolve; one answered with "hang" waits until the
// lookup's time is up.
func fakeLookup(t *testing.T, answers map[string][]string) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	old := LookupWebhookHost
	LookupWebhookHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
		calls.Add(1)
		list, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		if len(list) == 1 && list[0] == "hang" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		var out []netip.Addr
		for _, a := range list {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	t.Cleanup(func() { LookupWebhookHost = old })
	return &calls
}

// withWebhooks is the default config with the webhooks given.
func withWebhooks(hooks ...Webhook) *Config {
	cfg := Defaults()
	cfg.Webhooks = hooks
	return cfg
}

// A webhook may not reach the server itself: http://127.0.0.1:1 is refused
// unless the webhook allows private addresses.
func TestWebhookToLoopbackNeedsAllowPrivate(t *testing.T) {
	fakeLookup(t, nil)
	err := withWebhooks(Webhook{URL: "http://127.0.0.1:1", Events: []string{"progress"}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "webhooks[0]") || !strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "allowPrivate") {
		t.Fatalf("got %v, want a refusal of the loopback address", err)
	}
	if err := withWebhooks(Webhook{URL: "http://127.0.0.1:1", Events: []string{"progress"}, AllowPrivate: true}).Validate(); err != nil {
		t.Fatalf("with allowPrivate: %v", err)
	}
}

// The rule covers loopback, link-local, private and unique-local, shared
// (CGNAT) and unspecified addresses, IPv4 and IPv6, IPv4 written as IPv6 too,
// and the deprecated IPv4-compatible form; a NAT64, 6to4, Teredo or SIIT
// address is judged by the IPv4 address it reaches (every one it carries).
// Every other address passes. An address in the URL is not looked up.
func TestWebhookAddressRule(t *testing.T) {
	calls := fakeLookup(t, nil)
	for host, kind := range map[string]string{
		"127.0.0.1":                "loopback",
		"127.8.9.10":               "loopback",
		"[::1]":                    "loopback",
		"[::ffff:127.0.0.1]":       "loopback",
		"169.254.169.254":          "link-local",
		"[fe80::1]":                "link-local",
		"10.0.0.1":                 "private",
		"172.16.0.1":               "private",
		"172.31.255.255":           "private",
		"192.168.1.1":              "private",
		"[::ffff:10.1.2.3]":        "private",
		"[fc00::1]":                "unique-local",
		"[fd12:3456::1]":           "unique-local",
		"100.64.0.1":               "CGNAT",
		"100.100.100.200":          "CGNAT",
		"100.127.255.254":          "CGNAT",
		"0.0.0.0":                  "unspecified",
		"0.1.2.3":                  "unspecified",
		"[::]":                     "unspecified",
		"[::127.0.0.1]":            "IPv4-compatible",
		"[::8.8.8.8]":              "IPv4-compatible",
		"[64:ff9b::7f00:1]":        "loopback",
		"[64:ff9b::a00:1]":         "private",
		"[64:ff9b::a9fe:a9fe]":     "link-local",
		"[64:ff9b:1::c0a8:101]":    "private",
		"[64:ff9b:1:abcd::6440:1]": "CGNAT",
	} {
		err := withWebhooks(Webhook{URL: "https://" + host + ":8443/hook", Events: []string{"error"}}).Validate()
		if err == nil || !strings.Contains(err.Error(), kind) || !strings.Contains(err.Error(), "allowPrivate") {
			t.Errorf("%s: got %v, want a refusal of a %s address", host, err, kind)
		}
	}
	for _, host := range []string{"93.184.215.14", "8.8.8.8", "172.32.0.1", "100.63.255.255", "100.128.0.1",
		"[2606:2800:21f:cb07:6820:80da:af6b:8b2c]", "[2001:4860:4860::8888]", "[64:ff9b::808:808]", "[64:ff9b:1::5db8:d70e]", "[64:ff9b:2::1]"} {
		if err := withWebhooks(Webhook{URL: "https://" + host + "/hook", Events: []string{"error"}}).Validate(); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("looked up an address %d times", n)
	}
	err := withWebhooks(Webhook{URL: "https://[64:ff9b::a00:1]/hook", Events: []string{"error"}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "10.0.0.1 through NAT64") {
		t.Fatalf("a NAT64 address does not name the IPv4 address it reaches: %v", err)
	}
	err = withWebhooks(Webhook{URL: "http://[2002:a00:1::1]/hook", Events: []string{"error"}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "10.0.0.1 through 6to4") || !strings.Contains(err.Error(), "allowPrivate") {
		t.Fatalf("a 6to4 address does not name the IPv4 address it reaches: %v", err)
	}
}

// CheckWebhookAddr is the rule for one address, as the server applies it to
// every address it is about to connect a webhook to.
func TestCheckWebhookAddr(t *testing.T) {
	for addr, want := range map[string]string{
		"93.184.215.14":           "",
		"2001:4860:4860::8888":    "",
		"64:ff9b::808:808":        "",
		"127.0.0.1":               "127.0.0.1 is a loopback address",
		"::ffff:127.0.0.1":        "::ffff:127.0.0.1 is a loopback address",
		"fe80::1%eth0":            "fe80::1%eth0 is a link-local address",
		"0.0.0.0":                 "0.0.0.0 is an unspecified address",
		"100.100.100.200":         "100.100.100.200 is in the shared address space (CGNAT)",
		"::7f00:1":                "::7f00:1 is an IPv4-compatible address (deprecated)",
		"64:ff9b::a9fe:a9fe":      "64:ff9b::a9fe:a9fe reaches 169.254.169.254 through NAT64, which is a link-local address",
		"64:ff9b:1:ffff::808:808": "",
		// 6to4 (2002::/16): the IPv4 address in bits 16 to 47.
		"2002:a00:1::1":   "2002:a00:1::1 reaches 10.0.0.1 through 6to4, which is a private address",
		"2002:808:808::1": "",
		// Teredo (2001::/32): the server's IPv4 address in bits 32 to 63, the
		// client's inverted in the last 32; either one counts.
		"2001:0:4136:e378:8000:63bf:f5ff:fffe": "reaches 10.0.0.1 through Teredo, which is a private address",
		"2001:0:7f00:1::f7f7:f7f7":             "reaches 127.0.0.1 through Teredo, which is a loopback address",
		"2001:0:4136:e378:8000:63bf:f7f7:f7f7": "",
		// SIIT, IPv4-translated (::ffff:0:a.b.c.d).
		"::ffff:0:a00:1":   "::ffff:0:a00:1 reaches 10.0.0.1 through SIIT, which is a private address",
		"::ffff:0:808:808": "",
		// 64:ff9b:1::/48 in the form a prefix shorter than /96 gives (u
		// octet zero, the last three bytes zero): its IPv4 address cannot be
		// told. Read as a /96 the first would reach 1.0.0.0, a public address,
		// so it is refused for the form alone. The second reads as 0.0.0.0 under
		// /96 and was refused already (as unspecified), so it pins the message.
		"64:ff9b:1:0:a:0:100:0": "a NAT64 prefix shorter than /96",
		"64:ff9b:1:a00:0:100::": "a NAT64 prefix shorter than /96",
		// A non-zero u octet (byte 8) is in no RFC 6052 form: refused outright,
		// whatever the /96 reading. The first reads as 1.0.0.0 under /96 but as
		// 10.0.0.1 from bytes 9 to 12, which is where a /64 prefix puts it; the
		// second reads as 8.8.8.8.
		"64:ff9b:1:0:ff0a:0:100:0":   "u octet",
		"64:ff9b:1:0:ff00:0:808:808": "u octet",
	} {
		err := CheckWebhookAddr(netip.MustParseAddr(addr))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", addr, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "allowPrivate")):
			t.Errorf("%s: got %v, want %q", addr, err, want)
		}
	}
}

// A host name is looked up once, at validation: a host whose addresses all
// pass is accepted, and one with an address the rule refuses is refused. One
// that does not resolve is only a warning: DNS may be down while the server
// starts, and the host is checked again before every delivery. With
// allowPrivate nothing is looked up.
func TestWebhookHostIsResolved(t *testing.T) {
	calls := fakeLookup(t, map[string][]string{
		"hooks.example.com": {"93.184.215.14", "2606:2800:21f:cb07:6820:80da:af6b:8b2c"},
		"internal.example":  {"93.184.215.14", "10.0.0.7"},
		"localhost":         {"127.0.0.1", "::1"},
		"empty.example":     {},
	})
	hook := func(host string, allow bool) *Config {
		return withWebhooks(Webhook{URL: "https://" + host + "/conductor", Events: []string{"artifact"}, AllowPrivate: allow})
	}
	if cfg := hook("hooks.example.com", false); cfg.Validate() != nil || calls.Load() != 1 || len(cfg.Warnings()) != 0 {
		t.Fatalf("a public host: %v after %d lookups, warnings %q", cfg.Validate(), calls.Load(), cfg.Warnings())
	}
	for host, want := range map[string]string{
		"internal.example": "internal.example: 10.0.0.7 is a private address",
		"localhost":        "loopback",
	} {
		if err := hook(host, false).Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", host, err, want)
		}
	}
	for _, host := range []string{"nowhere.example", "empty.example"} {
		cfg := hook(host, false)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", host, err)
		}
		if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "webhooks[0]") || !strings.Contains(w[0], host) {
			t.Errorf("%s: warnings %q", host, w)
		}
		// Validating again starts over.
		if err := cfg.Validate(); err != nil || len(cfg.Warnings()) != 1 {
			t.Errorf("%s validated twice: %v, warnings %q", host, err, cfg.Warnings())
		}
	}
	calls.Store(0)
	for _, host := range []string{"internal.example", "localhost", "nowhere.example"} {
		if cfg := hook(host, true); cfg.Validate() != nil || len(cfg.Warnings()) != 0 {
			t.Errorf("%s with allowPrivate: %v, warnings %q", host, cfg.Validate(), cfg.Warnings())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("allowPrivate looked the host up %d times", n)
	}
}

// The hosts are looked up at once, each for at most 2 seconds: three that
// never answer hold validation up for 2 seconds, not 6, and are warnings.
func TestWebhookLookupsRunAtOnceFor2Seconds(t *testing.T) {
	fakeLookup(t, map[string][]string{
		"slow1.example": {"hang"}, "slow2.example": {"hang"}, "slow3.example": {"hang"},
		"hooks.example.com": {"93.184.215.14"},
	})
	var hooks []Webhook
	for _, host := range []string{"slow1.example", "hooks.example.com", "slow2.example", "slow3.example"} {
		hooks = append(hooks, Webhook{URL: "https://" + host + "/c", Events: []string{"error"}})
	}
	cfg := withWebhooks(hooks...)
	start := time.Now()
	err := cfg.Validate()
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took < 1900*time.Millisecond || took > 4*time.Second {
		t.Fatalf("validation took %v, want about 2s", took)
	}
	w := cfg.Warnings()
	if len(w) != 3 || !strings.Contains(w[0], "webhooks[0]") || !strings.Contains(w[0], "slow1.example") || !strings.Contains(w[2], "webhooks[3]") {
		t.Fatalf("warnings %q", w)
	}
}

// A webhook is an http(s) URL of at most 2048 bytes with a host, listing at
// least one known event type: an entry type, an attention state, or
// exit_nonzero. A config holds at most 16. The messages name the webhook by
// its index and never quote its URL, whose query or user info may hold a
// token, or its secret.
func TestWebhookValidation(t *testing.T) {
	fakeLookup(t, map[string][]string{"hooks.example.com": {"93.184.215.14"}})
	ok := Webhook{URL: "https://hooks.example.com/c", Events: []string{"progress"}, Secret: "s3cret"}
	events := []string{"needs_input", "working", "done", "exit_nonzero", "attention", "input", "join", "leave", "link", "status",
		"progress", "artifact", "handoff", "tool_use", "tool_denied", "error"}
	if err := withWebhooks(Webhook{URL: ok.URL, Events: events}).Validate(); err != nil {
		t.Fatalf("every event type: %v", err)
	}
	for name, tc := range map[string]struct {
		hook Webhook
		want string
	}{
		"no url":            {Webhook{Events: []string{"progress"}}, "url must not be empty"},
		"ftp":               {Webhook{URL: "ftp://hooks.example.com/c?token=t0k3n", Events: []string{"progress"}}, "http or https"},
		"no scheme":         {Webhook{URL: "hooks.example.com/c", Events: []string{"progress"}}, "http or https"},
		"no host":           {Webhook{URL: "https:///c?token=t0k3n", Events: []string{"progress"}}, "host"},
		"opaque":            {Webhook{URL: "https:t0k3n", Events: []string{"progress"}}, "host"},
		"not a url":         {Webhook{URL: "https://hooks.example.com:t0k3n/c", Events: []string{"progress"}}, "not a valid URL"},
		"too long":          {Webhook{URL: "https://hooks.example.com/?t0k3n=" + strings.Repeat("a", 2048), Events: []string{"progress"}}, "2048 bytes"},
		"no events":         {Webhook{URL: ok.URL}, "at least one event"},
		"unknown event":     {Webhook{URL: ok.URL, Events: []string{"progress", "exit_zero"}}, `"exit_zero"`},
		"clear is no event": {Webhook{URL: ok.URL, Events: []string{"clear"}}, `"clear"`},
	} {
		tc.hook.Secret = "s3cret"
		err := withWebhooks(ok, tc.hook).Validate()
		if err == nil || !strings.Contains(err.Error(), "webhooks[1]: ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "t0k3n") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the message quotes the url or the secret: %v", name, err)
		}
	}
	if err := withWebhooks(ok, Webhook{URL: "https://hooks.example.com/c", Events: []string{"progress"}}).Validate(); err != nil {
		t.Fatalf("a webhook without a secret: %v", err)
	}
	many := make([]Webhook, 17)
	for i := range many {
		many[i] = ok
	}
	if err := withWebhooks(many...).Validate(); err == nil || !strings.Contains(err.Error(), "at most 16") {
		t.Fatalf("17 webhooks: %v", err)
	}
	if err := withWebhooks(many[:16]...).Validate(); err != nil {
		t.Fatalf("16 webhooks: %v", err)
	}
}

// Webhooks come from the config file, or from CONDUCTOR_WEBHOOKS as a JSON
// array of the same objects, which replaces them. Unknown fields are refused
// in both, and the error does not quote the value.
func TestWebhooksFromFileAndEnvironment(t *testing.T) {
	fakeLookup(t, map[string][]string{"file.example.com": {"93.184.215.14"}, "env.example.com": {"93.184.215.15"}})
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	body := `{"allowedRoots":["` + dir + `"],"webhooks":[{"url":"https://file.example.com/h","events":["needs_input","exit_nonzero"],"secret":"s1"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_WEBHOOKS", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Webhooks) != 1 || cfg.Webhooks[0].URL != "https://file.example.com/h" || cfg.Webhooks[0].Secret != "s1" || len(cfg.Webhooks[0].Events) != 2 || cfg.Webhooks[0].AllowPrivate {
		t.Fatalf("from the file: %+v", cfg.Webhooks)
	}
	t.Setenv("CONDUCTOR_WEBHOOKS", `[{"url":"https://env.example.com/h","events":["error"],"secret":"s2"},{"url":"http://127.0.0.1:9/h","events":["done"],"allowPrivate":true}]`)
	if cfg, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Webhooks) != 2 || cfg.Webhooks[0].URL != "https://env.example.com/h" || cfg.Webhooks[0].Secret != "s2" || !cfg.Webhooks[1].AllowPrivate {
		t.Fatalf("from the environment: %+v", cfg.Webhooks)
	}
	t.Setenv("CONDUCTOR_WEBHOOKS", "[]")
	if cfg, err = Load(path); err != nil || len(cfg.Webhooks) != 0 {
		t.Fatalf("an empty array clears them: %+v %v", cfg.Webhooks, err)
	}
	for _, v := range []string{
		`[{"url":"https://env.example.com/h","events":["error"],"token":"s3cret"}]`,
		`{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"}`,
		`[{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"}] [`,
		`[{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"`,
		`[{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"}]]`,
		`[{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"}]}`,
		`[{"url":"https://env.example.com/h","events":["error"],"secret":"s3cret"}]}}garbage`,
		`[] []`,
	} {
		t.Setenv("CONDUCTOR_WEBHOOKS", v)
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "CONDUCTOR_WEBHOOKS") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: %v", v, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"webhooks":[{"url":"https://file.example.com/h","events":["error"],"headers":{}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_WEBHOOKS", "")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("an unknown field in the file: %v", err)
	}
}

// Endpoint is what the API shows of a webhook: its URL without user info,
// query or fragment, which may hold a token.
func TestWebhookEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"https://hooks.example.com/c":                             "https://hooks.example.com/c",
		"https://user:pw@hooks.example.com:8443/a/b?token=t#frag": "https://hooks.example.com:8443/a/b",
		"http://[::1]:9000/hook?x=1":                              "http://[::1]:9000/hook",
		"https://hooks.example.com":                               "https://hooks.example.com",
		"https://hooks.example.com/a%2Fb?t=1":                     "https://hooks.example.com/a%2Fb",
		"https://hooks.example.com:t0k3n/c":                       "",
	} {
		if got := (Webhook{URL: url}).Endpoint(); got != want {
			t.Errorf("%s: %q, want %q", url, got, want)
		}
	}
}

// A webhook may list decision, a durable decision's change, and a config
// that names it validates; a name beside it that is none still fails.
func TestDecisionIsAWebhookEvent(t *testing.T) {
	fakeLookup(t, map[string][]string{"hooks.example.com": {"93.184.215.14"}})
	if !ValidWebhookEvent(EventDecision) || EventDecision != "decision" {
		t.Fatal("decision is not a webhook's event")
	}
	if err := withWebhooks(Webhook{URL: "https://hooks.example.com/c", Events: []string{"decision"}}).Validate(); err != nil {
		t.Fatalf("a webhook of decisions: %v", err)
	}
	if err := withWebhooks(Webhook{URL: "https://hooks.example.com/c", Events: []string{"decision", "decisions"}}).Validate(); err == nil || !strings.Contains(err.Error(), `"decisions"`) {
		t.Fatalf("an unknown event beside it: %v", err)
	}
}
