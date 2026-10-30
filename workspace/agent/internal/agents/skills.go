package agents

// SkillsLayouter is an optional Agent capability: where the kind's CLI discovers the skills and
// commands a member can invoke, which the session skill picker, the launch modal's first-prompt
// list and the repository Skills view are built from (sessionx/session_skills.go). A kind
// without it offers no skills at all (shell, ssm). It is resolved by exact registry key.
type SkillsLayouter interface {
	SkillsLayout() SkillsLayout
}

// SkillsNative is where a kind's own (native) skill list comes from.
type SkillsNative int

const (
	// SkillsForeignOnly: no user-invocable mechanism of its own is confirmed; the picker offers
	// the other conventions' SKILL.md trees as foreign entries only.
	SkillsForeignOnly SkillsNative = iota
	// SkillsScan: the project roots and then the user roots, read from the filesystem.
	SkillsScan
	// SkillsAdvertised: the list the running CLI advertised (agents.AdvertisedCommands), which
	// is complete; until it arrives, the project roots alone.
	SkillsAdvertised
	// SkillsHosted: the session's own live host answers the list (muse's skill/list); with no
	// live host there is no native list, and NativeConvs go back to being offered as foreign.
	SkillsHosted
)

// SkillDirs is which directories a kind's project roots are read in, deepest first.
type SkillDirs int

const (
	// SkillDirsWorkingCopy: the working copy root alone.
	SkillDirsWorkingCopy SkillDirs = iota
	// SkillDirsCWDToHome: the CWD and every ancestor below $HOME (claude).
	SkillDirsCWDToHome
	// SkillDirsCWDToGitRoot: the CWD up to the nearest git root, the CWD alone outside one
	// (codex).
	SkillDirsCWDToGitRoot
)

// SkillsLayout describes one kind's skill discovery. Forms are "skills" (<dir>/*/SKILL.md) or
// "commands" (<dir>/**/*.md); sources are "project", "user" or "cli".
type SkillsLayout struct {
	Native SkillsNative
	// Invoke is the composer's invocation prefix for a native entry ("/" or "$").
	Invoke string
	// Project are the repository's roots, relative to each project directory, in the CLI's
	// precedence order. A directory above the working copy is labelled "user".
	Project     []SkillRoot
	ProjectDirs SkillDirs
	// User are the member's own roots, scanned after the project ones.
	User []UserSkillRoot
	// Bundled: the CLI ships skills of its own, added as "cli" entries (claude's init probe).
	Bundled bool
	// NativeConvs are the SKILL.md conventions the CLI reads itself, so the picker does not
	// offer them again as foreign entries.
	NativeConvs []string
	// Shared is the kind's user root that other kinds' sessions are offered as foreign entries;
	// nil when it holds nothing worth offering.
	Shared *SharedSkillRoot
	// FenceToCWD: the kind reads files only below the session's CWD (lcpp's harness), so a
	// foreign entry outside it is dropped and no other kind's user root is offered.
	FenceToCWD bool
}

// SkillRoot is a project root relative to a project directory.
type SkillRoot struct{ Rel, Form string }

// UserSkillRoot is a member-level root; Dir is read on every request.
type UserSkillRoot struct {
	Dir          func() string
	Source, Form string
}

// SharedSkillRoot is a user root offered to other kinds: Origin is the badge (".claude").
type SharedSkillRoot struct {
	Origin string
	Dir    func() string
}
