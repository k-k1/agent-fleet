// テナントのログイン面（docs/log/61 §61.9 / §61.11・ADR0043 決定 19 / 29-33）。
//
// AdminTab.tsx から切り出した。P3/P4 では「IA 刷新のときにまとめて移す」と決めて
// 管理モーダルの中に暫定で置いていたが、置き場が 2 つ（デプロイ管理者の管理モーダル /
// テナント管理者のテナント設定モーダル）に分かれたため、両方から同じ実装を差せる
// 場所が要る。読み手ごとの出し分けは props（isSuper / 読み取り専用）だけで、
// ★ 権限そのものは常にサーバ側が持つ:
//   - ログイン規則の PUT は withSuperAdmin 固定（決定 19）
//   - サインイン方法の「承認して有効化」は CP の setStatus が super_admin を見る（決定 30）
// UI の出し分けは案内であって、権限の実装ではない。
//
// この家系は 3 ファイル: ここ（両方が読む型）・tenantLoginRules.tsx（ログイン規則）・
// tenantSignInMethods.tsx（サインイン方法の一覧 / 編集 / 登録簿）。

// テナント行のうち、この画面が読む 3 列だけ（docs/log/61 §61.9.7）。管理 API の
// テナント表現の部分集合なので、呼び出し側の型をそのまま渡せる。
export interface TenantLoginFields {
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
  // 受け入れるが、このテナントのログイン画面には出さない方式（docs/log/61 §61.15.9）。
  // ★ 表示だけの欄で、門ではない。
  hidden_providers?: string;
}

// テナントが定義したサインイン方法（docs/log/61 §61.11）。client_secret は書き込み専用 —
// レスポンスには決して載らず、保存済みかどうかは has_secret で分かる。
export interface TenantIdP {
  id: string;
  name: string;
  label_ja?: string;
  label_en?: string;
  // kind は「どのアダプタで動くか」＝出す欄そのものを変える（docs/log/61 §61.15）。
  // 既定は oidc（P4 の行はこの列より古い）。
  kind?: string;
  issuer: string;
  client_id: string;
  client_secret?: string;
  trust: string;
  allowed_tids?: string;
  allowed_domains?: string;
  allowed_orgs?: string;
  // 規則 1.5 を当てるための安定クレーム名（docs/log/61 §61.15.10）。値ではなく「名前」だけ
  // が設定で、値は必ずトークンから読む。書けるのは CP が許した名前だけ。
  link_claim?: string;
  provider_id?: string;
  tenant_slug?: string;
  status?: string;
  has_secret?: boolean;
  approved_by?: string;
  approved_at?: string;
  usable?: boolean;
}
