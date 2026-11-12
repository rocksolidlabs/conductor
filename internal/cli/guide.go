package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/version"
)

// conductor guide and the MCP tool guide read the operating guide from
// the running server (internal/api, guide.go): in a session the
// session's own (GET /api/sessions/{id}/guide, with its token), else the
// admin's (GET /api/guide, with the workbench token).
const guideUsage = `Usage: conductor guide [--section S] [--json] [--server URL] [--token T] [--insecure]

What the running Conductor server says you may do here. Without --section,
the overview: the server's version, who you are and what you may do, and the
sections with their sizes. With it, that section: operations (what you may
call, with its MCP tool, command, route and input), limits, host, receipts,
providers, or briefs (a Brakeman brief's shape: its item kinds with their
fields and bounds, the ref forms, its JSON Schema with an example). --json
prints the server's reply as it is.

In a Conductor session it reads the session's guide with the session's token.
Elsewhere, or with --server or --token, it reads the admin's guide with the
workbench token, as conductor dispatch does: the server defaults to
CONDUCTOR_SERVER, else http://localhost:8080, the token to
CONDUCTOR_WORKBENCH_TOKEN, and a plain http:// server on another machine is
refused unless --insecure says to go ahead.
`

// runGuide is conductor guide: exit 2 for a usage error, 1 when the
// server cannot be read or refuses.
func runGuide(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := addAPIFlags(fs)
	section := fs.String("section", "", "the section: operations, limits, host, receipts, providers or briefs (the overview unless given)")
	asJSON := fs.Bool("json", false, "print the server's reply as it is")
	fs.Usage = func() {
		fmt.Fprint(stderr, guideUsage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if fs.NArg() != 0 {
		return 2, fmt.Errorf("guide takes no argument: %q", fs.Arg(0))
	}
	admin := *server.server != "" || *server.token != ""
	var raw []byte
	var err error
	how := "conductor guide --section NAME"
	if c, inSession := crewClientFromEnv(os.Getenv); inSession == nil && !admin {
		c.prefix = "guide"
		raw, err = sessionGuide(ctx, c, *section)
	} else {
		client, cerr := server.client(stderr)
		if cerr != nil {
			return 2, cerr
		}
		raw, err = adminGuide(ctx, client, *section)
	}
	if err != nil {
		return 1, err
	}
	if *asJSON {
		return printReply(stdout, raw, false)
	}
	text, err := guideText(raw, how)
	if err != nil {
		return 1, err
	}
	_, err = fmt.Fprintln(stdout, text)
	return 0, err
}

// guidePath is the guide's route under base with the section's query.
func guidePath(base, section string) string {
	if section == "" {
		return base
	}
	return base + "?section=" + url.QueryEscape(section)
}

// sessionGuide reads the session's guide with its token.
func sessionGuide(ctx context.Context, c *crewClient, section string) ([]byte, error) {
	var raw json.RawMessage
	if err := c.into(ctx, http.MethodGet, guidePath("/guide", section), nil, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// adminGuide reads the admin's guide with the workbench token.
func adminGuide(ctx context.Context, c *apiClient, section string) ([]byte, error) {
	var raw json.RawMessage
	if err := c.doBody(ctx, http.MethodGet, guidePath("/api/guide", section), nil, &raw); err != nil {
		return nil, fmt.Errorf("guide: %w", err)
	}
	return raw, nil
}

// guideToolText is the MCP tool guide's answer: the session's guide as text.
func guideToolText(ctx context.Context, c *crewClient, section string) (string, error) {
	raw, err := sessionGuide(ctx, c, section)
	if err != nil {
		return "", err
	}
	return guideText(raw, `call guide with {"section": NAME}`)
}

// guideText is a reply of the guide as an agent or a person reads it:
// first, when the server's version is not this binary's, that the guide
// is the server's; then the overview (who the caller is, the operations
// it may call, the limits that bind it, the rules and the sections) or
// the operations as lines, and any other section as its JSON. how says
// how to read a section.
func guideText(raw []byte, how string) (string, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("guide: an unreadable reply: %w", err)
	}
	var serverVersion, commit, section string
	_ = json.Unmarshal(env["version"], &serverVersion)
	_ = json.Unmarshal(env["commit"], &commit)
	_ = json.Unmarshal(env["section"], &section)
	var b strings.Builder
	if serverVersion != version.Version {
		fmt.Fprintf(&b, "this conductor is %s, the server is %s: the guide is the server's\n\n", version.Version, serverVersion)
	}
	switch section {
	case "overview":
		var ov struct {
			Caller struct {
				Kind, Session, Run, Member string
				Kinds, Scopes              []string
			}
			Sections []struct {
				Name  string
				Bytes int
			}
			// May is a pointer: a server older than the field (#691) sends
			// none, which is not "nothing may be called".
			May           *[]string
			Report        string
			Limits, Rules []string
		}
		if err := json.Unmarshal(env["overview"], &ov); err != nil {
			return "", fmt.Errorf("guide: an unreadable overview: %w", err)
		}
		fmt.Fprintf(&b, "Conductor %s (%s), guide %s\n", serverVersion, commit, env["guide"])
		who := "the admin (the workbench token)"
		if ov.Caller.Kind == "session" {
			who = "session " + ov.Caller.Session + " (" + strings.Join(ov.Caller.Kinds, ", ") + ")"
			if ov.Caller.Run != "" {
				who += ", member " + ov.Caller.Member + " of run " + ov.Caller.Run
			}
		}
		fmt.Fprintf(&b, "You are %s; your scopes: %s.\n", who, strings.Join(ov.Caller.Scopes, ", "))
		// The sentence asking for both, ahead of them (#726); a server
		// older than the field sends none.
		if ov.Report != "" {
			b.WriteString(ov.Report + "\n")
		}
		// What it may call now and within what, as the overview names them
		// (each whole in the operations and limits sections): an agent told
		// only to call the guide reads them here. An overview without the
		// field says nothing of what may be called, and neither does the
		// text: its operations section says it.
		switch {
		case ov.May == nil:
		case len(*ov.May) > 0:
			fmt.Fprintf(&b, "You may call: %s.\n", strings.Join(*ov.May, ", "))
		default:
			b.WriteString("You may call no operation here.\n")
		}
		if len(ov.Limits) > 0 {
			b.WriteString("Your limits:\n")
			for _, l := range ov.Limits {
				fmt.Fprintf(&b, "- %s\n", l)
			}
		}
		if len(ov.Rules) > 0 {
			b.WriteString("Rules:\n")
		}
		for _, r := range ov.Rules {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("Sections:")
		for _, s := range ov.Sections {
			fmt.Fprintf(&b, " %s (%d bytes)", s.Name, s.Bytes)
		}
		fmt.Fprintf(&b, "\nRead one: %s.", how)
	case "operations":
		var ops []struct {
			Name, Summary, Scope, Method, Path, MCP, Receipt, Revision string
			CLI                                                        []string
			// Input and Bounds are the running server's, which the MCP
			// tools' schemas (this binary's) need not be.
			Input  json.RawMessage
			Bounds map[string]int
		}
		if err := json.Unmarshal(env["operations"], &ops); err != nil {
			return "", fmt.Errorf("guide: unreadable operations: %w", err)
		}
		for i, op := range ops {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s [%s] %s %s\n    %s", op.Name, op.Scope, op.Method, op.Path, op.Summary)
			var via []string
			if op.MCP != "" {
				via = append(via, "tool "+op.MCP)
			}
			if len(op.CLI) > 0 {
				via = append(via, "command "+strings.Join(op.CLI, " "))
			}
			if op.Receipt != "" {
				via = append(via, "receipt "+op.Receipt)
			}
			if op.Revision != "" {
				via = append(via, "checks the "+op.Revision+"'s revision")
			}
			if len(via) > 0 {
				fmt.Fprintf(&b, "\n    %s", strings.Join(via, "; "))
			}
			if input, err := jsonText(op.Input, false); len(op.Input) > 0 && err == nil && input != "null" {
				fmt.Fprintf(&b, "\n    input %s", input)
			}
			if len(op.Bounds) > 0 {
				bounds := make([]string, 0, len(op.Bounds))
				for _, k := range slices.Sorted(maps.Keys(op.Bounds)) {
					bounds = append(bounds, fmt.Sprintf("%s %d", k, op.Bounds[k]))
				}
				fmt.Fprintf(&b, "\n    bounds %s", strings.Join(bounds, ", "))
			}
		}
		if len(ops) == 0 {
			b.WriteString("no operations")
		}
	case "providers":
		// GET /api/capabilities's rows (C9), as the server names them:
		// what this build adds beside the open-source feature set.
		var pv struct {
			Known     bool
			Providers []struct{ Kind, Name, State string }
		}
		if err := json.Unmarshal(env["providers"], &pv); err != nil {
			return "", fmt.Errorf("guide: unreadable providers: %w", err)
		}
		switch {
		case !pv.Known:
			b.WriteString("The server does not say which providers it has registered.")
		case len(pv.Providers) == 0:
			b.WriteString("No providers registered: the open-source feature set.")
		default:
			b.WriteString("Providers registered beside the open-source feature set:")
			for _, p := range pv.Providers {
				fmt.Fprintf(&b, "\n- %s: %s (%s)", p.Kind, p.Name, p.State)
			}
		}
	case "briefs":
		// A brief's shape: its words as lines, then its JSON Schema, an
		// example brief inside it, compact.
		var briefs struct {
			Words      []string
			JSONSchema json.RawMessage
		}
		if err := json.Unmarshal(env["briefs"], &briefs); err != nil {
			return "", fmt.Errorf("guide: unreadable briefs: %w", err)
		}
		for _, w := range briefs.Words {
			fmt.Fprintf(&b, "%s\n", w)
		}
		if schema, err := jsonText(briefs.JSONSchema, false); err == nil {
			fmt.Fprintf(&b, "\nIts JSON Schema, with an example brief: %s", schema)
		}
	default:
		text, err := jsonText(env[section], true)
		if err != nil {
			return "", fmt.Errorf("guide: an unreadable %s: %w", section, err)
		}
		b.WriteString(text)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
