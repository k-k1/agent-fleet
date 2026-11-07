package main

// Every key derived from the master key (masterDigest(AF_MASTER_KEY), manager.master32), in
// one place.
//
// Policy: a NEW key derived from the master key takes its own label, as
// HMAC-SHA256(master, "af-<purpose>/v<N>"), and is added to this list in the same change.
// Never reuse a key across purposes and never use the master digest itself as a key. The
// derivations below are older and stay as they are: changing one invalidates what it
// protects.
//
//	af-kek:<keyRef>                      per-tenant KEK of the local custodian (custodian.go)
//	<userKey> (no label)                 legacy per-member DEK (dek.go, legacyDEK)
//	af-token-signing-root/v<N>           token signing root at AF_TOKEN_SIGN_VERSION>=1 (bridge_epoch.go);
//	                                     the master itself at version 0
//	af-git-token-sign/v1                 git HTTP token (git_http.go)
//	af-git-oauth-token-sign/v1           Git OAuth bridge token (git_oauth_bridge.go)
//	af-memo-token-sign/v1                memo token (memo_bridge.go)
//	af-schedule-token-sign/v1            schedule token (schedule_bridge.go)
//	af-mcp-token-sign/v1                 MCP token (internal/mcpsrv/mcp_server_bridge.go)
//	af-docs-token-sign/v1                docs token (docs_bridge.go)
//	af-engine-token-sign/v1              engine token (engine_token.go)
//	af-aws-profiles-token-sign/v1        AWS profiles token (aws_profiles_bridge.go)
//	af-gcp-profiles-token-sign/v1        GCP profiles token (gcp_profiles_bridge.go)
//	af-branch-rules-token-sign/v1        branch rules token (tenant_branch_rules.go)
//	af-member-origin-sign/v<N>           member-origin Ed25519 seed (member_origin.go)
//
// AF_COOKIE_SECRET is a separate secret and is not derived from the master key; its
// per-purpose cookie keys are listed in oauth.go (cookiePurpose).
