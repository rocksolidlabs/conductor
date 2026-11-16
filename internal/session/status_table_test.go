package session

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// statusTable is testdata/status-table.json: what a session shows after
// each row's steps, and the status key the app gives it (PR-3's
// sessionStatus reads the same file).
type statusTable struct {
	About string      `json:"about"`
	Rows  []statusRow `json:"rows"`
}

type statusRow struct {
	Name  string       `json:"name"`
	Steps []statusStep `json:"steps"`
	// Attention is what Info().Attention then holds; Message is compared
	// only when the row gives one.
	Attention struct {
		State   AttentionState `json:"state"`
		Source  string         `json:"source"`
		Message string         `json:"message,omitempty"`
	} `json:"attention"`
	Turns  bool   `json:"turns"`
	Status string `json:"status"`
}

// statusStep is one of: a report (an attention change from its source),
// an Enter typed by a viewer, the answer (a choice's input) to the
// question shown, a prompt a viewer types (its text, then its Enter, two
// writes as a terminal sends them) or a submission's (a page's, a crew's).
type statusStep struct {
	Report *struct {
		State   AttentionState `json:"state"`
		Source  string         `json:"source"`
		Message string         `json:"message,omitempty"`
		Kind    string         `json:"kind,omitempty"`
		Options []Option       `json:"options,omitempty"`
	} `json:"report,omitempty"`
	Enter  bool   `json:"enter,omitempty"`
	Answer string `json:"answer,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Submit string `json:"submit,omitempty"`
}

func readStatusTable(t *testing.T) statusTable {
	t.Helper()
	data, err := os.ReadFile("testdata/status-table.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var tab statusTable
	if err := dec.Decode(&tab); err != nil {
		t.Fatal(err)
	}
	if len(tab.Rows) == 0 {
		t.Fatal("the table has no rows")
	}
	return tab
}

// TestTheStatusTable drives a session through each row of the table both
// sides are held to and compares what it shows: the attention's state and
// source (its words when the row gives them) and Info.Turns. A report goes
// in by its source's own way: a bell rung on the terminal, a window title
// by the title's rules, a hook's or an operator's through the API.
func TestTheStatusTable(t *testing.T) {
	for _, row := range readStatusTable(t).Rows {
		t.Run(row.Name, func(t *testing.T) {
			s, p := newLocalWith(t, quiet(Options{}))
			sub, err := s.Attach("", RoleControl, "", 80, 24, newChanSink(false))
			if err != nil {
				t.Fatal(err)
			}
			for i, step := range row.Steps {
				switch r := step.Report; {
				case r != nil && r.Source == SourceBell:
					p.outW.Write([]byte("\a"))
					waitAttention(t, s, isBell)
				case r != nil && (r.Source == SourceTitle || r.Source == SourceStatus):
					// A program status's working is weighed as a title's (readStatus).
					if err := s.setAttention(r.State, r.Message, r.Source, r.Kind, r.Options, titleRules); err != nil {
						t.Fatal(err)
					}
				case r != nil:
					if err := s.TrySetAttentionFull(r.State, r.Message, r.Source, r.Kind, r.Options); err != nil {
						t.Fatal(err)
					}
				case step.Enter:
					if err := s.Input(sub, []byte("\r")); err != nil {
						t.Fatal(err)
					}
					nextWrite(t, p)
				case step.Prompt != "":
					for _, keys := range []string{step.Prompt, "\r"} {
						if err := s.Input(sub, []byte(keys)); err != nil {
							t.Fatal(err)
						}
						nextWrite(t, p)
					}
				case step.Submit != "":
					if res, err := s.Submit(t.Context(), Submission{Text: step.Submit, ByName: "crew"}); err != nil || !res.Entered {
						t.Fatalf("step %d: %+v %v", i, res, err)
					}
					nextWrite(t, p)
					nextWrite(t, p)
				case step.Answer != "":
					q := s.Info().Attention
					if err := s.AnswerInput(t.Context(), sub, q.ID, []byte(step.Answer)); err != nil {
						t.Fatal(err)
					}
					nextWrite(t, p)
					waitAttention(t, s, func(a Attention) bool { return a.ID != q.ID })
				default:
					t.Fatalf("step %d does nothing", i)
				}
			}
			info := s.Info()
			want := row.Attention
			if a := info.Attention; a.State != want.State || a.Source != want.Source || want.Message != "" && a.Message != want.Message {
				t.Errorf("attention %q from %q (%q), want %q from %q (%q)", a.State, a.Source, a.Message, want.State, want.Source, want.Message)
			}
			if info.Turns != row.Turns {
				t.Errorf("turns %v, want %v", info.Turns, row.Turns)
			}
		})
	}
}
