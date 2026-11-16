package sessionx

import "sync"

// launching counts, per session name, the launches this Agent has in flight: a create, a start,
// a driver or route switch, a recreate. It is the Agent's own positive record that "something is
// bringing this session up", which tmux alone cannot give: before the pane exists, "not yet" and
// "stopped, and nobody is starting it" look the same.
//
// A when_ready input is the first instruction to a session being launched, so it must wait for a
// launch in flight and must be refused for a session that is stopped with none. Deciding that
// from tmux would refuse the first instruction of a session that is merely not up yet.
var (
	launchingMu sync.Mutex
	launching   = map[string]int{}
)

// markLaunching records a launch of name in flight and returns the function that ends it. Calls
// nest (a switch handler marks, and the launch inside it marks again).
func markLaunching(name string) (done func()) {
	launchingMu.Lock()
	launching[name]++
	launchingMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			launchingMu.Lock()
			defer launchingMu.Unlock()
			if launching[name]--; launching[name] <= 0 {
				delete(launching, name)
			}
		})
	}
}

// launchInFlight reports whether a launch of name is in flight. Read it before looking at tmux:
// a launch that ends between the two reads has left its pane behind, so tmux then answers yes.
func launchInFlight(name string) bool {
	launchingMu.Lock()
	defer launchingMu.Unlock()
	return launching[name] > 0
}
