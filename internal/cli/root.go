// Package cli parses command-line flags and dispatches subcommands. It holds
// no business logic.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/rocksolidlabs/conductor/internal/version"
)

const usage = `conductor - shared agent terminals

Usage:
  conductor serve [flags]      run the web server
  conductor switchyard [flags] run a coordinator of hosted sessions that launches nothing
  conductor host [flags] -- <command...>
                               host a local terminal session
  conductor notify [flags]     report "needs input" from inside a session
  conductor crew create|add|status|link
                               form a crew around this session, from inside it
  conductor task context|result|ticket
                               read this session's task in a managed run, submit its result, and
                               read and comment on the Dispatch ticket its run is linked to
  conductor up <crew-id> [--server URL] [--token T] [--open]
                               launch a saved crew as a run and print its URL
  conductor crews [--server URL] [--token T] [--ids]
                               list the saved crews (--ids: ids only, for completion)
  conductor dispatch projects|tickets|show|create|edit|comment|accept|link [--server URL] [--token T]
                               Dispatch's projects and tickets from the shell
  conductor hooks install <adapter>|all [--home DIR] [--data-dir DIR]
                               put Conductor's hooks into an agent's own config
  conductor hooks status       show which agents have Conductor's hooks
  conductor skill              print the Conductor skill (SKILL.md)
  conductor mcp                serve the skill's commands as MCP tools on stdio (registered with agents at launch)
  conductor completion zsh|bash
                               print a shell completion script
  conductor completion install [--shell zsh|bash] [--rc FILE]
                               add the line that loads it to ~/.zshrc or ~/.bashrc
  conductor version            print the version

Run "conductor <command> -h" for command flags.

` + Credit + "\n"

// Credit is the sponsor line --version and --help end with, and nothing else
// prints: never in run output or a log.
const Credit = "Sponsored and maintained by RockSolid Labs · rocksolidlabs.io"

// Run executes the CLI and returns the process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2, nil
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stdin, stdout, stderr)
	case "switchyard":
		return runServe(ctx, append([]string{"--switchyard"}, args[1:]...), stdin, stdout, stderr)
	case "host":
		return runHost(ctx, args[1:], stdin, stdout, stderr)
	case "notify":
		return runNotify(ctx, args[1:], stdin, stdout, stderr)
	case "mcp":
		return runMcp(ctx, args[1:], stdin, stdout, stderr)
	case "crew":
		return runCrew(ctx, args[1:], stdin, stdout, stderr)
	case "task":
		return runTask(ctx, args[1:], stdin, stdout, stderr)
	case "up":
		return runUp(ctx, args[1:], stdout, stderr)
	case "crews":
		return runCrews(ctx, args[1:], stdout, stderr)
	case "dispatch":
		return runDispatch(ctx, args[1:], stdin, stdout, stderr)
	case "hooks":
		return runHooks(ctx, args[1:], stdout, stderr)
	case "skill":
		return runSkill(args[1:], stdout, stderr)
	case "completion":
		return runCompletion(args[1:], stdout, stderr)
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, "conductor", version.String())
		fmt.Fprintln(stdout, Credit)
		return 0, nil
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0, nil
	}
	fmt.Fprint(stderr, usage)
	return 2, fmt.Errorf("unknown command %q", args[0])
}
