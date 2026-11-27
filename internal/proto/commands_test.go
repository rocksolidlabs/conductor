package proto

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// largestCommands is a commands message at every bound: MaxCommands
// commands, each name, alias, word and hint at its bound, every byte of the
// words and the hint one that JSON writes as two.
func largestCommands() TranscriptCommandsMsg {
	m := TranscriptCommandsMsg{T: TranscriptCommandsT, Rev: ^uint64(0), More: true}
	for i := range MaxCommands {
		m.Commands = append(m.Commands, Command{
			Name:    fmt.Sprintf("%0*d", MaxCommandName, i),
			Words:   strings.Repeat(`"`, MaxCommandWords),
			Hint:    strings.Repeat(`\`, MaxCommandHint),
			Aliases: []string{fmt.Sprintf("a%0*d", MaxCommandAlias-1, i), fmt.Sprintf("b%0*d", MaxCommandAlias-1, i)},
		})
	}
	return m
}

// The commands message at every bound is the committed fixture and fits a
// TRANSCRIPT frame; one command more, a name or an alias past its bound or
// of another shape, a name twice, words or a hint past theirs or holding a
// control character, and a field it does not know are refused. Run with
// CONDUCTOR_WRITE_FIXTURE=1 to write the fixture after a bound changes.
func TestCommandsFrameBounds(t *testing.T) {
	frame, err := EncodeCommands(largestCommands())
	if err != nil {
		t.Fatalf("the largest commands message: %v", err)
	}
	f, err := Decode(frame)
	if err != nil || f.Type != TypeTranscript || len(f.Payload) > MaxTranscript {
		t.Fatalf("its frame: %v", err)
	}
	const fixture = "testdata/commands-max.json"
	if os.Getenv("CONDUCTOR_WRITE_FIXTURE") == "1" {
		if err := os.WriteFile(fixture, append(bytes.Clone(f.Payload), '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(raw, "\n"), f.Payload) {
		t.Fatalf("the fixture is not the largest message (%d bytes, the fixture %d): run with CONDUCTOR_WRITE_FIXTURE=1", len(f.Payload), len(raw))
	}
	back, err := DecodeCommands(f.Payload)
	if err != nil || len(back.Commands) != MaxCommands || !back.More {
		t.Fatalf("decoded %d commands: %v", len(back.Commands), err)
	}

	for name, change := range map[string]func(*TranscriptCommandsMsg){
		"one command more":      func(m *TranscriptCommandsMsg) { m.Commands = append(m.Commands, Command{Name: "extra"}) },
		"a name past its bound": func(m *TranscriptCommandsMsg) { m.Commands[0].Name += "x" },
		"a name with a space":   func(m *TranscriptCommandsMsg) { m.Commands[0].Name = "fix issue" },
		"a name with an at":     func(m *TranscriptCommandsMsg) { m.Commands[0].Name = "fix@issue" },
		"a name with a slash":   func(m *TranscriptCommandsMsg) { m.Commands[0].Name = "a/b" },
		"a name beginning with a dash": func(m *TranscriptCommandsMsg) {
			m.Commands[0].Name = "-x"
		},
		"an empty name":           func(m *TranscriptCommandsMsg) { m.Commands[0].Name = "" },
		"a name with a bidi mark": func(m *TranscriptCommandsMsg) { m.Commands[0].Name = "fix" + string(rune(0x202E)) + "issue" },
		"a name twice":            func(m *TranscriptCommandsMsg) { m.Commands[1].Name = m.Commands[0].Name },
		"three aliases":           func(m *TranscriptCommandsMsg) { m.Commands[0].Aliases = append(m.Commands[0].Aliases, "c") },
		"an alias past its bound": func(m *TranscriptCommandsMsg) {
			m.Commands[0].Aliases[0] += "x"
		},
		"an alias with a space":  func(m *TranscriptCommandsMsg) { m.Commands[0].Aliases[0] = "a b" },
		"words past their bound": func(m *TranscriptCommandsMsg) { m.Commands[0].Words += "x" },
		"a hint past its bound":  func(m *TranscriptCommandsMsg) { m.Commands[0].Hint += "x" },
		"words with a line break": func(m *TranscriptCommandsMsg) {
			m.Commands[0].Words = "one\ntwo"
		},
		"a hint with an escape": func(m *TranscriptCommandsMsg) { m.Commands[0].Hint = "\x1b[31m" },
		"words not UTF-8":       func(m *TranscriptCommandsMsg) { m.Commands[0].Words = "\xff" },
	} {
		m := largestCommands()
		change(&m)
		if m.Valid() {
			t.Errorf("%s: valid", name)
		}
		if _, err := EncodeCommands(m); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
	for name, payload := range map[string]string{
		"a field it does not know": `{"t":"commands","rev":1,"commands":[],"account":{}}`,
		"a command's field":        `{"t":"commands","rev":1,"commands":[{"name":"x","description":"y"}]}`,
		"another t":                `{"t":"entry","rev":1,"commands":[]}`,
		"more after it":            `{"t":"commands","rev":1,"commands":[]}]`,
	} {
		if _, err := DecodeCommands([]byte(payload)); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	if _, err := DecodeCommands([]byte(`{"t":"commands","rev":1,"commands":[{"name":"compact"},{"name":"fix-issue","words":"Fix an issue.","hint":"<number>","aliases":["fi"]}]}`)); err != nil {
		t.Errorf("a plain message: %v", err)
	}
	if b, _ := EncodeCommands(TranscriptCommandsMsg{Rev: 1}); string(b[1:]) != `{"t":"commands","rev":1,"commands":[]}` {
		t.Errorf("an empty list: %s", b[1:])
	}
}
