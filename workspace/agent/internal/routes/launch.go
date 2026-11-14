package routes

// Launch is what a session ran on when it was launched (ADR 0138 D11): the route its create
// resolved to, explicit or not, and the binding of the credential instance behind it (D5b,
// internal/credbind). It is recorded at create, fork and recreate, moved only by a consented
// route switch (a recorded transition), and never re-resolved from today's credential: a policy
// judges an existing session on this record, and a resume whose current credential no longer
// matches it is a replacement, refused rather than moved. A session created before the record
// existed has none (nil); an empty Route is a create whose route could not be resolved.
type Launch struct {
	Route   string `json:"route,omitempty"`
	Binding string `json:"binding,omitempty"`
}

// Clone returns an independent copy, nil for nil.
func (l *Launch) Clone() *Launch {
	if l == nil {
		return nil
	}
	c := *l
	return &c
}
