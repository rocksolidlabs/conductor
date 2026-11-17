package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// conductor trackers: the person's GitHub tokens the server keeps, by
// host (docs/plans/tracker-linking.md, "The token"), through the admin's
// routes (GET /api/trackers, POST /api/trackers/github and its remove).
// A token is read from standard input alone, never from a flag or an
// argument, which another user's ps can read: from a terminal with its
// echo off (term.ReadPassword), else one line from a pipe. set takes no
// --token, so its argv holds no token of any kind: the workbench token
// comes from CONDUCTOR_WORKBENCH_TOKEN. The token read is sent once, in
// the request's body, and nothing this command prints or returns holds
// it (withoutToken): a usage line, an error and a reply's words alike.

const trackersUsage = `Usage:
  conductor trackers [--server URL] [--token T] [--insecure]
      the GitHub tokens the server keeps or is given, one host a line: the
      host, the account, where it came from (settings or the server's
      environment), whether GitHub took it, its fingerprint (which tells
      this server's tokens apart until it restarts) and when it was saved
      and checked; never a token
  conductor trackers set [--host HOST] [--server URL] [--insecure]
      keep a GitHub token for HOST (github.com when not given) once GitHub
      has taken it: the token is read from standard input alone, typed
      unseen in a terminal or piped in as one line, never given as a flag or
      an argument; the workbench token comes from CONDUCTOR_WORKBENCH_TOKEN,
      so that no token is in the command line
  conductor trackers remove [--host HOST] [--server URL] [--token T] [--insecure]
      drop the token kept for HOST (github.com when not given): Conductor no
      longer uses it, and it stays valid on GitHub until it is revoked there

The server defaults to CONDUCTOR_SERVER, else http://localhost:8080; the
workbench token to CONDUCTOR_WORKBENCH_TOKEN.
`

// trackerSaveRequestTimeout bounds a save's request: the server's own
// bound on a save (the file's lock and the token's check with GitHub) and
// a request's time beside it.
var trackerSaveRequestTimeout = api.TrackerSaveTimeout + requestTimeout

// maxTrackerInput bounds what set reads of a pipe: a token and its line's
// end, with room to say "more than a token" rather than cut one.
const maxTrackerInput = 4 << 10

// trackerView is a token as GET /api/trackers shows it.
type trackerView struct {
	Host        string     `json:"host"`
	Login       string     `json:"login"`
	Fingerprint string     `json:"fingerprint"`
	Scopes      []string   `json:"scopes"`
	SavedAt     *time.Time `json:"savedAt"`
	Source      string     `json:"source"`
	State       string     `json:"state"`
	CheckedAt   *time.Time `json:"checkedAt"`
}

// runTrackers is conductor trackers. Exit 2 for a usage error, before
// any request; 1 when the server refuses or cannot be asked.
func runTrackers(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	verb, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, rest = args[0], args[1:]
	}
	switch verb {
	case "", "set", "remove":
	case "help":
		fmt.Fprint(stderr, trackersUsage)
		return 0, nil
	default:
		// Never the word itself: it may be a token given by mistake.
		fmt.Fprint(stderr, trackersUsage)
		return 2, errors.New("trackers takes set or remove, or nothing to list the tokens")
	}
	name := strings.TrimSpace("trackers " + verb)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var server apiFlags
	var host *string
	if verb == "set" {
		// No --token: the command line holds no token of any kind.
		server = apiFlags{
			server:   fs.String("server", "", "conductor server URL (env CONDUCTOR_SERVER, default "+defaultServer+")"),
			token:    new(string),
			insecure: fs.Bool("insecure", false, "allow a plain http:// server that is not localhost or a loopback address: the workbench token and the GitHub token then travel in the clear"),
		}
	} else {
		server = addAPIFlags(fs)
	}
	if verb != "" {
		host = fs.String("host", "", "the GitHub host: github.com, or a GitHub Enterprise Server's name (github.com when not given)")
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, trackersUsage, "\nFlags of "+name+":\n")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if len(pos) > 0 {
		// Never the argument itself: a token given by mistake is not
		// printed back.
		if verb == "set" {
			return 2, errors.New("trackers set takes no argument: the token is read from standard input alone, never from the command line")
		}
		return 2, fmt.Errorf("%s takes no argument", name)
	}
	if verb == "set" && os.Getenv("CONDUCTOR_WORKBENCH_TOKEN") == "" && os.Getenv("CONDUCTOR_ADMIN_TOKEN") == "" {
		return 2, errors.New("trackers set reads the workbench token from CONDUCTOR_WORKBENCH_TOKEN alone, so that no token is in the command line")
	}
	c, err := server.client(stderr)
	if err != nil {
		return 2, err
	}
	switch verb {
	case "set":
		tok, err := readTrackerToken(stdin, stderr)
		if err != nil {
			return 2, err
		}
		return trackerSet(ctx, c, *host, tok, stdout)
	case "remove":
		return trackerRemove(ctx, c, *host, stdout)
	}
	return trackersList(ctx, c, stdout)
}

// readTrackerToken reads the token from stdin: from a terminal with its
// echo off, the prompt on stderr; from anything else, one line, its
// line's end ("\n" or "\r\n") not the token's. Empty, more than one line
// or out of a token's form (config.TrackerTokenForm) is refused before
// anything is sent, in words that never hold what was read.
func readTrackerToken(stdin io.Reader, stderr io.Writer) (string, error) {
	var b []byte
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "GitHub token (not shown as you type): ")
		read, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", errors.New("could not read the token from the terminal")
		}
		b = read
	} else {
		read, err := io.ReadAll(io.LimitReader(stdin, maxTrackerInput+1))
		if err != nil {
			return "", errors.New("could not read the token from standard input")
		}
		if len(read) > maxTrackerInput {
			return "", errors.New("standard input holds more than a token: give the token alone, as one line")
		}
		b = bytes.TrimSuffix(bytes.TrimSuffix(read, []byte("\n")), []byte("\r"))
	}
	defer clear(b)
	switch {
	case len(b) == 0:
		return "", errors.New("no token on standard input: pipe it in as one line, or run this in a terminal to type it unseen")
	case bytes.ContainsAny(b, "\r\n"):
		return "", errors.New("standard input holds more than one line: give the token alone, as one line")
	case !config.TrackerTokenForm(string(b)):
		return "", fmt.Errorf("a token is 1 to %d bytes of visible ASCII, with no space", config.MaxTrackerToken)
	}
	return string(b), nil
}

// withoutToken is err with nothing of tok in its words: the server's
// answers carry fixed words, and this holds them to it whatever they say.
func withoutToken(err error, tok string) error {
	if err == nil || tok == "" || !strings.Contains(err.Error(), tok) {
		return err
	}
	return errors.New("the server's answer held the token, so it is not printed: conductor trackers says what is kept")
}

// trackerSet sends the token for host to be kept, waiting the save's
// bound, and prints what the server keeps: the account and the
// fingerprint, never the token.
func trackerSet(ctx context.Context, c *apiClient, host, tok string, stdout io.Writer) (int, error) {
	body := map[string]string{"token": tok}
	if host != "" {
		body["host"] = host
	}
	var out struct {
		Tracker   trackerView `json:"tracker"`
		Unchanged bool        `json:"unchanged"`
	}
	if err := c.doWithin(ctx, trackerSaveRequestTimeout, http.MethodPost, "/api/trackers/github", body, &out, maxReply); err != nil {
		return 1, withoutToken(err, tok)
	}
	line := trackerLine(out.Tracker)
	if strings.Contains(line, tok) {
		return 1, withoutToken(errors.New(tok), tok)
	}
	if out.Unchanged {
		fmt.Fprintf(stdout, "%s: the token is the one kept already; nothing changed\n%s\n", oneLine(out.Tracker.Host), line)
		return 0, nil
	}
	fmt.Fprintf(stdout, "%s: kept\n%s\n", oneLine(out.Tracker.Host), line)
	return 0, nil
}

// trackerRemove drops host's token and prints what the server said: for
// the environment's host, that its token is still the one used.
func trackerRemove(ctx context.Context, c *apiClient, host string, stdout io.Writer) (int, error) {
	host = cmpOrHost(host)
	var out struct {
		Removed bool   `json:"removed"`
		InUse   string `json:"inUse"`
		Message string `json:"message"`
	}
	if err := c.doLimit(ctx, http.MethodPost, "/api/trackers/github/remove", map[string]string{"host": host}, &out, maxReply); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "%s: removed; the token stays valid on GitHub until it is revoked there\n", oneLine(host))
	if out.InUse != "" {
		fmt.Fprintf(stdout, "%s\n", oneLine(out.Message))
	}
	return 0, nil
}

// cmpOrHost is host, github.com when empty.
func cmpOrHost(host string) string {
	if host == "" {
		return "github.com"
	}
	return host
}

// trackersList prints the tokens kept or given, one a line.
func trackersList(ctx context.Context, c *apiClient, stdout io.Writer) (int, error) {
	var out struct {
		GitHub []trackerView `json:"github"`
	}
	if err := c.doLimit(ctx, http.MethodGet, "/api/trackers", nil, &out, maxReply); err != nil {
		return 1, err
	}
	if len(out.GitHub) == 0 {
		fmt.Fprintln(stdout, "no GitHub token is kept: conductor trackers set reads one from standard input")
		return 0, nil
	}
	for _, v := range out.GitHub {
		fmt.Fprintln(stdout, trackerLine(v))
	}
	return 0, nil
}

// trackerLine is a token as a line shows it: its host, its account, its
// source, its state, its fingerprint, when it was saved and checked.
func trackerLine(v trackerView) string {
	account := "no account read yet"
	if v.Login != "" {
		account = "@" + v.Login
	}
	parts := []string{v.Host, account, v.Source, trackerStateWords(v.State), "fingerprint " + v.Fingerprint}
	if len(v.Scopes) > 0 {
		parts = append(parts, "scopes "+strings.Join(v.Scopes, ", "))
	}
	if v.SavedAt != nil {
		parts = append(parts, "saved "+when(*v.SavedAt))
	}
	if v.CheckedAt != nil {
		parts = append(parts, "checked "+when(*v.CheckedAt))
	}
	return oneLine(strings.Join(parts, "  "))
}

// trackerStateWords is a token's state in words.
func trackerStateWords(state string) string {
	switch state {
	case "ok":
		return "taken by GitHub"
	case "refused":
		return "refused by GitHub: save a new one"
	case "unchecked":
		return "not checked yet"
	case "unused":
		return "unused: the server's environment gives this host's token"
	}
	return state
}
