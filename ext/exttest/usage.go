package exttest

import (
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

var _ ext.UsageObserver = (*FakeUsageObserver)(nil)

// FakeUsageObserver is a FakeIdentity that is also an ext.UsageObserver:
// it records every report it is handed, in order, and tells Changed at
// each, so a test waits on it (Await) rather than on a clock.
type FakeUsageObserver struct {
	*FakeIdentity

	umu    sync.Mutex
	usages []ext.Usage
}

// NewFakeUsageObserver is a FakeUsageObserver named name that knows no
// token and has been handed no report.
func NewFakeUsageObserver(name string) *FakeUsageObserver {
	return &FakeUsageObserver{FakeIdentity: NewFakeIdentity(name)}
}

// Used records u.
func (f *FakeUsageObserver) Used(u ext.Usage) {
	f.umu.Lock()
	f.usages = append(f.usages, u)
	f.umu.Unlock()
	f.tell()
}

// Usages answers a copy of the reports handed so far, in order.
func (f *FakeUsageObserver) Usages() []ext.Usage {
	f.umu.Lock()
	defer f.umu.Unlock()
	return append([]ext.Usage(nil), f.usages...)
}
