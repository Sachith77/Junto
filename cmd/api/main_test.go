package main

import (
	"reflect"
	"testing"

	"github.com/junto/junto/internal/service"
)

// TestSyncEngineServicesHasNoNilFields closes a real gap, not a wrong assertion (D104).
//
// Budget went missing from syncengine.Services in run()'s wiring while the entire test suite
// stayed green, because tests/stack_test.go builds its OWN copy of that literal rather than
// exercising run()'s. This is not the vacuous-pass shape CLAUDE.md's "standing principle"
// section catalogs elsewhere (a test whose assertion doesn't check what it claims to) — it is
// the shape one level earlier: a code path with no test on it AT ALL. The fix is to call the
// exact function run() calls (newSyncEngineServices), not a second hand-written copy that could
// itself drift the same way stack_test.go's did, and fail if anything it returns is nil — which
// is precisely the failure mode that shipped.
//
// Every service pointer is a distinct zero-value sentinel: allocation alone proves non-nil-ness
// without needing a database, which is what makes this test fast enough to run on every build
// rather than requiring the testcontainers path the full-stack tests use.
func TestSyncEngineServicesHasNoNilFields(t *testing.T) {
	got := newSyncEngineServices(
		&service.TripService{},
		&service.DayService{},
		&service.SlotService{},
		&service.SlotOptionService{},
		&service.VoteService{},
		&service.CommentService{},
		&service.BudgetService{},
	)

	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		field := v.Field(i)
		if field.Kind() != reflect.Ptr {
			t.Fatalf("syncengine.Services.%s is not a pointer (kind %s) — this test assumes "+
				"every field is a service pointer; update it if that assumption changes", name, field.Kind())
		}
		if field.IsNil() {
			t.Errorf("syncengine.Services.%s is nil — a real client submitting an op this "+
				"service handles would nil-panic in production, exactly the bug this test exists "+
				"to catch", name)
		}
	}
}
