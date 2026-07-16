// KeysTab — keyboard settings: rebind shortcuts and the terminal-input-priority toggle
// (docs/29 + ADR-0017). Rebindable actions come from features/keys/bindings.ts, which is
// the single source the dispatcher / overlays also read, so a change here takes effect
// live everywhere. Only direct accelerators and the three app chords (leader / palette /
// cheat-sheet) are rebindable; leader SEQUENCES (p r, w t …) are structural and fixed.
import { useEffect, useState } from "react";
import { useSettings, setSetting } from "../../lib/settings.ts";
import { Kbd } from "../../ui/Kbd.tsx";
import { OnOff } from "./controls.tsx";
import { eventChordString, shouldIgnore } from "../../lib/keys/chords.ts";
import {
  rebindSections,
  bindingConflicts,
  setBinding,
  resetBindings,
  overrides,
} from "../../features/keys/bindings.ts";

// Records the next chord the user presses. The settings modal is an open overlay, so the
// global dispatcher is inert while it's up (hasOpenOverlay() guards it); this capture-phase
// listener therefore owns the keyboard and just needs to preventDefault so the browser
// doesn't act on chords like Ctrl+P (print). Escape (no modifiers) cancels.
function KeyCapture({ onCapture, onCancel }: { onCapture: (chord: string) => void; onCancel: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnore(e)) return; // IME / auto-repeat
      const chord = eventChordString(e);
      if (chord == null) return; // modifier-only — keep waiting for the base key
      e.preventDefault();
      e.stopPropagation();
      if (chord === "escape") onCancel();
      else onCapture(chord);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [onCapture, onCancel]);
  return (
    <span className="kb-capture">
      キーを押す… <span className="muted">(Esc で取消)</span>
    </span>
  );
}

export function KeysTab() {
  // Subscribe so the rows re-render when a binding changes (rebindSections reads the store).
  const s = useSettings();
  const [recording, setRecording] = useState<string | null>(null);

  const sections = rebindSections();
  const conflicts = bindingConflicts();
  const titleById = new Map(sections.flatMap((s) => s.items.map((i) => [i.id, i.title] as const)));
  const dirty = Object.keys(overrides()).length > 0;

  return (
    <div className="display-settings keys-settings">
      <section className="ds-group">
        <h4 className="ds-title">端末入力の優先</h4>
        <div className="ds-row">
          <span className="ds-label">端末フォーカス中はアプリより端末を優先</span>
          <OnOff value={s.terminalPriority} onChange={(v) => setSetting("terminalPriority", v)} />
        </div>
        <p className="muted ds-note">
          オンにすると、ターミナルにフォーカスがある間は Ctrl 系のキーをすべて端末（シェル）へ渡します。
          アプリのショートカットは<strong>リーダー（下の「アプリ全体」で変更可）だけ</strong>が生き、そこから
          コマンドメニュー／パレットで全操作に到達できます。tmux やエディタを端末内で使うときに便利です。
        </p>
      </section>

      <section className="ds-group">
        <div className="kb-head">
          <h4 className="ds-title">ショートカットの割り当て</h4>
          <button
            type="button"
            className="btn-ghost kb-reset-all"
            disabled={!dirty}
            onClick={() => {
              setRecording(null);
              resetBindings();
            }}
          >
            すべて既定に戻す
          </button>
        </div>
        <p className="muted ds-note">
          リーダー配下のシーケンス（例: リーダー → p → r）は構造上の操作なので変更できません。ここでは直接キー
          （Alt+1 など）と 3 つのアプリ全体キーを変更できます。「?」でショートカット一覧を確認できます。
        </p>

        {sections.map((sec) => (
          <div className="kb-sec" key={sec.title}>
            <h5 className="kb-sec-title">{sec.title}</h5>
            {sec.items.map((it) => {
              const dup = it.chord ? conflicts.get(it.chord) : undefined;
              const others = dup?.filter((id) => id !== it.id).map((id) => titleById.get(id) || id) || [];
              return (
                <div className="kb-row" key={it.id}>
                  <span className="kb-label">{it.title}</span>
                  <span className="kb-chord">
                    {recording === it.id ? (
                      <KeyCapture
                        onCapture={(chord) => {
                          setBinding(it.id, chord);
                          setRecording(null);
                        }}
                        onCancel={() => setRecording(null)}
                      />
                    ) : it.chord ? (
                      <Kbd chord={it.chord} />
                    ) : (
                      <span className="kb-unbound">未設定</span>
                    )}
                  </span>
                  <div className="kb-actions">
                    {recording === it.id ? (
                      <button type="button" className="btn-ghost" onClick={() => setRecording(null)}>
                        取消
                      </button>
                    ) : (
                      <>
                        <button type="button" className="btn-ghost" onClick={() => setRecording(it.id)}>
                          変更
                        </button>
                        {it.chord && (
                          <button type="button" className="btn-ghost" onClick={() => setBinding(it.id, "")}>
                            解除
                          </button>
                        )}
                        {it.overridden && (
                          <button type="button" className="btn-ghost" onClick={() => setBinding(it.id, null)}>
                            既定
                          </button>
                        )}
                      </>
                    )}
                  </div>
                  {others.length > 0 && (
                    <span className="kb-conflict" role="alert">
                      ⚠ 「{others.join("」「")}」と重複
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </section>
    </div>
  );
}
