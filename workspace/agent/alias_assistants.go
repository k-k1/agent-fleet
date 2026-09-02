package main

// internal/assistants へ移したアシスタント層の別名（ADR 0067 のエイリアス移送）。
// 呼び出し側（chat 家系・bridge_operator・assistants_test）は 1 行も触らない。
// 回収はウェーブ境界の別セッションが行う。
//
// 遠側はすべて **func / type / const**。`var x = pkg.Y` が写しになる罠は遠側が var の
// ときだけなので、ここに該当は無い（assistants パッケージの var は下のフック 2 本のみで、
// それらは別名にせず init で書き込んでいる）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"

type assistant = assistants.Assistant

const (
	toolsNone    = assistants.ToolsNone
	toolsAFRead  = assistants.ToolsAFRead
	toolsAFWrite = assistants.ToolsAFWrite

	afAssistantID     = assistants.AFAssistantID
	operatorPersona   = assistants.OperatorPersona
	operatorPersonaEN = assistants.OperatorPersonaEN
)

var (
	validToolGrant   = assistants.ValidToolGrant
	validIntegration = assistants.ValidIntegration
	personaFor       = assistants.PersonaFor

	assistantsDir     = assistants.Dir
	assistantPath     = assistants.PathFor
	loadUserAssistant = assistants.LoadUser
	saveUserAssistant = assistants.SaveUser
)

// assistantDeps は main にしか置けない 2 つ（//go:embed のナレッジと chat 家系の
// 既定エージェント）を assistants へ**引数で**渡す唯一の場所。
//
// 🔥 最初はパッケージ変数のフックに init で代入していたが、レビューの変異試験で
// **その 2 行を消しても main のテストが全部緑**になった（develop ではコンパイラが強制していた
// 依存が、移送で「無言で外せる実行時代入」に化けていた）。
// 公開フィールドの struct でも同じで、**片方を書き落としてコンパイルが通る**（自分で実測した）。
// 引数 2 つの NewDeps にして初めて、渡し忘れがコンパイルエラーになる。
// **この関数を経由しない assistants 呼び出しを増やさないこと。**
func assistantDeps() assistants.Deps {
	return assistants.NewDeps(
		ensureBuiltinKnowledge, // //go:embed が main に残るため
		preferredHeadlessAgent, // chat 家系にあるため
	)
}

// Deps を取る 6 つだけは薄いラッパにする（呼び出し側の綴りは変えない）。
// 回収セッションへ: これらは `= assistants.X` の形をしていないので、
// `grep -rn '= assistants\.[A-Z]'` だけでは拾えない。`grep -rn 'assistants\.'` で見ること。
func builtinAssistants() []assistant             { return assistants.Builtins(assistantDeps()) }
func isBuiltinID(id string) bool                 { return assistants.IsBuiltinID(id, assistantDeps()) }
func listUserAssistants() []assistant            { return assistants.ListUser(assistantDeps()) }
func listAssistants() []assistant                { return assistants.List(assistantDeps()) }
func getAssistant(id string) (*assistant, error) { return assistants.Get(id, assistantDeps()) }
func resolveAssistant(name string) (*assistant, error) {
	return assistants.Resolve(name, assistantDeps())
}
