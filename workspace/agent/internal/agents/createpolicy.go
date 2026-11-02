package agents

// CreatePolicy is an optional Agent capability: what a create of this kind is checked against
// before any side effect. A kind without it has no gate and no contributor mode. Resolved by
// exact registry key, never through the claude fallback.
//
// The traits name ui-prefs keys rather than carrying values: this package cannot import uiprefs,
// and the pref must be read at the moment of the create, not captured at registration.
type CreatePolicy interface {
	CreateTraits() CreateTraits
}

// CreateTraits is a kind's create-time policy.
type CreateTraits struct {
	// EnabledPref names the member's on/off display setting for the kind (a ui-prefs key). When
	// that setting reads false the create is refused 403 with DisabledCode and DisabledMessage.
	// Empty: the kind cannot be switched off.
	EnabledPref     string
	DisabledCode    string
	DisabledMessage string
	// RequiresModel refuses a create whose model is blank (400 bad_model) with
	// ModelRequiredMessage: the kind has no CLI-picked default of its own.
	RequiresModel        bool
	ModelRequiredMessage string
	// ContributorModePref names the ui-prefs key that opts the session into the vendor's
	// contributor mode. It is read from the member's setting only, never the request. Empty:
	// the kind has no such mode.
	ContributorModePref string
}
