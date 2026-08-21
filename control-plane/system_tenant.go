// system_tenant.go — テナントの形をしているが、人の入れ物ではない予約テナント。
//
// 今のところ 1 つだけある: golden スナップショットの自動焼き直しが使う `af-golden`
// （golden_bake.go）。中の `af-golden-seed` / `af-golden-probe` は**製品の通常の Start 経路**
// で workspace を作る — そうでなければ焼けた golden は「製品が実際に作る home」の複製では
// なくなる — ので、テナントも membership も本物の行として存在してしまう。
//
// その結果、管理画面には「golden snapshot (system)」というテナントが人のテナントと並び、
// 種と probe が人のメンバーとして費用画面に出ていた。**消す物ではなく入れ物**（焼き直しの
// たびに使い回され、毎回捨てられるのは workspace と home とスロットだけ）なので、
// 正しい扱いは「隠す」であって「消す」ではない。
//
// ★ 判定を 1 か所に閉じてあるのは、これが 3 つの別々の面に効くから: 一覧から外す
// （tenants.go の listTenants）、削除させない（deleteTenant / deleteMembership）、
// 費用を共有インフラへ寄せる（cloudcost.go）。slug の直書きが散ると、次に予約テナントが
// 増えたときに 3 つのうち 1 つが取り残される。
package main

import "context"

// isSystemTenantSlug は「このテナントは人の入れ物ではない」を答える。
func isSystemTenantSlug(slug string) bool {
	return slug == goldenTenantSlug
}

// systemTenantSlugs は予約テナントの全部。増えたらここと isSystemTenantSlug の両方。
func systemTenantSlugs() []string { return []string{goldenTenantSlug} }

// systemMembershipIDs は予約テナントに属する membership id の集合を返す。
//
// active と inactive の**両方**を集める。予約メンバーシップは焼き直しの合間に
// inactive になっていることがあり（destroy は workspace を消すだけで membership 行は
// 残す）、そこで漏らすと「たまたま今アクティブでない golden の費用だけが人の列に出る」
// という、いちばん気づきにくい形になる。
//
// 予約テナントが 1 つも無いデプロイ（ecs-ec2 以外はこれが常態）では空を返す。
// エラーは返すが、呼び側は「集合が引けなければ畳まない」で先へ進んでよい — 費用の
// 取り込みを止めるほどの話ではない。
func (m *manager) systemMembershipIDs(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for _, slug := range systemTenantSlugs() {
		t, ok, err := m.store.GetTenantBySlug(ctx, slug)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		active, err := m.store.ListMembersByTenant(ctx, t.ID)
		if err != nil {
			return out, err
		}
		removed, err := m.store.ListRemovedMembersByTenant(ctx, t.ID)
		if err != nil {
			return out, err
		}
		for _, mi := range append(active, removed...) {
			out[mi.MembershipID] = true
		}
	}
	return out, nil
}
