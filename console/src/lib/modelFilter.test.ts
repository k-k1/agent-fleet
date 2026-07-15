import { describe, expect, it } from "vitest";
import type { ModelOption } from "./agentModels.ts";
import { filterModelOptions } from "./modelFilter.ts";

const options: ModelOption[] = [
  ["", "既定"],
  ["opencode-go/deepseek-v4-pro", "DeepSeek V4 Pro"],
  ["opencode-go/kimi-k2.7-code", "Kimi K2.7 Code"],
];

describe("filterModelOptions", () => {
  it("表示名を大文字小文字を区別せずに絞り込む", () => {
    expect(filterModelOptions(options, "  DEEPseek  ")).toEqual([options[1]]);
  });

  it("provider/model ID でも絞り込む", () => {
    expect(filterModelOptions(options, "opencode-go/kimi")).toEqual([options[2]]);
  });

  it("空の検索語では全候補を返す", () => {
    expect(filterModelOptions(options, "   ")).toBe(options);
  });
});
