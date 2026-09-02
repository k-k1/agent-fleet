// capabilities_test.go — どのアダプタがどの任意インタフェースを名乗るか
// (TestDocsMounterMarker は docs_bridge_test.go 由来).
//
// 任意インタフェースは `rt.(X)` / `f.(X)` の成否で分岐するので、**名乗らなくなっても
// コンパイルは通り、その機能が静かに消えるだけ**。名乗る方向は宣言箇所の
// `var _ X = (*T)(nil)` が押さえられるが、名乗らない方向はそれでは書けない——
// ここがその受け皿。未公開のアダプタ型そのものを見るので、実装と同じパッケージにしか
// 置けない。
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

// 焼けるのは EC2 スロットプールだけ。GoldenBakePool は共有スナップショットから新しい
// ホームを作る配備の話で、他のプロファイルには焼く相手も置き場も無い。
//
// 名乗る方向は runtime_ecs_ec2_golden.go の `var _ GoldenBakePool = (*ecsEC2Factory)(nil)`
// が押さえている。こちらは逆で、docker/native が誤って満たしてしまった場合に
// goldenBakerFor がベイカーを立てはじめる——CP 側からは「なぜか golden を焼こうとする
// docker 配備」に見え、しかもエラーにはならない。
func TestOnlyThePoolAdapterClaimsTheGoldenBake(t *testing.T) {
	for _, c := range []struct {
		name string
		f    RuntimeFactory
	}{
		{"dockerFactory", (*dockerFactory)(nil)},
		{"nativeFactory", (*nativeFactory)(nil)},
		{"ecsFactory", (*ecsFactory)(nil)},
	} {
		if _, ok := any(c.f).(GoldenBakePool); ok {
			t.Errorf("%s claims GoldenBakePool — it has no slot pool to seed a home from", c.name)
		}
	}
}
