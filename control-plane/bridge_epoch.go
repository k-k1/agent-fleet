// bridge_epoch.go — revocable per-membership bridge tokens (issue #3452).
//
// A bridge token is a deterministic HMAC of a signing key and the membership id. Nothing but
// the membership check retired one: re-activating the same membership brought the same
// values back, and short of changing the master key (which makes every member's store
// unreadable) there was no deployment-wide revocation. Two knobs fix that.
//
//   - A per-membership EPOCH (membership.bridge_epoch) is mixed into the signing key the
//     token is minted and verified under. Removal, re-activation and an administrator's
//     rotation advance it; only the current epoch verifies, so a token minted earlier is
//     refused on its next use. Epoch 0 uses the signing key unchanged, so every token minted
//     before the epoch existed stays valid until the first advance: upgrading invalidates
//     nothing (pinned by TestBridgeKeyEpochZeroIsLegacy).
//   - A deployment-wide signing VERSION (AF_TOKEN_SIGN_VERSION) selects the root every token
//     family derives its key from. 0 (the default) is the master itself, which is what every
//     token minted so far used. 1 and up derive a root of their own from the master, so the
//     token keys are separated from the credential keys (the DEK derives from the master
//     directly) and rotating the version rotates every token without touching a stored
//     credential. Changing it invalidates every token: restart every workspace afterwards.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

const tokenSignVersionEnv = "AF_TOKEN_SIGN_VERSION"

// checkTokenSignVersion validates AF_TOKEN_SIGN_VERSION. Unset is 0, the legacy root. A value
// that is set but not a non-negative integer is an error, and main refuses to start on it: the
// obvious fallback (0) would kill every token of a deployment running version 1 or later and
// bring back the tokens of the same epoch under version 0.
func checkTokenSignVersion() error {
	raw := strings.TrimSpace(os.Getenv(tokenSignVersionEnv))
	if raw == "" {
		return nil
	}
	if v, err := strconv.Atoi(raw); err != nil || v < 0 {
		return fmt.Errorf("%s=%q is not a non-negative integer", tokenSignVersionEnv, raw)
	}
	return nil
}

// tokenSignVersion reads AF_TOKEN_SIGN_VERSION. checkTokenSignVersion has already refused a
// malformed value at startup, so the 0 returned for one is reached only by a test.
func tokenSignVersion() int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(tokenSignVersionEnv)))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// tokenSigningRootFor is the root the token families derive their keys from at a version.
func tokenSigningRootFor(master []byte, version int) []byte {
	if version <= 0 {
		return master
	}
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("af-token-signing-root/v" + strconv.Itoa(version)))
	return mac.Sum(nil)
}

// tokenSigningRoot is what the git / memo / schedule / MCP / docs / engine / OAuth / profile /
// branch-rule signing keys derive from. tokenSignMaster stays the raw master: the member-origin
// keys have a version of their own (member_origin.go) and a running Agent holds their public
// halves, which a root change must not move.
func (m *manager) tokenSigningRoot() []byte {
	return tokenSigningRootFor(m.tokenSignMaster(), tokenSignVersion())
}

// bridgeEpochDomain prefixes the key derivation of every epoch above 0. It starts with a NUL,
// which no label of the families contains, and the epoch is decimal and NUL-terminated.
const bridgeEpochDomain = "\x00af-bridge-epoch\x00"

// bridgeKeyAt is the key a family's tokens of one membership are minted and verified under at
// an epoch. Epoch 0 is the family key itself.
func bridgeKeyAt(signKey []byte, epoch int64) []byte {
	if epoch == 0 {
		return signKey
	}
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(bridgeEpochDomain + strconv.FormatInt(epoch, 10) + "\x00"))
	return mac.Sum(nil)
}

// bridgeEpoch is the membership's current epoch. ok=false when it cannot be read or the
// membership is not active: a token is then refused, and a mint is skipped (a token minted
// under a guessed epoch would replace a working one with one that fails). A manager without a
// store (some unit tests) has only epoch 0.
func (m *manager) bridgeEpoch(ctx context.Context, membershipID string) (int64, bool) {
	if m == nil || m.store == nil {
		return 0, true
	}
	epoch, ok, err := m.store.BridgeEpoch(ctx, membershipID)
	if err != nil {
		log.Printf("bridge epoch of %s: %v", membershipID, err)
		return 0, false
	}
	return epoch, ok
}

// bridgeTokenMembership reads the membership id every bridge token embeds: "<prefix>_<b64 id>.<tag>".
// It proves nothing; the tag is checked under the epoch that id has.
func bridgeTokenMembership(token string) (string, bool) {
	body := strings.TrimSpace(token)
	us := strings.IndexByte(body, '_')
	dot := strings.LastIndexByte(body, '.')
	if us < 0 || dot < us {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body[us+1 : dot])
	if err != nil || len(raw) == 0 || bytes.IndexByte(raw, 0) >= 0 {
		return "", false
	}
	return string(raw), true
}

// bridgeKeyFor is signKey bound to the current epoch of the membership the token names.
func (m *manager) bridgeKeyFor(ctx context.Context, signKey []byte, token string) ([]byte, bool) {
	mid, ok := bridgeTokenMembership(token)
	if !ok {
		return nil, false
	}
	epoch, ok := m.bridgeEpoch(ctx, mid)
	if !ok {
		return nil, false
	}
	return bridgeKeyAt(signKey, epoch), true
}

// verifyBridge verifies a token of one family under its membership's CURRENT epoch and returns
// the membership id. verify is the family's own verifier, which checks prefix, shape and tag.
// The epoch is read live on every request, for the reason the role is: a rotation has to take
// effect on the very next call.
func (m *manager) verifyBridge(ctx context.Context, signKey []byte, token string, verify func(signKey []byte, token string) (string, bool)) (string, bool) {
	mid, _, ok := m.verifyBridgeAt(ctx, signKey, token, verify)
	return mid, ok
}

// verifyBridgeAt is verifyBridge that also returns the epoch the token verified under, for a
// caller that signs something on the strength of the verification (engine session tokens).
func (m *manager) verifyBridgeAt(ctx context.Context, signKey []byte, token string, verify func(signKey []byte, token string) (string, bool)) (string, int64, bool) {
	mid, ok := bridgeTokenMembership(token)
	if !ok {
		return "", 0, false
	}
	epoch, ok := m.bridgeEpoch(ctx, mid)
	if !ok {
		return "", 0, false
	}
	got, ok := verify(bridgeKeyAt(signKey, epoch), token)
	if !ok || got != mid {
		return "", 0, false
	}
	return got, epoch, true
}

// rotateBridgeTokens advances one membership's bridge and git epochs. The runtime memo carries
// the start env with the old tokens, so it is evicted: the next start mints under the new epoch.
// found=false when there is no such membership.
func (m *manager) rotateBridgeTokens(ctx context.Context, membershipID string) (epoch int64, found bool, err error) {
	epoch, found, err = m.store.BumpBridgeEpoch(ctx, membershipID)
	if err != nil || !found {
		return 0, found, err
	}
	m.evictMembershipCache(membershipID)
	return epoch, true, nil
}

// advanceAllBridgeEpochs is the one-shot that invalidates every token minted so far, those
// left in old task-definition revisions and logs included. It returns how many memberships
// moved. Running workspaces then hold dead tokens until restarted: their start marker no
// longer equals the epoch, which the Console shows as "restart required".
func (m *manager) advanceAllBridgeEpochs(ctx context.Context) (int64, error) {
	n, err := m.store.BumpAllBridgeEpochs(ctx)
	if err != nil {
		return 0, err
	}
	m.evictAllMembershipCaches()
	return n, nil
}

// bridgeTokensStale reports that the membership's bridge epoch moved since its container was
// started, so the running container holds tokens that no longer verify. The Console shows it
// as the "restart required" badge (workspace_handlers.go). When in doubt, false.
func (m *manager) bridgeTokensStale(ctx context.Context, membershipID string) bool {
	if m == nil || m.store == nil || membershipID == "" {
		return false
	}
	started, current, ok, err := m.store.BridgeEpochStarted(ctx, membershipID)
	return err == nil && ok && started != current
}
