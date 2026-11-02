package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
)

// getCapabilities answers GET /api/capabilities with token: its status and
// its body as sent.
func getCapabilities(t *testing.T, e *testEnv, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", e.http.URL+"/api/capabilities", nil)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// capabilityRows is the reply's rows, decoded as a reader would, refusing
// a field the row does not have.
func capabilityRows(t *testing.T, body string) []capabilityRow {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var r capabilitiesReply
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("the reply %q: %v", body, err)
	}
	if r.Providers == nil {
		t.Fatalf("the reply %q has no providers list", body)
	}
	return r.Providers
}

// A plain build, made with no providers, answers an empty list (never
// null), to the workbench token alone: no token and a host token are
// refused.
func TestCapabilitiesPlainBuild(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.StartExtensions(t.Context())
	for _, tok := range []string{"", "test-host-token", "not-a-token"} {
		if code, body := getCapabilities(t, e, tok); code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d %s, want 401", tok, code, body)
		}
	}
	code, body := getCapabilities(t, e, adminToken)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if got := strings.TrimSpace(body); got != `{"providers":[]}` {
		t.Fatalf("a plain build answered %s, want {\"providers\":[]}", got)
	}
}

// cloudSlots is one value in two slots, identity and media, and a Starter
// whose first Start fails: it stays starting until a later try succeeds.
type cloudSlots struct{ starts atomic.Int32 }

func (*cloudSlots) Name() string { return "cloud" }
func (*cloudSlots) Authenticate(context.Context, string) (ext.Principal, bool, error) {
	return ext.Principal{}, false, nil
}
func (*cloudSlots) ICEServers(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
	return nil, nil
}
func (*cloudSlots) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	return ext.RoomJoin{}, false, nil
}
func (c *cloudSlots) Start(context.Context, ext.Host) error {
	if c.starts.Add(1) == 1 {
		return errors.New("the service is down")
	}
	return nil
}

// plansOnly is an entitlement source and nothing else, with no Start: no
// code of this repository asks it (C7), and the route lists it by kind and
// name all the same.
type plansOnly struct{}

func (plansOnly) Name() string { return "plans" }
func (plansOnly) Entitled(context.Context, string) (ext.Entitlement, error) {
	return ext.Entitlement{Allowed: true}, nil
}

// The route names every registered provider, in the registry's order, a
// value in two slots once per slot, the entitlement source among them by
// kind and name only: a provider whose Start has not yet succeeded is
// starting (before its first try, and after a try that failed while its
// next waits on the backoff), then ready once a try succeeds, with no
// restart; one that needs no Start is ready from the first.
func TestCapabilitiesNamesProviders(t *testing.T) {
	cloud := &cloudSlots{}
	sink := &fakeSink{name: "push", got: make(chan ext.Notification, 8)}
	graph := &fakeContext{name: "graph"}
	p := ext.Providers{
		Identity: cloud, Media: cloud,
		Context:       []ext.ContextProvider{graph},
		Notifications: []ext.NotificationSink{sink},
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "cloud", Value: cloud},
			{Kind: ext.KindContext, Name: "graph", Value: graph},
			{Kind: ext.KindNotifications, Name: "push", Value: sink},
			{Kind: ext.KindEntitlements, Name: "plans", Value: plansOnly{}},
			{Kind: ext.KindMedia, Name: "cloud", Value: cloud},
		},
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithExtensions(local.Fill(p)))
	clock := newFakeClock(e.srv)

	want := func(cloudState string) []capabilityRow {
		return []capabilityRow{
			{Kind: ext.KindIdentity, Name: "cloud", State: cloudState},
			{Kind: ext.KindContext, Name: "graph", State: "ready"},
			{Kind: ext.KindNotifications, Name: "push", State: "ready"},
			{Kind: ext.KindEntitlements, Name: "plans", State: "ready"},
			{Kind: ext.KindMedia, Name: "cloud", State: cloudState},
		}
	}
	check := func(when, cloudState string) {
		t.Helper()
		code, body := getCapabilities(t, e, adminToken)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", when, code, body)
		}
		if got := capabilityRows(t, body); !reflect.DeepEqual(got, want(cloudState)) {
			t.Fatalf("%s: the route listed %+v, want %+v", when, got, want(cloudState))
		}
	}
	check("before the first Start", "starting")
	if code, _ := getCapabilities(t, e, "test-host-token"); code != http.StatusUnauthorized {
		t.Fatalf("a host token read the capabilities: %d", code)
	}

	e.srv.StartExtensions(t.Context())
	clock.next(t, ext.StartWait) // the first try begins
	backoff := clock.next(t, extStartFirst)
	// The first try has returned, failed, and the next waits on its backoff.
	if n := cloud.starts.Load(); n != 1 {
		t.Fatalf("cloud started %d times before its backoff, want 1", n)
	}
	check("after a Start that failed", "starting")

	backoff.fire <- time.Now()
	clock.next(t, ext.StartWait) // the second try begins
	waitState(t, e.srv, "cloud", extStarted)
	check("once a Start succeeded", "ready")
}

// The fullest registry the registry allows (a provider in each of the six
// one-value slots and MaxPerList in each list, every name 40 bytes, every
// one starting) answers within capabilitiesReplyMax.
func TestCapabilitiesReplyBounded(t *testing.T) {
	name := func(prefix string, i int) string {
		n := fmt.Sprintf("%s-%d-", prefix, i)
		return n + strings.Repeat("x", 40-len(n))
	}
	var all []ext.Entry
	for _, k := range []string{ext.KindIdentity, ext.KindDecisions, ext.KindSync, ext.KindEntitlements, ext.KindMedia, ext.KindRuntime} {
		all = append(all, ext.Entry{Kind: k, Name: name(k, 0)})
	}
	for _, k := range []string{ext.KindContext, ext.KindNotifications} {
		for i := range ext.MaxPerList {
			all = append(all, ext.Entry{Kind: k, Name: name(k, i)})
		}
	}
	for _, en := range all {
		if !ext.ValidName(en.Name) {
			t.Fatalf("the test's name %q is not one the registry takes", en.Name)
		}
	}
	srv := newExtServer(t, nil)
	srv.exts.all = all
	// Every one starting: the longer of the two states.
	for _, en := range all {
		srv.extStarts = append(srv.extStarts, &extStarter{name: en.Name})
	}
	r := srv.capabilities()
	if len(r.Providers) != len(all) {
		t.Fatalf("%d rows, want %d", len(r.Providers), len(all))
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > capabilitiesReplyMax {
		t.Fatalf("the fullest reply is %d bytes, more than %d", len(b), capabilitiesReplyMax)
	}
}
