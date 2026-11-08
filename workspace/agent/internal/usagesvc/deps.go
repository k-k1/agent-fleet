package usagesvc

// deps.go — every hand usagesvc reaches out to package main with, on one sheet.
//
// usagesvc does not import main (the dependency already runs the other way), so "call
// something in main" arrives as a value wired once at boot (main's usage_wiring.go). Configure
// panics on a gap: an unwired ModelsDevPath would read the operator's catalog slot as the
// fetched copy's.

import (
	"fmt"
	"reflect"
	"sort"
)

// Deps is the outside world as usagesvc sees it. It holds no type from main.
type Deps struct {
	// ModelsDevPath is where main's daily models.dev refresh stores the fetched catalog
	// (model_catalog_fetch.go); usagesvc reads it as one of the catalog sources.
	ModelsDevPath func() string
}

var deps Deps

// Configure is called once at boot (main's usage_wiring.go, or usagesvc's own tests).
// Completeness is taken with reflect so a field added later cannot be forgotten.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if v.Field(i).IsZero() {
			missing = append(missing, t.Field(i).Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("usagesvc.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
}

// Wired returns the current wiring, for a caller checking end to end that it is live.
// Configure catches only what is unwired, never what is wired wrong.
func Wired() Deps { return deps }
