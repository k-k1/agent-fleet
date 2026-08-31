package main

// エージェントメモリの版管理（docs/log/39 ★4 / 先行 OSS 調査の取り込み点 1）— export 時の
// secret スキャン。
//
// 決着 #2 で「v1 は平文 DL（暗号化なし）」と決めた代わりに、**このスキャンを v1 の必須
// 要件へ格上げ**した。持ち出したファイルの保管中の保護（age 等の暗号化）は別レイヤの
// 話だが、「メモリに書き溜まった資格情報をそうと気づかず外へ持ち出す」は今この経路で
// 起きうるので、ここで止める。
//
// 方針:
//   - 検出は gitleaks 級の**高シグネチャな正規表現**に限る。メモリは自然文の md なので、
//     エントロピー判定や緩い汎用パターンは偽陽性の海になり、警告が無視されるようになる
//     （警告疲れは防御の失敗）。
//   - 検出しても API は「握り潰さず、既定でブロックし、明示の ack で通す」。判断できるのは
//     本人だけで、機械には「これは実鍵か」の最終判定ができないため。
//   - **見つけた秘密そのものは決して返さない・ログにも書かない**。返すのは規則名・パス・
//     行番号・先頭数文字だけのマスク済みヒント。ここで生値を返すと、防御のつもりの機構が
//     秘密を新しい場所（監査ログ・ブラウザ履歴）へ配る経路に化ける。
//   - bundle は**全履歴**を運ぶので、走査も HEAD ツリーではなく到達可能な全 blob を見る。
//     「一度書いて消した鍵」は HEAD には無いが bundle には入っている。

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// memorySecretRule は 1 つの検出規則。Name は Console にそのまま出す識別子（i18n せず、
// gitleaks 等と同じ語彙にしておくと「何が引っかかったか」を利用者が外部知識で照合できる）。
type memorySecretRule struct {
	Name string
	Re   *regexp.Regexp
}

// memorySecretRules は高シグネチャな規則だけを並べたもの。追加するときは「メモリの
// 自然文に偶然現れないか」を基準に判断する。
var memorySecretRules = []memorySecretRule{
	{"aws-access-key-id", regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws[^\n]{0,24}?secret[^\n]{0,24}?[:=]\s*["'` + "`" + `]?([A-Za-z0-9/+=]{40})`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"github-fine-grained-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}\b`)},
	{"slack-webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9_/\-]{20,}`)},
	{"anthropic-api-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{24,}`)},
	{"openai-api-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"stripe-secret-key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{20,}\b`)},
	{"npm-token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"pypi-token", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_\-]{40,}`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"json-web-token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{"url-basic-auth", regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s/:@]+:[^\s/@"']{8,}@[^\s/"']+`)},
	// 汎用の代入形。引用符と 16 文字以上の非空白値を要求して、散文の「token: 使用量」等を外す。
	{"generic-secret-assignment", regexp.MustCompile(`(?i)\b(?:api[_\-]?key|secret[_\-]?key|access[_\-]?token|auth[_\-]?token|client[_\-]?secret|password|passwd)\b\s*[:=]\s*["'` + "`" + `]([^"'` + "`" + `\s]{16,})["'` + "`" + `]`)},
}

// memorySecretPlaceholders は「明らかに例示・伏字」の値を落とすための語。実鍵にこれらが
// 含まれることはまず無く、逆に手順書や設計メモには頻出する。
var memorySecretPlaceholders = []string{
	"example", "your-", "your_", "yourkey", "xxxx", "aaaa", "0000", "1234567890abcdef",
	"changeme", "redacted", "placeholder", "dummy", "sample", "<", ">", "...", "***", "…",
}

// memorySecretFinding は 1 件の検出。**生の秘密は入らない**（Hint はマスク済み）。
type memorySecretFinding struct {
	Path    string `json:"path"`              // repo 内パス
	Line    int    `json:"line"`              // 1 始まり
	Rule    string `json:"rule"`              // memorySecretRules の Name
	Hint    string `json:"hint"`              // 先頭数文字だけのマスク済み抜粋
	History bool   `json:"history,omitempty"` // 現在のツリーには無く、履歴にだけ居る
}

const (
	memorySecretMaxFindings   = 200       // これ以上は数えるだけ（UI も読めない）
	memorySecretMaxPerFile    = 20        //
	memorySecretMaxBlobBytes  = 4 << 20   // 1 blob あたりの走査上限
	memorySecretMaxScanBytes  = 128 << 20 // 全体の走査上限（暴走防止）
	memorySecretHintPrefixLen = 4
)

// memoryMaskSecret はマッチ文字列をマスクする。先頭数文字だけ残す — 「どの鍵か」を
// 本人が特定するには十分で、値としては役に立たない長さ。
func memoryMaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= memorySecretHintPrefixLen {
		return "…"
	}
	return s[:memorySecretHintPrefixLen] + "…(" + strconv.Itoa(len(s)) + ")"
}

// memoryLooksPlaceholder は例示・伏字っぽい値か。
func memoryLooksPlaceholder(s string) bool {
	low := strings.ToLower(s)
	for _, p := range memorySecretPlaceholders {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// memoryScanContent は 1 ファイル分の中身を走査する。バイナリ（NUL を含む）は対象外
// （メモリは md であり、バイナリは走査してもノイズにしかならない）。
func memoryScanContent(path string, b []byte) []memorySecretFinding {
	if bytes.IndexByte(b, 0) >= 0 {
		return nil
	}
	var out []memorySecretFinding
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		if len(line) > 8192 {
			line = line[:8192] // 1 行が異常に長いものは頭だけ見る（minified 等）
		}
		for _, rule := range memorySecretRules {
			m := rule.Re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			// 捕捉グループがあればその値を、無ければマッチ全体を秘密候補とする。
			val := m[0]
			if len(m) > 1 && m[1] != "" {
				val = m[1]
			}
			if memoryLooksPlaceholder(val) {
				continue
			}
			key := rule.Name + "\x00" + strconv.Itoa(i)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, memorySecretFinding{Path: path, Line: i + 1, Rule: rule.Name, Hint: memoryMaskSecret(val)})
			if len(out) >= memorySecretMaxPerFile {
				return out
			}
		}
	}
	return out
}

// memoryObjRef は走査対象の 1 オブジェクト（blob 候補）。
type memoryObjRef struct {
	SHA  string
	Path string
}

// memoryScanRevTree は 1 時点のツリーだけを走査する（tar export 用 — tar は最新しか運ばない）。
func memoryScanRevTree(rev string) ([]memorySecretFinding, error) {
	out, err := memoryGitRun("ls-tree", "-r", rev)
	if err != nil {
		return nil, err
	}
	var refs []memoryObjRef
	for _, line := range strings.Split(out, "\n") {
		meta, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 3 || f[1] != "blob" {
			continue
		}
		refs = append(refs, memoryObjRef{SHA: f[2], Path: p})
	}
	return memoryScanObjects(refs, nil)
}

// memoryScanAllReachable は到達可能な全 blob を走査する（bundle export 用）。bundle は
// 全履歴を運ぶので、「一度書いて消した鍵」も持ち出される — HEAD だけ見ては不足。
func memoryScanAllReachable() ([]memorySecretFinding, error) {
	out, err := memoryGitRun("rev-list", "--objects", "--all")
	if err != nil {
		return nil, err
	}
	var refs []memoryObjRef
	for _, line := range strings.Split(out, "\n") {
		sha, p, _ := strings.Cut(strings.TrimSpace(line), " ")
		if len(sha) < 40 || p == "" {
			continue // commit（パス無し）・空行
		}
		refs = append(refs, memoryObjRef{SHA: sha, Path: p})
	}
	// 現在のツリーに**その内容のまま**居るものは「履歴だけ」ではない。判定はパスではなく
	// blob の sha で行う — 同じパスが今も存在していても、鍵を消した後の版なら別 blob で、
	// 「消したはずの鍵が bundle には残っている」を見落としてしまう。
	head := map[string]bool{}
	if memoryHasCommits() {
		if lt, lerr := memoryGitRun("ls-tree", "-r", memoryBranch); lerr == nil {
			for _, line := range strings.Split(lt, "\n") {
				meta, _, ok := strings.Cut(line, "\t")
				if !ok {
					continue
				}
				if f := strings.Fields(meta); len(f) >= 3 && f[1] == "blob" {
					head[f[2]] = true
				}
			}
		}
	}
	return memoryScanObjects(refs, head)
}

// memoryScanObjects は blob を `git cat-file --batch` で一括して読み、走査する。
// オブジェクトごとに git を起動すると数百 fork になるため、1 プロセスで流す。
// headBlobs が非 nil なら、そこに無い blob 由来の検出に History 印を付ける。
func memoryScanObjects(refs []memoryObjRef, headBlobs map[string]bool) ([]memorySecretFinding, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	cmd := memoryGit("cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		w := bufio.NewWriter(stdin)
		for _, r := range refs {
			_, _ = w.WriteString(r.SHA + "\n")
		}
		_ = w.Flush()
		_ = stdin.Close()
	}()

	var findings []memorySecretFinding
	br := bufio.NewReaderSize(stdout, 64<<10)
	var scanned int64
	for i := 0; i < len(refs); i++ {
		header, herr := br.ReadString('\n')
		if herr != nil {
			break
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) < 3 {
			continue // "<sha> missing" — 走査対象から外れるだけ
		}
		size, perr := strconv.ParseInt(fields[2], 10, 64)
		if perr != nil {
			break // 同期が取れないので打ち切る（部分結果は返す）
		}
		body := make([]byte, 0)
		if fields[1] == "blob" && size <= memorySecretMaxBlobBytes && scanned < memorySecretMaxScanBytes {
			body = make([]byte, size)
			if _, rerr := io.ReadFull(br, body); rerr != nil {
				break
			}
			scanned += size
		} else if _, derr := io.CopyN(io.Discard, br, size); derr != nil {
			break
		}
		if _, derr := br.Discard(1); derr != nil { // オブジェクト後の改行
			break
		}
		if len(body) == 0 || len(findings) >= memorySecretMaxFindings {
			continue
		}
		for _, f := range memoryScanContent(refs[i].Path, body) {
			if headBlobs != nil && !headBlobs[refs[i].SHA] {
				f.History = true
			}
			findings = append(findings, f)
			if len(findings) >= memorySecretMaxFindings {
				break
			}
		}
	}
	_ = stdout.Close()
	_ = cmd.Wait()
	return memoryDedupeFindings(findings), nil
}

// memoryDedupeFindings は同一 (path, line, rule) を 1 件に畳む。履歴を走査すると同じ
// 行が版ごとに何度も出るため、これが無いと一覧が読めない。
func memoryDedupeFindings(in []memorySecretFinding) []memorySecretFinding {
	out := make([]memorySecretFinding, 0, len(in))
	seen := map[string]int{}
	for _, f := range in {
		key := fmt.Sprintf("%s\x00%d\x00%s", f.Path, f.Line, f.Rule)
		if i, ok := seen[key]; ok {
			// 現ツリーにも居るなら「履歴だけ」ではない、を優先する。
			if !f.History {
				out[i].History = false
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}
