package mcpreg

// Builtin ops integrations (docs/25 Phase 1), normalized into ServerDef so the
// registry is ONE list instead of "builtin catalog or registered server" branching
// everywhere (ADR0031 決定 6).
//
// They stay code-defined and not editable: each is launched as
// `workspace-agent mcp-run <id>`, a wrapper that injects the user's stored key as
// env at spawn — so the credential never reaches an MCP config file. That indirection
// is the whole point of the builtins and is not something a user-registered stdio
// definition can express.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

const (
	BuiltinAF         = "af"
	BuiltinPagerDuty  = "pagerduty"
	BuiltinGrafana    = "grafana"
	BuiltinCloudWatch = "cloudwatch"
	BuiltinAWS        = "aws"
)

// builtinSpec is the code half of a builtin: the mcp-run subcommand and the check
// for "has the user connected the credential this needs".
type builtinSpec struct {
	label   string
	runArgs []string
	ready   func(*secrets.Data) bool
	// targets overrides the default (assistant-only) attachment. Zero value means
	// Targets{Assistant: true}.
	targets Targets
}

var builtinSpecs = map[string]builtinSpec{
	// af は Agent Fleet 自身のセッション向けサーバー（docs/51 Phase 3 §自己申告
	// ファストパス＋docs/53 §53.8 Chromium Attach View）。他の builtin と違って接続情報を持たないので常に ready で、
	// 向き先も逆 — アシスタントではなく**セッション**に配る。ここに置いたのは
	// 「レジストリは1つのリスト」という ADR0031 決定6 のため: 自前のサーバーだけ
	// materialize の外に別配線を持つと、利用者からも見えず、名前衝突の調停からも外れる
	// （"af" は reservedNames で元から押さえてある）。
	BuiltinAF: {
		label:   "Agent Fleet",
		runArgs: []string{"mcp-stdio", "--self-report", "--chromium-attach"},
		ready:   func(*secrets.Data) bool { return true },
		targets: Targets{Session: true},
	},
	BuiltinPagerDuty: {
		label:   "PagerDuty",
		runArgs: []string{"mcp-run", "pagerduty"},
		ready:   func(s *secrets.Data) bool { return s.PagerDuty != nil && s.PagerDuty.APIKey != "" },
	},
	BuiltinGrafana: {
		label:   "Grafana",
		runArgs: []string{"mcp-run", "grafana"},
		ready: func(s *secrets.Data) bool {
			return s.Grafana != nil && s.Grafana.URL != "" && s.Grafana.Token != ""
		},
	},
	BuiltinCloudWatch: {
		label:   "CloudWatch",
		runArgs: []string{"mcp-run", "cloudwatch"},
		ready:   func(s *secrets.Data) bool { return s.CloudWatch != nil && s.CloudWatch.Profile != "" },
	},
	// aws = Agent Toolkit for AWS の AWS MCP Server（docs/25 §AWS MCP）。他の builtin と
	// 違い、対話セッションにも配る: 中身が「AWS 上に作る」ための道具（ドキュメント検索・
	// スキル取得・API 呼び出し）で、使いたいのは相談チャットよりコードを書くセッションだから。
	// 危険側（書き込みツール）は接続設定の Write opt-in で閉じてある。
	BuiltinAWS: {
		label:   "AWS",
		runArgs: []string{"mcp-run", "aws"},
		ready:   func(s *secrets.Data) bool { return s.AWS != nil && s.AWS.Profile != "" },
		targets: Targets{Assistant: true, Session: true},
	},
}

// IsBuiltin reports whether id names a builtin integration.
func IsBuiltin(id string) bool {
	_, ok := builtinSpecs[id]
	return ok
}

// BuiltinRunArgs returns the subcommand args that launch a builtin's MCP server.
// Callers supply the executable themselves because it is not always this binary's
// own path: agy's chat runs from an isolated HOME and needs its own resolved exe.
func BuiltinRunArgs(id string) ([]string, bool) {
	spec, ok := builtinSpecs[id]
	if !ok {
		return nil, false
	}
	return append([]string(nil), spec.runArgs...), true
}

// BuiltinReady reports whether the user has configured the credential the builtin
// needs. An unconfigured builtin is simply absent from the effective registry, so an
// assistant holding it just has no ops tools (the pre-registry behavior).
func BuiltinReady(id string, s *secrets.Data) bool {
	spec, ok := builtinSpecs[id]
	if !ok || s == nil {
		return false
	}
	return spec.ready(s)
}

// builtinDefs returns the builtins the user has actually connected, as ServerDefs.
// The default Targets is Assistant-only on purpose: the ops three were assistant
// integrations before the registry existed, and quietly attaching them to every
// interactive session would be a behavior change nobody asked for. Session use goes
// through a user registration — or through a spec that opts in explicitly (af, aws),
// which is safe for those two because neither existed before the registry, so nobody
// has a session whose behavior changes underneath them.
func builtinDefs(s *secrets.Data) []ServerDef {
	var out []ServerDef
	for _, id := range []string{BuiltinAF, BuiltinPagerDuty, BuiltinGrafana, BuiltinCloudWatch, BuiltinAWS} {
		spec := builtinSpecs[id]
		if !spec.ready(s) {
			continue
		}
		targets := spec.targets
		if targets == (Targets{}) {
			targets = Targets{Assistant: true}
		}
		out = append(out, ServerDef{
			ID:        id,
			Name:      id,
			Label:     spec.label,
			Origin:    OriginBuiltin,
			Transport: TransportStdio,
			Command:   paths.ExePath(),
			Args:      append([]string(nil), spec.runArgs...),
			Enabled:   true,
			Targets:   targets,
		})
	}
	return out
}
