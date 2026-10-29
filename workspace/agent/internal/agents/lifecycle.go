package agents

// ManagedLifecycler is an optional Agent capability: the kind has a Managed driver whose
// sessions package main reconciles at boot and aborts and shuts down on a graceful stop. A kind
// without it has nothing to reconcile or release; claude's Managed path is not reconciled from
// main. The registry is walked in internal/kinds order, so that order is the reconcile start,
// abort and shutdown order.
type ManagedLifecycler interface {
	ManagedLifecycle() ManagedLifecycle
}

// ManagedLifecycle is one kind's Managed lifecycle. Every field is set for a kind that has the
// capability.
type ManagedLifecycle struct {
	// Reconcile re-attaches the kind's managed sessions after an Agent boot; reason is logged.
	// Without it a managed session stays stopped across an Agent restart until the user resumes it.
	Reconcile func(reason string)
	// Abort interrupts every running managed turn, the managed counterpart of a pane's C-c.
	// Without it the shutdown's wait for idle runs to the deadline while a turn is in flight.
	Abort func()
	// Shutdown releases the kind's daemon or child processes. It runs only after every kind's
	// Abort, so a shared daemon's drain returns at once.
	Shutdown func()
}
