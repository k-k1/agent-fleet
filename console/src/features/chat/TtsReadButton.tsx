import { Icon } from "../../ui/Icon.tsx";
import { useSettings } from "../../lib/settings.ts";
import { speakText, type TtsOptions } from "./tts.ts";

// TtsReadButton — 過去の回答フッターに置く「読み上げ」ボタン（ChatView / MirrorView 共用）。
// 音声読み上げが有効（ttsEnabled）かつテキストがあるときだけ表示。className で各フッターの
// 既存ボタン（cm-copy / mt-copy）に見た目を合わせる。voice は声の上書き（アシスタントの声）。
// 押すとグローバル再生（1 本）で読み上げ、TopBar に停止 UI が出る（docs/24）。
export function TtsReadButton({
  text,
  source = "チャット",
  className = "cm-copy",
  voice,
}: {
  text: string;
  source?: string;
  className?: string;
  voice?: Partial<TtsOptions>;
}) {
  const enabled = useSettings().ttsEnabled;
  if (!enabled || !text.trim()) return null;
  return (
    <button
      type="button"
      className={"ghost " + className}
      title="読み上げ"
      onClick={() => speakText(text, source, voice)}
    >
      <Icon name="unmute" /> 読み上げ
    </button>
  );
}
