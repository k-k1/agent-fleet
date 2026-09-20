//go:build manuallive

package harness

// Opt-in live check against a real llama.cpp engine (docs/log/99 §4.14's own suggested
// "実エンジンに対する契約テスト", opt-in shape) — this is what actually drove ADR 0093 segment
// G's own acceptance requirement: a real conversation against the dev deployment's "llm"
// engine, grown until decision 7's compaction fires once, with the conversation still
// answering afterward. NOT part of `go test ./...` (build-tagged out, so it never touches a
// real, billed GPU box by accident) — run explicitly with:
//
//   go test ./internal/harness/ -tags manuallive -run TestManualLiveCompaction -v -timeout 20m
//
// with AF_LCPP_LIVE_BASE (the engine's own /v1-mount base URL, e.g.
// https://<cp>/engine/llm/v1) and AF_LCPP_LIVE_TOKEN (a session/engine-scoped bearer minted via
// POST /internal/engine/token — never the CP's own AF_ENGINE_ISSUE_TOKEN) set. This is exactly
// how two real chat-template incompatibilities were found and fixed in compact.go: Qwen's own
// template rejects a system-role message anywhere but the first, and rejects a request that
// does not end in a user turn — neither is exercisable with a scripted Client stub, only a
// real engine's own template actually raises them.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestManualLiveCompaction(t *testing.T) {
	base := os.Getenv("AF_LCPP_LIVE_BASE")
	token := os.Getenv("AF_LCPP_LIVE_TOKEN")
	if base == "" || token == "" {
		t.Skip("AF_LCPP_LIVE_BASE / AF_LCPP_LIVE_TOKEN not set")
	}
	model := os.Getenv("AF_LCPP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.8-27b-uncensored-q4_k_m"
	}
	client := NewClient(EngineConn{BaseURL: base, Token: token}, model)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	sys := "You are a terse test assistant used for an automated harness check. Reply in one short sentence."
	var full []Message
	reservedOutput := 128
	// Deliberately far below the model's real window (262144, confirmed live via
	// GET /engine/llm/props for this same deployment) — actually filling the real window would
	// take a very large number of turns and a long time on a GPU box shared with other
	// sessions, for no more evidence than a small forced window already gives: PrepareTurn's
	// judgement only cares about the ARITHMETIC (input_tokens + reserved > window*threshold),
	// and input_tokens itself is measured for real either way.
	window := 900

	compactedAtTurn := -1
	var lastInputTokens int
	for i := 0; i < 40 && compactedAtTurn < 0; i++ {
		full = append(full, Message{Role: RoleUser, Content: fmt.Sprintf(
			"Turn %d: repeat the number %d back to me and name one short unique fact about it.", i, i)})

		newFull, send, err := PrepareTurn(ctx, client, nil, sys, full, reservedOutput, window)
		if err != nil {
			t.Fatalf("PrepareTurn at turn %d: %v", i, err)
		}
		if len(newFull) > len(full) {
			compactedAtTurn = i
			t.Logf("compaction fired at turn %d: full grew %d -> %d entries; summary=%q",
				i, len(full), len(newFull), newFull[len(newFull)-1].Content)
		}
		full = newFull

		turn, err := client.Send(ctx, send, nil)
		if err != nil {
			t.Fatalf("Send at turn %d: %v", i, err)
		}
		tokens, err := client.InputTokens(ctx, send, nil)
		if err != nil {
			t.Fatalf("InputTokens at turn %d: %v", i, err)
		}
		lastInputTokens = tokens
		t.Logf("turn %d: input_tokens=%d assistant=%q", i, tokens, turn.Content)
		full = append(full, Message{Role: RoleAssistant, Content: turn.Content, Reasoning: turn.Reasoning})
	}
	if compactedAtTurn < 0 {
		t.Fatalf("never compacted after 40 turns (last input_tokens=%d, window=%d)", lastInputTokens, window)
	}

	// One more turn AFTER compaction, to prove the conversation still answers coherently and
	// BuildSendMessages' post-compaction slice (system + summary + this turn) is accepted.
	full = append(full, Message{Role: RoleUser, Content: "What number did I mention in my very first message to you?"})
	_, send, err := PrepareTurn(ctx, client, nil, sys, full, reservedOutput, window)
	if err != nil {
		t.Fatalf("PrepareTurn after compaction: %v", err)
	}
	turn, err := client.Send(ctx, send, nil)
	if err != nil {
		t.Fatalf("Send after compaction: %v", err)
	}
	t.Logf("post-compaction reply: %q", turn.Content)
	if turn.Content == "" {
		t.Fatal("post-compaction reply was empty — conversation looks broken after compaction")
	}
}
