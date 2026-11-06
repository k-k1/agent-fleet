// A JDK id is what the agent stores and serves for a Java selection (agent
// internal/jdkvendor): a bare major ("21") is Temurin, "<vendor>-<major>" ("corretto-21") is
// another vendor. Every place that shows one to a member goes through these, so a new
// vendor is one entry here instead of a hunt for "Temurin" literals.

const VENDORS: Record<string, string> = { corretto: "Corretto" };

// jdkVendor is the display name of an id's vendor ("Temurin", "Corretto").
export function jdkVendor(id: string): string {
  const m = /^([a-z]+)-\d+$/.exec(id);
  if (!m) return "Temurin";
  return VENDORS[m[1]] ?? m[1];
}

// jdkLabel is the display name of an id ("Temurin 21", "Corretto 21").
export function jdkLabel(id: string): string {
  const m = /^(?:([a-z]+)-)?(\d+)$/.exec(id);
  if (!m) return id;
  return `${jdkVendor(id)} ${m[2]}`;
}
