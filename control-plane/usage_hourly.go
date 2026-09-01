package main

import (
	"net/http"
	"time"
)

// 稼働時間ヒートマップの API（docs/log/83）。縦 24 時間 × 横 日付のマスを描くための、
// 1 時間 1 バケットの占有データを返す。
//
// ⚠️ **これは金額ではない。** 実費（Cost Explorer）は日単位でしか取れないので、時間別の
// 金額は「秒 × 誰かが一度打ち込んだ単価」＝見積にしかならず、ADR 0048 決定 2 がまさに
// それを否定している。ヒートマップは請求書の 1 日を **説明する**（何時に何本動いていたか）
// のであって、値段を付けない。だから置き場所も「クラウド費用」ではなく「稼働時間」。
//
// ⚠️ 応答の形は 3 つの入口で同一（本人・テナント合算・メンバー詳細）。クラウド費用が
// `/api/cost/me` と管理側で同じ形にして部品を 1 つで済ませたのと同じ理由で、DTO を
// 増やさない — 形が 2 つあると、片方だけ直った日から静かに食い違う。

// usageHourWindow は要求された日付範囲を UTC の時バケット境界へ広げる。
//
// ⚠️ 引数は日付（YYYY-MM-DD, UTC）で、返すのは [from T00, to T23]。**ローカル時刻へ
// ずらすのは描画側**なので、クライアントは見せたい範囲より前後 1 日ぶん広く要求する
// 必要がある（+09:00 なら UTC の 15:00 が翌日 00:00 のマスになる）。ここでずらさないのは、
// 時計を持っているのがブラウザだけだからで、サーバが推測したタイムゾーンで刻んだ
// バケットは「誰の時計でも合っていない」表になる。
func usageHourWindow(r *http.Request, now time.Time) (fromDay, toDay, fromHour, toHour string, aerr *apiError) {
	// 既定は 14 日。usageRange の 30 日を借りないのは、30 列 × 24 段だと日付ラベルが
	// 重なって読めなくなるから（クラウド費用の棒グラフで実際に踏んだ・docs/log/67）。
	fromDay = now.AddDate(0, 0, -(usageHourlyDefaultDays - 1)).Format(usageDayFmt)
	toDay = now.Format(usageDayFmt)
	for _, p := range []struct {
		name string
		dst  *string
	}{{"from", &fromDay}, {"to", &toDay}} {
		v := r.URL.Query().Get(p.name)
		if v == "" {
			continue
		}
		if _, err := time.Parse(usageDayFmt, v); err != nil {
			return "", "", "", "", &apiError{http.StatusBadRequest, "bad_request", p.name + " must be YYYY-MM-DD"}
		}
		*p.dst = v
	}
	if fromDay > toDay {
		return "", "", "", "", &apiError{http.StatusBadRequest, "bad_request", "from must not be after to"}
	}
	return fromDay, toDay, fromDay + "T00", toDay + "T23", nil
}

// usageHourPoint は 1 メンバーの 1 時間。ゼロ値は omitempty で落とす — 720 マスぶんの
// 密な配列を返す設計ではないが、止まっていた時間まで数値 7 個で埋めると応答が無駄に太る。
type usageHourPoint struct {
	Hour string `json:"hour"` // YYYY-MM-DDTHH (UTC)
	UsageHourCounters
}

// usageHourMember は 1 メンバーの系列。**時間の行は稼働していた時間だけ**入る
// （止まっていた時間は行が無い）。「観測していない」との区別は下の Observed が持つ。
type usageHourMember struct {
	Tenant  string           `json:"tenant"`
	UserKey string           `json:"user_key"`
	Email   string           `json:"email"`
	Hours   []usageHourPoint `json:"hours"`
}

// usageHourlyResponse は 3 つの入口すべての応答。
//
// ⚠️ Observed が**この API の要**。マスは 3 値（停止 / 稼働 / 未観測）で、
// 「サンプラが動いていた時間」を別に持たないと、CP が落ちていた日と誰も働かなかった日が
// 同じ灰色になる。Members に行が無い時間は、Observed に載っていれば「止まっていた」、
// 載っていなければ「分からない」。
// ⚠️ Observed は時刻の一覧ではなく**点**である。samples を載せるのは、それがマスの
// **分母**だから: 1 時間まるごとを 3600 秒と決め打つと、まだ途中の「今の時間」が必ず
// 薄くなるし、CP が途中で落ちた時間も薄くなる。「見ていた時間のうちどれだけ動いていたか」
// でなければ、色は稼働ではなく観測の欠けを表してしまう。
type usageHourlyResponse struct {
	From         string            `json:"from"`
	To           string            `json:"to"`
	IntervalSecs int               `json:"interval_secs"`
	Observed     []usageHourPoint  `json:"observed"`
	Members      []usageHourMember `json:"members"`
}

// buildUsageHourly は store の行を応答へ畳む。純関数 — サンプラの都合も HTTP の都合も
// 入れない（テストがここだけを撃てる）。
//
// ⚠️ membership_id=="" の行はメンバーではなくサンプラのハートビート。Members へ混ぜると
// 「名前の無いメンバー」が全テナントの一覧に現れる。
func buildUsageHourly(rows []UsageHourRow, from, to string, intervalSecs int) usageHourlyResponse {
	out := usageHourlyResponse{
		From: from, To: to, IntervalSecs: intervalSecs,
		Observed: []usageHourPoint{}, Members: []usageHourMember{},
	}
	idx := map[string]int{}
	for _, r := range rows {
		if r.MembershipID == "" {
			out.Observed = append(out.Observed,
				usageHourPoint{Hour: r.Hour, UsageHourCounters: UsageHourCounters{Samples: r.Samples}})
			continue
		}
		// 予約テナント（af-golden など）の稼働は人の稼働ではない。一覧から隠しておいて
		// ヒートマップにだけ現れると、隠した意味が無くなる（usage.go の同じ判断）。
		if isSystemTenantSlug(r.TenantSlug) {
			continue
		}
		i, ok := idx[r.MembershipID]
		if !ok {
			i = len(out.Members)
			idx[r.MembershipID] = i
			out.Members = append(out.Members, usageHourMember{
				Tenant: r.TenantSlug, UserKey: r.UserKey, Email: r.Email,
			})
		}
		out.Members[i].Hours = append(out.Members[i].Hours,
			usageHourPoint{Hour: r.Hour, UsageHourCounters: r.UsageHourCounters})
	}
	return out
}

// usageSampleInterval は 1 サンプルが表す秒数。マスの「その時間のうち何割動いていたか」は
// running_secs ÷ (samples × interval) で出すので、クライアントが分母を組み立てられるよう
// 応答に載せる。⚠️ 運用者が AF_USAGE_SAMPLE_INTERVAL を変えれば変わる値なので、
// Console 側に定数として写さない。
func (a adminAPI) usageSampleInterval() int {
	return int(a.mgr.usageInterval.Seconds())
}

// usageHourlyFor は 3 つの入口の共通部分。tenantID=="" は全テナント、
// membershipID!="" は 1 人だけに絞る。
func (a adminAPI) usageHourlyFor(w http.ResponseWriter, r *http.Request, tenantID, membershipID string) {
	fromDay, toDay, fromHour, toHour, aerr := usageHourWindow(r, time.Now().UTC())
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	rows, err := a.mgr.store.ListUsageHourly(r.Context(), tenantID, fromHour, toHour)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if membershipID != "" {
		kept := rows[:0:0]
		for _, row := range rows {
			// ハートビート行は誰の物でもないので残す — 1 人ぶんの画面でも
			// 「観測していない時間」は同じように空白でなければならない。
			if row.MembershipID == "" || row.MembershipID == membershipID {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	writeJSON(w, http.StatusOK, buildUsageHourly(rows, fromDay, toDay, a.usageSampleInterval()))
}

// myUsageHourly (GET /api/usage/me/hourly?from=&to=) — 本人の稼働だけ。
//
// ⚠️ 他人の合計を引き算で復元できる値を返さない（/api/cost/me と同じ作法）。返すのは
// 本人の行とハートビートだけで、デプロイ全体の合計はどこにも入っていない。
func (a adminAPI) myUsageHourly(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	// ⚠️ テナントで絞らない。破棄済みワークスペースの行も本人のものであり、
	// 絞ると「自信たっぷりの空っぽ」を本人に見せることになる（docs/log/67 §67.15 と同型）。
	a.usageHourlyFor(w, r, "", mv.MembershipID)
}

// adminUsageHourly (GET /api/admin/usage/hourly?tenant=&from=&to=) — 管理の合算ヒートマップ。
//
// 合算そのものではなく**メンバー別の系列**を返し、合計はクライアントで積む。ホバーの内訳が
// 同じデータから出る（別 API を叩かない）＝「合計と内訳が食い違う」が原理的に起きない。
func (a adminAPI) adminUsageHourly(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	a.usageHourlyFor(w, r, tenantID, "")
}

// memberUsageHourly (GET /api/admin/tenants/{slug}/members/{key}/usage-hourly) —
// メンバー詳細の 1 枚。強制停止・ディスク上限のボタンが並ぶ面に置く（費用と同じ理由で、
// 「週末も動きっぱなし」がそのまま隣のボタンの根拠になる）。
//
// ⚠️ 費用と同じく **membership だけで引く**。所属は tenantAdminFor + resolveMember が
// 証明済みで、テナントでも絞ると破棄済みワークスペースぶんが落ちる。
func (a adminAPI) memberUsageHourly(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.tenantAdminFor(w, r, r.PathValue("slug")); !ok {
		return
	}
	mem, _, _, aerr := a.resolveMember(r, r.PathValue("slug"), r.PathValue("key"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.usageHourlyFor(w, r, "", mem.ID)
}

// usageHourlyDefaultDays は既定の窓。30 日 × 24 段は横に長すぎて日付ラベルが潰れる
// （クラウド費用の棒グラフで実際に踏んだ）ので、ヒートマップの既定は 14 日にする。
const usageHourlyDefaultDays = 14
