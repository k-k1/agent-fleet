package agents

// UsageMeasurer is an optional Agent capability: how completely the kind's own transcript or
// engine reports token use (usagex.MeasuredExact / MeasuredPartial / MeasuredNone, docs/log/46
// §1-c). It stops the 0 of an agent that reports no tokens from reading as "consumed nothing",
// and the UI shows unmeasured consumption separately. Every registered kind declares it,
// "none" included (TestEveryKindDeclaresItsUsageAccuracy): a kind that said nothing would
// silently read none. Resolved by exact registry key, never through the claude fallback.
type UsageMeasurer interface {
	UsageMeasured() string
}

// NoHeadlessChat is an optional Agent marker: the kind runs no headless chat / one-shot backend,
// so chatx has no provider for it. Every registered kind either has a chatx provider or carries
// this marker, never both (TestEveryRegisteredKindHasAChatProviderOrIsExcluded); the backends
// themselves are chatx's code, which an agent package cannot import, so only the exclusion is
// declared here.
type NoHeadlessChat interface {
	NoHeadlessChat()
}
