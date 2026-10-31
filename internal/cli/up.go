package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

const (
	// defaultServer is where conductor serve listens unless told otherwise.
	defaultServer = "http://localhost:8080"
	// maxReply bounds what is read of a reply: a run is small.
	maxReply = 1 << 20
	// maxCrewsReply bounds a page of crews, which the server bounds by count,
	// not by bytes: a summary with a cwd of 4096 bytes and 12 members comes
	// to about 26 KB once escaped, so a page of crewsPage at most 2.6 MB.
	maxCrewsReply = 4 << 20
)

// requestTimeout bounds one request. conductor up waits for the launch reply,
// which comes once the sessions of the members that start at once exist. A
// variable so that a test need not wait it out.
var requestTimeout = 30 * time.Second

const crewsUsage = `Usage:
  conductor up <crew-id> [--server URL] [--token T] [--insecure] [--name N] [--open]
      launch a saved crew as a run; prints the run and the URL of its page,
      and the crew's view link when it has one; --name names the run
  conductor up --resume <run-id> [--server URL] [--token T] [--insecure] [--open]
      resume a stopped run as a new run (POST /api/runs/{run}/resume): every
      member with a conversation continues it in its kept worktree, the
      others start afresh; prints the new run and the URL of its page; the
      run's name is kept, so --name does not apply
  conductor crews [--server URL] [--token T] [--insecure] [--ids]
      list the saved crews: id, name and members; with --ids the ids only,
      one per line, for shell completion (nothing, and exit 0, when the
      server cannot be asked)

The server defaults to CONDUCTOR_SERVER, else http://localhost:8080; the
workbench token to CONDUCTOR_WORKBENCH_TOKEN. A plain http:// server on
another machine is refused unless --insecure says to go ahead: the token
would travel to it in the clear.
`

// errNoOpener is what openBrowser returns when the platform has no command to
// open a URL with.
var errNoOpener = errors.New("no opener found (xdg-open on Linux, open on macOS)")

// openBrowser opens target in the user's browser. A variable so that a test
// need not start one.
var openBrowser = openURL

// openURL runs the platform's opener on target and does not wait for it. The
// command is an argument vector; target is a URL that runUp built from a
// validated http or https server.
func openURL(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		// Not tried elsewhere: on Debian, open is another name for openvt.
		name = "open"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return errNoOpener
	}
	cmd := exec.Command(path, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runUp launches a saved crew through the server's API and prints the run and
// the URL of its page.
func runUp(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	api := addAPIFlags(fs)
	name := fs.String("name", "", "a name for this run; the pages otherwise say the crew and the start time")
	resume := fs.String("resume", "", "resume this stopped run as a new run instead of launching a crew; its name is kept")
	open := fs.Bool("open", false, "open the run's page in the browser (xdg-open, or open on macOS)")
	fs.Usage = func() {
		fmt.Fprint(stderr, crewsUsage)
		fs.PrintDefaults()
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	switch {
	case *resume != "" && len(rest) != 0:
		fs.Usage()
		return 2, errors.New("up: --resume names the run to resume; no crew with it")
	case *resume != "" && strings.TrimSpace(*name) != "":
		fs.Usage()
		return 2, errors.New("up: a resumed run keeps its name; --name does not apply with --resume")
	case *resume == "" && (len(rest) != 1 || rest[0] == ""):
		fs.Usage()
		return 2, errors.New("up: name one crew: conductor up <crew-id>")
	}
	c, err := api.client(stderr)
	if err != nil {
		return 2, err
	}
	// The reply is read for the run's ID and yolo alone: fields this version does not
	// know (a newer server adds them) are ignored on purpose, so no
	// DisallowUnknownFields here.
	var reply struct {
		Run struct {
			ID string `json:"id"`
			// Yolo: the run's members launch with their agents' yolo recipes.
			Yolo bool `json:"yolo"`
		} `json:"run"`
		// Only when the crew asks for a view link: its URL, the token in it.
		ViewLink *struct {
			URL string `json:"url"`
		} `json:"viewLink"`
	}
	var body any
	if n := strings.TrimSpace(*name); n != "" {
		body = map[string]string{"label": n}
	}
	path := "/api/runs/" + url.PathEscape(*resume) + "/resume"
	if *resume == "" {
		path = "/api/crews/" + url.PathEscape(rest[0]) + "/launch"
	}
	// A resume's reply names the new run as a launch's does.
	if err := c.doBody(ctx, http.MethodPost, path, body, &reply); err != nil {
		return 1, err
	}
	if reply.Run.ID == "" {
		return 1, fmt.Errorf("the reply from %s names no run", c.host)
	}
	page := c.base + "/runs/" + url.PathEscape(reply.Run.ID)
	fmt.Fprintf(stdout, "run %s\n%s\n", reply.Run.ID, page)
	if reply.ViewLink != nil {
		if printableURL(reply.ViewLink.URL) {
			fmt.Fprintf(stdout, "view %s\n", reply.ViewLink.URL)
		} else {
			fmt.Fprintln(stderr, "conductor up: the reply's view link is not an http(s) URL; not printed")
		}
	}
	if reply.Run.Yolo {
		fmt.Fprintln(stdout, "yolo: its members skip their permission prompts")
	}
	if *open {
		// The run is running whether or not a browser opens: a note, not a failure.
		if err := openBrowser(page); err != nil {
			fmt.Fprintf(stderr, "conductor up: --open: %v; the URL is above\n", err)
		}
	}
	return 0, nil
}

// printableURL reports whether a URL from a reply may be printed: http or
// https with a host, and nothing a terminal would read as a control sequence.
func printableURL(s string) bool {
	if strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// crewsPage is how many crews conductor crews asks for at a time.
const crewsPage = 100

// idsTimeout bounds all of conductor crews --ids: a completion that waits is
// worse than one that offers nothing. A test lowers it.
var idsTimeout = 2 * time.Second

// runCrews lists the saved crews, one per line, or with --ids their ids.
func runCrews(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("crews", flag.ContinueOnError)
	fs.SetOutput(stderr)
	api := addAPIFlags(fs)
	ids := fs.Bool("ids", false, "print the crew ids only, one per line, for shell completion: silent and exit 0 when the server cannot be reached or the token is missing")
	fs.Usage = func() {
		fmt.Fprint(stderr, crewsUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2, errors.New("crews: takes no arguments")
	}
	if *ids {
		return runCrewIDs(ctx, api, stdout, stderr)
	}
	c, err := api.client(stderr)
	if err != nil {
		return 2, err
	}
	all, err := c.crewList(ctx)
	if err != nil {
		return 1, err
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no crews")
		return 0, nil
	}
	for _, cr := range all {
		names := make([]string, len(cr.Members))
		for i, m := range cr.Members {
			names[i] = m.Name
		}
		fmt.Fprintf(stdout, "%s\t%s\t%d members: %s\n", cr.ID, cr.Name, len(names), strings.Join(names, ", "))
	}
	return 0, nil
}

// runCrewIDs prints the saved crews' ids, one per line, for shell
// completion: nothing and exit 0 when no client can be made (no token, or a
// plain server on another machine without --insecure) or the server does not
// answer within idsTimeout, and never an error, since the output lands on a
// command line (a warning --insecure prints goes to stderr). An id not shaped
// like a crew's (crew.ValidID) is left out.
func runCrewIDs(ctx context.Context, api apiFlags, stdout, stderr io.Writer) (int, error) {
	c, err := api.client(stderr)
	if err != nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, idsTimeout)
	defer cancel()
	all, err := c.crewList(ctx)
	if err != nil {
		return 0, nil
	}
	for _, cr := range all {
		if crew.ValidID(cr.ID) {
			fmt.Fprintln(stdout, cr.ID)
		}
	}
	return 0, nil
}

// crewLine is a crew as conductor crews reads it from the list.
type crewLine struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Members []struct {
		Name string `json:"name"`
	} `json:"members"`
}

// crewList reads every saved crew from GET /api/crews, crewsPage at a time,
// each page at most maxCrewsReply bytes. As in runUp, unknown fields are
// ignored on purpose. A reply without total (an older server) is one page.
func (c *apiClient) crewList(ctx context.Context) ([]crewLine, error) {
	var all []crewLine
	// Pages by offset and limit, with no snapshot across them: a crew made
	// or deleted between two pages shifts the ones after it, so one may be
	// listed twice or not at all. That is inherent to offset paging; a run
	// again lists them as they are.
	for offset := 0; ; {
		var reply struct {
			Crews []crewLine `json:"crews"`
			Total int        `json:"total"`
		}
		if err := c.doLimit(ctx, http.MethodGet, fmt.Sprintf("/api/crews?offset=%d&limit=%d", offset, crewsPage), nil, &reply, maxCrewsReply); err != nil {
			return nil, err
		}
		all = append(all, reply.Crews...)
		offset += len(reply.Crews)
		if len(reply.Crews) == 0 || offset >= reply.Total {
			return all, nil
		}
	}
}

// apiFlags are the flags both commands take.
type apiFlags struct {
	server, token *string
	insecure      *bool
}

func addAPIFlags(fs *flag.FlagSet) apiFlags {
	return apiFlags{
		server: fs.String("server", "", "conductor server URL (env CONDUCTOR_SERVER, default "+defaultServer+")"),
		// No default is shown or read here: -h would print the token.
		token:    fs.String("token", "", "workbench token (env CONDUCTOR_WORKBENCH_TOKEN)"),
		insecure: fs.Bool("insecure", false, "allow a plain http:// server that is not localhost or a loopback address (127.0.0.0/8, ::1), which is refused without it: the workbench token sent with every request, and the replies, then travel in the clear and unverified"),
	}
}

// plainAdmin is what the admin client would send and read in the clear from
// a plain server on another machine.
const plainAdmin = "what this client sends and reads, the workbench token on every request among it,"

// client checks the flags and the environment. Its errors are usage errors,
// and never repeat what was given: the server URL may carry a password, and
// the token is a secret. A server that is plain http on another machine is
// refused without --insecure, before any request, and with it the warning
// goes to stderr.
func (f apiFlags) client(stderr io.Writer) (*apiClient, error) {
	server := strings.TrimRight(cmp.Or(*f.server, os.Getenv("CONDUCTOR_SERVER"), defaultServer), "/")
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(server, "?#") {
		return nil, errors.New("the server must be an http or https URL with a host, and no query or fragment (--server or CONDUCTOR_SERVER)")
	}
	token := cmp.Or(*f.token, os.Getenv("CONDUCTOR_WORKBENCH_TOKEN"), os.Getenv("CONDUCTOR_ADMIN_TOKEN"))
	if token == "" {
		return nil, errors.New("a workbench token is required (--token or CONDUCTOR_WORKBENCH_TOKEN)")
	}
	if config.PlainOffMachine(server) {
		if !*f.insecure {
			return nil, plainServerRefusal(server, plainAdmin)
		}
		fmt.Fprintln(stderr, "conductor: "+plainServerWarning(server, plainAdmin))
	}
	// Userinfo in the URL is never used (the token travels in a header) and
	// must not be printed or handed to a browser with the run URL.
	u.User = nil
	return &apiClient{base: strings.TrimRight(u.String(), "/"), host: u.Host, token: token}, nil
}

// apiClient talks to the admin API of one server.
type apiClient struct {
	base  string // the server URL as given, without userinfo or a trailing slash
	host  string // the server's host, which is all an error says of the server
	token string // sent in the Authorization header and nowhere else
}

// doBody sends one request, with body as JSON (nil sends none), and decodes the 2xx reply into out. Any
// other status is an error carrying the API's code and message. An error names
// the server's host, never the path, the query or the token. A reply of more
// than maxReply bytes is an error.
func (c *apiClient) doBody(ctx context.Context, method, path string, body, out any) error {
	return c.doLimit(ctx, method, path, body, out, maxReply)
}

// doLimit is doBody with a reply of at most limit bytes, a whole number of MiB.
func (c *apiClient) doLimit(ctx context.Context, method, path string, body, out any, limit int) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		// HTML escaping off (encodeJSON): a Dispatch body's bounds are
		// measured with '<' one byte.
		b, err := encodeJSON(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("cannot build a request for %s", c.host)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	// A redirect is an answer, not a detour: the token goes to the server that
	// was named and to no other.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return c.transportError(err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return c.transportError(err)
	}
	if len(reply) > limit {
		return fmt.Errorf("the reply from %s is larger than %d MiB", c.host, limit>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp.Status, reply)
	}
	if err := json.Unmarshal(reply, out); err != nil {
		return fmt.Errorf("unreadable reply from %s: %w", c.host, err)
	}
	return nil
}

// statusError is the error for a non-2xx reply: "code: message" from the API's
// error envelope, else the status line.
func (c *apiClient) statusError(status string, body []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch {
		case e.Error.Code != "" && e.Error.Message != "":
			return fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
		case e.Error.Code != "" || e.Error.Message != "":
			return errors.New(e.Error.Code + e.Error.Message)
		}
	}
	return fmt.Errorf("%s answered %s", c.host, status)
}

// transportError describes a failure to get a reply. The net/http error
// carries the request URL, so only its cause is kept. A reply that never
// came is a lostReply, which a write with an operation id asks for once
// more (askTwice); an interruption is not.
func (c *apiClient) transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return lostReply{fmt.Errorf("no reply from %s within %s", c.host, requestTimeout)}
	case errors.Is(err, context.Canceled):
		return errors.New("interrupted")
	}
	return lostReply{fmt.Errorf("cannot reach %s: %w", c.host, err)}
}
