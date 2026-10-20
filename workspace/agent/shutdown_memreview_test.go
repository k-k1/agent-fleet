package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memreview"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type blockedSource struct{}

func (blockedSource) Candidates() []memreview.Candidate {
	return []memreview.Candidate{{Key: "k", Name: "s"}}
}
func (blockedSource) Turns(memreview.Candidate) ([]transcript.Turn, error) {
	return []transcript.Turn{{Role: "user", Text: "hello", Parts: []transcript.Part{{Kind: "text", Text: "hello"}}}}, nil
}

// The Agent's shutdown reaches the memory review: the helper call in flight is cancelled, so no
// CLI is left behind when the Agent exits.
func TestShutdownMemoryReviewCancelsTheHelperCall(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	lp := memreview.NewLoop(blockedSource{})
	lp.Enabled = func() bool { return true }
	lp.Reviewer.Enabled = func() bool { return true }
	lp.Reviewer.Snapshot = func(string) (memoryx.ReviewSnapshot, error) { return memoryx.ReviewSnapshot{}, nil }
	started, cancelled := make(chan struct{}), make(chan error, 1)
	lp.Reviewer.Call = func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return "", ctx.Err()
	}
	old := memreview.DefaultLoop
	memreview.DefaultLoop = lp
	t.Cleanup(func() { memreview.DefaultLoop = old })

	if err := lp.ReviewNow("s"); err != nil {
		t.Fatal(err)
	}
	<-started
	shutdownMemoryReview(5 * time.Second)
	select {
	case err := <-cancelled:
		if err != context.Canceled {
			t.Fatalf("err = %v", err)
		}
	default:
		t.Fatal("shutdown did not cancel the helper call")
	}
	if err := lp.ReviewNow("s"); err == nil {
		t.Fatal("a review started after shutdown")
	}
}

// gracefulShutdown has to call it (a refactor that drops the line would leave every other test
// green).
func TestGracefulShutdownStopsTheMemoryReview(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "shutdown.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "gracefulShutdown" {
			return true
		}
		ast.Inspect(fd.Body, func(m ast.Node) bool {
			if c, ok := m.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "shutdownMemoryReview" {
					found = true
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("gracefulShutdown does not stop the memory review")
	}
}
