package main

// 移送で main 側に残した 1 本（ADR 0067 / CP-STORE）。memoCreateFor は memo.go の
// API 層ヘルパで store ではない。移送前は internal/store/store_sqlite_test.go の
// TestSQLiteMemo の中に 6 行だけ紛れていた——store の CRUD を検査するテストの
// 途中で、1 箇所だけ API 層を呼んでいた。切断面の内側に memo.go を引きずり込む
// 理由は無いので、その 1 件分をここへ出した。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// 新規メモは、API が position を省いてきても repo/category グループの末尾に付く
// （ゼロ値の 0 をそのまま採らない）。
func TestMemoCreateForAppendsToItsGroup(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, _ := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")

	first := store.Memo{ID: store.NewID(), MembershipID: mem.ID, Repo: "repo-a", Category: "frontend",
		Kind: "text", Body: "tighten padding", Position: 0, CreatedAt: store.NowTS()}
	if err := st.CreateMemo(ctx, first); err != nil {
		t.Fatalf("create: %v", err)
	}

	created, aerr := memoCreateFor(ctx, st, store.MembershipView{MembershipID: mem.ID}, memoDTO{
		Repo: "repo-a", Category: "frontend", Kind: "text", Body: "add at bottom",
	})
	if aerr != nil || created.Position != 1 {
		t.Fatalf("new memo position: err=%v memo=%+v", aerr, created)
	}
}
