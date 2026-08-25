package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/resources"
)

// handleWorkspaceStats（GET /workspace/stats）は **この Workspace 自身の**
// メモリ / CPU / ディスクを返す（docs/63 §63.9）。
//
// CP はまずホスト側の cgroup を直接読もうとし（docker / native 構成でだけ成立
// する）、読めなかったときにここへ落ちてくる。ECS（Fargate / `ecs-ec2`）では
// 常にこちらが答える経路になる。
//
// 読めなかった軸はキーごと落ちる。CP は返ってきたキーだけを載せるので、
// 「測れない」は Console のタイルで「–」のまま残る——0 として描かれない。
//
// ⚠️ 呼び出し元は CP だけ（`/api/workspace/stats` と `/api/events` の stats
// ストリーム、および管理者のメンバー詳細）。Console からの直叩き用に
// control-plane 側の proxy 許可リストへ足す必要は無い——CP 側の 2 つの口が
// 自分でこれを呼ぶ形になっている。
func handleWorkspaceStats(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, resources.Read())
}
