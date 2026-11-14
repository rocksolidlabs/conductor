package brakeman

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rocksolidlabs/conductor/internal/capacity"
)

// What Brakeman reads of capacity (docs/plans/capacity.md, PR-6): an
// agent's view on an account, as the Capacity class shows it to a turn,
// as a capacity: ref keeps it once its brief is kept, and as a request
// keeps its own agent's view when it is queued. Its fields are listed
// here, one by one, from the admin's view (CapacityOf): labels and
// figures, never an account's folder, the server's name, a variable's
// name or value, or a reader's own words.

// MaxCapacity bounds a Capacity as encoded: the view's own bounds (16
// windows, 4 credit figures, a label of 60 characters) keep it under, at
// about 8.3 KiB with every text at its longest in characters of four bytes
// (TestCapacityOfKeepsItsList).
const MaxCapacity = 12 << 10

// Capacity is an agent's capacity on an account as Brakeman reads it.
type Capacity struct {
	// Ref is the ref a brief cites the reading by, for a view that shows
	// one (capacity:<agent>/<account>@<readAt>).
	Ref     string          `json:"ref,omitempty"`
	Agent   CapacityAgent   `json:"agent"`
	Account CapacityAccount `json:"account"`
	// Host is where a launch would run: native, undetermined or remote,
	// the kind alone; empty in a brief's snapshot of a reading, which
	// names no host.
	Host  string `json:"host"`
	State string `json:"state"`
	// Words are the state's own (capacity.StateWords), never a reader's.
	Words   string            `json:"words,omitempty"`
	Windows []capacity.Window `json:"windows"`
	Credits []capacity.Credit `json:"credits"`
	Limits  capacity.Limits   `json:"limits"`
	// Counts are this server's, an estimate, counted at CountedAt.
	Counts    capacity.Counts  `json:"counts"`
	CountedAt time.Time        `json:"countedAt"`
	ReadAt    *time.Time       `json:"readAt,omitempty"`
	ReadBy    *capacity.ReadBy `json:"readBy,omitempty"`
	Fresh     string           `json:"fresh"`
}

// CapacityAgent is the agent a Capacity is of.
type CapacityAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Version string `json:"version,omitempty"`
}

// CapacityAccount is the account a Capacity is of: its id and label,
// never its folder.
type CapacityAccount struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Default bool   `json:"default,omitempty"`
	Retired bool   `json:"retired,omitempty"`
	Plan    string `json:"plan,omitempty"`
}

// CapacityOf is v as Brakeman reads it, its counts counted at at: each
// field taken by name from v, the words the state's own, a version past
// capacity.MaxVersion left out, every time in UTC, and the ref of the
// reading v shows.
func CapacityOf(v capacity.View, at time.Time) Capacity {
	c := Capacity{
		Agent:     CapacityAgent{ID: v.Agent.ID, Name: v.Agent.Name, Adapter: v.Agent.Adapter},
		Account:   CapacityAccount{ID: v.Account.ID, Label: v.Account.Label, Default: v.Account.Default, Retired: v.Account.Retired, Plan: v.Account.Plan},
		Host:      v.Host.Kind,
		State:     v.State,
		Words:     capacity.StateWords(v.State),
		Windows:   []capacity.Window{},
		Credits:   []capacity.Credit{},
		Limits:    capacity.Limits{Reached: v.Limits.Reached, SpendControl: own(v.Limits.SpendControl), OrdinaryAllowed: own(v.Limits.OrdinaryAllowed)},
		Counts:    capacity.Counts{Running: v.Counts.Running, Started5h: v.Counts.Started5h, Started7d: v.Counts.Started7d, Since: utc(v.Counts.Since)},
		CountedAt: at.UTC(),
		Fresh:     v.Fresh,
	}
	if len(v.Agent.Version) <= capacity.MaxVersion {
		c.Agent.Version = v.Agent.Version
	}
	for _, w := range v.Windows {
		c.Windows = append(c.Windows, capacity.Window{Kind: w.Kind, Name: w.Name, Minutes: w.Minutes, Percent: w.Percent, ResetsAt: utc(w.ResetsAt), Severity: w.Severity, Active: own(w.Active), Limit: w.Limit})
	}
	for _, cr := range v.Credits {
		c.Credits = append(c.Credits, capacity.Credit{Kind: cr.Kind, Enabled: own(cr.Enabled), Used: own(cr.Used), Limit: own(cr.Limit), Balance: own(cr.Balance), Unlimited: cr.Unlimited, Currency: cr.Currency})
	}
	if v.ReadBy != nil {
		c.ReadBy = &capacity.ReadBy{Program: v.ReadBy.Program, Version: v.ReadBy.Version, Qualified: v.ReadBy.Qualified}
	}
	if v.ReadAt != nil {
		t := v.ReadAt.UTC()
		c.ReadAt = &t
		if ref := (Ref{Kind: RefCapacity, Agent: v.Agent.ID, Account: v.Account.ID, ReadAt: t.Format(time.RFC3339Nano)}); ref.Valid() == nil {
			c.Ref = ref.String()
		}
	}
	return c
}

// own is a copy of what p points to.
func own[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// utc is a copy of the time p points to, in UTC.
func utc(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	t := p.UTC()
	return &t
}

// checkCapacity refuses a Capacity past MaxCapacity as encoded (field
// names it).
func checkCapacity(field string, c *Capacity) error {
	if c == nil {
		return nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(b) > MaxCapacity {
		return fmt.Errorf("%w: %s is %d bytes, at most %d", ErrBound, field, len(b), MaxCapacity)
	}
	return nil
}
