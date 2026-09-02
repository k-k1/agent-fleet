// preview_host.go — ホスト方式のプレビュー（docs/log/81 / ADR 0062）。
//
// パス方式（preview.go の /preview/{port}/…）と違い、こちらは **Host ヘッダだけ**を
// 手がかりに Workspace を決める:
//
//	https://{slug}-{port}.{AF_PREVIEW_DOMAIN}/…  →  Agent /proxy/{port}/…
//
// slug は Workspace の起動ごとに引き直され（workspace_lifecycle.go）、停止で消える。
// ★ ラベルが 1 段なのは ACM のワイルドカード証明書が 1 段しか受け持たないため
// （`{port}.{slug}.…` は `*.*.…` を要求して発行できない — ADR 0062 決定 2）。
package main

import (
	"context"
	"crypto/rand"
	"net/url"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewSlugAlphabet は DNS ラベルに置ける文字のうち、slug に使うもの。`-` を含めない
// のはポートとの区切りに使っているから（`{slug}-{port}`）。紛らわしい字を落とさないのは、
// この文字列を人が読み上げたり書き写したりしないため（コピーして貼るだけ）。
const previewSlugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// previewSlugLen は 36^20 ≒ 2^103。URL そのものが「まだ認証していない人から見た唯一の
// 障壁」ではない（既定は認証必須）が、公開モード（docs/log/81 §6.1）では鍵そのものになる。
const previewSlugLen = 20

// newPreviewSlug mints one start's slug. crypto/rand の失敗は握りつぶさない — 弱い
// slug を配るくらいなら発行しない方がよく、呼び出し側は「プレビュー無しで起動」に倒す。
func newPreviewSlug() (string, error) {
	b := make([]byte, previewSlugLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// 36 は 256 を割り切らないので剰余に偏りが出る（0-3 がわずかに厚い）。ここでの
	// 用途（推測不能性）には無視できる差だが、rejection sampling で消しておく方が
	// 「なぜ偏っていいのか」を後から説明せずに済む。
	out := make([]byte, 0, previewSlugLen)
	for len(out) < previewSlugLen {
		if len(b) == 0 {
			b = make([]byte, previewSlugLen)
			if _, err := rand.Read(b); err != nil {
				return "", err
			}
		}
		c := b[0]
		b = b[1:]
		if int(c) >= 252 { // 252 = 36*7、これ以上は捨てる
			continue
		}
		out = append(out, previewSlugAlphabet[int(c)%len(previewSlugAlphabet)])
	}
	return string(out), nil
}

// previewHost は 1 つのプレビュー用ホスト名の中身。
type previewHost struct {
	slug string
	port int
}

// parsePreviewHost は Host ヘッダを {slug}-{port}.{domain} として読む。domain が空
// （AF_PREVIEW_DOMAIN 未設定）のときは常に一致しない ＝ ホスト方式そのものが無い。
//
// ★ 一致しなかったときに「惜しい」を返さない: 呼び出し側はこれを **素通しの判定**に
// 使うので、ALB のヘルスチェック（Host がタスクの IP）も Console のホストも、ここで
// false になってそのまま通常の mux へ行く。
func parsePreviewHost(host, domain string) (previewHost, bool) {
	if domain == "" || host == "" {
		return previewHost{}, false
	}
	// Host にはポートが付き得る（開発時の 127.0.0.1:8080 や、非標準ポートの公開）。
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	suffix := "." + strings.ToLower(strings.TrimPrefix(domain, "."))
	if !strings.HasSuffix(host, suffix) {
		return previewHost{}, false
	}
	label := host[:len(host)-len(suffix)]
	if label == "" || strings.Contains(label, ".") {
		return previewHost{}, false // ラベルは 1 段だけ（証明書がそれしか受け持たない）
	}
	i := strings.LastIndexByte(label, '-')
	if i <= 0 || i == len(label)-1 {
		return previewHost{}, false
	}
	slug, portStr := label[:i], label[i+1:]
	if !validPreviewSlug(slug) {
		return previewHost{}, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || portStr != strconv.Itoa(port) {
		return previewHost{}, false
	}
	return previewHost{slug: slug, port: port}, true
}

// validPreviewSlug は「自分が発行した形か」だけを見る。DB を引く前に弾くことで、
// ワイルドカードに向かって投げられた雑なホスト名が毎回 1 クエリになるのを防ぐ。
func validPreviewSlug(s string) bool {
	if len(s) != previewSlugLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// previewHostname は発行済みの slug とポートから公開ホスト名を組み立てる。
func previewHostname(slug string, port int, domain string) string {
	if slug == "" || domain == "" {
		return ""
	}
	return slug + "-" + strconv.Itoa(port) + "." + strings.TrimPrefix(domain, ".")
}

// previewURLFor は同上の https URL。プレビューの入口は必ず TLS（ワイルドカード証明書を
// 貼るのはそのため）なので、scheme は固定でよい。
func previewURLFor(slug string, port int, domain string) string {
	h := previewHostname(slug, port, domain)
	if h == "" {
		return ""
	}
	return "https://" + h
}

// previewOpenPathFor は「起動をまたいで有効な、貼れるリンク」の相対パス（決定 17）。
// ★ 形を組み立てるのはここ 1 か所にする —— Console 側で組み立てると、パラメータ名を
// 変えた瞬間に**貼られた古いリンクではなく、これから作るリンクが**壊れる。
func previewOpenPathFor(ownerUserKey string, port int) string {
	if ownerUserKey == "" {
		return ""
	}
	q := url.Values{}
	q.Set("owner", ownerUserKey)
	q.Set("port", strconv.Itoa(port))
	return previewOpenPath + "?" + q.Encode()
}

// defaultPreviewPorts は Workspace 設定が空のときの許可ポート（docs/log/81 §5・ADR 0062
// 決定 6）。要望そのもの（React 3000 / Spring Boot 8080）を既定にしてある。
var defaultPreviewPorts = []int{3000, 8080}

// maxPreviewPorts は設定に置ける数の上限。「全部開ける」へ向かう圧力に対する線で、
// 意図せず立っているサービス（DB 管理画面・デバッガ・MCP サーバ）を露出させないための
// 列挙という目的が、際限なく増やせる時点で失われるため。
const maxPreviewPorts = 8

// previewPortsOf は Workspace 設定の許可ポート（空 = 既定）。
func previewPortsOf(st wsSettings) []int {
	if len(st.PreviewPorts) == 0 {
		return defaultPreviewPorts
	}
	return st.PreviewPorts
}

// previewPortAllowed は「この Workspace でそのポートを外に出してよいか」。
func previewPortAllowed(st wsSettings, port int) bool {
	for _, p := range previewPortsOf(st) {
		if p == port {
			return true
		}
	}
	return false
}

// auditPreviewPublic records the public-mode toggle (ADR 0062 決定 12). ★ 監査に残す
// のは「誰が見たか」ではなく「誰が開けたか」——公開の事故は開けた瞬間ではなく、
// 忘れられた後に効いてくるので、後から辿れる形が要る。
func auditPreviewPublic(ctx context.Context, m *manager, res *resolved, on bool) {
	state := "off"
	if on {
		state = "on"
	}
	_ = m.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.preview_public", Target: res.ws.ID,
		Detail: "public=" + state, At: store.NowTS(),
	})
}

// auditPreviewShare records the tenant-share toggle (ADR 0062 決定 14), for the same
// reason as auditPreviewPublic: the accident is not the moment it is switched on, it is
// the months afterwards when nobody remembers it is.
//
// ★ 値が変わったときだけ書く。公開モードと違い、この設定は起動をまたいで残るので、
// 設定画面を開いて保存するたびに同じ行が積まれると、監査ログの側が使えなくなる。
//
// ⚠️ 閲覧者の**アクセス**は残さない。静的アセット 1 本ごとに 1 行になる。残すのは
// 「誰が開けたか」であって「誰が見たか」ではない（docs/log/81 §14.7）。
func auditPreviewShare(ctx context.Context, m *manager, res *resolved, on bool) {
	state := "off"
	if on {
		state = "on"
	}
	_ = m.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
		Action: "workspace.preview_tenant_share", Target: res.ws.ID,
		Detail: "tenant=" + state, At: store.NowTS(),
	})
}

// previewViewerAllowed reports whether `membershipID` may open THIS workspace's preview
// right now — the one place the question is answered, for both halves of the handshake.
//
// ★ 毎回引き直すことに意味がある（ADR 0062 決定 15）。呼び出し側は cookie に焼いた
// membership を渡してくるだけで、「見てよい」は cookie の中に無い。共有を切った瞬間、
// テナントから外した瞬間に、次のリクエストから閉じる。
//
// GetMembershipByID は active 行しか返さないので、無効化された membership はここで
// 落ちる（git_http.go が同じ理由で同じ関数を使っている）。
func previewViewerAllowed(ctx context.Context, m *manager, ws store.Workspace, st wsSettings, membershipID string) bool {
	if membershipID == "" {
		return false
	}
	if membershipID == ws.MembershipID {
		return true // 所有者本人
	}
	if !st.PreviewTenantShare {
		return false
	}
	mv, ok, err := m.store.GetMembershipByID(ctx, membershipID)
	return err == nil && ok && mv.TenantID == ws.TenantID
}

// sanitizePreviewPorts normalizes what the Console sent: 1..65535、重複を潰し、
// 上限で切る。エラーにせず落とすのは、設定画面の保存が「並びの些細な違い」で
// 失敗するより、保存された値が常に意味を持つ方が扱いやすいため。
func sanitizePreviewPorts(in []int) []int {
	var out []int
	seen := map[int]bool{}
	for _, p := range in {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= maxPreviewPorts {
			break
		}
	}
	return out
}
