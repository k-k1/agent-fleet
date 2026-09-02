package paths

import "testing"

// ValidIDSegment は id をそのままファイル名にする直前の path-traversal ガード。
// ウェーブ B では chat_store.go と internal/assistants の**2 実装**に割れていて、
// 片方だけ緩めても誰も気付かない形だった（RECLAIM-B で 1 本化）。
// 実装が 1 つになったので、判定を表でここに固定する。
func TestValidIDSegment(t *testing.T) {
	for _, c := range []struct {
		name string
		id   string
		want bool
	}{
		{"randUUID() の出力", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d", true},
		{"大文字は通さない（生成器は小文字 hex）", "0F2B1C3D-4E5F-6A7B-8C9D-0E1F2A3B4C5D", false},
		{"36 桁でない", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5", false},
		{"空", "", false},
		{"親ディレクトリ（長さで落ちる）", "..", false},
		{"36 桁ちょうどの traversal", "../../../etc/passwd/aaaaaaaaaaaaaaaaa", false},
		{"スラッシュ入り", "0f2b1c3d-4e5f-6a7b-8c9d/0e1f2a3b4c5d", false},
		{"ヌル文字入り", "0f2b1c3d-4e5f-6a7b-8c9d-0e1f2a3b4c\x005", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidIDSegment(c.id); got != c.want {
				t.Errorf("ValidIDSegment(%q) = %v, want %v", c.id, got, c.want)
			}
		})
	}
}
