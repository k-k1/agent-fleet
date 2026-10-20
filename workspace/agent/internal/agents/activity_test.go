package agents

import (
	"testing"
	"time"
)

func TestActivityProgress(t *testing.T) {
	var fresh Activity
	fresh.Touch()
	at, _ := fresh.At()
	later := at.Add(2 * time.Hour) // the same stamp, two hours on: stale
	soon := at.Add(10 * time.Second)

	yes := func() (bool, bool) { return true, true }
	no := func() (bool, bool) { return false, true }
	blind := func() (bool, bool) { return false, false }
	boom := func() (bool, bool) { t.Fatal("tool probe ran for a fresh stamp"); return false, false }

	t.Run("never heard from, no tool: cannot tell", func(t *testing.T) {
		var a Activity
		if _, ok := a.Progress(later, no); ok {
			t.Fatal("answered for a runtime never heard from")
		}
	})
	t.Run("never heard from, tool alive: now", func(t *testing.T) {
		var a Activity
		got, ok := a.Progress(later, yes)
		if !ok || !got.Equal(later) {
			t.Fatalf("got %v, %v; want now", got, ok)
		}
	})
	t.Run("stale stamp and a silent tool keeps holding", func(t *testing.T) {
		got, ok := fresh.Progress(later, yes)
		if !ok || !got.Equal(later) {
			t.Fatalf("got %v, %v; want now", got, ok)
		}
	})
	t.Run("stale stamp, no tool: the stamp, so the bound can lapse", func(t *testing.T) {
		got, ok := fresh.Progress(later, no)
		if !ok || !got.Equal(at) {
			t.Fatalf("got %v, %v; want the stamp %v", got, ok, at)
		}
	})
	t.Run("stale stamp and an unobservable probe: cannot tell, keeps holding", func(t *testing.T) {
		if got, ok := fresh.Progress(later, blind); ok {
			t.Fatalf("answered %v although the tool probe could not observe", got)
		}
	})
	t.Run("an unobservable probe does not hide a fresh stamp", func(t *testing.T) {
		if got, ok := fresh.Progress(soon, blind); !ok || !got.Equal(at) {
			t.Fatalf("got %v, %v; want the stamp", got, ok)
		}
	})
	t.Run("fresh stamp skips the probe", func(t *testing.T) {
		got, ok := fresh.Progress(soon, boom)
		if !ok || !got.Equal(at) {
			t.Fatalf("got %v, %v; want the stamp", got, ok)
		}
	})
	t.Run("nil probe: stamp only", func(t *testing.T) {
		got, ok := fresh.Progress(later, nil)
		if !ok || !got.Equal(at) {
			t.Fatalf("got %v, %v; want the stamp", got, ok)
		}
	})
}

func TestInFlight(t *testing.T) {
	var f InFlight
	if alive, observed := f.State(); alive || observed {
		t.Fatalf("zero value = %v, %v; want unknown", alive, observed)
	}
	f.Add("a") // a call restored after a gap is alive although the set is not known
	if alive, _ := f.State(); !alive {
		t.Fatal("a call seen running was not alive")
	}
	f.Done("a")
	if alive, observed := f.State(); alive || observed {
		t.Fatalf("after Done with no boundary seen = %v, %v; want unknown", alive, observed)
	}
	f.Reset()
	if alive, observed := f.State(); alive || !observed {
		t.Fatalf("after Reset = %v, %v; want known and empty", alive, observed)
	}
	f.Add("a")
	f.Add("b")
	f.Done("a")
	if alive, _ := f.State(); !alive {
		t.Fatal("the second call was dropped when the first ended")
	}
	f.Unknown()
	if alive, observed := f.State(); alive || observed {
		t.Fatalf("after Unknown = %v, %v; want unknown", alive, observed)
	}
	f.Add("")
	if alive, _ := f.State(); alive {
		t.Fatal("an empty id opened a call")
	}
}
