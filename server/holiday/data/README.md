# 节假日数据

`chinese-days.json` 来自 [vsme/chinese-days](https://github.com/vsme/chinese-days)，
MIT 许可。数据与国务院办公厅每年的《部分节假日安排的通知》逐条对应。

## 文件结构

```json
{
  "holidays":  { "2026-01-01": "New Year's Day,元旦,1" },
  "workdays":  { "2026-01-04": "New Year's Day,元旦,1" },
  "inLieuDays":{ "2026-01-02": "New Year's Day,元旦,1" }
}
```

- `holidays`：放假日，`off = true`
- `workdays`：调休补班日，`off = false`
- `inLieuDays`：调休来源标记，本应用不使用

每条的取值是 `英文名,中文名,类型码`，解析时取第二段作为中文名。

## 更新方式

`.github/workflows/holidays.yml` 每月自动拉取上游最新 JSON，有变化时提 PR。
人工只需要审阅合并，不必手工维护数据。

上游通常在**每年 11 月初**发布次年的安排。在此之前的年份不在数据范围内，
接口会通过 `min_year` / `max_year` 告知前端「尚未覆盖」，
前端不应把未覆盖的日期当作普通工作日。

## 手工更新

```bash
curl -sL https://cdn.jsdelivr.net/npm/chinese-days/dist/chinese-days.json \
  -o server/holiday/data/chinese-days.json
```

## 用户覆盖

如需覆盖（例如公司有特殊排班），在数据目录放置 `holidays.json`，
格式与本文件一致，启动时叠加、以覆盖文件为准：

```
<DATA_DIR>/holidays.json
```
