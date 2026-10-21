package reminder

import (
	"errors"
	"strings"
	"time"

	"smallgo/server/lunar"
	"smallgo/server/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CountdownDTO 在实体之外补两个计算字段。下一次发生的日期与剩余天数需要
// 农历和闰年规则，放在后端算可以避免前端再实现一套、两边结果对不上。
type CountdownDTO struct {
	Countdown
	// NextDate 是下一次发生的日期（YYYY-MM-DD）。每年重复时取今年或明年的那天；
	// 不重复时就是目标日期本身（可能已经过去）。
	NextDate string `json:"next_date"`
	// DaysLeft 相对今天：正数=还有几天，0=就是今天，负数=已经过去几天。
	DaysLeft int `json:"days_left"`
}

type SaveCountdownInput struct {
	Title        string `json:"title"`
	TargetDate   string `json:"target_date"`
	RepeatYearly bool   `json:"repeat_yearly"`
	Calendar     string `json:"calendar"`
	Notes        string `json:"notes"`
	Color        string `json:"color"`
}

// dateOnlyUTC 把任意时刻折算成「loc 时区的那一天」的 UTC 零点表示。
// 全项目只把这种值用于日期比较，相减得到的天数是精确整数天。
func dateOnlyUTC(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// safeSolarDate 构造某一年的月/日。2 月 29 日在平年会被顺延到 3 月 1 日，
// 这里折回该月最后一天（2 月 28 日），与「每月重复遇短月取月末」的既有约定一致。
func safeSolarDate(year, month, day int, loc *time.Location) time.Time {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, loc)
	if int(t.Month()) != month {
		t = time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, loc)
	}
	return dateOnlyUTC(t, loc)
}

// buildCountdownDTO 计算下一次发生的日期与相对今天的天数。
func buildCountdownDTO(row Countdown, today time.Time, loc *time.Location) CountdownDTO {
	dto := CountdownDTO{Countdown: row}
	next := row.TargetDate
	if row.RepeatYearly {
		switch row.Calendar {
		case "lunar":
			if anchor, ok := parseLunarAnchor(row.LunarAnchor); ok {
				// NextOccurrence 用严格 After 比较，传「今天零点减一秒」
				// 才能让「就是今天」的那次生日被算作下一次，而不是跳到明年。
				from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).Add(-time.Second)
				if occurrence, err := lunar.NextOccurrence(anchor, from, loc); err == nil {
					next = dateOnlyUTC(occurrence, loc)
				}
			}
		default:
			month, day := int(row.TargetDate.Month()), row.TargetDate.Day()
			candidate := safeSolarDate(today.Year(), month, day, loc)
			if candidate.Before(today) {
				candidate = safeSolarDate(today.Year()+1, month, day, loc)
			}
			next = candidate
		}
	}
	dto.NextDate = next.Format("2006-01-02")
	dto.DaysLeft = int(next.Sub(today).Hours() / 24)
	return dto
}

// normalize 校验并归一化输入，返回按 UTC 零点表示的日期。
func (in *SaveCountdownInput) normalize() (time.Time, error) {
	in.Title = strings.TrimSpace(in.Title)
	if n := len([]rune(in.Title)); n == 0 || n > 60 {
		return time.Time{}, errors.New("名称应为 1–60 个字符")
	}
	in.Notes = strings.TrimSpace(in.Notes)
	if in.Color == "" {
		in.Color = "rose"
	}
	if in.Calendar == "" {
		in.Calendar = "solar"
	}
	if in.Calendar != "solar" && in.Calendar != "lunar" {
		return time.Time{}, errors.New("日历类型无效")
	}
	if !in.RepeatYearly {
		// 不重复时农历没有意义，统一存公历，避免出现无法解释的组合。
		in.Calendar = "solar"
	}
	target, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(in.TargetDate), time.UTC)
	if err != nil {
		return time.Time{}, errors.New("日期格式应为 YYYY-MM-DD")
	}
	return target, nil
}

func listCountdowns(db *gorm.DB, userID uint) ([]CountdownDTO, error) {
	loc := shanghai()
	today := dateOnlyUTC(time.Now(), loc)
	var rows []Countdown
	if err := db.Where("user_id = ?", userID).
		Order("target_date ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]CountdownDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, buildCountdownDTO(row, today, loc))
	}
	return items, nil
}

func createCountdown(db *gorm.DB, userID uint, in SaveCountdownInput) (CountdownDTO, error) {
	target, err := in.normalize()
	if err != nil {
		return CountdownDTO{}, err
	}
	row := Countdown{
		UserID: userID, Title: in.Title, TargetDate: target,
		RepeatYearly: in.RepeatYearly, Calendar: in.Calendar,
		Notes: in.Notes, Color: in.Color,
	}
	if in.Calendar == "lunar" {
		anchor, err := normalizeLunarAnchor("", target)
		if err != nil {
			return CountdownDTO{}, err
		}
		row.LunarAnchor = anchor
	}
	var maxPosition int
	_ = db.Model(&Countdown{}).Where("user_id = ?", userID).
		Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error
	row.Position = maxPosition + 1
	if err := db.Create(&row).Error; err != nil {
		return CountdownDTO{}, err
	}
	loc := shanghai()
	return buildCountdownDTO(row, dateOnlyUTC(time.Now(), loc), loc), nil
}

func updateCountdown(db *gorm.DB, userID, id uint, in SaveCountdownInput) (CountdownDTO, error) {
	target, err := in.normalize()
	if err != nil {
		return CountdownDTO{}, err
	}
	var row Countdown
	if err := db.Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		return CountdownDTO{}, err
	}
	row.Title = in.Title
	row.TargetDate = target
	row.RepeatYearly = in.RepeatYearly
	row.Calendar = in.Calendar
	row.Notes = in.Notes
	row.Color = in.Color
	row.LunarAnchor = ""
	if in.Calendar == "lunar" {
		anchor, err := normalizeLunarAnchor("", target)
		if err != nil {
			return CountdownDTO{}, err
		}
		row.LunarAnchor = anchor
	}
	if err := db.Save(&row).Error; err != nil {
		return CountdownDTO{}, err
	}
	loc := shanghai()
	return buildCountdownDTO(row, dateOnlyUTC(time.Now(), loc), loc), nil
}

func handleListCountdowns(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := listCountdowns(db, currentUserID(c))
		if err != nil {
			response.ErrorInternal(c, "读取纪念日失败")
			return
		}
		response.Success(c, items)
	}
}

func handleCreateCountdown(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in SaveCountdownInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "纪念日参数无效")
			return
		}
		item, err := createCountdown(db, currentUserID(c), in)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, item)
	}
}

func handleUpdateCountdown(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseID(c)
		if !ok {
			return
		}
		var in SaveCountdownInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "纪念日参数无效")
			return
		}
		item, err := updateCountdown(db, currentUserID(c), id, in)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		response.Success(c, item)
	}
}

func handleDeleteCountdown(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseID(c)
		if !ok {
			return
		}
		res := db.Where("id = ? AND user_id = ?", id, currentUserID(c)).Delete(&Countdown{})
		if res.Error != nil {
			response.ErrorInternal(c, "删除纪念日失败")
			return
		}
		if res.RowsAffected == 0 {
			response.Error(c, 404, response.CodeNotFound, "纪念日不存在")
			return
		}
		response.Success(c, gin.H{"deleted": true})
	}
}
