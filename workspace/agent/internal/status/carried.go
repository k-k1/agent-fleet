package status

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// 持ち越し（carried interaction）— docs/75 §75.6。
//
// pending-question / pending-plan / pending-perm は「**今まさにモーダルが出ている**」と
// いう意味で、Console はそれをキー列（Down/Enter）で答える。セッションが畳まれると
// そのモーダルは二度と戻らない — claude を --resume しても未応答の tool_use は親ポインタで
// 迂回され、会話木から外れる（docs/75 §75.10 A で実測）。つまり畳んだ後に残せるのは
// **モーダルではなく意図**だけで、答えは文章として注入するしかない。
//
// だから同じファイルを使い回さない。停止中のカードが生きたペインへ Down/Enter を撃つ
// 事故（AUQ 誤配達クラス）を型で防ぐため、**別ストア・別 wire キー**にする:
// pending-* があれば「キーで答えろ」、carried-interaction があれば「文章で答えろ」。
type Carried struct {
	// Kind は "question" | "plan" | "permission"。question が plan に優先する
	// （EffectiveModal と同じ順序）。
	Kind string `json:"kind"`
	// CapturedAt は畳んだ時刻（RFC3339）。TTL の起点。
	CapturedAt string `json:"capturedAt"`
	// Reason は畳まれた経緯: "halt"（tier1 / 利用者の停止）| "stopped"（ペインが
	// 消えているのを一覧が見つけた＝ Workspace 停止・クラッシュ・利用者の /exit）。
	Reason string `json:"reason,omitempty"`
	// Questions は AskUserQuestion の tool_input.questions を生のまま。
	Questions json.RawMessage `json:"questions,omitempty"`
	// Plan は ExitPlanMode の計画本文。★保留中のプランは転写に載らない
	// （docs/75 §75.10 D の実測）ので、これが唯一の記録になる。
	Plan string `json:"plan,omitempty"`
	// Permission は許可を求めていたツールの説明（"Bash · npm ci"）。答えは死んだ
	// ツール呼び出しには届かないので、これは**事実の記録**であって回答対象ではない。
	Permission string `json:"permission,omitempty"`
	// Text は質問直前の地の文（pending-text）。カードの文脈。
	Text string `json:"text,omitempty"`
}

// CarriedTTL は持ち越しの寿命。過ぎたものは PromoteCarried / ReadCarried の入口で捨てる。
//
// なぜ寿命が要るか: pending-* には寿命が無く、実開発機には 5〜6 週間前の未回答
// ペイロードが残っていた（docs/75 D9）。sid は決定論なので、同じ dir+name の
// セッションが将来また作られれば亡霊のカードが surface しうる。
const CarriedTTL = 14 * 24 * time.Hour

func carriedFresh(c Carried, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, c.CapturedAt)
	if err != nil {
		return false // 時刻が読めない＝いつのものか言えない。持ち越さない
	}
	return now.Sub(t) < CarriedTTL
}

// PromoteCarried は「モーダルが出たまま畳まれた」を持ち越しへ昇格させる。
// 昇格したら true。
//
// 呼ばれるのは 3 箇所（docs/75 §75.6.3）。halt は status.Remove がペイロードを消す直前、
// 一覧はペインが消えているのを**初めて見つけた**とき（＝Workspace 停止・クラッシュ・
// 利用者の /exit をまとめて拾う）、SessionStart(boot) は再開時の消去の直前。3 つ目は
// 保険で、SIGKILL のように 1 も 2 も走らなかった経路でも消える前に拾える。
//
// 冪等ではあるが**上書きはしない**: 既に鮮度のある持ち越しがあるならそれを残す。
// 再開の boot フックが「消す前に昇格」する都合上、halt で昇格済みのものを
// 空の pending で上書きしてしまう順序があるため。
func PromoteCarried(sid, reason string) bool {
	if sid == "" {
		return false
	}
	if prev, ok := ReadCarried(sid); ok && prev.Kind != "" {
		return false
	}
	c := Carried{CapturedAt: time.Now().Format(time.RFC3339), Reason: reason}
	if q, ok := ReadPendingQuestion(sid); ok && len(q) > 0 {
		c.Kind = "question"
		c.Questions = append(json.RawMessage(nil), q...)
	} else if p, ok := ReadPendingPlan(sid); ok && strings.TrimSpace(p) != "" {
		c.Kind = "plan"
		c.Plan = p
	} else if pm, ok := ReadPendingPermission(sid); ok && strings.TrimSpace(pm) != "" {
		c.Kind = "permission"
		c.Permission = pm
	} else {
		return false
	}
	if txt, ok := ReadPendingText(sid); ok {
		c.Text = strings.TrimSpace(txt)
	}
	if err := carriedFiles.Write(sid, c); err != nil {
		return false
	}
	return true
}

// ReadCarried returns the carried interaction, dropping (and deleting) one that has
// outlived CarriedTTL.
func ReadCarried(sid string) (Carried, bool) {
	c, ok := carriedFiles.Read(sid)
	if !ok || c.Kind == "" {
		return Carried{}, false
	}
	if !carriedFresh(c, time.Now()) {
		carriedFiles.Remove(sid)
		return Carried{}, false
	}
	return c, true
}

func RemoveCarried(sid string) { carriedFiles.Remove(sid) }

// SweepCarried drops every carried entry past its TTL. Called at agent start —
// the store is small (one small file per session that was ever halted mid-modal),
// but nothing else would ever delete an entry whose session is gone.
func SweepCarried() int {
	dir := carriedFiles.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	dropped := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		if c, ok := carriedFiles.Read(sid); !ok || !carriedFresh(c, now) {
			carriedFiles.Remove(sid)
			dropped++
		}
	}
	return dropped
}
