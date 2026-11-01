package adminnotice

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The CP's thresholds are the Console's, number for number: this reads the TypeScript source so
// that moving one side alone fails here.
func TestHomePressureThresholdsMatchTheConsole(t *testing.T) {
	raw, err := os.ReadFile("../../../console/src/core/store/homeDiskPressure.ts")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	num := func(name string) uint64 {
		m := regexp.MustCompile(`export const ` + name + ` = (\d+)(?: \* GIB)?;`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in homeDiskPressure.ts", name)
		}
		v, _ := strconv.ParseUint(m[1], 10, 64)
		if regexp.MustCompile(`export const ` + name + ` = \d+ \* GIB;`).MatchString(src) {
			v *= gib
		}
		return v
	}
	for name, want := range map[string]uint64{
		"RAISE_FREE_BYTES": homePressureRaiseFreeBytes, "RAISE_PCT": homePressureRaisePct,
		"CLEAR_FREE_BYTES": homePressureClearFreeBytes, "CLEAR_PCT": homePressureClearPct,
	} {
		if got := num(name); got != want {
			t.Errorf("%s: Console %d, CP %d", name, got, want)
		}
	}
}

// The same cases as homeDiskPressure.test.ts.
func TestNextHomeDiskPressureMirrorsTheConsoleCases(t *testing.T) {
	const G = uint64(gib)
	cases := []struct {
		prev        bool
		used, total uint64
		want        bool
	}{
		{false, 99 * G, 100 * G, true},
		{false, 920 * G, 1000 * G, true},
		{false, 50 * G, 100 * G, false},
		{true, 97*G + G/2, 100 * G, true},
		{true, 86 * G, 100 * G, true},
		{true, 80 * G, 100 * G, false},
		{true, 0, 0, true},
		{false, 1, 0, false},
	}
	for _, c := range cases {
		if got := nextHomeDiskPressure(c.prev, c.used, c.total); got != c.want {
			t.Errorf("next(%v, %d, %d) = %v, want %v", c.prev, c.used, c.total, got, c.want)
		}
	}
}
