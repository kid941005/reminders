package reminder

import (
	"testing"
	"time"
)

func TestCountdownDaysLeft(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	today := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		row      Countdown
		wantNext string
		wantDays int
	}{
		{
			name:     "未来的一次性倒数",
			row:      Countdown{Title: "考试", TargetDate: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)},
			wantNext: "2026-10-22",
			wantDays: 3,
		},
		{
			name:     "已经过去的一次性纪念",
			row:      Countdown{Title: "入职", TargetDate: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)},
			wantNext: "2026-10-14",
			wantDays: -5,
		},
		{
			name: "每年重复且今年已过则取明年",
			row: Countdown{
				Title: "元旦", RepeatYearly: true, Calendar: "solar",
				TargetDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			wantNext: "2027-01-01",
			wantDays: 74,
		},
		{
			name: "每年重复且就是今天应算今天而不是明年",
			row: Countdown{
				Title: "生日", RepeatYearly: true, Calendar: "solar",
				TargetDate: time.Date(1995, 10, 19, 0, 0, 0, 0, time.UTC),
			},
			wantNext: "2026-10-19",
			wantDays: 0,
		},
		{
			// 2 月 29 日在平年没有对应日期，折回 2 月 28 日，
			// 与「每月重复遇短月取月末」的既有约定保持一致。
			name: "闰日在平年折回二月末",
			row: Countdown{
				Title: "闰日纪念", RepeatYearly: true, Calendar: "solar",
				TargetDate: time.Date(2020, 2, 29, 0, 0, 0, 0, time.UTC),
			},
			wantNext: "2027-02-28",
			wantDays: 132,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := buildCountdownDTO(tc.row, today, loc)
			if dto.NextDate != tc.wantNext {
				t.Fatalf("next date = %s, want %s", dto.NextDate, tc.wantNext)
			}
			if dto.DaysLeft != tc.wantDays {
				t.Fatalf("days left = %d, want %d", dto.DaysLeft, tc.wantDays)
			}
		})
	}
}

func TestCountdownLunarYearlyUsesLunarAnchor(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	// 2026-10-19 对应的农历日期作为锚点；下一次农历同一天必须落在未来一年内。
	created, err := createCountdown(appDB, 101, SaveCountdownInput{
		Title: "农历生日", TargetDate: "2026-10-19", RepeatYearly: true, Calendar: "lunar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.LunarAnchor == "" {
		t.Fatal("lunar countdown must persist a lunar anchor")
	}
	next, err := time.ParseInLocation("2006-01-02", created.NextDate, time.UTC)
	if err != nil {
		t.Fatalf("invalid next date %q: %v", created.NextDate, err)
	}
	today := dateOnlyUTC(time.Now(), mustShanghai(t))
	if next.Before(today) {
		t.Fatalf("lunar next date %s should not be before today", created.NextDate)
	}
	if next.Sub(today) > 400*24*time.Hour {
		t.Fatalf("lunar next date %s is more than a year away", created.NextDate)
	}
}

func TestCountdownValidationRejectsBadInput(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	if _, err := createCountdown(appDB, 102, SaveCountdownInput{Title: "  ", TargetDate: "2026-10-19"}); err == nil {
		t.Fatal("empty title must be rejected")
	}
	if _, err := createCountdown(appDB, 102, SaveCountdownInput{Title: "日期错", TargetDate: "2026/10/19"}); err == nil {
		t.Fatal("malformed date must be rejected")
	}
	if _, err := createCountdown(appDB, 102, SaveCountdownInput{Title: "日历错", TargetDate: "2026-10-19", Calendar: "mars"}); err == nil {
		t.Fatal("unknown calendar must be rejected")
	}
}

func mustShanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
