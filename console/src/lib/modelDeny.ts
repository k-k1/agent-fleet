// 使わないモデル（設定 > エージェント > 各カード > 動作設定、settings.hiddenModels）。
// 判定規則は Agent 側 workspace/agent/model_deny.go と対で、必ず両方を同時に直すこと —
// ここがズレると「ピッカーには出るのに起動が 400 で断られる」という最悪の食い違いになる。
//
// 規則: 区切り（/ . _ 空白 :）をハイフンへ寄せて小文字化し、トークン境界つきの部分一致を見る。
//   - "fable" は "claude-fable-5" にも当たる（claude は別名でも完全 id でも --model に渡せる）
//   - "opencode/glm-5.2" は "opencode-go/glm-5.2" には当たらない（課金経路違いを巻き添えにしない）
//   - "fablet" のような単なる部分文字列には当たらない

export function normModelToken(s: string): string {
  let out = (s || "").trim().toLocaleLowerCase().replace(/[/._ :]/g, "-");
  while (out.includes("--")) out = out.replace(/--/g, "-");
  return out.replace(/^-+|-+$/g, "");
}

export function modelMatchesHidden(requested: string, hidden: string): boolean {
  const r = normModelToken(requested);
  const h = normModelToken(hidden);
  if (!r || !h) return false;
  if (r === h) return true;
  return `-${r}-`.includes(`-${h}-`);
}

// hiddenModelsFor は kind の実効除外リスト。claude だけフェイルセーフを持つ（固定4ティアに
// 「既定」の選択肢が無いので、全部隠すと起動できるモデルが消える）— Agent 側と同じ規則。
export function hiddenModelsFor(
  hiddenModels: Record<string, string[]> | undefined,
  kind: string,
  catalogIds?: string[],
): string[] {
  const raw = hiddenModels?.[kind];
  if (!Array.isArray(raw)) return [];
  const list = raw.filter((v): v is string => typeof v === "string" && !!v.trim());
  if (!list.length) return [];
  if (catalogIds?.length && catalogIds.every((id) => list.some((h) => modelMatchesHidden(id, h)))) {
    return []; // 全部隠す設定は無視する
  }
  return list;
}

export function isModelHidden(
  hiddenModels: Record<string, string[]> | undefined,
  kind: string,
  model: string,
  catalogIds?: string[],
): boolean {
  if (!model.trim()) return false; // 未指定＝CLI 既定に委ねる
  return hiddenModelsFor(hiddenModels, kind, catalogIds).some((h) => modelMatchesHidden(model, h));
}
