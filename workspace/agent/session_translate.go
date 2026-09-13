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
// No "translated by" field, and that is deliberate. Which backend and model actually ran is
// decided inside OneShotHeadless (the first AVAILABLE agent in Settings > AI assistance order,
// with that backend's prose model), and it is not returned — it is written straight into the
// usage ledger. Storing what we REQUESTED would repeat, one level up in the display, the exact
// mistake ADR 0029 §1 forbids for `kind`: measured live on this host, the reply claimed sonnet
// while the ledger recorded the run as agy, because agy is first in this workspace's order.
type sessionTranslation struct {
	Hash      string `json:"hash"` // translateHash of the source text
	Lang      string `json:"lang"` // target language ("ja" | "en")
	Text      string `json:"text"` // the translation
	CreatedAt int64  `json:"created_at"`
}

func sessionTranslationsPath(name string) string {
	return filepath.Join(paths.AgentConfigDir(), "session-translations", name+".json")
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

// lookupTranslation returns the stored translation of that source hash into that language.
func lookupTranslation(name, hash, lang string) *sessionTranslation {
	for _, e := range readSessionTranslations(name) {
		if e.Hash == hash && e.Lang == lang {
			return e
		}
	}
	return nil
}

// putTranslation stores one entry, replacing any earlier translation of the same source into
// the same language (a retry after an unusable result must not leave the old one to win).
func putTranslation(name string, e *sessionTranslation) {
	translateStoreMu.Lock()
	defer translateStoreMu.Unlock()
	list := readSessionTranslations(name)
	out := list[:0]
	for _, x := range list {
		if x.Hash == e.Hash && x.Lang == e.Lang {
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

// translateOneShot is the generation seam tests replace.
var translateOneShot = func(ctx context.Context, text, lang string) (string, error) {
	return chatx.OneShotHeadless(ctx, chatx.OneShotProse, translatePersona, translatePrompt(text, lang), translateModel())
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
	To    string `json:"to"`
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
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
	out := make([]translatePart, 0, len(texts))
	for _, text := range texts {
		hash := translateHash(text)
		if hit := lookupTranslation(name, hash, lang); hit != nil {
			out = append(out, translatePart{Hash: hash, Text: hit.Text, Cached: true})
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), translatePartTimeout)
		ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureTranslate, Trigger: usagex.TriggerManual, Ref: name})
		reply, err := translateOneShot(ctx, text, lang)
		cancel()
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "translation failed")
			return
		}
		clean, err := cleanTranslation(reply, text)
		if err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "translation returned no usable text")
			return
		}
		e := &sessionTranslation{Hash: hash, Lang: lang, Text: clean, CreatedAt: time.Now().UnixMilli()}
		putTranslation(name, e)
		out = append(out, translatePart{Hash: hash, Text: clean})
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
func handleSessionTranslations(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	lang := translateTargetLang(r.URL.Query().Get("lang"))
	entries := map[string]string{}
	for _, e := range readSessionTranslations(name) {
		if e.Lang == lang {
			entries[e.Hash] = e.Text
		}
	}
	httpx.WriteJSON(w, http.StatusOK, translationsReply{Lang: lang, Entries: entries})
}
