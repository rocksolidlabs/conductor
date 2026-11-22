package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// dialHostUpgrade dials /ws/host presenting token ("" for none) and
// answers the upgrade's response; the connection, when one was made, is
// closed at the test's end.
func dialHostUpgrade(t *testing.T, e *testEnv, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	c, resp, err := websocket.Dial(ctx, strings.Replace(e.http.URL, "http://", "ws://", 1)+"/ws/host", &websocket.DialOptions{HTTPHeader: h})
	if c != nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

// An upgrade of /ws/host the owner's own server accepts for a host's
// credential carries the server's key of this start, 64 lowercase hex
// digits, the same on every upgrade of that start; a refused one carries
// none, a switchyard's none, and another start of the server another key.
func TestHostUpgradeCarriesServerKey(t *testing.T) {
	e := newTestEnv(t, nil)
	var keys []string
	for _, token := range []string{"test-host-token", "test-host-token", adminToken} {
		_, resp, err := dialHostUpgrade(t, e, token)
		if err != nil {
			t.Fatalf("dial with %q: %v", token, err)
		}
		k := resp.Header.Get(proto.ServerKeyHeader)
		if proto.ParseServerKey(k) == nil {
			t.Fatalf("the upgrade's key %q is not 64 lowercase hex digits", k)
		}
		keys = append(keys, k)
	}
	if keys[0] != keys[1] || keys[0] != keys[2] {
		t.Fatalf("one start gave the keys %q", keys)
	}
	for _, token := range []string{"wrong-token", ""} {
		_, resp, err := dialHostUpgrade(t, e, token)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a refused upgrade (%q): %v %v", token, resp, err)
		}
		if v := resp.Header.Values(proto.ServerKeyHeader); len(v) != 0 {
			t.Fatalf("a refused upgrade carries the key %q", v)
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	_, resp, err := dialHostUpgrade(t, yard, "test-host-token")
	if err != nil {
		t.Fatal(err)
	}
	if v := resp.Header.Values(proto.ServerKeyHeader); len(v) != 0 {
		t.Fatalf("a switchyard's upgrade carries the key %q", v)
	}
	again := newTestEnv(t, nil)
	_, resp, err = dialHostUpgrade(t, again, "test-host-token")
	if err != nil {
		t.Fatal(err)
	}
	if k := resp.Header.Get(proto.ServerKeyHeader); k == keys[0] || proto.ParseServerKey(k) == nil {
		t.Fatalf("another start's key %q (the first's %q)", k, keys[0])
	}
}

// registerHostAt registers a host saying h of itself at e's /ws/host with
// the host token, and answers its registered, or the error that closed it.
func registerHostAt(t *testing.T, e *testEnv, h proto.HostInfo) (proto.Registered, error) {
	t.Helper()
	c, _, err := dialHostUpgrade(t, e, "test-host-token")
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(proto.MaxHostMessage)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h.Name = "laptop"
	reg := proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: h, Session: proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"x"}, Cwd: e.root, Cols: 80, Rows: 24}}
	if err := c.Write(ctx, websocket.MessageText, encoded(t, reg)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		return proto.Registered{}, err
	}
	var m proto.Registered
	if err := json.Unmarshal(data, &m); err != nil || m.T != proto.HostRegistered {
		t.Fatalf("registered %s: %v", data, err)
	}
	return m, nil
}

// listedRaw is e's GET /api/sessions as its bytes, and as its sessions.
func listedRaw(t *testing.T, e *testEnv) ([]byte, []map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.http.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var list struct{ Sessions []map[string]any }
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		t.Fatalf("the listing: %d %s", resp.StatusCode, raw)
	}
	return raw, list.Sessions
}

// A host's register whose machine is out of its form is refused at the
// route (the connection closed, nothing listed). One whose platform is out
// of its form registers with no platform kept. One whose machine is in its
// form is listed under a hash of it, with its platform and its version,
// the key it sent appearing nowhere in the listing.
func TestHostRegisterMachineRefusedOutOfForm(t *testing.T) {
	e := newTestEnv(t, nil)
	sent := strings.Repeat("0a", 16)
	for _, h := range []proto.HostInfo{{Machine: sent + "0"}, {Machine: strings.ToUpper(sent)}, {Machine: sent + "0", Platform: "linux/amd64"}} {
		if _, err := registerHostAt(t, e, h); websocket.CloseStatus(err) != websocket.StatusCode(proto.CloseProtocolError) {
			t.Fatalf("%+v: %v", h, err)
		}
	}
	if _, list := listedRaw(t, e); len(list) != 0 {
		t.Fatalf("%d sessions listed after refusals", len(list))
	}
	for _, p := range []string{"linux", strings.Repeat("x", 33)} {
		m, err := registerHostAt(t, e, proto.HostInfo{Machine: sent, Platform: p})
		if err != nil {
			t.Fatalf("platform %q refused the register: %v", p, err)
		}
		_, list := listedRaw(t, e)
		for _, got := range list {
			if got["id"] == m.SessionID && (got["hostPlatform"] != nil || got["hostKey"] != machine.Group(sent)) {
				t.Fatalf("platform %q: %v", p, got)
			}
		}
	}
	m, err := registerHostAt(t, e, proto.HostInfo{Machine: sent, Platform: "linux/arm64", Version: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	raw, list := listedRaw(t, e)
	if bytes.Contains(raw, []byte(sent)) {
		t.Fatalf("the listing holds the key the host sent: %s", raw)
	}
	found := false
	for _, got := range list {
		if got["id"] != m.SessionID {
			continue
		}
		found = true
		if got["hostKey"] != machine.Group(sent) || got["hostKeyFrom"] != "machine" || got["hostPlatform"] != "linux/arm64" || got["hostVersion"] != "v9.9.9" || got["hostLink"] != "up" {
			t.Fatalf("the hosted session: %v", got)
		}
	}
	if !found {
		t.Fatalf("the session is not listed: %s", raw)
	}
}

// A switchyard keeps none of what a host says of its machine, though the
// host sent all three in their forms: its listing has no hostPlatform, no
// hostVersion and no key made of the machine key.
func TestSwitchyardListsNothingOfAMachine(t *testing.T) {
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	sent := strings.Repeat("0b", 16)
	if _, err := registerHostAt(t, yard, proto.HostInfo{Machine: sent, Platform: "linux/amd64+wsl", Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	raw, list := listedRaw(t, yard)
	if len(list) != 1 {
		t.Fatalf("the switchyard lists %s", raw)
	}
	for _, k := range []string{"hostPlatform", "hostVersion", sent, machine.Group(sent), "v1.2.3", "linux/amd64"} {
		if bytes.Contains(raw, []byte(k)) {
			t.Fatalf("the switchyard's listing holds %s: %s", k, raw)
		}
	}
	if list[0]["hostKeyFrom"] == "machine" {
		t.Fatalf("the switchyard keyed a machine: %v", list[0])
	}
}

// A server session rooted at a home whose config directory holds this
// machine's id: a view link's file read of the id is refused, as is one
// of its directory, while a file beside the directory under the home is
// served.
func TestSharedSessionCannotReadMachineID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the user's config directory is not the home's there")
	}
	e := newTestEnv(t, nil)
	t.Setenv("HOME", e.root)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir, err := machine.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, e.root+string(filepath.Separator)) {
		t.Fatalf("the machine's directory %s is not under the test home %s", dir, e.root)
	}
	id, err := machine.ID(dir)
	if err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(dir), "notes.txt")
	if err := os.WriteFile(beside, []byte("beside"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid := e.createSession("cat")
	_, out := e.do("POST", "/api/sessions/"+sid+"/links", adminToken, map[string]any{"role": "view", "label": "pair"})
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("no link: %v", out)
	}
	idRel, _ := filepath.Rel(e.root, filepath.Join(dir, machine.FileName))
	dirRel, _ := filepath.Rel(e.root, dir)
	besideRel, _ := filepath.Rel(e.root, beside)
	for _, who := range []string{token, adminToken} {
		for _, p := range []string{idRel, dirRel} {
			status, body, code := e.getFile(sid, who, filepath.ToSlash(p), "raw")
			if status == http.StatusOK && code == "" || strings.Contains(body, id) {
				t.Fatalf("%s read as %q: %d %q %q", p, who, status, body, code)
			}
		}
		if status, body, _ := e.getFile(sid, who, filepath.ToSlash(besideRel), "raw"); status != http.StatusOK || body != "beside" {
			t.Fatalf("the file beside: %d %q", status, body)
		}
	}
}

// A data directory that is the machine's directory sends nothing of the
// id to the sync store: not in the start's replay, not in a delivery made
// after it.
func TestSyncNeverCarriesMachineID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	id, err := machine.ID(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, dir, f)
	e.srv.StartExtensions(t.Context())
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	// A delivery after the replay: the catalog saved again.
	before := 0
	for _, c := range f.all() {
		if c.d.Path == catalogFile {
			before++
		}
	}
	if err := e.srv.store.Save(catalogFile, map[string]any{"hidden": []string{"exit"}}); err != nil {
		t.Fatal(err)
	}
	calls := f.until(t, "the catalog's delivery", func(c []syncCall) bool {
		n := 0
		for _, x := range c {
			if x.d.Path == catalogFile {
				n++
			}
		}
		return n > before
	})
	raw, _ := hex.DecodeString(id)
	for _, c := range calls {
		if strings.Contains(strings.ToLower(c.d.Path), machine.FileName) || bytes.Contains(c.d.Data, []byte(id)) || bytes.Contains(c.d.Data, raw) {
			t.Fatalf("a call carries the machine's id: %s", describeCalls([]syncCall{c}))
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, machine.FileName)); err != nil || string(b) != id+"\n" {
		t.Fatalf("the id's file: %q %v", b, err)
	}
}
