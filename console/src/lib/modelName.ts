// Display form of a model id. Shared by the mirror's turn header and the assistant
// chat's reply header so a model reads the same wherever it is shown: the vendor
// prefix, the release date and the tier separators are noise once the turn already
// says which agent ran. The raw id stays available (title attribute) because that is
// what a user pastes back into a model picker.
//
// "claude-opus-4-8" → "opus 4.8", "claude-sonnet-5-20260501" → "sonnet 5".
// Ids that don't end in a version (codex "gpt-5.6-codex", opencode
// "opencode-go/glm-5.2") pass through untouched — guessing at other vendors'
// shapes would mangle more than it shortens.
export function prettyModel(m: string) {
  return m
    .replace(/^claude-/, "")
    .replace(/-\d{8}$/, "") // release date: the API reports dated ids (…-5-20260501)
    .replace(/-(\d+)-(\d+)$/, " $1.$2")
    .replace(/-(\d+)$/, " $1")
    .replace(/-latest$/, "");
}
