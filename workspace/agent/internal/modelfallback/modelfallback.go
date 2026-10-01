// Package modelfallback owns every concrete model id the Agent pins in code. Each one is a
// fallback or a fixed preference that the live catalog cannot supply, and each will go stale
// when the vendor moves on — so they live here, with who owns them, where the value came
// from and why discovery cannot provide it, instead of scattered through the features.
//
// scripts/model-id-lint fails CI on a model-id-shaped literal anywhere else in the Go
// modules. Adding an id: put a constant and an Entries row here, and read the constant from
// the feature. The precedence around each value (environment override, user settings,
// catalog) stays with the caller; this package only names the value.
package modelfallback

// Assistant chat (internal/chatx). The user's per-assistant model and Settings > Assistant
// models always win; these apply only when nothing is pinned.
const (
	// ChatClaude is the assistant chat's claude model when nothing pins one. Overridden
	// deployment-wide by AF_CHAT_MODEL.
	ChatClaude = "claude-sonnet-5"
	// ChatCodex is the codex assistant default when the live catalog cannot be read; with a
	// catalog the newest "-luna" it lists is used instead.
	ChatCodex = "gpt-5.6-luna"
	// ChatOpencode is the opencode assistant default for an account that does not list
	// ChatOpencodeGo: a free model every opencode account can run.
	ChatOpencode = "opencode/nemotron-3-ultra-free"
	// ChatOpencodeGo is preferred over ChatOpencode only when the account's catalog lists it
	// (an OpenCode Go subscription).
	ChatOpencodeGo = "opencode-go/glm-5.2"
	// OneShotOpencodeGo is the opencode pick for one-shot calls (titles, reply chips) when
	// the account's catalog lists it; otherwise the CLI's own default runs.
	OneShotOpencodeGo = "opencode-go/deepseek-v4-flash"
	// ChatAgy is the agy assistant default, in `agy models` display-name syntax. A name the
	// live catalog no longer lists is dropped at send time, so a rename upstream degrades to
	// agy's own default rather than an error.
	ChatAgy = "Gemini 3.5 Flash (Medium)"
)

// Image generation (internal/imagegen). Both are the DRIVER model of the turn, not the image
// model, which the CLI's built-in tool chooses itself.
const (
	// ImagegenCodexDriver is overridden deployment-wide by AF_IMAGEGEN_CODEX_MODEL.
	ImagegenCodexDriver = "gpt-5.4-mini"
	// ImagegenAgyDriver is overridden deployment-wide by AF_IMAGEGEN_AGY_MODEL.
	ImagegenAgyDriver = "gemini-3.8-flash-low"
)

// Entry documents one pinned id.
type Entry struct {
	ID string
	// Kind is the agent kind whose CLI receives the id.
	Kind string
	// Owner is the feature that reads it — the place to ask before changing it.
	Owner string
	// Source is where the value came from.
	Source string
	// WhyNotDiscovered says why the live catalog cannot supply it.
	WhyNotDiscovered string
}

// Entries lists every constant above. A test keeps it complete and filled in.
var Entries = []Entry{
	{
		ID: ChatClaude, Kind: "claude", Owner: "assistant chat (chatx.chatModel)",
		Source: "product choice: Sonnet keeps assistant chats fast and cheap (feat(chat) 63e6775b7)",
		WhyNotDiscovered: "Claude Code over OAuth has no account-linked catalog; the launch picker only offers tier aliases, " +
			"and a conversation snapshots a concrete id",
	},
	{
		ID: ChatCodex, Kind: "codex", Owner: "assistant chat (chatx.codexNewestLuna)",
		Source:           "product choice: the high-volume Luna tier for conversation (feat(chat) 92d298584)",
		WhyNotDiscovered: "used only when the codex catalog cannot be read; with a catalog the newest -luna it lists wins (#972)",
	},
	{
		ID: ChatOpencode, Kind: "opencode", Owner: "assistant chat (chatx.recommendedAssistantModelV)",
		Source:           "product choice: a free model every opencode account can run (feat(chat) 172c907f9)",
		WhyNotDiscovered: "the catalog lists models but says nothing about which one suits chat or is an entitlement",
	},
	{
		ID: ChatOpencodeGo, Kind: "opencode", Owner: "assistant chat (chatx.recommendedAssistantModelV)",
		Source:           "product choice for OpenCode Go accounts (feat(chat) 5a28f6c32)",
		WhyNotDiscovered: "a preference among listed models; the catalog has no notion of a recommended row",
	},
	{
		ID: OneShotOpencodeGo, Kind: "opencode", Owner: "one-shot calls: titles, branch names, reply chips (chatx.oneShotRecommendation)",
		Source: "product choice for OpenCode Go accounts (feat(chat) 5a28f6c32)",
		WhyNotDiscovered: "opencode is deliberately not price-ranked: its cheapest rows are short-lived -free promotions " +
			"that are listed but not entitlements",
	},
	{
		ID: ChatAgy, Kind: "agy", Owner: "assistant chat and agy one-shot fallback (chatx.agyNamedModel)",
		Source:           "product choice: Gemini Flash is the quota-cheapest agy model on Starter (docs/log/32 Track D)",
		WhyNotDiscovered: "a preference among listed models; dropped at send time when the catalog no longer lists it",
	},
	{
		ID: ImagegenCodexDriver, Kind: "codex", Owner: "image generation, codex route (imagegen.newCodexProvider)",
		Source:           "measured 2026-09-06: the cheapest tier that drives image_gen reliably (ADR 0069 P0)",
		WhyNotDiscovered: "the catalog does not say which model can drive the built-in image tool",
	},
	{
		ID: ImagegenAgyDriver, Kind: "agy", Owner: "image generation, agy route (imagegen.newAgyProvider)",
		Source:           "measured 2026-09-07 with agy 1.1.5: the cheapest capable driver (ADR 0069)",
		WhyNotDiscovered: "the catalog does not say which model can drive the built-in image tool",
	},
}

// Is reports whether id is one of the pinned values above.
func Is(id string) bool {
	for _, e := range Entries {
		if e.ID == id {
			return true
		}
	}
	return false
}
