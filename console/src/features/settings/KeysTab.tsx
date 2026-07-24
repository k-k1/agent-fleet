// KeysTab — keyboard settings: rebind shortcuts and the terminal-input-priority toggle
// (docs/29 + ADR-0017). Rebindable actions come from features/keys/bindings.ts, which is
// the single source the dispatcher / overlays also read, so a change here takes effect
// live everywhere. Only direct accelerators and the three app chords (leader / palette /
// cheat-sheet) are rebindable; leader SEQUENCES (p r, w t …) are structural and fixed.
// Action / section names are i18n keys (resolved with cmdLabel); chrome uses t().
import { useEffect, useState } from "react";
import { useSettings, setSetting, MIRROR_SEND_MODES } from "../../lib/settings.ts";
import { Kbd } from "../../ui/Kbd.tsx";
import { OnOff, Row, Choice } from "./controls.tsx";
import { t, useLocale } from "../../lib/i18n/index.ts";
import { eventChordString, shouldIgnore } from "../../lib/keys/chords.ts";
import {
  rebindSections,
  bindingConflicts,
  setBinding,
  resetBindings,
  overrides,
} from "../../features/keys/bindings.ts";
import { cmdLabel } from "../../features/keys/labels.ts";

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
      {t("keys.kt.capture")} <span className="muted">{t("keys.kt.captureHint")}</span>
    </span>
  );
}

export function KeysTab() {
  // Subscribe so the rows re-render when a binding (or language) changes.
  const s = useSettings();
  useLocale();
  const [recording, setRecording] = useState<string | null>(null);

  const sections = rebindSections();
  const conflicts = bindingConflicts();
  const titleById = new Map(sections.flatMap((sec) => sec.items.map((i) => [i.id, i.title] as const)));
  const dirty = Object.keys(overrides()).length > 0;

  return (
    <div className="display-settings keys-settings">
      <section className="ds-group">
        <h4 className="ds-title">{t("keys.kt.termPrioTitle")}</h4>
        <div className="ds-row">
          <span className="ds-label">{t("keys.kt.termPrioLabel")}</span>
          <OnOff value={s.terminalPriority} onChange={(v) => setSetting("terminalPriority", v)} />
        </div>
        <p className="muted ds-note">{t("keys.kt.termPrioNote")}</p>
        <div className="ds-row">
          <span className="ds-label">{t("keys.kt.shellPassLabel")}</span>
          <OnOff value={s.shellTermPassthrough} onChange={(v) => setSetting("shellTermPassthrough", v)} />
        </div>
        <p className="muted ds-note">{t("keys.kt.shellPassNote")}</p>

        <Row label={t("display.send_key")}>
          <Choice
            value={s.mirrorSend}
            options={MIRROR_SEND_MODES.map((m) => [m.id, t(m.labelKey)])}
            onChange={(v) => setSetting("mirrorSend", v)}
          />
        </Row>
        <p className="muted ds-note">{s.mirrorSend === "enter" ? t("display.send_note_enter") : t("display.send_note_mod")}</p>

        <div className="ds-row">
          <span className="ds-label">{t("keys.kt.quickRepliesLabel")}</span>
          <OnOff value={s.quickRepliesEnabled} onChange={(v) => setSetting("quickRepliesEnabled", v)} />
        </div>
        <p className="muted ds-note">{t("keys.kt.quickRepliesNote")}</p>
      </section>

      <section className="ds-group">
        <div className="kb-head">
          <h4 className="ds-title">{t("keys.kt.assignTitle")}</h4>
          <button
            type="button"
            className="btn-ghost kb-reset-all"
            disabled={!dirty}
            onClick={() => {
              setRecording(null);
              resetBindings();
            }}
          >
            {t("keys.kt.resetAll")}
          </button>
        </div>
        <p className="muted ds-note">{t("keys.kt.assignNote")}</p>

        {sections.map((sec) => (
          <div className="kb-sec" key={sec.title}>
            <h5 className="kb-sec-title">{cmdLabel(sec.title)}</h5>
            {sec.items.map((it) => {
              const dup = it.chord ? conflicts.get(it.chord) : undefined;
              const others = dup?.filter((id) => id !== it.id).map((id) => cmdLabel(titleById.get(id) || id)) || [];
              return (
                <div className="kb-row" key={it.id}>
                  <span className="kb-label">{cmdLabel(it.title)}</span>
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
                      <span className="kb-unbound">{t("keys.kt.unset")}</span>
                    )}
                  </span>
                  <div className="kb-actions">
                    {recording === it.id ? (
                      <button type="button" className="btn-ghost" onClick={() => setRecording(null)}>
                        {t("keys.kt.cancel")}
                      </button>
                    ) : (
                      <>
                        <button type="button" className="btn-ghost" onClick={() => setRecording(it.id)}>
                          {t("keys.kt.change")}
                        </button>
                        {it.chord && (
                          <button type="button" className="btn-ghost" onClick={() => setBinding(it.id, "")}>
                            {t("keys.kt.clear")}
                          </button>
                        )}
                        {it.overridden && (
                          <button type="button" className="btn-ghost" onClick={() => setBinding(it.id, null)}>
                            {t("keys.kt.default")}
                          </button>
                        )}
                      </>
                    )}
                  </div>
                  {others.length > 0 && (
                    <span className="kb-conflict" role="alert">
                      ⚠ {t("keys.kt.conflict", { names: others.join(" / ") })}
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
