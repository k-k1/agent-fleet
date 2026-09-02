// docs_mounter_test.go — どのアダプタが <dataDir>/docs を用意するか
// (docs_bridge_test.go 由来).
//
// 未公開のアダプタ型そのものを見る検査なので、実装と同じパッケージにしか置けない。
package runtime

import "testing"

// Which adapters stage <dataDir>/docs is a fact the start path reads off the type. If an
// ECS adapter ever claimed the marker it would copy megabytes onto the CP's disk that no
// task can read; if docker/native lost it, their bind mount would go empty.
func TestDocsMounterMarker(t *testing.T) {
	var _ DocsMounter = (*dockerRuntime)(nil)
	var _ DocsMounter = (*nativeRuntime)(nil)
	if _, ok := any((*ecsRuntime)(nil)).(DocsMounter); ok {
		t.Error("ecsRuntime has no host seam to mount from — it must not claim staged docs")
	}
	if _, ok := any((*ecsEC2Runtime)(nil)).(DocsMounter); ok {
		t.Error("ecsEC2Runtime has no host seam to mount from — it must not claim staged docs")
	}
}
