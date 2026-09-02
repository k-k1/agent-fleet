package browserx

import "sync"

// browserViewerLeasePool enforces the Workspace-wide rendering/viewer cap
// across Agent-owned browser Pages and external-owner attachments. A lease is
// held for the lifetime of a viewer socket (including a temporarily hidden
// pane); hidden sockets stop screencasting but cannot race a third viewer into
// the two-pane resource budget when they become visible again.
type browserViewerLeasePool struct {
	mu     sync.Mutex
	limit  int
	owners map[string]struct{}
}

var workspaceBrowserViewerLeases = &browserViewerLeasePool{
	limit: browserConfigInt("AF_BROWSER_VIEWER_LIMIT", 2, 1, 16), owners: make(map[string]struct{}),
}

func (p *browserViewerLeasePool) acquire(owner string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.owners[owner]; exists {
		return true
	}
	if len(p.owners) >= p.limit {
		return false
	}
	p.owners[owner] = struct{}{}
	return true
}

func (p *browserViewerLeasePool) release(owner string) {
	p.mu.Lock()
	delete(p.owners, owner)
	p.mu.Unlock()
}

func browserPageViewerLease(id string) string       { return "page:" + id }
func browserAttachmentViewerLease(id string) string { return "attachment:" + id }
