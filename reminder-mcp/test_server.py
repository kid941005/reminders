"""reminder-mcp 纯逻辑测试。

运行：
    python -m unittest -v test_server.py

不访问网络，也不要求配置 REMINDER_API_KEY。
"""

from __future__ import annotations

import unittest
from datetime import datetime
from unittest import mock

import server


class ParseDueTest(unittest.TestCase):
    def test_date_only_uses_local_timezone(self) -> None:
        parsed = server._parse_due("2026-11-17")
        self.assertEqual(parsed.isoformat(), "2026-11-17T00:00:00+08:00")

    def test_z_suffix_is_utc(self) -> None:
        parsed = server._parse_due("2026-11-17T00:00:00Z")
        self.assertEqual(parsed.utcoffset().total_seconds(), 0)

    def test_naive_datetime_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            server._parse_due("2026-11-17T15:00:00")


class FormatDueTest(unittest.TestCase):
    def test_all_day_has_no_midnight(self) -> None:
        self.assertEqual(
            server._format_due("2026-11-17T00:00:00+08:00", all_day=True),
            "2026-11-17（全天）",
        )

    def test_utc_is_rendered_in_local_time(self) -> None:
        self.assertEqual(
            server._format_due("2026-11-17T00:00:00Z"),
            "2026-11-17 08:00",
        )


class PickTest(unittest.TestCase):
    def test_exact_title_wins(self) -> None:
        items = [{"id": 1, "title": "买牛奶"}, {"id": 2, "title": "买牛奶回家"}]
        item, candidates = server._pick(items, "买牛奶")
        self.assertEqual(item["id"], 1)
        self.assertEqual(candidates, [])

    def test_multiple_matches_need_choice(self) -> None:
        items = [{"id": 1, "title": "交房租"}, {"id": 2, "title": "交房租"}]
        item, candidates = server._pick(items, "交房租")
        self.assertIsNone(item)
        self.assertEqual(len(candidates), 2)

    def test_single_fuzzy_match_is_accepted(self) -> None:
        item, candidates = server._pick([{"id": 1, "title": "买牛奶"}], "牛奶")
        self.assertEqual(item["id"], 1)
        self.assertEqual(candidates, [])

    def test_empty_result_is_not_a_choice(self) -> None:
        item, candidates = server._pick([], "不存在")
        self.assertIsNone(item)
        self.assertEqual(candidates, [])


class ChannelsTest(unittest.TestCase):
    def test_default_and_dedupe(self) -> None:
        self.assertEqual(server._channels(""), ["inapp"])
        self.assertEqual(server._channels("inapp, email,inapp"), ["inapp", "email"])


class CreateTodoTest(unittest.TestCase):
    def test_all_day_today_is_allowed_after_midnight(self) -> None:
        today = datetime.now(server.TZ).date().isoformat()
        captured: dict = {}

        def fake_request(method: str, path: str, **kwargs):
            captured.update(kwargs["json"])
            return {
                "id": 1,
                "title": captured["title"],
                "due_at": captured["due_at"],
                "all_day": captured["all_day"],
            }

        with mock.patch.object(server, "_dedupe_create", return_value=None), mock.patch.object(
            server, "_request", side_effect=fake_request
        ):
            result = server.create_todo("今天补记", today, all_day=True)

        self.assertTrue(result["ok"])
        self.assertEqual(captured["due_at"], f"{today}T00:00:00+08:00")
        self.assertTrue(captured["all_day"])

    def test_past_all_day_is_rejected(self) -> None:
        result = server.create_todo("昨天", "2020-01-01", all_day=True)
        self.assertFalse(result["ok"])
        self.assertIn("日期已经过去", result["error"])


class NoMatchTest(unittest.TestCase):
    def test_complete_returns_not_found(self) -> None:
        with mock.patch.object(server, "_search", return_value=[]):
            result = server.complete_todo("不存在")
        self.assertFalse(result["ok"])
        self.assertIn("没有找到", result["error"])

    def test_snooze_returns_not_found(self) -> None:
        with mock.patch.object(server, "_search", return_value=[]):
            result = server.snooze_todo("不存在", until="2099-01-01T10:00:00+08:00")
        self.assertFalse(result["ok"])
        self.assertIn("没有找到", result["error"])


class WeatherTest(unittest.TestCase):
    def test_city_and_days_are_forwarded(self) -> None:
        captured: dict = {}

        def fake_request(method: str, path: str, **kwargs):
            captured["method"] = method
            captured["path"] = path
            captured.update(kwargs)
            return {
                "location": {"name": "杭州"},
                "current": {"condition": "晴", "temperature": 20},
                "days": [{"date": "2026-11-17"}],
                "alerts": [],
                "title": "☀️ 今日天气 · 杭州",
                "summary": "今天晴，10~20℃",
            }

        with mock.patch.object(server, "_request", side_effect=fake_request):
            result = server.get_weather("杭州", 2)

        self.assertTrue(result["ok"])
        self.assertEqual(captured["method"], "GET")
        self.assertEqual(captured["path"], "/weather/query")
        self.assertEqual(captured["params"], {"days": 2, "city": "杭州"})
        self.assertEqual(result["speech"], "今天晴，10~20℃")
        self.assertEqual(result["location"]["name"], "杭州")

    def test_days_are_clamped_and_empty_city_is_omitted(self) -> None:
        captured: dict = {}

        def fake_request(method: str, path: str, **kwargs):
            captured.update(kwargs)
            return {"summary": "晴"}

        with mock.patch.object(server, "_request", side_effect=fake_request):
            server.get_weather("", 99)

        self.assertEqual(captured["params"], {"days": 7})

    def test_api_error_is_reported(self) -> None:
        with mock.patch.object(
            server, "_request", side_effect=server.ReminderAPIError("管理员尚未配置和风天气")
        ):
            result = server.get_weather("杭州")
        self.assertFalse(result["ok"])
        self.assertIn("尚未配置", result["error"])

    def test_city_name_is_bounded(self) -> None:
        result = server.get_weather("杭" * 61)
        self.assertFalse(result["ok"])
        self.assertIn("过长", result["error"])


class VersionTest(unittest.TestCase):
    def test_mcp_reports_constant_version(self) -> None:
        self.assertEqual(server.mcp.version, server.__version__)

    def test_version_matches_repo_version(self) -> None:
        from pathlib import Path

        version_file = Path(__file__).resolve().parents[1] / "VERSION"
        if not version_file.exists():
            self.skipTest("VERSION file not found")
        expected = version_file.read_text(encoding="utf-8").strip().lstrip("vV")
        self.assertEqual(server.__version__, expected)


class ToolRegistrationTest(unittest.TestCase):
    def test_official_mcp_server_exposes_six_tools(self) -> None:
        import asyncio

        from mcp.server import MCPServer

        self.assertIsInstance(server.mcp, MCPServer)
        names = {tool.name for tool in asyncio.run(server.mcp.list_tools())}
        self.assertEqual(
            names,
            {
                "get_current_time",
                "list_todos",
                "create_todo",
                "complete_todo",
                "snooze_todo",
                "get_weather",
            },
        )


if __name__ == "__main__":
    unittest.main()
