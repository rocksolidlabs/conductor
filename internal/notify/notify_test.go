package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMapClaudeHook(t *testing.T) {
	cases := []struct {
		in    string
		state string
		msg   string
		ok    bool
	}{
		{`{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Allow Bash?"}`, "needs_input", "Allow Bash?", true},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, "done", "idle prompt", true},
		{`{"hook_event_name":"Notification","notification_type":"auth_success"}`, "working", "auth success", true},
		{`{"hook_event_name":"Stop","last_assistant_message":"All done."}`, "done", "All done.", true},
		{`{"hook_event_name":"UserPromptSubmit"}`, "working", "", true},
		{`{"hook_event_name":"SessionEnd"}`, "", "", false},
		{`not json`, "", "", false},
	}
	for i, c := range cases {
		req, ok := MapClaudeHook([]byte(c.in))
		if ok != c.ok || req.State != c.state || req.Message != c.msg {
			t.Fatalf("case %d: got %+v %v", i, req, ok)
		}
	}
}

func TestMapCodex(t *testing.T) {
	req, ok := MapCodex([]byte(`{"type":"agent-turn-complete","last-assistant-message":"Need a decision"}`))
	if !ok || req.State != "done" || req.Message != "Need a decision" {
		t.Fatalf("%+v %v", req, ok)
	}
	if _, ok := MapCodex([]byte(`{"type":"other"}`)); ok {
		t.Fatal("unknown type must not map")
	}
}

func TestFromEnvAndSend(t *testing.T) {
	if _, _, err := FromEnv(func(string) string { return "" }); !errors.Is(err, ErrNotInSession) {
		t.Fatalf("expected ErrNotInSession, got %v", err)
	}
	var got Request
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL+"/api/x", "tok", Request{State: "needs_input", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || got.State != "needs_input" || got.Message != "m" {
		t.Fatalf("server saw %q %+v", auth, got)
	}
	if err := Send(context.Background(), srv.URL+"/redirect", "tok", Request{State: "done"}); err == nil {
		t.Fatal("redirect must be an error")
	}
}

// A Claude Code permission request is reported with no choices (#288): its
// dialog's choices differ by tool, mode and release (2.1.304's Bash dialog
// has four, its 3 switching to auto mode, where the fixed list's "No,
// explain…" was 3), and the session reads them off the dialog itself.
func TestMapClaudeHookPermissionCarriesNoChoices(t *testing.T) {
	req, ok := MapClaudeHook([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf x"}}`))
	if !ok || req.State != "needs_input" || req.Kind != "permission" || req.Message != "Allow Bash?" || req.Options != nil {
		t.Fatalf("%+v %v", req, ok)
	}
}

func TestMapClaudeHookPermissionPromptNotification(t *testing.T) {
	req, ok := MapClaudeHook([]byte(`{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`))
	if !ok || req.Kind != "permission" || req.Options != nil || req.Message != "Claude needs your permission to use Bash" {
		t.Fatalf("%+v", req)
	}
}

// The plan's approval and AskUserQuestion come as permission requests too
// (seen with Claude Code 2.1.304): their words say what they ask, the
// plan's approval and the question, never "Allow ExitPlanMode?".
func TestMapClaudeHookPlanAndQuestionSayWhatTheyAsk(t *testing.T) {
	for _, c := range []struct{ payload, want, plan string }{
		// The plan's approval names the plan's file (#289), which the question carries.
		{`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"Create hello.txt containing hi.\n","planFilePath":"/home/dev/.claude/plans/plan-creating-a-file.md"}}`, "Claude Code has a plan for you to approve", "/home/dev/.claude/plans/plan-creating-a-file.md"},
		{`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"Create hello.txt containing hi.\n"}}`, "Claude Code has a plan for you to approve", ""},
		{`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Do you prefer red or blue?","header":"Color","options":[{"label":"Red","description":"Prefer red"},{"label":"Blue","description":"Prefer blue"}],"multiSelect":false}]}}`, "Do you prefer red or blue?", ""},
		{`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{}}`, "Claude Code asks you a question", ""},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls","planFilePath":"/home/dev/.claude/plans/x.md"}}`, "Allow Bash?", ""},
	} {
		req, ok := MapClaudeHook([]byte(c.payload))
		if !ok || req.State != "needs_input" || req.Kind != "permission" || req.Message != c.want || req.Options != nil || req.Plan != c.plan {
			t.Fatalf("%s: %+v %v", c.payload, req, ok)
		}
	}
	// A path past the bound names no file a session keeps.
	long := `{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"planFilePath":"/` + strings.Repeat("p", 1100) + `.md"}}`
	if req, ok := MapClaudeHook([]byte(long)); !ok || req.Plan != "" {
		t.Fatalf("a long plan path: %+v", req)
	}
}

func TestMapClaudeHookUnknownNotificationIsPlainPrompt(t *testing.T) {
	req, ok := MapClaudeHook([]byte(`{"hook_event_name":"Notification","notification_type":"something_new","message":"hi"}`))
	if !ok || req.Kind != "prompt" || len(req.Options) != 0 || req.Message != "hi" {
		t.Fatalf("%+v", req)
	}
}

func TestMapClaudeHookPermissionWithoutToolName(t *testing.T) {
	req, _ := MapClaudeHook([]byte(`{"hook_event_name":"PermissionRequest"}`))
	if req.Message != "Allow this action?" || req.Options != nil {
		t.Fatalf("%+v", req)
	}
}

func TestMapClaudeHookStopIsDoneKind(t *testing.T) {
	req, _ := MapClaudeHook([]byte(`{"hook_event_name":"Stop","last_assistant_message":"All done."}`))
	if req.Kind != "done" {
		t.Fatalf("%+v", req)
	}
}

// The end of a Codex turn is done, as Claude Code's Stop is: the agent is
// idle, and a crew's handoffs and broadcasts are typed into it.
func TestMapCodexIsDoneKind(t *testing.T) {
	req, _ := MapCodex([]byte(`{"type":"agent-turn-complete","last-assistant-message":"Need a decision"}`))
	if req.State != "done" || req.Kind != "done" {
		t.Fatalf("%+v", req)
	}
	// The hidden thread that titles the conversation (live, Codex 0.159) is not a turn of the user's.
	if _, ok := MapCodex([]byte(`{"type":"agent-turn-complete","input-messages":["Generate a concise, single-line task title for this conversation"],"last-assistant-message":"Reply READY"}`)); ok {
		t.Fatal("the title thread's turn mapped")
	}
}

func TestRequestJSONCarriesKindAndOptions(t *testing.T) {
	b, _ := json.Marshal(Request{State: "needs_input", Kind: "permission", Options: []Option{{Label: "Yes", Input: "1"}}})
	if !strings.Contains(string(b), `"kind":"permission"`) || !strings.Contains(string(b), `"input":"1"`) {
		t.Fatalf("json %s", b)
	}
}

func TestRequestEventTargetsEventsRoute(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { path = r.URL.Path; w.WriteHeader(202) }))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL+"/api/sessions/s1/attention", "tok", Request{Event: "progress", Message: "1/7"}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/sessions/s1/events" {
		t.Fatalf("path %q", path)
	}
}

// sendTo posts req to srv under path and returns the path, the credential and
// the decoded JSON body the server saw.
func sendTo(t *testing.T, path string, req Request) (string, string, map[string]any) {
	t.Helper()
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL+path, "tok", req); err != nil {
		t.Fatal(err)
	}
	return gotPath, gotAuth, body
}

// The events route rejects unknown fields, so an event must travel as the
// route's own body: {"type", "message", "url", "to", "tool", …}, not as the
// attention request with an extra "event" key.
func TestRequestEventBodyIsAnEventsRequest(t *testing.T) {
	path, auth, body := sendTo(t, "/api/sessions/s1/attention", Request{
		Event: "artifact", Message: "PR opened", URL: "https://github.com/x/y/pull/1", To: "review", Tool: "gh",
	})
	if path != "/api/sessions/s1/events" || auth != "Bearer tok" {
		t.Fatalf("server saw %q %q", path, auth)
	}
	want := map[string]any{"type": "artifact", "message": "PR opened", "url": "https://github.com/x/y/pull/1", "to": "review", "tool": "gh"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body %v, want %v", body, want)
	}
}

func TestRequestEventBodyKeepsKindAndOptions(t *testing.T) {
	_, _, body := sendTo(t, "/api/sessions/s1/attention", Request{Event: "needs_input", Message: "Allow?", Kind: "permission", Options: []Option{{Label: "Yes", Input: "1"}, {Label: "Yes, and always allow", Input: "2"}, {Label: "No", Input: "3"}}})
	if body["type"] != "needs_input" || body["kind"] != "permission" || len(body["options"].([]any)) != 3 {
		t.Fatalf("body %v", body)
	}
	for _, key := range []string{"state", "event"} {
		if _, ok := body[key]; ok {
			t.Fatalf("body carries %q: %v", key, body)
		}
	}
}

// Without Event the request goes to /attention exactly as before, and the
// body keeps the attention shape.
func TestRequestWithoutEventKeepsTheAttentionRoute(t *testing.T) {
	path, _, body := sendTo(t, "/api/sessions/s1/attention", Request{State: "needs_input", Message: "m"})
	if path != "/api/sessions/s1/attention" {
		t.Fatalf("path %q", path)
	}
	want := map[string]any{"state": "needs_input", "message": "m"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body %v, want %v", body, want)
	}
}

// Only a URL that ends in /attention is rewritten; anything else is where the
// caller pointed it.
func TestRequestEventLeavesOtherURLsAlone(t *testing.T) {
	for _, in := range []string{"/api/x", "/api/sessions/s1/attention/more", "/attentions"} {
		if path, _, _ := sendTo(t, in, Request{Event: "progress"}); path != in {
			t.Fatalf("%s was sent to %s", in, path)
		}
	}
}

// An attention word the server refuses with 429 (the session's bucket is
// empty) is tried again after a short wait, a few times, within the 5 s Send
// has. An event is not: a hook must not hold up its agent for one.
func TestSendRetriesARateLimitedAttentionWord(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
	var calls atomic.Int32
	limited := func(first int32) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= first {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	srv := limited(2)
	for _, req := range []Request{{State: "needs_input"}, {Event: "done"}} {
		calls.Store(0)
		if err := Send(t.Context(), srv.URL+"/api/sessions/s/attention", "tok", req); err != nil || calls.Load() != 3 {
			t.Fatalf("%+v: %v after %d calls", req, err, calls.Load())
		}
	}
	calls.Store(0)
	if err := Send(t.Context(), srv.URL+"/api/sessions/s/attention", "tok", Request{Event: "tool_use", Tool: "Bash"}); err == nil || !strings.Contains(err.Error(), "429") || calls.Load() != 1 {
		t.Fatalf("event: %v after %d calls", err, calls.Load())
	}
	// A server that never lets up: the word is given up after the last wait.
	always := limited(1 << 30)
	calls.Store(0)
	if err := Send(t.Context(), always.URL+"/api/sessions/s/attention", "tok", Request{State: "working"}); err == nil || calls.Load() != int32(len(retryDelays)+1) {
		t.Fatalf("always limited: %v after %d calls", err, calls.Load())
	}
}

// The waits fit the budget, and a context that ends during a wait ends them:
// the 429 is the answer. The context ends inside the wait (the retryWait
// seam), never on a wall-clock deadline that a slow first POST could miss.
func TestSendRetriesWithinItsBudget(t *testing.T) {
	var total time.Duration
	for _, d := range retryDelays {
		total += d
	}
	if total >= sendTimeout {
		t.Fatalf("the waits add up to %v, not within %v", total, sendTimeout)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var waits []time.Duration
	old := retryWait
	retryWait = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		cancel()
		return make(chan time.Time) // never: only the context ends this wait
	}
	t.Cleanup(func() { retryWait = old })
	if err := Send(ctx, srv.URL+"/a/attention", "tok", Request{State: "working"}); err == nil || !strings.Contains(err.Error(), "429") || calls.Load() != 1 || len(waits) != 1 || waits[0] != retryDelays[0] {
		t.Fatalf("%v after %d calls, waits %v", err, calls.Load(), waits)
	}
}

// A server that predates the agent-session fields refuses them as unknown:
// the state goes again without them, once.
// A server older than ask refuses it as an unknown field: the question is
// reported again without it, and shows there as any.
func TestSendDropsTheMarkForAnOlderServer(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["ask"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"json: unknown field \"ask\""}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL+"/api/sessions/s1/attention", "tok", Request{State: "needs_input", Message: "Which database?", Ask: true}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["ask"] != true || bodies[1]["state"] != "needs_input" || bodies[1]["message"] != "Which database?" || bodies[1]["ask"] != nil {
		t.Fatalf("bodies %v", bodies)
	}
}

func TestSendDropsTheAgentSessionForAnOlderServer(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["agentSession"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"json: unknown field \"agentSession\""}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL+"/api/sessions/s1/attention", "tok", Request{State: "done", AgentSession: "s1", Turn: true}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[1]["state"] != "done" || bodies[1]["agentSession"] != nil || bodies[1]["turn"] != nil {
		t.Fatalf("bodies %v", bodies)
	}
}
