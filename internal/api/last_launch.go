package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"
)

// The last launch (UX3 R89): what Launch agent, the welcome and New agent
// trial read to start where the person left off, after a restart of the
// server too, which the app's own storage does not outlast (the desktop
// app's server takes a fresh port at each start, and what a page keeps
// goes with its origin). The server keeps it itself, as launch/last.json
// in the data directory: a document of its own, not the layout document,
// which a sync provider may carry to other machines (the sync seam
// delivers an allowlisted set of documents, and this is not one), since a
// folder's path stays on this machine.
//
// A launch on this server that started records it: POST /api/sessions
// alone (a crew's members, a resume, a clone and a hosted session do
// not). It holds the agent launched (never the built-in shell, which is
// no agent to start with), the folder as the launch named it ("" for the
// server's default), and for each agent its last model, the model names
// it was last launched with, newest first, and the way it was launched
// (its terminal or Chat): never the arguments (they can hold a key), the
// task, the name, the account or yolo. A new agent trial the server took
// (POST /api/crews/templates/compare) records its agents, its folder and,
// for each of the last folders, its Check line's words: never the task,
// what the agents were told, their arguments or approaches. And the note
// on the vendor's terms in Chat, once drawn, is marked shown
// (POST /api/launch/chat-noted). GET /api/launch/last answers it,
// {"v":1,"models":{}} when nothing was kept yet or the record does not
// read.

// The record's bounds.
const (
	lastLaunchDoc    = "last.json"
	lastLaunchAgents = 32
	lastLaunchNames  = 5
	// lastLaunchText bounds a folder kept: one longer is kept as none.
	lastLaunchText = 4096
	// lastLaunchModelLen bounds a model name kept: one longer is not.
	lastLaunchModelLen = 256
	// lastTrialAgents is a trial's most candidates.
	lastTrialAgents = 4
	// lastTrialFolders bounds the folders whose Check line is kept, the
	// most recent first.
	lastTrialFolders = 20
	// lastTrialFolderLen bounds a trial's folder kept; lastTrialCheckLen
	// bounds its Check line, its words together: one past it is kept as
	// none.
	lastTrialFolderLen = 1024
	lastTrialCheckLen  = 1024
	// lastTrialCheckID is the id the trial form sends its Check line
	// under (compareTemplate.ts CHECK_ID).
	lastTrialCheckID = "check"
	maxLastLaunch    = 128 << 10
	// maxChatNotedBody bounds POST /api/launch/chat-noted's body, `{}`.
	maxChatNotedBody = 64
	// lastLaunchShell is the built-in shell's id (catalog defaults).
	lastLaunchShell = "shell"
	// The ways a launch is kept as.
	lastWayTerminal = "terminal"
	lastWayProtocol = "protocol"
)

// lastLaunchModel is what an agent was last launched with.
type lastLaunchModel struct {
	Model string   `json:"model"`
	Names []string `json:"names"`
	// Way is the way it was last launched: "terminal" or "protocol" (as
	// a chat); none for a record written before ways were kept.
	Way string `json:"way,omitempty"`
}

// lastTrialCheck is a folder's last Check line, as its words.
type lastTrialCheck struct {
	Folder string   `json:"folder"`
	Check  []string `json:"check"`
}

// lastTrial is the last new agent trial the server took.
type lastTrial struct {
	Agents []string         `json:"agents"`
	Folder string           `json:"folder"`
	Checks []lastTrialCheck `json:"checks"`
}

// lastLaunch is the record of the last launch.
type lastLaunch struct {
	V      int                        `json:"v"`
	Agent  string                     `json:"agent,omitempty"`
	Folder string                     `json:"folder,omitempty"`
	Models map[string]lastLaunchModel `json:"models"`
	// Order is the agents of Models, the one launched longest ago first:
	// the first let go past lastLaunchAgents.
	Order []string `json:"order,omitempty"`
	// Trial is the last new agent trial; none before one.
	Trial *lastTrial `json:"trial,omitempty"`
	// ChatNoted is whether the note on the vendor's terms in Chat was
	// drawn once.
	ChatNoted bool `json:"chatNoted,omitempty"`
}

// emptyLastLaunch is the record before any launch.
func emptyLastLaunch() lastLaunch { return lastLaunch{V: 1, Models: map[string]lastLaunchModel{}} }

// checkLen is the length of a Check line's words together.
func checkLen(argv []string) int {
	n := 0
	for _, a := range argv {
		n += len(a) + 1
	}
	return n
}

// sound reports whether d is a record this server writes: a broken one
// is read as none, and the next launch writes a whole one over it.
func (d lastLaunch) sound() bool {
	if d.V != 1 || len(d.Models) > lastLaunchAgents || len(d.Order) != len(d.Models) {
		return false
	}
	for _, id := range d.Order {
		m, ok := d.Models[id]
		if !ok || len(m.Names) > lastLaunchNames || (m.Way != "" && m.Way != lastWayTerminal && m.Way != lastWayProtocol) {
			return false
		}
	}
	if t := d.Trial; t != nil {
		if len(t.Agents) > lastTrialAgents || len(t.Folder) > lastTrialFolderLen || len(t.Checks) > lastTrialFolders {
			return false
		}
		for _, c := range t.Checks {
			if len(c.Folder) > lastTrialFolderLen || checkLen(c.Check) > lastTrialCheckLen {
				return false
			}
		}
	}
	return true
}

// keptWay is the way a launch is kept as: Chat, else its terminal.
func keptWay(way string) string {
	if way == lastWayProtocol {
		return lastWayProtocol
	}
	return lastWayTerminal
}

// after is d with the launch of agent in folder with model, the way way.
func (d lastLaunch) after(agent, folder, model, way string) lastLaunch {
	model = strings.TrimSpace(model)
	if len(model) > lastLaunchModelLen {
		model = ""
	}
	if len(folder) > lastLaunchText || !utf8.ValidString(folder) {
		folder = ""
	}
	next := lastLaunch{V: 1, Agent: d.Agent, Folder: folder, Models: map[string]lastLaunchModel{}, Trial: d.Trial, ChatNoted: d.ChatNoted}
	if agent != lastLaunchShell {
		next.Agent = agent
	}
	for _, id := range d.Order {
		if id != agent {
			next.Order = append(next.Order, id)
			next.Models[id] = d.Models[id]
		}
	}
	names := d.Models[agent].Names
	if model != "" {
		kept := []string{model}
		for _, n := range names {
			if n != model && len(kept) < lastLaunchNames {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	if names == nil {
		names = []string{}
	}
	next.Order = append(next.Order, agent)
	next.Models[agent] = lastLaunchModel{Model: model, Names: names, Way: keptWay(way)}
	for len(next.Order) > lastLaunchAgents {
		delete(next.Models, next.Order[0])
		next.Order = next.Order[1:]
	}
	return next
}

// afterTrial is d with a trial of agents in folder, its Check line check
// (its words; none for no line): the agents and the folder, and the line
// first among the folders' lines. A folder or a line past its bound is
// kept as none.
func (d lastLaunch) afterTrial(agents []string, folder string, check []string) lastLaunch {
	folder = strings.TrimSpace(folder)
	if len(folder) > lastTrialFolderLen || !utf8.ValidString(folder) {
		folder = ""
	}
	if checkLen(check) > lastTrialCheckLen {
		check = nil
	}
	if check == nil {
		check = []string{}
	}
	t := &lastTrial{Agents: []string{}, Folder: folder, Checks: []lastTrialCheck{}}
	for _, a := range agents {
		if a != "" && len(t.Agents) < lastTrialAgents {
			t.Agents = append(t.Agents, a)
		}
	}
	if folder != "" {
		t.Checks = append(t.Checks, lastTrialCheck{Folder: folder, Check: append([]string(nil), check...)})
	}
	if d.Trial != nil {
		for _, c := range d.Trial.Checks {
			if c.Folder != folder && len(t.Checks) < lastTrialFolders {
				t.Checks = append(t.Checks, c)
			}
		}
	}
	d.Trial = t
	return d
}

// readLastLaunch reads the record; the caller holds lastLaunchMu. A
// record that is missing, past its bound or broken is the empty one.
func (s *Server) readLastLaunch() lastLaunch {
	if s.store == nil {
		return emptyLastLaunch()
	}
	st, err := s.store.Sub("launch")
	if err != nil {
		s.log.Warn("launch: the last launch's folder does not open", "err", err)
		return emptyLastLaunch()
	}
	var d lastLaunch
	ok, err := st.LoadLimit(lastLaunchDoc, &d, maxLastLaunch)
	if err != nil {
		s.log.Warn("launch: the last launch does not read", "err", err)
		return emptyLastLaunch()
	}
	if !ok || !d.sound() {
		return emptyLastLaunch()
	}
	return d
}

// changeLastLaunch writes the record as change makes it of the one kept.
// A record that cannot be written, or would be past its bound, is said in
// the log and the one kept stays; what was asked for stands.
func (s *Server) changeLastLaunch(change func(lastLaunch) lastLaunch) {
	if s.store == nil {
		return
	}
	s.lastLaunchMu.Lock()
	defer s.lastLaunchMu.Unlock()
	next := change(s.readLastLaunch())
	if b, err := json.Marshal(next); err != nil || len(b) > maxLastLaunch {
		s.log.Warn("launch: the last launch is past its bound, not kept", "bytes", len(b))
		return
	}
	st, err := s.store.Sub("launch")
	if err == nil {
		err = st.Save(lastLaunchDoc, next)
	}
	if err != nil {
		s.log.Warn("launch: the last launch is not kept", "err", err)
	}
}

// noteLaunch records a launch on this server that started, the way way.
func (s *Server) noteLaunch(agent, folder, model, way string) {
	if agent == "" {
		return
	}
	s.changeLastLaunch(func(d lastLaunch) lastLaunch { return d.after(agent, folder, model, way) })
}

// noteTrial records a new agent trial the server took: its agents, its
// folder and its Check line's words.
func (s *Server) noteTrial(agents []string, folder string, check []string) {
	s.changeLastLaunch(func(d lastLaunch) lastLaunch { return d.afterTrial(agents, folder, check) })
}

// handleLastLaunch answers GET /api/launch/last.
func (s *Server) handleLastLaunch(w http.ResponseWriter, r *http.Request) {
	s.lastLaunchMu.Lock()
	d := s.readLastLaunch()
	s.lastLaunchMu.Unlock()
	writeJSON(w, http.StatusOK, d)
}

// chatNotedRequest is POST /api/launch/chat-noted's body: nothing.
type chatNotedRequest struct{}

// handleChatNoted answers POST /api/launch/chat-noted: the note on the
// vendor's terms in Chat was drawn, and is not drawn again. Its body is
// `{}` (at most maxChatNotedBody bytes, unknown fields refused); 204.
// Without a data directory it is kept nowhere: 503 store_unavailable.
func (s *Server) handleChatNoted(w http.ResponseWriter, r *http.Request) {
	var req chatNotedRequest
	if err := decodeJSONLimit(w, r, &req, maxChatNotedBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	s.changeLastLaunch(func(d lastLaunch) lastLaunch {
		d.ChatNoted = true
		return d
	})
	w.WriteHeader(http.StatusNoContent)
}
