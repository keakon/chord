package tools

// get returns the registered process for id; used by tests that verify the
// registry's external lifecycle behavior. Production lookups go through the
// dedicated cancel/start/stop methods that already operate atomically.
func (r *SpawnRegistry) get(id string) (*spawnedProcess, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	proc, ok := r.processes[id]
	return proc, ok
}
