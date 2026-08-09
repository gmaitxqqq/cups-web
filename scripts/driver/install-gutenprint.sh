#!/usr/bin/env bash
# 安装 printer-driver-gutenprint：仅 amd64/arm64 上安装。
#
# printer-driver-gutenprint 在 trixie armhf 上没有 binary 包（gutenprint 的
# 链接依赖 libgutenprint9 未完成 t64 迁移），仅在 amd64/arm64 上安装。
# armhf 用户仍可通过 printer-driver-all 推荐的其他驱动覆盖大部分打印机。

set -eux

# ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────────────
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（无对应架构的二进制包）
#   其他非零 = 真正的失败
# 这里必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为驱动可用（实际什么都没装）。
ARCH="$(dpkg --print-architecture)"
if [ "${ARCH}" = "armhf" ] || [ "${ARCH}" = "armel" ]; then
    echo "[gutenprint] unsupported arch=${ARCH} (no binary package on trixie)"
    exit 3
fi

apt-get update
apt-get install -y --no-install-recommends printer-driver-gutenprint
apt-get clean
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi

echo "[gutenprint] installed"
