#!/usr/bin/env bash
# Epson 国行私有驱动：仅 amd64 best-effort 安装。
#
# `epson-inkjet-printer-201601w` 与 `epson-printer-utility` 是 Epson 中国区
# 发布的**闭源专有** .deb 包（无源码，无 arm64/armhf 二进制），覆盖 L380/L455
# 等国行早期喷墨机型。对应功能大部分可以被 Debian 自带的 `printer-driver-escpr`
# 覆盖，但原厂 PPD 在墨水检测、尺寸预设等细节上更完整。
#
# ⚠️ 原下载源 download-center.epson.com.cn 的 UUID 会定期轮换导致 URL 失效，
# 因此把 .deb 镜像到本仓库的 GitHub Releases（cups-driver tag）。
# 此处采用 **fail-fast**：下载 / dpkg 任一步失败则脚本立刻中断，
# 避免发布镜像里缺少国行驱动却静默成功。arm64/armhf 在脚本入口直接退出，
# 不受影响。
# 升级方法：把新版 .deb 上传到 https://github.com/hanxi/cups-web/releases 的
# cups-driver tag，更新下方 DEB 变量即可。

set -eo pipefail

# 仅 amd64 安装。
# ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────────────
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（厂商未提供该架构二进制）
#   其他非零 = 真正的失败（下载 / dpkg / 编译失败等）
# 这里必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为驱动可用（实际什么都没装）。用 3 让上层
# 能明确区分"本架构不支持"和"真失败"。
ARCH="$(dpkg --print-architecture)"
if [ "${ARCH}" != "amd64" ]; then
    echo "[epson-cn] unsupported arch=${ARCH} (only amd64 supported)"
    exit 3
fi

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
EPSON_PROP_DRIVER_DEB="epson-inkjet-printer-201601w_1.0.1-1_amd64.deb"
EPSON_PROP_UTILITY_DEB="epson-printer-utility_1.2.2-1_amd64.deb"
EPSON_PROP_UA="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

EPSON_DRV_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${EPSON_PROP_DRIVER_DEB}"
EPSON_UTIL_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${EPSON_PROP_UTILITY_DEB}"

# ────────────────────────────────────────────────────────────────────
# 下载 & dpkg
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/epson-cn.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[epson-cn] downloading ${EPSON_DRV_URL}"
wget --tries=3 --timeout=60 --retry-connrefused \
     --user-agent="${EPSON_PROP_UA}" \
     -O "${EPSON_PROP_DRIVER_DEB}" "${EPSON_DRV_URL}"

echo "[epson-cn] downloading ${EPSON_UTIL_URL}"
wget --tries=3 --timeout=60 --retry-connrefused \
     --user-agent="${EPSON_PROP_UA}" \
     -O "${EPSON_PROP_UTILITY_DEB}" "${EPSON_UTIL_URL}"

# dpkg -i 失败时用 apt-get -f install 兜底处理依赖。
# ⚠️ 必须先 apt-get update：apt 需要包索引才能下载缺失依赖，AIO 运行时的镜像里
# /var/lib/apt/lists 可能是空的（构建期为省体积清过）。
if ! dpkg -i ./*.deb; then
    echo "[epson-cn] dpkg reported dependency issues, fixing with apt-get -f install"
    apt-get update
    apt-get install -y -f --no-install-recommends
fi

echo "[epson-cn] installed Epson CN proprietary driver + utility"
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi
