import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText, rel } from "../../core/api/client.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { getLocale, useT } from "../../lib/i18n/index.ts";

// AccountTab — 自分のアカウントに紐づいたサインイン方法の一覧と、2 つ目の追加
// （docs/log/61 §61.16 + ADR0043 決定 37）。
//
// なぜこの面が要るか: 別々の IdP が同じメールアドレスを名乗る組み合わせはログイン時に
// 拒否される（決定 32）。開けてよいのは「アカウントの持ち主が、自分で押したとき」だけ
// なので、ログイン画面ではなくログイン済みのこの面が入口になる。
//
// ★ ここに出る一覧は VIEW であって門ではない（決定 14）。押せる方式を絞るのと同じ規則を
// CP 側（handleOAuthLink / linkableFor）が持っていて、そちらが実際の許可を決める。
// 紐づけ自体もその方式の門（org・許可ドメイン）を通ったときだけ成立する。

interface LinkedMethod {
  provider: string;
  // 行を名指しする鍵は provider だけでは足りない（同じ provider に subject が 2 つ
  // ある行が理屈のうえではありうる）。解除も React の key もこの対で持つ。
  subject: string;
  email?: string;
  last_login_at?: string;
  current?: boolean;
  // ★ 外せるかどうかは CP が答える（残り 1 つでない・いまのセッションの方式でない）。
  // ここはその写しで、判断はしない — 解除 API 側が同じ規則をもう一度見ている（決定 14）。
  removable?: boolean;
  label_ja?: string;
  label_en?: string;
}

interface LinkableMethod {
  provider: string;
  label_ja?: string;
  label_en?: string;
  tenant?: string;
}

// ラベルは CP がログインボタンと同じ規則で両言語返す。空（設定から消えた方式・停止中の
// テナント行）のときだけ id を出す — 行を隠すと「知らないうちに増えた方式」が見えなくなる。
function labelOf(m: { provider: string; label_ja?: string; label_en?: string }): string {
  const label = getLocale() === "en" ? m.label_en : m.label_ja;
  return label || m.provider;
}

export function AccountTab() {
  const tr = useT();
  const askConfirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [linked, setLinked] = useState<LinkedMethod[] | null>(null);
  const [linkable, setLinkable] = useState<LinkableMethod[]>([]);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    setErr("");
    api("api/me/login-methods")
      .then((res) => {
        if (res && res.error) {
          setErr(res.error.message || tr("account.load_failed"));
          return;
        }
        setEnabled(res.enabled !== false);
        setLinked(res.linked || []);
        setLinkable(res.linkable || []);
      })
      .catch(() => setErr(tr("account.load_failed")));
  }, [tr]);
  useEffect(load, [load]);

  // 紐づけは CP 側の往復（/oauth2/link → IdP → /oauth2/callback）。結果は CP が描く小さな
  // 画面に出て、そこから戻ってくる — 途中で Console を離れるので、いまいる場所を next で渡す。
  const startLink = (provider: string) => {
    const next = location.pathname + location.search;
    const q = new URLSearchParams({ provider, next });
    location.assign(rel("oauth2/link") + "?" + q.toString());
  };

  // 解除（docs/log/61 §61.16.4）。★ provider / subject はクエリで渡す — テナント定義の
  // provider id は "t:<slug>:<name>" で ":" を含み、パスの区切りに載せられない。
  const detach = async (m: LinkedMethod) => {
    const ok = await askConfirm({
      title: tr("account.detach_title", { name: labelOf(m) }),
      body: tr("account.detach_body"),
      confirmLabel: tr("account.detach"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const q = new URLSearchParams({ provider: m.provider, subject: m.subject });
      const res = await apiJSON("api/me/login-methods?" + q.toString(), "DELETE");
      if (res?.error) {
        setErr(errText(res.error));
        return;
      }
      load();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="display-settings">
      <p className="muted ds-note">{tr("account.intro")}</p>

      {!enabled && <p className="muted ds-note">{tr("account.disabled")}</p>}
      {err && <p className="muted pad">{err}</p>}

      {linked && linked.length > 0 && (
        <table className="pat-table account-methods">
          <thead>
            <tr>
              <th>{tr("account.th_method")}</th>
              <th>{tr("account.th_email")}</th>
              <th>{tr("account.th_last_login")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {linked.map((m) => (
              <tr key={m.provider + "\u0000" + m.subject}>
                <td>
                  {labelOf(m)}
                  {m.current && <span className="muted"> — {tr("account.current")}</span>}
                </td>
                <td>{m.email || "—"}</td>
                <td>{fmtDate(m.last_login_at) || "—"}</td>
                <td className="allow-acts">
                  {/* 外せない行でもボタンは出す（消すと「なぜ外せないのか」を読む場所が
                      無くなる）。理由は title に出し、押せるかどうかはサーバの答え。 */}
                  <button
                    type="button"
                    className="ghost xs danger"
                    disabled={busy || !m.removable}
                    title={m.removable ? undefined : m.current ? tr("account.detach_current") : tr("account.detach_last")}
                    onClick={() => detach(m)}
                  >
                    {tr("account.detach")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {linked && linked.length === 0 && <p className="muted pad">{tr("account.none")}</p>}

      {enabled && linkable.length > 0 && (
        <section className="ds-group">
          <h4 className="ds-title">{tr("account.add_title")}</h4>
          <p className="muted ds-note">{tr("account.add_note")}</p>
          <div className="ds-row account-add">
            {linkable.map((m) => (
              <button key={m.provider} type="button" onClick={() => startLink(m.provider)}>
                {labelOf(m)}
                {m.tenant ? ` (${m.tenant})` : ""}
              </button>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function fmtDate(s: string | undefined): string {
  if (!s) return "";
  const d = new Date(s);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
