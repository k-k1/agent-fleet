// NotProvisioned — 「サインインはできたが、まだどのテナントにも所属していない」の着地面
// （docs/log/61 §61.10.2・P7-2）。
//
// ★ これは異常ではなく、招待制デプロイの**正常な最初の一歩**。新規インストールの既定が
// AF_PROVISION=invite になったので、招待される前の人が最初に見る画面はここになる。
// それまではこの状態でも通常の Console が開き、以後すべてのリクエストが 403 で
// 弾かれてトーストが 1 つずつ出るだけだった — 「自分は何をすればいいのか」がどこにも
// 書かれていない状態。
//
// この画面が答えるのは 3 つだけ:
//   ① 失敗ではない（サインインは通っている）
//   ② 次にすることは「管理者に依頼する」
//   ③ ★ そのとき伝えるべき**自分のアドレス**。管理者は名簿にアドレスで人を足すので、
//      これが読めないと「どのアドレスで入ったか分からない」という往復が必ず 1 回増える。
//      サインイン方法が複数ある人ほど、自分がどれで入ったのか分かっていない。
//
// ★ super_admin はここに来ない。CP は所属ゼロの super_admin にも 200 を返す
// （tenants.go・決定 23）ので、最初のテナントを作る人が締め出されることはない。
// ★ ui/Button を使う。.primary / .ghost に見た目があるのは設定モーダルの中だけ
// （settings.css の :where(.settings-modal) スコープ）で、この面は外なので、素の
// <button className="primary"> はブラウザ既定のまま出る。
import { Button } from "../../ui/Button.tsx";
import { rel, clearLocalState } from "../../core/api/client.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useT } from "../../lib/i18n/index.ts";

export function NotProvisioned() {
  const tr = useT();
  const whoami = useTenantStore((s) => s.whoami);
  const email = whoami?.email || whoami?.user || "";

  return (
    <div className="app-shell notprov">
      <main className="notprov-body">
        <div className="notprov-card">
          <span className="codicon codicon-mail notprov-icon" aria-hidden="true" />
          <h1>{tr("notprov.title")}</h1>
          <p className="notprov-lead">{tr("notprov.lead")}</p>
          {/* ★ アドレスは選択してコピーできる必要がある（管理者に貼って渡すため）。
              画像でも省略でもなく、素のテキストで全部出す。 */}
          {email && (
            <p className="notprov-who">
              {tr("notprov.signed_in_as")} <code>{email}</code>
            </p>
          )}
          <p className="notprov-hint">{tr("notprov.hint")}</p>
          <div className="notprov-acts">
            {/* 再読み込み: 追加された直後の人が、ログアウトせずに入り直せる導線。
                init() は成功すれば notProvisioned を落とすが、ここは素直に
                リロードでよい（追加は人の操作なので、ポーリングを足す価値が無い）。 */}
            <Button variant="primary" icon="refresh" onClick={() => location.reload()}>
              {tr("notprov.retry")}
            </Button>
            <Button
              variant="ghost"
              icon="sign-out"
              onClick={() => {
                // ログアウトの作法は TopBar と同じ: 先に手元の状態を捨ててから CP へ。
                // 別のアカウントでこのブラウザに入る人に、前の人の状態を見せない。
                clearLocalState();
                location.assign(rel("oauth2/logout"));
              }}
            >
              {tr("notprov.switch_account")}
            </Button>
          </div>
        </div>
      </main>
    </div>
  );
}
