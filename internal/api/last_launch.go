package api

import (
	"net/http"
	"strings"
)

// The last launch (UX3 R89): what Launch agent and the welcome read to
// start where the person left off, after a restart of the server too,
// which the app's own storage does not outlast (the desktop app's server
// takes a fresh port at each start, and what a page keeps goes with its
// origin). The server keeps it itself, as launch/last.json in the data
// directory: a document of its own, not the layout document, which a sync
// provider may carry to other machines (the sync seam delivers an
// allowlisted set of documents, and this is not one), since a folder's
// path stays on this machine.
//
// A launch on this server that started records it: POST /api/sessions
// alone (a crew's members, a resume, a clone and a hosted session do
// not). It holds the agent launched (never the built-in shell, which is
// no agent to start with), the folder as the launch named it ("" for the
// server's default), and for each agent its last model and the model
// names it was last launched with, newest first: never the arguments
// (they can hold a key), the task, the name, the account or yolo. GET
// /api/launch/last answers it, {"v":1,"models":{}} when nothing was
// launched yet or the record does not read.

// The record's bounds.
const (
	lastLaunchDoc    = "last.json"
	lastLaunchAgents = 32
	lastLaunchNames  = 5
	maxLastLaunch    = 64 << 10
	// lastLaunchShell is the built-in shell's id (catalog defaults).
	lastLaunchShell = "shell"
)

// lastLaunchModel is what an agent was last launched with.
type lastLaunchModel struct {
	Model string   `json:"model"`
	Names []string `json:"names"`
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
}

// emptyLastLaunch is the record before any launch.
func emptyLastLaunch() lastLaunch { return lastLaunch{V: 1, Models: map[string]lastLaunchModel{}} }

// sound reports whether d is a record this server writes: a broken one
// is read as none, and the next launch writes a whole one over it.
func (d lastLaunch) sound() bool {
	if d.V != 1 || len(d.Models) > lastLaunchAgents || len(d.Order) != len(d.Models) {
		return false
	}
	for _, id := range d.Order {
		m, ok := d.Models[id]
		if !ok || len(m.Names) > lastLaunchNames {
			return false
		}
	}
	return true
}

// after is d with the launch of agent in folder with model.
func (d lastLaunch) after(agent, folder, model string) lastLaunch {
	model = strings.TrimSpace(model)
	next := lastLaunch{V: 1, Agent: d.Agent, Folder: folder, Models: map[string]lastLaunchModel{}}
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
	next.Models[agent] = lastLaunchModel{Model: model, Names: names}
	for len(next.Order) > lastLaunchAgents {
		delete(next.Models, next.Order[0])
		next.Order = next.Order[1:]
	}
	return next
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

// noteLaunch records a launch on this server that started. A record that
// cannot be written is said in the log; the launch stands.
func (s *Server) noteLaunch(agent, folder, model string) {
	if s.store == nil || agent == "" {
		return
	}
	s.lastLaunchMu.Lock()
	defer s.lastLaunchMu.Unlock()
	next := s.readLastLaunch().after(agent, folder, model)
	st, err := s.store.Sub("launch")
	if err == nil {
		err = st.Save(lastLaunchDoc, next)
	}
	if err != nil {
		s.log.Warn("launch: the last launch is not kept", "err", err)
	}
}

// handleLastLaunch answers GET /api/launch/last.
func (s *Server) handleLastLaunch(w http.ResponseWriter, r *http.Request) {
	s.lastLaunchMu.Lock()
	d := s.readLastLaunch()
	s.lastLaunchMu.Unlock()
	writeJSON(w, http.StatusOK, d)
}
