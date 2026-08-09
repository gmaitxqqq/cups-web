#!/bin/bash
set -eo pipefail

DRIVERS_BASE="/opt/cups-drivers"
SCRIPTS_DIR="${DRIVERS_BASE}/scripts"
DATA_DIR="${DRIVERS_BASE}/data"

log() {
    echo "[driver-install] $*"
}

# ── 架构名探测 ─────────────────────────────────────────────────────────
# 必须用 Debian 架构名（amd64 / arm64 / armhf），因为：
#   ① metadata.txt 的 arch= 会被 Go 后端与 driver-list 读取比较；
#   ② install-*.sh 内部一律用 `dpkg --print-architecture` 做架构分支。
# ⚠️ 不要用 dpkg-architecture —— 它属于 dpkg-dev 包，runtime 镜像**没有安装**
# （只有构建阶段才装），调用失败会静默回落到错误的默认值。`dpkg` 本体一定在。
detect_deb_arch() {
    local arch=""
    arch="$(dpkg --print-architecture 2>/dev/null || true)"
    if [ -z "$arch" ]; then
        # 极端兜底：连 dpkg 都没有时用 uname -m（注意这不是 Debian 架构名，
        # 会得到 x86_64 / aarch64，仅作为诊断信息展示用）。
        arch="$(uname -m 2>/dev/null || echo unknown)"
    fi
    echo "$arch"
}

# ── multiarch 库目录探测 ───────────────────────────────────────────────
# 闭源驱动（Canon UFR II 的 libcnpkbidir*.so 等）会把 .so 装到
# /usr/lib/<triplet>/，必须纳入监控，否则驱动重启后丢库。
# 老实现用 `dpkg-architecture -qDEB_HOST_MULTIARCH`，但该命令在 runtime 镜像
# 里不存在 → 静默回落到硬编码的 x86_64-linux-gnu → 在 arm64/armhf 上监控的是
# 一个不存在的目录，共享库变更**完全抓不到**。这里改为：
#   ① dpkg-architecture 存在就用（构建期/开发机）；
#   ② 否则用 glob 探测 /usr/lib/*-linux-gnu*；
#   ③ 都拿不到就返回空字符串，调用方跳过该目录（绝不使用猜错的路径）。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        for d in /usr/lib/*-linux-gnu*; do
            if [ -d "$d" ]; then
                triplet="$(basename "$d")"
                break
            fi
        done
    fi
    if [ -n "$triplet" ] && [ -d "/usr/lib/${triplet}" ]; then
        echo "/usr/lib/${triplet}"
    else
        echo ""
    fi
    return 0
}

DEB_ARCH="$(detect_deb_arch)"
MULTIARCH_LIBDIR="$(detect_multiarch_libdir)"

MONITORED_DIRS=(
    /usr/lib/cups
    /usr/share/cups
    /usr/share/ppd
    /lib/firmware
    /usr/share/foomatic
)
# 探测不到 multiarch 目录时不要塞一个猜的路径进去（find 会静默扫不到东西，
# 更糟的是可能扫到另一个架构的系统库）。
if [ -n "${MULTIARCH_LIBDIR}" ]; then
    MONITORED_DIRS+=("${MULTIARCH_LIBDIR}")
fi

# ── manifest 路径白名单 ────────────────────────────────────────────────
# 为什么必须过滤：manifest.txt 会被 driver-remove 逐条 `rm -f`，被
# restore-drivers 逐条 `cp -a` 覆盖回系统。任何非"驱动产物"的路径进了
# manifest，卸载该驱动时就会删掉系统文件。
# 老实现对 dpkg 来源的文件**完全没有过滤**（`dpkg -L $pkg` 的全部文件直接
# 追加），AIO 模式下 build-essential / gcc / binutils / libc6-dev 都会被算作
# "新装包" → /usr/bin/gcc、/usr/share/man/**、/etc/** 全部进 manifest →
# 卸载一次 canon-capt 就把编译工具链和系统库删了，容器直接残废。
#
# 规则：
#   ① 必须落在 ALLOWED_PREFIXES 之内（真正的驱动产物目录）；
#   ② 即使落在①内，命中 DENIED 模式也一律排除（doc/man/locale/etc/var、
#      /usr/bin 与 /usr/sbin 下的通用系统二进制、dev 包的 .a/.o/.la/pkgconfig）。
# 驱动真正需要的可执行文件在 /usr/lib/cups/filter/ 与 /usr/lib/cups/backend/，
# 绝不会出现在 /usr/bin 或 /usr/sbin。
ALLOWED_PREFIXES=(
    /usr/lib/cups
    /usr/share/cups
    /usr/share/ppd
    /usr/share/foomatic
    /lib/firmware
    /usr/lib/firmware
)
if [ -n "${MULTIARCH_LIBDIR}" ]; then
    ALLOWED_PREFIXES+=("${MULTIARCH_LIBDIR}")
fi

_is_monitored_path() {
    local p="$1" prefix
    case "$p" in
        /usr/bin/*|/usr/sbin/*|/bin/*|/sbin/*|/usr/local/bin/*|/usr/local/sbin/*) return 1 ;;
        /etc/*|/var/*|/usr/include/*|/opt/cups-drivers/*|/tmp/*) return 1 ;;
        /usr/share/doc/*|/usr/share/man/*|/usr/share/locale/*|/usr/share/info/*) return 1 ;;
        /usr/share/cups/doc-root/*) return 1 ;;  # CUPS 自带 Web UI 静态资源，不是驱动产物
        */pkgconfig/*|*.a|*.o|*.la) return 1 ;;
    esac
    for prefix in "${ALLOWED_PREFIXES[@]}"; do
        [ -n "$prefix" ] || continue
        case "$p" in
            "${prefix}/"*) return 0 ;;
        esac
    done
    return 1
}

usage() {
    echo "Usage: driver-install <driver-name>"
    echo ""
    echo "Install a printer driver into the CUPS container."
    echo ""
    echo "Available drivers:"
    if [ -d "${SCRIPTS_DIR}" ]; then
        for script in "${SCRIPTS_DIR}"/install-*.sh; do
            [ -f "$script" ] || continue
            name="$(basename "$script" .sh)"
            name="${name#install-}"
            status="not installed"
            if [ -f "${DATA_DIR}/${name}/manifest.txt" ]; then
                status="installed"
            fi
            echo "  ${name}  (${status})"
        done
    else
        echo "  (no driver scripts found in ${SCRIPTS_DIR})"
    fi
    exit 1
}

# --- Argument validation ---
if [ -z "$1" ]; then
    usage
fi

DRIVER_NAME="$1"
INSTALL_SCRIPT="${SCRIPTS_DIR}/install-${DRIVER_NAME}.sh"
DRIVER_DATA="${DATA_DIR}/${DRIVER_NAME}"

# 安装失败 / 架构不支持时清掉可能已经创建的空数据目录：
# driver-list 只按 manifest.txt 判断"已安装"，但留一堆空目录会让人（和
# 后续排障）困惑，也会让 `driver-remove` 的列表出现幽灵条目。
# 只在确认没有 manifest.txt（即本次安装没成功）时才删，绝不碰已安装的驱动。
discard_driver_data() {
    if [ -d "${DRIVER_DATA}" ] && [ ! -f "${DRIVER_DATA}/manifest.txt" ]; then
        rm -rf "${DRIVER_DATA}"
        log "Discarded incomplete driver data dir: ${DRIVER_DATA}"
    fi
}

# 临时文件统一在退出时清理（注意：bash 对同一信号只保留最后注册的 handler，
# 所以本脚本全局只允许这一个 EXIT trap）。
cleanup_tmp() {
    rm -f /tmp/pre-install.txt /tmp/post-install.txt \
          /tmp/new-files.txt /tmp/new-files-filtered.txt \
          /tmp/pre-dpkg.txt /tmp/post-dpkg.txt /tmp/new-packages.txt
}
trap cleanup_tmp EXIT

if [ ! -f "${INSTALL_SCRIPT}" ]; then
    log "ERROR: Driver '${DRIVER_NAME}' not found."
    log "No install script at: ${INSTALL_SCRIPT}"
    echo ""
    usage
fi

# --- Check if already installed ---
if [ -f "${DRIVER_DATA}/manifest.txt" ]; then
    log "Driver '${DRIVER_NAME}' is already installed."
    log "To reinstall, first remove it: driver-remove ${DRIVER_NAME}"
    exit 1
fi

# --- Record pre-install filesystem state ---
log "Recording pre-install filesystem state..."

# Capture files in monitored directories
: > /tmp/pre-install.txt
for dir in "${MONITORED_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        find "$dir" -type f >> /tmp/pre-install.txt 2>/dev/null || true
    fi
done
sort -u /tmp/pre-install.txt -o /tmp/pre-install.txt

# Capture dpkg state
dpkg --get-selections > /tmp/pre-dpkg.txt 2>/dev/null || true

# --- Run the install script ---
# 退出码约定（install-*.sh 共同遵守）：
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（厂商没有对应架构的二进制）
#   其他非零 = 真正的失败（下载失败、编译失败、dpkg 失败……）
# 老实现不区分退出码，架构不支持的脚本 `exit 0` 后照样写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为可用——必须把这两种情况分开。
log "Installing driver '${DRIVER_NAME}'..."
export CUPS_AIO=1
install_rc=0
bash "${INSTALL_SCRIPT}" || install_rc=$?

if [ "${install_rc}" -eq 3 ]; then
    log "ERROR: 当前架构 ${DEB_ARCH} 不支持驱动 '${DRIVER_NAME}'（厂商未提供该架构的二进制）。"
    log "Nothing was installed; no manifest written."
    discard_driver_data
    exit 3
fi

if [ "${install_rc}" -ne 0 ]; then
    log "ERROR: install script for '${DRIVER_NAME}' failed (exit code ${install_rc})."
    discard_driver_data
    exit "${install_rc}"
fi

log "Install script completed."

# --- Record post-install filesystem state ---
log "Recording post-install filesystem state..."

: > /tmp/post-install.txt
for dir in "${MONITORED_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        find "$dir" -type f >> /tmp/post-install.txt 2>/dev/null || true
    fi
done
sort -u /tmp/post-install.txt -o /tmp/post-install.txt

# Find new files from monitored directories
comm -13 /tmp/pre-install.txt /tmp/post-install.txt > /tmp/new-files.txt

# --- Capture files from new dpkg packages ---
dpkg --get-selections > /tmp/post-dpkg.txt 2>/dev/null || true

# Find newly installed packages
: > /tmp/new-packages.txt
comm -13 <(awk '{print $1}' /tmp/pre-dpkg.txt | sort) \
         <(awk '/install$/{print $1}' /tmp/post-dpkg.txt | sort) \
         > /tmp/new-packages.txt || true

if [ -s /tmp/new-packages.txt ]; then
    log "New packages detected:"
    while IFS= read -r pkg; do
        log "  - ${pkg}"
        # 只接管落在驱动产物白名单内的文件（见 _is_monitored_path 的注释）：
        # 否则 gcc / binutils / libc6-dev 之类编译依赖的系统文件会进 manifest，
        # driver-remove 时把它们删掉，容器直接残废。
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            [ -f "$f" ] || continue
            if _is_monitored_path "$f"; then
                echo "$f"
            fi
        done < <(dpkg -L "$pkg" 2>/dev/null || true) >> /tmp/new-files.txt
    done < /tmp/new-packages.txt
fi

# --- Filter + deduplicate ---
# find 差分来源虽然已受 MONITORED_DIRS 限制，但同一套白名单再过一遍更安全
# （例如 /usr/share/cups/doc-root/** 之类不该由我们接管的路径）。
sort -u /tmp/new-files.txt -o /tmp/new-files.txt
: > /tmp/new-files-filtered.txt
while IFS= read -r f; do
    [ -n "$f" ] || continue
    if _is_monitored_path "$f"; then
        echo "$f" >> /tmp/new-files-filtered.txt
    else
        log "  skip (outside driver whitelist): $f"
    fi
done < /tmp/new-files.txt
mv /tmp/new-files-filtered.txt /tmp/new-files.txt

# "装完了但一个文件都没产生"一定是异常（下载到临时目录就退出、装到了未监控
# 位置、或者脚本其实什么都没做）。此时绝不能写 manifest.txt —— 否则
# driver-list / Web UI 会显示"已安装"，用户以为好了。
if [ ! -s /tmp/new-files.txt ]; then
    log "ERROR: No new files detected after installing '${DRIVER_NAME}'."
    log "The driver may have installed files in unmonitored locations, or the"
    log "install script silently did nothing. Refusing to write manifest.txt."
    discard_driver_data
    exit 1
fi

# --- Persist driver files ---
log "Persisting driver files to ${DRIVER_DATA}..."
mkdir -p "${DRIVER_DATA}"

file_count=0
while IFS= read -r filepath; do
    [ -z "$filepath" ] && continue
    [ -f "$filepath" ] || continue
    dest="${DRIVER_DATA}${filepath}"
    mkdir -p "$(dirname "$dest")"
    cp -a "$filepath" "$dest"
    file_count=$((file_count + 1))
done < /tmp/new-files.txt

# Save manifest
cp /tmp/new-files.txt "${DRIVER_DATA}/manifest.txt"

# Save install metadata
echo "driver=${DRIVER_NAME}" > "${DRIVER_DATA}/metadata.txt"
echo "installed_at=$(date -Iseconds)" >> "${DRIVER_DATA}/metadata.txt"
echo "file_count=${file_count}" >> "${DRIVER_DATA}/metadata.txt"
# arch 必须是 Debian 架构名（amd64 / arm64 / armhf）：driver-list 用它跟当前
# 架构做比较，Go 后端也会把它透给前端展示。用 detect_deb_arch 统一基准，避免
# 一边写 uname -m 的 aarch64、一边比 dpkg 的 arm64 造成误报"架构不一致"。
echo "arch=${DEB_ARCH}" >> "${DRIVER_DATA}/metadata.txt"

# --- Run ldconfig ---
log "Updating shared library cache..."
ldconfig 2>/dev/null || true

# --- Cleanup ---
# 临时文件由 EXIT trap（cleanup_tmp）统一清理，覆盖所有提前退出的分支。

log "Driver '${DRIVER_NAME}' installed successfully. (${file_count} files persisted)"
