"""上传新二进制到生产机并重启服务。

只替换 /opt/asset-mgr/asset-mgr，不动 config.yaml / .env / cert/ ——
本次改动全部在编译进二进制的前端资源里，服务器侧配置无需变更。
密码从环境变量 SSH_PW 读取，不写进文件。
"""
import hashlib
import os
import sys
import time

import paramiko

HOST = "<your-host>"
USER = "root"
LOCAL = os.path.join(os.path.dirname(os.path.abspath(__file__)), "asset-mgr")
REMOTE_TMP = "/tmp/asset-mgr.new"
REMOTE_BIN = "/opt/asset-mgr/asset-mgr"


def md5(path):
    h = hashlib.md5()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def run(cli, cmd, label):
    _, out, err = cli.exec_command(cmd, timeout=120)
    stdout = out.read().decode("utf-8", "replace").strip()
    stderr = err.read().decode("utf-8", "replace").strip()
    code = out.channel.recv_exit_status()
    print(f"[{label}] exit={code}")
    if stdout:
        print(stdout)
    if stderr:
        print("stderr:", stderr)
    return code, stdout


def main():
    pw = os.environ.get("SSH_PW")
    if not pw:
        sys.exit("SSH_PW 未设置")

    local_md5 = md5(LOCAL)
    print(f"local  {LOCAL} md5={local_md5} size={os.path.getsize(LOCAL)}")

    cli = paramiko.SSHClient()
    cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    cli.connect(HOST, username=USER, password=pw, timeout=15, banner_timeout=30)

    try:
        run(cli, f"md5sum {REMOTE_BIN}; systemctl is-active asset-mgr", "before")

        sftp = cli.open_sftp()
        try:
            t0 = time.time()
            sftp.put(LOCAL, REMOTE_TMP)
            print(f"uploaded {os.path.getsize(LOCAL)} bytes in {time.time() - t0:.1f}s")
        finally:
            sftp.close()

        run(cli, f"md5sum {REMOTE_TMP}", "uploaded-md5")

        ts = time.strftime("%Y%m%d-%H%M%S")
        script = (
            "set -e; "
            f"cp -a {REMOTE_BIN} {REMOTE_BIN}.bak-{ts}; "
            f"chmod 755 {REMOTE_TMP}; chown root:root {REMOTE_TMP}; "
            f"mv -f {REMOTE_TMP} {REMOTE_BIN}; "
            f"systemctl restart asset-mgr; sleep 4; "
            f"systemctl is-active asset-mgr; md5sum {REMOTE_BIN}"
        )
        code, _ = run(cli, f"bash -lc '{script}'", "deploy")
        if code != 0:
            sys.exit("部署命令失败")

        run(cli, "ss -lntp | grep -E ':8080|:8443'", "ports")
        run(cli, "journalctl -u asset-mgr -n 25 --no-pager", "journal")
    finally:
        cli.close()


if __name__ == "__main__":
    main()
