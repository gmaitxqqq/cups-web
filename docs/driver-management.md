# 驱动管理深度说明

> 本文档是 [AGENTS.md](../AGENTS.md)「驱动管理」章节的深度补充，收录持久化原理、白名单翻车案例、EXIT trap 约定由来、架构探测历史、上传机制与 `lpinfo` 解析细节。规则速查（目录约定表、退出码表、ALLOW/DENY 速查）请回 AGENTS.md。

相关文件：`cmd/server/driver_handlers.go`、`cmd/server/driver_registry.go`、`scripts/driver/*.sh`、`frontend/src/views/DriversView.vue`（前端「驱动」页，路由 `/drivers`，仅 admin 可见）。

## 持久化原理（为什么不需要 `CAP_SYS_ADMIN`）

`driver-install.sh` 的流程是**纯文件系统 diff**，不涉及任何 overlay/mount 魔法：

1. 安装前对 `MONITORED_DIRS` 做 `find -type f` 快照（`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/lib/firmware`、`/usr/share/foomatic`，外加探测到的 multiarch 目录 `/usr/lib/<triplet>`），同时 `dpkg --get-selections` 记录包状态
2. 跑 `install-<name>.sh`（`export CUPS_AIO=1`）
3. 安装后再快照，`comm -13` 求出新增文件；再把"本次新装的 dpkg 包"（`dpkg -L` 展开）里**通过白名单的**文件也并进来
4. 白名单过滤 + 去重 → 逐个 `cp -a` 到 `/opt/cups-drivers/data/<driver>/<绝对路径>` → 写 `manifest.txt` + `metadata.txt` → `ldconfig`
5. 容器重启时 `entrypoint.sh` 第一步跑 `restore-drivers`，逐行读 manifest，`mkdir -p` 父目录后 `cp -a` 回系统路径，最后 `ldconfig`

## ⚠️ manifest 白名单：为什么必须存在，且三处都要有

老实现对 dpkg 来源的文件**完全没过滤**——AIO 模式下编译型驱动会现场 `apt-get install build-essential`，于是 `gcc` / `binutils` / `libc6-dev` 全被算作"新装包"，`/usr/bin/gcc`、`/usr/share/man/**`、`/etc/**` 一股脑写进 manifest。而 `driver-remove` 是**按 manifest 逐条 `rm -f`** 的：**卸载一次驱动就把系统 gcc/binutils 和一堆系统库删了，容器直接残废。** `restore-drivers` 同理会用几个月前的旧二进制 `cp -a` 覆盖系统当前文件。

现在 `driver-install.sh` / `driver-remove.sh` / `restore-drivers.sh` **三个脚本各有一份同样的白名单**（函数名分别是 `_is_monitored_path` / `_is_removable_path` / `_is_restorable_path`），规则完全一致：

**ALLOW（必须落在其中之一）**：`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/usr/share/foomatic`、`/lib/firmware`、`/usr/lib/firmware`，以及探测到的 `/usr/lib/<multiarch-triplet>`（闭源驱动的 `.so` 会装在这里）。

**DENY（即使落在 ALLOW 内也一律排除）**：
`/usr/bin/*`、`/usr/sbin/*`、`/bin/*`、`/sbin/*`、`/usr/local/bin/*`、`/usr/local/sbin/*`、`/etc/*`、`/var/*`、`/usr/include/*`、`/opt/cups-drivers/*`、`/tmp/*`、`/usr/share/{doc,man,locale,info}/*`、`/usr/share/cups/doc-root/*`（CUPS 自带 Web UI 静态资源，不是驱动产物）、`*/pkgconfig/*`、`*.a`、`*.o`、`*.la`。

> 驱动真正需要的可执行文件都在 `/usr/lib/cups/filter/` 与 `/usr/lib/cups/backend/`，**绝不会**出现在 `/usr/bin` 或 `/usr/sbin`——所以排除这些目录是安全的。

**🚫 不要因为"install 侧已经过滤了"就删掉 remove / restore 侧的守卫。** 跑过旧版本的用户手上已经存在**被污染的 `.drivers` 快照**，那些老 manifest 里就躺着 `/usr/bin/gcc`。remove/restore 两侧的守卫是给这批存量快照兜底的，必须**永久保留**；命中时的行为是**只告警并跳过，绝不 `rm` / 绝不 `cp`**，并在结尾汇总 `skipped_count`。

## ⚠️ AIO 编译脚本的「单一 EXIT trap」约定

bash 对同一信号**只保留最后一次注册的 handler**。老实现里编译型脚本注册了两个 `trap ... EXIT`（一个卸载 AIO 编译依赖、一个删临时构建目录），后注册的直接把前一个覆盖掉，两种翻车都真实发生过：

- `install-canon-capt.sh` / `install-foo2zjs-firmware.sh`：**AIO 清理 trap 被覆盖** → `build-essential` / `gcc` 永不卸载 → 被 `driver-install` 当成"新装包"，整条工具链的文件（在加白名单之前）被写进 manifest → 卸载驱动时删掉系统 gcc
- `install-escpr2.sh`：**删临时目录的 trap 被覆盖** → 几十 MB 的构建目录泄漏在容器 `/tmp`

现在这三个脚本统一成**全局唯一一个** `trap _cleanup EXIT`，`_cleanup()` 内部按分支做所有清理（`rm -rf "${BUILD_DIR}"` + `_AIO_DEPS_INSTALLED=1` 时 `apt-get purge -y --auto-remove ${BUILD_DEPS}`），并且 `local rc=$?` / `return $rc` 保住原退出码。**新增编译型驱动脚本时必须遵守这条约定**：一个脚本只允许一个 EXIT trap，所有清理写进那个函数里。

另一条相关约定：`_cleanup` 里 AIO 模式下**只 `apt-get clean`，绝不 `rm -rf /var/lib/apt/lists/*`**。运行中的容器清掉 apt 索引后，紧接着装第二个驱动就会因为没有包索引而 `apt-get install` 失败（"连续装两个驱动直接翻车"）。各 `install-*.sh` 末尾清索引的语句也统一加了 `if [ "${CUPS_AIO:-0}" != "1" ]` 守卫——只有构建期才为省体积清。

## 退出码约定的由来

`install-*.sh` 共同遵守（`driver-install.sh` 里以注释形式写死）：

| 退出码 | 含义 | `driver-install` 的行为 |
| --- | --- | --- |
| `0` | 安装成功 | 继续做 diff / 写 manifest |
| `3` | **当前 CPU 架构不支持该驱动**（厂商未提供该架构二进制） | 打印中文说明、`discard_driver_data`（删掉可能已创建的空数据目录）、**不写 manifest**、以 3 退出 |
| 其他非零 | 真正的失败（下载 / 编译 / dpkg 失败） | 同样 `discard_driver_data` 后原样透传退出码 |

为什么必须区分：老实现里"架构不支持"分支是 `exit 0`，`driver-install` 照常写 `manifest.txt`，Web UI 于是显示**「已安装」**，用户以为驱动可用。当前 `exit 3` 的脚本：`install-gutenprint.sh`（armhf / armel）、`install-canon-ufr2.sh`（非 amd64/arm64）、`install-epson-cn.sh`（非 amd64）、`install-konica-bizhub.sh`（非 amd64/arm64）。

还有一条同源约定：**退出码 0 但一个新文件都没产生，也视为失败。** `driver-install.sh` 在 diff+过滤后如果 `new-files.txt` 为空，会打印明确错误、`discard_driver_data`、`exit 1`，**拒绝写 manifest.txt**——否则又会出现"UI 显示已安装、实际什么都没装"。

## 架构探测约定（为什么不用 `dpkg-architecture`）

runtime 镜像**没有 `dpkg-dev`**，所以：

- **不能用 `dpkg-architecture`**。老代码用它取架构和 multiarch triplet，在 arm 上命令直接不存在 → 静默回落到硬编码的 `x86_64-linux-gnu`，导致监控目录是个不存在的路径，闭源驱动的 `.so` 变更**完全抓不到**；`driver-list.sh` 那边则回落到 `uname -m` 的 `aarch64`，和 `metadata.txt` 里 `arch=arm64` 永远不相等，于是每个已装驱动都被误报"架构不一致"。
- 统一用 **`dpkg --print-architecture`**（`dpkg` 本体一定在）拿 Debian 架构名 `amd64` / `arm64` / `armhf`。`driver-install.sh::detect_deb_arch` 在连 `dpkg` 都没有时才退到 `uname -m`，且只作诊断展示。
- multiarch 库目录用 `detect_multiarch_libdir()`：`dpkg-architecture` 存在就用（构建期/开发机）→ 否则 glob `/usr/lib/*-linux-gnu*` → 都拿不到就**返回空串，调用方跳过该目录**（绝不使用猜错的路径）。
- Go 侧 `driver_registry.go::currentDebArch()` 把 `GOARCH` 映射到**同一套 Debian 命名**（`amd64`→`amd64`、`arm64`→`arm64`、`arm`→`armhf`、`386`→`i386`，未知架构原样返回），这样 `DriverMeta.Arch`（写的是 `amd64`/`arm64`/`armhf`/`all`）、`metadata.txt` 的 `arch=`、脚本里的判断三方才能直接比较。二进制是 `CGO_ENABLED=0` 交叉编译的，`GOARCH` 就是运行架构。

## 上传自定义驱动

### `.ppd`

校验首 256 字节含 `*PPD-Adobe` → 写 `/usr/share/cups/model/custom/<name>.ppd` → 在 `custom-ppd/` 下存一份同结构副本 → **追加 manifest**（`appendManifestLine` 幂等去重）+ 写 `metadata.txt`。**能被 `restore-drivers` 恢复。**

### `.deb`

`dpkg -i` 失败时 `apt-get install -y -f --no-install-recommends` 补依赖，**然后必须再 `dpkg -i` 一次**（老实现修完依赖就返回，等于白跑一趟 apt）。成功后只把原件归档到 `custom-deb/packages/`，**故意不写 `manifest.txt`**——`restore-drivers` 是按 manifest 里的绝对路径 `cp -a` 回文件系统的，对 `.deb` 毫无意义（真正的安装动作在 maintainer script 里），写了只会把 `.deb` 文件拷到荒谬的路径。因此 **`.deb` 上传不会随容器重启自动恢复，重启后需要手动重装**；`GET /api/admin/drivers` 会连同 `customDebNotice` 一起把这句话回给前端，`upload` 响应里也有 `warning` 字段。

### 🔐 安全风险面（有意保留的管理员能力）

上传 `.deb` 等价于**容器内 root RCE**——dpkg 会以 root 执行包里的 maintainer script（`preinst`/`postinst`…），可以做任何事。该接口受 `RequireSession` + `RequireAdmin` + `ValidateCSRF` 三重保护，且每次上传都把上传者用户名写进日志用于审计。**部署时请把管理员账号密码视作等同于容器 root 凭据。**

### 文件名与大小上限

- 文件名一律经 `safeUploadFilename` 收敛（先手工切掉 Windows 反斜杠路径，再 `filepath.Base`，拒绝 `.`/`..`/隐藏文件/含分隔符），不依赖标准库 multipart 恰好做过 `Base`。
- ⚠️ **大小上限的正确写法**：`r.ParseMultipartForm(n)` 的 `n` 是 **maxMemory（内存缓冲上限）而不是请求体上限**——超出部分 Go 会静默 spool 到临时文件，所以单靠它**拦不住**超大上传（本接口原先写 `ParseMultipartForm(50 << 20)` + 注释 `// 50 MB limit`，就是对 Go 语义的误解）。真正的硬上限必须 `r.Body = http.MaxBytesReader(w, r.Body, driverUploadMaxBytes)` 包一层，之后 `ParseMultipartForm` 才会在超限时报错；`maxMemory` 另外给个小值（本接口 8MB）让大包落盘而不是整个进内存。

> 📌 遗留待办：`print_handlers.go` / `convert_handler.go` / `estimate_handler.go` / `compose_handler.go` 目前都是 `ParseMultipartForm(512 << 20)`，同样把 maxMemory 当成了上限——含义是「允许把最多 512MB 塞进内存」且**没有任何请求体硬上限**。这几处属于本次改动之外的历史代码，未一并修改；后续收敛时同样应改成 `MaxBytesReader` + 小 maxMemory。

## `lpinfo` 检测：格式假设与型号解析优先级

`GET /api/admin/drivers/detect` 用的是 **`lpinfo -l -v` 长格式**。老代码调的是短格式 `lpinfo -v`（每行只有 `<class> <uri>` 两列，**根本没有厂商型号**）却按长格式去解析引号里的 make-and-model，后果是连锁的：网络打印机型号恒为空 → `checkHasDriver(" ")` 因 `strings.Contains(desc, " ")` 恒为 `true` → `findBestPPD("")` 返回 `lpinfo -m` 的**第一条 PPD**，给打印机套上一个完全无关的驱动。

现在的实现（`parseLpinfoDevices` / `buildDetectedPrinter`）：

- 以 `Device:` 开块，块内每行按第一个 `=` 拆 key/value，未知 key 忽略——刻意宽容，某版本改了字段顺序或缩进也不会整体解析失败
- **过滤裸 backend 行**：`lpinfo` 还会输出 backend 自身（`network socket` / `direct hp` / `ipp` / `lpd` / `beh` / `dnssd`…），这类行第二列不是完整 URI（**不含 `://`**），必须丢掉，否则会凭空多出 5~6 台"假打印机"；同时跳过 `cups-pdf` / `cups-brf` / `file:///dev/null` 等虚拟设备
- **型号解析优先级（按可信度）**：`make-and-model` → `device-id` 的 `MFG`/`MDL`（含 `MANUFACTURER`/`MODEL` 长写法，并去掉型号里重复的厂商前缀）→ `info` → URI 路径（仅 `usb://厂商/型号` 能解析出来）。`splitMakeAndModel` 把 CUPS 填的 `Unknown` 等价于空；只有一个词时当型号处理
- **空型号短路**：打分引擎（`ppd_match.go::ScorePPDCandidates`）里 `len(compact) < 2` 时不产出任何非 generic 候选（连"单字符解析残渣"一起挡掉）。⚠️ **历史实现挑不到 PPD 时不传 `-m`，注释声称"让 CUPS 走 driverless / IPP Everywhere"——这是错的。** `lpadmin` 不传 `-m` 建的是 **raw 队列**（无 PPD），不是 IPP Everywhere。raw 队列拿不到 PPD 选项 → `/api/printer-info` 的 `mediaSourceSupported` 为空 → 前端进纸盒下拉消失。现在的实现：无候选且支持 driverless → 显式 `-m everywhere`；无候选且不支持 → **报错**，绝不静默建 raw
- PPD 匹配走打分引擎（`ppd_match.go` 纯函数 + `ppd_query.go` 副作用层），`lpinfo -m` 走 TTL 10 分钟缓存（装卸驱动后显式失效）

## 一键设置（`/drivers/setup`）的步骤

请求字段名以 `/detect` 的响应为准：`{deviceUri, driverName?, manufacturer?, model?, deviceId?, ppdUri?, printerName?, allowRaw?}`。

任务内部依次：驱动未装则 `driver-install`（以 `manifest.txt` 存在与否判断）→ 确定厂商/型号（优先级：`req.Manufacturer/Model` → `parseDeviceID(deviceId)` → `parseDeviceURI(uri)`）→ **三态决策树**决定 `-m`（显式 `ppdUri` > `everywhere` > 自动 Top-1 > 报错）→ `uniquePrinterName` 去重队列名 → `lpadmin -p <name> -E -v <uri> [-m <ppd>]` → 默认 A4 → **验证**（`lpstat -p` + `lpoptions -l`，PPD 未生效时 `isNew` 队列回滚 `lpadmin -x`）。

## 异步任务模型（`driver_handlers.go`）

`install` / `remove` / `setup` 三个接口**必须是异步**的，不要"简化"回同步：

- `main.go` 的 `http.Server` 是全局 `WriteTimeout = 120s`，而编译型驱动（`canon-capt`、`foo2zjs-firmware`、arm64 上的 `escpr2`）在容器里现场 `apt-get install build-essential` + `make`，几分钟到十几分钟都正常。
- 同步实现里用 `exec.CommandContext(r.Context(), ...)`：连接一超时，请求 context 被 cancel，**`CommandContext` 会直接 kill 掉正在 `make` 的进程**，留下半编译产物（以及没被 EXIT trap 卸载干净的编译依赖），客户端还什么都拿不到。
- 现在的实现：handler 立刻 `202` 返回 `jobId`，真正的命令跑在 `context.Background()` 派生的 goroutine 里（硬超时常量 `driverJobTimeout = 30 * time.Minute`），前端轮询 `jobs/{id}` 拿增量日志。命令的 stdout/stderr 都写进加锁的 `safeBuffer`，所以轮询能看到"正在编译"的实时输出而不是等结束才一次性拿到。

**单飞（single-flight）**：`startDriverJob` 在锁内扫一遍任务表，**同一时刻只允许一个驱动任务在跑**——apt/dpkg 自身有全局锁，并发安装只会互相失败，报错还很难懂。已有任务运行中时接口返回 `409` 并带上正在跑的 `jobId`，前端可以直接切过去轮询：

```json
{ "error": "已有驱动任务正在执行，请等待其完成后重试", "jobId": "…" }
```

`.deb` 上传（同步执行 `dpkg -i`）也走同一把逻辑锁：`runningDriverJobID() != ""` 时直接 `409`，避免和后台任务抢 dpkg 锁。

**任务保留期**：`driverJobRetention = time.Hour`，`pruneDriverJobsLocked` 在每次新建任务时清掉完成超过 1 小时的旧任务，防止长期运行的进程无限累积（任务只存在内存里，进程重启即丢，前端已按此假设做超时提示）。`jobId` 是 `randomToken()` 生成的不透明大写 base32 串，路由约束因此放宽成 `{id:[A-Za-z0-9]+}`。
