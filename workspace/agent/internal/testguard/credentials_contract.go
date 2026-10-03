//go:build contract || contract_live || contract_manual

package testguard

// keepCredentials is true in a contract binary: it drives the real CLIs, whose sign-in lives
// under HOME and the agent config dirs, so moving them makes every contract report "not
// signed in" before it tests anything.
const keepCredentials = true
