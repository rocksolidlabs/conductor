package hostagent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
)

// yardJudge is a switchyard's identity provider that admits the "cred-"
// tokens as a host's, and, while out is set, cannot tell: an error.
type yardJudge struct{ out atomic.Bool }

func (*yardJudge) Name() string { return "cloud" }
func (y *yardJudge) Authenticate(_ context.Context, tok string) (ext.Principal, bool, error) {
	if y.out.Load() {
		return ext.Principal{}, false, errors.New("its keys are out of reach")
	}
	return ext.Principal{Subject: "u-ada", Name: "Ada", Grants: []ext.Grant{ext.GrantHost}}, strings.HasPrefix(tok, "cred-"), nil
}

// A registration the switchyard answers 503 not_checked, its identity
// provider not having judged the credential, is a refusal that passes with
// time, tried again, and never a credential withheld; the same
// registration once the provider judges it registers. A 401 is what it
// was: not tried again, and owed only when the credential was withheld.
func TestRegistrationNotCheckedIsTriedAgain(t *testing.T) {
	y := &yardJudge{}
	y.out.Store(true)
	url, auths := yardRecording(t, false, y)
	l := catLocal(t)
	u := &Uplink{ServerURL: url, Token: "cred-invented-1", HostName: "office", Log: discardLog}
	_, err := u.Publish(t.Context(), l)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Status != http.StatusServiceUnavailable || refused.Code != "not_checked" || refused.CredentialWithheld() {
		t.Fatalf("a registration not judged: %v", err)
	}
	if again, _ := refused.Retryable(); !again {
		t.Fatal("a 503 is not tried again")
	}
	y.out.Store(false)
	pub, err := u.Publish(t.Context(), l)
	if err != nil {
		t.Fatalf("the same registration once judged: %v", err)
	}
	pub.Stop()
	if got := auths(); len(got) != 2 || got[0] != "Bearer cred-invented-1" || got[1] != got[0] {
		t.Fatalf("the registrations presented %d headers", len(got))
	}
	u.Token = "nobody-invented-1"
	_, err = u.Publish(t.Context(), l)
	if !errors.As(err, &refused) || refused.Status != http.StatusUnauthorized || refused.CredentialWithheld() {
		t.Fatalf("a token nobody knows: %v", err)
	}
	if again, _ := refused.Retryable(); again {
		t.Fatal("a 401 is tried again")
	}
	// Owed for a withheld credential: a 401's alone.
	if (&RefusedError{Status: http.StatusServiceUnavailable, Withheld: true}).CredentialWithheld() {
		t.Fatal("a 503 is owed as a withheld credential")
	}
	if !(&RefusedError{Status: http.StatusUnauthorized, Withheld: true}).CredentialWithheld() {
		t.Fatal("a withheld credential's 401 is not owed")
	}
}
