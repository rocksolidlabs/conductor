package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agentproto"
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// GET /api/catalog/{id}/flags: an agent's own flags, read from the help of
// the version installed here, for an arguments field that offers them as a
// person types (and a Model field that offers the model names the help
// gives). The help is read by running the agent's program with its adapter
// recipe's help arguments (agents.HelpArgs) as a version probe runs it:
// argv only, nothing of the request in it, only for a program the identity
// probe identified, in the probes' slots and ended by the server's stop.

// The states of a flags reply, and the reasons of state none.
const (
	flagsRead    = "read"
	flagsReading = "reading"
	flagsNone    = "none"

	whyNoArgs        = "no_args"
	whyNoRecipe      = "no_recipe"
	whyNotInstalled  = "not_installed"
	whyNotIdentified = "not_identified"
	whyUnreadable    = "unreadable"
	whyTooLong       = "too_long"
	whyNoFlags       = "no_flags"
)

const (
	// maxFlagsReply bounds a reply: a list past it answers none, too_long.
	maxFlagsReply = 96 << 10
	// maxHelps is how many help texts the cache keeps (one for each
	// command, environment and version); the one asked for longest ago goes.
	maxHelps = 32
	// helpFailedTTL is how long a failed read is kept before it is tried
	// again.
	helpFailedTTL = 60 * time.Second
	// maxModels bounds the model names of a reply.
	maxModels = 32
)

// flagsReply is the route's answer.
type flagsReply struct {
	Agent   string `json:"agent"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
	// Chat says the agent has a chat way at its installed version: a lone
	// "--" in a launch's arguments then keeps its session in its terminal,
	// as the flags marked keepsTerminal do.
	Chat   bool        `json:"chat,omitempty"`
	Why    string      `json:"why,omitempty"`
	Flags  []replyFlag `json:"flags"`
	Models []string    `json:"models"`
}

// replyFlag is a flag as the help gives it, and whether a session launched
// with it stays in its terminal (its chat refuses it: agentproto).
type replyFlag struct {
	agents.Flag
	KeepsTerminal bool `json:"keepsTerminal,omitempty"`
}

// helpEntry is one help read: its text, or why it gave none.
type helpEntry struct {
	text  string
	why   string // "" for a help read whole
	at    time.Time
	asked time.Time
}

type helpCall struct {
	done chan struct{}
	e    helpEntry
	// stopped says the read was not made, or was ended, by the server's
	// stop: no answer, so the askers are told reading.
	stopped bool
}

func (s *Server) handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.Lock()
	cat := s.catalog
	s.catalogMu.Unlock()
	a, ok := cat.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no agent with that id")
		return
	}
	// The lookup, the identity and the help read together wait at most
	// the probes' wait; what has not answered by then goes on, its answer
	// kept for the next ask.
	ctx, cancel := context.WithTimeout(r.Context(), s.probes.wait)
	defer cancel()
	out := s.flagsOf(ctx, a)
	s.log.Debug("agent flags", "agent", a.ID, "state", out.State, "why", out.Why)
	writeJSON(w, http.StatusOK, out)
}

// flagsOf is the flags reply for agent a.
func (s *Server) flagsOf(ctx context.Context, a catalog.Agent) flagsReply {
	out := flagsReply{Agent: a.ID, Name: a.Name, Flags: []replyFlag{}, Models: []string{}}
	none := func(why string) flagsReply {
		out.State, out.Why, out.Flags, out.Models, out.Chat = flagsNone, why, []replyFlag{}, []string{}, false
		return out
	}
	if !a.AllowArgs {
		return none(whyNoArgs)
	}
	args, ok := agents.HelpArgs(a)
	if !ok {
		return none(whyNoRecipe)
	}
	if len(a.Command) == 0 || relativePath(a.Command[0]) || !a.Probed() {
		// A relative program resolves in the session's folder, which the
		// server cannot stand for; a probe turned off identifies nothing.
		return none(whyNotIdentified)
	}
	path, found, known := s.lookups.found(ctx, a.Command[0])
	switch {
	case !known:
		out.State = flagsReading
		return out
	case !found || path == "" || s.lookups.onWindows(path):
		return none(whyNotInstalled)
	}
	id := s.identityOf(ctx, a, path, false)
	switch {
	case id == nil:
		return none(whyNotIdentified)
	case id.Pending:
		out.State = flagsReading
		return out
	case !id.Identified:
		return none(whyNotIdentified)
	}
	out.Version = id.Version
	command := resolvedCommand(a, path)
	key := probeKey(append(slices.Clone(command), args...), a.Adapter, a.Env) + "\x00\x02" + id.Version
	e, ok := s.probes.helpText(ctx, key, command, args, a.Env)
	if !ok {
		out.State = flagsReading
		return out
	}
	if e.why != "" {
		return none(e.why)
	}
	flags, err := agents.ParseFlags(a, e.text)
	switch {
	case errors.Is(err, agents.ErrHelpTooLong):
		return none(whyTooLong)
	case err != nil:
		return none(whyNoFlags)
	}
	chat := a.Protocol != nil && !a.Protocol.Off() && agentproto.MovesFor(a.Protocol.Name, a.Adapter) && agentproto.Qualified(a.Protocol.Name, id.Version)
	out.Chat = chat
	for _, f := range flags {
		rf := replyFlag{Flag: f}
		if chat {
			for _, n := range append([]string{f.Name}, f.Aliases...) {
				if agentproto.StaysInTerminal(a.Protocol.Name, n) {
					rf.KeepsTerminal = true
				}
			}
		}
		out.Flags = append(out.Flags, rf)
		if f.Set == agents.SetModel {
			names := slices.Clone(f.Examples)
			for _, c := range f.Choices {
				names = append(names, c.Value)
			}
			for _, m := range names {
				if modelPattern.MatchString(m) && !slices.Contains(out.Models, m) && len(out.Models) < maxModels {
					out.Models = append(out.Models, m)
				}
			}
		}
	}
	out.State = flagsRead
	if b, err := json.Marshal(out); err != nil || len(b) > maxFlagsReply {
		return none(whyTooLong)
	}
	return out
}

// helpText is the help read under key (command, its help arguments, its
// environment and its version): ok false while the read is under way past
// ctx (reading), or the server is stopping. A read under way is joined; one
// that is kept is answered from the cache, and a failed one is tried again
// only after helpFailedTTL.
func (c *probeCache) helpText(ctx context.Context, key string, command, args []string, env map[string]string) (helpEntry, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return helpEntry{}, false
	}
	now := c.now()
	if e, ok := c.helps[key]; ok {
		if e.why == "" || now.Sub(e.at) < helpFailedTTL {
			e.asked = now
			out := *e
			c.mu.Unlock()
			return out, true
		}
		delete(c.helps, key)
	}
	call, joined := c.helpCalls[key]
	if !joined {
		call = &helpCall{done: make(chan struct{})}
		c.helpCalls[key] = call
		// Counted under mu, where close sets closed: close waits for it.
		c.probing.Add(1)
	}
	c.mu.Unlock()
	if !joined {
		go c.readHelp(key, command, args, env, call)
	}
	select {
	case <-call.done:
		return call.e, !call.stopped
	case <-ctx.Done():
		return helpEntry{}, false
	}
}

// readHelp reads one help once a slot is free, under the cache's life (the
// read outlives its asker's request, not the server), and settles call. A
// read the stop ended, or that failed on the run's own side (Retry), is not
// kept.
func (c *probeCache) readHelp(key string, command, args []string, env map[string]string, call *helpCall) {
	defer c.probing.Done()
	var text string
	res := agents.ProbeResult{Error: errProbeStopping}
	ran := c.inSlot(func(r *agents.ProbeProcess) { text, res = c.help(c.life, r, command, args, env) })
	e := helpEntry{text: text}
	switch {
	case res.TooLong:
		e.why, e.text = whyTooLong, ""
	case !res.Ran:
		e.why, e.text = whyUnreadable, ""
	}
	c.mu.Lock()
	e.at, e.asked = c.now(), c.now()
	call.e = e
	call.stopped = !ran || c.closed
	if c.helpCalls[key] == call {
		delete(c.helpCalls, key)
	}
	if !call.stopped && !res.Retry {
		if _, had := c.helps[key]; !had && len(c.helps) >= maxHelps {
			oldest := ""
			for k, old := range c.helps {
				if oldest == "" || old.asked.Before(c.helps[oldest].asked) {
					oldest = k
				}
			}
			delete(c.helps, oldest)
		}
		kept := e
		c.helps[key] = &kept
	}
	c.mu.Unlock()
	close(call.done)
}
