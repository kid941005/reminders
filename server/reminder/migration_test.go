package reminder

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"smallgo/server/database"
)

// SQLite 给**已存在的表**新增 NOT NULL 列时，必须同时给出默认值，否则
// 启动迁移直接报 "Cannot add a NOT NULL column with default value NULL"，
// 服务起不来。全新数据库建表时不受这个限制，因此普通测试发现不了——
// v0.11.0 就是这样把线上老库迁崩的。
//
// 两层保障：
//  1. 断言这两个字段的 gorm tag 必须带 default（确定性检查，任何环境都会跑）；
//  2. 删掉列后重跑迁移，复现真实的 ALTER TABLE 路径（依赖 SQLite 支持 DROP COLUMN）。
func TestNewColumnsAreSafeToAddToExistingTable(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	newColumns := []string{"remind_before_minutes", "repeat_notify_times"}

	stmt := &gorm.Statement{DB: appDB}
	if err := stmt.Parse(&Reminder{}); err != nil {
		t.Fatal(err)
	}
	for _, field := range stmt.Schema.Fields {
		if !containsString(newColumns, field.DBName) {
			continue
		}
		tag := field.Tag.Get("gorm")
		if strings.Contains(tag, "not null") && !strings.Contains(tag, "default:") {
			t.Fatalf("%s 是 NOT NULL 但没有 default，老库升级会失败（tag=%q）", field.DBName, tag)
		}
	}

	migrator := appDB.Migrator()
	dropped := 0
	for _, column := range newColumns {
		if !migrator.HasColumn(&Reminder{}, column) {
			continue
		}
		if err := migrator.DropColumn(&Reminder{}, column); err != nil {
			t.Logf("跳过端到端部分（不支持 DROP COLUMN）：%v", err)
			return
		}
		dropped++
	}
	if dropped == 0 {
		return
	}

	if err := database.AutoMigrate(appDB); err != nil {
		t.Fatalf("老库升级迁移失败：%v", err)
	}
	for _, column := range newColumns {
		if !migrator.HasColumn(&Reminder{}, column) {
			t.Fatalf("迁移后缺少列 %s", column)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
