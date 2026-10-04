package sessionsearch

import (
	"strings"
	"testing"
)

func TestIndexTextCutsCJKRunsIntoBigrams(t *testing.T) {
	cases := []struct{ in, want string }{
		{"前回の認証エラー", "前回 回の の認 認証 証エ エラ ラー"},
		{"API認証を直す", "API 認証 証を を直 直す"},
		{"a 字 b", "a 字 b"}, // a one-character run stays a unigram
		{"ＡＰＩとｶﾀｶﾅ", "API とカ カタ タカ カナ"}, // width-folded first
		{"ﾊﾞｸﾞ修正", "バグ グ修 修正"},          // and recomposed: the voiced mark must not cut the run
		{"plain text, only", "plain text, only"},
	}
	for _, c := range cases {
		if got := strings.Join(strings.Fields(IndexText(c.in)), " "); got != c.want {
			t.Errorf("IndexText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchQueryQuotesEveryTerm(t *testing.T) {
	cases := []struct{ in, want string }{
		{"認証", `"認証"`},
		{"認証エラー", `"認証 証エ エラ ラー"`},
		{"auth*", `"auth"*`},
		{"認", `"認"*`}, // a lone CJK character is only stored inside bigrams
		{"NOT OR", `"NOT" "OR"`},
		{`say"hi`, `"say""hi"`}, // the quote stays inside the phrase, escaped
		{"foo.bar　認証", `"foo.bar" "認証"`},
		{"ﾊﾞｸﾞ", `"バグ"`},
		{"① Ⅲ", `"①" "Ⅲ"`}, // number-like, not digits: unicode61 keeps them as tokens
		{"dup dup", `"dup"`},
	}
	for _, c := range cases {
		got, err := MatchQuery(c.in)
		if err != nil || got != c.want {
			t.Errorf("MatchQuery(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, empty := range []string{"", "   ", "!!! ...", "*"} {
		if _, err := MatchQuery(empty); err != errEmptyQuery {
			t.Errorf("MatchQuery(%q) err = %v, want errEmptyQuery", empty, err)
		}
	}
}

func TestSnippetCentresOnTheFirstTerm(t *testing.T) {
	text := strings.Repeat("あ", 300) + "認証エラーを直した" + strings.Repeat("い", 300)
	got := Snippet(text, []string{"認証"}, 40)
	if !strings.Contains(got, "認証エラー") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet = %q", got)
	}
	if got := Snippet("Fix the\n\nAuth   bug", []string{"auth"}, 200); got != "Fix the Auth bug" {
		t.Fatalf("whitespace/case: %q", got)
	}
	if got := Snippet("abcdef", []string{"zzz"}, 3); got != "abc…" {
		t.Fatalf("no term found should give the head: %q", got)
	}
}
