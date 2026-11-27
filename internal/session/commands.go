package session

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The agent's own commands on a chat (docs/plans/chat-or-terminal.md,
// row 12; I19). The driver hands the whole list its program gives
// (ProtocolEvents.Commands); the session cleans and bounds it, keeps it on
// the chat in memory with a revision raised by each change, and sends it in
// a TRANSCRIPT message of its own (proto.TranscriptCommandsMsg) to the
// operator's own chat connections alone (operatorChatLocked): after the
// transcript's replay as one attaches, on a change (at most one a second),
// and after a move to the chat. A link's viewer, a peer over the host link
// or a switchyard and a quiet connection are never sent it, for it names
// what is installed on the operator's machine; nor is it in Info, a log
// line, the journal, the activity, sync or export. It goes with the chat.

// CommandsEvery is the least time between two sends of a changed list.
const CommandsEvery = time.Second

// commandsScan bounds the entries of a list the session reads: past it the
// rest is not looked at (the list says more).
const commandsScan = 1000

// cleanCommands is the list as the session keeps it: names of
// proto.CommandNameShape within proto.MaxCommandName (any other dropped, a
// name already kept dropped), at most proto.MaxCommandAliases aliases of
// the same shape within proto.MaxCommandAlias, an alias that is the name of
// any command kept or an alias already given (to this command or an
// earlier one) dropped, so that a name and an alias never mean two
// commands; the words cut to proto.MaxCommandWords with
// an ellipsis and the hint to proto.MaxCommandHint, each of one line with
// every control and hidden character dropped; at most proto.MaxCommands
// commands, more saying the agent listed more that would be kept.
func cleanCommands(list []Command) (kept []proto.Command, more bool) {
	seen := map[string]bool{}
	var from [][]string
	for i, c := range list {
		if i >= commandsScan {
			more = true
			break
		}
		if len(c.Name) > proto.MaxCommandName || !proto.CommandNameShape.MatchString(c.Name) || seen[c.Name] {
			continue
		}
		if len(kept) == proto.MaxCommands {
			more = true
			break
		}
		seen[c.Name] = true
		kept = append(kept, proto.Command{Name: c.Name, Words: commandWords(c.Words, proto.MaxCommandWords, "…"), Hint: commandWords(c.Hint, proto.MaxCommandHint, "")})
		from = append(from, c.Aliases)
	}
	return withAliases(kept, from, seen), more
}

// withAliases gives each kept command its aliases, once every name is
// known: an alias that is a kept name (seen) or one already given is
// dropped, as one of another shape or past its bound is.
func withAliases(kept []proto.Command, from [][]string, seen map[string]bool) []proto.Command {
	given := map[string]bool{}
	for i := range kept {
		for _, a := range from[i] {
			if len(kept[i].Aliases) == proto.MaxCommandAliases {
				break
			}
			if seen[a] || given[a] || len(a) > proto.MaxCommandAlias || !proto.CommandNameShape.MatchString(a) {
				continue
			}
			given[a] = true
			kept[i].Aliases = append(kept[i].Aliases, a)
		}
	}
	return kept
}

// commandWords is s on one line, valid UTF-8, every control and hidden
// character dropped (a line break and a tab a space, runs of white space
// one), cut at a character's end to at most max bytes, mark (an ellipsis)
// ending it when cut.
func commandWords(s string, max int, mark string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	room := max - len(mark)
	for room > 0 && !utf8.RuneStart(s[room]) {
		room--
	}
	return strings.TrimRight(s[:room], " ") + mark
}

// chatCommands is what a chat keeps of the agent's commands: the list, its
// revision (0 while none came), whether the agent listed more, and when it
// was last sent, a send held until CommandsEvery has passed from it.
type chatCommands struct {
	list   []proto.Command
	rev    uint64
	more   bool
	sentAt time.Time
	held   func() bool
}

// Commands is the driver's list: kept, cleaned, and sent on when it
// changed (at most one send a CommandsEvery, the last list sent once the
// time has come).
func (e protocolEvents) Commands(list []Command) {
	kept, more := cleanCommands(list)
	if !e.begin() {
		return
	}
	s, p := e.s, e.p
	c := &p.commands
	if c.rev > 0 && c.more == more && slices.EqualFunc(c.list, kept, sameCommand) {
		s.mu.Unlock()
		return
	}
	c.list, c.more = kept, more
	c.rev++
	s.sendCommandsSoonLocked(p)
	s.mu.Unlock()
}

func sameCommand(a, b proto.Command) bool {
	return a.Name == b.Name && a.Words == b.Words && a.Hint == b.Hint && slices.Equal(a.Aliases, b.Aliases)
}

// sendCommandsSoonLocked sends the list now when CommandsEvery has passed
// since it was last sent, else once it has (one held send at a time). The
// caller holds s.mu.
func (s *Local) sendCommandsSoonLocked(p *protocolWay) {
	c := &p.commands
	if c.held != nil {
		return
	}
	wait := CommandsEvery - s.now().Sub(c.sentAt)
	if c.sentAt.IsZero() || wait <= 0 {
		s.sendCommandsLocked(p)
		return
	}
	c.held = s.afterFunc(wait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.way.Load() != p || s.info.Status.Ended() {
			// The chat went: its list with it.
			return
		}
		p.commands.held = nil
		s.sendCommandsLocked(p)
	})
}

// sendCommandsLocked sends the list to every operator's own chat
// connection, and to no other. The caller holds s.mu.
func (s *Local) sendCommandsLocked(p *protocolWay) {
	f := s.commandsFrameLocked(p)
	if f == nil {
		return
	}
	p.commands.sentAt = s.now()
	s.hub.Each(func(sub *Subscription) {
		if s.operatorChatLocked(sub) {
			sub.send(f)
		}
	})
}

// commandsWithdrawnLocked sends an empty list, under a revision of its
// own, to each of was that is no longer an operator's own chat connection
// (the session's standing changed: a crew formed around it), so its page
// keeps no list the session no longer sends it. The caller holds s.mu.
func (s *Local) commandsWithdrawnLocked(p *protocolWay, was []*Subscription) {
	c := &p.commands
	if c.rev == 0 {
		return
	}
	f, err := proto.EncodeCommands(proto.TranscriptCommandsMsg{Rev: c.rev + 1})
	if err != nil {
		return
	}
	for _, sub := range was {
		if !s.operatorChatLocked(sub) {
			sub.send(f)
		}
	}
}

// commandsFrameLocked is the list's TRANSCRIPT frame, nil while no list
// came. The caller holds s.mu.
func (s *Local) commandsFrameLocked(p *protocolWay) []byte {
	c := &p.commands
	if c.rev == 0 {
		return nil
	}
	f, err := proto.EncodeCommands(proto.TranscriptCommandsMsg{Rev: c.rev, Commands: c.list, More: c.more})
	if err != nil {
		// The list is cleaned to its bounds, so this does not happen; a
		// list that cannot be sent is not sent.
		return nil
	}
	return f
}
