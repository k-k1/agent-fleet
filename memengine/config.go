package memengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Role is what the engine knows about a model: never an ID. A config file maps
// each role to a candidate, i.e. a runner and a model ID, so a retired model
// is replaced by editing data.
type Role string

const (
	// RoleSmall is the default for every operation.
	RoleSmall Role = "small"
	// RoleLarge takes the operations decision 6 escalates (supersede, merge).
	RoleLarge Role = "large"
)

// RunnerSpec is how one kind of model is reached. Argv is a one-shot command
// (prompt on stdin, JSON on stdout) in which "{model}" stands for the model ID.
// A Session runner has no command: its answers are made elsewhere (an agent
// session) and replayed, so it can be scored but never called.
type RunnerSpec struct {
	Argv    []string `json:"argv,omitempty"`
	Session bool     `json:"session,omitempty"`
}

// Candidate is a tier that can be evaluated and mapped to a role. Tier is
// "<runner>:<model id>". Cost is a relative price, only used to pick the
// cheapest candidate that meets the bar.
type Candidate struct {
	Tier string  `json:"tier"`
	Cost float64 `json:"cost"`
}

// Config is the whole model configuration.
type Config struct {
	Roles      map[Role]string       `json:"roles"`
	Runners    map[string]RunnerSpec `json:"runners"`
	Candidates []Candidate           `json:"candidates"`
}

// DefaultRunners are the runner templates known without a config file. They
// contain no model ID.
func DefaultRunners() map[string]RunnerSpec {
	return map[string]RunnerSpec{
		// CLAUDE_CODE_DISABLE_CLAUDE_MDS keeps the host's instruction files out of every call.
		"claude": {Argv: []string{"env", "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "claude", "-p", "--output-format", "json", "--tools", "",
			"--system-prompt", "You output only the JSON object asked for.", "--no-session-persistence", "--disable-slash-commands",
			"--strict-mcp-config", "--setting-sources", "", "--model", "{model}"}},
		"codex": {Argv: []string{"codex", "exec", "--model", "{model}", "--json", "--skip-git-repo-check", "--sandbox", "read-only"}},
	}
}

// LoadConfig reads a config file; runners it does not define fall back to
// DefaultRunners.
func LoadConfig(path string) (Config, error) {
	var c Config
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.Runners == nil {
		c.Runners = map[string]RunnerSpec{}
	}
	for k, v := range DefaultRunners() {
		if _, ok := c.Runners[k]; !ok {
			c.Runners[k] = v
		}
	}
	return c, nil
}

// SplitTier splits "<runner>:<model id>". The model ID is everything after the
// first colon.
func SplitTier(tier string) (runner, model string, ok bool) {
	runner, model, ok = strings.Cut(tier, ":")
	return runner, model, ok && runner != "" && model != ""
}

// Command builds the one-shot command for a tier. It fails, rather than
// choosing something else, for an unknown runner or a session-only one.
func (c Config) Command(tier string) (Command, error) {
	runner, model, ok := SplitTier(tier)
	if !ok {
		return Command{}, fmt.Errorf("memengine: tier %q is not <runner>:<model>", tier)
	}
	spec, ok := c.Runners[runner]
	if !ok {
		return Command{}, fmt.Errorf("memengine: no runner %q for tier %q", runner, tier)
	}
	if spec.Session || len(spec.Argv) == 0 {
		return Command{}, fmt.Errorf("memengine: runner %q is session-only: its answers are replayed, never called", runner)
	}
	argv := make([]string, len(spec.Argv))
	for i, a := range spec.Argv {
		argv[i] = strings.ReplaceAll(a, "{model}", model)
	}
	return Command{Argv: argv}, nil
}

// Model is the model mapped to a role.
func (c Config) Model(role Role) (Model, error) {
	tier, ok := c.Roles[role]
	if !ok {
		return nil, fmt.Errorf("memengine: no model is mapped to the %s role", role)
	}
	cmd, err := c.Command(tier)
	if err != nil {
		return nil, fmt.Errorf("memengine: role %s: %w", role, err)
	}
	return cmd, nil
}

// Cost is the configured relative cost of a tier; unknown tiers cost +Inf-ish
// (they never win a "cheapest" pick by accident).
func (c Config) Cost(tier string) float64 {
	for _, k := range c.Candidates {
		if k.Tier == tier {
			return k.Cost
		}
	}
	return 1e18
}

// Pipeline classifies with the small model and sends the answers decision 6
// escalates (supersede, merge) to the large one. There is no fallback: when a
// configured model is gone its command fails, the error comes back, and
// nothing is applied. Falling back to another model would change what the
// evaluation measured without anyone seeing it.
type Pipeline struct {
	Small, Large Model
}

// NewPipeline resolves both roles from the config.
func NewPipeline(c Config) (Pipeline, error) {
	s, err := c.Model(RoleSmall)
	if err != nil {
		return Pipeline{}, err
	}
	l, err := c.Model(RoleLarge)
	if err != nil {
		return Pipeline{}, err
	}
	return Pipeline{Small: s, Large: l}, nil
}

// Classify runs the small tier and, on an escalating answer, the large tier,
// whose answer then stands.
func (p Pipeline) Classify(ctx context.Context, req ClassifyRequest) (ClassifyAnswer, Role, error) {
	a, _, err := Classify(ctx, p.Small, req)
	if err != nil {
		return ClassifyAnswer{}, RoleSmall, err
	}
	if !a.Operation.Escalates() {
		return a, RoleSmall, nil
	}
	a, _, err = Classify(ctx, p.Large, req)
	return a, RoleLarge, err
}

// Patch runs a patch call on the small tier.
func (p Pipeline) Patch(ctx context.Context, req PatchRequest) (PatchAnswer, error) {
	a, _, err := Patch(ctx, p.Small, req)
	return a, err
}

// IsInvalid reports whether err is a dropped answer rather than a failed call.
func IsInvalid(err error) bool { return errors.Is(err, ErrInvalid) }

// Roles lists the roles in a stable order.
func Roles() []Role { return slices.Clone([]Role{RoleSmall, RoleLarge}) }
