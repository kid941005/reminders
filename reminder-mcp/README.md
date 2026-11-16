# reminder-mcp

把 Reminder 的 REST API 包装成小智（ESP32）可用的 MCP 工具，让设备通过语音新增、查询、完成和推迟待办。

```text
ESP32 小智 --语音--> 小智服务端 / LLM --MCP(WebSocket)--> mcp_pipe.py
                                                          |
                                                          | stdio
                                                          v
                                                     server.py
                                                          |
                                                          | HTTP + X-API-Key
                                                          v
                                                   Reminder 后端 (8906)
```

- **ESP32 固件不用改**，设备只负责拾音和播报。
- **Reminder 后端不用改**，本目录只做 MCP 协议适配。
- `server.py` 暴露 5 个工具：`get_current_time`、`list_todos`、`create_todo`、`complete_todo`、`snooze_todo`。
- `mcp_pipe.py` 负责 stdio ↔ WebSocket，把 MCP Server 接到小智的 MCP 接入点。

## 目录

| 文件 | 说明 |
|---|---|
| `server.py` | MCP Server，调用 Reminder REST API |
| `mcp_pipe.py` | 小智官方 stdio ↔ WebSocket 管道 |
| `mcp_config.json` | mcp_pipe 拉起的子进程配置 |
| `Dockerfile` | 适配器镜像 |
| `test_server.py` | 纯逻辑测试，不访问网络 |
| `.env.example` | 配置模板 |

## 使用 Docker Compose（推荐）

仓库根目录的 `docker-compose.yml` 已包含一个 **默认不启动** 的 `reminder-mcp` 服务（profile `mcp`）。

镜像由 GitHub Actions 构建并推送到 `ghcr.io/kid941005/reminders-mcp:latest`，
不再在本地执行 `--build`。如果 GHCR 包仍是私有，拉取前需要先 `docker login ghcr.io`。

1. 准备配置：

```bash
cp reminder-mcp/.env.example reminder-mcp/.env
```

编辑 `reminder-mcp/.env`：

```ini
# 同一 compose 网络内用服务名；本机直接运行 server.py 时改成 127.0.0.1
REMINDER_API_URL=http://kid941005-reminders:8906

# Reminder「个人设置」页生成的 API Key
REMINDER_API_KEY=你的APIKey

# 小智 MCP 接入点，从智控台复制
MCP_ENDPOINT=ws://192.168.50.100:38004/mcp_endpoint/mcp/?token=你的token
```

2. 拉取镜像并启动 Reminder + 适配器：

```bash
docker compose --profile mcp pull
docker compose --profile mcp up -d
```

只启动 Reminder 时仍用原来的命令，不受影响：

```bash
docker compose up -d
```

3. 看日志确认已连上小智：

```bash
docker logs -f reminder-mcp
```

### 镜像构建

`.github/workflows/mcp-image.yml` 在 `reminder-mcp/**` 变更、推送 `v*` 标签或手动触发时，
构建并推送 `ghcr.io/kid941005/reminders-mcp`。主分支构建 `latest` 与 `sha-*`，
版本标签构建 `x.y.z`、`x.y` 与 `latest`。

## 配置说明

| 变量 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `REMINDER_API_URL` | 是 | `http://127.0.0.1:8906` | Reminder 地址；Docker 内用服务名或宿主机 IP |
| `REMINDER_API_KEY` | 是 | — | Reminder 个人设置页生成，等于账号全权凭证 |
| `MCP_ENDPOINT` | 是 | — | 小智 MCP 接入点，`ws://` 或 `wss://` |
| `REMINDER_DEFAULT_CHANNELS` | 否 | `inapp` | 新建待办的默认渠道，逗号分隔 |
| `REMINDER_TIMEZONE` | 否 | `Asia/Shanghai` | 解析和展示时间用的时区 |
| `REMINDER_TIMEOUT` | 否 | `8` | 调用 Reminder API 的超时秒数 |

### 两个地址不要混

- **mcp_pipe 连接地址**：`ws://<IP>:<端口>/mcp_endpoint/mcp/?token=...`
- **小智服务端模块注册地址**：`http://<IP>:<端口>/mcp_endpoint/health?key=...`

如果 `mcp-endpoint-server` 和 Reminder 在同一个 compose，`MCP_ENDPOINT` 可以用
`ws://mcp-endpoint-server:8004/mcp_endpoint/mcp/?token=...`；如果分开部署，用宿主机局域网 IP
和映射出来的端口（例如 `38004`）。

## 本机调试

```powershell
cd reminder-mcp
python -m venv .venv
.\.venv\Scripts\Activate.ps1
pip install -r requirements.txt

# 把 .env.example 复制成 .env，REMINDER_API_URL 改成 http://127.0.0.1:8906
python mcp_pipe.py
```

`mcp_config.json` 里的 `command` 是 `python`，所以本机运行前要先激活虚拟环境，
否则子进程会用到系统 Python 而找不到 `fastmcp`。

纯逻辑测试不访问网络：

```bash
python -m unittest -v test_server.py
```

## 工具行为

| 工具 | 作用 | 说明 |
|---|---|---|
| `get_current_time` | 当前日期、时间、星期 | 用户说“明天/下周三”时先调用 |
| `list_todos` | 查询今天/计划/已完成/全部 | `view` 默认 `today` |
| `create_todo` | 新增待办 | 标题 ≤ 200 字；过去时间拒绝；同标题 + 同时间（±2 分钟）去重 |
| `complete_todo` | 完成待办 | 按标题搜索，唯一匹配才完成；多条返回候选让用户确认 |
| `snooze_todo` | 推迟提醒 | `until` 必须是带时区的 ISO 8601 |

`due_at` 示例：`2026-11-17T15:00:00+08:00`。只给日期 `2026-11-17` 时按当天 00:00（全天）处理。

## 安全与限制

- API Key 只放在 `.env`，不要写进固件、Prompt 或 Git。
- 一个 API Key 对应一个 Reminder 用户，不要多人共用。
- 只暴露新增、查询、完成、推迟；不暴露删除和批量完成，避免语音误操作。
- 语音识别可能把标题听错，完成多条匹配时工具会返回候选，不会直接猜。
- 钉钉日程等只读来源的条目也能被完成，但不会回写来源日历。

## 常见问题

**启动报 `ZoneInfoNotFoundError: No time zone found with key Asia/Shanghai`**
Windows 自带 Python 缺少 IANA 时区库，`requirements.txt` 已包含 `tzdata`，重新
`pip install -r requirements.txt` 即可。

**容器日志提示连接失败**
检查 `MCP_ENDPOINT` 是否带 `token`、端口是否是 mcp-endpoint-server 映射到宿主机的端口，
以及容器能否访问该地址。

**工具调用返回“提醒服务认证失败”**
重新生成 API Key 并更新 `REMINDER_API_KEY`；重置后旧 Key 立即失效。

**创建成功但设备没有播报**
工具返回的 `speech` 是给小智播报的文本；如果设备没反应，检查小智控制台里该智能体是否
挂载了本 MCP 接入点，以及 mcp_pipe 日志是否显示工具已注册。
