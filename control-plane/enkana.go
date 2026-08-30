// enkana.go — 英単語を「カタカナ英語」に変換する前処理（docs/log/24）。VOICEVOX(OpenJTalk) は
// 英語を綴りのままだと読めないので、CMU 発音辞書で英単語→発音記号(ARPABET)を引き、それを
// 日本語モーラ(カタカナ)に写像してから合成に渡す。結果は "それっぽい" カタカナ英語（日本語
// アクセント）で、ネイティブ発音ではない。日英混在はそのまま扱える（英単語トークンのみ変換）。
//
// 辞書: CMU Pronouncing Dictionary（assets/cmudict.dict.gz, BSD-2, (c) 1993-2015 CMU。
// ライセンス全文 assets/cmudict.LICENSE）。GPL の alkana/bep-eng.dic は Apache-2.0 の本リポジトリ
// と非互換のため不採用（docs/log/24 参照）。辞書は初回利用時に遅延ロードする。
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"log"
	"strings"
	"sync"
	"unicode"
)

//go:embed assets/cmudict.dict.gz
var cmudictGz []byte

var (
	cmuOnce sync.Once
	cmuMap  map[string]string // 小文字語 → ARPABET（最初の異形のみ）
)

func loadCmudict() {
	cmuMap = make(map[string]string, 140000)
	zr, err := gzip.NewReader(bytes.NewReader(cmudictGz))
	if err != nil {
		log.Printf("enkana: cmudict gzip open failed: %v", err)
		return
	}
	defer zr.Close()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, ";;;") {
			continue
		}
		// "word  P1 P2 ... [# comment]"。異形は "word(2)"。
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		w := fields[0]
		if j := strings.IndexByte(w, '('); j >= 0 {
			w = w[:j] // 異形サフィックスを落とす
		}
		w = strings.ToLower(w)
		if _, ok := cmuMap[w]; ok {
			continue // 最初の異形を採用
		}
		cmuMap[w] = strings.Join(fields[1:], " ")
	}
	if err := sc.Err(); err != nil {
		// 部分ロードでも変換自体は動く(未収載語は素通し)ので続行するが、黙らせない
		log.Printf("enkana: cmudict scan failed (dictionary partially loaded, %d words): %v", len(cmuMap), err)
	}
}

// englishToKana は文中の英数字トークン（英字で始まる [A-Za-z0-9] の連なり）をカタカナに
// 変換し、それ以外（日本語・記号・単独の数字・空白）はそのまま通す。英字始まりに限るので
// 単独の数字（"3個" の 3）は日本語読みのまま残る。EC2 / iPhone15 のような英字＋数字は
// 1 トークンとして拾う。語中のアポストロフィ（it's / don't）は語に含める（下の isApostrophe）。
func englishToKana(text string) string {
	cmuOnce.Do(loadCmudict)
	var b strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if isAsciiLetter(runes[i]) {
			j := i
			for j < len(runes) {
				if isAsciiLetter(runes[j]) || isAsciiDigit(runes[j]) {
					j++
					continue
				}
				// 語中のアポストロフィ（it's, don't, we'll, User's）は語に含める。次が英字の
				// ときだけ＝末尾の所有格 devs' や引用符の閉じ 'foo' は含めない。
				if isApostrophe(runes[j]) && j+1 < len(runes) && isAsciiLetter(runes[j+1]) {
					j++
					continue
				}
				break
			}
			b.WriteString(convertToken(string(runes[i:j])))
			i = j
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

func isAsciiLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// isApostrophe は ASCII アポストロフィ(') とタイプグラフィ版(’ U+2019) を同一視する。
func isApostrophe(r rune) bool { return r == '\'' || r == '’' }

func isAsciiDigit(r rune) bool { return r >= '0' && r <= '9' }

func hasDigit(s string) bool {
	for _, r := range s {
		if isAsciiDigit(r) {
			return true
		}
	}
	return false
}

// 数字を英語読みで（EC2→…ツー, S3→…スリー）。技術語の版番号・型番向け。単独数字は
// englishToKana で拾わないので、ここに来るのは英字と地続きの数字のみ。
var digitKana = map[rune]string{
	'0': "ゼロ", '1': "ワン", '2': "ツー", '3': "スリー", '4': "フォー",
	'5': "ファイブ", '6': "シックス", '7': "セブン", '8': "エイト", '9': "ナイン",
}

// convertToken は 1 英数字トークンを変換する。まずトークン全体をオーバーライド辞書で引き、
// 数字を含むなら英字塊/数字塊に割って変換、英字のみなら convertWord。語中にアポストロフィを
// 含むトークン（短縮形・所有格）は convertContraction へ。
func convertToken(tok string) string {
	if strings.ContainsRune(tok, '\'') || strings.ContainsRune(tok, '’') {
		return convertContraction(tok)
	}
	if k, ok := techKana[strings.ToLower(tok)]; ok {
		return k
	}
	if hasDigit(tok) {
		return convertAlnum(tok)
	}
	return convertWord(tok)
}

// convertContraction は語中にアポストロフィを含むトークン（it's / don't / User's）を変換する。
// CMUdict はこれらを分断読みしてしまう（"it's"→"イット'エス"）ため、頻出の短縮形は
// contractionKana で読みを固定し、所有格 's は「元の語＋ズ」、その他は ' を除いて通常変換へ。
func convertContraction(tok string) string {
	norm := strings.ReplaceAll(tok, "’", "'")
	lower := strings.ToLower(norm)
	if k, ok := contractionKana[lower]; ok {
		return k
	}
	if strings.HasSuffix(lower, "'s") { // 所有格: React's → リアクトズ, user's → ユーザーズ
		return convertToken(norm[:len(norm)-2]) + "ズ"
	}
	return convertToken(strings.ReplaceAll(norm, "'", "")) // 未知の短縮形は ' を落として読む
}

// convertAlnum は英字塊・数字塊が混じるトークン（EC2, iPhone15, utf8）を変換する。数字塊は
// 一桁ずつ英語読み、英字塊は convertWord（未変換なら英字名読みにフォールバック＝s3→エススリー）。
func convertAlnum(tok string) string {
	var b strings.Builder
	rs := []rune(tok)
	for i := 0; i < len(rs); {
		if isAsciiDigit(rs[i]) {
			for i < len(rs) && isAsciiDigit(rs[i]) {
				b.WriteString(digitKana[rs[i]])
				i++
			}
			continue
		}
		j := i
		for j < len(rs) && isAsciiLetter(rs[j]) {
			j++
		}
		seg := string(rs[i:j])
		conv := convertWord(seg)
		if conv == seg { // 辞書外の英字塊は英字名読み（S3→エス, utf→ユーティーエフ）
			conv = spellLetters(strings.ToUpper(seg))
		}
		b.WriteString(conv)
		i = j
	}
	return b.String()
}

// convertWord は 1 英単語をカタカナへ。辞書ヒット→ARPABET 変換、全大文字の略語→英字名読み、
// それ以外の未知語→camelCase 等で分割して各部を再帰、なお未知なら綴りのまま残す。
func convertWord(word string) string {
	lower := strings.ToLower(word)
	if k, ok := techKana[lower]; ok { // AWS/開発ジャルゴンのオーバーライド優先
		return k
	}
	// 単独の大文字 1 文字（案A・パターンB 等のラベル）は CMUdict の実在語（a/i/o が
	// 冠詞・代名詞として載っている）に化けて誤読される（例: "A"→"ア"）ため、CMUdict より
	// 先に英字名読みで確定させる。小文字（英文中の a 等）は対象外＝そのまま自然文として扱う。
	if len(word) == 1 && isAllUpper(word) {
		return spellLetters(word)
	}
	if arpa, ok := cmuMap[lower]; ok {
		return arpabetToKana(arpa)
	}
	// 全大文字（2〜5字）は略語として英字名読み（AWS→エーダブリューエス）。
	if len(word) >= 2 && len(word) <= 5 && isAllUpper(word) {
		return spellLetters(word)
	}
	// camelCase / 区切りで分割して各部を変換（getUserById → get/User/By/Id）。
	parts := splitIdentifier(word)
	if len(parts) > 1 {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(convertWord(p))
		}
		return b.String()
	}
	return word // 未知語は綴りのまま（VOICEVOX 側の挙動に委ねる）
}

func isAllUpper(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// splitIdentifier は camelCase / PascalCase を語に割る（連続大文字は略語塊として保持）。
func splitIdentifier(s string) []string {
	rs := []rune(s)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(cur)
		upperRunEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if lowerToUpper || upperRunEnd {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	parts = append(parts, string(rs[start:]))
	return parts
}

var letterNames = map[rune]string{
	'A': "エー", 'B': "ビー", 'C': "シー", 'D': "ディー", 'E': "イー", 'F': "エフ",
	'G': "ジー", 'H': "エイチ", 'I': "アイ", 'J': "ジェー", 'K': "ケー", 'L': "エル",
	'M': "エム", 'N': "エヌ", 'O': "オー", 'P': "ピー", 'Q': "キュー", 'R': "アール",
	'S': "エス", 'T': "ティー", 'U': "ユー", 'V': "ブイ", 'W': "ダブリュー", 'X': "エックス",
	'Y': "ワイ", 'Z': "ゼット",
}

func spellLetters(word string) string {
	var b strings.Builder
	for _, r := range word {
		if n, ok := letterNames[r]; ok {
			b.WriteString(n)
		}
	}
	return b.String()
}

// --- ARPABET → カタカナ・モーラ ------------------------------------------------

type vowelInfo struct {
	base  byte // a/i/u/e/o
	glide string
}

var vowels = map[string]vowelInfo{
	"AA": {'a', ""}, "AE": {'a', ""}, "AH": {'a', ""}, "AO": {'o', "ー"},
	"AW": {'a', "ウ"}, "AY": {'a', "イ"}, "EH": {'e', ""}, "ER": {'a', "ー"},
	"EY": {'e', "イ"}, "IH": {'i', ""}, "IY": {'i', "ー"}, "OW": {'o', "ー"},
	"OY": {'o', "イ"}, "UH": {'u', ""}, "UW": {'u', "ー"},
}

var baseVowelKana = map[byte]string{'a': "ア", 'i': "イ", 'u': "ウ", 'e': "エ", 'o': "オ"}
var vowelIdx = map[byte]int{'a': 0, 'i': 1, 'u': 2, 'e': 3, 'o': 4}

// 拗音の小書き母音（イ段 + これで キャ/キュ/キョ 等）。i は小書き無し。
var smallVowel = map[byte]string{'a': "ャ", 'i': "", 'u': "ュ", 'e': "ェ", 'o': "ョ"}

// 子音 → [a,i,u,e,o] のカタカナ（外来音の拡張仮名を含む）。
var consKana = map[string][5]string{
	"P": {"パ", "ピ", "プ", "ペ", "ポ"}, "B": {"バ", "ビ", "ブ", "ベ", "ボ"},
	"T": {"タ", "ティ", "トゥ", "テ", "ト"}, "D": {"ダ", "ディ", "ドゥ", "デ", "ド"},
	"K": {"カ", "キ", "ク", "ケ", "コ"}, "G": {"ガ", "ギ", "グ", "ゲ", "ゴ"},
	"M": {"マ", "ミ", "ム", "メ", "モ"}, "N": {"ナ", "ニ", "ヌ", "ネ", "ノ"},
	"F": {"ファ", "フィ", "フ", "フェ", "フォ"}, "V": {"ヴァ", "ヴィ", "ヴ", "ヴェ", "ヴォ"},
	"S": {"サ", "シ", "ス", "セ", "ソ"}, "Z": {"ザ", "ジ", "ズ", "ゼ", "ゾ"},
	"SH": {"シャ", "シ", "シュ", "シェ", "ショ"}, "ZH": {"ジャ", "ジ", "ジュ", "ジェ", "ジョ"},
	"CH": {"チャ", "チ", "チュ", "チェ", "チョ"}, "JH": {"ジャ", "ジ", "ジュ", "ジェ", "ジョ"},
	"TH": {"サ", "シ", "ス", "セ", "ソ"}, "DH": {"ザ", "ジ", "ズ", "ゼ", "ゾ"},
	"HH": {"ハ", "ヒ", "フ", "ヘ", "ホ"}, "L": {"ラ", "リ", "ル", "レ", "ロ"},
	"R": {"ラ", "リ", "ル", "レ", "ロ"}, "W": {"ワ", "ウィ", "ウ", "ウェ", "ウォ"},
	"Y": {"ヤ", "イ", "ユ", "イェ", "ヨ"},
}

// 末尾/子音連結時の既定モーラ。
var codaKana = map[string]string{
	"P": "プ", "B": "ブ", "T": "ト", "D": "ド", "K": "ク", "G": "グ",
	"M": "ム", "N": "ン", "NG": "ング", "F": "フ", "V": "ヴ", "S": "ス", "Z": "ズ",
	"SH": "シュ", "ZH": "ジュ", "CH": "チ", "JH": "ジ", "TH": "ス", "DH": "ズ",
	"HH": "フ", "L": "ル", "R": "ー", "W": "", "Y": "",
}

// 促音（ッ）を挿入する末尾閉鎖音。
var geminateCoda = map[string]bool{"P": true, "T": true, "K": true, "CH": true}

func stripStress(ph string) string {
	return strings.TrimRight(ph, "012")
}

func arpabetToKana(arpa string) string {
	phs := strings.Fields(arpa)
	var b strings.Builder
	lastVowelMora := false // 直前が母音で終わるモーラか（促音判定用）
	for i := 0; i < len(phs); i++ {
		ph := stripStress(phs[i])
		if v, ok := vowels[ph]; ok {
			b.WriteString(baseVowelKana[v.base] + v.glide)
			lastVowelMora = v.glide == ""
			continue
		}
		// 子音。次が母音なら CV モーラ、そうでなければ末尾モーラ。
		if row, ok := consKana[ph]; ok {
			// C + Y + 母音 → 拗音（M Y UW → ミュ, K Y UW → キュ）。
			if i+2 < len(phs) && stripStress(phs[i+1]) == "Y" {
				if nv, ok := vowels[stripStress(phs[i+2])]; ok {
					b.WriteString(row[1] + smallVowel[nv.base] + nv.glide) // row[1]=イ段
					lastVowelMora = nv.glide == ""
					i += 2
					continue
				}
			}
			if i+1 < len(phs) {
				if nv, ok := vowels[stripStress(phs[i+1])]; ok {
					b.WriteString(row[vowelIdx[nv.base]] + nv.glide)
					lastVowelMora = nv.glide == ""
					i++
					continue
				}
			}
			// coda
			if geminateCoda[ph] && lastVowelMora {
				b.WriteString("ッ")
			}
			b.WriteString(codaKana[ph])
			lastVowelMora = false
			continue
		}
		// 未知の音素は無視。
	}
	return collapseChoon(b.String())
}

// collapseChoon は連続する長音符（ー）を 1 つに畳む（AO+R などで重複しうる）。
func collapseChoon(s string) string {
	var b strings.Builder
	prevChoon := false
	for _, r := range s {
		if r == 'ー' {
			if prevChoon {
				continue
			}
			prevChoon = true
		} else {
			prevChoon = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
