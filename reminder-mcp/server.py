#!/usr/bin/env python3
"""reminder-mcp

把 Reminder（待办）的 REST API 包装成小智 MCP 工具。
设备只负责语音，LLM 通过本 MCP Server 的 create_todo / complete_todo 等工具操作待办。

通信方式：stdio（由 mcp_pipe.py 或 xiaozhi-server 拉起）。
日志必须写 stderr，绝不能 print 到 stdout，否则会破坏 MCP 协议。
"""

from __future__ import annotations

import logging
import os
import sys
from datetime import datetime, timedelta
from typing import Any
from zoneinfo import ZoneInfo

import httpx
from mcp.server import MCPServer

try:  # 允许直接运行 server.py 调试；mcp_pipe.py 也会加载 .env
    from dotenv import load_dotenv

    load_dotenv()
except Exception:  # pragma: no cover
    pass


logging.basicConfig(
    stream=sys.stderr,
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
logger = logging.getLogger("reminder-mcp")

__version__ = "0.24.1"

def _env(name: str, default: str) -> str:
    return (os.getenv(name) or "").strip() or default


BASE = _env("REMINDER_API_URL", "http://127.0.0.1:8906").rstrip("/")
if BASE.endswith("/api"):
    BASE = BASE[:-4]
API_KEY = (os.getenv("REMINDER_API_KEY") or "").strip()
DEFAULT_CHANNELS = _env("REMINDER_DEFAULT_CHANNELS", "inapp")
TIMEZONE = _env("REMINDER_TIMEZONE", "Asia/Shanghai")
TZ = ZoneInfo(TIMEZONE)
try:
    TIMEOUT = float(_env("REMINDER_TIMEOUT", "8"))
except ValueError:
    TIMEOUT = 8.0

mcp = MCPServer("reminder", version=__version__)


class ReminderAPIError(RuntimeError):
    """调用 Reminder API 失败。"""


def _request(method: str, path: str, **kwargs: Any) -> Any:
    if not API_KEY:
        raise ReminderAPIError("未配置 REMINDER_API_KEY")
    url = f"{BASE}/api/reminder{path}"
    try:
        resp = httpx.request(
            method,
            url,
            headers={"X-API-Key": API_KEY},
            timeout=TIMEOUT,
            **kwargs,
        )
    except httpx.HTTPError as exc:
        logger.warning("request %s %s failed: %s", method, url, exc)
        raise ReminderAPIError("提醒服务暂时不可用") from exc

    try:
        payload = resp.json()
    except ValueError as exc:
        raise ReminderAPIError(
            f"提醒服务返回了非 JSON 响应（HTTP {resp.status_code}）"
        ) from exc

    if not isinstance(payload, dict):
        raise ReminderAPIError("提醒服务返回格式不正确")
    if payload.get("code") != 0:
        message = payload.get("message") or f"HTTP {resp.status_code}"
        if resp.status_code == 401:
            message = "提醒服务认证失败，请检查 API Key"
        elif resp.status_code == 404:
            message = "没有找到对应的待办"
        raise ReminderAPIError(str(message))
    return payload.get("data")


def _parse_due(value: str) -> datetime:
    raw = (value or "").strip()
    if not raw:
        raise ValueError("due_at 为空")
    if len(raw) == 10:  # YYYY-MM-DD
        return datetime.strptime(raw, "%Y-%m-%d").replace(tzinfo=TZ)
    if raw.endswith("Z"):
        raw = raw[:-1] + "+00:00"
    dt = datetime.fromisoformat(raw)
    if dt.tzinfo is None:
        raise ValueError("due_at 必须带时区，例如 2026-11-17T15:00:00+08:00")
    return dt


def _iso(dt: datetime) -> str:
    return dt.isoformat(timespec="seconds")


def _local(dt: datetime) -> datetime:
    return dt.astimezone(TZ)


def _format_due(value: Any, all_day: bool = False) -> str:
    if not value:
        return "未设置时间"
    try:
        local = _local(_parse_due(str(value)))
        if all_day:
            return local.strftime("%Y-%m-%d") + "（全天）"
        return local.strftime("%Y-%m-%d %H:%M")
    except Exception:
        return str(value)


def _item_view(item: dict) -> dict:
    return {
        "id": item.get("id"),
        "title": item.get("title", ""),
        "due_at": item.get("due_at"),
        "due_text": _format_due(item.get("due_at"), bool(item.get("all_day"))),
        "all_day": bool(item.get("all_day")),
        "priority": item.get("priority", 0),
        "list_name": item.get("list_name", ""),
        "completed": bool(item.get("completed_at")),
    }


def _search(query: str, view: str) -> list[dict]:
    params: dict[str, Any] = {"view": view}
    q = (query or "").strip()
    if q:
        params["q"] = q
    data = _request("GET", "/items", params=params)
    return data if isinstance(data, list) else []


def _pick(items: list[dict], query: str) -> tuple[dict | None, list[dict]]:
    q = (query or "").strip()
    if not q:
        return None, items[:5]
    exact = [i for i in items if (i.get("title") or "").strip() == q]
    if len(exact) == 1:
        return exact[0], []
    if len(items) == 1:
        return items[0], []
    return None, items[:5]


def _dedupe_create(title: str, due_at: str | None) -> dict | None:
    try:
        items = _search(title, "all")
    except ReminderAPIError:
        return None
    target_due = _parse_due(due_at) if due_at else None
    for item in items:
        if (item.get("title") or "").strip() != title.strip():
            continue
        if target_due is None:
            if not item.get("due_at"):
                return item
            continue
        try:
            current = _parse_due(str(item["due_at"]))
        except Exception:
            continue
        if abs((current - target_due).total_seconds()) <= 120:
            return item
    return None


def _channels(value: str) -> list[str]:
    raw = (value or "").strip() or DEFAULT_CHANNELS
    result: list[str] = []
    for item in raw.split(","):
        channel = item.strip()
        if channel and channel not in result:
            result.append(channel)
    return result or ["inapp"]


@mcp.tool()
def get_current_time() -> dict:
    """当用户说“今天/明天/后天/下周三”等相对时间，或需要确认当前日期时，先调用本工具。"""
    now = datetime.now(TZ)
    weekdays = "一二三四五六日"
    return {
        "iso": _iso(now),
        "date": now.strftime("%Y-%m-%d"),
        "time": now.strftime("%H:%M"),
        "weekday": "星期" + weekdays[now.weekday()],
        "timezone": str(TZ),
    }


@mcp.tool()
def list_todos(view: str = "today", query: str = "", limit: int = 10) -> dict:
    """查询待办。用户问“我今天有什么安排/还有哪些没做/完成了什么”时调用。

    view 可选：today（今天及逾期未完成）、planned（所有有日期的未完成）、
    completed（已完成）、all（全部未完成，含没有日期的）。
    """
    allowed = {"today", "planned", "completed", "all"}
    if view not in allowed:
        view = "today"
    try:
        items = _search(query, view)
    except ReminderAPIError as exc:
        return {"ok": False, "error": str(exc)}

    limit = max(1, min(int(limit or 10), 20))
    result = [_item_view(i) for i in items[:limit]]
    if not result:
        speech = "没有找到符合条件的待办"
    else:
        speech = f"找到{len(result)}条待办：" + "；".join(
            f"{i['title']}（{i['due_text']}）" for i in result[:3]
        )
    return {
        "ok": True,
        "count": len(result),
        "items": result,
        "speech": speech,
        "now": _iso(datetime.now(TZ)),
    }


@mcp.tool()
def create_todo(
    title: str,
    due_at: str = "",
    all_day: bool = False,
    notes: str = "",
    priority: int = 0,
    channels: str = "",
) -> dict:
    """新增待办/提醒。用户说“提醒我/记一下/帮我加个待办”时调用。

    due_at 必须是带时区的 ISO 8601，例如 2026-11-17T15:00:00+08:00；
    用户没说时间就留空。all_day=true 时 due_at 可以只给日期 YYYY-MM-DD。
    channels 为空时使用 REMINDER_DEFAULT_CHANNELS（默认 inapp）。
    """
    title = (title or "").strip()
    if not title:
        return {"ok": False, "error": "待办标题不能为空"}
    if len(title) > 200:
        return {"ok": False, "error": "待办标题不能超过 200 个字符"}

    due_at = (due_at or "").strip()
    if due_at:
        try:
            parsed = _parse_due(due_at)
        except Exception as exc:
            return {"ok": False, "error": f"时间格式不正确：{exc}"}
        now = datetime.now(TZ)
        if all_day:
            # 全天待办按本地日期处理，允许今天已经过了零点再补记。
            parsed = parsed.astimezone(TZ).replace(hour=0, minute=0, second=0, microsecond=0)
            if parsed.date() < now.date():
                return {"ok": False, "error": "提醒日期已经过去了，请确认日期"}
        elif parsed < now - timedelta(minutes=1):
            return {"ok": False, "error": "提醒时间已经过去了，请确认时间"}
        due_at = _iso(parsed)

    existing = _dedupe_create(title, due_at or None)
    if existing:
        return {
            "ok": True,
            "deduplicated": True,
            "item": _item_view(existing),
            "speech": f"这条待办已经存在：{existing.get('title')}",
        }

    payload: dict[str, Any] = {
        "title": title,
        "notes": (notes or "").strip(),
        "all_day": bool(all_day),
        "priority": max(0, min(int(priority or 0), 3)),
        "channels": _channels(channels),
    }
    if due_at:
        payload["due_at"] = due_at

    try:
        item = _request("POST", "/items", json=payload)
    except ReminderAPIError as exc:
        return {"ok": False, "error": str(exc)}
    if not isinstance(item, dict):
        return {"ok": False, "error": "提醒服务返回格式不正确"}

    logger.info("created reminder id=%s due=%s", item.get("id"), due_at or "-")
    return {
        "ok": True,
        "item": _item_view(item),
        "speech": f"已创建：{title}，{_format_due(item.get('due_at'), bool(item.get('all_day')))}",
    }


@mcp.tool()
def complete_todo(query: str = "", todo_id: int = 0) -> dict:
    """完成待办。用户说“完成了/做完了/搞定/划掉”时调用。

    优先用 query 按标题查找；只有唯一匹配才直接完成，多条返回 candidates
    让用户确认。只有用户明确给出 todo_id 时才直接按 ID 完成。
    """
    if not todo_id:
        q = (query or "").strip()
        if not q:
            return {"ok": False, "error": "请告诉我要完成哪一条待办"}
        try:
            items = _search(q, "planned")
            if not items:
                items = _search(q, "all")
        except ReminderAPIError as exc:
            return {"ok": False, "error": str(exc)}
        if not items:
            return {"ok": False, "error": "没有找到符合条件的待办"}

        item, candidates = _pick(items, q)
        if item is None:
            return {
                "ok": False,
                "need_choice": True,
                "candidates": [_item_view(i) for i in candidates],
                "speech": "找到多条，请确认要完成哪一条",
            }
        todo_id = int(item["id"])

    try:
        item = _request("POST", f"/items/{todo_id}/complete")
    except ReminderAPIError as exc:
        return {"ok": False, "error": str(exc)}
    if not isinstance(item, dict):
        return {"ok": False, "error": "提醒服务返回格式不正确"}

    speech = f"已完成：{item.get('title')}"
    if item.get("due_at"):
        speech += f"，下次：{_format_due(item.get('due_at'), bool(item.get('all_day')))}"
    return {"ok": True, "item": _item_view(item), "speech": speech}


@mcp.tool()
def snooze_todo(query: str = "", todo_id: int = 0, until: str = "") -> dict:
    """稍后提醒。用户说“晚点再提醒我/推迟到/过半小时再叫我”时调用。

    until 必须是带时区的 ISO 8601，例如 2026-11-17T19:00:00+08:00。
    """
    if not until:
        return {"ok": False, "error": "请提供稍后提醒的时间"}
    try:
        when = _parse_due(until)
    except Exception as exc:
        return {"ok": False, "error": f"时间格式不正确：{exc}"}
    if when < datetime.now(TZ) - timedelta(minutes=1):
        return {"ok": False, "error": "稍后提醒时间已经过去了"}

    if not todo_id:
        q = (query or "").strip()
        if not q:
            return {"ok": False, "error": "请告诉我要推迟哪一条待办"}
        try:
            items = _search(q, "planned")
            if not items:
                items = _search(q, "all")
        except ReminderAPIError as exc:
            return {"ok": False, "error": str(exc)}
        if not items:
            return {"ok": False, "error": "没有找到符合条件的待办"}

        item, candidates = _pick(items, q)
        if item is None:
            return {
                "ok": False,
                "need_choice": True,
                "candidates": [_item_view(i) for i in candidates],
                "speech": "找到多条，请确认要推迟哪一条",
            }
        todo_id = int(item["id"])

    try:
        item = _request("POST", f"/items/{todo_id}/snooze", json={"until": _iso(when)})
    except ReminderAPIError as exc:
        return {"ok": False, "error": str(exc)}
    if not isinstance(item, dict):
        return {"ok": False, "error": "提醒服务返回格式不正确"}

    return {
        "ok": True,
        "item": _item_view(item),
        "speech": f"已推迟：{item.get('title')}，{_format_due(item.get('due_at'), bool(item.get('all_day')))}",
    }


@mcp.tool()
def get_weather(city: str = "", days: int = 3) -> dict:
    """查询和风天气（实况、未来几天预报和气象预警）。用户问天气时调用。

    city 为空时使用 Reminder「天气提醒」里配置的城市；也可以直接传城市名，
    例如“杭州”“北京朝阳”。days 为 1 到 7，默认 3。
    """
    city = (city or "").strip()
    if len(city) > 60:
        return {"ok": False, "error": "城市名称过长"}
    try:
        days = int(days or 3)
    except (TypeError, ValueError):
        days = 3
    days = max(1, min(days, 7))

    params: dict[str, Any] = {"days": days}
    if city:
        params["city"] = city
    try:
        data = _request("GET", "/weather/query", params=params)
    except ReminderAPIError as exc:
        return {"ok": False, "error": str(exc)}
    if not isinstance(data, dict):
        return {"ok": False, "error": "提醒服务返回格式不正确"}

    summary = str(data.get("summary") or "").strip()
    return {
        "ok": True,
        "location": data.get("location") or {},
        "current": data.get("current") or {},
        "days": data.get("days") or [],
        "alerts": data.get("alerts") or [],
        "title": data.get("title") or "",
        "speech": summary or "暂时没有天气数据",
    }


if __name__ == "__main__":
    if not API_KEY:
        logger.error("REMINDER_API_KEY 未设置，工具调用会失败")
    logger.info("starting reminder-mcp, api=%s", BASE)
    mcp.run(transport="stdio")
