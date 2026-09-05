import { useEffect, useState } from "react";
import {
  useSettings,
  setSetting,
  setSettings,
  VOICEVOX_ZUNDAMON,
  TTS_SPEEDS,
  TTS_CACHE_SIZES,
  TTS_PROVIDERS,
  TTS_LANGS,
  TTS_POLLY_VOICES,
  TTS_WORK_READ_MODES,
  TTS_BACKGROUND_PLAYBACK_MODES,
  TTS_RESET,
  type TtsCharConf,
} from "../../../lib/settings.ts";
import { voiceCharacters, isDefaultVoice, previewVoice } from "../../chat/tts.ts";
import { loadSpeakers, speakersCatalog } from "../../chat/ttsSpeakers.ts";
import { loadTtsStatus, ttsStatusCache, voicevoxAvailable, pollyAvailable } from "../../chat/ttsStatus.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Choice, OnOff, Row, Slider } from "../parts/controls.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// TtsTab — the settings tab for text-to-speech (TTS, docs/log/24 + ADR0013). The items are
// grouped by concern rather than left flat: voice (what reads), auto-read (when it reads),
// pronunciation (how it reads) and advanced. Everything is a client-side setting (the settings
// store), so it can be shown and changed regardless of whether the workspace is running.
// Audio notification is the one setting that works independently of ttsEnabled, so it sits
// outside that toggle, at the end.
export function TtsTab() {
  const s = useSettings();
  const tr = useT();
  const confirm = useConfirm();
  // On a deployment without a VOICEVOX engine (the default ECS configuration, for one), the
  // VOICEVOX settings have no effect at all even when listed: speaker, character and emotion
  // style are VOICEVOX-only, and auto falls back to Polly even for Japanese. Do not show a
  // setting that can be chosen but does nothing. Until it is known (before the fetch, or when
  // it fails) show everything: nothing is hidden on the basis of no evidence.
  const [engines, setEngines] = useState(ttsStatusCache());
  useEffect(() => {
    let alive = true;
    void loadTtsStatus().then((st) => alive && st && setEngines(st));
    return () => {
      alive = false;
    };
  }, []);
  const noVv = voicevoxAvailable(engines) === false;
  // A deployment without Polly (no region configured on the CP), handled symmetrically with
  // voicevox: drop it from the options and switch the reading-language note to say that
  // choosing English still will not be read by Polly. Without this check the CP falls back to
  // voicevox in chooseTTSProvider while the UI alone promises Polly (docs/log/84 §84.7).
  const noPolly = pollyAvailable(engines) === false;
  // Drop it from the engine choices, except when it is the current value: removing the current
  // value leaves it undisplayable, and a broken setting then hides silently (a warning is also
  // shown below).
  const engineOptions = TTS_PROVIDERS.filter(
    ([id]) =>
      (!noVv || id !== "voicevox" || s.ttsProvider === "voicevox") &&
      (!noPolly || id !== "polly" || s.ttsProvider === "polly"),
  );
  // Reset returns the TTS settings to their initial state (TTS_RESET = the TTS keys of
  // DEFAULTS). Characters go back to ttsVoicePool: {}, i.e. the standard 14, matching what a
  // new user starts with. The reading dictionary is the user's own input and is not cleared
  // (it is not part of TTS_RESET). Many keys are written at once, so setSettings (batched)
  // keeps it to one render and one save.
  const resetTts = async () => {
    if (!(await confirm({
      title: tr("tts.reset_title"),
      body: tr("tts.reset_body"),
      confirmLabel: tr("tts.reset_confirm"),
    }))) return;
    setSettings(TTS_RESET);
  };
  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">{tr("tts.tts")}</h4>
        <Row label={tr("tts.tts")}>
          <OnOff value={s.ttsEnabled} onChange={(v) => setSetting("ttsEnabled", v)} />
        </Row>
        <p className="muted ds-note">
          {tr("tts.note_tts")}
          {s.ttsEnabled && <>{tr("tts.note_tts_credit")}</>}
        </p>
        <Row label={tr("tts.bg_playback")}>
          <Choice
            value={s.ttsBackgroundPlayback}
            options={TTS_BACKGROUND_PLAYBACK_MODES.map(([id, k]) => [id, tr(k)])}
            onChange={(v) => setSetting("ttsBackgroundPlayback", v as "mute" | "quiet" | "normal")}
          />
        </Row>
        <p className="muted ds-note">{tr("tts.note_bg_playback")}</p>
        {s.ttsBackgroundPlayback === "quiet" && (
          <Row label={tr("tts.bg_volume")}>
            <Slider value={s.ttsBackgroundVolume} onChange={(v) => setSetting("ttsBackgroundVolume", v)} />
          </Row>
        )}
        <Row label={tr("tts.stereo")}>
          <OnOff value={s.ttsStereoByPane} onChange={(v) => setSetting("ttsStereoByPane", v)} />
        </Row>
        <p className="muted ds-note">{tr("tts.note_stereo")}</p>
      </section>
      {s.ttsEnabled && (
        <>
          <section className="ds-group">
            <h4 className="ds-title">{tr("tts.h_voice")}</h4>
            <Row label={tr("tts.engine")}>
              <Choice
                value={s.ttsProvider}
                options={engineOptions.map(([id, k]) => [id, tr(k)])}
                onChange={(v) => setSetting("ttsProvider", v)}
              />
            </Row>
            <p className="muted ds-note">{noVv ? tr("tts.note_no_voicevox") : tr("tts.note_engine")}</p>
            {noVv && s.ttsProvider === "voicevox" && <p className="form-err">{tr("tts.warn_voicevox_missing")}</p>}
            {/* Reading language (docs/log/84). It decides the routing when the engine is set
                to auto (en → Polly) and Polly's default voice. It is its own setting rather
                than the assistant's answer language: sharing that one made switching the chat
                answers to English change the voice of the mirror's reading too. */}
            <Row label={tr("tts.lang")}>
              <Choice
                value={s.ttsLang}
                options={TTS_LANGS.map(([id, k]) => [id, tr(k)])}
                onChange={(v) => setSetting("ttsLang", v)}
              />
            </Row>
            <p className="muted ds-note">{noPolly ? tr("tts.note_lang_no_polly") : tr("tts.note_lang")}</p>
            {!noVv && s.ttsProvider !== "polly" && (
              <>
                <Row label={tr("tts.speaker_voicevox")}>
                  <Choice
                    value={s.ttsVoiceVoicevox}
                    options={VOICEVOX_ZUNDAMON}
                    onChange={(v) => setSetting("ttsVoiceVoicevox", v)}
                  />
                </Row>
                <Row label={tr("tts.zundamon_volume")}>
                  <Slider value={s.ttsZundamonVolume} onChange={(v) => setSetting("ttsZundamonVolume", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_zundamon_volume")}</p>
              </>
            )}
            {(s.ttsProvider !== "voicevox" || noVv) && (
              <Row label={tr("tts.speaker_polly")}>
                <Choice
                  value={s.ttsVoicePolly}
                  options={TTS_POLLY_VOICES.map(([id, k]) => [id, tr(k)])}
                  onChange={(v) => setSetting("ttsVoicePolly", v)}
                />
              </Row>
            )}
            <Row label={tr("tts.voice_per_session")}>
              <OnOff value={s.ttsVoicePerSession} onChange={(v) => setSetting("ttsVoicePerSession", v)} />
            </Row>
            <p className="muted ds-note">{tr("tts.note_voice_per_session")}</p>
            {!noVv && s.ttsProvider !== "polly" && (
              <>
                <div className="ds-row">
                  <span className="ds-label">{tr("tts.characters")}</span>
                </div>
                <CharList />
                <p className="muted ds-note">{tr("tts.note_characters")}</p>
              </>
            )}
            {/* Emotion style swaps VOICEVOX speaker variants (emotionOpts), so it does nothing on Polly */}
            {!noVv && (
              <>
                <Row label={tr("tts.emotion")}>
                  <OnOff value={s.ttsEmotion} onChange={(v) => setSetting("ttsEmotion", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_emotion")}</p>
              </>
            )}
            <Row label={tr("tts.speed")}>
              <Choice value={s.ttsSpeed} options={TTS_SPEEDS.map(([v, k]) => [v, tr(k)])} onChange={(v) => setSetting("ttsSpeed", v)} />
            </Row>
          </section>
          <section className="ds-group">
            <h4 className="ds-title">{tr("tts.h_autoread")}</h4>
            <Row label={tr("tts.autoread_mirror")}>
              <OnOff value={s.ttsAutoReadMirror} onChange={(v) => setSetting("ttsAutoReadMirror", v)} />
            </Row>
            <p className="muted ds-note">{tr("tts.note_autoread_mirror")}</p>
            {s.ttsAutoReadMirror && (
              <>
                <Row label={tr("tts.workread")}>
                  <Choice
                    value={s.ttsWorkRead}
                    options={TTS_WORK_READ_MODES.map(([id, k]) => [id, tr(k)])}
                    onChange={(v) => setSetting("ttsWorkRead", v)}
                  />
                </Row>
                <p className="muted ds-note">{tr("tts.note_workread")}</p>
                {s.ttsWorkRead !== "off" && (
                  <Row label={tr("tts.work_volume")}>
                    <Slider value={s.ttsWorkVolume} onChange={(v) => setSetting("ttsWorkVolume", v)} />
                  </Row>
                )}
                <Row label={tr("tts.autoread_all")}>
                  <OnOff value={s.ttsAutoReadAllPanes} onChange={(v) => setSetting("ttsAutoReadAllPanes", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_autoread_all")}</p>
                <Row label={tr("tts.summary_read")}>
                  <OnOff value={s.ttsSummaryRead} onChange={(v) => setSetting("ttsSummaryRead", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_summary_read")}</p>
              </>
            )}
            <Row label={tr("tts.read_pending")}>
              <OnOff value={s.ttsReadPending} onChange={(v) => setSetting("ttsReadPending", v)} />
            </Row>
            <p className="muted ds-note">{tr("tts.note_read_pending")}</p>
          </section>
          <section className="ds-group">
            <h4 className="ds-title">{tr("tts.h_howto")}</h4>
            <Row label={tr("tts.abbrev_code")}>
              <OnOff value={s.ttsAbbrevCode} onChange={(v) => setSetting("ttsAbbrevCode", v)} />
            </Row>
            <p className="muted ds-note">{tr("tts.note_abbrev_code")}</p>
            {/* Both the particle pause and the katakana reading of English are preprocessing
                on the VOICEVOX path only (the CP applies them only once voicevox is chosen),
                so they are not shown without that engine. */}
            {!noVv && (
              <>
                <Row label={tr("tts.particle_pause")}>
                  <OnOff value={s.ttsParticlePause} onChange={(v) => setSetting("ttsParticlePause", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_particle_pause")}</p>
                <Row label={tr("tts.english_kana")}>
                  <OnOff value={s.ttsEnglishKana} onChange={(v) => setSetting("ttsEnglishKana", v)} />
                </Row>
                <p className="muted ds-note">{tr("tts.note_english_kana")}</p>
              </>
            )}
            <div className="ds-userdict-block">
              <span className="ds-label">{tr("tts.userdict")}</span>
              <textarea
                className="ds-userdict"
                value={s.ttsUserDict}
                onChange={(e) => setSetting("ttsUserDict", e.target.value)}
                rows={4}
                spellCheck={false}
                placeholder={tr("tts.userdict_placeholder")}
              />
              <p className="muted ds-note">{tr("tts.note_userdict")}</p>
            </div>
          </section>
          <section className="ds-group">
            <h4 className="ds-title">{tr("tts.h_advanced")}</h4>
            <Row label={tr("tts.cache")}>
              <Choice value={s.ttsCacheSec} options={TTS_CACHE_SIZES.map(([v, k]) => [v, tr(k)])} onChange={(v) => setSetting("ttsCacheSec", v)} />
            </Row>
            <p className="muted ds-note">{tr("tts.note_cache")}</p>
          </section>
        </>
      )}
      <section className="ds-group ds-reset">
        <Button variant="ghost" icon="discard" onClick={resetTts}>
          {tr("tts.reset_btn")}
        </Button>
        <p className="muted ds-note">{tr("tts.note_reset")}</p>
      </section>
    </div>
  );
}

// CharList — character settings (docs/log/24): use on/off, base style, per-character speed and
// preview. The list is driven by the engine's real catalog (GET /api/tts/speakers); until that
// can be fetched (including while the engine is stopped) it shows a static fallback of the
// default 14 characters, with the normal style only.
function CharList() {
  const s = useSettings();
  const tr = useT();
  const [, setLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  const chars = voiceCharacters();
  const live = !!speakersCatalog(); // from the engine's real catalog (false = static fallback)
  const pool = s.ttsVoicePool || {};
  const patch = (name: string, p: TtsCharConf) => setSetting("ttsVoicePool", { ...pool, [name]: { ...pool[name], ...p } });
  return (
    <div className="ds-charlist">
      {chars.map((c) => {
        const conf = pool[c.name];
        const use = conf?.use ?? isDefaultVoice(c.name);
        const style = conf?.style && c.styles.some((st) => st.id === conf.style) ? conf.style : c.profile.base;
        return (
          <div key={c.name} className={"ds-char" + (use ? "" : " off")}>
            <label className="ds-char-use" title={use ? tr("tts.char_remove") : tr("tts.char_add")}>
              <input type="checkbox" checked={use} onChange={(e) => patch(c.name, { use: e.target.checked })} />
              <span className="ds-char-name">{c.name}</span>
            </label>
            <select
              value={style}
              disabled={!use || c.styles.length < 2}
              title={tr("tts.char_style_title")}
              onChange={(e) => patch(c.name, { style: e.target.value })}
            >
              {c.styles.map((st) => (
                <option key={st.id} value={st.id}>
                  {st.name}
                </option>
              ))}
            </select>
            <select
              value={conf?.speed ?? 0}
              disabled={!use}
              title={tr("tts.char_speed_title")}
              onChange={(e) => patch(c.name, { speed: Number(e.target.value) || undefined })}
            >
              <option value={0}>{tr("common.default")}</option>
              {TTS_SPEEDS.map(([v, k]) => (
                <option key={v} value={v}>
                  {tr(k)}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="ds-char-play"
              title={tr("tts.char_preview_title")}
              onClick={() => previewVoice(c.name, style, conf?.speed)}
            >
              <Icon name="unmute" />
            </button>
          </div>
        );
      })}
      {!live && <p className="muted ds-note">{tr("tts.char_fallback_note")}</p>}
    </div>
  );
}
