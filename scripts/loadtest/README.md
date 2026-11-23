# 压测脚本（k6）

用 [k6](https://k6.io/) 对提醒 API 做基线压测，先量化瓶颈再决定是否需要更重的重构（例如换语言）。

## 前置

- 服务已启动，且已创建 API Key（登录后在个人设置生成）。
- 本机安装 k6（Windows: `winget install k6`；macOS: `brew install k6`）。

## 运行

```bash
k6 run \
  -e BASE_URL=http://127.0.0.1:8906 \
  -e API_KEY=<your-api-key> \
  scripts/loadtest/reminder.js
```

可用参数：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| BASE_URL | http://127.0.0.1:8906 | 服务地址 |
| API_KEY | 空 | 必填，否则读接口会 401 |
| WRITE_RATIO | 0.1 | 每次迭代创建提醒的概率 |

## 指标

脚本把读、写、业务失败分开统计：

- `read_latency`：`/api/health`、`/api/reminder/summary`、`/api/reminder/items` 的 p95 应 < 300ms。
- `write_latency`：创建提醒的 p95 应 < 800ms。
- `http_req_failed` < 1%，`business_failures` < 1%。

## 说明

- 写路径会真实写入 SQLite，压测后请清理测试数据（或使用独立的 data-dir）。
- 默认不勾选通知渠道（`channels: []`），因此不会触发真实推送；若要压测投递链路，需要另行绑定测试渠道并承担外部限流。
- 服务端连接池默认 `MaxOpenConns=4`、`busy_timeout=10s`；若压测出现 `SQLITE_BUSY`，先看写并发与事务长度，而不是盲目加连接数。
