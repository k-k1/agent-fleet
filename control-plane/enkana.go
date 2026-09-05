// enkana.go — preprocessing that rewrites English words as katakana English
// (docs/log/24). VOICEVOX (OpenJTalk) cannot read raw English spelling, so English words
// are looked up in the CMU pronouncing dictionary (word -> ARPABET) and mapped onto
// Japanese morae before synthesis. The result is plausible katakana English with a
// Japanese accent, not native pronunciation. Mixed Japanese/English text needs no special
// handling — only English tokens are converted.
//
// Dictionary: the CMU Pronouncing Dictionary (assets/cmudict.dict.gz, BSD-2,
// (c) 1993-2015 CMU; full license in assets/cmudict.LICENSE). alkana and bep-eng.dic are
// GPL and incompatible with this Apache-2.0 repository, so they are not used
// (docs/log/24). The dictionary is loaded lazily on first use.
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
	cmuMap  map[string]string // lowercased word -> ARPABET (first variant only)
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
		// "word  P1 P2 ... [# comment]"; a variant is spelled "word(2)".
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		w := fields[0]
		if j := strings.IndexByte(w, '('); j >= 0 {
			w = w[:j] // drop the variant suffix
		}
		w = strings.ToLower(w)
		if _, ok := cmuMap[w]; ok {
			continue // keep the first variant
		}
		cmuMap[w] = strings.Join(fields[1:], " ")
	}
	if err := sc.Err(); err != nil {
		// A partial load still converts (unlisted words pass through), so carry on —
		// but say so.
		log.Printf("enkana: cmudict scan failed (dictionary partially loaded, %d words): %v", len(cmuMap), err)
	}
}

// englishToKana converts the alphanumeric tokens in a text (a run of [A-Za-z0-9] that
// starts with a letter) to katakana and passes everything else through untouched:
// Japanese, punctuation, standalone numbers, whitespace. Requiring a leading letter is
// what leaves a standalone number to be read as Japanese, while letter-plus-digit forms
// like EC2 or iPhone15 are still picked up as one token. A word-internal apostrophe
// (it's / don't) belongs to the word — see isApostrophe below.
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
				// A word-internal apostrophe (it's, don't, we'll, User's) belongs to the
				// word — but only when a letter follows, so a trailing possessive (devs')
				// and a closing quote ('foo') are left out.
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

// isApostrophe treats the ASCII apostrophe (') and the typographic one (U+2019) alike.
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

// Digits read in English (EC2, S3), for the version and model numbers in technical terms.
// englishToKana never picks up a standalone digit, so what reaches here is only a digit
// that adjoins letters.
var digitKana = map[rune]string{
	'0': "ゼロ", '1': "ワン", '2': "ツー", '3': "スリー", '4': "フォー",
	'5': "ファイブ", '6': "シックス", '7': "セブン", '8': "エイト", '9': "ナイン",
}

// convertToken converts one alphanumeric token: the override table is consulted for the
// whole token first, a token containing digits is split into letter and digit runs, and a
// letters-only token goes to convertWord. A token with a word-internal apostrophe (a
// contraction or a possessive) goes to convertContraction.
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

// convertContraction converts a token with a word-internal apostrophe (it's / don't /
// User's). CMUdict reads those as two separate pieces ("it's" comes out as the word plus
// the letter S), so the frequent contractions get a fixed reading from contractionKana, a
// possessive 's becomes the base word plus a ZU mora, and anything else drops the
// apostrophe and goes through the normal path.
func convertContraction(tok string) string {
	norm := strings.ReplaceAll(tok, "’", "'")
	lower := strings.ToLower(norm)
	if k, ok := contractionKana[lower]; ok {
		return k
	}
	if strings.HasSuffix(lower, "'s") { // possessive: React's, user's
		return convertToken(norm[:len(norm)-2]) + "ズ"
	}
	return convertToken(strings.ReplaceAll(norm, "'", "")) // unknown contraction: drop the '
}

// convertAlnum converts a token that mixes letter runs and digit runs (EC2, iPhone15,
// utf8). A digit run reads one digit at a time in English; a letter run goes through
// convertWord, falling back to spelling the letters out when convertWord left it unchanged.
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
		if conv == seg { // a letter run outside the dictionary is spelled out (S3, utf)
			conv = spellLetters(strings.ToUpper(seg))
		}
		b.WriteString(conv)
		i = j
	}
	return b.String()
}

// convertWord turns one English word into katakana: a dictionary hit converts through
// ARPABET, an all-caps acronym is spelled out letter by letter, any other unknown word is
// split (camelCase and friends) with each part recursing, and a word still unknown after
// that keeps its spelling.
func convertWord(word string) string {
	lower := strings.ToLower(word)
	if k, ok := techKana[lower]; ok { // AWS and dev-jargon overrides win
		return k
	}
	// A lone uppercase letter is a label ("option A", "pattern B") and must be settled by
	// spelling it out before CMUdict is consulted: a, i and o are real CMUdict entries
	// (article, pronoun) and would be mis-read as words. Lowercase is excluded, so an "a"
	// inside an English sentence keeps reading as prose.
	if len(word) == 1 && isAllUpper(word) {
		return spellLetters(word)
	}
	if arpa, ok := cmuMap[lower]; ok {
		return arpabetToKana(arpa)
	}
	// 2 to 5 all-caps letters are an acronym and get spelled out (AWS).
	if len(word) >= 2 && len(word) <= 5 && isAllUpper(word) {
		return spellLetters(word)
	}
	// Split on camelCase and separators, then convert each part
	// (getUserById -> get/User/By/Id).
	parts := splitIdentifier(word)
	if len(parts) > 1 {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(convertWord(p))
		}
		return b.String()
	}
	return word // unknown word: leave the spelling and let VOICEVOX decide
}

func isAllUpper(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// splitIdentifier splits camelCase / PascalCase into words, keeping a run of capitals
// together as one acronym.
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

// --- ARPABET -> katakana morae -------------------------------------------------

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

// Small vowels for palatalized morae: the i-row kana plus one of these. i has none.
var smallVowel = map[byte]string{'a': "ャ", 'i': "", 'u': "ュ", 'e': "ェ", 'o': "ョ"}

// Consonant -> its kana for [a,i,u,e,o], including extended kana for foreign sounds.
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

// Default mora for a coda or a consonant cluster.
var codaKana = map[string]string{
	"P": "プ", "B": "ブ", "T": "ト", "D": "ド", "K": "ク", "G": "グ",
	"M": "ム", "N": "ン", "NG": "ング", "F": "フ", "V": "ヴ", "S": "ス", "Z": "ズ",
	"SH": "シュ", "ZH": "ジュ", "CH": "チ", "JH": "ジ", "TH": "ス", "DH": "ズ",
	"HH": "フ", "L": "ル", "R": "ー", "W": "", "Y": "",
}

// Final stops that take a geminate (small tsu) in front of them.
var geminateCoda = map[string]bool{"P": true, "T": true, "K": true, "CH": true}

func stripStress(ph string) string {
	return strings.TrimRight(ph, "012")
}

func arpabetToKana(arpa string) string {
	phs := strings.Fields(arpa)
	var b strings.Builder
	lastVowelMora := false // did the previous mora end in a vowel (decides the geminate)
	for i := 0; i < len(phs); i++ {
		ph := stripStress(phs[i])
		if v, ok := vowels[ph]; ok {
			b.WriteString(baseVowelKana[v.base] + v.glide)
			lastVowelMora = v.glide == ""
			continue
		}
		// Consonant: a CV mora when a vowel follows, otherwise a coda mora.
		if row, ok := consKana[ph]; ok {
			// C + Y + vowel -> a palatalized mora (M Y UW, K Y UW).
			if i+2 < len(phs) && stripStress(phs[i+1]) == "Y" {
				if nv, ok := vowels[stripStress(phs[i+2])]; ok {
					b.WriteString(row[1] + smallVowel[nv.base] + nv.glide) // row[1] = i-row kana
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
		// Unknown phonemes are ignored.
	}
	return collapseChoon(b.String())
}

// collapseChoon folds a run of long-vowel marks into one; AO+R and the like can emit two.
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
