package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/paste"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// recordingMedia is a media provider that records every call it is given
// and answers what answer does (no server when answer is nil).
type recordingMedia struct {
	name   string
	answer func(context.Context, ext.ICERequest) ([]ext.ICEServer, error)

	mu    sync.Mutex
	asked []ext.ICERequest
	rooms int
}

func (m *recordingMedia) Name() string { return m.name }
func (m *recordingMedia) ICEServers(ctx context.Context, r ext.ICERequest) ([]ext.ICEServer, error) {
	m.mu.Lock()
	m.asked = append(m.asked, r)
	m.mu.Unlock()
	if m.answer == nil {
		return nil, nil
	}
	return m.answer(ctx, r)
}
func (m *recordingMedia) Room(context.Context, ext.RoomRequest) (ext.RoomJoin, bool, error) {
	m.mu.Lock()
	m.rooms++
	m.mu.Unlock()
	return ext.RoomJoin{URL: "https://room.example.test/r"}, true, nil
}

// calls answers the ICE requests m was given so far, and how many rooms.
func (m *recordingMedia) calls() ([]ext.ICERequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ext.ICERequest(nil), m.asked...), m.rooms
}

// answering is a recording media provider that adds servers to every
// connection.
func answering(servers []ext.ICEServer) *recordingMedia {
	return &recordingMedia{name: "turn", answer: func(context.Context, ext.ICERequest) ([]ext.ICEServer, error) {
		return servers, nil
	}}
}

// mediaEnv is a test server configured with servers, the free build when m
// is nil and m registered as the media provider otherwise (a second server
// over the first's store, made as `conductor serve` makes it with a
// registry that holds m), logging to log (nil: nowhere), with a paste
// answerer that hands every ICE list it is given to pastes.
func mediaEnv(t *testing.T, servers []config.ICEServer, m ext.MediaRoomProvider, log *syncLog, pastes chan<- []proto.ICEServer) *testEnv {
	t.Helper()
	mutate := func(c *config.Config) { c.ICEServers = servers }
	var e *testEnv
	if log == nil {
		e = newTestEnv(t, mutate)
	} else {
		e = newTestEnvLogging(t, mutate, logTo(log))
	}
	if m != nil {
		p := local.Fill(ext.Providers{Media: m, All: []ext.Entry{{Kind: ext.KindMedia, Name: m.Name(), Value: m}}})
		srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store, WithExtensions(p))
		if err != nil {
			t.Fatal(err)
		}
		if srv.home != "" {
			srv.home = t.TempDir()
		}
		e = e.serve(srv)
	}
	e.srv.SetPasteAnswerer(func(ctx context.Context, l *session.Local, offer string, role session.Role, label string, ice []proto.ICEServer) (PastePeer, string, error) {
		if pastes != nil {
			pastes <- ice
		}
		return &fakePastePeer{}, "v=0\r\ns=answer\r\n", nil
	})
	return e
}

// iceSites is what the four ICE sites hand out on e: a hosted viewer's
// welcome (its iceServers and its viewer id), the host's registered, the
// paste answerer's servers, and GET /api/ice's body, as the wire carries
// each.
type iceSites struct {
	welcome    []proto.ICEServer
	viewerID   string
	registered []proto.ICEServer
	paste      []proto.ICEServer
	offer      []byte
	host       *fakeHost
	session    string // the server session the paste was for
}

// visitICESites drives each ICE site of e once: a host registers and a
// viewer joins its session by a control link, a paste invite is answered
// for a server session, and GET /api/ice is asked without a token.
func visitICESites(t *testing.T, e *testEnv, pastes <-chan []proto.ICEServer) iceSites {
	t.Helper()
	var got iceSites
	host, reg := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop"}, proto.HostSession{Name: "hosted", AgentID: "cat", Command: []string{"cat"}, Cwd: e.root, Cols: 80, Rows: 24}, "Bearer test-host-token")
	got.host = host
	got.registered = iceOf(t, reg)
	v := dialViewer(t, e, host.sessionID, host.link("control", ""))
	v.hello(80, 24)
	w := v.expectControl(proto.CtlWelcome)
	got.welcome = iceOf(t, w)
	got.viewerID, _ = w["viewerId"].(string)

	got.session = e.createSession("cat")
	offer, _ := paste.EncodeBlob("offer", "v=0\r\ns=viewer offer\r\n")
	if resp, out := e.do("POST", "/api/sessions/"+got.session+"/paste", adminToken, map[string]any{"offer": offer, "role": "control"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("paste: %d %v", resp.StatusCode, out)
	}
	got.paste = <-pastes

	resp, err := e.client.Get(e.http.URL + "/api/ice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got.offer, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ice: %d %s", resp.StatusCode, got.offer)
	}
	return got
}

// iceOf reads a message's iceServers as the wire carries them: every
// field a proto.ICEServer has, and no other.
func iceOf(t *testing.T, m map[string]any) []proto.ICEServer {
	t.Helper()
	raw, ok := m["iceServers"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var out []proto.ICEServer
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("iceServers %s: %v", b, err)
	}
	return out
}

// mainICE is what main handed a viewer, a host and the paste answerer:
// every configured server as configured, in order.
func mainICE(servers []config.ICEServer) []proto.ICEServer {
	ice := make([]proto.ICEServer, 0, len(servers))
	for _, srv := range servers {
		ice = append(ice, proto.ICEServer{URLs: srv.URLs, Username: srv.Username, Credential: srv.Credential})
	}
	return ice
}

// mainOffer is GET /api/ice's body on main, byte for byte: the configured
// servers' stun: and stuns: URLs, as writeJSON encodes them.
func mainOffer(servers []config.ICEServer) []byte {
	urls := []string{}
	for _, srv := range servers {
		for _, u := range srv.URLs {
			if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
				urls = append(urls, u)
			}
		}
	}
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(map[string]any{"stun": urls})
	return b.Bytes()
}

// protoICE is a provider's servers as the wire carries them.
func protoICE(servers []ext.ICEServer) []proto.ICEServer {
	var out []proto.ICEServer
	for _, s := range servers {
		out = append(out, proto.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return out
}

// configuredServers is n configured servers: STUN ones, and every third a
// TURN one with its user name and credential, as a config file holds them.
func configuredServers(n int) []config.ICEServer {
	out := make([]config.ICEServer, 0, n)
	for i := range n {
		if i%3 == 2 {
			out = append(out, config.ICEServer{URLs: []string{fmt.Sprintf("turn:turn%d.example.test:3478?transport=udp", i), fmt.Sprintf("turns:turn%d.example.test:5349", i)}, Username: fmt.Sprintf("user%d", i), Credential: fmt.Sprintf("secret%d", i)})
			continue
		}
		out = append(out, config.ICEServer{URLs: []string{fmt.Sprintf("stun:stun%d.example.test:3478", i)}})
	}
	return out
}

// mintedServers is n servers a provider adds: minted TURN credentials.
func mintedServers(n int) []ext.ICEServer {
	out := make([]ext.ICEServer, 0, n)
	for i := range n {
		out = append(out, ext.ICEServer{URLs: []string{fmt.Sprintf("turn:minted%d.example.test:3478", i)}, Username: fmt.Sprintf("minted-user-%d", i), Credential: fmt.Sprintf("minted-secret-%d", i)})
	}
	return out
}

// The welcome, registered, the paste answerer's servers and GET /api/ice
// are main's for STUN only, for STUN with a TURN server and its
// credential, and for nine and twenty configured servers, every one kept:
// with no provider exactly, and with one adding eight, main's followed by
// the provider's eight, GET /api/ice unchanged.
func TestICEStaticUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		servers []config.ICEServer
	}{
		{"stun", []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}},
		{"stun and turn", []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478", "stuns:stun.example.test:5349"}}, {URLs: []string{"turn:turn.example.test:3478?transport=udp", "turns:turn.example.test:5349"}, Username: "configured-user", Credential: "configured-secret"}}},
		{"nine", configuredServers(9)},
		{"twenty", configuredServers(20)},
	} {
		for _, added := range []int{0, ext.MaxICEServers} {
			t.Run(fmt.Sprintf("%s, a provider adding %d", tc.name, added), func(t *testing.T) {
				var m ext.MediaRoomProvider
				if added > 0 {
					m = answering(mintedServers(added))
				}
				pastes := make(chan []proto.ICEServer, 1)
				e := mediaEnv(t, tc.servers, m, nil, pastes)
				got := visitICESites(t, e, pastes)
				want := append(mainICE(tc.servers), protoICE(mintedServers(added))...)
				for site, ice := range map[string][]proto.ICEServer{"welcome": got.welcome, "registered": got.registered, "paste": got.paste} {
					if !reflect.DeepEqual(ice, want) {
						t.Errorf("%s: %+v\nwant %+v", site, ice, want)
					}
				}
				if want := mainOffer(tc.servers); !bytes.Equal(got.offer, want) {
					t.Errorf("GET /api/ice: %s\nwant %s", got.offer, want)
				}
			})
		}
	}
}

// GET /api/ice never asks the provider, however often it is called and
// whatever the provider would add; the sites that may ask it do.
func TestICEOfferNeverAsksTheProvider(t *testing.T) {
	servers := []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}
	m := answering(mintedServers(2))
	e := mediaEnv(t, servers, m, nil, nil)
	for range 5 {
		resp, out := e.do("GET", "/api/ice", "", nil)
		if b, _ := json.Marshal(out["stun"]); resp.StatusCode != http.StatusOK || string(b) != `["stun:stun.example.test:3478"]` {
			t.Fatalf("GET /api/ice: %d %v", resp.StatusCode, out)
		}
	}
	if asked, rooms := m.calls(); len(asked) != 0 || rooms != 0 {
		t.Fatalf("GET /api/ice asked the provider: %+v, rooms %d", asked, rooms)
	}
	// The recorder records: a host's registration asks it once.
	dialFakeHost(t, e, "hosted-agent-token")
	if asked, _ := m.calls(); len(asked) != 1 || asked[0].Purpose != icePurposeHost {
		t.Fatalf("a registration's asks: %+v", asked)
	}
	// Called directly, the offer asks nothing either.
	if got := e.srv.iceServersFor(t.Context(), ext.ICERequest{Purpose: icePurposeOffer}); !reflect.DeepEqual(got, []proto.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}) {
		t.Fatalf("the offer's servers: %+v", got)
	}
	if asked, _ := m.calls(); len(asked) != 1 {
		t.Fatalf("the offer asked the provider: %+v", asked)
	}
}

// A provider's servers reach a hosted session's viewer, its host and a
// paste invite's answer, after the configured ones, and the provider is
// told which connection each is for: the viewer's session, role and id;
// the host's session; the paste's session and role.
func TestMediaServersReachViewersAndHosts(t *testing.T) {
	servers := []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}
	minted := mintedServers(2)
	m := answering(minted)
	pastes := make(chan []proto.ICEServer, 1)
	e := mediaEnv(t, servers, m, nil, pastes)
	got := visitICESites(t, e, pastes)
	want := append(mainICE(servers), protoICE(minted)...)
	for site, ice := range map[string][]proto.ICEServer{"welcome": got.welcome, "registered": got.registered, "paste": got.paste} {
		if !reflect.DeepEqual(ice, want) {
			t.Errorf("%s: %+v\nwant %+v", site, ice, want)
		}
	}
	asked, rooms := m.calls()
	wantAsked := []ext.ICERequest{
		{Purpose: icePurposeHost, Session: got.host.sessionID},
		{Purpose: icePurposeViewer, Session: got.host.sessionID, Role: "control", Viewer: got.viewerID},
		{Purpose: icePurposePaste, Session: got.session, Role: "control"},
	}
	if got.viewerID == "" || !reflect.DeepEqual(asked, wantAsked) {
		t.Fatalf("the provider was asked %+v\nwant %+v", asked, wantAsked)
	}
	if rooms != 0 {
		t.Fatalf("a room was asked for %d times; nothing here asks", rooms)
	}
}

// No TURN URL, user name or credential, the configured ones' or a
// provider's, reaches GET /api/ice, which anyone may call, and none
// reaches the server's log from any site.
func TestICENeverLeaksCredentials(t *testing.T) {
	servers := []config.ICEServer{
		{URLs: []string{"stun:stun.example.test:3478", "turn:configured.example.test:3478"}, Username: "configured-user", Credential: "configured-secret"},
		{URLs: []string{"turns:configured.example.test:5349"}, Username: "configured-user", Credential: "configured-secret"},
	}
	minted := []ext.ICEServer{
		{URLs: []string{"stun:minted.example.test:3478", "turn:minted.example.test:3478"}, Username: "minted-user", Credential: "minted-secret"},
	}
	var logs syncLog
	pastes := make(chan []proto.ICEServer, 1)
	e := mediaEnv(t, servers, answering(minted), &logs, pastes)
	got := visitICESites(t, e, pastes)
	if string(got.offer) != `{"stun":["stun:stun.example.test:3478"]}`+"\n" {
		t.Fatalf("GET /api/ice: %s", got.offer)
	}
	for _, secret := range []string{"turn", "configured-user", "configured-secret", "minted", "username", "credential"} {
		if bytes.Contains(got.offer, []byte(secret)) {
			t.Errorf("GET /api/ice carries %q: %s", secret, got.offer)
		}
	}
	// The sites that are authenticated do carry them, as main's do.
	if len(got.welcome) != 3 || got.welcome[2].Credential != "minted-secret" || got.welcome[0].Credential != "configured-secret" {
		t.Fatalf("welcome: %+v", got.welcome)
	}
	for _, secret := range []string{"configured-secret", "minted-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log carries %q", secret)
		}
	}
}

// A provider that is late, fails, panics or answers a server ext refuses
// adds none, the configured servers kept; one that answers more than
// ext.MaxICEServers has its first eight used; a provider's servers in a
// welcome stop where the frame would pass proto.MaxControl, the
// configured ones never cut.
func TestMediaBounds(t *testing.T) {
	configured := []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}
	viewer := ext.ICERequest{Purpose: icePurposeViewer, Session: "s1", Role: "control", Viewer: "v1"}
	server := func(answer func(context.Context) ([]ext.ICEServer, error)) *Server {
		m := &recordingMedia{name: "turn", answer: func(ctx context.Context, _ ext.ICERequest) ([]ext.ICEServer, error) { return answer(ctx) }}
		p := local.Fill(ext.Providers{Media: m, All: []ext.Entry{{Kind: ext.KindMedia, Name: m.Name(), Value: m}}})
		srv := newExtServer(t, nil, WithExtensions(p))
		srv.cfg.ICEServers = configured
		return srv
	}
	tooLong := strings.Repeat("a", ext.MaxICESecret+1)
	for _, tc := range []struct {
		name   string
		answer func(context.Context) ([]ext.ICEServer, error)
	}{
		// Late: it answers only once its call's context has ended, at
		// ext.ICEWait, so it is late whatever the machine's load.
		{"late", func(ctx context.Context) ([]ext.ICEServer, error) {
			<-ctx.Done()
			return mintedServers(1), nil
		}},
		{"failing", func(context.Context) ([]ext.ICEServer, error) {
			return mintedServers(1), errors.New("the service is down")
		}},
		{"panicking", func(context.Context) ([]ext.ICEServer, error) { panic("the provider broke") }},
		{"a url no browser takes", func(context.Context) ([]ext.ICEServer, error) {
			return append(mintedServers(1), ext.ICEServer{URLs: []string{"http://minted.example.test"}}), nil
		}},
		{"turn without a credential", func(context.Context) ([]ext.ICEServer, error) {
			return []ext.ICEServer{{URLs: []string{"turn:minted.example.test:3478"}, Username: "u"}}, nil
		}},
		{"a credential past the bound", func(context.Context) ([]ext.ICEServer, error) {
			return []ext.ICEServer{{URLs: []string{"turn:minted.example.test:3478"}, Username: "u", Credential: tooLong}}, nil
		}},
		{"too many urls", func(context.Context) ([]ext.ICEServer, error) {
			return []ext.ICEServer{{URLs: []string{"stun:a.test", "stun:b.test", "stun:c.test", "stun:d.test", "stun:e.test"}}}, nil
		}},
		{"no url", func(context.Context) ([]ext.ICEServer, error) { return []ext.ICEServer{{}}, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := server(tc.answer)
			if got := srv.iceServersFor(t.Context(), viewer); !reflect.DeepEqual(got, mainICE(configured)) {
				t.Fatalf("got %+v, want the configured servers alone", got)
			}
		})
	}

	t.Run("past eight, cut to eight", func(t *testing.T) {
		srv := server(func(context.Context) ([]ext.ICEServer, error) {
			// The ninth is one ext refuses: it is never read.
			return append(mintedServers(ext.MaxICEServers), ext.ICEServer{URLs: []string{"http://minted.example.test"}}), nil
		})
		want := append(mainICE(configured), protoICE(mintedServers(ext.MaxICEServers))...)
		if got := srv.iceServersFor(t.Context(), viewer); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})

	// Eight servers at ext's bounds are more than one CONTROL frame holds:
	// the welcome keeps the configured servers and as many of the
	// provider's as fit, in order.
	big := make([]ext.ICEServer, ext.MaxICEServers)
	for i := range big {
		var urls []string
		for j := range ext.MaxICEURLs {
			u := fmt.Sprintf("turn:m%d-%d.", i, j)
			urls = append(urls, u+strings.Repeat("a", ext.MaxICEURL-len(u)-len(".test"))+".test")
		}
		big[i] = ext.ICEServer{URLs: urls, Username: strings.Repeat("u", ext.MaxICESecret), Credential: strings.Repeat("c", ext.MaxICESecret)}
	}
	t.Run("a welcome within its frame", func(t *testing.T) {
		pastes := make(chan []proto.ICEServer, 1)
		e := mediaEnv(t, configured, answering(big), nil, pastes)
		got := visitICESites(t, e, pastes)
		if len(got.registered) != 1+ext.MaxICEServers || len(got.paste) != 1+ext.MaxICEServers {
			t.Fatalf("registered %d and paste %d servers: the host's and the paste's are not cut", len(got.registered), len(got.paste))
		}
		n := len(got.welcome) - 1
		if n < 1 || n >= ext.MaxICEServers || !reflect.DeepEqual(got.welcome, got.registered[:1+n]) {
			t.Fatalf("the welcome holds %d of the provider's servers, want the first that fit, at least one and fewer than %d", n, ext.MaxICEServers)
		}
		// fitWelcome keeps exactly what fits: one more is past the frame.
		w := proto.Welcome{T: proto.CtlWelcome, ICEServers: got.registered}
		fitted := fitWelcome(w, 1)
		if size := len(proto.MustControl(fitted)) - 1; size > proto.MaxControl {
			t.Fatalf("a fitted welcome is %d bytes, past %d", size, proto.MaxControl)
		}
		w.ICEServers = got.registered[:len(fitted.ICEServers)+1]
		if size := len(proto.MustControl(w)) - 1; size <= proto.MaxControl {
			t.Fatalf("one more server, %d bytes, would have fitted", size)
		}
	})
	t.Run("the configured servers never cut", func(t *testing.T) {
		var many []proto.ICEServer
		for _, s := range big {
			many = append(many, proto.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
		}
		w := proto.Welcome{T: proto.CtlWelcome, ICEServers: append(append([]proto.ICEServer{}, many...), many...)}
		if got := fitWelcome(w, len(many)); len(got.ICEServers) != len(many) {
			t.Fatalf("kept %d servers, want the %d configured", len(got.ICEServers), len(many))
		}
		if got := fitWelcome(proto.Welcome{T: proto.CtlWelcome, ICEServers: many[:2]}, 0); len(got.ICEServers) != 2 {
			t.Fatalf("a welcome that fits lost servers: %d", len(got.ICEServers))
		}
	})
}

// The free build's media slot has no room, and nothing here asks a
// provider for one: the four ICE sites ask for servers alone.
func TestRoomDefault(t *testing.T) {
	srv := newExtServer(t, nil)
	if j, ok, err := srv.exts.media.Room(t.Context(), ext.RoomRequest{Kind: "run", ID: "r1", By: ada}); ok || err != nil || j != (ext.RoomJoin{}) {
		t.Fatalf("the default's room: %+v %v %v", j, ok, err)
	}
	m := answering(mintedServers(1))
	pastes := make(chan []proto.ICEServer, 1)
	e := mediaEnv(t, []config.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}, m, nil, pastes)
	visitICESites(t, e, pastes)
	if asked, rooms := m.calls(); len(asked) != 3 || rooms != 0 {
		t.Fatalf("asked %d times for servers and %d for a room; want 3 and none", len(asked), rooms)
	}
}
