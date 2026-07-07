// SsmLoginModal — drives the SSM SSO handshake for one session WITHOUT attaching
// the terminal yet. Shared by New Session (after create) and the Sessions list
// (resume). Polls /api/sessions/{name}/ssm-login:
//   authorize → shows the device-auth URL + code (manual open — the user must
//               verify the code first; device-code phishing guard)
//   pending   → 接続中 (cached-token case goes straight to ready)
//   ready     → onReady(name) so the caller attaches
//   error     → shows the failure
// `start` (resume) first POSTs /start; `force` re-authenticates.
import { useEffect, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { api, raw, rawJSON } from "../../core/api/client.ts";

interface SsmLoginModalProps {
  name: string;
  start?: boolean;
  force?: boolean;
  onReady: (name: string) => void;
  onCancel: () => void;
}

export function SsmLoginModal({ name, start = false, force = false, onReady, onCancel }: SsmLoginModalProps) {
  const [phase, setPhase] = useState("pending");
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    const poll = async () => {
      if (!alive) return;
      let d = null;
      try {
        d = await api(`api/sessions/${encodeURIComponent(name)}/ssm-login`);
      } catch {
        d = null;
      }
      if (!alive) return;
      if (d && !d.error) {
        if (d.phase === "authorize") {
          if (d.url) setUrl(d.url);
          if (d.code) setCode(d.code);
        }
        if (d.phase === "ready") {
          onReady(name);
          return;
        }
        setPhase(d.phase);
        if (d.phase === "error") {
          setError(d.message || "");
          return;
        }
      }
      if (alive) setTimeout(poll, 1500);
    };
    const run = async () => {
      if (start) {
        try {
          await rawJSON(`api/sessions/${encodeURIComponent(name)}/start${force ? "?force=1" : ""}`, "POST");
        } catch {
          /* the poll surfaces the failure */
        }
      }
      setTimeout(poll, start ? 900 : 600);
    };
    void run();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  const cancel = async () => {
    try {
      await raw(`api/sessions/${encodeURIComponent(name)}/stop`, { method: "POST" });
    } catch {
      /* best effort */
    }
    onCancel();
  };

  return (
    <Modal title={`SSM ログイン（${name}）`} onClose={cancel}>
      <div className="ui-modal-body">
        {phase === "error" ? (
          <p className="ssm-error">ログインに失敗しました。{error ? " " + error : ""}</p>
        ) : phase === "authorize" ? (
          <>
            <p className="ui-field-hint">
              下の認証コードを確認し、「サインインして承認」を押してください。開いたページに表示されるコードが
              一致することを確かめてから承認してください。承認後、自動で接続します。
            </p>
            {code && (
              <div className="ssm-code-row">
                <span className="ui-field-label">認証コード</span>
                <span className="ssm-code">{code}</span>
              </div>
            )}
            <div>
              <Button
                variant="primary"
                icon="link-external"
                disabled={!url}
                onClick={() => url && window.open(url, "_blank", "noopener")}
              >
                サインインして承認
              </Button>
            </div>
            <p className="ui-field-hint">
              ⚠ 自分で開始したこのログインのみ承認してください（コードが一致しない場合は承認しない）。
            </p>
          </>
        ) : (
          <p className="ui-field-hint">接続中… しばらくお待ちください（認証が必要な場合はここに URL が表示されます）。</p>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={cancel}>
          キャンセル
        </Button>
      </footer>
    </Modal>
  );
}
