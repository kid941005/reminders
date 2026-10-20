// Package holiday 提供中国大陆法定节假日与调休补班的查询。
//
// 数据随二进制内置（go:embed），不依赖运行时外部接口：自托管部署不应该
// 因为第三方服务不可用而丢失「今天上不上班」这种基础判断。数据来源为
// vsme/chinese-days（MIT），与国务院公告逐条对应，更新方式见 data/README.md。
package holiday

import (
	"embed"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/chinese-days.json
var builtinData embed.FS

const builtinPath = "data/chinese-days.json"

// Day 描述某一天的节假日属性。
type Day struct {
	// Name 是节日名，例如「国庆节」。
	Name string `json:"name"`
	// Off 为 true 表示放假，false 表示调休上班（补班）。
	Off bool `json:"off"`
}

// RangeResult 是一段日期区间的查询结果。
type RangeResult struct {
	Days map[string]Day `json:"days"`
	// MinYear/MaxYear 是内置数据实际覆盖的年份范围。前端据此判断「这一年
	// 还没有公告数据」，避免把未覆盖的年份误当成普通工作日。
	MinYear int `json:"min_year"`
	MaxYear int `json:"max_year"`
}

type rawFile struct {
	Holidays map[string]string `json:"holidays"`
	Workdays map[string]string `json:"workdays"`
}

// Dataset 是只读的节假日索引。
type Dataset struct {
	days    map[string]Day
	minYear int
	maxYear int
}

var (
	loadOnce sync.Once
	loaded   *Dataset
	loadErr  error
	mu       sync.RWMutex
	current  *Dataset
)

// parseName 从 "New Year's Day,元旦,1" 里取出中文名。
// 缺少分隔符时退化为整串，保证不会因为格式微调就丢掉名称。
func parseName(raw string) string {
	parts := strings.Split(raw, ",")
	if len(parts) >= 2 {
		if name := strings.TrimSpace(parts[1]); name != "" {
			return name
		}
	}
	return strings.TrimSpace(parts[0])
}

func parse(data []byte) (*Dataset, error) {
	var file rawFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	ds := &Dataset{days: make(map[string]Day, len(file.Holidays)+len(file.Workdays))}
	for date, raw := range file.Holidays {
		ds.days[date] = Day{Name: parseName(raw), Off: true}
	}
	// 补班日写在后面：同一天若同时出现在两个集合里，以「上班」为准，
	// 因为放假安排不会把补班日也标成休息。
	for date, raw := range file.Workdays {
		ds.days[date] = Day{Name: parseName(raw), Off: false}
	}
	ds.recomputeYears()
	return ds, nil
}

func (ds *Dataset) recomputeYears() {
	ds.minYear, ds.maxYear = 0, 0
	for date := range ds.days {
		// 覆盖文件由用户提供，可能含畸形键；短于 10 位或年份非数字时跳过，
		// 不能让一条脏数据把整个启动流程 panic 掉。
		if len(date) < 10 {
			continue
		}
		year, err := strconv.Atoi(date[:4])
		if err != nil {
			continue
		}
		if ds.minYear == 0 || year < ds.minYear {
			ds.minYear = year
		}
		if year > ds.maxYear {
			ds.maxYear = year
		}
	}
}

// merge 把覆盖数据叠加到当前数据集上（覆盖优先）。
func (ds *Dataset) merge(other *Dataset) {
	for date, day := range other.days {
		ds.days[date] = day
	}
	ds.recomputeYears()
}

func builtin() (*Dataset, error) {
	loadOnce.Do(func() {
		data, err := builtinData.ReadFile(builtinPath)
		if err != nil {
			loadErr = err
			return
		}
		loaded, loadErr = parse(data)
	})
	return loaded, loadErr
}

// Load 载入内置数据，并在 overridePath 存在时叠加用户覆盖。
//
// 覆盖文件格式与内置数据一致：{"holidays":{...},"workdays":{...}}。
// 文件缺失或解析失败都不阻断启动——节假日是辅助信息，不该让服务起不来。
func Load(overridePath string) error {
	base, err := builtin()
	if err != nil {
		return err
	}
	ds := &Dataset{
		days:    make(map[string]Day, len(base.days)),
		minYear: base.minYear,
		maxYear: base.maxYear,
	}
	for date, day := range base.days {
		ds.days[date] = day
	}
	if overridePath != "" {
		if data, err := os.ReadFile(overridePath); err == nil {
			if override, err := parse(data); err == nil {
				ds.merge(override)
			}
		}
	}
	mu.Lock()
	current = ds
	mu.Unlock()
	return nil
}

// Default 返回当前数据集；未调用过 Load 时退回内置数据。
func Default() *Dataset {
	mu.RLock()
	ds := current
	mu.RUnlock()
	if ds != nil {
		return ds
	}
	if base, err := builtin(); err == nil {
		return base
	}
	return &Dataset{days: map[string]Day{}}
}

// Lookup 查询某一天（YYYY-MM-DD）。第二个返回值表示是否命中数据。
func (ds *Dataset) Lookup(date string) (Day, bool) {
	day, ok := ds.days[date]
	return day, ok
}

// Years 返回内置数据覆盖的年份范围。
func (ds *Dataset) Years() (int, int) {
	return ds.minYear, ds.maxYear
}

// Range 返回 [from, to) 内的节假日，日期格式为 YYYY-MM-DD。
func (ds *Dataset) Range(from, to string) RangeResult {
	result := RangeResult{
		Days:    make(map[string]Day),
		MinYear: ds.minYear,
		MaxYear: ds.maxYear,
	}
	if from == "" || to == "" {
		return result
	}
	for date, day := range ds.days {
		if date >= from && date < to {
			result.Days[date] = day
		}
	}
	return result
}
