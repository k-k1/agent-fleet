// A JDK id is what the agent stores and serves for a Java selection (agent
// internal/jdkvendor): a bare major ("21") is Temurin, "<vendor>-<major>" ("corretto-21") is
// another vendor. Every place that shows one to a member goes through these, so a new
// vendor is one entry here instead of a hunt for "Temurin" literals.

// Keys are the agent's id prefixes; "" is Temurin's bare major. Keep it in step with
// jdkvendor.All, which is also what refuses any other prefix.
const VENDORS: Record<string, string> = { "": "Temurin", corretto: "Corretto" };

function parse(id: string): { vendor: string; major: string } | null {
  const m = /^(?:([a-z]+)-)?(\d+)$/.exec(id);
  if (!m) return null;
  const vendor = VENDORS[m[1] ?? ""];
  return vendor ? { vendor, major: m[2] } : null;
}

// jdkVendor is the display name of an id's vendor ("Temurin", "Corretto"), or "" for a
// value that is not an id.
export function jdkVendor(id: string): string {
  return parse(id)?.vendor ?? "";
}

// jdkLabel is the display name of an id ("Temurin 21", "Corretto 21"); anything else is
// shown as is, like the agent's jdkvendor.Label.
export function jdkLabel(id: string): string {
  const p = parse(id);
  return p ? `${p.vendor} ${p.major}` : id;
}
