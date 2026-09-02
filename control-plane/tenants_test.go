package main

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// tenant.limits の blob は limits.go の tenantLimits が**唯一のエンコーダ**で、
// internal/tenantsrv は Limits という写し（json タグ無し）越しに値を渡すだけ——
// その取り決めを機械で縛るのがこの検査。ADR 0067 決定 5 の但し書き
// （「公開 struct で依存を受けるなら reflect の網羅検査が必須」）を、
// 切断面を跨ぐ唯一の struct に当てている。
//
// 🔴 なぜ要るか: PUT /api/admin/tenants/{slug}/limits は blob を**丸ごと書き換える**。
// limits.go にフィールドが 1 本増えて写しに増えなければ、alias_tenant.go の
// tenantLimitsIn がそれを埋めないまま marshal し、**保存のたびに既存の設定が
// 静かに消える**。ビルドもテストも通り、消えるのは運用者が入れた値だけ、という
// 一番高くつく壊れ方なので、フィールド集合の一致そのものを検査する。
func TestTenantLimitsProjectionCoversEveryStoredField(t *testing.T) {
	src := reflect.TypeOf(tenantLimits{})
	dst := reflect.TypeOf(tenantsrv.Limits{})

	fields := func(rt reflect.Type) map[string]string {
		out := map[string]string{}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			out[f.Name] = f.Type.String()
		}
		return out
	}
	a, b := fields(src), fields(dst)
	for name, typ := range a {
		got, ok := b[name]
		if !ok {
			t.Errorf("tenantLimits.%s は tenantsrv.Limits に無い: limits の保存で無言に落ちる", name)
			continue
		}
		if got != typ {
			t.Errorf("%s: tenantLimits は %s / tenantsrv.Limits は %s", name, typ, got)
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			t.Errorf("tenantsrv.Limits.%s は tenantLimits に無い: 書いても保存されない", name)
		}
	}
}

// 写しの検査だけでは「フィールドはあるが詰め替えを書き忘れた」を捕まえられない
// （tenantLimitsIn / tenantLimitsOut は手書きの代入 16 本）。そこで**全フィールドに
// 非ゼロ値を入れて往復させ、元に戻ることを見る**。1 本写し忘れればゼロ値で返るので落ちる。
func TestTenantLimitsRoundTripsThroughTheSeam(t *testing.T) {
	var src tenantLimits
	v := reflect.ValueOf(&src).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Int64:
			f.SetInt(int64(i+1) * 1024)
		case reflect.String:
			f.SetString(v.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.ValueOf([]string{v.Type().Field(i).Name}))
		default:
			t.Fatalf("%s: 未知の kind %s — 往復検査を足すこと", v.Type().Field(i).Name, f.Kind())
		}
	}
	if got := tenantLimitsIn(tenantLimitsOut(src)); !reflect.DeepEqual(got, src) {
		t.Errorf("往復で値が変わった:\n in  = %+v\n out = %+v", src, got)
	}
}
