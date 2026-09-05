package main

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// TestTenantLimitsProjectionCoversEveryStoredField holds limits.go's tenantLimits (the
// only encoder of the stored tenant.limits blob) and internal/tenantsrv's Limits copy
// (no json tags) to the same field set. It is the reflect sweep ADR 0067 decision 5
// requires of a public struct taken as a dependency, applied to the one struct that
// crosses the seam.
//
// PUT /api/admin/tenants/{slug}/limits rewrites the whole blob, so a field added to
// limits.go but not to the copy is marshalled unset by tenant_wiring.go's
// tenantLimitsIn and every save silently erases what the operator configured. Build
// and tests stay green while only operator-entered values disappear, so the field sets
// themselves are what gets checked.
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
			t.Errorf("tenantLimits.%s is not in tenantsrv.Limits: it is silently dropped when limits are saved", name)
			continue
		}
		if got != typ {
			t.Errorf("%s: tenantLimits has %s / tenantsrv.Limits has %s", name, typ, got)
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			t.Errorf("tenantsrv.Limits.%s is not in tenantLimits: writing it does not save it", name)
		}
	}
}

// TestTenantLimitsRoundTripsThroughTheSeam catches what the field-set check cannot: a
// field that exists on both sides but whose hand-written assignment was never added
// (tenantLimitsIn / tenantLimitsOut are 16 assignments each). Every field gets a
// non-zero value and has to survive the round trip; a missing assignment comes back
// zero and fails.
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
			t.Fatalf("%s: unknown kind %s - add it to the round-trip check", v.Type().Field(i).Name, f.Kind())
		}
	}
	if got := tenantLimitsIn(tenantLimitsOut(src)); !reflect.DeepEqual(got, src) {
		t.Errorf("the value changed across the round trip:\n in  = %+v\n out = %+v", src, got)
	}
}
