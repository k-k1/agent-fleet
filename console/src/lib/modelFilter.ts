import type { ModelOption } from "./agentModels.ts";

// Model catalogs can contain dozens of provider/model ids. Match both the
// human-facing label and the exact launch value so either form can be pasted.
export function filterModelOptions(options: ModelOption[], query: string): ModelOption[] {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return options;
  return options.filter(
    ([value, label]) => value.toLocaleLowerCase().includes(q) || label.toLocaleLowerCase().includes(q),
  );
}
