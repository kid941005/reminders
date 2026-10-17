# AGENTS.md — 开发约定

本文件是代码里多处注释引用的开发约定（`server/server/server.go`、`server/user/service.go`、
`server/server/server_test.go`、`web/src/stores/theme.ts`）。原始版本未随仓库提交，
本文件按现有代码行为重建，改动代码时若发现约定已变，请同步更新这里。

---

## 1. 项目边界

- 单二进制服务：Go 后端 + 内嵌 Vue 前端（`server/static/dist`），一个进程、一个端口（`8906`）。
- 数据只存本地 SQLite（WAL），不依赖外部数据库、Redis 或消息队列。
- 面向中国大陆用户，**时区语义固定为 `Asia/Shanghai`**（`main.go` 内嵌 `time/tzdata`）。
- 本仓库是 TechFunWay/reminders 的独立发行分支，发行标识见第 8 节。

## 2. 目录职责

| 目录 | 职责 |
|---|---|
| `server/apps/` | 应用注册表。新功能模块通过 `init()` 注册，不往 `server.go` 里堆代码 |
| `server/server/` | 进程启动、路由装配、中间件链、飞牛双监听器 |
| `server/reminder/` | 核心业务：清单、提醒、重复规则、投递调度、通知渠道 |
| `server/user/` `server/auth/` | 账号、角色、JWT、密码与飞牛身份绑定 |
| `server/sysconfig/` | 配置中心（系统级 + 用户级），配置项通过 `RegisterConfig` 声明 |
| `server/database/` | 建库、AutoMigrate、版本化数据迁移 |
| `server/middleware/` | 日志、CORS、限流、gzip、静态缓存、认证中间件 |
| `server/stats/` | 匿名统计与赞赏计数（本分支默认关闭） |
| `server/audit/` | 操作审计日志与导出 |
| `web/src/api/` | 所有 HTTP 调用集中在此，页面不直接写 `axios` |
| `web/src/stores/` | Pinia：`auth`、`theme`、`support` |
| `web/src/utils/` | 网关判定、启动引导、存储键、密码规则等纯逻辑 |

## 3. 后端约定

### 3.1 分层

- `handler` 只做参数解析、鉴权上下文取 `userID`、调用 service、映射错误；业务逻辑放 `service`。
- 用户态从 `currentUserID(c)` 取，**不要在 handler 里信任请求体里的 `user_id`**。
- 所有涉及用户数据的查询显式带 `user_id = ?`；跨表操作前先校验归属
  （`assertListOwner`、`ownedEmailBindingIDs`）。
- 软删除用 `DeletedAt *time.Time`，查询统一带 `deleted_at IS NULL`。

### 3.2 错误码契约

响应统一信封：`{ code, message, data }`，`code == 0` 表示成功（`server/response/response.go`）。

| code | HTTP | 含义 |
|---|---|---|
| `0` | 200 | 成功 |
| `400` / `401` / `403` / `404` / `500` | 同名 | 通用错误，与 HTTP 状态码一致 |
| `1001` | **409** | 用户名已存在 |
| `1002` | 401 | 凭证无效（**登录失败一律用它，不区分用户是否存在**） |
| `1003` | 403 | 注册功能已关闭 |

- 错误文案（`message`）**就是用户看到的文字**，中文、可直接展示，不要写英文技术术语。
- 登录失败不得区分「用户不存在」与「密码错误」，避免账号枚举（`TestLoginGenericErrorDoesNotEnumerateUsers`）。

### 3.3 新功能模块

```go
func init() {
    database.RegisterModels(&MyModel{})      // 进 AutoMigrate
    sysconfig.RegisterConfig(sysconfig.ConfigDef{...})
    apps.Register(apps.App{
        Name: "myapp", RoutePrefix: "/myapp",
        SetupAuth: func(api *gin.RouterGroup, db *gorm.DB) { ... },
    })
}
```

`main.go` 需要 blank-import 该包才会执行 `init()`。

### 3.4 数据迁移

- 加字段：靠 `AutoMigrate`，不要写迁移。
- 改存量数据语义：追加 `database.Upgrades`，`Version` 填**引入该变更的版本号**。
- `RunUpgrades` 只执行 `Version <= 当前二进制版本` 的迁移；库里记录的版本高于二进制时**拒绝启动**。
  → 因此**发版必须同步升 `VERSION`**，否则预埋迁移永远不跑。
- 迁移必须幂等（可能重跑），涉及 SQLite DDL 且无法包在事务里的用 `NoTx: true`。

### 3.5 投递任务

- 提醒不直接发送，一律先写 `DeliveryJob`，由 `scheduler` 每分钟领取。
- 幂等靠 `(reminder_id, channel, scheduled_for)` 唯一索引 + `idempotency_key`。
- 领取必须用条件 `UPDATE ... WHERE status IN ('pending','retry')` 并检查 `RowsAffected == 1`，
  **不要用进程内锁代替**（多副本部署会重复投递）。
- 失败必须分类：配置错/未绑定/参数非法 → `Permanent: true` 不重试；网络抖动 → 走退避重试。
- 修改提醒的时间、渠道、完成状态后，必须重建或取消未执行的 job，否则会发错内容。

### 3.6 时区与时间

- 存库一律 UTC（`time.Now().UTC()`），展示按 `Asia/Shanghai` 换算。
- 取时区用 `shanghai()`（失败回退 `FixedZone(+8)`），不要裸写 `time.LoadLocation` 后忽略错误。
- SQLite 把时间当文本比较，**同一条查询里不要混用本地时间与 UTC**，否则未来的提醒会被判定为已到期。

### 3.7 敏感数据

- 渠道目标（邮箱、手机号、openid、webhook）与 provider 密钥一律 AES-GCM 加密入库。
- API 只返回脱敏值（`TargetMasked`），原文不出库、不进日志。
- `ProviderConfig.Secret` 等字段用 `json:"-"` 确保不被序列化。

## 4. 前端约定

### 4.1 网关 Authorization（关键）

飞牛统一网关的接入层会把请求里的 `Authorization` 当成**它自己的**会话 token；
应用自己的 `Bearer <jwt>` 会被判为 `invalid token` 直接返回 200 纯文本，请求到不了应用。

→ **网关域上一律不带 `Authorization`**，用 `onFnOSGatewayOrigin()`（`utils/gateway.ts`）判断；
直连端口照旧带 JWT。登录态由服务端用网关注入的 `X-Trim-Userid` 解析。

违反这条的典型症状：飞牛登录后一刷新就回登录页。

### 4.2 主题三态

`useThemeStore().toggle()` 是唯一入口，循环顺序固定：

```
dark → light → system → dark
```

组件不要自己写 `mode === 'dark' ? ... : ...` 的两态逻辑，否则三态会被压成两态。

### 4.3 其他

- HTTP 调用只写在 `src/api/*.ts`，页面通过 api 模块调用。
- 浏览器存储键统一带应用前缀（`utils/storage-keys.ts`），避免飞牛网关同源下与兄弟应用互相覆盖。
- 四个智能视图复用 `HomeView.vue`，靠路由 `meta.reminderView` 区分，不要复制页面。
- 列表接口统一用 `SuccessPage` 的 `{ items, total, page, pageSize }` 形状。

## 5. 测试约定

- 后端测试用 `httptest` + 临时目录 SQLite 起完整路由（`setupTestRouter(t)`），测真实 HTTP 行为，
  不断言内部函数。
- 涉及安全的改动必须补测试：越权访问、信息泄露、路径穿越、限流、密码规则。
- 断言错误时同时校验 **HTTP 状态码与 `code` 字段**，因为前端按 `code` 分支。
- 前端目前没有测试；新增纯逻辑（`utils/`）时优先补 vitest，组件测试暂不作为门槛。

## 6. 提交规范

```
<类型>：<一句话说明>

背景：
- 为什么要改，原来有什么问题

改动内容：
- 具体改了什么，分条列
```

类型：`功能` / `修复` / `重构` / `文档` / `构建` / `测试` / `发版`。

## 7. 发版流程

1. 同步三处版本号：`VERSION`（带 `v`）、`app.json`、`fnpack/manifest`（不带 `v`）。
2. 在 `CHANGELOG.md` 顶部加版本小节；更新 `fnpack/manifest` 的 `changelog` 字段。
3. `cd server && go test ./...` 全绿；`cd web && npm run build` 通过。
4. `make package PACKAGE_TARGET=fnos|apps|docker` 产出对应包。
5. 打 tag（`vX.Y.Z`）推送，CI 会构建并推送多架构镜像。

**版本号只能升不能降**：库里记录的迁移版本高于二进制时会拒绝启动。

## 8. 本发行分支特有约束

- 发行标识：`kid941005-reminders`，镜像 `kid941005/reminders`，网关前缀 `/app/kid941005-reminders`。
  改这些要同步 `app.json`、`fnpack/manifest`、`fnpack/app/ui/config`、`fnpack/config/resource`、
  `server/config/config.go` 的默认值，以及 `server_test.go` 里的硬编码路径。
- 统计上报默认关闭：`-disable-stats` 默认 `true`，且 `stats.Endpoint()` 不内置地址，
  未配置 `STATS_ENDPOINT` 时 `stats.Start` 直接返回。
- 赞赏入口默认关闭：`web/src/stores/support.ts` 的 `SUPPORT_ENABLED = false`。
  启用前必须替换 `web/src/assets/donate-wechat.png`（当前是上游作者的收款码）。
- 与上游的关系：功能改动尽量保持可合并（避免大范围重命名业务代码），便于后续 `git merge upstream/main`。
