package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/notify"
)

// conductor crew: what an agent does with its own session from inside it
// (docs/protocol.md, the self-service routes): form a crew around the
// session, add a member to its run, read the run, mint a view link. The
// session's URL and token come from the environment Conductor injects, as
// conductor notify's do; outside a session the commands exit 0 silently
// (--quiet=false says why), so a skill that runs them elsewhere does no harm.
const crewUsage = `Usage:
  conductor crew create <crew.json|-> [--self NAME] [--open]   form a crew around this session and launch it
  conductor crew add <member.json|->                          add a member to this session's run
  conductor crew status                                        this session's run and its members
  conductor crew link [--ttl 2h] [--label TEXT]                a view-only link to this session (a day at most)

crew.json: {"name", "goal", "members": [{"name", "agentId", "prompt", "start": {"when": "immediately|after|manual", "member"?}}], "self"?, "isolation"?}
member.json: one member of the list above. "-" reads stdin.
`

// maxCrewFile bounds what create and add read.
const maxCrewFile = 2 << 20

// crewClient talks to the session's own routes; prefix names the command
// in its errors (crew, or task).
type crewClient struct {
	base, token, prefix string
	client              *http.Client
}

// crewClientFromEnv derives the session's routes from the notify URL
// (…/api/sessions/<id>/attention) and takes the token.
func crewClientFromEnv(getenv func(string) string) (*crewClient, error) {
	url, token, err := notify.FromEnv(getenv)
	if err != nil {
		return nil, err
	}
	base, ok := strings.CutSuffix(url, "/attention")
	if !ok {
		return nil, fmt.Errorf("crew: %s does not name a session's attention route", notify.EnvURL)
	}
	return &crewClient{base: base, token: token, prefix: "crew", client: &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect refused: a token never follows a rewrite")
		},
	}}, nil
}

// maxSessionReply bounds what the session client reads of a reply, in
// whole MiB: the largest any session route makes is its run's ticket
// (GET /api/sessions/{id}/ticket), under api.MaxTicketReply, which this
// holds (TestSessionClientHoldsTheLargestTicket).
const maxSessionReply = 4 << 20

// call sends body (nil for none) and decodes the reply; an error reply is an error with the server's words.
func (c *crewClient) call(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var out map[string]any
	if err := c.into(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// into is call decoding a reply of at most maxSessionReply bytes into
// out. The body is encoded with HTML escaping off (encodeJSON), so a
// comment of 16 KiB of '<' fits its route's 64 KiB. A reply that never
// came, the request refused, cut or left unanswered, is a lostReply,
// which a write with an operation id asks for once more (askTwice).
func (c *crewClient) into(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := encodeJSON(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return lostReply{err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionReply+1))
	if err != nil {
		return lostReply{err}
	}
	if len(raw) > maxSessionReply {
		return fmt.Errorf("%s: the server's reply is larger than %d MiB", c.prefix, maxSessionReply>>20)
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return fmt.Errorf("%s: the server answered %d with no JSON", c.prefix, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error map[string]any `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return fmt.Errorf("%s: %v (%v)", c.prefix, e.Error["message"], e.Error["code"])
		}
		return fmt.Errorf("%s: the server answered %d", c.prefix, resp.StatusCode)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: an unreadable reply: %w", c.prefix, err)
		}
	}
	return nil
}

// readJSONArg reads the file named, or stdin for "-", as a JSON object.
func readJSONArg(name string, stdin io.Reader) (map[string]any, error) {
	var r io.Reader
	if name == "-" {
		r = stdin
	} else {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxCrewFile+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCrewFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, maxCrewFile)
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func runCrew(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, crewUsage)
		if len(args) == 0 {
			return 2, nil
		}
		return 0, nil
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("crew "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	self := fs.String("self", "", "create: the member this session becomes (else the file's \"self\")")
	open := fs.Bool("open", false, "create: ask the workbench to offer the run (a toast with Open)")
	ttl := fs.Duration("ttl", 0, "link: how long it lasts (2h unless given, 24h at most)")
	label := fs.String("label", "", "link: its label")
	quiet := fs.Bool("quiet", true, "exit 0 silently when not running inside a conductor session")
	fs.Usage = func() {
		fmt.Fprint(stderr, crewUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	// The file comes first for create and add; flags may follow it.
	var file string
	if (sub == "create" || sub == "add") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") || (len(rest) > 0 && rest[0] == "-") {
		file, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if file == "" && fs.NArg() > 0 {
		file = fs.Arg(0)
	}
	c, err := crewClientFromEnv(os.Getenv)
	if err != nil {
		if *quiet {
			return 0, nil
		}
		return 1, err
	}
	switch sub {
	case "create":
		if file == "" {
			return 2, errors.New("crew create needs a crew.json (or - for stdin)")
		}
		body, err := readJSONArg(file, stdin)
		if err != nil {
			return 2, err
		}
		if *self != "" {
			body["self"] = *self
		}
		if s, _ := body["self"].(string); s == "" {
			return 2, errors.New("crew create: say which member this session becomes (--self NAME, or \"self\" in the file)")
		}
		if *open {
			body["open"] = true
		}
		out, err := c.call(ctx, http.MethodPost, "/crew", body)
		if err != nil {
			return 1, err
		}
		run, _ := out["run"].(map[string]any)
		fmt.Fprintf(stdout, "run %v\n%v\nmember %v\n", run["id"], out["url"], out["member"])
		printMembers(stdout, run)
		return 0, nil
	case "add":
		if file == "" {
			return 2, errors.New("crew add needs a member.json (or - for stdin)")
		}
		body, err := readJSONArg(file, stdin)
		if err != nil {
			return 2, err
		}
		out, err := c.call(ctx, http.MethodPost, "/run/members", body)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "added %v\n", body["name"])
		printMembers(stdout, out["run"].(map[string]any))
		return 0, nil
	case "status":
		out, err := c.call(ctx, http.MethodGet, "/run", nil)
		if err != nil {
			return 1, err
		}
		run, _ := out["run"].(map[string]any)
		fmt.Fprintf(stdout, "run %v (%v) %v\nyou are %v\n", run["id"], run["state"], run["name"], out["member"])
		printMembers(stdout, run)
		return 0, nil
	case "link":
		body := map[string]any{}
		if *ttl > 0 {
			body["ttlSeconds"] = int64(ttl.Seconds())
		}
		if *label != "" {
			body["label"] = *label
		}
		out, err := c.call(ctx, http.MethodPost, "/links/agent", body)
		if err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, out["url"])
		return 0, nil
	}
	fmt.Fprint(stderr, crewUsage)
	return 2, fmt.Errorf("unknown crew command %q", sub)
}

// printMembers lists a run's members: name, status, agent.
func printMembers(w io.Writer, run map[string]any) {
	members, _ := run["members"].([]any)
	for _, m := range members {
		mm, _ := m.(map[string]any)
		status := fmt.Sprint(mm["status"])
		if ni, _ := mm["needsInput"].(bool); ni {
			status += " (needs input)"
		}
		fmt.Fprintf(w, "  %-20s %-22s %v\n", mm["name"], status, mm["agentId"])
	}
}
