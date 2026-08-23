import { useEffect, useState } from "react";
import {
  useSettings,
  setSetting,
  setSettings,
  VOICEVOX_ZUNDAMON,
  TTS_SPEEDS,
  TTS_CACHE_SIZES,
  TTS_PROVIDERS,
  TTS_POLLY_VOICES,
  TTS_WORK_READ_MODES,
  TTS_BACKGROUND_PLAYBACK_MODES,
  TTS_RESET,
  type TtsCharConf,
} from "../../lib/settings.ts";
import { voiceCharacters, isDefaultVoice, previewVoice } from "../chat/tts.ts";
import { loadSpeakers, speakersCatalog } from "../chat/ttsSpeakers.ts";
import { loadTtsStatus, ttsStatusCache, voicevoxAvailable } from "../chat/ttsStatus.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { Choice, OnOff, Row, Slider } from "./controls.tsx";
import { useT } from "../../lib/i18n/index.ts";

// TtsTab — 音声読み上げ（TTS, docs/24 + ADR0013）の設定タブ。もとは AgentsTab から分離した
// 1 セクションだったが、項目が増えて関心事（声の選択・読むタイミング・テキスト加工・性能）が
// フラットに混在したため、「声＝何で読むか」「自動読み上げ＝いつ読むか」「読み方＝どう読むか」
// 「詳細」のグループに分けている。すべてクライアント側の設定（settings store）なので、
// ワークスペースの起動状態に依らず表示・変更できる。
// 「音声通知」だけは読み上げ本体（ttsEnabled）と独立に効くため、トグルの外＝最後に置く。
export function TtsTab() {
  const s = useSettings();
  const tr = useT();
  const confirm = useConfirm();
  // VOICEVOX エンジンを持たないデプロイ（ECS の既定構成など）では、ずんだもん系の設定は
  // 並んでいても一切効かない — 話者もキャラクターも感情スタイルも VOICEVOX 専用で、auto は
  // 日本語まで含めて Polly に落ちる。選べるのに効かない設定は出さない。
  // 分かるまで（取得前・取得失敗）は従来どおり全部出す＝判断材料が無いときに隠さない。
  const [engines, setEngines] = useState(ttsStatusCache());
  useEffect(() => {
    let alive = true;
    void loadTtsStatus().then((st) => alive && st && setEngines(st));
    return () => {
      alive = false;
    };
  }, []);
  const noVv = voicevoxAvailable(engines) === false;
  // エンジン選択から voicevox を落とす。ただし現在値が voicevox のときだけは残す — 選択肢から
  // 消すと現在値が表示できず、壊れた設定のまま黙って隠れてしまう（下で警告も出す）。
  const engineOptions = TTS_PROVIDERS.filter(([id]) => !noVv || id !== "voicevox" || s.ttsProvider === "voicevox");
  // リセット＝音声読み上げ設定を「初期状態」(TTS_RESET = DEFAULTS の TTS キー) に戻す。キャラは
  // ttsVoicePool: {} ＝標準 14 キャラのスタートで、新規ユーザーの初期状態と揃う。読み仮名辞書は
  // ユーザーが打ち込んだ内容なので消さない（TTS_RESET に含めていない）。多数のキーを一度に書くため
  // setSettings（バッチ）で 1 レンダー・1 保存にまとめる。
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
            {/* 感情スタイルは VOICEVOX の話者バリアント差し替え（emotionOpts）なので Polly には効かない */}
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
            {/* 助詞の小休止も英語カタカナ読みも VOICEVOX 経路だけの前処理（CP は voicevox に
                決まったときしか適用しない）。エンジンが無ければ出さない。 */}
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

// CharList — キャラクター設定（docs/24）。使用の ON/OFF・基準スタイル・キャラ別速度・試聴。
// 一覧はエンジン実カタログ（GET /api/tts/speakers）駆動で、取得できるまで（エンジン停止中
// 含む）は既定 14 キャラの静的フォールバックを表示する（スタイルはノーマルのみ）。
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
  const live = !!speakersCatalog(); // エンジン実カタログか（false = 静的フォールバック）
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
