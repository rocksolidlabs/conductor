package ext

// ResetRegistry empties the registry and opens it again: ext's own tests
// register into it, and no other test may (the composition root's tests
// replace what reads it instead).
func ResetRegistry() {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.frozen = false
	reg.held = Providers{}
	reg.slots = [kindCount][]Entry{}
	reg.names = nil
}
