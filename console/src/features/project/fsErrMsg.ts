import { errText, type ApiError } from "../../core/api/client.ts";

/** The message of a rename or delete the Agent refused. A space's folder or marker
 *  (space_protected) is worded in the member's language, with where to delete the space instead;
 *  the server's sentence would say it only in English. Every other refusal keeps the server's
 *  message, as the file tree always showed it. */
export const fsErrMsg = (error: unknown): string => {
  const e = error as ApiError | null;
  if (e && typeof e === "object" && e.code === "space_protected") return errText(e);
  return (e as { message?: string } | null)?.message || String(error);
};
