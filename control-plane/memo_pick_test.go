package main

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// pickMemo is a pending, released, sendable message; tweak edits it before the release hash is taken.
func pickMemo(id string, pos int, tweak func(*store.Memo)) store.Memo {
	m := store.Memo{ID: id, Repo: "r", Category: "c", Kind: "text", Body: "body " + id, State: store.MemoStatePending,
		CreatedAt: "2026-01-01T00:00:00Z", Position: pos}
	if tweak != nil {
		tweak(&m)
	}
	m.ReleaseHash = memoContentHash(m)
	return m
}

func pickIDs(ms []store.Memo) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestMemoPickBatch(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	all := func(store.Memo) bool { return true }
	runNext := func(m *store.Memo) { m.RunNext = true }
	tooBig := func(m *store.Memo) { m.Body = strings.Repeat("x", memoDeliveryItemBodyMax+1) }
	claimed := func(m *store.Memo) { m.DeliveryID = "d1" }

	tests := []struct {
		name     string
		q        store.MemoQueue
		rows     []store.Memo
		only     string
		auto     bool
		eligible func(store.Memo) bool
		want     []string
		refusal  string // error code, "" for none
		holds    []string
	}{
		{name: "oldest first within the batch size", q: store.MemoQueue{Batch: 2},
			rows: []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, nil), pickMemo("c", 3, nil)}, want: []string{"a", "b"}},
		{name: "marked messages come first, then the rest in order", q: store.MemoQueue{Batch: 2},
			rows: []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, nil), pickMemo("c", 3, runNext)}, want: []string{"c", "a"}},
		{name: "a marked message is not taken twice", q: store.MemoQueue{Batch: 5},
			rows: []store.Memo{pickMemo("a", 1, runNext), pickMemo("b", 2, nil)}, want: []string{"a", "b"}},
		{name: "claimed and held messages are skipped", q: store.MemoQueue{Batch: 3},
			rows: []store.Memo{pickMemo("a", 1, claimed), pickMemo("b", 2, func(m *store.Memo) { m.State = store.MemoStateHeld }), pickMemo("c", 3, nil)},
			want: []string{"c"}},
		{name: "an ineligible message does not stop the ones behind it", q: store.MemoQueue{Batch: 2},
			rows:     []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, nil)},
			eligible: func(m store.Memo) bool { return m.ID != "a" }, want: []string{"b"}},
		{name: "an unsendable marked message is skipped by the mark and does not refuse the batch", q: store.MemoQueue{Batch: 1},
			rows: []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, func(m *store.Memo) { runNext(m); tooBig(m) })},
			want: []string{"a"}},
		{name: "an unsendable marked message is refused at its own place once the oldest-first order reaches it", q: store.MemoQueue{Batch: 2},
			rows:    []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, func(m *store.Memo) { runNext(m); tooBig(m) })},
			refusal: errCodeMemoQueueItemInvalid},
		{name: "an unsendable message in the oldest-first order refuses the claim", q: store.MemoQueue{Batch: 2},
			rows: []store.Memo{pickMemo("a", 1, tooBig), pickMemo("b", 2, nil)}, refusal: errCodeMemoQueueItemInvalid},
		{name: "a stale release is held and skipped", q: store.MemoQueue{Batch: 2},
			rows: []store.Memo{func() store.Memo { m := pickMemo("a", 1, nil); m.Body = "edited"; return m }(), pickMemo("b", 2, nil)},
			want: []string{"b"}, holds: []string{"a"}},
		{name: "a fire by itself leaves a reference while the Agent shows none", q: store.MemoQueue{Batch: 2, NoRefsAt: "t"}, auto: true,
			rows: []store.Memo{pickMemo("a", 1, func(m *store.Memo) { m.RefKey = "k" }), pickMemo("b", 2, nil)}, want: []string{"b"}},
		{name: "a press takes that reference", q: store.MemoQueue{Batch: 2, NoRefsAt: "t"}, only: "a",
			rows: []store.Memo{pickMemo("a", 1, func(m *store.Memo) { m.RefKey = "k" }), pickMemo("b", 2, nil)}, want: []string{"a"}},
		{name: "a press is a batch of that message alone", q: store.MemoQueue{Batch: 5}, only: "b",
			rows: []store.Memo{pickMemo("a", 1, nil), pickMemo("b", 2, nil)}, want: []string{"b"}},
		{name: "a press on a claimed message is refused", q: store.MemoQueue{Batch: 5}, only: "a",
			rows: []store.Memo{pickMemo("a", 1, claimed)}, refusal: errCodeMemoQueueMemoNotReady},
		{name: "a press on a message with open prerequisites is refused", q: store.MemoQueue{Batch: 5}, only: "a",
			rows: []store.Memo{pickMemo("a", 1, nil)}, eligible: func(store.Memo) bool { return false }, refusal: errCodeMemoQueueMemoNotReady},
		{name: "a press on a stale release holds it and refuses", q: store.MemoQueue{Batch: 5}, only: "a",
			rows:    []store.Memo{func() store.Memo { m := pickMemo("a", 1, nil); m.Body = "edited"; return m }()},
			refusal: errCodeMemoQueueMemoNotReady, holds: []string{"a"}},
		{name: "nothing pending is an empty batch without a refusal", q: store.MemoQueue{Batch: 2}, rows: nil, want: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			el := tc.eligible
			if el == nil {
				el = all
			}
			rows := append([]store.Memo(nil), tc.rows...)
			memoSortOldestFirst(rows)
			got := memoPickBatch(memoPickInput{Queue: tc.q, Rows: rows, Only: tc.only, Auto: tc.auto, Now: now, Eligible: el})
			if tc.refusal != "" {
				if got.Refusal == nil || got.Refusal.code != tc.refusal || got.Refusal.status != http.StatusConflict {
					t.Fatalf("refusal = %+v, want code %s", got.Refusal, tc.refusal)
				}
				if len(got.Batch) != 0 {
					t.Fatalf("a refusal carries no batch, got %v", pickIDs(got.Batch))
				}
			} else if got.Refusal != nil {
				t.Fatalf("unexpected refusal %+v", got.Refusal)
			} else if g := pickIDs(got.Batch); !reflect.DeepEqual(g, tc.want) {
				t.Fatalf("batch = %v, want %v", g, tc.want)
			}
			hs := []string{}
			for _, h := range got.Holds {
				hs = append(hs, h.Memo.ID)
				if h.Memo.State != store.MemoStateHeld || h.Memo.ReleaseHash != "" {
					t.Fatalf("hold %s is not a held, unreleased row: %+v", h.Memo.ID, h.Memo)
				}
			}
			if want := append([]string{}, tc.holds...); !reflect.DeepEqual(hs, want) {
				t.Fatalf("holds = %v, want %v", hs, want)
			}
		})
	}
}

func TestMemoPickBatchOrdersByCreatedAtThenPositionThenID(t *testing.T) {
	mk := func(id string, created string, pos int) store.Memo {
		return pickMemo(id, pos, func(m *store.Memo) { m.CreatedAt = created })
	}
	rows := []store.Memo{mk("d", "2026-01-01T00:00:02Z", 1), mk("c", "2026-01-01T00:00:01Z", 2), mk("b", "2026-01-01T00:00:01Z", 1), mk("a", "2026-01-01T00:00:01Z", 1)}
	memoSortOldestFirst(rows)
	if got, want := fmt.Sprint(pickIDs(rows)), "[a b c d]"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}
