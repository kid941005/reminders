package version

import "time"

// startedAt 进程启动时间：前端用它区分「本次运行」与「重启后」，
// 例如赞赏提示弹窗在应用重启后才会再次出现。
var startedAt = time.Now()

var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
	AppName   = "提醒事项"
)

// AppID 是匿名统计上报使用的应用标识（应用目录名），与面向用户的
// AppName 区分：统计看板按它聚合装机量，必须与家族其他应用一致地
// 使用稳定英文标识，不能随界面语言变化。
const AppID = "reminders"

// GetVersion 返回 /api/version 的版本信息，全部字段均为字符串。
func GetVersion() map[string]string {
	return map[string]string{
		"version":   Version,
		"buildTime": BuildTime,
		"gitCommit": GitCommit,
		"appName":   AppName,
		"startedAt": startedAt.Format(time.RFC3339),
	}
}

func PrintVersion() {
	println(AppName, Version)
	println("Version:", Version)
	println("BuildTime:", BuildTime)
	println("GitCommit:", GitCommit)
}
