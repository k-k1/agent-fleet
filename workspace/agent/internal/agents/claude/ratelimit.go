package claude

// 利用上限が解ける時刻の確定（docs/log/47 §4-4）。
//
// 上限でターンが切れると claude は転写にバナーを 1 行残す:
//
//	You've hit your session limit · resets 7:50pm (Asia/Tokyo)
//
// これは「時刻が来れば解ける中断」だが、abort.go の分類は retryable / blocked の 2 値
// しかないので blocked に倒している（再送しても同じ時刻までは同じ結果になる）。ここは
// その第 3 クラスのために「いつ解けるか」だけを答える。答えは 2 つの独立な材料から作る:
//
//   - バナーの壁時計（転写＝そのセッションが実際に受け取った文言）。日付は書かれていない。
//   - statusline 捕捉（af-usage.json）の resets_at。unix epoch なので曖昧さが無いが、
//     アカウント単位で、しかも最後に描画されたときの値なので古いことがある。
//
// 壁時計だけだと「翌日の同じ時刻」と区別できず、epoch だけだと 5 時間窓と週次窓の
// どちらに当たったのかが分からない。よって**バナーで窓を選び、epoch で日付を確定する**。

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// resetClockRe pulls the reset wall-clock out of the banner. 実測しているのは
// "resets 7:50pm (Asia/Tokyo)" の形だけなので、それを確実に取りつつ、日付を伴う形
// （"resets Aug 3 at 9am (…)"）や分・タイムゾーンの欠落にも耐えるようにしてある。
// am/pm は必須 — 数字だけを拾うと本文中の無関係な数値に当たる。
var resetClockRe = regexp.MustCompile(`(?i)resets\s+(?:[a-z]{3,9}\.?\s+\d{1,2},?\s+)?(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b\s*(?:\(([^)]+)\))?`)

// weeklyBannerRe recognises the weekly window's banner — "You've hit your weekly limit ·
// resets 9am (Asia/Tokyo)"（実測コーパス 2026-08-20）。日付を伴う形（"resets Aug 3 at 9am"）は
// 壁時計だけの曖昧さが無いので除外する。
var weeklyBannerRe = regexp.MustCompile(`(?i)weekly limit.*resets\s+(?:at\s+)?\d{1,2}(?::\d{2})?\s*(?:am|pm)\b`)

// parseResetClock reads the banner's reset time. loc is the banner's own zone when it
// names one (claude prints the IANA name), else the container's local zone — which is
// the same zone claude renders in, so the fallback is not a guess about the user.
func parseResetClock(msg string) (hour, min int, loc *time.Location, ok bool) {
	m := resetClockRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0, nil, false
	}
	h, err := strconv.Atoi(m[1])
	if err != nil || h < 1 || h > 12 {
		return 0, 0, nil, false
	}
	if m[2] != "" {
		if min, err = strconv.Atoi(m[2]); err != nil || min > 59 {
			return 0, 0, nil, false
		}
	}
	// 12 時制 → 24 時制。12am = 0 時、12pm = 12 時。
	h %= 12
	if strings.EqualFold(m[3], "pm") {
		h += 12
	}
	loc = time.Local
	if m[4] != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(m[4])); err == nil {
			loc = l
		}
	}
	return h, min, loc, true
}

// firstAfter returns the first instant whose wall clock in loc is hour:min and which is
// strictly after base. base は「バナーが書かれた時刻」＝中断レコードのタイムスタンプ。
// now ではなく base を基準にするのは、メニューが何時間も出しっぱなしのまま発見される
// ことがあるから（実測 約16時間）。now 基準だと既に過ぎたリセットを「翌日」と読む。
func firstAfter(base time.Time, hour, min int, loc *time.Location) time.Time {
	b := base.In(loc)
	t := time.Date(b.Year(), b.Month(), b.Day(), hour, min, 0, 0, loc)
	if !t.After(b) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// capturedResets returns the reset instants of the last statusline capture (five-hour
// and seven-day), oldest first. Zero/absent windows are dropped.
func capturedResets() []time.Time {
	c, _ := readCapturedUsage()
	if c == nil {
		return nil
	}
	var out []time.Time
	for _, w := range []*capturedWindow{c.FiveHour, c.SevenDay} {
		if w != nil && w.ResetsAt > 0 {
			out = append(out, time.Unix(w.ResetsAt, 0))
		}
	}
	if len(out) == 2 && out[1].Before(out[0]) {
		out[0], out[1] = out[1], out[0]
	}
	return out
}

// resetMatchWindow is how far a captured epoch may sit from the banner's wall clock and
// still be considered the same reset. claude rounds the banner to the minute, so this is
// slack for rounding, not for a different window (the two windows are hours apart).
const resetMatchWindow = 2 * time.Minute

// resolveResetAt is the pure decision: when does the limit behind msg lift?
//
//	abortedAt — 中断レコードの時刻（バナーが書かれた瞬間）。ゼロなら now で代用する。
//	captured  — statusline 捕捉の resets_at 群。
//
// source は判断材料のラベル（ログ用）。ok=false は「決められなかった」で、呼び出し側は
// 自動再開を仕込まない — 当てずっぽうの時刻に起こしても、また上限に当たるだけ。
func resolveResetAt(msg string, abortedAt time.Time, captured []time.Time, now time.Time) (at time.Time, source string, ok bool) {
	base := abortedAt
	if base.IsZero() {
		base = now
	}
	if h, m, loc, parsed := parseResetClock(msg); parsed {
		want := firstAfter(base, h, m, loc)
		// 捕捉した epoch が同じ壁時計を指しているなら、そちらを正とする（日付が確定する）。
		for _, c := range captured {
			if c.After(base) && absDur(c.Sub(want)) <= resetMatchWindow {
				return c, "banner+capture", true
			}
		}
		// 週次の窓だけはバナー単独で決めない（docs/log/47 §4-10）。バナーは壁時計しか書かない
		// ので "resets 9am" は「今日か明日の 9時」としか読めないが、週次のリセットは数日先に
		// あり得る。firstAfter が返す明日の 9時に起こしても同じ 429 を踏み、そのたびに新しい
		// エピソードが開いて予約し直す — 本当のリセットまで毎日 1 ターンずつ焼く。
		//
		// 上の一致判定は「同じ瞬間か」なので、数日先の週次リセットには当たらない。ここでは
		// **壁時計だけ**を突き合わせて日付は捕捉の epoch に決めさせる。新しい方から見るのは、
		// 捕捉が返す 2 つの窓（5時間・週次）のうち週次は必ず後ろだから。
		if weeklyBannerRe.MatchString(msg) {
			for i := len(captured) - 1; i >= 0; i-- {
				c := captured[i]
				if l := c.In(loc); c.After(base) && l.Hour() == h && l.Minute() == m {
					return c, "banner+capture", true
				}
			}
			return time.Time{}, "", false
		}
		return want, "banner", true
	}
	// バナーが読めない（版で文言が変わった等）。捕捉した窓のうち、まだ来ていない最も
	// 早いものへ賭ける — 上限に当たっている以上、次に解けるのはそのどれかである。
	for _, c := range captured {
		if c.After(now) {
			return c, "capture", true
		}
	}
	return time.Time{}, "", false
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ResetAt answers when the session's usage limit lifts, reading the session's own
// transcript tail (the banner) and the shared statusline capture. ok=false when neither
// material yields a time.
func ResetAt(sid string, now time.Time) (time.Time, string, bool) {
	a, _ := AbortInfo(sid)
	return resolveResetAt(a.Msg, a.At, capturedResets(), now)
}
