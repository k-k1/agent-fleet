// エイリアス（ADR 0067 規律①: 呼び出し側を 1 行も触らない）。
//
// 実体は parts/mcpForm.tsx へ移した。features/repos/ProjectActionPanels.tsx が
// `../settings/mcpForm.tsx` から Field / Meta を引いており、そこは FE-SETTINGS の
// 所有外なので、旧パスをこの 1 枚で生かしておく。
//
// ★ 設定モーダルの外から使われている時点で、この部品は本来 src/ui の住人である。
//    昇格は次のウェーブの別 PR（起動プロンプトの指示）。この 1 枚の回収も同じ機会に。
export * from "./parts/mcpForm.tsx";
