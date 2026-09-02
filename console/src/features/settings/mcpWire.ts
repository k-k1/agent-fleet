// エイリアス（ADR 0067 規律①: 呼び出し側を 1 行も触らない）。
//
// 実体は mcp/mcpWire.ts へ移した。features/chat/AssistantModal.tsx が
// `../settings/mcpWire.ts` から McpServer 型を引いており、そこは FE-SETTINGS の
// 所有外なので、旧パスをこの 1 枚で生かしておく。
//
// ★ この 1 枚の回収（呼び出し側を新しいパスへ張り替えて消す）は、ウェーブ境界で
// 別セッションが行う。ここで消すと features/chat と衝突する。
export * from "./mcp/mcpWire.ts";
