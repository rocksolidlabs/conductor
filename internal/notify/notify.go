// Package notify implements `conductor notify`: a tiny client agents (or
// their hooks) run inside a Conductor session to report whether they are
// waiting for a human. It reads the session's URL and token from the
// environment that Conductor injects into the PTY.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Environment variables injected into every Conductor session.
const (
	EnvSessionID = "CONDUCTOR_SESSION_ID"
	EnvURL       = "CONDUCTOR_NOTIFY_URL"
	EnvToken     = "CONDUCTOR_NOTIFY_TOKEN"
)

// Option is one quick-reply choice; Input is what the browser types for it.
type Option struct {
	Label string `json:"label"`
	Input string `json:"input"`
}

// Request is the update to send: an attention state, or an event when Event
// is set. Kind and Options let the workbench offer one-click answers (see
// docs/protocol.md, Attention).
// FileRef is one file a tool call touched, as a hook payload named it.
type FileRef struct {
	Op   string
	Path string
}

type Request struct {
	State   string   `json:"state"`
	Message string   `json:"message,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Options []Option `json:"options,omitempty"`
	// Event reports something the agent did instead of a state: one of the
	// six event types of docs/protocol.md (Events), or an attention word
	// (needs_input, working, done, clear). URL, To and Tool go with the events
	// that use them. Send ignores State for an event.
	Event string `json:"event,omitempty"`
	URL   string `json:"url,omitempty"`
	To    string `json:"to,omitempty"`
	Tool  string `json:"tool,omitempty"`
	// Op and Path go with the `file` event: what the agent did (read, edit,
	// write, delete) and to which file. Files are the file events a hook
	// payload yields beside its own event (a tool call that touched files):
	// Send reports each after the request itself.
	Op    string    `json:"op,omitempty"`
	Path  string    `json:"path,omitempty"`
	Files []FileRef `json:"-"`
	// AgentSession is the agent's own session id, as its hook payload names
	// it (Claude Code's session_id, Codex's thread-id…), and Turn says the
	// payload reports a turn: a prompt taken or finished. They go with an
	// attention state only; the server keeps the id for Resume.
	AgentSession string `json:"agentSession,omitempty"`
	Turn         bool   `json:"turn,omitempty"`
	// Ask, with the state needs_input, says the question is the agent's
	// own (conductor notify --ask, the MCP tool ask): the session keeps it
	// through the agent's turn's done, until someone answers. Never a
	// hook's: a program's wait reported as needs_input (a permission
	// prompt) gives way to done as before.
	Ask bool `json:"ask,omitempty"`
	// Plan, with the state needs_input, is the path of the plan the
	// question asks approval for (Claude Code's ExitPlanMode hook names it
	// as planFilePath, #289): the session opens it in the editor from the
	// question. Goes with an attention state only.
	Plan string `json:"plan,omitempty"`
}

// eventBody is what the events route reads: the same report under its own
// names. The route refuses fields it does not know, so an event cannot travel
// as a Request.
type eventBody struct {
	Type    string   `json:"type"`
	Message string   `json:"message,omitempty"`
	URL     string   `json:"url,omitempty"`
	To      string   `json:"to,omitempty"`
	Tool    string   `json:"tool,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Options []Option `json:"options,omitempty"`
	Op      string   `json:"op,omitempty"`
	Path    string   `json:"path,omitempty"`
}

// ErrNotInSession is returned when the environment is not set. Callers treat
// it as a silent no-op so hooks are safe outside Conductor.
var ErrNotInSession = errors.New("notify: not running inside a conductor session")

// FromEnv builds the target from the environment.
func FromEnv(getenv func(string) string) (url, token string, err error) {
	url, token = getenv(EnvURL), getenv(EnvToken)
	if url == "" || token == "" {
		return "", "", ErrNotInSession
	}
	return url, token, nil
}

// eventsURL turns a session's attention URL, which is the one Conductor puts
// in the environment, into the URL of its events route. Any other URL is
// where the caller pointed it, and stays.
func eventsURL(u string) string {
	if base, ok := strings.CutSuffix(u, "/attention"); ok {
		return base + "/events"
	}
	return u
}

// sendTimeout bounds a Send, its retries included: a hook waits at most this.
const sendTimeout = 5 * time.Second

// retryDelays are the waits between the attempts of an attention word the
// server answers 429: the session's bucket refills at 20 tokens a second, so a
// short wait usually finds one. They add up to well under sendTimeout, which
// still ends them.
var retryDelays = []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

// retryWait starts one of retryDelays; tests replace it to end the context
// while the wait is on.
var retryWait = time.After

// attentionWord reports whether r reports an attention state, by the
// attention route or as an attention word of the events route: what a 429
// retries.
func (r Request) attentionWord() bool {
	switch r.Event {
	case "", "needs_input", "working", "done", "clear":
		return true
	}
	return false
}

// Send posts the request to url with the agent token. A request with Event
// set goes to the session's events route (url with /attention replaced by
// /events) as an event; any other goes to url as an attention update.
// Redirects are refused so a token never follows a rewrite. An attention word
// answered 429 is tried again after each of retryDelays, while the 5 s budget
// lasts; anything else is tried once.
func Send(ctx context.Context, url, token string, req Request) error {
	events := eventsURL(url)
	// A request of files alone (a file tool's hook with --files) has no event of its own.
	if req.Event != "" || req.State != "" || len(req.Files) == 0 {
		var payload any = req
		if req.Event != "" {
			url = events
			payload = eventBody{Type: req.Event, Message: req.Message, URL: req.URL, To: req.To, Tool: req.Tool, Kind: req.Kind, Options: req.Options, Op: req.Op, Path: req.Path}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err := send(ctx, url, token, req, body); err != nil {
			return err
		}
	}
	// The files a tool call touched (design 4e), each its own event after the call's; a file event's refusal (an older server) ends the rest.
	for _, f := range req.Files {
		fb, err := json.Marshal(eventBody{Type: "file", Op: f.Op, Path: f.Path, Tool: req.Tool})
		if err != nil {
			return err
		}
		if err := send(ctx, events, token, Request{Event: "file"}, fb); err != nil {
			return err
		}
	}
	return nil
}

func send(ctx context.Context, url, token string, req Request, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		status, msg, err := post(ctx, url, token, body)
		if err != nil {
			return err
		}
		if status < 300 {
			return nil
		}
		if status == http.StatusBadRequest && strings.Contains(msg, "unknown field") && (req.AgentSession != "" || req.Turn || req.Ask || req.Plan != "") && req.Event == "" {
			// A server older than the agent-session fields, than ask or
			// than plan: the state alone (an older server's question shows
			// as any).
			req.AgentSession, req.Turn, req.Ask, req.Plan = "", false, false, ""
			if body, err = json.Marshal(req); err != nil {
				return err
			}
			continue
		}
		failed := fmt.Errorf("notify: server returned %d: %s", status, msg)
		if status != http.StatusTooManyRequests || !req.attentionWord() || attempt >= len(retryDelays) {
			return failed
		}
		select {
		case <-ctx.Done():
			return failed
		case <-retryWait(retryDelays[attempt]):
		}
	}
}

// post makes one attempt: the status and, for a failure, the start of the
// reply.
func post(ctx context.Context, url, token string, body []byte) (int, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode, strings.TrimSpace(string(msg)), nil
}

// ReadAllBounded reads at most 1 MiB from r.
func ReadAllBounded(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 1<<20))
}

// Getenv is os.Getenv, exposed for tests.
var Getenv = os.Getenv
