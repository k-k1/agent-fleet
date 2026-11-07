package auth

import (
	"errors"
	"fmt"
	"strings"
)

// AUTH=dev answers every request as one fixed super_admin with no credentials. It is meant for a
// single-user native install and nothing else, so it must never be what an unset AUTH silently
// falls back to: a hand-written compose file or manifest that forgets the variable would
// otherwise start an open deployment without a word (#3485).
//
// Rules (resolveAuthMode):
//   - native / wsl runtimes are single-user by construction (the runtime itself refuses any
//     other mode), so an unset AUTH there means dev and needs no opt-in.
//   - every other runtime refuses to start with AUTH unset, and accepts AUTH=dev only with
//     AF_ALLOW_INSECURE_DEV_AUTH=1, then warns at start and in the Console (auth_dev).
//   - AUTH=dev is refused anywhere when PUBLIC_BASE_URL is https: an https origin means the
//     Console is meant to be reached from outside the machine.

// AllowInsecureDevAuthEnv is the explicit opt-in for AUTH=dev outside the native runtimes.
const AllowInsecureDevAuthEnv = "AF_ALLOW_INSECURE_DEV_AUTH"

// ResolveMode turns the AUTH environment value into the mode the Control Plane runs in, or
// an error that stops it from starting. runtime is the AF_RUNTIME profile ("local" when unset).
func ResolveMode(auth, runtime, allowInsecure, publicBaseURL string) (string, error) {
	native := runtime == "native" || runtime == "wsl"
	if auth == "" {
		if !native {
			return "", errors.New("AUTH is not set. Set AUTH=oauth (CP-native sign-in) or AUTH=proxy " +
				"(upstream gateway); AUTH=dev (no authentication) needs " + AllowInsecureDevAuthEnv + "=1 outside the native runtime")
		}
		auth = "dev"
	}
	if auth != "dev" {
		return auth, nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(publicBaseURL)), "https://") {
		return "", fmt.Errorf("AUTH=dev refused: PUBLIC_BASE_URL is https (%s), which means the Console is reachable from outside "+
			"this machine and AUTH=dev has no authentication. Use AUTH=oauth or AUTH=proxy", publicBaseURL)
	}
	if !native && allowInsecure != "1" {
		return "", errors.New("AUTH=dev has no authentication and is refused outside the native runtime unless " +
			AllowInsecureDevAuthEnv + "=1 is set explicitly. Use AUTH=oauth or AUTH=proxy for a shared deployment")
	}
	return "dev", nil
}
