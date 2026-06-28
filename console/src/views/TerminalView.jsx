import { useEffect, useRef, useState } from "react";
import { ensureTerm, fit, focusTerm, onSession, reconstructURL } from "../term.js";
import Icon from "../components/Icon.jsx";
import TermKeys from "../components/TermKeys.jsx";

// TerminalView hosts the persistent xterm instance. The container stays mounted
// across mode switches (App hides it rather than unmounting), so the PTY socket
// and scrollback survive. On re-show we refit so the geometry is correct.
export default function TerminalView({ active }) {
  const ref = useRef(null);
  const [session, setSession] = useState(null);
  const [copyLabel, setCopyLabel] = useState("⧉ sign-in URL");
  const [fullscreen, setFullscreen] = useState(false);
  // The Keyboard Lock API (which lets fullscreen capture Ctrl+W etc.) is
  // Chromium-only; Firefox / Safari don't implement it, so be honest in the UI.
  const canLock = !!(navigator.keyboard && navigator.keyboard.lock);

  useEffect(() => {
    ensureTerm(ref.current);
    return onSession(setSession);
  }, []);

  useEffect(() => {
    const onFs = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
  }, []);

  // While a session is attached, guard against accidentally closing/reloading the
  // tab (e.g. Ctrl+W on Firefox, which can't be captured) — the browser shows its
  // "leave page?" confirmation. Removed as soon as no session is attached.
  useEffect(() => {
    if (!session) return;
    const handler = (e) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [session]);

  // Fullscreen lets the Keyboard Lock API (engaged on terminal focus) capture
  // browser-reserved shortcuts like Ctrl+W, so they reach the shell instead of
  // closing the tab. Focus the terminal on enter so the lock engages.
  const toggleFullscreen = () => {
    if (document.fullscreenElement) document.exitFullscreen?.();
    else document.documentElement.requestFullscreen?.().then(() => focusTerm()).catch(() => {});
  };

  useEffect(() => {
    if (active) {
      fit();
      focusTerm();
    }
  }, [active]);

  const copyLogin = async () => {
    const url = reconstructURL();
    if (!url) {
      setCopyLabel("画面に URL なし");
    } else {
      try {
        await navigator.clipboard.writeText(url);
        setCopyLabel("コピーしました");
      } catch {
        setCopyLabel("コピー失敗");
      }
    }
    setTimeout(() => setCopyLabel("⧉ sign-in URL"), 1400);
  };

  return (
    <div className="termview">
      <header className="view-head">
        <span className="view-title">{session ? `session: ${session}` : "セッション未接続"}</span>
        <span className="spacer" />
        <button
          className="ghost"
          title={
            canLock
              ? "全画面にすると Ctrl+W などのブラウザ予約キーもターミナルに渡ります（タブが閉じない）"
              : "全画面表示。※ Ctrl+W 等の捕捉は Chrome / Edge のみ（このブラウザは Keyboard Lock 非対応）"
          }
          onClick={toggleFullscreen}
        >
          <Icon name="screen-full" /> {fullscreen ? "全画面解除" : "全画面"}
        </button>
        <button
          className="ghost"
          title="端末に表示された最新のサインイン URL をコピー"
          onClick={copyLogin}
        >
          {copyLabel}
        </button>
      </header>
      <div className="terminal" ref={ref} />
      <TermKeys />
    </div>
  );
}
