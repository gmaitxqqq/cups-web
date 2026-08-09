#!/bin/bash
# 容器启动时把 .drivers 快照里的驱动文件恢复回系统路径。
#
# ⚠️ 故意**不使用 `set -e`**：本脚本是 entrypoint 的第一步，任何一次
# mkdir/cp 失败（快照被旧版本写坏、目标路径已被别的包占成目录、挂载只读……）
# 都不应该中断整个恢复流程，更不能让容器起不来——用户连 Web UI 都进不去时
# 就没法自救删掉坏驱动了。策略是"尽力而为 + 汇总报错"。
set -uo pipefail

DRIVERS_BASE="/opt/cups-drivers"
DATA_DIR="${DRIVERS_BASE}/data"

# ── 恢复白名单（安全网）─────────────────────────────────────────────────
# 跑过旧版本的用户手上已经存在被污染的快照：老 driver-install 把
# `dpkg -L build-essential` 之类编译依赖的**全部文件**写进了 manifest，
# 快照里因此躺着 /usr/bin/gcc、/usr/share/man/**、libc6-dev 的头文件与库。
# 无脑 `cp -a` 回去会用几个月前的旧二进制覆盖系统当前文件，属于实打实的破坏。
# 所以恢复前必须校验路径落在"驱动产物"目录内，不在的一律跳过并告警。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        # dpkg-architecture 属于 dpkg-dev 包，runtime 镜像没装；用 glob 探测。
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

_is_restorable_path() {
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

# Exit silently if no driver data directory exists
if [ ! -d "${DATA_DIR}" ]; then
    exit 0
fi

# Check if there are any driver subdirectories
shopt -s nullglob
driver_dirs=("${DATA_DIR}"/*)
shopt -u nullglob

if [ ${#driver_dirs[@]} -eq 0 ]; then
    exit 0
fi

restored_count=0
restored_drivers=()
total_errors=0
total_skipped=0

for driver_dir in "${driver_dirs[@]}"; do
    [ -d "$driver_dir" ] || continue

    driver_name="$(basename "$driver_dir")"
    manifest="${driver_dir}/manifest.txt"

    if [ ! -f "$manifest" ]; then
        continue
    fi

    echo "[restore-drivers] Restoring driver: ${driver_name}"
    file_count=0
    missing_count=0
    error_count=0
    skipped_count=0

    while IFS= read -r filepath; do
        [ -z "$filepath" ] && continue

        # 老快照可能包含系统路径（见文件头注释），拒绝恢复以免覆盖系统文件。
        if ! _is_restorable_path "$filepath"; then
            echo "[restore-drivers]   WARNING: refusing to restore non-driver path: ${filepath}"
            skipped_count=$((skipped_count + 1))
            continue
        fi

        source_file="${driver_dir}${filepath}"

        if [ ! -f "$source_file" ]; then
            missing_count=$((missing_count + 1))
            continue
        fi

        # Create parent directory if needed（失败只记账，继续下一个文件）
        parent_dir="$(dirname "$filepath")"
        if [ ! -d "$parent_dir" ]; then
            if ! mkdir -p "$parent_dir" 2>/dev/null; then
                echo "[restore-drivers]   WARNING: mkdir failed: ${parent_dir}"
                error_count=$((error_count + 1))
                continue
            fi
        fi

        # Copy preserving permissions, ownership, and timestamps
        if cp -a "$source_file" "$filepath" 2>/dev/null; then
            file_count=$((file_count + 1))
        else
            echo "[restore-drivers]   WARNING: copy failed: ${filepath}"
            error_count=$((error_count + 1))
        fi
    done < "$manifest"

    echo "[restore-drivers]   Restored ${file_count} files"
    if [ "$missing_count" -gt 0 ]; then
        echo "[restore-drivers]   WARNING: ${missing_count} files missing from backup"
    fi
    if [ "$skipped_count" -gt 0 ]; then
        echo "[restore-drivers]   WARNING: ${skipped_count} files skipped (outside driver whitelist)"
    fi
    if [ "$error_count" -gt 0 ]; then
        echo "[restore-drivers]   WARNING: ${error_count} files failed to restore"
    fi

    total_errors=$((total_errors + error_count))
    total_skipped=$((total_skipped + skipped_count))
    restored_count=$((restored_count + 1))
    restored_drivers+=("$driver_name")
done

# Update shared library cache if any drivers were restored
if [ "$restored_count" -gt 0 ]; then
    echo "[restore-drivers] Updating shared library cache..."
    ldconfig 2>/dev/null || true
    echo "[restore-drivers] Restored ${restored_count} driver(s): ${restored_drivers[*]}"
fi

if [ "$total_errors" -gt 0 ] || [ "$total_skipped" -gt 0 ]; then
    echo "[restore-drivers] Summary: ${total_errors} file(s) failed, ${total_skipped} file(s) skipped by whitelist."
    echo "[restore-drivers] 驱动恢复不完整，但不阻塞容器启动；可在 Web UI 里卸载后重新安装该驱动。"
fi

# 始终以 0 退出：恢复是尽力而为的，失败已在上面汇总打印，绝不阻塞 entrypoint。
exit 0
