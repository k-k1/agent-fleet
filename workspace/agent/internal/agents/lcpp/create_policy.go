package lcpp

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

var _ agents.CreatePolicy = agentImpl{}

// CreateTraits is agents.CreatePolicy: lcpp can be switched off in Settings (ui-prefs
// lcppEnabled, docs/log/105 §106.2) and never launches without an explicit model (docs/log/109;
// a router deployment dispatches on the id and the Console records it as the conversation's
// model).
func (agentImpl) CreateTraits() agents.CreateTraits {
	return agents.CreateTraits{
		EnabledPref:          "lcppEnabled",
		DisabledCode:         "lcpp_disabled",
		DisabledMessage:      "llama.cpp は設定でオフになっています。設定 > エージェント > llama.cpp でオンにしてください。",
		RequiresModel:        true,
		ModelRequiredMessage: "llama.cpp セッションにはモデルの指定が必須です（CLI 自身の既定は存在しません）",
	}
}
