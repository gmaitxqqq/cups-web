# 容器启动流程深度说明

> 本文档是 [AGENTS.md](../AGENTS.md)「容器启动流程」章节的深度补充，收录 entrypoint 各步骤的设计理由、cupsd watchdog 的 127 死循环案例与 restore-drivers 的容错设计。

单容器形态下 `entrypoint.sh` 是 `ENTRYPOINT`，顺序如下（编号与文件中的注释块一致）：

1. **`restore-drivers`**：恢复 `.drivers` 快照里的驱动文件
2. **CUPS 管理员用户**：`/etc/shadow` 里没有 `$CUPSADMIN` 时 `useradd -r -G lpadmin` + `chpasswd`，并按 `$TZ` 配 tzdata
3. **CUPS 配置还原**：`/etc/cups/cupsd.conf` 不存在（挂了空卷）时从镜像内的 `/etc/cups-bak/` 复制
4. **HP 1020 PPD 的 Letter → A4 一次性修补**（issue #48）：只改 `*Product` + `*FoomaticIDs` 双重指纹命中、且当前默认仍是 Letter、且 `*PageSize A4` 存在的存量 PPD，改前备份 `.bak-cupsweb-issue48`
5. **HP host-based 固件上传**：容器内没有 udev daemon，手动喂 `SUBSYSTEM=usb` 调用 foo2zjs 上游的 `/usr/lib/udev/hplj{1000,1005,1018,1020}` + `hpljP{1005,1006,1505}`，**后台跑**（上游脚本里有 `sleep 3`，同步调用会拖慢 cupsd 启动），日志在 `/var/log/cups/hp-firmware.log`
6. **dbus + avahi + ipp-usb**：后台拉起，用于 driverless / IPP Everywhere 发现；三者均允许缺失/失败，不影响 cupsd
7. **cupsd + watchdog**（见下方专门说明）
8. **等 cupsd 就绪**：`lpstat -r` 轮询，最多 30 次 × 1s
9. **AirPrint A4 `media-ready` 修补**（issue #82）：后台对命中的 HP 1020 队列执行 `lpadmin -p NAME -o media=iso_a4_210x297mm`。iOS 打印面板的纸张候选读的是 `media-ready`/`media` 而不是 `media-default`，所以第 4 步只改 PPD 默认值还不够
10. **`exec /cups-web`**：作为 PID 1 前台运行（`exec` 替换父进程不会杀掉已 fork 的后台子 shell）

## ⚠️ cupsd 必须在 watchdog 子 shell **内部前台**启动

bash 的 `wait` 只能等待**当前 shell 自己的子进程**。老实现在主 shell 里 `cupsd -f &` 拿 PID，再在 watchdog 子 shell 里 `wait $CUPSD_PID`——那个 PID 对子 shell 来说是**兄弟进程**，bash 立刻返回 **127**（not a child of this shell）而不阻塞（老代码还用 `|| true` 把这个错误吞了）。于是循环秒进下一轮 → `sleep 2` → 又 fork 一个 cupsd → 631 端口已被占用、新进程秒退 → **每 2 秒一次重启风暴，日志刷满**。

正确形态（当前实现）：`/usr/sbin/cupsd -f` 写在 `( while true; … ) &` 子 shell **里面**、以前台方式跑。这样它是子 shell 的直接子进程，子 shell 会阻塞到 cupsd 真正退出，`$?` 也是 cupsd 的真实退出码；整个子 shell 再 `&` 到后台，不阻塞后续启动步骤。

**🚫 不要把 `/usr/sbin/cupsd -f` 挪到子 shell 外面再配 `wait`** —— 那就是上面那个 127 死循环。

### fast-fail 退避

`CUPSD_MIN_UPTIME=5`、`CUPSD_MAX_FAST_FAILS=5`。存活 < 5s 记一次"短命退出"，连续 5 次就打印醒目中文错误（提示大概率是 `cupsd.conf` 语法错或 631 端口被占用）并 `break` 彻底放弃重启；只要有一次存活超过 5s（说明是偶发崩溃而非配置问题）计数器清零。

## ⚠️ `restore-drivers` 必须永远 `exit 0`

`entrypoint.sh` 是 `set -e`，而 `restore-drivers` 是它的**第一步**。驱动恢复是"尽力而为"的操作：快照可能被旧版本写坏、目标路径可能被别的包占成目录、挂载可能只读。这些**都不该阻塞启动**——一旦容器起不来，用户连 Web UI 都进不去，**没法自救卸载那个坏驱动**。

所以是双层保险：`restore-drivers.sh` 本身**故意不用 `set -e`**（只 `set -uo pipefail`），逐文件记账、结尾汇总 `total_errors` / `total_skipped` 并打印"驱动恢复不完整，但不阻塞容器启动；可在 Web UI 里卸载后重新安装该驱动"，最后**无条件 `exit 0`**；`entrypoint.sh` 那一行再补一层 `|| echo "[entrypoint] WARN: restore-drivers 部分失败，继续启动"` 兜底，防它因意外信号/非零退出把 `set -e` 的 entrypoint 带崩。
