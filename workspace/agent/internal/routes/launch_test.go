package routes

import "testing"

func TestLaunchClone(t *testing.T) {
	var l *Launch
	if l.Clone() != nil {
		t.Error("nil Launch must clone to nil")
	}
	a := &Launch{Route: "r", Binding: "b"}
	c := a.Clone()
	c.Binding = "x"
	if a.Binding != "b" {
		t.Error("Clone aliases")
	}
}
