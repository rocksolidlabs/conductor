package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const openUsage = `Usage: conductor open <url>

Opens a link meant for the person (an artifact, a pull request, a preview, a
sign-in page) in the browser this session's agent's settings name, on the
computer the desktop app runs on; with no desktop app, the workbench offers it.
Prints what happened, in one line. Run it from inside a Conductor session; an
agent with a browser set gets it as $BROWSER too.
`

// runOpen is conductor open: the link goes to the session's open route,
// which the MCP tool open_url calls too (#436).
func runOpen(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) (int, error) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, openUsage)
		return 0, nil
	}
	if len(args) != 1 {
		fmt.Fprint(stderr, openUsage)
		return 2, errors.New("open takes one link")
	}
	c, err := crewClientFromEnv(getenv)
	if err != nil {
		return 1, errNotInSession
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	text, err := openLink(cctx, c, args[0])
	if err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, text)
	return 0, nil
}

// openLink asks the server to open link and returns its one line.
func openLink(ctx context.Context, c *crewClient, link string) (string, error) {
	if link == "" {
		return "", errors.New("open_url needs a url")
	}
	c.prefix = "open"
	var out struct {
		Message string `json:"message"`
	}
	if err := c.into(ctx, http.MethodPost, "/open", map[string]any{"url": link}, &out); err != nil {
		return "", err
	}
	return out.Message, nil
}
