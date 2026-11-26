package session

// QueueMoveLineage is the queue role a committed move's target inherited. Previous starts with
// the original session and ends with the immediate source. Keeping the chain on the target lets
// the run outlive archiving, deleting or reopening a replaced session.
type QueueMoveLineage struct {
	Previous  []string   `json:"previous"`
	Origin    string     `json:"origin"`
	Parent    string     `json:"parent,omitempty"`
	Delivery  string     `json:"delivery,omitempty"`
	Category  string     `json:"category,omitempty"`
	FirstTurn *QueueTurn `json:"firstTurn,omitempty"`
}

// QueueGraph resolves the live owners of queue nodes from one metadata snapshot. MovedTo covers
// moves committed before lineage was stored, and the interval between commit and finalization.
type QueueGraph struct {
	metas map[string]Meta
	next  map[string]string
	prev  map[string]string
}

// NewQueueGraph indexes the committed edges in metas.
func NewQueueGraph(metas map[string]Meta) QueueGraph {
	g := QueueGraph{metas: metas, next: map[string]string{}, prev: map[string]string{}}
	for _, m := range metas {
		if m.QueueSuccessor != "" {
			g.next[m.Name] = m.QueueSuccessor
		} else if m.MovedTo != "" {
			g.next[m.Name] = m.MovedTo
		}
	}
	for _, m := range metas {
		if m.QueueMove == nil {
			continue
		}
		to := m.Name
		for i := len(m.QueueMove.Previous) - 1; i >= 0; i-- {
			from := m.QueueMove.Previous[i]
			g.next[from] = to
			to = from
		}
	}
	for from, to := range g.next {
		g.prev[to] = from
	}
	return g
}

// ReadQueueGraph builds the graph from the session metadata on disk.
func ReadQueueGraph() QueueGraph {
	metas := map[string]Meta{}
	for _, m := range ListMetas() {
		metas[m.Name] = m
	}
	return NewQueueGraph(metas)
}

// Current returns the last committed name in a move chain, including a target since deleted.
// A cycle returns the supplied name.
func (g QueueGraph) Current(name string) string {
	cur := name
	for i := 0; g.next[cur] != ""; i++ {
		if i >= len(g.next) {
			return name
		}
		cur = g.next[cur]
	}
	return cur
}

// lineage recovers the role of one node, including the unsaved edge of a legacy commit.
func (g QueueGraph) lineage(name string) QueueMoveLineage {
	var trail []string
	cur := name
	for {
		m := g.metas[cur]
		if m.QueueMove != nil {
			out := *m.QueueMove
			out.Previous = append(append([]string(nil), out.Previous...), trail...)
			return out
		}
		from := g.prev[cur]
		if from == "" || len(trail) >= len(g.prev) {
			origin := OriginOf(m)
			if m.QueueDelivery != "" {
				origin = OriginQueue
			}
			return QueueMoveLineage{Previous: trail, Origin: origin, Parent: m.OriginSession,
				Delivery: m.QueueDelivery, Category: m.QueueCategoryID}
		}
		trail = append([]string{from}, trail...)
		cur = from
	}
}

// Lineage returns a node's inherited role and whether it belongs to a queue. A deleted parent
// is a gap unless a committed move provides its replacement.
func (g QueueGraph) Lineage(name string) (QueueMoveLineage, bool) {
	role := g.lineage(name)
	for cur, node, i := name, role, 0; i <= len(g.metas); i++ {
		switch node.Origin {
		case OriginQueue:
			return role, true
		case OriginSession:
			parent := g.Current(node.Parent)
			if parent == "" || parent == cur {
				return role, false
			}
			cur, node = parent, g.lineage(parent)
		default:
			return role, false
		}
	}
	return role, false
}

// Project applies queue ownership without changing the session's launch provenance on disk.
func (g QueueGraph) Project(m Meta) Meta {
	role, ok := g.Lineage(m.Name)
	if !ok {
		return m
	}
	if g.Current(m.Name) != m.Name {
		m.Origin, m.OriginSession, m.QueueDelivery, m.QueueCategoryID = OriginUser, "", "", ""
		return m
	}
	m.Origin, m.OriginSession = role.Origin, g.Current(role.Parent)
	m.QueueDelivery, m.QueueCategoryID = role.Delivery, role.Category
	return m
}
