package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A host is handed, with registered, the recipe of the agent it names
// (#293): what the catalog's entry for it says to watch on its screen, as
// a server session of it is started with; none for an agent the catalog
// does not have, none named, or one with nothing to watch.
func TestRegisteredCarriesTheAgentsRecipe(t *testing.T) {
	asker := catalog.Agent{ID: "asker", Name: "asker", Command: []string{"/bin/sh"},
		Signal:       &catalog.Signal{Kind: catalog.SignalBell, WorkingTitle: "^Working$"},
		TrustPrompt:  `Trust\s*this\s*folder\?`,
		TrustAnswers: []catalog.Answer{{Label: "Yes", Input: "\r"}, {Label: "No", Input: "\x1b[B\r"}},
		Questions:    []catalog.Question{{Prompt: `proceed\s*\(y\)`, Anytime: true, Answers: []catalog.Answer{{Label: "Yes, proceed", Input: "y"}}}}}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{asker})
	register := func(agentID string) map[string]any {
		_, reg := dialFakeHostWith(t, e, proto.HostInfo{Name: "laptop"}, proto.HostSession{Name: "hosted", AgentID: agentID, Command: []string{"x"}, Cwd: e.root, Cols: 80, Rows: 24}, "Bearer test-host-token")
		return reg
	}
	reg := register("asker")
	raw, _ := json.Marshal(reg["recipe"])
	var got proto.HostRecipe
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("recipe %s: %v", raw, err)
	}
	if !reflect.DeepEqual(got, *agents.Recipe(asker)) {
		t.Fatalf("recipe %+v, want %+v", got, *agents.Recipe(asker))
	}
	if got.TrustPrompt != asker.TrustPrompt || len(got.TrustAnswers) != 2 || got.WorkingTitle != "^Working$" || len(got.Questions) != 1 || !got.Questions[0].Anytime {
		t.Fatalf("recipe %+v", got)
	}
	for _, id := range []string{"", "not-in-the-catalog", "cat"} {
		if r, ok := register(id)["recipe"]; ok {
			t.Fatalf("agent %q: recipe %v", id, r)
		}
	}
}

// registered never passes a host message for its recipe (#293, review
// round 1): a recipe just under proto.MaxHostRecipe beside the most ICE
// servers a provider answers, four URLs of ext.MaxICEURL bytes each and a
// username and credential of ext.MaxICESecret bytes whose characters JSON
// escapes six bytes wide, is past proto.MaxHostMessage with the recipe and
// within it without; the recipe is left out, and said so. A registered
// that fits keeps it.
func TestRegisteredDropsTheRecipeThatWouldPassAHostMessage(t *testing.T) {
	// The largest recipe within its bounds that a server sends: every pattern at its bound, then answers (labels of
	// proto.MaxRecipeAnswerLabel two-byte characters, keys of escape bytes JSON writes six wide) until the next would pass
	// proto.MaxHostRecipe.
	pattern := func(c string) string { return strings.Repeat(c, proto.MaxRecipePattern) }
	recipe := &proto.HostRecipe{TrustPrompt: pattern("t"), WorkingTitle: pattern("w")}
	for range proto.MaxRecipeQuestions {
		recipe.Questions = append(recipe.Questions, proto.RecipeQuestion{Prompt: pattern("p"), Words: pattern("w"), ModeAnswers: pattern("m"), TypedAnswers: pattern("y")})
	}
	answer := proto.RecipeAnswer{Label: strings.Repeat("é", proto.MaxRecipeAnswerLabel), Input: strings.Repeat("\x1b", proto.MaxRecipeAnswerInput)}
fill:
	for i := range recipe.Questions {
		for range proto.MaxRecipeAnswers {
			q := &recipe.Questions[i]
			q.Answers = append(q.Answers, answer)
			if len(encoded(t, recipe)) > proto.MaxHostRecipe {
				q.Answers = q.Answers[:len(q.Answers)-1]
				break fill
			}
		}
	}
	if err := recipe.Check(); err != nil {
		t.Fatal(err)
	}
	if n := len(encoded(t, recipe)); n < proto.MaxHostRecipe-1024 {
		t.Fatalf("the recipe is %d bytes, not near %d", n, proto.MaxHostRecipe)
	}
	url := func(i int) string {
		tail := fmt.Sprintf("relay%d.example.com:3478?transport=udp", i)
		host := ""
		for len("turn:")+len(host)+61+len(tail) <= ext.MaxICEURL {
			host += strings.Repeat("a", 60) + "."
		}
		host += strings.Repeat("a", ext.MaxICEURL-len("turn:")-len(host)-len(tail)-1) + "."
		return "turn:" + host + tail
	}
	ice := make([]proto.ICEServer, ext.MaxICEServers)
	for i := range ice {
		s := ext.ICEServer{URLs: []string{url(4 * i), url(4*i + 1), url(4*i + 2), url(4*i + 3)},
			Username: strings.Repeat("<", ext.MaxICESecret), Credential: strings.Repeat(">", ext.MaxICESecret)}
		if err := s.Valid(); err != nil || len(s.URLs[0]) != ext.MaxICEURL {
			t.Fatalf("ice server %d (a url of %d bytes): %v", i, len(s.URLs[0]), err)
		}
		ice[i] = proto.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential}
	}
	m := proto.Registered{T: proto.HostRegistered, SessionID: strings.Repeat("s", 16), Secret: strings.Repeat("x", 64), ShareBaseURL: "https://" + strings.Repeat("h", 253),
		Resumed: true, ICEServers: ice, Links: []string{}, Recipe: recipe}
	if n := len(encoded(t, recipe)); n > proto.MaxHostRecipe {
		t.Fatalf("the recipe is %d bytes, past %d", n, proto.MaxHostRecipe)
	}
	if n := len(encoded(t, m)); n <= proto.MaxHostMessage {
		t.Fatalf("registered with the recipe is %d bytes, within %d: the case does not test the bound", n, proto.MaxHostMessage)
	}
	b, dropped := registeredMessage(m)
	if !dropped || len(b) > proto.MaxHostMessage || strings.Contains(string(b), `"recipe"`) {
		t.Fatalf("registered is %d bytes (bound %d), dropped %v, recipe sent %v", len(b), proto.MaxHostMessage, dropped, strings.Contains(string(b), `"recipe"`))
	}

	m.ICEServers = ice[:1]
	b, dropped = registeredMessage(m)
	if dropped || !strings.Contains(string(b), `"recipe":{`) || len(b) > proto.MaxHostMessage {
		t.Fatalf("a registered that fits: %d bytes, dropped %v", len(b), dropped)
	}
}

// registeredRecipe registers a host saying host of itself, with a session
// of agentID, and answers the recipe of the registered it is sent, its
// bytes as they came (nil for none).
func registeredRecipe(t *testing.T, e *testEnv, host proto.HostInfo, agentID string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, strings.Replace(e.http.URL, "http://", "ws://", 1)+"/ws/host", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-host-token"}}})
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	c.SetReadLimit(proto.MaxHostMessage)
	reg := proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: host, Session: proto.HostSession{Name: "hosted", AgentID: agentID, Command: []string{"x"}, Cwd: e.root, Cols: 80, Rows: 24}}
	if err := c.Write(ctx, websocket.MessageText, encoded(t, reg)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		T      string          `json:"t"`
		Recipe json.RawMessage `json:"recipe"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.T != proto.HostRegistered {
		t.Fatalf("registered %s: %v", data, err)
	}
	return m.Recipe
}

// isForm is a recipe's question that is a form (agents.HostRecipe).
func isForm(q proto.RecipeQuestion) bool {
	return q.Anytime && q.AnswersFromScreen && len(q.Answers) > 0
}

// Codex's form asking to allow a tool of an MCP server (#716) is in the
// recipe registered carries only for a host whose register says it reads
// forms (proto.HostInfo.Forms, #970), and only from the owner's own
// server: such a host is sent the recipe a server session of the agent
// is started with (agents.Recipe), byte for byte. A host that does not say
// it (an earlier release's, or one that says answerAs alone) is sent the
// recipe without the form, byte for byte what it was sent before; so is a
// host that says it to a switchyard.
func TestRegisteredCarriesTheFormsOnlyToAHostThatReadsThem(t *testing.T) {
	codex, ok := catalog.Default().Get("codex")
	if !ok {
		t.Fatal("no codex in the default catalog")
	}
	whole, without := encoded(t, agents.Recipe(codex)), encoded(t, agents.HostRecipe(codex))
	var r proto.HostRecipe
	if err := json.Unmarshal(whole, &r); err != nil || !slices.ContainsFunc(r.Questions, isForm) {
		t.Fatalf("Codex's recipe has no form: %s", whole)
	}
	if err := json.Unmarshal(without, &r); err != nil || slices.ContainsFunc(r.Questions, isForm) {
		t.Fatalf("the recipe without forms has one: %s", without)
	}
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	own := newTestEnvAgents(t, nil, discard, []catalog.Agent{codex})
	yard := newTestEnvAgents(t, func(c *config.Config) { c.Switchyard.Enabled = true }, discard, []catalog.Agent{codex})
	for _, c := range []struct {
		name string
		e    *testEnv
		host proto.HostInfo
		want []byte
	}{
		{"a host that reads forms", own, proto.HostInfo{Name: "laptop", AnswerAs: true, Forms: true}, whole},
		{"a host that says answerAs alone", own, proto.HostInfo{Name: "laptop", AnswerAs: true}, without},
		{"an earlier release's host", own, proto.HostInfo{Name: "laptop"}, without},
		{"a host that reads forms, to a switchyard", yard, proto.HostInfo{Name: "laptop", Forms: true}, without},
	} {
		if got := registeredRecipe(t, c.e, c.host, "codex"); string(got) != string(c.want) {
			t.Errorf("%s was sent\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

func encoded(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
