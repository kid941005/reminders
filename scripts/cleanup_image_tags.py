#!/usr/bin/env python3
"""清理 GHCR 镜像标签：只保留 latest 与 x.y.z 版本号。

GHCR 不支持单独删除某个 tag（DELETE /v2/.../manifests/<tag> 返回 UNSUPPORTED），
删除 package version 会连同该 version 上所有 tag 一起删除。因此对同时含有
可保留 tag 与待清理 tag 的 version，先在本机 pull 保留镜像，删除 version，再用
保留 tag 重新 push。默认只预演（APPLY=false）。
"""
import os
import re
import subprocess
import sys
import time
import requests

OWNER = os.environ["GITHUB_REPOSITORY_OWNER"]
REPO = os.environ["GITHUB_REPOSITORY"]
TOKEN = os.environ["GH_TOKEN"]
APPLY = os.environ.get("APPLY", "false").lower() == "true"
PACKAGES = [p.strip() for p in os.environ.get("PACKAGES", "reminders,reminders-mcp").split(",") if p.strip()]
SEMVER = re.compile(r"^\d+\.\d+\.\d+$")
API = "https://api.github.com"
HEADERS = {
    "Authorization": f"Bearer {TOKEN}",
    "Accept": "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
}

def api(method, path, **kwargs):
    r = requests.request(method, API + path, headers=HEADERS, timeout=60, **kwargs)
    if r.status_code >= 300:
        raise RuntimeError(f"{method} {path} -> {r.status_code}: {r.text[:400]}")
    return r

def versions_base(pkg):
    candidates = [
        f"/repos/{REPO}/packages/container/{pkg}/versions",
        f"/users/{OWNER}/packages/container/{pkg}/versions",
        f"/user/packages/container/{pkg}/versions",
    ]
    for path in candidates:
        r = requests.get(API + path + "?per_page=1", headers=HEADERS, timeout=60)
        if r.status_code == 200:
            print(f"versions endpoint: {path}", flush=True)
            return path
        print(f"endpoint unavailable: {path} -> {r.status_code}", flush=True)
    raise RuntimeError(f"no versions endpoint available for {pkg}")

def list_versions(pkg, base):
    out, page = [], 1
    while True:
        r = api("GET", f"{base}?per_page=100&page={page}")
        batch = r.json()
        if not batch:
            break
        out.extend(batch)
        if len(batch) < 100:
            break
        page += 1
    return out

def docker(*args):
    print("+ docker", " ".join(args), flush=True)
    subprocess.run(["docker", *args], check=True)

def keep_tags(tags):
    return [t for t in tags if t == "latest" or SEMVER.match(t)]

def clean(pkg):
    print(f"=== {pkg} ===", flush=True)
    base = versions_base(pkg)
    versions = list_versions(pkg, base)
    print(f"versions={len(versions)}", flush=True)
    for v in versions:
        vid = v["id"]
        tags = (v.get("metadata") or {}).get("container", {}).get("tags") or []
        keep = keep_tags(tags)
        drop = [t for t in tags if t not in keep]
        if not drop:
            continue
        print(f"version {vid}: keep={keep} drop={drop}", flush=True)
        if not APPLY:
            continue
        if not keep:
            api("DELETE", f"{base}/{vid}")
            print("  deleted (no keep tags)", flush=True)
            continue
        source = next((t for t in keep if t != "latest"), keep[0])
        image = f"ghcr.io/{OWNER}/{pkg}"
        docker("pull", f"{image}:{source}")
        for t in keep:
            docker("tag", f"{image}:{source}", f"{image}:{t}")
        api("DELETE", f"{base}/{vid}")
        for t in keep:
            docker("push", f"{image}:{t}")
        time.sleep(1)

def verify(pkg, base):
    versions = list_versions(pkg, base)
    tags = []
    for v in versions:
        tags += (v.get("metadata") or {}).get("container", {}).get("tags") or []
    bad = [t for t in tags if t != "latest" and not SEMVER.match(t)]
    print(f"{pkg}: tags={sorted(set(tags))}")
    print(f"{pkg}: non-conforming={bad}")
    return not bad

def main():
    print(f"apply={APPLY} repo={REPO} packages={PACKAGES}", flush=True)
    for pkg in PACKAGES:
        clean(pkg)
    ok = True
    for pkg in PACKAGES:
        ok = verify(pkg, versions_base(pkg)) and ok
    if not ok:
        print("WARNING: non-conforming tags remain", file=sys.stderr)
        return 1
    return 0

if __name__ == "__main__":
    sys.exit(main())
