package session

import "testing"

func TestManagedSettingsPersistInMeta(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	want := Meta{
		Name:   "managed-settings",
		Dir:    "/tmp/repo",
		Kind:   KindCodex,
		Driver: DriverManaged,
		Model:  "gpt-test",
		Effort: "high",
		Mode:   "plan",
	}
	WriteMeta(want)
	got, ok := ReadMeta(want.Name)
	if !ok {
		t.Fatal("saved meta was not readable")
	}
	if got.Model != want.Model || got.Effort != want.Effort || got.Mode != want.Mode {
		t.Fatalf("managed settings did not round-trip: %+v", got)
	}
}
