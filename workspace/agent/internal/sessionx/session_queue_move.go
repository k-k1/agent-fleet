package sessionx

import (
	"fmt"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// keepQueueMove saves a committed edge on the target before its source can be reopened or
// removed. Callers hold the session metadata lock so another move cannot extend the chain
// while the edge is saved. The source's MovedTo is the commit point and the recovery witness.
func keepQueueMove(source session.Meta) error {
	if source.MovedTo == "" {
		return nil
	}
	graph := session.ReadQueueGraph()
	if _, queue := graph.Lineage(source.Name); !queue || graph.Current(source.Name) == source.Name {
		return nil
	}
	target, st := session.ReadMetaState(source.MovedTo)
	switch st {
	case session.MetaAbsent:
		return nil
	case session.MetaUnreadable:
		return fmt.Errorf("move target %s is unreadable", source.MovedTo)
	}
	if target.QueueMove != nil {
		return nil
	}
	role, ok := graph.Lineage(target.Name)
	if !ok {
		return nil
	}
	if role.Delivery != "" && role.FirstTurn == nil && len(role.Previous) != 0 {
		var row session.Session
		if original, ok := session.ReadMeta(role.Previous[0]); ok {
			fillQueueTurn(&row, original)
		}
		role.FirstTurn = row.QueueTurn
	}
	target.QueueMove = &role
	return writeMoveTargetMeta(target)
}

// PreserveQueueMoveBeforeTrash retains the continuation when the workspace removes the source.
// It is called under the session metadata lock, immediately before removal.
func PreserveQueueMoveBeforeTrash(source session.Meta) error {
	return keepQueueMove(source)
}

func projectQueueMove(row *session.Session, meta session.Meta, metas map[string]session.Meta, graph session.QueueGraph) {
	// A liveness stamp can reread the metadata after the snapshot. Mixing its move
	// fields with the graph can point to a target that the snapshot never listed.
	row.Moving, row.MovedTo = meta.Moving, meta.MovedTo
	role, queue := graph.Lineage(meta.Name)
	if !queue {
		return
	}
	projected := graph.Project(meta)
	row.Origin, row.OriginSession = projected.Origin, projected.OriginSession
	row.QueueDelivery, row.QueueCategoryID = projected.QueueDelivery, projected.QueueCategoryID
	if owner := graph.Current(meta.Name); owner != meta.Name {
		row.QueueSuccessor = owner
	} else {
		row.QueueReplaces = role.Previous
		if row.QueueDelivery != "" && len(role.Previous) != 0 {
			row.QueueTurn = role.FirstTurn
			if row.QueueTurn == nil {
				fillQueueTurn(row, metas[role.Previous[0]])
			}
		}
	}
}

// queueRetireMetas preserves the children started on earlier names while excluding the old
// sessions themselves from the retire.
func queueRetireMetas(metas []session.Meta, root string) ([]session.Meta, *session.QueueGraph) {
	byName := make(map[string]session.Meta, len(metas))
	for _, m := range metas {
		byName[m.Name] = m
	}
	graph := session.NewQueueGraph(byName)
	if _, ok := graph.Lineage(root); !ok {
		return metas, nil
	}
	out := slices.Clone(metas)
	for i, m := range out {
		out[i] = graph.Project(m)
	}
	return out, &graph
}

// markQueueMoveTarget protects the new session from a conditional retire based on a list
// taken before the member moved it.
func markQueueMoveTarget(source, target string) {
	graph := session.ReadQueueGraph()
	if _, queue := graph.Lineage(source); queue && graph.Current(source) == source {
		notePersonAct(target)
	}
}
