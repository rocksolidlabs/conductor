package proto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A machine key is taken in its form and refuses the register one past it
// or out of it, whole: never cut down. A platform (and a version) is taken
// at its bound and in its form, and one past it or out of it is only not
// kept: the register stands, so a later release that widens the form still
// connects.
func TestRegisterHostMachineAndPlatformBounds(t *testing.T) {
	key := strings.Repeat("0123456789abcdef", 2)
	ok := []HostInfo{
		{},
		{Machine: key},
		{Platform: "linux/amd64"},
		{Platform: "linux/amd64+wsl"},
		{Platform: "darwin/arm64"},
		// Each part at its 15, and the whole at its 32 bytes.
		{Platform: strings.Repeat("a", 15) + "/" + strings.Repeat("b", 15)},
		{Platform: strings.Repeat("a", 12) + "/" + strings.Repeat("b", 15) + "+wsl"},
		{Machine: key, Platform: "windows/amd64"},
	}
	for _, h := range ok {
		if err := h.CheckMachine(); err != nil {
			t.Errorf("%+v refused: %v", h, err)
		}
	}
	bad := map[string]HostInfo{
		"a machine one short":          {Machine: key[:31]},
		"a machine one past":           {Machine: key + "0"},
		"a machine in upper case":      {Machine: strings.ToUpper(key)},
		"a machine not hex":            {Machine: key[:31] + "g"},
		"a machine with a newline":     {Machine: key[:31] + "\n"},
		"a machine of 64 digits":       {Machine: key + key},
		"a bad machine, good platform": {Machine: key + "0", Platform: "linux/amd64"},
	}
	for name, h := range bad {
		if err := h.CheckMachine(); !errors.Is(err, ErrHostMachine) {
			t.Errorf("%s: %v, want ErrHostMachine", name, err)
		}
	}
	for _, h := range ok {
		if h.Platform != "" && !ValidHostPlatform(h.Platform) {
			t.Errorf("platform %q not kept", h.Platform)
		}
	}
	// A platform out of its form is not kept, and does not refuse the
	// register.
	for name, p := range map[string]string{
		"one past its bound": strings.Repeat("a", 13) + "/" + strings.Repeat("b", 15) + "+wsl",
		"a part too long":    strings.Repeat("a", 16) + "/amd64",
		"no arch":            "linux/",
		"no slash":           "linux",
		"upper case":         "Linux/amd64",
		"another suffix":     "linux/amd64+mac",
		"a space":            "linux/amd64 ",
		"a newline":          "linux/amd64\n",
		"two slashes":        "linux/amd64/x",
		"markup":             "<b>",
		"far past its bound": strings.Repeat("a", 4096),
	} {
		if ValidHostPlatform(p) {
			t.Errorf("platform %s (%q) kept", name, p)
		}
		if err := (HostInfo{Machine: key, Platform: p}).CheckMachine(); err != nil {
			t.Errorf("platform %s refused the register: %v", name, err)
		}
	}

	// The version a server keeps: at its bound and in its form, or none.
	for _, v := range []string{"dev", "v1.2.3", "v0.1.0-rc.4-12-gabcdef0-dirty", "1.0.0+build_7", strings.Repeat("v", MaxHostVersion)} {
		if !ValidHostVersion(v) {
			t.Errorf("version %q not kept", v)
		}
	}
	for _, v := range []string{"", strings.Repeat("v", MaxHostVersion+1), "-v1", ".v1", "v1 2", "v1\n", "v1/2", "<script>", "v1é"} {
		if ValidHostVersion(v) {
			t.Errorf("version %q kept", v)
		}
	}
	// A version is not the register's to refuse: an older host's of any
	// form still registers.
	if err := (HostInfo{Version: "anything at all / of any form"}).CheckMachine(); err != nil {
		t.Errorf("a version out of its form refused the register: %v", err)
	}

	// A register at the bounds fits a host message.
	reg := Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: strings.Repeat("n", 120), Version: strings.Repeat("v", MaxHostVersion), Machine: key, Platform: strings.Repeat("a", 15) + "/" + strings.Repeat("b", 15)}}
	if b, _ := json.Marshal(reg); len(b) > MaxHostMessage {
		t.Errorf("a register at the bounds is %d bytes", len(b))
	}

	// A desk's register is held to the same forms.
	desk := RegisterDesk{T: HostRegisterDesk, Proto: ProtoVersion, Host: HostInfo{Instance: "i"}, LocalID: DeskLocalID("r1"), Run: DeskRun{ID: "r1", Name: "run"}}
	if err := desk.Validate(); err != nil {
		t.Fatalf("a desk: %v", err)
	}
	desk.Host.Machine = key + "0"
	if err := desk.Validate(); !errors.Is(err, ErrHostMachine) {
		t.Errorf("a desk with a machine past its bound: %v", err)
	}
	desk.Host.Machine, desk.Host.Platform = "", "linux"
	if err := desk.Validate(); err != nil {
		t.Errorf("a desk with a platform out of its form refused: %v", err)
	}
}

// The server's key in the upgrade's header is taken only as exactly 64
// lowercase hex digits; anything else is no key.
func TestParseServerKey(t *testing.T) {
	good := strings.Repeat("0123456789abcdef", 4)
	if k := ParseServerKey(good); len(k) != ServerKeyLen || k[0] != 0x01 || k[31] != 0xef {
		t.Fatalf("a good key: %x", k)
	}
	for _, v := range []string{"", good[:63], good + "0", strings.ToUpper(good), good[:63] + "g", " " + good[1:], good[:63] + "\n", good + good} {
		if k := ParseServerKey(v); k != nil {
			t.Errorf("%q gave a key: %x", v, k)
		}
	}
}

// An older host's register, which says nothing of its machine, is encoded
// byte for byte as before: neither new field appears when empty.
func TestHostInfoOmitsEmpty(t *testing.T) {
	reg := Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "box", Version: "v1", User: "u", Instance: "i"}, Session: HostSession{Name: "s", AgentID: "a", Command: []string{"a"}, Cwd: "/w", Cols: 80, Rows: 24, LocalID: "1"}}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"t":"register","proto":1,"host":{"name":"box","version":"v1","user":"u","instance":"i"},"session":{"name":"s","agentId":"a","command":["a"],"cwd":"/w","cols":80,"rows":24,"localId":"1"}}`
	if string(b) != want {
		t.Fatalf("an older host's register:\n got %s\nwant %s", b, want)
	}
	reg.Host.Machine, reg.Host.Platform = strings.Repeat("a", MaxHostMachine), "linux/amd64"
	b, _ = json.Marshal(reg)
	if !strings.Contains(string(b), `"machine":"`+strings.Repeat("a", MaxHostMachine)+`","platform":"linux/amd64"`) {
		t.Fatalf("a register with both: %s", b)
	}
}
