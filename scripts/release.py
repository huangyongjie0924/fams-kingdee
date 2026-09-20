#!/usr/bin/env python3
"""发布一个版本：构建 → 打标签 → 推送 → 创建 GitHub Release（附 Linux 二进制）。

用法：
    python scripts/release.py v1.0.1 --notes release-notes/v1.0.1.md
    python scripts/release.py v1.0.1 --notes release-notes/v1.0.1.md --dry-run

约定：
    - 发布目标默认是**私有库** origin（fams-kingdee-dev）。
      公开库按需另行推送：`git push public main --tags`
    - 标签一律打在**当前 HEAD**，且要求工作区干净、本地与远端同步。
    - Release 附的二进制由本次构建产出，脚本会打印 SHA256 供部署后核对。

凭据：
    优先读环境变量 GH_TOKEN；没有则用 `git credential fill` 取（与 git push 同一份凭据）。
    凭据只放在进程内存里，不落盘、不打印。
"""

import argparse
import hashlib
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_REPO = "huangyongjie0924/fams-kingdee-dev"
BINARY_NAME = "asset-mgr-linux-amd64"
LOCAL_BINARY = os.path.join(ROOT, "deploy", "asset-mgr")


def sh(cmd, cwd=ROOT, check=True, capture=True):
    r = subprocess.run(
        cmd, cwd=cwd, shell=isinstance(cmd, str),
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        text=True,
    )
    if check and r.returncode != 0:
        sys.exit(f"命令失败（exit {r.returncode}）: {cmd}\n{r.stdout or ''}")
    return (r.stdout or "").strip()


def token():
    t = os.environ.get("GH_TOKEN")
    if t:
        return t
    p = subprocess.run(
        ["git", "credential", "fill"],
        input="protocol=https\nhost=github.com\n\n",
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    for line in p.stdout.splitlines():
        if line.startswith("password="):
            return line[len("password="):]
    sys.exit("拿不到 GitHub 凭据：既没有 GH_TOKEN，git credential fill 也没返回")


def api(url, tok, method="GET", data=None, ctype="application/json"):
    h = {
        "Authorization": f"Bearer {tok}",
        "Accept": "application/vnd.github+json",
        "User-Agent": "fams-release",
    }
    if data is not None:
        h["Content-Type"] = ctype
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req) as r:
            return json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        sys.exit(f"GitHub API {method} {url} 失败: {e.code}\n{body}")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def preflight(version, dry_run):
    if not version.startswith("v"):
        sys.exit("版本号要以 v 开头，例如 v1.0.1")

    dirty = sh("git status --porcelain")
    if dirty:
        sys.exit(f"工作区不干净，先提交或暂存：\n{dirty}")

    branch = sh("git rev-parse --abbrev-ref HEAD")
    if branch != "main":
        sys.exit(f"当前在 {branch}，发布请在 main 上做")

    sh("git fetch origin --tags")
    behind = sh("git rev-list --count HEAD..origin/main")
    ahead = sh("git rev-list --count origin/main..HEAD")
    if behind != "0":
        sys.exit(f"落后 origin/main {behind} 个提交，先 pull")
    if ahead != "0":
        print(f"提示：本地领先 origin/main {ahead} 个提交，将随本次发布一起推送")

    existing = sh(f"git tag -l {version}")
    tag_ready = False
    if existing:
        # 标签已存在：只有当它正指向 HEAD 时才允许继续。
        # 这是为了「标签推上去了、但创建 Release 失败」时能直接重跑续上，
        # 而不是逼人去删标签重来。
        at = sh(f"git rev-list -n1 {version}")
        if at != sh("git rev-parse HEAD"):
            sys.exit(
                f"标签 {version} 已存在且指向 {at[:7]}，不是当前 HEAD —— 换个版本号，"
                f"或先 `git tag -d {version}` 再决定"
            )
        tag_ready = True
        print(f"标签 {version} 已存在且指向 HEAD，将跳过打标签、只创建 Release")

    head = sh("git rev-parse --short HEAD")
    print(f"发布目标: {version}  @ {head}  (分支 {branch})")
    if dry_run:
        print("[dry-run] 到此为止，未构建、未打标签、未推送")
        return head, None
    return head, tag_ready


def build():
    print("\n[1/4] 构建前端（vite build）…")
    out = sh("npm run build", cwd=os.path.join(ROOT, "web"))
    print("  " + out.splitlines()[-1] if out else "  ok")

    print("[2/4] 交叉编译 linux/amd64…")
    sh("GOOS=linux GOARCH=amd64 go build -o deploy/asset-mgr .")
    if not os.path.exists(LOCAL_BINARY):
        sys.exit("编译未产出 deploy/asset-mgr")
    digest = sha256(LOCAL_BINARY)
    size = os.path.getsize(LOCAL_BINARY)
    print(f"  {BINARY_NAME}  {size:,} 字节  sha256={digest}")
    return digest, size


def release(version, head, notes_path, repo, tok, digest, size, tag_ready):
    if not os.path.exists(notes_path):
        sys.exit(f"发布说明文件不存在: {notes_path}")
    with open(notes_path, encoding="utf-8") as f:
        body = f.read()

    # 把校验信息追加进发布说明，避免「说明与产物对不上」
    body += (
        f"\n\n---\n\n### 产物校验（本次发布自动记录）\n\n"
        f"| 项 | 值 |\n|---|---|\n"
        f"| 文件 | `{BINARY_NAME}` |\n"
        f"| 大小 | {size:,} 字节 |\n"
        f"| SHA256 | `{digest}` |\n"
        f"| 构建提交 | `{head}` |\n"
        f"| 目标平台 | linux/amd64 |\n"
    )

    if tag_ready:
        print(f"[3/4] 标签 {version} 已在 HEAD 上，跳过打标签")
    else:
        print(f"[3/4] 打标签 {version} 并推送到私有库 origin…")
        sh(f'git tag -a {version} -m "{version}"')
        sh(f"git push origin {version}")

    print("[4/4] 创建 GitHub Release 并上传二进制…")
    payload = json.dumps({
        "tag_name": version,
        "name": f"FAMS {version}",
        "body": body,
        "draft": False,
        "prerelease": False,
    }).encode()
    rel = api(f"https://api.github.com/repos/{repo}/releases", tok, "POST", payload)
    print(f"  release: {rel['html_url']}")

    with open(LOCAL_BINARY, "rb") as f:
        blob = f.read()
    asset = api(
        f"https://uploads.github.com/repos/{repo}/releases/{rel['id']}/assets?name={BINARY_NAME}",
        tok, "POST", blob, "application/octet-stream",
    )
    print(f"  asset:   {asset['name']}  {asset['size']:,} 字节")
    print(f"  download: {asset['browser_download_url']}")

    print(f"\n完成。校验 SHA256 应为：{digest}")
    print("公开库如需同步：git push public main --tags")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("version", help="版本号，如 v1.0.1")
    ap.add_argument("--notes", required=True, help="发布说明 markdown 文件路径")
    ap.add_argument("--repo", default=DEFAULT_REPO, help=f"发布目标仓库（默认 {DEFAULT_REPO}）")
    ap.add_argument("--dry-run", action="store_true", help="只做前置检查，不构建不推送")
    args = ap.parse_args()

    head, tag_ready = preflight(args.version, args.dry_run)
    if args.dry_run:
        return

    digest, size = build()
    release(args.version, head, args.notes, args.repo, token(), digest, size, tag_ready)


if __name__ == "__main__":
    main()
