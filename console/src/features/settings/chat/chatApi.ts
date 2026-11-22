// chatApi — typed wrappers for the chat-integration endpoints (Discord / Slack cards and the
// master notification switch in personal settings). The paths stay string literals except the
// shared `PUT api/connections/<kind>` of setChatNotifyOff
// (control-plane/console_routes_test.go reads them as text).
//
// api() can resolve with a JSON `null` body: every wrapper keeps `| null` and the callers keep
// their `res && res.error` handling.
import { apiJSON } from "../../../core/api/client.ts";
import type { ProviderConn } from "../../../types/session.ts";
import type { Rep } from "../workspace/workspaceApi.ts";

/** A chat connection's status as GET /api/connections and the PUT answers carry it: the shared
 *  ProviderConn fields plus what the card edits. `testError` rides only the answer to a connect. */
export type ChatConn = ProviderConn & {
  mode?: "channel" | "dm" | string;
  channelId?: string;
  userId?: string;
  mentionUserId?: string;
  botName?: string;
  teamName?: string;
  operator?: boolean;
  threads?: boolean;
  receive?: boolean;
  mirrorInput?: boolean;
  fullText?: boolean;
  events?: string[];
  testError?: string;
};

/** The PUT body the cards build (edit = token fields empty, the Agent reuses the stored ones). */
export type ChatConnBody = Record<string, unknown>;

export type DiscordInspect = { botName: string; inviteUrl: string };
export type DiscordGuild = {
  name: string;
  ownerId?: string;
  ownerName?: string;
  channels?: { id: string; name: string }[];
};
export type DiscordGuilds = { guilds?: DiscordGuild[] };
export type SlackInspect = { botName: string; teamName: string };
export type SlackChannels = { channels?: { id: string; name: string }[]; resolvedUserId?: string };

export const inspectDiscord = (token: string): Promise<Rep<DiscordInspect> | null> =>
  apiJSON("api/connections/discord/inspect", "POST", { token });
export const listDiscordGuilds = (token: string): Promise<Rep<DiscordGuilds> | null> =>
  apiJSON("api/connections/discord/guilds", "POST", { token });
export const putDiscordConn = (body: ChatConnBody): Promise<Rep<ChatConn> | null> =>
  apiJSON("api/connections/discord", "PUT", body);

/** The master notification switch of personal settings: the same PUT as the cards', one path per
 *  kind. */
export const putChatConn = (kind: "discord" | "slack", body: ChatConnBody): Promise<Rep<ChatConn> | null> =>
  apiJSON(`api/connections/${kind}`, "PUT", body);

export const inspectSlack = (botToken: string, appToken: string): Promise<Rep<SlackInspect> | null> =>
  apiJSON("api/connections/slack/inspect", "POST", { botToken, appToken });
export const listSlackChannels = (botToken: string, email: string): Promise<Rep<SlackChannels> | null> =>
  apiJSON("api/connections/slack/channels", "POST", { botToken, email });
export const putSlackConn = (body: ChatConnBody): Promise<Rep<ChatConn> | null> =>
  apiJSON("api/connections/slack", "PUT", body);
