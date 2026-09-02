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

	builtinAssistants = assistants.Builtins
	isBuiltinID       = assistants.IsBuiltinID

	assistantsDir      = assistants.Dir
	assistantPath      = assistants.PathFor
	loadUserAssistant  = assistants.LoadUser
	saveUserAssistant  = assistants.SaveUser
	listUserAssistants = assistants.ListUser
	listAssistants     = assistants.List
	getAssistant       = assistants.Get
	resolveAssistant   = assistants.Resolve
)

// main にしか無いものをフックで渡す（internal/agents の opencode.UsagePref /
// mcpreg.PeerMessagingEnabled と同じ形）。どちらもリクエスト時にしか呼ばれないので
// init 順に依存しない。
func init() {
	assistants.KnowledgeDir = ensureBuiltinKnowledge // //go:embed が main に残るため
	assistants.DefaultAgent = preferredHeadlessAgent // chat 家系にあるため
}
