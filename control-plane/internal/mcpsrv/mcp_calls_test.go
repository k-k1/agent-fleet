package mcpsrv

import (
	"strconv"
	"sync"
	"testing"
)

func TestCallLogIsBoundedAndConcurrencySafe(t *testing.T) {
	var l CallLog
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				l.add("pat:p", CallRecord{Status: i})
				_ = l.Recent("pat:p")
			}
		}()
	}
	wg.Wait()
	if n := len(l.Recent("pat:p")); n != callRingSize {
		t.Errorf("ring holds %d, want %d", n, callRingSize)
	}
	for i := 0; i < callMaxKeys+100; i++ {
		l.add("grant:"+strconv.Itoa(i), CallRecord{})
	}
	if len(l.rings) > callMaxKeys {
		t.Errorf("%d rings, want <= %d", len(l.rings), callMaxKeys)
	}
}

func TestCallLogNilAndNewestFirst(t *testing.T) {
	var nl *CallLog
	nl.add("k", CallRecord{})
	if got := nl.Recent("k"); got == nil || len(got) != 0 {
		t.Errorf("nil log: %v", got)
	}
	var l CallLog
	for i := 1; i <= 3; i++ {
		l.add("k", CallRecord{Status: i})
	}
	if got := l.Recent("k"); got[0].Status != 3 || got[2].Status != 1 {
		t.Errorf("order: %+v", got)
	}
}

func TestClipKeepsRunesWhole(t *testing.T) {
	if got := clip("あいう", 4); got != "あ" {
		t.Errorf("clip: %q", got)
	}
}
