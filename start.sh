#!/usr/bin/env bash
# 启动固定资产台账服务。金蝶凭据从 .env 读入环境变量（config.go 只认 os.Getenv）。
set -euo pipefail
cd "$(dirname "$0")"

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

exec ./bin/asset-mgr.exe "$@"
