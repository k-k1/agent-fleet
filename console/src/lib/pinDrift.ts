// pinDrift — 設定>環境「ツールのバージョン」表のピンずれ判定。実体の版とイメージ
// ビルド時ピン（versions.json）を比較し、色分けバッジの方向を返す。
// 版形状は多様（semver 2.1.220 / 日付版数 2026.07.23-e383d2b / "(取得失敗)" /
// "(timeout)"）なので、数値セグメントだけを順に比較し、判定できない形は
// unknown（バッジなし）へ倒す。behind＝ピンより古い（更新が届いていない・警告色）、
// ahead＝ピンより新しい（自己更新などで前進・情報色）。

export type PinDrift = "behind" | "ahead" | "same" | "unknown";

export function pinDrift(version: string | undefined, pin: string | undefined): PinDrift {
  const a = segs(version);
  const b = segs(pin);
  if (!a || !b) return "unknown";
  const n = Math.max(a.length, b.length);
  for (let i = 0; i < n; i++) {
    // 欠けたセグメントは 0（"1.2" < "1.2.3"）。cursor のピン 2026.07.23-e383d2b は
    // sha 接尾辞が segs で落ちるので、実効の 2026.07.23（extractVer が接尾辞を落とした
    // 形）と同日付なら same になる。
    const x = a[i] ?? 0;
    const y = b[i] ?? 0;
    if (x < y) return "behind";
    if (x > y) return "ahead";
  }
  return "same";
}

// 数値セグメント列へ。先頭から数値の並びだけ拾い、非数値が出たらそこで打ち切る
// （以降はビルド sha などの接尾辞扱い）。先頭から数値が取れない形は null＝判定不能。
function segs(v: string | undefined): number[] | null {
  if (!v) return null;
  const out: number[] = [];
  for (const p of v.trim().split(/[.-]/)) {
    if (!/^\d+$/.test(p)) break;
    out.push(parseInt(p, 10));
  }
  return out.length ? out : null;
}
