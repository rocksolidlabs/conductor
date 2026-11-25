package api

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A provider's line held back at a screen that is not its agent's prompt,
// the session asking nothing, is refused in the words of that refusal, not
// as a question the session asks; nothing is typed.
func TestHostSubmitAtAScreenThatIsNotThePromptSaysSo(t *testing.T) {
	paused := catalog.Agent{ID: "paused", Name: "Paused", Command: []string{"/bin/sh", "-c", `printf '  Your session has paused\r\n\r\n  Press Enter to resume'; exec cat >/dev/null`}, PromptShape: `(?m)^> [^\n]*\s*\z`}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{paused})
	id := e.createSession("paused")
	l := e.local(id)
	if !waitFor(10*time.Second, func() bool { return strings.Contains(strings.Join(l.ScreenLines(24), "\n"), "Press Enter to resume") }) {
		t.Fatalf("the screen never showed: %q", l.ScreenLines(24))
	}
	h := testHost(e.srv, t.Context(), "sched")
	_, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Text: "now run the tests", By: ada})
	if !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), session.NotAtPromptWords) {
		t.Fatalf("a line at a screen that is not the prompt: %v", err)
	}
	if a := l.Info().Attention; a.State == session.AttentionNeedsInput {
		t.Fatalf("the session asks: %+v", a)
	}
	if got := inputs(l); len(got) != 0 {
		t.Fatalf("typed %q", got)
	}
}
