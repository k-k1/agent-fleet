package routes

import "testing"

func TestParseBedrockModelID(t *testing.T) {
	good := map[string][2]string{ // id -> scope, provider
		"anthropic.claude-opus-4-6-v1":                     {"", "anthropic"},
		"us.anthropic.claude-opus-5-5":                     {"us", "anthropic"},
		"eu.anthropic.claude-sonnet-4-5-20250929-v1:0":     {"eu", "anthropic"},
		"apac.anthropic.claude-sonnet-4-5-20250929-v1:0":   {"apac", "anthropic"},
		"jp.anthropic.claude-haiku-4-5-20251001-v1:0":      {"jp", "anthropic"},
		"us-gov.anthropic.claude-3-7-sonnet-20250219-v1:0": {"us-gov", "anthropic"},
		"global.anthropic.claude-opus-5-5":                 {"global", "anthropic"},
		"anthropic.claude-3-5-sonnet-20240620-v1:0:200k":   {"", "anthropic"},
		"global.openai.gpt-oss-120b-1:0":                   {"global", "openai"},
	}
	for id, want := range good {
		m, err := ParseBedrockModelID(id)
		if err != nil || m.ID != id || m.Scope != want[0] || m.Provider != want[1] {
			t.Errorf("ParseBedrockModelID(%q) = %+v, %v; want scope %q provider %q", id, m, err, want[0], want[1])
		}
	}
	bad := []string{
		"", " us.anthropic.claude-opus-5-5", "opus", "claude-opus-5-5", "claude-opus-5-5[1m]",
		"us.anthropic.claude-opus-5-5[1m]",
		"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-5-5",
		"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc123",
		"application-inference-profile/abc123", "us.anthropic.claude-opus-5-5:x", "anthropic.claude-v1:0:9q",
		"Anthropic.Claude-Opus", "us.anthropic", "global.anthropic", "anthropic..claude", "anthropic.claude opus",
		"anthropic.claude-opus-5-5:", ".anthropic.claude",
	}
	for _, id := range bad {
		if m, err := ParseBedrockModelID(id); err == nil {
			t.Errorf("ParseBedrockModelID(%q) = %+v, want an error", id, m)
		}
	}
}

func TestParseBedrockAnthropicModelID(t *testing.T) {
	if _, err := ParseBedrockAnthropicModelID("us.anthropic.claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"global.openai.gpt-oss-120b-1:0", "us.meta.llama3-1-70b-instruct-v1:0", "claude-opus-5-5"} {
		if _, err := ParseBedrockAnthropicModelID(id); err == nil {
			t.Errorf("ParseBedrockAnthropicModelID(%q) accepted a model that is not Anthropic's on Bedrock", id)
		}
	}
}
