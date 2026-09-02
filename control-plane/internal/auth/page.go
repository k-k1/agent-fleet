package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// The login page vocabulary and assets. They live beside the providers because
// a provider builds its own button label (DefaultProviderLabel) and Google's
// historical label is one of these strings — keeping the table in the root
// package would have meant duplicating four localized strings across the seam,
// which is exactly how a translation quietly diverges from the button.
//
// The PAGE is still rendered in the root package (config.handleLogin): what is
// here is the vocabulary, not the HTTP layer.

// ProviderInList reports membership in a tenant's allowed_providers; an empty list
// means "every provider the deployment enabled".
func ProviderInList(allowed []string, id string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == id {
			return true
		}
	}
	return false
}

// ProviderIcon returns the inline SVG mark for a provider's button: the Google
// wordmark for Google (unchanged), a neutral key glyph for everything else — CP
// must not ship third-party logos it has no license for.
func ProviderIcon(id string) string {
	if id == GoogleProviderID {
		return googleIconSVG
	}
	return genericIconSVG
}

func LoginErrorBlock(code, lang string) string {
	t := LoginText[lang]
	var msg string
	switch code {
	case "forbidden":
		msg = t.ErrForbidden
	case "denied":
		msg = t.ErrDenied
	case "session", "exchange":
		msg = t.ErrSession
	case "reauth":
		msg = t.ErrReauth
	case "provider":
		msg = t.ErrProvider
	case "email_taken":
		msg = t.ErrEmailTaken
	default:
		return ""
	}
	return `<div class="err">` + msg + `</div>`
}

// PreferredUILang picks the UI language for CP-rendered pages (login / OAuth
// callbacks) from Accept-Language, since these are served before any locale cookie
// exists (docs/log/28 P3). It scans the header's language ranges in order and returns the
// first supported one; Japanese is the default (the product's primary audience and the
// prior hardcoded language). The Console SPA owns locale once signed in.
func PreferredUILang(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(part)
		if i := strings.IndexByte(tag, ';'); i >= 0 { // drop the q-value
			tag = tag[:i]
		}
		switch tag = strings.ToLower(strings.TrimSpace(tag)); {
		case strings.HasPrefix(tag, "ja"):
			return "ja"
		case strings.HasPrefix(tag, "en"):
			return "en"
		}
	}
	return "ja"
}

// LoginText holds the localized strings for the CP-rendered login page. ja is the
// default; en is served when Accept-Language prefers English (PreferredUILang).
type LoginStrings struct {
	Title, Signin, SigninWith, Note                  string
	ErrForbidden, ErrDenied, ErrSession, ErrProvider string
	ErrUnconfigured, ErrReauth                       string
	// Per-tenant login page (docs/log/61 §61.9.3). TenantNote takes the tenant name.
	TenantNote, ErrTenantNoProvider string
	// ErrEmailTaken: a tenant-defined provider asserted an address that already
	// belongs to an account on this deployment (docs/log/61 §61.11 rule 2').
	ErrEmailTaken string
	// The new-account notice (docs/log/61 受入条件 3). NewBody takes the email.
	NewTitle, NewBody, NewNote, NewContinue, NewSwitch string
	// The result page of a link flow (docs/log/61 §61.16 + 決定 37).
	LinkTitle, LinkNote, LinkBack          string
	LinkOK, LinkTaken, LinkEmail, LinkGate string
	LinkSession, LinkProvider, LinkFailed  string
}

var LoginText = map[string]LoginStrings{
	"ja": {
		Title:           "Agent Fleet — サインイン",
		Signin:          "Google でサインイン",
		SigninWith:      "%s でサインイン",
		Note:            "アクセスは許可されたアカウントに限定されています。",
		ErrForbidden:    "このアカウントはアクセスを許可されていません。管理者にメールアドレスの追加を依頼してください。",
		ErrDenied:       "サインインがキャンセルされました。もう一度お試しください。",
		ErrSession:      "セッションの確立に失敗しました。もう一度サインインしてください。",
		ErrProvider:     "指定されたサインイン方法は利用できません。下のボタンから選び直してください。",
		ErrUnconfigured: "サインイン方法が設定されていません。管理者に連絡してください。",
		ErrReauth:       "セッションの確認ができなくなりました。もう一度サインインしてください。",
		TenantNote:      "%s のサインインページです。アクセスは許可されたアカウントに限定されています。",
		ErrTenantNoProvider: "このテナントに設定されたサインイン方法は、現在このデプロイでは利用できません。" +
			"管理者に連絡してください。",
		ErrEmailTaken: "このメールアドレスは、すでにこのデプロイの別のサインイン方法で使われています。" +
			"いつも使っているサインイン方法でログインしたうえで、" +
			"<b>設定 → アカウント → サインイン方法</b> からこの方式を追加してください。",
		NewTitle: "Agent Fleet — 新しいアカウント",
		NewBody: "%s でサインインしました。このメールアドレスはこのデプロイで使われたことがないため、" +
			"<b>新しいワークスペース</b>を作成しました。以前から使っているワークスペースがある場合、" +
			"このアカウントからは入れません。",
		NewNote: "以前のワークスペースに入るには、いつも使っているメールアドレスでサインインし直してください。" +
			"メールアドレスの違うアカウント同士を後から 1 つにまとめることはできません。",
		NewContinue: "新しいワークスペースで続ける",
		NewSwitch:   "サインアウトして入り直す",
		LinkTitle:   "Agent Fleet — サインイン方法の追加",
		LinkNote:    "この画面はサインイン方法を追加したときだけ表示されます。",
		LinkBack:    "Agent Fleet に戻る",
		LinkOK:      "このサインイン方法を、いまのアカウントに追加しました。次回からどちらの方法でも同じワークスペースに入れます。",
		LinkTaken: "このサインイン方法は、すでにこのデプロイの別のアカウントで使われています。" +
			"アカウント同士を 1 つにまとめることはできません。",
		LinkEmail: "このサインイン方法が名乗ったメールアドレスは、いまのアカウントのものと違います。" +
			"追加できるのは、同じメールアドレスを名乗る方法だけです。",
		LinkGate: "このサインイン方法では、いまのアカウントは許可されていません。" +
			"組織（org）への参加やドメインの許可について、管理者に確認してください。",
		LinkSession: "サインインの状態が確認できませんでした。サインインし直してから、もう一度お試しください。",
		LinkProvider: "指定されたサインイン方法は利用できません。" +
			"設定の「サインイン方法」から選び直してください。",
		LinkFailed: "サインイン方法の追加に失敗しました。もう一度お試しください。",
	},
	"en": {
		Title:           "Agent Fleet — Sign in",
		Signin:          "Sign in with Google",
		SigninWith:      "Sign in with %s",
		Note:            "Access is limited to allowed accounts.",
		ErrForbidden:    "This account isn't allowed access. Ask an administrator to add your email address.",
		ErrDenied:       "Sign-in was canceled. Please try again.",
		ErrSession:      "Couldn't establish a session. Please sign in again.",
		ErrProvider:     "That sign-in method isn't available. Pick one of the buttons below.",
		ErrUnconfigured: "No sign-in method is configured. Please contact your administrator.",
		ErrReauth:       "We couldn't re-verify your session. Please sign in again.",
		TenantNote:      "Sign-in page for %s. Access is limited to allowed accounts.",
		ErrTenantNoProvider: "The sign-in methods configured for this tenant aren't available on this " +
			"deployment. Please contact your administrator.",
		ErrEmailTaken: "This email address is already used by another sign-in method on this deployment. " +
			"Sign in the way you normally do, then add this method under " +
			"<b>Settings → Account → Sign-in methods</b>.",
		NewTitle: "Agent Fleet — New account",
		NewBody: "You signed in as %s. That address hasn't been used on this deployment, " +
			"so a <b>new workspace</b> was created. A workspace you already had is not " +
			"reachable from this account.",
		NewNote: "To get back to it, sign in again with the address you normally use. " +
			"Accounts under different email addresses cannot be merged afterwards.",
		NewContinue: "Continue to the new workspace",
		NewSwitch:   "Sign out and use another account",
		LinkTitle:   "Agent Fleet — Add a sign-in method",
		LinkNote:    "This page only appears when you add a sign-in method.",
		LinkBack:    "Back to Agent Fleet",
		LinkOK: "This sign-in method was added to your account. From now on either method " +
			"takes you to the same workspace.",
		LinkTaken: "That sign-in method already belongs to another account on this deployment. " +
			"Accounts cannot be merged.",
		LinkEmail: "The address that sign-in method asserted is not the one on your account. " +
			"Only a method that asserts the same address can be added.",
		LinkGate: "That sign-in method doesn't allow this account. Ask your administrator about " +
			"the organization membership or the allowed domains.",
		LinkSession:  "We couldn't confirm you were signed in. Please sign in again and retry.",
		LinkProvider: "That sign-in method isn't available. Pick one under Settings → Sign-in methods.",
		LinkFailed:   "Couldn't add the sign-in method. Please try again.",
	},
}

// DefaultProviderLabel builds a button label for a provider that declared no
// AF_OIDC_<ID>_LABEL_*: "<Id> でサインイン" / "Sign in with <Id>".
func DefaultProviderLabel(id, lang string) string {
	t, ok := LoginText[lang]
	if !ok {
		t = LoginText["ja"]
	}
	name := id
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	return fmt.Sprintf(t.SigninWith, name)
}

const googleIconSVG = `<svg viewBox="0 0 48 48" aria-hidden="true">
    <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/>
    <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/>
    <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/>
    <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/>
   </svg>
   `

const genericIconSVG = `<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="#1f2937" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <rect x="3" y="11" width="18" height="10" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>
   </svg>
   `

// LoginPageHTML — self-contained (inline CSS, no external assets but the brand
// banner). The banner carries the wordmark + tagline; if it fails to load the
// text wordmark below it shows instead. Tokens {{ERROR}} and {{BUTTONS}} are
// substituted by handleLogin (no fmt verbs — the CSS contains literal % units).
const LoginPageHTML = `<!doctype html><html lang="{{LANG}}"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<style>
:root{--teal:#2aa79b;--ink:#e8eef6;--muted:#9fb0c4}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;
 font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--ink);
 background:radial-gradient(1200px 600px at 50% -10%,#1d3357,#0c1626)}
.card{width:min(94vw,560px);background:#0f1c30;border:1px solid #22344f;border-radius:16px;
 overflow:hidden;box-shadow:0 20px 60px rgba(0,0,0,.45)}
.hero{display:block;width:100%;height:auto;background:#0d3b66}
.body{padding:28px 32px 32px;text-align:center}
.wordmark{display:none;font-size:34px;font-weight:800;letter-spacing:.5px;margin:8px 0}
.wordmark b{color:var(--teal)}
.tag{color:var(--muted);letter-spacing:3px;font-size:12px;text-transform:uppercase;margin:0 0 22px}
.btns{display:grid;gap:10px}
.gbtn{display:inline-flex;align-items:center;gap:12px;justify-content:center;width:100%;
 padding:13px 18px;border-radius:10px;border:0;cursor:pointer;background:#fff;color:#1f2937;
 font-size:15px;font-weight:600;text-decoration:none}
.gbtn:hover{background:#f1f3f5}
.gbtn svg{width:20px;height:20px}
.note{margin-top:18px;color:var(--muted);font-size:13px}
.err{margin:0 0 18px;padding:11px 14px;border-radius:9px;background:rgba(220,68,68,.12);
 border:1px solid rgba(220,68,68,.4);color:#ffb4b4;font-size:14px;text-align:left}
.msg{margin:0 0 18px;padding:11px 14px;border-radius:9px;background:rgba(42,167,155,.12);
 border:1px solid rgba(42,167,155,.45);color:#cfe9e5;font-size:14px;text-align:left}
.gbtn.ghost{background:transparent;color:var(--ink);border:1px solid #22344f}
.gbtn.ghost:hover{background:#16273f}
</style></head><body>
<main class="card">
 <img class="hero" src="/brand/agent-fleet-banner.webp" alt="Agent Fleet — Deploy. Connect. Scale."
  onerror="this.style.display='none';document.getElementById('wm').style.display='block';document.getElementById('tg').style.display='block'">
 <div class="body">
  <div id="wm" class="wordmark">Agent <b>Fleet</b></div>
  <p id="tg" class="tag" style="display:none">Deploy. Connect. Scale.</p>
  {{ERROR}}
  <div class="btns">
  {{BUTTONS}}
  </div>
  <p class="note">{{NOTE}}</p>
 </div>
</main>
</body></html>`
