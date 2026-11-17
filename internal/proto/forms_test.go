package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// forms on a host's register (#970): said, it is one bool on the wire; not
// said (an earlier release's host), it is absent; a register that says it
// is read by a server of an earlier release, whose HostInfo has no such
// field, as if it had not, and a register without it is read here as not
// saying it. A bool: the register's bound (MaxHostMessage) does not change.
func TestRegisterForms(t *testing.T) {
	b, _ := json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", AnswerAs: true, Forms: true}})
	if !strings.Contains(string(b), `"answerAs":true,"forms":true}`) {
		t.Fatalf("register %s", b)
	}
	b, _ = json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", AnswerAs: true}})
	if strings.Contains(string(b), "forms") {
		t.Fatalf("a register that reads no forms %s", b)
	}
	var reg Register
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","forms":true},"session":{}}`), &reg); err != nil || !reg.Host.Forms {
		t.Fatalf("from the wire %v %+v", err, reg)
	}
	reg = Register{}
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","answerAs":true},"session":{}}`), &reg); err != nil || reg.Host.Forms || !reg.Host.AnswerAs {
		t.Fatalf("an earlier release's register %v %+v", err, reg)
	}
	// A server of an earlier release decodes the register as ws_host.go
	// does (json.Unmarshal), into a HostInfo without the field.
	var older struct {
		T    string `json:"t"`
		Host struct {
			Name     string `json:"name"`
			AnswerAs bool   `json:"answerAs,omitempty"`
		} `json:"host"`
	}
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","answerAs":true,"forms":true},"session":{}}`), &older); err != nil || older.T != HostRegister || older.Host.Name != "laptop" || !older.Host.AnswerAs {
		t.Fatalf("an earlier release's server %v %+v", err, older)
	}
}
