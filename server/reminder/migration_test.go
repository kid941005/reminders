package reminder

import (
	"testing"

	"smallgo/server/database"
)

// SQLite 给**已存在的表**新增 NOT NULL 列时，必须同时给出默认值，否则
// 启动迁移直接报 "Cannot add a NOT NULL column with default value NULL"，
// 服务起不来。全新数据库建表时不会触发这个限制，所以普通测试发现不了。
//
// 这里把新加的两列删掉再跑一次迁移，复现「老库升级」这条唯一的真实路径。
func TestAutoMigrateAddsNewColumnsToExistingTable(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	migrator := appDB.Migrator()
	columns := []string{"remind_before_minutes", "repeat_notify_times"}
	for _, column := range columns {
		if !migrator.HasColumn(&Reminder{}, column) {
			continue
		}
		if err := migrator.DropColumn(&Reminder{}, column); err != nil {
			t.Skipf("当前 SQLite 不支持 DROP COLUMN，跳过：%v", err)
		}
	}

	if err := database.AutoMigrate(appDB); err != nil {
		t.Fatalf("老库升级迁移失败：%v", err)
	}
	for _, column := range columns {
		if !migrator.HasColumn(&Reminder{}, column) {
			t.Fatalf("迁移后缺少列 %s", column)
		}
	}
}
