package claude

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata なし (%s): %v", name, err)
	}
	return loc
}

// TestParseResetClock: バナーからリセットの壁時計を読む。実測している形（session limit）
// を軸に、12 時制の境界と表記ゆれを固定する。
func TestParseResetClock(t *testing.T) {
	mustLoc(t, "Asia/Tokyo") // tzdata が無い環境ではこのテーブルは意味を成さない
	for _, tc := range []struct {
		name         string
		msg          string
		wantH, wantM int
		wantZone     string
		wantUnparsed bool
	}{
		{
			name:  "実測（2026-07-31 / s5jjqv4）",
			msg:   "You've hit your session limit · resets 7:50pm (Asia/Tokyo)",
			wantH: 19, wantM: 50, wantZone: "Asia/Tokyo",
		},
		{
			name:  "分なし",
			msg:   "You've hit your session limit · resets 9am (Asia/Tokyo)",
			wantH: 9, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// 12am/12pm は %12 の境界。ここを間違えると 12 時間ずれた時刻に起こす。
			name:  "12am は 0 時",
			msg:   "resets 12am (Asia/Tokyo)",
			wantH: 0, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			name:  "12pm は 12 時",
			msg:   "resets 12pm (Asia/Tokyo)",
			wantH: 12, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// 週次上限のように日付が付く形（未実測・防御的）。日付は捨てるが、時刻は拾う。
			name:  "日付つき",
			msg:   "You've reached your weekly limit · resets Aug 3 at 9am (Asia/Tokyo)",
			wantH: 9, wantM: 0, wantZone: "Asia/Tokyo",
		},
		{
			// タイムゾーン表記が無ければコンテナのローカル（claude が描画に使う zone）。
			name:  "タイムゾーンなし",
			msg:   "resets 7:50pm",
			wantH: 19, wantM: 50, wantZone: time.Local.String(),
		},
		{
			// am/pm を必須にしているので、無関係な数値には当たらない。
			name: "am/pm なしは読まない", msg: "resets in 4 hours", wantUnparsed: true,
		},
		{
			name: "中断エラーだが上限ではない", msg: "API Error: Connection closed mid-response.", wantUnparsed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m, loc, ok := parseResetClock(tc.msg)
			if ok == tc.wantUnparsed {
				t.Fatalf("parseResetClock ok = %v, want %v", ok, !tc.wantUnparsed)
			}
			if tc.wantUnparsed {
				return
			}
			if h != tc.wantH || m != tc.wantM {
				t.Errorf("clock = %02d:%02d, want %02d:%02d", h, m, tc.wantH, tc.wantM)
			}
			if loc.String() != tc.wantZone {
				t.Errorf("zone = %s, want %s", loc, tc.wantZone)
			}
		})
	}
}

// TestResolveResetAt: 「いつ解けるか」の決め方。
func TestResolveResetAt(t *testing.T) {
	jst := mustLoc(t, "Asia/Tokyo")
	banner := "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	// 実測の中断時刻（バナーが書かれた瞬間）。
	abortedAt := time.Date(2026, 7, 30, 18, 8, 48, 0, jst)
	want := time.Date(2026, 7, 30, 19, 50, 0, 0, jst)

	t.Run("バナーだけ", func(t *testing.T) {
		got, src, ok := resolveResetAt(banner, abortedAt, nil, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(want) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, want)
		}
		if src != "banner" {
			t.Errorf("source = %q, want banner", src)
		}
	})

	t.Run("捕捉epochが同じ壁時計を指すならそれを正とする", func(t *testing.T) {
		// 5時間窓が 19:50、週次窓が数日先。窓の取り違えはここで起きる。
		captured := []time.Time{want.Add(37 * time.Second), want.AddDate(0, 0, 4)}
		got, src, ok := resolveResetAt(banner, abortedAt, captured, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(captured[0]) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, captured[0])
		}
		if src != "banner+capture" {
			t.Errorf("source = %q, want banner+capture", src)
		}
	})

	t.Run("放置されたメニューは過ぎたリセットを翌日にしない", func(t *testing.T) {
		// 本番の壊れ方: メニューが約16時間出しっぱなしで発見される。now を基準にすると
		// 19:50 は「翌日の 19:50」に化け、まる一日待つことになる。
		now := abortedAt.Add(16 * time.Hour)
		got, _, ok := resolveResetAt(banner, abortedAt, nil, now)
		if !ok {
			t.Fatal("ok = false")
		}
		if !got.Equal(want) {
			t.Fatalf("resolveResetAt = %v, want %v（中断時刻の直後の 19:50）", got, want)
		}
		if !got.Before(now) {
			t.Error("既に過ぎたリセットが未来として返っている — 呼び出し側の即時再開に落ちない")
		}
	})

	t.Run("バナーが読めなければ捕捉した未来の窓", func(t *testing.T) {
		now := abortedAt
		captured := []time.Time{abortedAt.Add(-time.Hour), abortedAt.Add(3 * time.Hour)}
		got, src, ok := resolveResetAt("You've hit some new limit wording", abortedAt, captured, now)
		if !ok || !got.Equal(captured[1]) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, captured[1])
		}
		if src != "capture" {
			t.Errorf("source = %q, want capture", src)
		}
	})

	// 週次の窓（実測コーパス "You've hit your weekly limit · resets 9am (Asia/Tokyo)"）。
	// バナーの壁時計は「今日か明日の 9時」としか読めないが、週次のリセットは数日先に
	// あり得る。明日の 9時に起こしても同じ 429 を踏み、そのたび新しいエピソードが予約を
	// 引き直す — 本当のリセットまで毎日 1 ターンずつ焼く（docs/47 §4-10）。
	t.Run("週次はバナー単独では決めない", func(t *testing.T) {
		weekly := "You've hit your weekly limit · resets 9am (Asia/Tokyo)"
		if at, src, ok := resolveResetAt(weekly, abortedAt, nil, abortedAt.Add(time.Minute)); ok {
			t.Errorf("resolveResetAt = %v (%s, ok=true) — 明日の 9時に賭けている", at, src)
		}
		// 捕捉の epoch が同じ壁時計を指していれば日付が確定するので、そのときは答える。
		real := time.Date(2026, 8, 3, 9, 0, 0, 0, jst)
		got, src, ok := resolveResetAt(weekly, abortedAt, []time.Time{real}, abortedAt.Add(time.Minute))
		if !ok || !got.Equal(real) {
			t.Fatalf("resolveResetAt = %v (%s, ok=%v), want %v", got, src, ok, real)
		}
		if src != "banner+capture" {
			t.Errorf("source = %q, want banner+capture", src)
		}
	})

	t.Run("材料が無ければ決めない", func(t *testing.T) {
		if _, _, ok := resolveResetAt("", time.Time{}, nil, abortedAt); ok {
			t.Error("ok = true — 当てずっぽうの時刻で起こしても、また上限に当たるだけ")
		}
		// 捕捉が全部過去（古いまま更新されていない）のときも決めない。
		if _, _, ok := resolveResetAt("unknown", abortedAt, []time.Time{abortedAt.Add(-time.Hour)}, abortedAt); ok {
			t.Error("ok = true — 過去の epoch を再開時刻に採用している")
		}
	})
}
