package session

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// codexUpdateScreen is Codex 0.161.0's update question as it drew it in a
// PTY on 2026-10-11 (an isolated CODEX_HOME whose version.json named
// 0.162.1): the heading, the highlighted Update now, and Skip until next
// version drawn word by word with cursor moves between.
const codexUpdateScreen = "\x1b[?2026h\x1b[?25l\x1b[2;3H\x1b[1mUpdate available\x1b[22m\x1b[2m\x1b[2m \xc2\xb7 0.161.0 \xe2\x86\x92 0.162.1\x1b[3;3HRelease notes: \x1b[4m\x1b]8;;https://github.com/openai/codex/releases/latest\ahttps://github.com/openai/codex/releases/latest\x1b]8;;\a\x1b[5;1H\x1b[24m\x1b[22m\x1b[7m\x1b[1m\xe2\x80\xba 1. Update now (runs `npm install -g @openai/codex`)                                               \x1b[6;3H\x1b[27m\x1b[22m2.\x1b[6;6HSkip\x1b[7;3H3.\x1b[7;6HSkip\x1b[7;11Huntil\x1b[7;17Hnext\x1b[7;22Hversion\x1b[9;3H\x1b[1menter\x1b[22m\x1b[2m\x1b[2m continue \xc2\xb7 \x1b[22m\x1b[1mesc\x1b[22m\x1b[2m\x1b[2m skip\x1b[39m\x1b[49m\x1b[0m\x1b[?2026l"

// codexUpdateOverSplash is Codex 0.163.0's update question drawn over its
// splash, as it drew it in a PTY on 2026-10-18 (an isolated HOME and
// CODEX_HOME whose version.json named 0.164.0; no key was sent to that
// screen): the TUI writes only the cells that differ from the frame before,
// so the "e" of "Update", on the screen already from "OpenAI", is skipped
// and the output reads "Updat available" where the screen reads the
// heading whole (#165).
const codexUpdateOverSplash = "\x1b[?2004h\x1b[>4;0m\x1b[>7u\x1b[?1004h\x1b[6n\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b[?u\x1b[c\x1b[?2026h\x1b[?25l\x1b[?1049h\x1b[>4;0m\x1b[>7u\x1b[?1007l\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h\x1b[?25l\x1b[1;1H\x1b[J\x1b[?2026h\x1b[?25l\x1b[2;3H\x1b[38;2;99;168;248;49m>_ \x1b[1m\x1b[39;49mOpenAI Codex\x1b[22m\x1b[2m\x1b[2m (v0.163.0)\x1b[3;6Hloading\x1b[21;1H\x1b[22m\x1b[1m\xe2\x80\xba\x1b[21;3H\x1b[22m\x1b[2m\x1b[2mAsk Codex to do anything\x1b[24;3H\x1b[22m\x1b[1m?\x1b[24;5H\x1b[22mfor\x1b[24;9Hshortcuts\x1b[39m\x1b[49m\x1b[0m\x1b[1;1H\x1b[0 q\x1b[1;1H \x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[?25l\x1b[3;6H\x1b[2m/tmp/conductor-live/c2\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[39m\x1b[49m\x1b[0m\x1b[21;3H\x1b[?25h\x1b[?2026l\x1b[?2026h\x1b[?25l\x1b[21;2H\x1b[0m\x1b[49m\x1b[K\x1b[24;2H\x1b[0m\x1b[49m\x1b[K\x1b[2;3H\x1b[1mUpdat\x1b[2;9H available\x1b[22m\x1b[2m\x1b[2m \xc2\xb7 0.163.0 \xe2\x86\x92 0.164.0\x1b[3;3HRelease n\x1b[3;13Htes: \x1b[4m\x1b]8;;https://github.com/openai/codex/releases/latest\ahttps://github.com/openai/codex/releases/latest\x1b]8;;\a\x1b[5;1H\x1b[24m\x1b[22m\x1b[7m\x1b[1m\xe2\x80\xba 1. Update now (runs `npm install -g @openai/codex`)                           \x1b[6;3H\x1b[27m\x1b[22m2.\x1b[6;6HSkip\x1b[7;3H3.\x1b[7;6HSkip\x1b[7;11Huntil\x1b[7;17Hnext\x1b[7;22Hversion\x1b[9;3H\x1b[1menter\x1b[22m\x1b[2m\x1b[2m continue \xc2\xb7 \x1b[22m\x1b[1mesc\x1b[22m\x1b[2m\x1b[2m skip\x1b[21;1H\x1b[22m \x1b[39m\x1b[49m\x1b[0m\x1b[?2026l"

// A question's words can be other words of its screen than the ones that
// find it (the catalog's words): Codex's update question, found by its
// highlighted "Update now (runs", shows its heading, so that "answered:"
// never reads as if Update had been picked; its "Skip until next version"
// is the digit 3 alone, which answers it without an Enter.
func TestAQuestionsWordsAreItsHeading(t *testing.T) {
	upd := []Option{{Label: "Skip this update", Input: "\x1b"}, {Label: "Skip until next version", Input: "3"}}
	s, p := newLocalWith(t, quiet(Options{Questions: []StartupQuestion{{
		Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`),
		Words:   regexp.MustCompile(`(?i)(update\s*available)\W*?(\d+(?:\.\d+)+\s*(?:→|->)\s*\d+(?:\.\d+)+)`),
		Answers: upd,
	}}}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(codexUpdateScreen))
	att := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if att.State != AttentionNeedsInput || att.Message != "Update available 0.161.0 → 0.162.1" || len(att.Options) != 2 || att.Options[1] != upd[1] {
		t.Fatalf("attention %+v", att)
	}
	if err := s.Input(sub, []byte("3")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "3" {
		t.Fatalf("written %q", got)
	}
	// Consumed by its words: a reply refused, and withdrawn once the screen moves on.
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("3")); !errors.Is(err, ErrQuestionAnswered) || err.Error() != QuestionConsumedWords {
		t.Fatalf("a reply after the key: %v", err)
	}
	p.outW.Write([]byte("\x1b[2J\x1b[H\x1b[2;3H› Ask Codex to do anything"))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
		t.Fatalf("the question withdrawn: %+v, answered %+v", a, s.Info().LastAnswer)
	}
	// Without its words a question is told by what found it; a words pattern that matches nothing changes nothing.
	q := StartupQuestion{Pattern: regexp.MustCompile(`(?i)update\s*now\s*\(\s*runs`), Words: regexp.MustCompile(`never here`)}
	if got := q.words("› 1. Update now (runs `npm`"); got != "Update now (runs" {
		t.Fatalf("words %q", got)
	}
	q.Words = regexp.MustCompile(`(?i)release\s*notes`)
	if got := q.words("Release notes: x › 1. Update now (runs `npm`"); got != "Release notes" {
		t.Fatalf("words without a group %q", got)
	}
	if got := q.words("nothing to see"); got != "" {
		t.Fatalf("words of a screen without the question %q", got)
	}
}

// approvalChoices are the answers to Codex's command approval, as the
// catalog has them: each key picks at once.
var approvalChoices = []Option{{Label: "Yes, proceed", Input: "y"}, {Label: "Yes, and don't ask again", Input: "p"}, {Label: "No, tell Codex what to do differently", Input: "\x1b"}}

// approvalQuestions are the questions Codex's approvals are held by, as the
// built-in catalog entry has them: asked at any time, the tool approval's
// choices read off its form, those that remember it mode answers (#716).
func approvalQuestions() []StartupQuestion {
	codex, _ := catalog.Default().Get("codex")
	var out []StartupQuestion
	for _, q := range codex.Questions {
		if !q.Anytime {
			continue
		}
		answers := make([]Option, len(q.Answers))
		for i, a := range q.Answers {
			answers[i] = Option{Label: a.Label, Input: a.Input}
		}
		sq := StartupQuestion{Pattern: regexp.MustCompile(q.Prompt), Words: regexp.MustCompile(q.Words), Anytime: true, Answers: answers, FromScreen: q.AnswersFromScreen}
		if q.ModeAnswers != "" {
			sq.Mode = regexp.MustCompile(q.ModeAnswers)
		}
		out = append(out, sq)
	}
	return out
}

// approvalDialog is Codex 0.161's command approval for cmd as a TUI draws
// it at the bottom of an 80 by 24 screen: each run of cells placed with a
// cursor move, styles between the words, each key in a style of its own
// (the words of the screen the owner saw in the rc.7 app).
func approvalDialog(cmd string) string {
	return "\x1b[?2026h\x1b[12;1H\x1b[J\x1b[13;3H\x1b[1mWould you like to run the following command?\x1b[22m" +
		"\x1b[15;3HEnvironment: local\x1b[17;3HReason: May I rerun the curl command with network access?" +
		"\x1b[19;3H\x1b[2m$\x1b[22m " + cmd +
		"\x1b[20;1H\x1b[36m› 1. Yes, proceed (\x1b[1my\x1b[22m)" +
		"\x1b[21;3H\x1b[39m2. Yes, and don't ask again for commands that start with" +
		"\x1b[22;6H`" + cmd + "` (\x1b[1mp\x1b[22m)" +
		"\x1b[23;3H3. No, and tell Codex what to do differently (\x1b[1mesc\x1b[22m)" +
		"\x1b[24;3H\x1b[2mPress enter to confirm or esc to cancel\x1b[22m\x1b[?2026l"
}

// approvalNext is the next queued approval, for cmd, drawn over
// approvalDialog's the way Codex 0.161 draws it (approval_overlay.rs
// advances its queue in place, custom_terminal.rs writes only the cells
// that changed): the command's row and the prefix's, nothing else.
func approvalNext(cmd string) string {
	return "\x1b[?2026h\x1b[19;5H" + cmd + "\x1b[K\x1b[22;7H" + cmd + "` (\x1b[1mp\x1b[22m)\x1b[K\x1b[?2026l"
}

// approvalGone is what Codex draws once the dialog is answered: the overlay
// gives way to what it says next.
func approvalGone(next string) string { return "\x1b[12;1H\x1b[J" + next }

const approvalWords = "Would you like to run the following command? $ curl -sI https://example.com"

func asked(a Attention) bool { return a.State == AttentionNeedsInput && a.Source == SourceQuestion }

// Codex's command approval comes after the first prompt, which stops the
// startup questions' watcher: the questions asked during the work have a
// watcher of their own. The session needs input with the dialog's words
// and its three choices; a person's typed text is refused with nothing
// written (its letters would reach the dialog's keys, its Enter would pick
// Yes), Conductor's own text waits, an arrow answers nothing, and the
// choice's key alone answers it.
func TestAnApprovalAfterTheFirstPromptIsAskedWithItsChoices(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		TrustAnswers: []Option{{Label: "Yes, trust this folder", Input: "\r"}},
		Questions:    approvalQuestions(),
	}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h› Ask Codex to do anything"))
	waitPaste(t, s, true)
	// The crew's first prompt: its Enter ends the startup questions' watch.
	if res, err := s.Submit(t.Context(), Submission{Text: "check example.com", ByName: "crew"}); err != nil || !res.Entered {
		t.Fatalf("the first prompt: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, asked)
	if att.Message != approvalWords || att.Kind != KindPermission || !sameOptions(att.Options, approvalChoices) {
		t.Fatalf("attention %+v", att)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "yes please", By: sub}); !errors.Is(err, ErrQuestion) {
		t.Fatalf("typed text at the approval: %v", err)
	}
	// A chat to the agent gets the refusal's words, not the trust question's.
	if m := decodeControl(t, ChatErrorFrame(ErrQuestion, "r1")); m["code"] != proto.ErrCodeNotSent || m["message"] != QuestionWords || m["requestId"] != "r1" {
		t.Fatalf("the chat's refusal %v", m)
	}
	if res, err := s.Submit(t.Context(), Submission{Text: "handoff", ByName: "crew", UnlessWaiting: true}); err != nil || res.Typed {
		t.Fatalf("Conductor's own text: %+v %v", res, err)
	}
	noWrite(t, p, 200*time.Millisecond)
	if err := s.Input(sub, []byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := s.Info().Attention; !asked(a) || a.Since == nil || *a.Since != *att.Since {
		t.Fatalf("an arrow answered the approval: %+v", a)
	}
	if err := s.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "y" {
		t.Fatalf("written %q", got)
	}
	// Consumed: the program's dialog has the key, a reply to it is refused,
	// and it is withdrawn once the dialog has gone from the screen.
	if a := s.Info().Attention; !asked(a) || a.ID != att.ID {
		t.Fatalf("the key changed the dialog shown: %+v", a)
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("y")); !errors.Is(err, ErrQuestionAnswered) || err.Error() != QuestionConsumedWords {
		t.Fatalf("a reply after the key: %v", err)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\n› ")))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
		t.Fatalf("the dialog withdrawn: %+v, answered %+v", a, s.Info().LastAnswer)
	}
}

// The bell Codex rings as it shows the dialog (while its terminal is not
// focused) gives way to the dialog's question once the screen is quiet; a
// bell after the question is seen leaves it as it is, words, choices and
// all. A bell with nothing waiting still asks.
func TestABellBeforeTheApprovalGivesWayAndOneAfterLeavesIt(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\a"))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNeedsInput }); a.Source != SourceBell || a.Message != "terminal bell" {
		t.Fatalf("a bell with nothing waiting: %+v", a)
	}
	s.Input(sub, []byte("x"))
	nextWrite(t, p)
	time.Sleep(600 * time.Millisecond) // past the bell's burst limit
	// The bell before the dialog, in the same write.
	p.outW.Write([]byte("\a" + approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, asked)
	if att.Message != approvalWords || len(att.Options) != 3 {
		t.Fatalf("the question after the bell: %+v", att)
	}
	time.Sleep(600 * time.Millisecond)
	p.outW.Write([]byte("\a"))
	time.Sleep(300 * time.Millisecond)
	if a := s.Info().Attention; !asked(a) || a.Message != approvalWords || len(a.Options) != 3 || *a.Since != *att.Since {
		t.Fatalf("a bell after the question replaced it: %+v", a)
	}
}

// The same dialog, answered, comes again for the next command: it is asked
// again with its choices, and answered again, by Escape this time.
func TestTheSameApprovalAnsweredThenShownAgainIsAskedAgain(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	first := waitAttention(t, s, asked)
	s.Input(sub, []byte("y"))
	nextWrite(t, p)
	if a := s.Info().Attention; !asked(a) || a.ID != first.ID {
		t.Fatalf("the key changed the dialog shown: %+v", a)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\nHTTP/2 200\r\n\x1b[2m• Working\x1b[22m")))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
	time.Sleep(800 * time.Millisecond)
	if a := s.Info().Attention; a.State != AttentionNone {
		t.Fatalf("the answered dialog was asked again without being drawn: %+v", a)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	again := waitAttention(t, s, asked)
	if again.Message != approvalWords || len(again.Options) != 3 || !again.Since.After(*first.Since) {
		t.Fatalf("asked again: %+v", again)
	}
	s.Input(sub, []byte("\x1b"))
	if got := nextWrite(t, p); got != "\x1b" {
		t.Fatalf("written %q", got)
	}
	// An Escape that ends a write puts the dialog behind; it is cleared
	// once the dialog goes.
	p.outW.Write([]byte(approvalGone("✗ You canceled the request to run curl -sI https://example.com\r\n")))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// A person who answers the dialog in its terminal before the session has
// seen the screen quiet leaves its words in the text the watcher keeps:
// the key that answers it (one of the choices' keys, or an Enter) puts
// them behind, so that the question is not raised once Codex has moved on.
func TestAnApprovalAnsweredInTheTerminalBeforeItIsSeenIsNotAskedAfter(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(100 * time.Millisecond) // read, not yet quiet
	s.Input(sub, []byte("y"))
	nextWrite(t, p)
	p.outW.Write([]byte(approvalGone("HTTP/2 200\r\n› ")))
	time.Sleep(900 * time.Millisecond)
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("the answered dialog was asked: %+v", a)
	}
}

// A dialog drawn just before Conductor types (a handoff, a broadcast) and
// not yet seen quiet is looked at before the text: nothing is written (the
// overlay would drop the paste, and the handoff with it), the handoff waits
// for the answer, and a person's reply is refused. One drawn during the
// pause, after the text, holds the Enter back: the screen is looked at
// again before it, and the Enter would have approved the command.
func TestAnApprovalDrawnJustBeforeASubmissionHoldsIt(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h› "))
	waitPaste(t, s, true)
	time.Sleep(700 * time.Millisecond)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true})
	if err != nil || res.Typed || res.Entered {
		t.Fatalf("a handoff at the dialog: %+v %v", res, err)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "yes", By: sub}); !errors.Is(err, ErrQuestion) {
		t.Fatalf("a reply at the dialog: %v", err)
	}
	noWrite(t, p, 200*time.Millisecond)
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("attention %+v", a)
	}

	s2, p2 := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), SubmitPause: 100 * time.Millisecond}))
	p2.outW.Write([]byte("\x1b[?2004h› "))
	waitPaste(t, s2, true)
	time.Sleep(700 * time.Millisecond)
	var once sync.Once
	p2.onWrite = func() {
		once.Do(func() { go p2.outW.Write([]byte(approvalDialog("curl -sI https://example.com"))) })
	}
	res, err = s2.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true})
	if err != nil || !res.Typed || res.Entered {
		t.Fatalf("a handoff whose pause saw the dialog drawn: %+v %v", res, err)
	}
	nextWrite(t, p2)
	noWrite(t, p2, 200*time.Millisecond)
	if a := s2.Info().Attention; !asked(a) {
		t.Fatalf("attention %+v", a)
	}
}

// What the question takes the place of, and what it does not: a hook's
// report without choices (the PermissionRequest hook's "Allow exec?") gives
// way to the dialog's words and choices, and the same report coming late
// does not hide them; nor does an OSC notification. A report with choices
// of its own gives way too (its choices would land on the dialog), and
// does not hide the question coming late; a startup question stays.
func TestAnApprovalTakesThePlaceOfAnyWaitButAStartupQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow exec?", SourceAPI, KindPermission, nil); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, asked)
	if att.Message != approvalWords || len(att.Options) != 3 {
		t.Fatalf("attention %+v", att)
	}
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Allow exec?", SourceAPI, KindPermission, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	p.outW.Write([]byte("\x1b]9;Codex wants to run a command\a"))
	time.Sleep(300 * time.Millisecond)
	if a := s.Info().Attention; !asked(a) || *a.Since != *att.Since {
		t.Fatalf("a report without choices hid the question: %+v", a)
	}

	s2, p2 := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	mine := []Option{{Label: "Postgres", Input: "Postgres\r"}, {Label: "SQLite", Input: "SQLite\r"}}
	if err := s2.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, mine); err != nil {
		t.Fatal(err)
	}
	p2.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att2 := waitAttention(t, s2, asked)
	if att2.Message != approvalWords || len(att2.Options) != 3 {
		t.Fatalf("the question after a report with choices: %+v", att2)
	}
	if err := s2.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, mine); err != nil {
		t.Fatal(err)
	}
	if a := s2.Info().Attention; !asked(a) || *a.Since != *att2.Since {
		t.Fatalf("a report with choices hid the question: %+v", a)
	}

	s3, p3 := newLocalWith(t, quiet(Options{TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`), Questions: approvalQuestions()}))
	p3.outW.Write([]byte("\x1b[2;1HTrust this folder?"))
	waitAttention(t, s3, func(a Attention) bool { return a.Source == SourceTrust })
	p3.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(900 * time.Millisecond)
	if a := s3.Info().Attention; a.Source != SourceTrust {
		t.Fatalf("the question replaced a startup question: %+v", a)
	}
}

// Codex 0.161 advances queued approvals in place and writes only the cells
// that changed: the next approval, drawn after an answer, sends its command
// and its prefix alone. It is asked again, with its own command, since the
// session reads the screen, not the bytes since the answer.
func TestAQueuedApprovalDrawnAsWhatChangedIsAskedAgain(t *testing.T) {
	// The first answered by one of its choices, by a digit (Codex's list
	// picks on it at once), by Ctrl+C, or by a key the dialog takes that
	// Conductor does not know (d, n): the next is asked with its own words
	// and a Since of its own.
	for _, key := range []string{"y", "\x1b", "1", "3", "\x03", "d"} {
		s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
		sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		first := waitAttention(t, s, asked)
		s.Input(sub, []byte(key))
		nextWrite(t, p)
		p.outW.Write([]byte(approvalNext("rm -rf build")))
		next := waitAttention(t, s, func(a Attention) bool { return asked(a) && a.Message != approvalWords })
		if next.Message != "Would you like to run the following command? $ rm -rf build" || len(next.Options) != 3 || !next.Since.After(*first.Since) {
			t.Fatalf("%q, the queued approval: %+v", key, next)
		}
		if info := s.Info(); info.LastAnswer != nil {
			t.Fatalf("%q was recorded as an answer: %+v", key, info.LastAnswer)
		}
	}
}

// The screen is as tall as the window, whatever its size: a dialog drawn
// at the bottom of a 500-row window reads whole.
func TestAnApprovalAtTheBottomOfATallWindowIsAsked(t *testing.T) {
	p := newFakeProc()
	s := NewLocal(Info{ID: "tall", Cwd: t.TempDir(), Cols: 80, Rows: 500}, p, quiet(Options{Questions: approvalQuestions()}))
	t.Cleanup(p.exit)
	dialog := strings.NewReplacer("\x1b[12;", "\x1b[488;", "\x1b[13;", "\x1b[489;", "\x1b[15;", "\x1b[491;", "\x1b[17;", "\x1b[493;", "\x1b[19;", "\x1b[495;",
		"\x1b[20;", "\x1b[496;", "\x1b[21;", "\x1b[497;", "\x1b[22;", "\x1b[498;", "\x1b[23;", "\x1b[499;", "\x1b[24;", "\x1b[500;").Replace(approvalDialog("curl -sI https://example.com"))
	p.outW.Write([]byte(dialog))
	if a := waitAttention(t, s, asked); a.Message != approvalWords {
		t.Fatalf("attention %+v", a)
	}
}

// A program that never stops drawing (a spinner) runs out the wait before
// a submission's Enter; a dialog drawn as it runs out is still found, since
// the screen is looked at once more, quiet or not, before the Enter. The
// dialog is drawn as the submission comes to its Enter (the beforeEnter
// seam: after the wait, before that look), and the seam waits for the
// screen to show it; drawn by the clock, 300 ms before the wait's budget
// ran out, a loaded machine drew it after the look (#281).
func TestAnApprovalDrawnAsTheLookRunsOutHoldsTheEnter(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(40 * time.Millisecond):
			}
			p.outW.Write([]byte(fmt.Sprintf("\x1b[10;1H\x1b[K• Working (%d)", i)))
		}
	}()
	var stopOnce sync.Once
	stopSpinner := func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
	defer stopSpinner()
	var drawn sync.Once
	hook := func() {
		drawn.Do(func() {
			// The spinner ran the wait out; the dialog, on the screen before the look that follows.
			stopSpinner()
			p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(s.asked.Text(), "curl -sI https://example.com") {
				if time.Now().After(deadline) {
					t.Error("the dialog did not reach the screen within 5 s")
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
	s.beforeEnter.Store(&hook)
	time.Sleep(200 * time.Millisecond) // the spinner is drawing
	res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true})
	if err != nil || !res.Typed || res.Entered {
		t.Fatalf("result %+v %v", res, err)
	}
	if a := s.Info().Attention; !asked(a) || a.Message != approvalWords {
		t.Fatalf("attention %+v", a)
	}
}

// The dialog can go with a key Conductor does not know as an answer (d,
// which Codex shows on some of its dialogs; a click): once a person typed
// into the session while the question showed and the screen no longer
// shows it, the question gives way, answered by that person. With no key
// typed it stays: the dialog cannot have been answered, and the held
// question keeps every Enter off it.
func TestAnApprovalGoneAfterAKeyTypedIntoItGivesWay(t *testing.T) {
	// After a key the dialog names (d), a click, or nothing at all (Codex
	// moved on by itself): the dialog gone from the screen is withdrawn, no
	// answer recorded.
	for _, key := range []string{"d", "\x1b[<0;12;23M", ""} {
		s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
		sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
		waitAttention(t, s, asked)
		if key != "" {
			s.Input(sub, []byte(key))
			nextWrite(t, p)
		}
		if a := s.Info().Attention; !asked(a) {
			t.Fatalf("%q answered the question before the dialog went: %+v", key, a)
		}
		p.outW.Write([]byte(approvalGone("■ Conversation interrupted - tell the model what to do differently.\r\n› ")))
		a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
		if info := s.Info(); a.Source != SourceScreen || info.LastAnswer != nil {
			t.Fatalf("%q: %+v %+v", key, a, info.LastAnswer)
		}
	}
}

// The words are the command whole: a "$ " inside it does not start them
// again, its rows are kept as rows (what tells it from the next), and the
// message shown joins them; a command longer than a message can hold is
// shown cut with an ellipsis, never silently, and is told whole.
func TestAnApprovalsWordsAreItsWholeCommand(t *testing.T) {
	q := approvalQuestions()[0]
	screen := func(cmdRows ...string) string {
		return "  Would you like to run the following command?\n\n  Reason: needs the network.\n\n  " + strings.Join(cmdRows, "\n  ") +
			"\n\n› 1. Yes, proceed (y)\n  2. Yes, and don't ask again for commands that start with `rm` (p)\n  3. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel"
	}
	if got := shownWords(q.words(screen("$ rm -rf build; printf '$ done'"))); got != "Would you like to run the following command? $ rm -rf build; printf '$ done'" {
		t.Fatalf("words %q", got)
	}
	rows := q.words(screen("$ cat <<'EOF' > notes.txt", "hello", "EOF"))
	if rows != "Would you like to run the following command? $ cat <<'EOF' > notes.txt\nhello\nEOF" {
		t.Fatalf("rows of a command on three rows %q", rows)
	}
	if got := shownWords(rows); got != "Would you like to run the following command? $ cat <<'EOF' > notes.txt hello EOF" {
		t.Fatalf("words of a command on three rows %q", got)
	}
	long := "$ " + strings.Repeat("echo abcdefghij && ", 40) + "rm -rf /tmp/x"
	whole := q.words(screen(long))
	got := shownWords(whole)
	if len(got) > MaxAttentionMessage || !strings.HasSuffix(got, "…") || !strings.Contains(got, "$ echo abcdefghij && echo") || !utf8.ValidString(got) {
		t.Fatalf("words of a long command (%d bytes) %q", len(got), got)
	}
	// Alike up to the cut and different after it, two commands are two questions, shown alike.
	other := q.words(screen(strings.TrimSuffix(long, "x") + "y"))
	if shownWords(other) != got || sameWords(other, whole) || !strings.HasSuffix(whole, "/tmp/x") {
		t.Fatalf("two long commands that differ after the cut: %q and %q", whole, other)
	}
}

// Codex 0.161's other approvals: an accept-or-decline request ("No,
// continue without running it (d)", Escape only in the footer) and a
// network one ("Do you want to approve network access to …?", "Yes, just
// this once (y)"). Each is asked with the keys it shows, and neither text
// nor an Enter is typed into it.
func TestCodexsOtherApprovalsAreAsked(t *testing.T) {
	for _, tc := range []struct {
		name, screen, words string
		keys                []string
	}{
		{"deny", "\x1b[14;3HWould you like to run the following command?\x1b[16;3H$ git push --force\x1b[18;1H› 1. Yes, proceed (y)\x1b[19;3H2. No, continue without running it (d)\x1b[21;3HPress enter to confirm or esc to cancel",
			"Would you like to run the following command? $ git push --force", []string{"y", "d", "\x1b"}},
		{"network", "\x1b[14;3HDo you want to approve network access to \"example.com\"?\x1b[16;1H› 1. Yes, just this once (y)\x1b[17;3H2. Yes, and allow this host in the future (p)\x1b[18;3H3. No, and block this host in the future (d)\x1b[20;3HPress enter to confirm or esc to cancel",
			`Do you want to approve network access to "example.com"?`, []string{"y", "\x1b"}},
	} {
		s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
		sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
		if err != nil {
			t.Fatal(err)
		}
		p.outW.Write([]byte(tc.screen))
		a := waitAttention(t, s, asked)
		var keys []string
		for _, o := range a.Options {
			keys = append(keys, o.Input)
		}
		if a.Message != tc.words || strings.Join(keys, ",") != strings.Join(tc.keys, ",") {
			t.Fatalf("%s: %+v", tc.name, a)
		}
		if _, err := s.Submit(t.Context(), Submission{Text: "no, stop", By: sub}); !errors.Is(err, ErrQuestion) {
			t.Fatalf("%s: a reply: %v", tc.name, err)
		}
		noWrite(t, p, 100*time.Millisecond)
	}
}

// Conductor's own text that is no handoff, a crew's prompt, is refused like
// a person's at a dialog the screen shows: neither the text (the overlay
// drops a paste) nor its Enter (which picks Yes) is written.
func TestAPromptAtAnApprovalIsRefused(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	res, err := s.Submit(t.Context(), Submission{Text: "Plan it.", ByName: "crew", Confirm: true})
	if !errors.Is(err, ErrQuestion) || res.Typed {
		t.Fatalf("a prompt at the dialog: %+v %v", res, err)
	}
	noWrite(t, p, 200*time.Millisecond)
}

// A look before a submission that finds nothing leaves the quiet period's
// own look to come: a question a person's unknown key took off the screen
// gives way once the screen is quiet, though a handoff looked in between and
// no output follows.
func TestALookBeforeASubmissionLeavesTheQuietLook(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	s.Input(sub, []byte("n"))
	nextWrite(t, p)
	p.outW.Write([]byte(approvalGone("› ")))
	time.Sleep(50 * time.Millisecond)
	if res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true}); err != nil || res.Typed {
		t.Fatalf("the handoff: %+v %v", res, err)
	}
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
}

// A dialog drawn while the session shows a report with choices of its own,
// not yet seen quiet: a reply to the report, a crew's prompt and a handoff
// write nothing (the dialog would take the text, its Enter would pick Yes),
// and the look before them asks the dialog. A choice of the report clicked
// late, its keys with an Enter, is refused too.
func TestADialogOnTheScreenHoldsWhatWasMeantForAnotherQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	mine := []Option{{Label: "Postgres", Input: "Postgres\r"}, {Label: "SQLite", Input: "SQLite\r"}}
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, mine); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	if _, err := s.Submit(t.Context(), Submission{Text: "Postgres", By: sub}); !errors.Is(err, ErrQuestion) {
		t.Fatalf("a reply: %v", err)
	}
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("the look did not ask the dialog: %+v", a)
	}
	if _, err := s.Submit(t.Context(), Submission{Text: "Plan it.", ByName: "crew", Confirm: true}); !errors.Is(err, ErrQuestion) {
		t.Fatalf("a prompt: %v", err)
	}
	if res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true}); err != nil || res.Typed {
		t.Fatalf("a handoff: %+v %v", res, err)
	}
	if err := s.Input(sub, []byte(mine[0].Input)); !errors.Is(err, ErrQuestion) {
		t.Fatalf("the report's choice clicked late: %v", err)
	}
	noWrite(t, p, 200*time.Millisecond)
	// The dialog's own keys, and a bare Enter, still go.
	if err := s.Input(sub, []byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "\x1b[B" {
		t.Fatalf("written %q", got)
	}
}

// A choice of the question the session shows, clicked as a dialog is drawn
// and before the screen is seen quiet, is weighed against the dialog: the
// look before the input finds it, and the choice, the keys of another
// question with their Enter, is refused, nothing written.
func TestAChoiceClickedAsADialogIsDrawnIsRefused(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	mine := []Option{{Label: "Postgres", Input: "Postgres\r"}, {Label: "SQLite", Input: "SQLite\r"}}
	if err := s.TrySetAttentionFull(AttentionNeedsInput, "Which database?", SourceAPI, KindPrompt, mine); err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	if err := s.Input(sub, []byte(mine[1].Input)); !errors.Is(err, ErrQuestion) {
		t.Fatalf("the choice: %v", err)
	}
	noWrite(t, p, 200*time.Millisecond)
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("attention %+v", a)
	}
}

// Right after a key that may have done nothing (a digit past the dialog's
// lines), several keys with an Enter (a stale choice, a paste) are refused
// until the screen has been looked at again: they would land on the dialog
// still there.
func TestSeveralKeysAfterAKeyThatDidNothingAreRefused(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	s.Input(sub, []byte("9"))
	nextWrite(t, p)
	if err := s.Input(sub, []byte("SQLite\r")); !errors.Is(err, ErrQuestion) {
		t.Fatalf("several keys right after: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
	waitAttention(t, s, asked) // looked at again: the dialog is asked again
	if err := s.Input(sub, []byte("SQLite\r")); !errors.Is(err, ErrQuestion) {
		t.Fatalf("several keys at the dialog asked again: %v", err)
	}
	noWrite(t, p, 100*time.Millisecond)
}

// A key typed into the dialog before it is asked (the person was quicker
// than the quiet look) is noted against the dialog on the screen: when the
// program is slow to take the dialog away and the look asks it meanwhile,
// it gives way to that key once it goes.
func TestAKeyBeforeTheDialogIsAskedAnswersItWhenItGoesLate(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(100 * time.Millisecond) // read, not yet quiet
	s.Input(sub, []byte("\x1b"))
	nextWrite(t, p)
	waitAttention(t, s, asked) // the program has not drawn since: the dialog is asked
	p.outW.Write([]byte(approvalGone("■ Conversation interrupted\r\n› ")))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
		t.Fatalf("%+v %+v", a, s.Info().LastAnswer)
	}
}

// A key is noted against the dialog the screen shows as it is written, not
// the question the session still shows: a network approval answered with
// p (a key it shows but Conductor does not offer for it), the next dialog
// drawn and answered with Escape before it is asked, then, the program
// being slow, asked anyway and taken away: it gives way to the Escape.
func TestAKeyIsNotedAgainstTheDialogOnTheScreen(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[14;3HDo you want to approve network access to \"example.com\"?\x1b[16;1H› 1. Yes, just this once (y)\x1b[17;3H2. Yes, and allow this host in the future (p)\x1b[18;3H3. No, and block this host in the future (d)\x1b[20;3HPress enter to confirm or esc to cancel"))
	if a := waitAttention(t, s, asked); !strings.HasPrefix(a.Message, "Do you want to approve network access") {
		t.Fatalf("the network question %+v", a)
	}
	s.Input(sub, []byte("p"))
	nextWrite(t, p)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	time.Sleep(50 * time.Millisecond) // read, not yet quiet
	s.Input(sub, []byte("\x1b"))
	nextWrite(t, p)
	waitAttention(t, s, func(a Attention) bool { return asked(a) && a.Message == approvalWords })
	p.outW.Write([]byte(approvalGone("■ Conversation interrupted\r\n› ")))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
		t.Fatalf("%+v %+v", a, s.Info().LastAnswer)
	}
}

// A digit past the dialog's lines does nothing in Codex, and the dialog
// stays: a handoff that comes right after it waits for the screen to be
// looked at again, finds the dialog, and writes nothing.
func TestAHandoffAfterAKeyThatDidNothingWritesNothing(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h"))
	waitPaste(t, s, true)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	s.Input(sub, []byte("9"))
	nextWrite(t, p)
	res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true})
	if err != nil || res.Typed || res.Entered {
		t.Fatalf("the handoff: %+v %v", res, err)
	}
	noWrite(t, p, 200*time.Millisecond)
	if a := s.Info().Attention; !asked(a) {
		t.Fatalf("the dialog is not asked again: %+v", a)
	}
}

// A program slow to draw what follows an answer (past the quiet look, which
// asks the dialog still on the screen again) takes it away later: the
// question asked again gives way then, answered by the same key.
func TestAnAnswerTheProgramTakesLateLeavesNoQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	first := waitAttention(t, s, asked)
	s.Input(sub, []byte("y"))
	nextWrite(t, p)
	time.Sleep(2 * patternQuiet)
	if a := s.Info().Attention; !asked(a) || a.ID != first.ID {
		t.Fatalf("the dialog the program is slow to take away: %+v", a)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\n› ")))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen || s.Info().LastAnswer != nil {
		t.Fatalf("after the late answer %+v: %+v", a, s.Info().LastAnswer)
	}
}

// A handoff that comes as a person answers the dialog, before the program
// has drawn what follows, writes nothing (the dialog consumed still shows,
// and the look before a submission finds it until output comes); once the
// dialog is gone it is withdrawn, and the handoff is typed with its Enter,
// no question left held.
func TestAHandoffAsTheDialogIsAnsweredLeavesNoQuestion(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h"))
	waitPaste(t, s, true)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	waitAttention(t, s, asked)
	s.Input(sub, []byte("y"))
	nextWrite(t, p)
	if res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true}); err != nil || res.Typed {
		t.Fatalf("the handoff at the dialog consumed: %+v %v", res, err)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\n› ")))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
	res, err := s.Submit(t.Context(), Submission{Text: "over to you", ByName: "crew", UnlessWaiting: true})
	if err != nil || !res.Typed || !res.Entered {
		t.Fatalf("the handoff: %+v %v", res, err)
	}
	time.Sleep(900 * time.Millisecond)
	if a := s.Info().Attention; a.State == AttentionNeedsInput {
		t.Fatalf("a question left held: %+v", a)
	}
}

// A question's words are the same question whatever the width the screen
// broke them at: a row break reads as a space, also inside a word the
// program cut at the edge, and words cut to a message are told by the tag
// of the whole. Other words are another question.
func TestSameWordsIsTheQuestionWhateverTheWidth(t *testing.T) {
	q := approvalQuestions()[0]
	screen := func(cmdRows ...string) string {
		return "  Would you like to run the following command?\n\n  " + strings.Join(cmdRows, "\n  ") +
			"\n\n› 1. Yes, proceed (y)\n  2. Yes, and don't ask again for commands that start with `rm` (p)\n  3. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel"
	}
	short := "$ curl https://example.com/" + strings.Repeat("a", 60)
	if a, b := q.words(screen(short[:50], short[50:])), q.words(screen(short[:40], short[40:])); a == b || !sameWords(a, b) {
		t.Fatalf("one command broken at two widths: %q and %q", a, b)
	}
	long := "$ " + strings.Repeat("echo abcdefghij && ", 40) + "rm -rf /tmp/x"
	if a, b := q.words(screen(long[:300], long[300:])), q.words(screen(long[:290], long[290:])); !sameWords(a, b) {
		t.Fatalf("one long command broken at two widths: %q and %q", a, b)
	}
	if a, b := q.words(screen(long)), q.words(screen(strings.TrimSuffix(long, "x")+"y")); sameWords(a, b) {
		t.Fatalf("two long commands that differ after the cut: %q and %q", a, b)
	}
	if sameWords(q.words(screen("$ rm -rf build")), q.words(screen("$ rm -rf built"))) {
		t.Fatal("two commands are one question")
	}
	// A space within a row is the command's own: two commands that differ by one are two questions.
	if a, b := q.words(screen(`$ rm -rf "./build output"`)), q.words(screen(`$ rm -rf "./buildoutput"`)); sameWords(a, b) {
		t.Fatalf("two commands that differ by a space: %q and %q", a, b)
	}
	if a, b := q.words(screen(`$ rm -rf "./build output"`)), q.words(screen(`$ rm -rf "./build  output"`)); sameWords(a, b) {
		t.Fatalf("two commands that differ by one space and two: %q and %q", a, b)
	}
	// Broken between two words at one width and not at another, a command is one question.
	if a, b := q.words(screen(`$ rm -rf "./build`, `output"`)), q.words(screen(`$ rm -rf "./build output"`)); !sameWords(a, b) {
		t.Fatalf("one command broken between two words: %q and %q", a, b)
	}
	// What a command shows is never taken for a tag of anything: two commands alike but for one are two questions.
	if a, b := q.words(screen("$ echo harmless # … #deadbeef")), q.words(screen("$ rm -rf ./backups # … #deadbeef")); sameWords(a, b) {
		t.Fatalf("two commands that end alike: %q and %q", a, b)
	}
}

// The approval's dialog erased by a frame that ends on the alternate screen:
// the window made smaller with the dialog on it (the screen clipped until
// erased whole), then the program erasing the main screen and drawing it
// again without the dialog in a synchronized update, switching to the
// alternate screen, ending the update there and returning. The main screen
// reads settled, the question whose dialog the frame erased is withdrawn,
// and typed text is taken again (#226).
func TestAnApprovalErasedByAFrameEndedOnTheAlternateScreenIsWithdrawn(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{
		TrustPattern: regexp.MustCompile(`Trust\s*this\s*folder\?`),
		TrustAnswers: []Option{{Label: "Yes, trust this folder", Input: "\r"}},
		Questions:    approvalQuestions(),
	}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h› Ask Codex to do anything"))
	waitPaste(t, s, true)
	if res, err := s.Submit(t.Context(), Submission{Text: "check example.com", ByName: "crew"}); err != nil || !res.Entered {
		t.Fatalf("the first prompt: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, asked)
	if err := s.ResizeWith(sub, 78, 24, true); err != nil {
		t.Fatal(err)
	}
	if _, settled, _, _ := s.asked.Whole(); settled {
		t.Fatal("the screen made smaller with the dialog on it reads settled")
	}
	// One frame: the main screen erased and drawn again, the alternate
	// screen entered, the update ended there, the main screen back.
	p.outW.Write([]byte("\x1b[?2026h\x1b[2J\x1b[H✔ You approved codex to run curl -sI https://example.com this time\r\n› \x1b[?47h\x1b[?2026l\x1b[?47l"))
	if a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone }); a.Source != SourceScreen {
		t.Fatalf("the dialog withdrawn: %+v", a)
	}
	if a := s.Info().Attention; a.ID == att.ID {
		t.Fatalf("the question still stands: %+v", a)
	}
	if _, settled, _, _ := s.asked.Whole(); !settled {
		t.Fatal("the main screen, erased and drawn again by the frame, is not settled")
	}
	if res, err := s.Submit(t.Context(), Submission{Text: "thanks", By: sub}); err != nil || !res.Entered {
		t.Fatalf("typed text after the dialog went: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
}

// A startup question's words are read off the screen, where a heading drawn
// over the previous frame's cells reads whole; the output's tail holds it
// in pieces (#165). Codex's update question drawn over its splash is raised
// with its heading and its choices (the catalog offers the two skips, never
// Update now); a key typed at it is taken as its answer, and the question
// goes with the next screen.
func TestAStartupQuestionsWordsAreReadOffTheScreen(t *testing.T) {
	s, p := newLocalWith(t, quiet(Options{Questions: startupQuestions()}))
	sub, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "nater", Cols: 80, Rows: 24}, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(codexUpdateOverSplash))
	att := waitAttention(t, s, func(a Attention) bool { return a.Source == SourceTrust })
	if tail := s.trust.Text(); !strings.Contains(tail, "Updat available") || strings.Contains(tail, "Update available") {
		t.Fatalf("the output's tail reads %q", tail)
	}
	if !strings.HasPrefix(att.Message, "Update available") || !strings.Contains(att.Message, "0.163.0 → 0.164.0") {
		t.Fatalf("the question's words: %q", att.Message)
	}
	if len(att.Options) != 2 || att.Options[1].Label != "Skip until next version" {
		t.Fatalf("the question's choices: %+v", att.Options)
	}
	// A person's key typed at the dialog: the program's dialog has it, the
	// question is consumed by the screen's words too (holdStartupAnswered,
	// or it would stay and block typed text), and it is withdrawn once the
	// program draws the next screen; typed text is taken again then.
	if err := s.Input(sub, []byte("3")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "3" {
		t.Fatalf("written %q", got)
	}
	p.outW.Write([]byte("\x1b[2J\x1b[H› Ask Codex to do anything"))
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
	if res, err := s.Submit(t.Context(), Submission{Text: "hello", By: sub}); err != nil || !res.Entered {
		t.Fatalf("typed text after the question went: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
}

// A question answered by keys typed in its terminal reads answered in the
// terminal from that moment (#258): the session's info, the terminal's
// control channel and the change hook all say so at once, so that no page
// offers choices nobody can take while the dialog waits for the program to
// move on; a reply that still names it is refused as answered in the
// terminal; the screen moving on withdraws it as before, the mark gone.
func TestAQuestionAnsweredByKeysInTheTerminalReadsAnsweredAtOnce(t *testing.T) {
	changes := make(chan Info, 64)
	s, p := newLocalWith(t, quiet(Options{Questions: approvalQuestions(), OnChange: func(i Info) {
		select {
		case changes <- i:
		default:
		}
	}}))
	sink := newChanSink(false)
	sub, err := s.Attach("", RoleControl, "", 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte(approvalDialog("curl -sI https://example.com")))
	att := waitAttention(t, s, asked)
	if att.Answered != "" {
		t.Fatalf("asked, not answered: %+v", att)
	}
	if err := s.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if got := nextWrite(t, p); got != "y" {
		t.Fatalf("written %q", got)
	}
	if a := s.Info().Attention; !asked(a) || a.ID != att.ID || a.Answered != AnsweredTerminal {
		t.Fatalf("after the key: %+v", a)
	}
	if m := waitControl(sink, func(m map[string]any) bool {
		return m["t"] == "attention" && m["id"] == att.ID && m["answered"] == AnsweredTerminal
	}); m == nil {
		t.Fatal("the control channel never said the question was answered in the terminal")
	}
	deadline := time.After(3 * time.Second)
	for told := false; !told; {
		select {
		case i := <-changes:
			told = i.Attention.ID == att.ID && i.Attention.Answered == AnsweredTerminal
		case <-deadline:
			t.Fatal("the change hook never said the question was answered in the terminal")
		}
	}
	if err := s.AnswerInput(t.Context(), sub, att.ID, []byte("y")); !errors.Is(err, ErrQuestionAnswered) || err.Error() != QuestionConsumedWords {
		t.Fatalf("a reply after the key: %v", err)
	}
	p.outW.Write([]byte(approvalGone("✔ You approved codex to run curl -sI https://example.com this time\r\n› ")))
	a := waitAttention(t, s, func(a Attention) bool { return a.State == AttentionNone })
	if a.Source != SourceScreen || a.Answered != "" || s.Info().LastAnswer != nil {
		t.Fatalf("the dialog withdrawn: %+v, answered %+v", a, s.Info().LastAnswer)
	}
}

// stubApproval is the e2e stub's command approval as it draws it (web/e2e/stub-agent.sh,
// approval): Codex's bell first on a cleared screen, the title Action Required and the
// program status blocked with its kind after.
func stubApproval(cmd string) string {
	return "\a\x1b[2J\x1b[H  Would you like to run the following command?\r\n\r\n  Environment: local\r\n\r\n  Reason: May I run this with network access? The sandboxed attempt failed.\r\n\r\n  $ " + cmd +
		"\r\n\r\n› 1. Yes, proceed (y)\r\n  2. Yes, and don't ask again for commands that start with `" + cmd + "` (p)\r\n  3. No, and tell Codex what to do differently (esc)\r\n\r\n  Press enter to confirm or esc to cancel\r\n" +
		"\x1b]0;[ ! ] Action Required\a\x1b]7501;state=blocked:app=codex:kind=permission\x1b\\"
}

// The same, as the e2e stub plays it with Codex's titles and program status: the
// approval of a line typed (the title's frames while the stub works, stopped for the
// dialog), answered by a key, the turn ended (working with its frames, the dialog
// erased, the hook's done, the status done, Ready, the bell), and the same approval
// for the next line typed is a fresh question (the rc.7 approval flow, #258).
func TestTheSameApprovalInALaterTurnAsTheStubPlaysItIsAskedFresh(t *testing.T) {
	const cmd = "curl -sI https://example.com"
	s, p := newLocalWith(t, titled(100*time.Millisecond, Options{}))
	sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
	if err != nil {
		t.Fatal(err)
	}
	p.outW.Write([]byte("\x1b[?2004h› Ask anything "))
	waitPaste(t, s, true)
	working := func(d time.Duration) {
		out(p, "\x1b]7501;state=working:app=codex\x1b\\")
		stop := make(chan struct{})
		done := frames(p, stop)
		time.Sleep(d)
		close(stop)
		<-done
	}
	turn := func() Attention {
		if res, err := s.Submit(t.Context(), Submission{Text: "stub approve " + cmd, ByName: "crew"}); err != nil || !res.Entered {
			t.Fatalf("the line: %+v %v", res, err)
		}
		nextWrite(t, p)
		nextWrite(t, p)
		working(300 * time.Millisecond)
		out(p, stubApproval(cmd))
		return waitAttention(t, s, asked)
	}
	first := turn()
	if err := s.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	nextWrite(t, p)
	if a := s.Info().Attention; !asked(a) || a.ID != first.ID || a.Answered != AnsweredTerminal {
		t.Fatalf("after the key: %+v", a)
	}
	out(p, "\x1b[2J\x1b[H✔ You approved codex to run "+cmd+"\r\n")
	working(300 * time.Millisecond)
	// The page's reply bar goes with the question: its terminal grows by its rows, with the stub's words on the screen.
	if err := s.Resize(sub, 80, 30); err != nil {
		t.Fatal(err)
	}
	working(400 * time.Millisecond)
	// The turn's end: its hook reports done, then the stub's status, title and bell.
	s.SetAttention(AttentionDone, "", SourceAPI)
	out(p, "\x1b]7501;state=done:app=codex\x1b\\", "\x1b]0;Ready\a", "\a", "\r\n› ")
	waitAttention(t, s, func(a Attention) bool { return a.State == AttentionDone })
	time.Sleep(800 * time.Millisecond)
	// The next line: the stub's status blocked brings the reply bar back before the dialog is read, and the terminal shrinks
	// with the dialog on it.
	if res, err := s.Submit(t.Context(), Submission{Text: "stub approve " + cmd, ByName: "crew"}); err != nil || !res.Entered {
		t.Fatalf("the line: %+v %v", res, err)
	}
	nextWrite(t, p)
	nextWrite(t, p)
	working(300 * time.Millisecond)
	out(p, stubApproval(cmd))
	time.Sleep(50 * time.Millisecond)
	if err := s.Resize(sub, 80, 24); err != nil {
		t.Fatal(err)
	}
	again := waitAttention(t, s, asked)
	if again.ID == first.ID || again.Answered != "" {
		t.Fatalf("asked again as the one answered: %+v", again)
	}
	if err := s.AnswerInput(t.Context(), sub, again.ID, []byte("y")); err != nil {
		t.Fatalf("a reply to the fresh question: %v", err)
	}
	if got := nextWrite(t, p); got != "y" {
		t.Fatalf("written %q", got)
	}
}
