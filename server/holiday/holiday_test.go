package holiday

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinCoversKnownHolidays(t *testing.T) {
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
	ds := Default()

	cases := []struct {
		date string
		name string
		off  bool
	}{
		{"2026-10-01", "国庆节", true},
		{"2026-02-17", "春节", true},
		{"2026-09-25", "中秋", true},
		// 2026-10-10 是国庆调的补班日，必须是「上班」而不是休息。
		{"2026-10-10", "国庆节", false},
	}
	for _, tc := range cases {
		day, ok := ds.Lookup(tc.date)
		if !ok {
			t.Fatalf("%s 未命中，内置数据可能缺失", tc.date)
		}
		if day.Name != tc.name || day.Off != tc.off {
			t.Fatalf("%s = %+v, want name=%s off=%v", tc.date, day, tc.name, tc.off)
		}
	}

	minYear, maxYear := ds.Years()
	if minYear < 2004 || maxYear < 2026 {
		t.Fatalf("coverage = %d-%d, 至少应覆盖 2004-2026", minYear, maxYear)
	}
}

// 未覆盖的年份必须查不到，而不是返回「正常工作日」。
// 前端据此提示「该年公告尚未发布」。
func TestUncoveredYearReturnsMiss(t *testing.T) {
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
	_, maxYear := Default().Years()
	if _, ok := Default().Lookup("2099-01-01"); ok {
		t.Fatal("超出覆盖范围的日期不应命中")
	}
	if maxYear < 2026 {
		t.Fatalf("max year = %d", maxYear)
	}
}

func TestRangeFiltersByDate(t *testing.T) {
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
	result := Default().Range("2026-10-01", "2026-11-01")
	if _, ok := result.Days["2026-10-01"]; !ok {
		t.Fatal("区间起点应包含")
	}
	if _, ok := result.Days["2026-10-10"]; !ok {
		t.Fatal("区间内的补班日应包含")
	}
	if _, ok := result.Days["2026-11-01"]; ok {
		t.Fatal("区间是左闭右开，终点不应包含")
	}
	if result.MinYear == 0 || result.MaxYear == 0 {
		t.Fatal("区间结果应带上覆盖年份")
	}
	// 越界区间返回空集合而不是 nil，避免前端多做一次判空。
	if empty := Default().Range("", ""); empty.Days == nil {
		t.Fatal("空区间应返回空 map 而非 nil")
	}
}

func TestOverrideFileTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "holidays.json")
	// 覆盖文件把 2026-10-10 改成休息，模拟「公司当天放假」的特殊排班。
	content := `{"holidays":{"2026-10-10":"Company,公司调休,1"},"workdays":{}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	day, ok := Default().Lookup("2026-10-10")
	if !ok || day.Name != "公司调休" || !day.Off {
		t.Fatalf("覆盖未生效: %+v ok=%v", day, ok)
	}
	// 未覆盖的其它日期仍来自内置数据。
	if builtin, ok := Default().Lookup("2026-10-01"); !ok || builtin.Name != "国庆节" {
		t.Fatalf("内置数据被破坏: %+v ok=%v", builtin, ok)
	}

	if err := Load(""); err != nil {
		t.Fatal(err)
	}
}

// 覆盖文件损坏时不能阻断启动，应静默退回内置数据。
func TestBrokenOverrideFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "holidays.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(path); err != nil {
		t.Fatalf("损坏的覆盖文件不应返回错误: %v", err)
	}
	if _, ok := Default().Lookup("2026-10-01"); !ok {
		t.Fatal("应退回内置数据")
	}
	if err := Load(""); err != nil {
		t.Fatal(err)
	}
}

func TestParseName(t *testing.T) {
	cases := map[string]string{
		"New Year's Day,元旦,1": "元旦",
		"国庆节":                  "国庆节",
		" ,中秋,1":               "中秋",
	}
	for raw, want := range cases {
		if got := parseName(raw); got != want {
			t.Fatalf("parseName(%q) = %q, want %q", raw, got, want)
		}
	}
}
