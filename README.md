# Reminder（提醒事项）

面向中国大陆用户的轻量多渠道提醒应用。界面借鉴移动端提醒工具的低学习成本，支持清单、
今天/计划/全部/已完成视图、重复提醒、稍后提醒，以及站内、邮件、短信、飞书、QQ、钉钉和 Bark 通知。

版本功能与修复记录见 [CHANGELOG.md](./CHANGELOG.md)。

> **关于本仓库**：这是 [TechFunWay/reminders](https://github.com/TechFunWay/reminders) 的独立发行分支，
> 基于上游 v0.3.0（MIT 许可）。本分支自行维护发行标识、版本号与隐私默认值，
> 功能层面的改动见 CHANGELOG。上游项目与其作者不对本分支的发行物负责。
>
> **部署形态：仅支持 Docker。** 本分支已移除飞牛 fnOS 集成（网关免登、应用包、安装向导），
> 只保留标准的 TCP 监听与用户名密码登录。

## 下载与安装

| 渠道 | 获取方式 |
|---|---|
| Docker 镜像 | `docker pull ghcr.io/kid941005/reminders:latest`（amd64 / arm64 多平台） |
| GitHub Releases | <https://github.com/kid941005/reminders/releases> —— 各平台压缩包与 `docker-compose.yml` |

> 镜像托管在 GitHub Container Registry。**首次发布后包默认是私有的**，需要到
> 仓库右侧 Packages → `reminders` → Package settings 里把可见性改为 Public，
> 匿名用户才能拉取；私有状态下需先 `docker login ghcr.io` 并用自己的 GitHub
> token 认证。镜像由 tag 触发的 CI 自动构建，支持 `linux/amd64` 与 `linux/arm64`。

快速启动：

```bash
docker run -d --name reminders -p 8906:8906 \
  -v reminders-data:/app/data \
  ghcr.io/kid941005/reminders:latest
```

或使用仓库自带的 compose：

```bash
docker compose up -d
```

> 默认端口 `8906`；数据默认是挂载目录下的 SQLite 单文件，备份即拷贝，恢复支持上传本地备份文件。
> 首次启动访问 `http://<主机>:8906`，注册的第一个账号自动成为管理员。
>
> **从上游飞牛版本迁移**：本分支不再提供飞牛应用包，也无法在飞牛应用中心安装。
> 原飞牛部署里的 SQLite 数据文件（`<data-dir>/db/reminder.db`）格式未变，可复制到 Docker 数据卷继续使用；
> 飞牛账号绑定关系已随功能移除，迁移后需用原应用账号密码登录。

## 界面预览

| PC 端：计划视图 | PC 端：通知方式 |
| --- | --- |
| ![PC 端计划视图](images/screenshots/reminders-pc-planned.jpg) | ![PC 端通知方式](images/screenshots/reminders-pc-channels.jpg) |

| 手机端：计划视图 | 手机端：通知方式 |
| --- | --- |
| ![手机端计划视图](images/screenshots/reminders-mobile-planned.jpg) | ![手机端通知方式](images/screenshots/reminders-mobile-channels.jpg) |

## 技术栈

- Go + Gin + GORM + SQLite（WAL）
- Vue 3 + TypeScript + Pinia + Vue Router
- Tailwind CSS + Vite

## 本地开发

要求 Go 1.26+、Node.js 20+、CGO 和本地 C 编译器。

```bash
# 构建 Vue 前端、复制到后端静态目录、构建并启动 Go 后端
make dev
```

浏览器访问 `http://localhost:8906`。开发模式只启动 Go 后端一个服务，API 和前端页面
共用 8906 端口；首次运行会自动安装前端依赖。可通过 `PORT=9000 make dev` 临时改端口。

需要邮件、短信、飞书或 QQ 时，先复制 `.env.example` 为 `.env`，填入相应服务端凭证，再运行
`make dev`。`.env` 会在本地启动时自动读取，且不会覆盖已经设置的系统环境变量。

仅构建或启动已构建产物：

```bash
make build
make start
```

首次注册的用户自动成为管理员。

## 节假日数据

法定节假日与调休补班的数据来自 [vsme/chinese-days](https://github.com/vsme/chinese-days)（MIT），
随二进制通过 `go:embed` 内置，运行时不访问任何外部接口。

国务院通常在**每年 11 月初**发布次年安排，因此：

- `.github/workflows/holidays.yml` 每月检查上游数据、有变更自动开 PR；
- 遇到次年公告尚未发布时，界面会提示「XXXX 年放假安排尚未公布」，**不会**把未覆盖的日期当作普通工作日；
- 如需覆盖（公司特殊排班），在数据目录放置 `holidays.json`（格式见 `server/holiday/data/README.md`），启动时叠加生效。

## 核心能力

- 默认清单和自定义清单；
- 今天、计划、本周、本月、全部、已完成智能视图；
- 快捷新增、编辑、删除、完成、恢复；
- 每天、每周、每月、每年重复；
- 稍后 10 分钟提醒；
- 提前提醒（默认提前 15 分钟，全天提醒按整天计算）与过期提醒次数上限；
- 持久化投递任务、幂等领取、失败分类和最多三次重试；
- 站内通知中心；
- SMTP 邮件；
- 可配置短信 HTTP 网关；
- 飞书企业自建应用机器人单聊；
- QQ 机器人主动单聊；
- 钉钉个人群自定义机器人（支持自定义关键词与加签两种安全设置）；
- Bark 推送到 iPhone（支持官方服务与自建服务器）；
- 倒数纪念日：生日、纪念日、考试倒计时，支持每年重复与农历；
- 日历订阅（ICS）：把提醒同步到系统日历、Google Calendar 或 Outlook；
- 法定节假日与调休：日历上直接标出「休 / 班」，数据随二进制内置，离线可用；
- 浅色/深色主题和响应式手机界面。

## 外部渠道配置

```text
# 邮件
SMTP_HOST
SMTP_PORT
SMTP_USERNAME
SMTP_PASSWORD
SMTP_FROM_NAME
SMTP_FROM_ADDRESS

# 短信 HTTP 网关
SMS_WEBHOOK_URL
SMS_WEBHOOK_TOKEN

# 飞书
FEISHU_APP_ID
FEISHU_APP_SECRET
FEISHU_BASE_URL

# QQ
QQ_BOT_APP_ID
QQ_BOT_APP_SECRET
QQ_BOT_API_BASE

# 可选：单独的数据加密密钥
REMINDER_DATA_KEY
```

渠道接收目标在数据库中使用 AES-GCM 加密。未设置 `REMINDER_DATA_KEY` 时，会从安装实例的
内部 JWT 密钥派生独立加密密钥。

## 匿名统计与赞赏

本发行版**默认不向任何服务器上报数据**，也不展示赞赏入口。

- **统计默认关闭**：`-disable-stats` 默认值为 `true`，且代码内不内置上报端点。
  只有同时满足「显式开启统计」与「配置 `STATS_ENDPOINT`」两个条件才会发出请求，
  否则 `stats.Start` 直接返回，进程不产生任何对外连接。
- **赞赏入口默认关闭**：上游版本内置的微信收款码属于原作者，本发行版不再显示启动弹窗、
  顶部横幅与偏好设置卡片（开关在 `web/src/stores/support.ts` 的 `SUPPORT_ENABLED`）。
  要为自己的发行版启用，需替换 `web/src/assets/donate-wechat.png` 并准备好接收端。
- **如需自行接入统计**：设置 `STATS_ENDPOINT`，并用 `-disable-stats=false` 或
  `DISABLE_STATS=0` 开启；上报内容仅为哈希后的设备标识、操作系统、架构、部署形态与版本号，
  不含用户名、主机名、提醒内容或 IP 明文。管理员也可在「系统配置」中用 `stats_enabled` 运行时开关。

`DEVICE_TYPE`（`-device-type`）仅用于标识部署形态（`docker`），在未启用统计时不起作用。

### 钉钉个人提醒

无需创建钉钉应用，也无需配置服务器环境变量。请使用 Windows 或 Mac 的钉钉电脑端操作；手机端不能
创建或配置此机器人：

1. 新建一个专用提醒群，或打开一个现有群。
2. 如果不希望其他人看到提醒，请让群内只保留自己：可新建个人群，或将其他成员移出。
3. 打开群设置，进入“群机器人 / 机器人”，添加“自定义机器人”。
4. 安全设置二选一：
   - **自定义关键词**：填写 `提醒`。应用发出的每条消息都固定包含这两个字，无需额外配置。
   - **加签**：复制密钥（以 `SEC` 开头）。在应用里连同 Webhook 一起填写，服务端会为每次请求
     计算 `timestamp` 与 `sign` 签名。
5. 在应用的“通知方式 → 钉钉群机器人”粘贴 Webhook（加签模式再填密钥），并发送测试。

提醒会发送给该群的所有当前成员；群内只有你一人时，只有你能收到。每位用户的 Webhook 与加签密钥
独立加密保存；应用只接受钉钉官方机器人地址。

### Bark 推送（iPhone）

Bark 是一个开源的 iOS 推送客户端，通过一个带设备 Key 的 URL 接收通知，不需要 Apple 开发者账号：

1. 在 iPhone 上安装 Bark 并打开，首页会显示一条形如 `https://api.day.app/AbCdEfGhijk` 的推送地址。
2. 在应用的“通知方式 → Bark”粘贴这条完整地址，发送测试。
3. 自建服务器同样支持：填你自己的域名，形如 `https://bark.example.com/你的设备Key`。

地址请只填到设备 Key 为止，不要带查询参数或子路径。设备 Key 与 Webhook 一样加密保存，页面只显示
主机名与 Key 末四位。推送会带上 `group=提醒事项` 并归档到 Bark 的历史记录。

## 运行日志

运行日志默认仅写入 `<data-dir>/logs/`，按日期和级别分为 `info-YYYY-MM-DD.log`、
`warn-YYYY-MM-DD.log`、`error-YYYY-MM-DD.log` 与 `audit-YYYY-MM-DD.log`，默认保留 30 天。
这避免通知通道或后台任务的重复错误刷屏终端；需要排查时可直接查看对应文件。仅本地调试时可使用
`-log-console` 或 `LOG_CONSOLE=true` 同时输出到终端。

## 构建与测试

```bash
cd server && go test ./...
cd web && npm run build
make build
```

### 发布打包

```bash
# 所有桌面/服务器平台压缩包（默认）
make package PACKAGE_TARGET=apps

# 仅构建本地 Docker 镜像（按需执行）
make package PACKAGE_TARGET=docker
```

打 tag 推送后，CI 会自动构建 amd64/arm64 多架构镜像并创建 Release，无需本地执行上述命令。

开发约定、目录职责与提交规范见 [AGENTS.md](./AGENTS.md)。

## 致谢与许可

本项目基于 [TechFunWay/reminders](https://github.com/TechFunWay/reminders)（MIT 许可）二次发行，
原始版权归其作者所有。本分支的改动与发行物由本仓库维护者负责，与上游项目无关。

完整许可条款见 [LICENSE](./LICENSE)。
