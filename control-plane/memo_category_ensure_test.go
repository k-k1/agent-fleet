package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func (e *inboxEnv) categoryRows(repo string) []store.MemoCategory {
	e.t.Helper()
	all, err := e.st.ListCategories(context.Background(), e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.MemoCategory
	for _, c := range all {
		if c.Repo == repo {
			out = append(out, c)
		}
	}
	return out
}

// A note filed under a new name by any face makes the category row, at the end of its repo's
// order; a second note under the same name makes no second row.
func TestMemoCreateMakesTheCategoryRow(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.category("r", "first")
		faces := append([]memoFaceClient{{"console", e}}, workspaceFaces(e)...)
		for i, f := range faces {
			name := "new-" + f.name
			for n := 0; n < 2; n++ {
				if code, _ := f.create(textMemo("r", name, fmt.Sprintf("note %d", n))); code != http.StatusCreated {
					t.Fatalf("%s create: %d", f.name, code)
				}
			}
			rows := e.categoryRows("r")
			if len(rows) != i+2 {
				t.Fatalf("%s: %d rows, want %d: %+v", f.name, len(rows), i+2, rows)
			}
			if last := rows[len(rows)-1]; last.Name != name || last.Position != i+1 {
				t.Fatalf("%s: last row %+v, want %q at position %d", f.name, last, name, i+1)
			}
		}
		// The unfiled bucket and another repo are not touched.
		if code, _ := (memoFaceClient{"token", e}).create(textMemo("r", "", "unfiled")); code != http.StatusCreated {
			t.Fatalf("unfiled: %d", code)
		}
		if code, _ := (memoFaceClient{"token", e}).create(textMemo("other", "new-token", "elsewhere")); code != http.StatusCreated {
			t.Fatalf("other repo: %d", code)
		}
		if n := len(e.categoryRows("r")); n != len(faces)+1 {
			t.Fatalf("repo r has %d rows", n)
		}
		if rows := e.categoryRows("other"); len(rows) != 1 || rows[0].Position != 0 {
			t.Fatalf("other repo rows: %+v", rows)
		}
	})
}

// A note that fails validation leaves no row.
func TestMemoCreateRejectedLeavesNoCategoryRow(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		for _, f := range workspaceFaces(e) {
			if code, _ := f.create(textMemo("r", "ghost-"+f.name, "")); code < 400 {
				t.Fatalf("%s empty body: %d", f.name, code)
			}
		}
		if rows := e.categoryRows("r"); len(rows) != 0 {
			t.Fatalf("rejected notes left rows: %+v", rows)
		}
	})
}

// Concurrent notes under one new name yield one row and no error.
func TestMemoCreateSameNewCategoryConcurrently(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("r", "race", fmt.Sprintf("c%d", i)))
				if r.Code != http.StatusCreated {
					t.Errorf("create %d: %d %s", i, r.Code, r.Body.String())
				}
			}(i)
		}
		wg.Wait()
		if rows := e.categoryRows("r"); len(rows) != 1 || rows[0].Name != "race" {
			t.Fatalf("rows: %+v", rows)
		}
	})
}
