package apps

import "testing"

func TestVisibleFiltersAndSorts(t *testing.T) {
	original := registered
	t.Cleanup(func() { registered = original })
	registered = []App{
		{Name: "bottom", DisplayName: "底部应用", RoutePrefix: "/bottom", NavPosition: 0},
		{Name: "second", DisplayName: "第二", RoutePrefix: "/second", NavPosition: 2},
		{Name: "first", DisplayName: "第一", RoutePrefix: "/first", NavPosition: 1},
		{Name: "backend-only", RoutePrefix: "/internal"},
	}
	got := Visible()
	if len(got) != 3 {
		t.Fatalf("want 3 visible apps, got %d: %+v", len(got), got)
	}
	if got[0].Name != "first" || got[1].Name != "second" || got[2].Name != "bottom" {
		t.Fatalf("unexpected order: %+v", got)
	}
}
