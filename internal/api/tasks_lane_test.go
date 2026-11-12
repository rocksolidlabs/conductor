package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The message route answers the engine's lane refusal 409 lane, with its
// words; the engine writes nothing for it (internal/crew,
// TestMessagesStayInTheirLane). A command a comparison does not take is a
// task_state refusal.
func TestALaneRefusalIsAnswered409Lane(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: cand-b is in another lane of this comparison: a message stays in its lane", crew.ErrLane), http.StatusConflict, "lane"},
		{crew.ErrNotSelected, http.StatusConflict, "task_state"},
	} {
		w := httptest.NewRecorder()
		(&Server{}).taskError(w, "message", "s-1", tc.err)
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || body.Error.Code != tc.code || !strings.Contains(body.Error.Message, tc.err.Error()) {
			t.Fatalf("%v: %d %+v", tc.err, w.Code, body)
		}
	}
}
