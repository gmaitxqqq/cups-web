#!/bin/bash
set -eo pipefail

DRIVERS_BASE="/opt/cups-drivers"
DATA_DIR="${DRIVERS_BASE}/data"

log() {
    echo "[driver-remove] $*"
}

# ── 删除白名单（安全网）─────────────────────────────────────────────────
# 即使 driver-install 已经把 manifest 过滤到驱动产物路径，这里也要**再校验
# 一遍**：跑过旧版本的用户手上已经存在被污染的 .drivers 快照（老 driver-install
# 把 `dpkg -L build-essential` 之类的全部文件写进了 manifest），直接按老
# manifest 逐条 rm 会删掉 /usr/bin/gcc、/usr/share/man/**、libc6-dev 的库，
# 把容器搞残。不在白名单里的路径一律**跳过并告警，绝不 rm**。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        # dpkg-architecture 属于 dpkg-dev，runtime 镜像没有；用 glob 探测。
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

MULTIARCH_LIBDIR="$(detect_multiarch_libdir)"

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

_is_removable_path() {
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

if [ -z "$1" ]; then
    echo "Usage: driver-remove <driver-name>"
    echo ""
    echo "Remove an installed printer driver."
    echo ""
    echo "Installed drivers:"
    if [ -d "${DATA_DIR}" ]; then
        found=false
        for driver_dir in "${DATA_DIR}"/*/; do
            [ -d "$driver_dir" ] || continue
            [ -f "${driver_dir}manifest.txt" ] || continue
            found=true
            echo "  $(basename "$driver_dir")"
        done
        if ! $found; then
            echo "  (none)"
        fi
    else
        echo "  (none)"
    fi
    exit 1
fi

DRIVER_NAME="$1"
DRIVER_DATA="${DATA_DIR}/${DRIVER_NAME}"
MANIFEST="${DRIVER_DATA}/manifest.txt"

if [ ! -f "${MANIFEST}" ]; then
    log "ERROR: Driver '${DRIVER_NAME}' is not installed."
    log "No manifest found at: ${MANIFEST}"
    exit 1
fi

log "Removing driver '${DRIVER_NAME}'..."

# Remove files listed in the manifest from system paths
removed_count=0
missing_count=0
skipped_count=0
failed_count=0

while IFS= read -r filepath; do
    [ -z "$filepath" ] && continue

    # 安全网：manifest 里出现非驱动产物路径（老版本污染的快照）时只告警不删。
    if ! _is_removable_path "$filepath"; then
        log "  WARNING: refusing to remove non-driver path: ${filepath}"
        skipped_count=$((skipped_count + 1))
        continue
    fi

    if [ -f "$filepath" ]; then
        if rm -f "$filepath"; then
            removed_count=$((removed_count + 1))
        else
            log "  WARNING: failed to remove ${filepath}"
            failed_count=$((failed_count + 1))
        fi
    else
        missing_count=$((missing_count + 1))
    fi
done < "${MANIFEST}"

# Clean up empty directories left behind in monitored paths
for dir in /usr/lib/cups /usr/share/cups /usr/share/ppd /lib/firmware /usr/share/foomatic; do
    if [ -d "$dir" ]; then
        find "$dir" -type d -empty -delete 2>/dev/null || true
    fi
done

# Remove the driver data directory
log "Removing persisted driver data..."
rm -rf "${DRIVER_DATA}"

# Update shared library cache
log "Updating shared library cache..."
ldconfig 2>/dev/null || true

log "Driver '${DRIVER_NAME}' removed successfully."
log "  Files removed: ${removed_count}"
if [ $missing_count -gt 0 ]; then
    log "  Files already missing: ${missing_count}"
fi
if [ $skipped_count -gt 0 ]; then
    log "  Files skipped (outside driver whitelist, kept for safety): ${skipped_count}"
fi
if [ $failed_count -gt 0 ]; then
    log "  Files failed to remove: ${failed_count}"
fi
