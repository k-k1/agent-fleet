import { useEffect, useRef, useState } from "react";
import { api, raw, rawJSON } from "../api.js";
import Modal from "./Modal.jsx";

// SsmLoginModal drives the SSM SSO handshake for one session WITHOUT attaching the
// terminal yet (docs/history/p3-ssm-session.md). Shared by the New Session modal (after
// create) and the Sessions list (resume a stopped ssm session). It polls
// /api/sessions/{name}/ssm-login and:
//   - authorize → shows the device-auth URL + code (auto-opens once)
//   - pending   → "接続中…" (also the cached-token case: no URL, goes straight to ready)
//   - ready     → calls onReady(name) so the caller attaches the terminal
//   - error     → shows the failure
// `start` (resume) first POSTs /start to relaunch the stopped session; `force`
// re-authenticates (logout + login). Cancel stops the background session.
export default function SsmLoginModal({ name, start = false, force = false, onReady, onCancel }) {
  const [phase, setPhase] = useState("pending");
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const opened = useRef(false);

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
          if (d.url && !opened.current) {
            opened.current = true;
            window.open(d.url, "_blank", "noopener");
          }
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
    run();
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
    <Modal title="SSM ログイン" onClose={cancel} className="session-modal">
      <div className="modal-body">
        <div className="field">
          <div className="field-label">SSM ログイン（{name}）</div>
          {phase === "error" ? (
            <div className="field-help danger">ログインに失敗しました。{error ? " " + error : ""}</div>
          ) : phase === "authorize" ? (
            <>
              <div className="field-help">
                別タブで AWS にサインインして承認してください。承認後、自動で接続します（別タブが開かない場合は下のリンク）。
              </div>
              <div className="flow">
                {url && (
                  <a href={url} target="_blank" rel="noopener" className="flow-link">
                    → 別タブでサインイン ↗
                  </a>
                )}
                {code && <span className="oauth-code">{code}</span>}
              </div>
              <div className="field-help">⚠ 自分で開始したこのログインのみ承認してください。</div>
            </>
          ) : (
            <div className="field-help">接続中… しばらくお待ちください（認証が必要な場合はここに URL が表示されます）。</div>
          )}
        </div>
      </div>
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={cancel}>
          キャンセル
        </button>
      </footer>
    </Modal>
  );
}
