package api

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The ticket list's external search, the admin's: an imported ticket is
// found by its linked item's page (a pasted link's fragment left behind),
// a ticket by a live link's URL, and nothing else; an address of
// dispatch.MaxLinkURL bytes is taken and one byte more is 400
// invalid_request; a Brakeman turn's shared read of the list does not
// take the parameter, though it reads the list. The app's copy of the
// bound (web/app/utils/protocol.ts) is the store's.
func TestTheTicketListSearchesAnExternalAddress(t *testing.T) {
	f := newActFixture(t, []string{brakeman.ActPrepareFollowup}, nil)
	key, _, _, _ := sourceTicket(t, f.launchTest)
	list := func(external string, status int) []string {
		t.Helper()
		out := f.expect(t, "GET", "/api/dispatch/tickets?external="+url.QueryEscape(external), nil, status, "")
		var keys []string
		if status == http.StatusOK {
			for _, x := range out["tickets"].([]any) {
				keys = append(keys, x.(map[string]any)["key"].(string))
			}
		}
		return keys
	}
	if got := list("https://github.com/octo/app/issues/41#issuecomment-1", http.StatusOK); len(got) != 1 || got[0] != key {
		t.Fatalf("the linked item's page found %q", got)
	}
	if got := list("https://example.com/trace/7", http.StatusOK); len(got) != 1 || got[0] != key {
		t.Fatalf("a link's URL found %q", got)
	}
	if got := list("https://github.com/octo/app/issues/42", http.StatusOK); len(got) != 0 {
		t.Fatalf("another item found %q", got)
	}
	long := "https://github.com/octo/app/issues/41?" + strings.Repeat("a", dispatch.MaxLinkURL)
	list(long[:dispatch.MaxLinkURL], http.StatusOK)
	f.expect(t, "GET", "/api/dispatch/tickets?external="+url.QueryEscape(long[:dispatch.MaxLinkURL+1]), nil, http.StatusBadRequest, "invalid_request")

	if status, out := f.asTurn(t, http.MethodGet, "/api/dispatch/tickets", nil); status != http.StatusOK {
		t.Fatalf("the turn's read of the list: %d %v", status, out)
	}
	if status, out := f.asTurn(t, http.MethodGet, "/api/dispatch/tickets?external="+url.QueryEscape("https://github.com/octo/app/issues/41"), nil); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
		t.Fatalf("a turn's search by an address: %d %v", status, out)
	}

	src, err := os.ReadFile("../../web/app/utils/protocol.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^export const MAX_TICKET_EXTERNAL_SEARCH = (\d+)$`).FindSubmatch(src)
	if m == nil || string(m[1]) != strconv.Itoa(dispatch.MaxLinkURL) {
		t.Fatalf("protocol.ts's MAX_TICKET_EXTERNAL_SEARCH is not %d: %q", dispatch.MaxLinkURL, m)
	}
}
