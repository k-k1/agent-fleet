// cx — tiny className joiner (truthy parts only). The ui/ primitives' only helper.
export const cx = (...parts: Array<string | false | null | undefined>): string =>
  parts.filter(Boolean).join(" ");
