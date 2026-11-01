package api

import (
	"errors"
	"fmt"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// A routed answer for a hosted session (the decision seam, C5): its
// question is shown by its host, so its answer is applied there, under the
// host's own claim, the first answer winning. The server sends it in the
// host connection's own lane (answer_as), only to a host whose register
// said answerAs, and only from the owner's own server: a switchyard, which
// someone else may run, has no authority over the terminal, and sends
// none. Nothing types a plain line into a hosted session (no question
// named): the protocol gains an answer, not a typing channel.

// routesHosted reports whether the decision router reads the hosted
// session hs: on the owner's own server, when its host said answerAs.
func (s *Server) routesHosted(hs *signal.HostedSession) bool {
	return !s.cfg.Switchyard.Enabled && hs.TakesAnswers()
}

// answerHosted sends r, an answer to the question the hosted session d
// shows, to its host: "sent", the host's next attention saying whether it
// was applied. Refused, ext.ErrUnsupported: on a switchyard, a line naming
// no question, a session that is no hosted one, and one whose host did not
// say answerAs (an older host); ext.ErrRefused with the words why: a line
// blank once cleaned, an answer past the message's bounds, a host not
// connected or not reading.
func (s *Server) answerHosted(d session.Driver, r ext.SubmitRequest) (ext.SubmitResult, error) {
	hs, ok := d.(*signal.HostedSession)
	switch {
	case !ok:
		return ext.SubmitResult{}, fmt.Errorf("%w: the session takes no submission from a provider", ext.ErrUnsupported)
	case s.cfg.Switchyard.Enabled:
		return ext.SubmitResult{}, fmt.Errorf("%w: a switchyard answers no hosted session's question", ext.ErrUnsupported)
	case r.Question == "":
		return ext.SubmitResult{}, fmt.Errorf("%w: a hosted session takes an answer to its question, never a line of its own", ext.ErrUnsupported)
	case r.Text != "" && session.SubmitLine(r.Text) == "":
		return ext.SubmitResult{}, refused("the line is blank")
	}
	m := proto.HostAnswerAsMsg{T: proto.HostAnswerAs, Question: r.Question, Input: r.Input, Text: r.Text, ByName: r.By.Name, BySubject: r.By.Subject}
	if !m.Valid() {
		return ext.SubmitResult{}, refused("the answer is past what a hosted session's answer holds")
	}
	switch err := hs.AnswerAs(m); {
	case errors.Is(err, ext.ErrUnsupported):
		return ext.SubmitResult{}, fmt.Errorf("%w: the session's host takes no routed answer (an older host)", ext.ErrUnsupported)
	case errors.Is(err, signal.ErrHostGone):
		return ext.SubmitResult{}, refused("the session's host is not connected")
	case errors.Is(err, signal.ErrSlowHost):
		return ext.SubmitResult{}, refused("the session's host is not reading")
	case err != nil:
		return ext.SubmitResult{}, refused("%v", err)
	}
	return ext.SubmitResult{Status: "sent"}, nil
}

// recordRouted records the status entry words of a route with
// destinations in the session's activity: a server session's own, a hosted
// session's through its host, which records it and reports it back.
func recordRouted(d session.Driver, words string) {
	e := session.ActivityEntry{Type: session.ActivityStatus, Message: words}
	switch drv := d.(type) {
	case *session.Local:
		drv.Record(e)
	case *signal.HostedSession:
		_ = drv.ForwardActivity(e)
	}
}
