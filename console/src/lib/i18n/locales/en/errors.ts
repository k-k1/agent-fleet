// English カタログ / ドメイン: errors
// キー接頭辞: err
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { errors as jaErrors } from "../ja/errors.ts";

export const errors: Record<keyof typeof jaErrors, string> = {
  // --- API errors (mirror of ERR_TEXT + inline fallbacks) ---
  "err.ip_not_allowed":
    "This tenant can only be used from the networks its administrator allows, and this one is not among them.",
  "err.ssm_search_forbidden":
    "You don't have permission to search AWS instances. Ask your AWS administrator to grant ssm:DescribeInstanceInformation.",
  "err.quota_sessions":
    "You've reached the limit on concurrently running sessions. Stop one of the running sessions before creating another.",
  "err.sessions_running":
    "This working copy has running sessions. Switching would swap and break the working tree underfoot, so it's blocked here. Open the branch as a separate working copy instead.",
  "err.branch_in_use":
    "Another working copy already has this branch checked out. git allows one working copy per branch — open that copy, or pick a different branch.",
  "err.sessions_running_delete":
    "This working copy has running sessions. Deleting would remove the working directory underfoot and break them, so stop those sessions first.",
  "err.worktree_dirty":
    "This worktree has uncommitted/unpushed changes. Force-deleting it will lose them.",
  "err.has_worktrees":
    "This working copy has derived worktrees attached to it. Delete the worktrees first.",
  "err.locked":
    "This is locked against deletion. Unlock it first, then delete.",
  "err.locked_sessions":
    "This working copy hosts sessions that are locked against deletion; removing it would leave them unresumable. Unlock those sessions first.",
  "err.worktree_remove_failed": "Failed to remove the worktree.",
  "err.question_pending":
    "The agent is waiting for an answer to its question. Answer it from the question card before sending.",
  "err.plan_pending":
    "The agent is waiting for a plan decision. Approve or reject it from the plan card before sending — typed text would be swallowed by the dialog and approve the plan.",
  "err.permission_pending":
    "The agent is waiting for a permission decision. Allow or deny it from the permission card before sending — typed text would be swallowed by the menu and allow it.",
  "err.interaction_pending":
    "The agent is showing an interactive prompt. Answer it from its card before sending.",
  "err.auth_expired":
    "This workspace's Claude login has expired. Re-authenticate from Settings > Agents before sending (sent now, the terminal would take the text but no turn would ever start).",
  "err.not_running": "The session is stopped. Resume it before sending.",
  // The workspace is mid-boot (container up, Agent not answering yet) and something
  // that needs the Agent arrived. Not a failure — a "not yet", so it asks for a retry.
  "err.workspace_starting": "The workspace is still starting. Try again once it is ready.",
  "err.driver_unavailable": "Managed execution isn't available for this agent.",
  "err.runtime_failed": "Couldn't start the agent. Wait a moment and try again.",
  // The line above is for failures that waiting fixes. A shared daemon that was not
  // started because the CLI is not signed in never fixes itself, so it gets its own
  // code instead of collapsing into runtime_failed (paired with runtime_err.go on the
  // Agent). Which login is missing differs per kind — errDetail() appends the server's
  // message for that.
  "err.agent_not_connected":
    "Couldn't start: you aren't signed in to this agent. Connect it from Settings > Agents.",
  "err.send_failed": "Failed to send.",
  "err.network": "Network error",
  "err.unknown": "An unknown error occurred.",
  // per-tenant login (docs/log/61 §61.9). provider_required has a dedicated modal that
  // offers the re-sign-in link; this string is the fallback outside it.
  "err.provider_required": "This tenant needs a different sign-in method. Please sign in again.",
  "err.not_provisioned": "You don't belong to any tenant yet. Ask an administrator to add you.",
  "err.domain_not_allowed": "That email domain can't be invited to this tenant.",
  "err.email_required": "This tenant restricts invites by domain. Invite by email address.",
  "err.auto_join_conflict": "That auto-join domain already belongs to another tenant.",
  "err.unknown_provider": "That sign-in method isn't enabled on this deployment.",
  "err.self_removal": "You can't remove your last membership — it is the way back in. Ask another administrator.",
  "err.bad_share": "That share request is invalid.",
  "err.member_not_found": "That recipient isn't a member of this tenant. Pick one from the search results.",
  "err.share_self": "You can't share with yourself.",
  "err.workspace_not_running": "The owner's workspace must be running.",
  "err.share_target_not_found": "The share target could not be found.",
  "err.owner_session_archived": "The owner archived this session, so it is no longer listed for recipients. It comes back if they restore it.",
  "err.settings_change_failed": "Couldn't change the setting.",
  "err.bad_path": "The file path is invalid.",
  "err.symlink_not_allowed": "Files reached through symbolic links can't be used.",
  "err.bad_request": "The request is malformed.",
  "err.unsupported_media_type": "Only JSON requests are accepted.",
  "err.denied": "This file can't be accessed.",
  "err.not_file": "The target regular file was not found.",
  "err.revision_conflict": "The file changed after it was read.",
  "err.too_large": "The file or request is too large.",
  "err.binary_not_supported": "Binary files and unsupported text encodings can't be edited.",
  "err.unsupported_newline": "Files with CRLF or CR newlines can't be edited yet.",
  "err.read_failed": "Failed to read the file.",
  "err.write_failed": "Failed to save the file.",
  "err.write_state_unknown": "The content is live, but its durability couldn't be confirmed.",
  // docs/log/28 P3: workspace/agent handler stable codes (mirror of errcodes.go).
  "err.chat_assistant_not_found": "Assistant not found.",
  "err.chat_agent_unsupported": "Unsupported agent.",
  "err.chat_prompt_empty": "The prompt is empty.",
  "err.chat_title_empty": "The display name is empty.",
  "err.chat_message_empty": "The message is empty.",
  "err.chat_conversation_not_found": "Conversation not found.",
  "err.chat_nothing_to_compact": "Nothing to compact yet (available after the first reply).",
  "err.conn_api_key_required": "Enter an API key.",
  "err.conn_grafana_fields_required": "Enter the Grafana URL and service account token.",
  "err.conn_jira_fields_required": "Enter the Jira account email and API token.",
  "err.conn_url_scheme": "The URL must start with http(s)://.",
  "err.conn_aws_profile_required": "Specify an AWS profile.",
  "err.conn_sso_region_missing": "No SSO region found (check the SSM profile configuration).",
  "err.conn_discord_token_required": "Enter a Discord bot token.",
  "err.conn_discord_destination_required": "Enter exactly one destination (channel ID or user ID).",
  "err.conn_discord_destination_invalid": "The destination must be a numeric Discord ID (use Developer Mode → Copy ID).",
  "err.conn_discord_token_invalid": "Discord rejected the token (check the bot token).",
  "err.conn_slack_token_required": "Enter a Slack bot token (xoxb-).",
  "err.conn_slack_destination_required": "Enter a channel ID or a user ID (receive also needs the bound user ID).",
  "err.conn_slack_destination_invalid": "The destination must be a Slack ID (channel C…, user U…).",
  "err.conn_slack_token_invalid": "Slack rejected the token (check the bot / app-level token).",
  "err.conn_slack_app_token_required": "Receiving replies needs an app-level token (xapp-).",
  // MCP registry (docs/log/48)
  "err.mcp_not_found": "MCP server not found.",
  "err.mcp_read_only": "This server is not editable (it can only be disabled).",
  "err.mcp_name_taken": "A server with this name is already registered.",
  "err.mcp_invalid": "The MCP server definition is invalid.",
  "err.mcp_name_invalid": "Use 1-48 characters of letters, digits, dash or underscore, starting with a letter or digit.",
  "err.mcp_name_reserved": "That name is reserved by Agent Fleet.",
  "err.mcp_transport_unsupported": "Unsupported transport (stdio / remote HTTP only).",
  "err.mcp_command_required": "A stdio server needs a command.",
  "err.mcp_stdio_no_url": "A stdio server cannot carry a URL or headers.",
  "err.mcp_tenant_stdio": "Tenant-distributed MCP servers cannot use stdio (remote only).",
  "err.mcp_url_required": "A remote server needs a URL.",
  "err.mcp_url_invalid": "That URL cannot be parsed.",
  "err.mcp_url_scheme": "The URL must be http or https.",
  "err.mcp_url_host": "The URL has no host.",
  "err.mcp_url_credentials": "Do not embed credentials in the URL — use a header.",
  "err.mcp_http_no_command": "A remote server cannot carry a command, arguments or environment variables.",
  "err.mcp_env_name_invalid": "Invalid environment variable name.",
  "err.mcp_header_name_invalid": "Invalid header name.",
  "err.mcp_header_value_invalid": "A header value cannot contain a newline.",
  "err.mcp_kind_unknown": "Unknown agent kind.",
  "err.mcp_timeout_range": "The timeout must be between 1000 and 120000 ms.",
  "err.mcp_headers_unreadable": "The stored headers cannot be decrypted — re-enter every header value.",
  // Egress allowlist requests (docs/log/48 §9 / control-plane/egress_member.go)
  "err.egress_entry_invalid":
    "An allowlist entry must be a host or a .suffix.example.com — no scheme, port or path.",
  "err.egress_entry_too_broad": "A whole TLD (.com and the like) cannot be requested — name a domain.",
  "err.egress_too_many_proposals": "Too many pending requests — ask an administrator to work through the queue.",
  "err.mcp_tenant_bridge_off":
    "Tenant distribution is unavailable in this deployment (the CP public URL / token is unset).",
  "err.mcp_tenant_fetch_failed": "Could not fetch the tenant set.",
  "err.assistant_not_found": "Assistant not found.",
  "err.assistant_builtin_readonly_edit": "Built-in assistants can't be edited.",
  "err.assistant_builtin_readonly_delete": "Built-in assistants can't be deleted.",
  "err.paste_too_large": "The file is too large.",
  "err.paste_unsupported_kind": "This session type can't accept images.",
  "err.paste_unsupported_agent": "Only claude / codex assistants can accept images.",
  "err.fork_unsupported_kind": "This session type doesn't support forking.",
  "err.fork_missing_dir": "Can't fork: the working folder doesn't exist.",
  "err.fork_at_unsupported": "This session can't branch from a past message (managed sessions only).",
  "err.fork_bad_anchor": "That branch point can't be used. Reload the chat and try again.",
  "err.title_feature_disabled": "AI suggestions are off (turn on title auto-suggestion in Display settings).",
  "err.title_no_content": "Not enough conversation yet (try again after a few exchanges).",
  "err.memory_bad_request": "The request is malformed.",
  "err.memory_bad_rev": "No snapshot was found for that point in time.",
  "err.memory_bad_path": "That path is outside the managed memory roots.",
  "err.memory_no_snapshots": "There are no snapshots yet.",
  "err.memory_snapshot_failed": "Failed to take the snapshot.",
  "err.memory_diff_failed": "Failed to load the diff.",
  "err.memory_bad_scope": "The restore scope is invalid.",
  "err.memory_restore_failed": "The restore failed.",
  "err.memory_export_failed": "The export failed.",
  "err.memory_import_failed": "The import failed.",
  "err.memory_bad_import": "That file can't be imported (pick a file exported from another environment).",
  "err.memory_secret_detected": "The contents to export contain possible secrets.",
  "err.memory_too_large": "The file is too large.",
  "err.tenant_idp_link_claim_required":
    "This deployment already has a sign-in method for the same issuer. That issuer gives each app registration a different subject for the same person, so without \"how the same account is recognised\", everybody already using this deployment would be refused at login as a duplicate address.",

  // Agent sign-in / OAuth (opencode, kiro, agy, cursor). The codes are shared across
  // drivers on purpose: the wording holds for every one of them, and the driver-specific
  // cause arrives as the server's message, which errDetail appends.
  "err.already_connected": "Already connected. Disconnect first to sign in again.",
  "err.no_url": "The agent did not return a login URL.",
  "err.no_selector": "The agent did not show the sign-in method options.",
  "err.serve_unavailable": "The agent's service could not be started.",
  "err.login_failed": "The sign-in did not complete.",
  "err.bad_method": "That sign-in method is not valid.",
  "err.method_unsupported": "That sign-in method is not available yet.",
  "err.opencode_unsupported": "opencode was not found (the image may be out of date).",
  "err.kiro_unsupported": "kiro-cli was not found (it may not be installed).",
  "err.cursor_unsupported": "cursor-agent was not found (the image may be out of date).",
  // These carry err.Error() as the whole message, so the catalogue supplies the human
  // framing and errDetail appends the raw cause after it.
  "err.oauth_start_failed": "Could not start the sign-in.",
  "err.oauth_poll_failed": "Could not confirm that the sign-in completed.",
  "err.oauth_disconnect_failed": "Could not disconnect.",
  "err.oauth_error": "The authorization server returned an error.",
  "err.logout_failed": "Could not sign out.",
  "err.store_failed": "Could not save the credentials.",
  "err.pty_failed": "Could not start the agent's CLI.",
  "err.serve_not_ready": "The agent's service did not respond.",
  "err.agy_unsupported": "agy was not found (the image may be out of date).",
  "err.no_flow": "That sign-in attempt is unknown or has expired. Start again.",
  "err.bad_code": "Enter the code.",
  "err.bad_key": "Enter the key.",
  "err.bad_env": "That environment variable name is invalid (use capitals and _, like ANTHROPIC_API_KEY).",
  "err.bad_workspace_id": "The workspace ID is invalid.",
};
