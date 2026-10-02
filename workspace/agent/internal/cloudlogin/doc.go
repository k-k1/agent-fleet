// Package cloudlogin is the provider-neutral half of the Console login (ADR 0102; ADR 0107
// "What carries over from AWS" and decision 3): a wrapper run that needs a login and has
// nobody at a terminal files a request, the Console shows a toast, and the login process
// starts only when the member presses "Log in" there.
//
// What lives here, for every cloud alike:
//
//   - Requests: one file per request key under the Agent's state directory, read and
//     written by the wrapper and the Agent (both running as the member) under one lock.
//     A request expires RequestTTL after the last run asked for it, never while an
//     attempt started from it runs; a cancel leaves a marker that holds new runs back
//     for CancelHold.
//   - The outbox notice of a new request, whose payload is only the request id: the
//     outbox is writable by every agent, so anything else in it would be text an agent
//     could forge.
//   - Attempts: one login process per request key, in its own process group, with an id
//     that cannot be guessed and is returned only to the caller that started it. A new
//     attempt for the key replaces the running one.
//   - Wait: the wrapper's wait for the member, ending in credentials or in an error the
//     wrapper turns into exit 3.
//
// What a backend (internal/awsx today, internal/gcpx for ADR 0107) supplies, and what
// this package must keep able to express:
//
//   - Backend.State reads what the credential store holds for a request key, and
//     Backend.Landed decides whether a state read later shows a login that may have
//     settled a request recorded against an earlier one. The package only stores and
//     compares states; it never interprets them. AWS compares SSO token caches. Google
//     Cloud resolves a request filed because an API rejected the token only by a login
//     completed after it, never by the same cached credential succeeding again: its state
//     carries a mark of completed logins, so "landed" can mean exactly that.
//   - WaitSpec.Check obtains credentials and WaitSpec.LoginNeeded classifies its errors:
//     only an error a login fixes keeps the run waiting; any other ends the request with
//     its reason (for gcloud, permission denied or a disabled API, not invalid_grant).
//   - Process describes the login process: how it is run, how its output is parsed and
//     validated into the URL (and, for AWS, the code) shown to the tab that started it,
//     and what its exit means. A Process with Stdin set keeps a pipe to the process, and
//     Attempt.Submit writes a code the member pasted to it once, under the attempt's
//     lock, and only while the attempt waits for it (gcloud's --no-launch-browser
//     login reads the verification code from stdin).
//
// This package must import no backend: the backends import it.
//
// The request and marker files keep the JSON names the AWS backend first wrote
// ("ssoSession" for the request key, "cache" for the recorded state), so an Agent
// upgraded while a request is pending still reads it. To another backend they are only
// names.
package cloudlogin
