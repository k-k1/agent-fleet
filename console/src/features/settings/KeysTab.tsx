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
import {
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  pinQuickReply,
  unpinQuickReply,
  isQuickReplyPinned,
  quickReplyKey,
  oneTimeQuickReplies,
} from "../../lib/quickReplies.ts";
import { Icon } from "../../ui/Icon.tsx";

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

// 学習済みクイック返信の管理（返信サジェスト Layer A）。チップ側の削除・ピン留めは右クリック /
// 長タップのメニューに集約したが、まとめて見直すのはここ。並びは実際のランキングと同じ
// 「ピン留め（ピンした順）→ 使用回数 → 最近」。「すべて消去」は学習と隠しを落として初期状態
// （シードだけ）へ戻す — ピンは明示的な指定なので、外すのは別のボタンにする。
// 「1回だけの候補を消去」はその中間で、常用の候補を残したまま使い捨ての言い回しだけを落とす
// （全消しは常用まで巻き添えにするので、掃除のたびに学習をやり直すことになる）。こちらは隠しに
// 積まない＝また送れば学習し直す（oneTimeQuickReplies の注記）。
function LearnedQuickReplies() {
  const s = useSettings();
  const learned = s.quickReplies || {};
  const hidden = s.quickRepliesHidden || [];
  const pinned = s.quickRepliesPinned || [];
  // 保存キーではなく綴りから引き直したキーでまとめる。全角/半角の同一視を入れる前に別キーで
  // 学習された同じ文（「ＯＫ」と「OK」）が2行に見えないように、チップ行と同じ畳み方で1件にする
  // （回数は合算・綴りは新しい方。保存の実体はその文を次に送るか消したときに畳まれる）。
  const merged = new Map<string, { key: string; text: string; count: number; at: number }>();
  for (const v of Object.values(learned)) {
    const key = quickReplyKey(v.text);
    const prev = merged.get(key);
    merged.set(key, {
      key,
      text: !prev || v.at >= prev.at ? v.text : prev.text,
      count: (prev?.count ?? 0) + v.count,
      at: Math.max(prev?.at ?? 0, v.at),
    });
  }
  const learnedRows = [...merged.values()].sort((a, b) => b.count - a.count || b.at - a.at);
  // ピンは学習に無い文（シードや✨候補）でも留められるので、行が無ければここで足す。
  const pinnedRows = pinned.map((text) => {
    const hit = learnedRows.find((r) => isQuickReplyPinned([text], r.text));
    return hit ?? { key: "pin:" + text, text, count: 0, at: 0 };
  });
  const rows = [...pinnedRows, ...learnedRows.filter((r) => !isQuickReplyPinned(pinned, r.text))];
  // 消える行と件数は同じ判定から出す（ボタンの「{n}件」と実際に消える数がズレないように）。
  const onceTexts = oneTimeQuickReplies(learned, pinned);
  if (!rows.length && !hidden.length) return null;
  return (
    <div className="qr-learned">
      <div className="kb-head">
        <h5 className="kb-sec-title">{t("keys.kt.qrLearnedTitle", { n: rows.length })}</h5>
        <span className="kb-head-actions">
          {onceTexts.length > 0 && (
            <button
              type="button"
              className="btn-ghost qr-clear-once"
              title={t("keys.kt.qrClearOnceHint")}
              onClick={() =>
                setSetting(
                  "quickReplies",
                  onceTexts.reduce((m, text) => forgetQuickReply(m, text), learned),
                )
              }
            >
              {t("keys.kt.qrClearOnce", { n: onceTexts.length })}
            </button>
          )}
          <button
            type="button"
            className="btn-ghost qr-clear-all"
            onClick={() => {
              setSetting("quickReplies", {});
              setSetting("quickRepliesHidden", []);
            }}
          >
            {t("keys.kt.qrClearAll")}
          </button>
        </span>
      </div>
      <div className="qr-list">
        {rows.map((r) => {
          const pin = isQuickReplyPinned(pinned, r.text);
          return (
            <span className={"qr-item" + (pin ? " pinned" : "")} key={r.key}>
              <span className="qr-text">{r.text}</span>
              <span className="muted qr-count">{r.count}</span>
              <button
                type="button"
                className="qr-pin"
                title={pin ? t("mirror.suggest_unpin") : t("mirror.suggest_pin")}
                aria-label={pin ? t("mirror.suggest_unpin") : t("mirror.suggest_pin")}
                aria-pressed={pin}
                onClick={() => {
                  if (pin) {
                    setSetting("quickRepliesPinned", unpinQuickReply(pinned, r.text));
                    return;
                  }
                  setSetting("quickRepliesPinned", pinQuickReply(pinned, r.text));
                  setSetting("quickRepliesHidden", unhideQuickReply(hidden, r.text));
                }}
              >
                <Icon name={pin ? "pinned" : "pin"} />
              </button>
              <button
                type="button"
                className="qr-del"
                title={t("mirror.suggest_forget")}
                aria-label={t("mirror.suggest_forget")}
                onClick={() => {
                  setSetting("quickReplies", forgetQuickReply(learned, r.text));
                  setSetting("quickRepliesHidden", hideQuickReply(hidden, r.text));
                  setSetting("quickRepliesPinned", unpinQuickReply(pinned, r.text));
                }}
              >
                ×
              </button>
            </span>
          );
        })}
      </div>
      {pinned.length > 0 && (
        <p className="muted ds-note">
          {t("keys.kt.qrPinnedNote", { n: pinned.length })}{" "}
          <button type="button" className="btn-ghost" onClick={() => setSetting("quickRepliesPinned", [])}>
            {t("keys.kt.qrUnpinAll")}
          </button>
        </p>
      )}
      {hidden.length > 0 && (
        <p className="muted ds-note">
          {t("keys.kt.qrHiddenNote", { n: hidden.length })}{" "}
          <button type="button" className="btn-ghost" onClick={() => setSetting("quickRepliesHidden", [])}>
            {t("keys.kt.qrUnhideAll")}
          </button>
        </p>
      )}
    </div>
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
        {s.quickRepliesEnabled && <LearnedQuickReplies />}

        <div className="ds-row">
          <span className="ds-label">{t("keys.kt.replySuggestLabel")}</span>
          <OnOff value={s.replySuggestEnabled} onChange={(v) => setSetting("replySuggestEnabled", v)} />
        </div>
        <p className="muted ds-note">{t("keys.kt.replySuggestNote")}</p>
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
