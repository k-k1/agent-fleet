import { getSettings } from "../../../lib/settings.ts";
import { getLocale, t } from "../../../lib/i18n/index.ts";
import { emotionOf } from "../ttsText.ts";
import { speakersCatalog, type Speaker, type SpeakerStyle } from "../ttsSpeakers.ts";
import { type TtsOptions } from "./ttsOptions.ts";

// --- Per-session voices (docs/log/24) -----------------------------------------------
// With several sessions running at once, the voice tells you which session an answer came from.
// A voice is picked deterministically from the speaker pool by hashing the session name, so the
// same session name always gets the same voice. The pool is the engine's real catalogue
// (ttsSpeakers.ts) intersected with the user's character settings (settings.ttsVoicePool) - see
// activeVoicePool. When the catalogue has not been fetched (engine down, say) the static list
// below is the fallback, and it doubles as the definition of which characters are enabled by
// default. Characters that have emotional styles also carry those variants here, used by the
// emotional reading; with a catalogue they are derived from the style names instead. Polly gets
// the same treatment across its three Japanese voices.
export interface VoiceProfile {
  name: string; // the engine's character name (the key in settings.ttsVoicePool)
  base: string; // speaker number of the normal style
  happy?: string; // bright style
  angry?: string; // sharp style
}
const SESSION_VOICES: VoiceProfile[] = [
  { name: "ずんだもん", base: "3", happy: "1", angry: "7" },
  { name: "四国めたん", base: "2", happy: "0", angry: "6" },
  { name: "春日部つむぎ", base: "8" },
  { name: "雨晴はう", base: "10" },
  { name: "波音リツ", base: "9" },
  { name: "冥鳴ひまり", base: "14" },
  { name: "九州そら", base: "16", happy: "15", angry: "18" },
  { name: "もち子さん", base: "20" },
  { name: "玄野武宏", base: "11", happy: "39", angry: "40" }, // male voice
  { name: "白上虎太郎", base: "12", happy: "32", angry: "34" },
  { name: "青山龍星", base: "13" }, // deep male voice
  { name: "WhiteCUL", base: "23", happy: "24" },
  { name: "ナースロボ＿タイプＴ", base: "47", happy: "48" },
  { name: "櫻歌ミコ", base: "43" },
];
const SESSION_POLLY_VOICES = ["Takumi", "Kazuha", "Tomoko"]; // Polly's Japanese neural voices are currently these three

// Characters enabled by default (the value used when ttsVoicePool has no `use` entry).
const DEFAULT_VOICE_NAMES = new Set(SESSION_VOICES.map((p) => p.name));
export function isDefaultVoice(name: string): boolean {
  return DEFAULT_VOICE_NAMES.has(name);
}

// --- Character settings: per-user characters, base style and speed (docs/log/24) -----------
// Keywords (substring match) used to derive the emotional variants from the engine's style names.
const HAPPY_STYLES = ["あまあま", "わーい", "喜び", "たのしい", "楽々", "元気", "うきうき"];
const ANGRY_STYLES = ["ツンツン", "おこ", "ツンギレ", "不機嫌", "怒り"];

// profileOf builds the default profile from one character in the catalogue. base is the normal
// style (named 「ノーマル」 or 「ふつう」, else the first one); happy/angry come from the style
// names.
function profileOf(sp: Speaker): VoiceProfile {
  const byName = (words: string[]) => sp.styles.find((st) => words.some((w) => st.name.includes(w)))?.id;
  const normal = sp.styles.find((st) => st.name === "ノーマル" || st.name === "ふつう")?.id ?? sp.styles[0].id;
  return { name: sp.name, base: normal, happy: byName(HAPPY_STYLES), angry: byName(ANGRY_STYLES) };
}

// One row of the TtsTab character list. styles are the choices for the base style; with no
// catalogue fetched, only the normal one.
export interface VoiceCharRow {
  name: string;
  styles: SpeakerStyle[];
  profile: VoiceProfile;
}

// voiceCharacters is the character list behind both the settings UI and pool resolution. It
// returns the engine's real catalogue when there is one, so new characters and styles appear
// automatically, and the static fallback otherwise.
export function voiceCharacters(): VoiceCharRow[] {
  const cat = speakersCatalog();
  if (cat && cat.length) return cat.map((sp) => ({ name: sp.name, styles: sp.styles, profile: profileOf(sp) }));
  return SESSION_VOICES.map((p) => ({ name: p.name, styles: [{ id: p.base, name: "ノーマル" }], profile: p }));
}

// activeVoicePool is the pool of characters currently in use: voiceCharacters with the user's
// character settings (use/style/speed) applied. It is the single source for both session voice
// assignment and the voice list in the reader view. voice is the speaker number of the base
// style; speed is the per-character speed, undefined meaning "follow the global setting".
export interface ActiveVoice {
  name: string;
  voice: string;
  speed?: number;
  profile: VoiceProfile;
}
export function activeVoicePool(): ActiveVoice[] {
  const pool = getSettings().ttsVoicePool || {};
  const out: ActiveVoice[] = [];
  for (const c of voiceCharacters()) {
    const conf = pool[c.name];
    if (!(conf?.use ?? DEFAULT_VOICE_NAMES.has(c.name))) continue;
    // Fall back to the normal style when the saved one is no longer in the catalogue (after an
    // engine update, for instance).
    const style = conf?.style && c.styles.some((st) => st.id === conf.style) ? conf.style : c.profile.base;
    out.push({ name: c.name, voice: style, speed: conf?.speed || undefined, profile: c.profile });
  }
  return out;
}


// speaker number -> character name, folding the different styles of one character together. Used
// by the TopBar "now reading" indicator, so that with per-session voices and emotional styles
// you can see who is speaking.
const VV_CHAR_NAMES: Record<string, string> = {
  "3": "ずんだもん", "1": "ずんだもん", "7": "ずんだもん", "5": "ずんだもん", "22": "ずんだもん", "38": "ずんだもん",
  "2": "四国めたん", "0": "四国めたん", "6": "四国めたん", "4": "四国めたん",
  "8": "春日部つむぎ", "10": "雨晴はう", "9": "波音リツ", "14": "冥鳴ひまり",
  "16": "九州そら", "15": "九州そら", "18": "九州そら", "17": "九州そら", "19": "九州そら",
  "20": "もち子さん",
  "11": "玄野武宏", "39": "玄野武宏", "40": "玄野武宏", "41": "玄野武宏",
  "12": "白上虎太郎", "32": "白上虎太郎", "33": "白上虎太郎", "34": "白上虎太郎", "35": "白上虎太郎",
  "13": "青山龍星",
  "23": "WhiteCUL", "24": "WhiteCUL", "25": "WhiteCUL", "26": "WhiteCUL",
  "47": "ナースロボT", "48": "ナースロボT", "49": "ナースロボT", "50": "ナースロボT",
  "43": "櫻歌ミコ", "44": "櫻歌ミコ", "45": "櫻歌ミコ",
};

// voiceCharName is the character label for the voice being played. For an explicit polly it is
// the VoiceId as is. With an engine catalogue the name is looked up there, so new characters and
// styles come out right.
//
// Passing heard (the provider that actually sounded, i.e. heardProvider's value) also follows the
// auto fallback: even with the setting on auto, if the CP dropped to Polly this returns the Polly
// voice name. Naming the voice from the setting alone would show "Zundamon" in the TopBar while
// Polly is speaking, on a deployment with no VOICEVOX. An empty string (nothing synthesised yet,
// or an older CP) keeps the settings-based best effort.
export function voiceCharName(opts: TtsOptions, heard = ""): string {
  // Treated like an explicit polly. The default VoiceId matches the CP's pollyVoiceFor
  // (en = Joanna, otherwise Takumi).
  if (heard === "polly" && opts.provider !== "polly") return opts.pollyVoice || (opts.lang === "en" ? "Joanna" : "Takumi");
  if (opts.provider === "polly") return opts.pollyVoice || "Polly";
  const cat = speakersCatalog();
  if (cat) {
    for (const sp of cat) if (sp.styles.some((st) => st.id === opts.voice)) return sp.name;
  }
  return VV_CHAR_NAMES[opts.voice] || "";
}

// sessionVoiceOpts returns the voice override (voice / pollyVoice) for a session name, or
// undefined when the setting is off or there is no session name, leaving the selected speaker in
// place. Spread it into the opts of startTts / startNarration.
// voicePoolOpts picks a voice deterministically from the enabled pool (activeVoicePool) by
// hashing a key string, so the same key always gets the same voice. Shared by sessions
// (sessionVoiceOpts) and assistant chat (assistantVoiceOpts).
function voicePoolOpts(key: string): Partial<TtsOptions> | undefined {
  const pool = activeVoicePool();
  if (!pool.length) return undefined; // every character off -> keep the selected speaker
  let h = 0;
  for (const c of key) h = (h * 31 + c.codePointAt(0)!) >>> 0;
  // Fold the high bits in before taking the modulus. A bare h % N looks only at the low bits, and
  // since 31 = -1 (mod 8) that reduces to an alternating sum of character codes, which clusters
  // for similarly shaped names (a shared prefix plus a number): names differing only 1 vs 9, or
  // 0 vs 8, in the last character always land on the same voice. The fold makes it uniform.
  h = (h ^ (h >>> 16)) >>> 0;
  const v = pool[h % pool.length];
  const o: Partial<TtsOptions> = {
    voice: v.voice,
    pollyVoice: SESSION_POLLY_VOICES[h % SESSION_POLLY_VOICES.length],
  };
  if (v.speed) o.speed = v.speed; // per-character speed; never create an unset key, since this is spread over other opts
  return o;
}

export function sessionVoiceOpts(session: string): Partial<TtsOptions> | undefined {
  if (!session || !getSettings().ttsVoicePerSession) return undefined;
  return voicePoolOpts(session);
}

// assistantVoiceOpts is the voice for assistant chat. An explicit voice on the assistant
// (assistant.voice, set when creating or editing it) wins. Otherwise, when "a different voice per
// session" is on, one is assigned from the pool by hashing the assistant id, so the same
// assistant always gets the same voice. With neither, undefined leaves the speaker from settings.
export function assistantVoiceOpts(assistantId?: string, explicit?: string): Partial<TtsOptions> | undefined {
  if (explicit) return voiceChoiceOpts(explicit);
  if (!assistantId || !getSettings().ttsVoicePerSession) return undefined;
  return voicePoolOpts("assistant:" + assistantId);
}

// workVoiceOpts is the override for reading settled work steps quietly. If the current VOICEVOX
// character has the wanted style it is used; otherwise only the volume drops. Volume applies to
// client-side playback, so even after a fallback to Polly these stay distinguishable from the
// normal voice.
export function workVoiceOpts(
  base?: Partial<TtsOptions>,
  mode = getSettings().ttsWorkRead,
): Partial<TtsOptions> | undefined {
  if (mode === "off") return undefined;
  // A hushed/whispering style acts the part, but the output gain is also clearly lowered on top
  // of it; how far is the user's slider (ttsWorkVolume). Some styles are loud in themselves, so
  // the value has to land below the final answer either way.
  const volume = Math.max(0, Math.min(1, getSettings().ttsWorkVolume));
  const voice = base?.voice || getSettings().ttsVoiceVoicevox;
  const wanted = mode === "hushed" ? ["ヒソヒソ"] : ["ささやき", "囁き"];
  const cat = speakersCatalog();
  if (cat) {
    const speaker = cat.find((sp) => sp.styles.some((st) => st.id === voice));
    const style = speaker?.styles.find((st) => wanted.some((w) => st.name.includes(w)));
    if (style) return { voice: style.id, volume };
  }
  // Even before the catalogue is fetched, Zundamon's style numbers are fixed by the bundled
  // configuration, so those styles can still be used.
  if (VV_CHAR_NAMES[voice] === "ずんだもん") return { voice: mode === "hushed" ? "38" : "22", volume };
  return { volume };
}

// --- Voice selection in the reader view (docs/log/24) --------------------------------
// For the voice select in the ReaderView header. "" keeps the speaker from settings.
// "vv:<speaker>" is a VOICEVOX character, with the provider raised to auto so that Polly reads
// in its place while the engine is absent and the chosen character returns once it is back.
// "polly:<VoiceId>" is an explicit Polly. The list is the characters enabled in the character
// settings (activeVoicePool), reflecting the base style and per-character speed.
export function readerVoiceChoices(): [string, string][] {
  return [
    ["", t("tts.voice_default")],
    ...activeVoicePool().map((v): [string, string] => ["vv:" + v.voice, v.name]),
    ...SESSION_POLLY_VOICES.map((v): [string, string] => ["polly:" + v, t("tts.voice_polly", { voice: v })]),
  ];
}

// voiceChoiceOpts resolves a readerVoiceChoices value into a TtsOptions override; "" and unknown
// values give undefined, leaving the settings as they are. A per-character speed, if set, is
// carried along.
export function voiceChoiceOpts(v: string): Partial<TtsOptions> | undefined {
  if (v.startsWith("vv:")) {
    const id = v.slice(3);
    const o: Partial<TtsOptions> = { provider: "auto", voice: id };
    const pv = activeVoicePool().find((p) => p.voice === id);
    if (pv?.speed) o.speed = pv.speed;
    return o;
  }
  if (v.startsWith("polly:")) return { provider: "polly", pollyVoice: v.slice(6) };
  return undefined;
}

// --- Reading with an emotional style (docs/log/24) -----------------------------------
// A sentence containing error or failure words is read in a sharp style, one about success or
// completion in a bright style (emotionOf decides; the switch happens per sentence, i.e. per
// synthesis). It applies only to speakers that have emotional variants, derived from the style
// names in the engine catalogue or from happy/angry in SESSION_VOICES without one; Polly and
// speakers without styles are left alone. A base style other than the normal one is also left
// alone out of respect for the user's choice - the style changes only when voice equals the
// normal speaker number.
function emotionProfile(voice: string): VoiceProfile | undefined {
  for (const c of voiceCharacters()) {
    if (c.profile.base === voice && (c.profile.happy || c.profile.angry)) return c.profile;
  }
  return undefined;
}

export function emotionOpts(text: string, base: TtsOptions): TtsOptions {
  if (getLocale() !== "ja") return base; // the emotion is decided from Japanese vocabulary, so ja only (docs/log/28 §2.4)
  if (!getSettings().ttsEmotion) return base;
  const prof = emotionProfile(base.voice);
  if (!prof) return base;
  const e = emotionOf(text);
  if (e === "happy" && prof.happy) return { ...base, voice: prof.happy };
  if (e === "angry" && prof.angry) return { ...base, voice: prof.angry };
  return base;
}
