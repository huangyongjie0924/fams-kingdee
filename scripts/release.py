#!/usr/bin/env python3
"""发布一个版本：构建 → 打标签 → 推送 → 创建 GitHub Release（附 Linux 二进制）。

用法：
    python scripts/release.py v1.0.1 --notes release-notes/v1.0.1.md
    python scripts/release.py v1.0.1 --notes release-notes/v1.0.1.md --dry-run

约定：
    - 发布目标默认是**私有库** origin（fams-kingdee-dev）。
      公开库按需另行推送：`git push public main --tags`
    - 标签一律打在**当前 HEAD**，且要求工作区干净、本地与远端同步。
    - 中断后可以原样重跑：已存在的标签与 Release 会被复用，同名附件会被替换。
    - Release 附的二进制由本次构建产出，脚本会打印 SHA256 供部署后核对。

凭据：
    优先读环境变量 GH_TOKEN；没有则用 `git credential fill` 取（与 git push 同一份凭据）。
    凭据只放在进程内存里，不落盘、不打印。
"""

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_REPO = "huangyongjie0924/fams-kingdee-dev"
BINARY_NAME = "asset-mgr-linux-amd64"
LOCAL_BINARY = os.path.join(ROOT, "deploy", "asset-mgr")


def sh(cmd, cwd=ROOT, check=True, capture=True, env=None):
    """跑一条命令。

    env：额外/覆盖的环境变量。**不要**写成 `GOOS=linux go build …` 这种前缀 ——
    Windows 上 shell=True 走的是 cmd.exe，它不认 `VAR=value cmd`，会直接报
    "'GOOS' is not recognized"。要传环境变量就走 env= 参数。
    """
    r = subprocess.run(
        cmd, cwd=cwd, shell=isinstance(cmd, str),
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        text=True,
        env={**os.environ, **env} if env else None,
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


def _request(url, tok, method="GET", data=None, ctype="application/json"):
    """发一次请求，返回 (状态码, 响应体文本)。HTTP 错误码不当异常抛。"""
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
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


def _parse(method, url, status, body):
    if status >= 400:
        sys.exit(f"GitHub API {method} {url} 失败: {status}\n{body}")
    # DELETE 附件返回 204，没有响应体
    return json.loads(body) if body.strip() else None


def api(url, tok, method="GET", data=None, ctype="application/json"):
    status, body = _request(url, tok, method, data, ctype)
    return _parse(method, url, status, body)


def api_optional(url, tok, method="GET"):
    """和 api() 一样，但 404 视为「这东西不存在」返回 None（其余错误照旧中止）。

    续跑时要靠它判断 Release 是否已经建过：直接 GET，比 POST 撞 422 再兜底干净。
    """
    status, body = _request(url, tok, method)
    if status == 404:
        return None
    return _parse(method, url, status, body)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def origin_slug():
    """从 `git remote get-url origin` 解析出 owner/repo。

    支持 https://github.com/<owner>/<repo>.git 与 git@github.com:<owner>/<repo>.git。
    """
    url = sh("git remote get-url origin")
    m = re.match(r"^(?:https?://[^/]+/|git@[^:]+:)([^/]+)/(.+?)(?:\.git)?/?$", url)
    if not m:
        sys.exit(f"看不懂 origin 的地址，请检查 git remote -v：{url}")
    return f"{m.group(1)}/{m.group(2)}"


def preflight(version, dry_run, repo, allow_repo_mismatch):
    if not version.startswith("v"):
        sys.exit("版本号要以 v 开头，例如 v1.0.1")

    dirty = sh("git status --porcelain")
    if dirty:
        sys.exit(f"工作区不干净，先提交或暂存：\n{dirty}")

    branch = sh("git rev-parse --abbrev-ref HEAD")
    if branch != "main":
        sys.exit(f"当前在 {branch}，发布请在 main 上做")

    # 标签推到 origin，Release 建在 --repo；两者不是一个仓库的话，
    # 会「标签在一个库、Release 在另一个库」而且没有任何提示。默认拒绝。
    origin = origin_slug()
    if origin.lower() != repo.lower():
        if not allow_repo_mismatch:
            sys.exit(
                f"--repo 是 {repo}，但 git remote origin 指向 {origin}。\n"
                f"标签会推到 origin（{origin}），Release 却建在 {repo}，两者对不上。\n"
                f"要么改 --repo，要么改 remote；确实要跨库发版请显式加 --allow-repo-mismatch。"
            )
        print(f"警告：--repo ({repo}) 与 origin ({origin}) 不一致，因 --allow-repo-mismatch 继续")

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


def dist_assets():
    """列出 web/dist 下所有带内容哈希的产物文件名（assets/*.js、assets/*.css）。"""
    d = os.path.join(ROOT, "web", "dist", "assets")
    if not os.path.isdir(d):
        sys.exit("web/dist/assets 不存在 —— 前端没构建成功")
    names = sorted(f for f in os.listdir(d) if f.endswith((".js", ".css")))
    if not names:
        sys.exit("web/dist/assets 是空的 —— 前端没构建成功")
    return names


def verify_embed(names):
    """确认二进制里内嵌的前端产物就是刚构建出来的那一批。

    这道检查防的是「构建失败但产物照出」：前端构建如果失败、而磁盘上还留着
    上一次的 dist，go build 会拿旧 dist 编出一个「有产物、能启动、大小正常」的
    二进制，看起来完全没问题。只靠 check=True 拦不住这种情况。

    go:embed all:web/dist 会把 dist 下每个文件的路径都写进二进制，
    所以「文件名出现在二进制里」是可靠判据。
    """
    with open(LOCAL_BINARY, "rb") as f:
        blob = f.read()
    missing = [n for n in names if f"assets/{n}".encode() not in blob]
    if missing:
        sys.exit(
            "二进制里找不到下列前端产物，说明它内嵌的不是刚构建的那批（很可能是旧 dist）：\n  "
            + "\n  ".join(missing[:10])
            + "\n请删掉 web/dist 后重新构建。"
        )
    return len(names)


def read_build_info(path):
    """读二进制内嵌的构建信息，返回 (go 版本, {键: 值})。

    `go version -m` 只解析文件头，**不运行**目标程序，所以交叉编译出来的
    linux 二进制在 Windows 上照样能读。输出每行形如 "\\tbuild\\tvcs.revision=xxxx"。
    """
    out = sh(f'go version -m "{path}"')
    lines = out.splitlines()
    goversion = lines[0].split(": ", 1)[1].strip() if lines and ": " in lines[0] else "unknown"
    info = {}
    for line in lines:
        parts = line.split("\t")
        if len(parts) >= 3 and parts[1] == "build":
            k, _, v = parts[2].partition("=")
            info[k] = v
    return goversion, info


def verify_build_info():
    """断言二进制确实编自当前 HEAD、且构建时工作区是干净的。

    防的是两种「看起来发布成功、其实对不上」：
      - 脏工作区构建：vcs.modified=true，标签指向一个复现不出该二进制的提交；
      - 拿旧二进制上传：vcs.revision 与 HEAD 不同，Release 正文却写着当前提交。
    """
    goversion, info = read_build_info(LOCAL_BINARY)
    revision = info.get("vcs.revision")
    modified = info.get("vcs.modified")
    if not revision or modified is None:
        sys.exit(
            "二进制里没有 vcs.revision / vcs.modified，无法追溯它编自哪次提交。\n"
            "（常见原因：构建时不在 git 仓库里，或加了 -buildvcs=false）"
        )
    if modified != "false":
        sys.exit(
            f"二进制内嵌 vcs.modified={modified}：它是在**有未提交改动**的工作区里编出来的，\n"
            f"标签会指向一个复现不出该二进制的提交。请先提交或 stash 再重跑。"
        )
    head_full = sh("git rev-parse HEAD")
    if revision != head_full:
        sys.exit(
            f"二进制内嵌的 vcs.revision = {revision}\n"
            f"当前 HEAD                = {head_full}\n"
            f"两者不一致 —— 上传的会是别的提交编出来的旧二进制。请删掉 deploy/asset-mgr 重新构建。"
        )
    return goversion, revision, modified


def build():
    print("\n[1/4] 构建前端（vite build）…")
    out = sh("npm run build", cwd=os.path.join(ROOT, "web"))
    print("  " + (out.splitlines()[-1] if out else "ok"))
    names = dist_assets()

    print("[2/4] 交叉编译 linux/amd64…")
    # -trimpath：不把绝对路径编进二进制，换台机器/换个目录才可能复现出同一份产物。
    # 环境变量走 env=，不能写成 "GOOS=linux go build"（Windows 上 shell 是 cmd.exe，不认）。
    sh(
        ["go", "build", "-trimpath", "-o", "deploy/asset-mgr", "."],
        env={"GOOS": "linux", "GOARCH": "amd64"},
    )
    if not os.path.exists(LOCAL_BINARY):
        sys.exit("编译未产出 deploy/asset-mgr")

    n = verify_embed(names)
    print(f"  已确认二进制内嵌本次构建的 {n} 个前端产物 ✓")

    goversion, revision, modified = verify_build_info()
    print(f"  构建信息: {goversion}  vcs.revision={revision[:7]}  vcs.modified={modified} ✓")

    digest = sha256(LOCAL_BINARY)
    size = os.path.getsize(LOCAL_BINARY)
    print(f"  {BINARY_NAME}  {size:,} 字节  sha256={digest}")
    return digest, size, {"go": goversion, "revision": revision, "modified": modified}


def release(version, head, notes_path, repo, tok, digest, size, tag_ready, binfo):
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
        f"| vcs.revision | `{binfo['revision']}` |\n"
        f"| vcs.modified | `{binfo['modified']}` |\n"
        f"| Go 版本 | `{binfo['go']}` |\n"
        f"| 目标平台 | linux/amd64 |\n"
    )

    print("[3/4] 推送分支与标签到 origin…")
    # 先推分支：标签必须落在远端 main 的祖先链上，否则 Release 指向一个
    # 远端 main 上根本看不见的提交（preflight 那句「将随本次发布一起推送」才算兑现）。
    # 已同步时这条是 no-op，git 会输出 Everything up-to-date。
    print("  推送 main…")
    sh("git push origin main")
    if tag_ready:
        print(f"  标签 {version} 已在 HEAD 上，跳过打标签")
    else:
        sh(f'git tag -a {version} -m "{version}"')
    # 即使标签早就存在也要推一次：本地建了标签但从没推上去的情况要能救回来。
    # 幂等，已推送时输出 Everything up-to-date；不要加 --force。
    print(f"  推送标签 {version}…")
    sh(f"git push origin {version}")

    print("[4/4] 创建 GitHub Release 并上传二进制…")
    rel = api_optional(f"https://api.github.com/repos/{repo}/releases/tags/{version}", tok)
    if rel is None:
        payload = json.dumps({
            "tag_name": version,
            "name": f"FAMS {version}",
            "body": body,
            "draft": False,
            "prerelease": False,
        }).encode()
        rel = api(f"https://api.github.com/repos/{repo}/releases", tok, "POST", payload)
    else:
        # 续跑：标签推上去了但附件上传失败（网络抖动），重跑会走到这里。
        # 复用已有的 Release 并刷新正文，再删掉同名旧附件重传 ——
        # 直接 POST 只会撞 422 already_exists。
        print(f"  release 已存在（{rel['html_url']}），复用并替换附件")
        rel = api(
            f"https://api.github.com/repos/{repo}/releases/{rel['id']}", tok, "PATCH",
            json.dumps({"body": body}).encode(),
        )
        for a in rel.get("assets", []):
            if a["name"] == BINARY_NAME:
                print(f"  删除旧附件 {a['name']} (id={a['id']})")
                api(f"https://api.github.com/repos/{repo}/releases/assets/{a['id']}", tok, "DELETE")
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
    ap.add_argument(
        "--allow-repo-mismatch", action="store_true",
        help="允许 --repo 与 git remote origin 不一致（默认拒绝，防止标签与 Release 落在两个库）",
    )
    ap.add_argument("--dry-run", action="store_true", help="只做前置检查，不构建不推送")
    args = ap.parse_args()

    head, tag_ready = preflight(args.version, args.dry_run, args.repo, args.allow_repo_mismatch)
    if args.dry_run:
        return

    digest, size, binfo = build()
    release(args.version, head, args.notes, args.repo, token(), digest, size, tag_ready, binfo)


if __name__ == "__main__":
    main()
