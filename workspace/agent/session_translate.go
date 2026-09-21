package main

// Mirror translation (docs/log/97): one finished answer, re-read in the language the reader
// reads in, on a button press — without spending a turn of the session itself.
//
// Generation goes through the same backend-agnostic one-shot channel as the title, reply and
// edit suggestions (chatx.OneShotHeadless): a separate short-lived CLI process, so the
// session's own agent is never steered, its context is untouched, and the work is not queued
// behind whatever that session is doing.
//
// Why the cache is keyed by a hash of the SOURCE TEXT rather than by the turn:
//   - a transcript line ordinal (transcript.Idx) moves under compaction;
//   - an anchor id is per session, so a fork asks again for text that is character for
//     character the same;
//   - a running turn grows part by part, so "the turn" is not a stable thing to key on, while
//     the text the reader pressed the button on is.
//
// The server hashes the text it received itself. Accepting a client-supplied key would let a
// stale pane read an unrelated entry out of the store, and the key is the only thing deciding
// which translation is shown as this answer's.
//
// The store is per session and is deleted with it (removeSessionSideFiles). Session names are
// SLOT names and get reused: left behind, one conversation's translations would attach to the
// next session in that slot. The hash key makes a wrong hit unlikely, but the disk and the
// answer's privacy are reason enough to remove the file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const (
	// translatePartTimeout: a whole answer is a far longer output than a title, so this is the
	// edit-suggestion tier (90s) rather than the title/reply pair's 60s. Applied per part, and
	// the Console's own timeout is wider still (mirror/translate.ts TRANSLATE_TIMEOUT_MS).
	translatePartTimeout = 90 * time.Second
	// translateMaxBody: wire limit for the request. Above the per-part limit times the part
	// count, so an over-long part is refused by the check that can name it rather than by the
	// decoder.
	translateMaxBody = 1024 * 1024
	// translateMaxPartBytes: one prose part handed to the model (UTF-8 bytes). Far above any
	// real answer; beyond it the model would silently return a shortened text, and a
	// half-translated answer that looks whole is worse than a refusal the Console can word.
	translateMaxPartBytes = 32 * 1024
	// translateMaxParts: prose parts in one request = one turn's worth. A turn with more text
	// blocks than this is not a thing that happens; the cap is here so one press cannot fan out
	// into an unbounded number of CLI runs.
	translateMaxParts = 8
	// Store caps, oldest evicted first. A translation is a regenerable derivative, so losing
	// the oldest ones costs one more press, while an uncapped file would grow with every
	// button press for the life of the session.
	translateStoreMaxEntries = 400
	translateStoreMaxBytes   = 2 * 1024 * 1024
)

// translateModel is the claude-side model for the one-shot. Translating a whole answer is
// quality-sensitive in the same way an edit suggestion is (the reader keeps the text and acts
// on it), so the default is sonnet rather than haiku. It applies to the claude backend only;
// the others follow the prose model in Settings > AI assistance (OneShotProse, docs/log/84).
func translateModel() string { return envOr("AF_TRANSLATE_MODEL", "sonnet") }

// translatePersona is deliberately NOT locale-branched. The rule in docs/log/28 §6.6 branches a
// prompt whose OUTPUT the user reads — but here the output language IS the parameter, so a
// second copy of these instructions would change nothing the reader sees and would only have to
// be kept in step (the same reason fs_suggest_edit.go stays single-language).
//
// Everything it forbids is something that has to survive the round trip verbatim: a code block
// the reader may copy, a path they may click, an identifier they may grep for.
const translatePersona = "あなたは技術文書の翻訳専用ツールです。" +
	"受け取った Markdown を指定された言語に翻訳し、訳文だけを出力します。" +
	"前置き・後書き・注釈・「翻訳しました」のような報告は一切付けない。" +
	"Markdown の構造（見出し・箇条書き・番号・表・引用・強調・改行位置）は原文のまま保つ。" +
	"コードフェンスの中身・インラインコード・URL・ファイルパス・コマンド名・識別子・ログ出力は翻訳せずそのまま残す。" +
	"全体をコードフェンスで囲まない。" +
	"すでに指定言語で書かれている部分はそのまま残す。" +
	"意味を足したり省いたりせず、技術用語は分野の慣用訳を使う。"

// translateLangName spells the target for the prompt. The instructions are Japanese (see the
// persona), so the language is named in Japanese too — mixing "translate into en" into a
// Japanese instruction is the kind of split signal docs/log/28 §6.6 warns about.
func translateLangName(lang string) string {
	if lang == "en" {
		return "英語"
	}
	return "日本語"
}

func translatePrompt(text, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "次のテキストを%sに翻訳してください。\n\n", translateLangName(lang))
	b.WriteString("--- 原文ここから ---\n")
	b.WriteString(text)
	b.WriteString("\n--- 原文ここまで ---\n\n")
	b.WriteString("訳文だけを出力してください。")
	return b.String()
}

// translateTargetLang resolves what "the language I read in" means, in the same order the
// Console uses: the answer language when the user fixed one (Settings > 回答言語), otherwise the
// display language. The Console sends its own resolution so the language the button OFFERS is
// the one that comes back; an absent or unknown value falls back to resolving it here rather
// than failing, because a missing preference must not break the feature.
func translateTargetLang(req string) string {
	switch req {
	case "ja", "en":
		return req
	}
	if v := uiprefs.ChatOutputLanguage(); v != "" {
		return v
	}
	return uiprefs.Locale()
}

// translateHash keys the cache. FNV-1a over the UTF-8 bytes, twice with different offsets, hex
// — not a cryptographic digest, because the Console computes the SAME key while it renders and
// the browser's only built-in digest (SubtleCrypto) is async and absent from jsdom, which would
// put every render test out of reach. A cache key has to be stable and cheap here, not
// unforgeable: the value it guards is a translation of text the caller already holds.
//
// console/src/features/mirror/translate.ts holds the twin. The two are pinned to the same
// vectors (session_translate_test.go / translate.test.ts) — including a Japanese and an emoji
// string, because hashing UTF-16 code units instead of UTF-8 bytes is the way the two drift
// apart without any ASCII test noticing.
func translateHash(s string) string {
	return fmt.Sprintf("%08x%08x", fnv1a32(s, 2166136261), fnv1a32(s, 2166136261^0x9e3779b9))
}

func fnv1a32(s string, seed uint32) uint32 {
	h := seed
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// sessionTranslation is one cached translation. Source text is NOT stored: the hash is what the
// lookup needs, and keeping a second copy of every answer on disk buys nothing.
//
// The reply sent to the CLIENT never names a model (still true — anything this handler could
// put ON THE WIRE would be the REQUEST, not the outcome, the exact mistake ADR 0029 §1 forbids
// for `kind`; the outcome goes straight into the usage ledger). Kind/Model here are a different
// thing: docs/log/103 decision 8 — since a feature can now be pinned to a specific
// agent/model, WHAT translated a piece of text is part of its identity, so a cached answer from
// a different resolved backend/model must not silently stand in when that pin changes.
//
// Kind/Model are the values chatx.OneShotHeadlessRun actually reported (never a prediction —
// docs/log/103 §103.8-2: the 1-minute availability cache can flip between an earlier guess and
// the moment the call actually runs). Empty (both fields) marks a pre-103 entry: no model
// dimension existed yet when it was written, and it is read as a hit regardless of the
// feature's current pin (docs/log/103 §103.9 migration 1) — otherwise every existing
// translation goes stale, and "same text again is free" (aiassist.note_mirror_translate) breaks
// for everyone on the very next release, not just the few who touch the new per-feature setting.
type sessionTranslation struct {
	Hash      string `json:"hash"`            // translateHash of the source text
	Lang      string `json:"lang"`            // target language ("ja" | "en")
	Kind      string `json:"kind,omitempty"`  // the backend that actually ran ("" = pre-103 entry)
	Model     string `json:"model,omitempty"` // the model that actually ran ("" = pre-103 entry)
	Text      string `json:"text"`            // the translation
	CreatedAt int64  `json:"created_at"`
}

func sessionTranslationsPath(name string) string {
	return filepath.Join(paths.AgentStateDir(), "session-translations", name+".json")
}

// translateStoreMu serialises the store's read-modify-write. Two panes (or one reader on a
// phone and one at the desk) can press the button at the same moment, and without this the
// second write drops the first entry — the failure is silent and looks like "the translation
// did not stick". The Agent is the only writer, so one in-process lock is enough.
var translateStoreMu sync.Mutex

func readSessionTranslations(name string) []*sessionTranslation {
	b, err := os.ReadFile(sessionTranslationsPath(name))
	if err != nil {
		return nil
	}
	var list []*sessionTranslation
	if json.Unmarshal(b, &list) != nil {
		// Broken JSON must not break an answer's display over an auxiliary feature: start empty.
		return nil
	}
	out := list[:0]
	for _, e := range list {
		if e != nil && e.Hash != "" && e.Lang != "" {
			out = append(out, e)
		}
	}
	return out
}

func writeSessionTranslations(name string, list []*sessionTranslation) error {
	path := sessionTranslationsPath(name)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// trimTranslations evicts oldest-first until both caps hold. Entries are appended in press
// order, so the tail is the newest; sorting by CreatedAt would reorder entries written within
// the same millisecond for no gain.
func trimTranslations(list []*sessionTranslation) []*sessionTranslation {
	if len(list) > translateStoreMaxEntries {
		list = list[len(list)-translateStoreMaxEntries:]
	}
	total := 0
	for _, e := range list {
		total += len(e.Text)
	}
	for len(list) > 1 && total > translateStoreMaxBytes {
		total -= len(list[0].Text)
		list = list[1:]
	}
	return list
}

// errTranslateUnusable separates "the model did not answer" from "it answered with nothing a
// reader could read". Both are generation_failed on the wire; only the wording differs.
var errTranslateUnusable = errors.New("translation returned no usable text")

// translateFlight is one run in progress, and the result every later asker for the same text
// gets instead of starting a second one.
type translateFlight struct {
	done chan struct{}
	text string
	err  error
}

// The store only dedupes a text once its run has FINISHED, so two readers pressing within the
// same ~17 seconds (measured, §97.5) both miss the cache and both pay. With automatic
// translation that simultaneity stops being a coincidence: every pane open on the same session
// presses for the same turn in the same second it completes. Keyed by session+language+hash —
// the same three things that decide which stored entry answers the press.
var (
	translateFlightMu sync.Mutex
	translateFlights  = map[string]*translateFlight{}
)

// translateShared runs gen for this key, or waits for the run already in flight and returns its
// outcome — including its failure. A waiter retrying on its own would be exactly the second
// model run this exists to prevent, and a failed press is something the reader can repeat.
//
// The wait is bounded by the CALLER's context, not the leader's: the leader is bounded by
// translatePartTimeout, but its request can also be abandoned (a closed pane), and a waiter must
// not be held past its own deadline by someone else's.
func translateShared(ctx context.Context, key string, gen func() (string, error)) (string, error) {
	translateFlightMu.Lock()
	if f, ok := translateFlights[key]; ok {
		translateFlightMu.Unlock()
		select {
		case <-f.done:
			return f.text, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &translateFlight{done: make(chan struct{})}
	translateFlights[key] = f
	translateFlightMu.Unlock()
	f.text, f.err = gen()
	translateFlightMu.Lock()
	delete(translateFlights, key)
	translateFlightMu.Unlock()
	close(f.done)
	return f.text, f.err
}

// isLegacyTranslation reports a pre-103 entry (Kind and Model both empty — that dimension did
// not exist when it was written). It always hits: there is nothing to compare it against, and
// refusing it would make an upgrade discard every existing translation (§103.9 migration 1).
// Checked BEFORE resolve() anywhere this matters, so a press that only touches legacy entries
// never calls it — see lookupTranslation's doc.
func isLegacyTranslation(e *sessionTranslation) bool {
	return e.Kind == "" && e.Model == ""
}

// translationMatches decides whether a NON-legacy stored entry is still valid for the
// feature's CURRENT resolution (docs/log/103 decision 8, as corrected by 103-impl-review §1
// 重大4/中8):
//
//   - the entry must be from the same backend KIND. Kind never drifts under an unpinned
//     feature the way a model id can (chatx.OneShotHeadlessRun's per-backend ③ fallback only
//     ever refines the model within the resolved kind), so this alone is enough to catch "the
//     feature now resolves to a different agent".
//   - modelOK adds the model dimension whenever a SETTING names one — either ① the feature's
//     own pin, or ② the shared tier default (aiShortModels/aiProseModels[kind]). ③ (env
//     overrides, "recommended", the CLI's own default) is never tracked: it already varied run
//     to run before this feature existed, untracked, and matching it here would make an
//     unconfigured feature's cache miss on nearly every press. One asymmetry is accepted on
//     purpose: switching a pin from a concrete model back to "recommended" drops modelOK, so a
//     translation made under that concrete model is reused under "recommended" too, until a
//     press under a DIFFERENT concrete model evicts it.
func translationMatches(e *sessionTranslation, kind, model string, modelOK bool) bool {
	if e.Kind != kind {
		return false
	}
	return !modelOK || e.Model == model
}

// lookupTranslation returns the stored translation of that source hash into that language that
// still matches (isLegacyTranslation / translationMatches). resolve is called AT MOST ONCE, and
// only when actually needed — a legacy entry matches without it, so a press that only touches
// pre-103 entries (the common case right after an upgrade) never pays resolve's cost
// (103-impl-review 軽12: resolve can shell out — see translateCacheModel — so calling it
// unconditionally turned even an all-cache-hit press into up to 5 `auth status` calls whenever
// the availability cache was cold). Callers share one memoized resolve (sync.OnceValues) across
// a press so the write path reuses whatever the read path already resolved instead of
// resolving twice.
func lookupTranslation(name, hash, lang string, resolve func() (kind, model string, modelOK bool)) *sessionTranslation {
	for _, e := range readSessionTranslations(name) {
		if e.Hash != hash || e.Lang != lang {
			continue
		}
		if isLegacyTranslation(e) {
			return e
		}
		kind, model, modelOK := resolve()
		if translationMatches(e, kind, model, modelOK) {
			return e
		}
	}
	return nil
}

// putTranslation stores one entry, replacing any earlier translation of the same source into
// the same language FROM THE SAME BACKEND/MODEL (a retry after an unusable result must not
// leave the old one to win). A different Kind/Model is a different entry, not a replacement —
// switching a feature's pin back and forth must not throw away the translation that pin
// produced last time (docs/log/103 decision 8, the same "kind-scoped, never erase the other
// CLI's value" shape as aiFeatureModels itself).
func putTranslation(name string, e *sessionTranslation) {
	translateStoreMu.Lock()
	defer translateStoreMu.Unlock()
	list := readSessionTranslations(name)
	out := list[:0]
	for _, x := range list {
		if x.Hash == e.Hash && x.Lang == e.Lang && x.Kind == e.Kind && x.Model == e.Model {
			continue
		}
		out = append(out, x)
	}
	out = append(out, e)
	_ = writeSessionTranslations(name, trimTranslations(out))
}

// removeSessionTranslations cleans up once the slot name can be reused (see the file header).
func removeSessionTranslations(name string) {
	_ = os.Remove(sessionTranslationsPath(name))
}

// translateOneShot is the generation seam tests replace. It returns the backend/model that
// ACTUALLY ran (chatx.OneShotHeadlessRun, not a prediction) — putTranslation keys on that.
var translateOneShot = func(ctx context.Context, text, lang string) (reply, kind, model string, err error) {
	return chatx.OneShotHeadlessRun(ctx, usagex.FeatureTranslate, chatx.OneShotProse, translatePersona, translatePrompt(text, lang), translateModel())
}

// translateCacheModel resolves what the cache key's model dimension needs to know about the
// CURRENT resolution: the backend kind, and the SETTING-derived model when one exists — ① the
// feature's own pin, or ② the shared tier default (chatx.ResolveOneShot's configured already
// folds both in, docs/log/103-impl-review §3(b): resolveOneShot's `ok` comes from ② whenever ①
// is unset, not just from ①). "recommended" is excluded — it tracks the live catalog, so
// putting it in the key would evict on every press.
//
// This CAN shell out (chatx.ResolveOneShot -> oneShotKind -> headlessAgentAvailable's exec
// fallback on a cold 1-minute cache), which is exactly why callers must defer it
// (lookupTranslation's doc, 軽12) rather than call it unconditionally up front.
func translateCacheModel() (kind, model string, modelOK bool) {
	kind, m, configured, _ := chatx.ResolveOneShot(usagex.FeatureTranslate, chatx.OneShotProse)
	if configured && m != "" && m != chatx.AssistantRecommendedModel {
		return kind, m, true
	}
	return kind, "", false
}

// onceCacheModel memoizes translateCacheModel for one press — sync.OnceValues only covers two
// return values, and this one has three. Every caller within a request shares the SAME
// returned closure so the value resolves at most once (lookupTranslation's doc / 軽12).
func onceCacheModel() func() (kind, model string, modelOK bool) {
	var once sync.Once
	var kind, model string
	var modelOK bool
	return func() (string, string, bool) {
		once.Do(func() { kind, model, modelOK = translateCacheModel() })
		return kind, model, modelOK
	}
}

// translateCacheModelPeek is translateCacheModel's cache-only twin for the prefetch (GET
// /sessions/{name}/translations — 103-final-review 軽6). A pane can open, and so call this,
// before anything has ever warmed headlessAgentAvailable's cache; translateCacheModel's own
// kind resolution can shell out on exactly that cold-cache case (`auth status` per candidate
// kind, up to 5 measured), turning "open a pane" into the same cost as pressing a button. This
// answers from the availability cache alone (chatx.ResolveOneShotCached), never running a CLI.
//
// known is false when the cache cannot yet say what would run. The caller does not treat that
// as "no match" — it treats it the same as a legacy entry (isLegacyTranslation), i.e. gives up
// matching and shows whatever is stored, which is the same coarse behaviour every entry had
// before per-feature pinning existed. That is a deliberate, temporary degrade: once something
// warms the cache (a real generation, or the POST /translate path's own translateCacheModel),
// later prefetches match exactly again.
func translateCacheModelPeek() (kind, model string, modelOK, known bool) {
	kind, _, ok := chatx.ResolveOneShotCached(usagex.FeatureTranslate)
	if !ok {
		return "", "", false, false
	}
	m, configured := chatx.ResolveOneShotModelCached(usagex.FeatureTranslate, chatx.OneShotProse, kind)
	if configured && m != "" && m != chatx.AssistantRecommendedModel {
		return kind, m, true, true
	}
	return kind, "", false, true
}

// oncePeekCacheModel is onceCacheModel's twin for translateCacheModelPeek — see onceCacheModel's
// doc for why one shared closure per request matters even though this twin never shells out (a
// GET can still list several texts, and every one of them should see the same resolution).
func oncePeekCacheModel() func() (kind, model string, modelOK, known bool) {
	var once sync.Once
	var kind, model string
	var modelOK, known bool
	return func() (string, string, bool, bool) {
		once.Do(func() { kind, model, modelOK, known = translateCacheModelPeek() })
		return kind, model, modelOK, known
	}
}

// cleanTranslation undoes the one thing the model does despite the persona: wrapping the whole
// answer in a code fence. It is only unwrapped when the SOURCE was not itself one fenced block
// — otherwise the fence is content, and stripping it would turn the reader's code block into
// prose. Nothing else is rewritten: this text is what the reader will read as the answer, and a
// server-side "tidy-up" of somebody's answer is a silent edit.
func cleanTranslation(reply, source string) (string, error) {
	out := strings.TrimSpace(reply)
	if out == "" {
		return "", errors.New("empty translation")
	}
	if strings.HasPrefix(out, "```") && strings.HasSuffix(out, "```") && !strings.HasPrefix(strings.TrimSpace(source), "```") {
		body := out[3:]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			body = body[nl+1:]
			// Only when the closing fence is the LAST one: a body with fences of its own
			// (a translated answer that contains code) is left exactly as it came.
			if end := strings.LastIndex(body, "```"); end >= 0 && !strings.Contains(body[:end], "```") {
				out = strings.TrimSpace(body[:end])
			}
		}
	}
	if out == "" {
		return "", errors.New("empty translation")
	}
	return out, nil
}

type translateRequest struct {
	To string `json:"to"`
	// Trigger tells the ledger who pressed: absent/"manual" = the reader's own press, "auto" =
	// the press the Console made for them when a turn finished (docs/log/97 §97.12). The ledger
	// records what actually happened, so an unattended run must not be filed as a manual one —
	// the same rule ADR 0029 §1 states for `kind`.
	//
	// The Console omits this field for a manual press ON PURPOSE: DecodeStrictJSON rejects
	// unknown fields, so a Console newer than the Agent it is talking to (a deployment mid-roll)
	// would turn every press into a 400. Sending it only for the automatic press keeps the
	// reader's own button working against an older Agent; the automatic one fails silently
	// there, which is what an automatic press does on any failure.
	Trigger string `json:"trigger"`
	Parts   []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

// translateTrigger maps the request's claim onto the ledger's vocabulary. Anything unknown is
// read as the reader's own press: a mis-spelled value must not invent a third kind of run.
func translateTrigger(v string) string {
	if v == usagex.TriggerAuto {
		return usagex.TriggerAuto
	}
	return usagex.TriggerManual
}

type translatePart struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
	// Cached distinguishes "already had it" from "just ran a model", which is the only place the
	// Console can honestly tell the reader whether a press cost anything. It is also the only
	// thing this reply says about the run — see the note on sessionTranslation for why it does
	// not name a model.
	Cached bool `json:"cached"`
}

// translateReply answers one press: the language it resolved to (which the Console compares
// against the language it offered) and one entry per requested part, in request order.
type translateReply struct {
	Lang  string          `json:"lang"`
	Parts []translatePart `json:"parts"`
}

// translationsReply is the whole per-language store, source hash -> translation. A map rather
// than a list because the Console's only question is "do I already have this text translated",
// which it asks once per rendered part.
type translationsReply struct {
	Lang    string            `json:"lang"`
	Entries map[string]string `json:"entries"`
}

// handleSessionTranslate — POST /sessions/{name}/translate. One press = one turn's prose parts.
// Parts already in the store come back without a model run, which is what makes re-pressing,
// re-opening the pane and a second reader free.
func handleSessionTranslate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if !uiprefs.MirrorTranslate() {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTranslateDisabled, "mirror translation is turned off")
		return
	}
	if _, found := session.ReadMeta(name); !found {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	var req translateRequest
	if serr := httpx.DecodeStrictJSON(r, &req, translateMaxBody); serr != nil {
		httpx.WriteErr(w, serr.Status, serr.Code, serr.Message)
		return
	}
	if len(req.Parts) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTranslateEmpty, "nothing to translate")
		return
	}
	if len(req.Parts) > translateMaxParts {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTranslateTooLong, "too many parts in one turn")
		return
	}
	texts := make([]string, 0, len(req.Parts))
	for _, p := range req.Parts {
		if strings.TrimSpace(p.Text) == "" {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeTranslateEmpty, "nothing to translate")
			return
		}
		if len(p.Text) > translateMaxPartBytes {
			httpx.WriteErr(w, http.StatusBadRequest, errCodeTranslateTooLong, "this answer is too long to translate")
			return
		}
		texts = append(texts, p.Text)
	}
	lang := translateTargetLang(req.To)
	trigger := translateTrigger(req.Trigger)
	// Shared by every part of this press, and resolved AT MOST ONCE (sync.OnceValues): the read
	// and write paths must use the exact same values (103-impl-review 重大4 — a run's ACTUAL
	// kind/model used to be written while a separate prediction was read, and the two can
	// disagree, e.g. agy's agyChatModel silently drops an id that fell out of the live catalog
	// to "", which made a real account get 3 fresh generations for 3 presses of the same text
	// with never a cache hit again). Deferred rather than called up front so a press that only
	// touches legacy entries stays free of translateCacheModel's possible CLI probe (軽12).
	resolveCacheModel := onceCacheModel()
	out := make([]translatePart, 0, len(texts))
	for _, text := range texts {
		hash := translateHash(text)
		if hit := lookupTranslation(name, hash, lang, resolveCacheModel); hit != nil {
			out = append(out, translatePart{Hash: hash, Text: hit.Text, Cached: true})
			continue
		}
		ran := false
		clean, err := translateShared(r.Context(), name+"\x00"+lang+"\x00"+hash, func() (string, error) {
			// Asked again inside the flight: a run that finished between the lookup above and
			// this moment has already paid for this text.
			if hit := lookupTranslation(name, hash, lang, resolveCacheModel); hit != nil {
				return hit.Text, nil
			}
			ran = true
			ctx, cancel := context.WithTimeout(r.Context(), translatePartTimeout)
			// The tag is the LEADER's: one run, one row, filed under the press that started it.
			ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureTranslate, Trigger: trigger, Ref: name})
			// The run's own kind/model are deliberately dropped: the cache key is what the next
			// lookup can predict, not what this run happened to reach (see putTranslation below).
			reply, _, _, gerr := translateOneShot(ctx, text, lang)
			cancel()
			if gerr != nil {
				return "", gerr
			}
			got, gerr := cleanTranslation(reply, text)
			if gerr != nil {
				// Wrapped rather than returned as-is: the reader is told which of the two
				// failures happened, and a waiter on this flight gets the same wording.
				return "", fmt.Errorf("%w: %v", errTranslateUnusable, gerr)
			}
			// BOTH fields come from resolveCacheModel — the SAME values the next lookup will
			// compare against — never from what the run actually reported (OneShotHeadlessRun's
			// return is discarded here on purpose; see resolveCacheModel's declaration).
			//
			// Taking kind from the run instead looks free ("③ only refines the model within a
			// kind") and is not: the resolution and the run are separate resolveOneShot calls,
			// and on a cold store the resolution first runs AFTER generation, so the window is
			// the whole generation time against a 1-minute availability cache. A flip inside it
			// writes a row whose kind and model came from different resolutions — a combination
			// no lookup can ever match, so that press is cached and never read again. That is
			// exactly the permanent miss this key was corrected to avoid (docs/log/103 決定 8).
			cacheKind, cacheModel, cacheModelOK := resolveCacheModel()
			model := ""
			if cacheModelOK {
				model = cacheModel
			}
			putTranslation(name, &sessionTranslation{
				Hash: hash, Lang: lang, Kind: cacheKind, Model: model,
				Text: got, CreatedAt: time.Now().UnixMilli(),
			})
			return got, nil
		})
		if err != nil {
			msg := "translation failed"
			if errors.Is(err, errTranslateUnusable) {
				msg = "translation returned no usable text"
			}
			httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", msg)
			return
		}
		out = append(out, translatePart{Hash: hash, Text: clean, Cached: !ran})
	}
	httpx.WriteJSON(w, http.StatusOK, translateReply{Lang: lang, Parts: out})
}

// handleSessionTranslations — GET /sessions/{name}/translations?lang=ja. What is already
// translated, so re-opening a pane (or opening the session on a phone) shows the translations
// the reader already paid for instead of offering to buy them again.
//
// Deliberately NOT part of the /messages poll payload: the mirror polls every second, and a
// map of whole answers on every tick is the one shape that would make this feature cost
// battery on a phone that never presses the button.
//
// This prefetch must apply the SAME match as the press (translationMatches) — before
// 103-impl-review 重大1 it returned the plain `hash -> text` map regardless of Kind/Model, so a
// feature pinned to a different agent/model since the last press showed one translation here
// and a DIFFERENT one on the next press of the actual button (measured: opening the pane showed
// the new pin's answer, pressing showed the cache hit under the old pin — same text, two
// different translations, no way for a reader to tell which was "right").
func handleSessionTranslations(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	lang := translateTargetLang(r.URL.Query().Get("lang"))
	// The peek variant, not onceCacheModel: this is a GET, and translateCacheModel's kind step
	// can shell out on a cold availability cache (軽6) — opening a pane must not cost what
	// pressing a button costs.
	resolveCacheModel := oncePeekCacheModel()
	entries := map[string]string{}
	for _, e := range readSessionTranslations(name) {
		if e.Lang != lang {
			continue
		}
		if _, done := entries[e.Hash]; done {
			continue // a higher-priority (earlier) entry for this hash already matched
		}
		if isLegacyTranslation(e) {
			entries[e.Hash] = e.Text
			continue
		}
		kind, model, modelOK, known := resolveCacheModel()
		// An unknown resolution (cache cold) can't be matched against, so it isn't treated as a
		// mismatch either — see translateCacheModelPeek's doc for why showing the stored entry
		// anyway is the right degrade here.
		if !known || translationMatches(e, kind, model, modelOK) {
			entries[e.Hash] = e.Text
		}
	}
	httpx.WriteJSON(w, http.StatusOK, translationsReply{Lang: lang, Entries: entries})
}
