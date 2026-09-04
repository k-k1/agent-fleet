package paths

import "testing"

// TestValidIDSegment pins ValidIDSegment, the path-traversal guard applied just before an id
// is used as a file name. It was once split across two implementations (chat_store.go and
// internal/assistants), so relaxing one of them went unnoticed; RECLAIM-B merged them.
// With a single implementation left, the table below pins the decision.
func TestValidIDSegment(t *testing.T) {
	for _, c := range []struct {
		name string
		id   string
		want bool
	}{
		{"randUUID() output", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", true},
		{"uppercase rejected (the generator emits lowercase hex)", "0F2B1C3D-4E5F-6A7B-8C9D-0E1F2A3B4C5D", false},
		{"not 36 characters", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5", false},
		{"empty", "", false},
		{"parent directory (rejected on length)", "..", false},
		{"traversal of exactly 36 characters", "../../../etc/passwd/aaaaaaaaaaaaaaaaa", false},
		{"contains a slash", "0f2b1c3d-4e5f-6a7b-8c9d/0e1f2a3b4c5d", false},
		{"contains a NUL byte", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c\x005", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidIDSegment(c.id); got != c.want {
				t.Errorf("ValidIDSegment(%q) = %v, want %v", c.id, got, c.want)
			}
		})
	}
}
