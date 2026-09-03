import { getSettings } from "../../../lib/settings.ts";
import { getLocale } from "../../../lib/i18n/index.ts";
import { applyReadings } from "../ttsText.ts";

export interface TtsOptions {
  provider: string; // "auto" | "voicevox" | "polly"
  voice: string; // VOICEVOX speaker 番号
  speed: number; // speedScale
  enkana?: boolean; // 英語をカタカナ英語に前処理して読ませる（CP の enkana。voicevox 時のみ効く）
  pollyVoice?: string; // Polly の VoiceId（auto のフォールバック先でも使う）
  lang?: string; // 言語ヒント（設定 > 読み上げ の ttsLang）: "auto" | "ja" | "en"
  particlePause?: boolean; // 設定 ttsParticlePause。CP 側で読点ポーズを詰める（voicevox のみ）
  volume?: number; // 再生音量（0..1）。合成条件ではなく Web Audio の出力ゲイン
  paneId?: string; // 発生元ペイン。設定ON時、現在の列位置からステレオのパンを決める
}

// settings から TtsOptions を組む共通処理（announce / speakText / startNarration / ChatView）。
export function ttsOptsFromSettings(s = getSettings()): TtsOptions {
  // 日本語専用の読み整形（enkana カタカナ英語・助詞ポーズ）は UI ロケールが ja のときだけ
  // 効かせる（英語 UI では素の音声へ流し、かなパイプラインをスキップ・docs/log/28 §2.4）。
  const ja = getLocale() === "ja";
  return {
    provider: s.ttsProvider,
    voice: s.ttsVoiceVoicevox,
    speed: s.ttsSpeed,
    enkana: ja && s.ttsEnglishKana,
    pollyVoice: s.ttsVoicePolly,
    // 読み上げ専用の言語軸（docs/log/84）。以前はアシスタントの回答言語
    // （outputLanguage）を借りていたため、チャットの回答を English にしただけで
    // ミラーの読み上げ・朗読ビューまで Polly / Joanna に切り替わっていた。
    // "auto" は UI 表示言語に従う（上の enkana / 助詞ポーズと同じ軸で揃える。
    // 借用時代の auto は常に日本語扱いで、英語 UI でも VOICEVOX に流れていた）。
    lang: s.ttsLang === "auto" ? getLocale() : s.ttsLang,
    particlePause: ja && s.ttsParticlePause,
  };
}

// applyReadings（辞書 → 組み込み読み補正 → 助詞ポーズ）は日本語の発音整形なので UI ロケールが
// ja のときだけ適用する。非 ja では素のテキストをそのまま返す（既に trim 済みの前提・docs/log/28 §2.4）。
export function localizedReadings(t: string, dict: [string, string][], particlePause: boolean): string {
  return getLocale() === "ja" ? applyReadings(t, dict, particlePause) : t;
}
