# cups-web 定制版（免登录 · 发票修复 · 图片打印排版增强）

本仓库是基于 [hanxi/cups-web](https://github.com/hanxi/cups-web) 的定制 fork，面向家庭 / 内网打印机场景，在原版基础上做了若干实用增强。所有改动已编译进同一个镜像，开箱即用。

---

## 一、功能特性（相比原版）

| 特性 | 说明 |
|---|---|
| **免登录** | 设置 `AUTH_DISABLED=true` 后直接打开主界面，无需账号密码（仅限内网） |
| **发票金额修复** | 打印 / 合成前自动用 Ghostscript 嵌入缺失字体，解决发票 PDF 金额空白问题 |
| **图片手动缩放** | 图片支持「自动适应」或「手动缩放 1–100%」，进度条 / 输入框均可调 |
| **水平对齐** | 图片支持 **居中 / 居左 / 居右** |
| **垂直对齐** | 图片支持 **居中 / 靠上 / 靠下**（纵向图片也能选，不再只能居中） |
| **服务器侧预览** | 预览由后端渲染成 PNG 返回，绕过 pdf.js，所有 PDF / 图片预览稳定 |
| **版本号显示** | 左上角显示构建版本（git SHA），方便核对当前部署是否是预期版本 |

> 图片排版相关选项（缩放方式、水平对齐、垂直对齐）**仅在选择图片文件时出现**，普通 PDF / Office 文档走原有排版逻辑，互不影响。

---

## 二、快速开始（Docker 部署）

把下面这份 `docker-compose.yml` 放到一个目录，执行 `docker compose up -d` 即可。

关键两点：**换镜像地址** + **加 `AUTH_DISABLED=true`**。

```yaml
services:
  cups:
    # 你自己构建的镜像（也可改回 hanxi/cups-web:latest 还原原版）
    image: ghcr.io/gmaitxqqq/cups-web:latest
    container_name: cups
    user: root
    security_opt:
      - apparmor:unconfined
    environment:
      - CUPSADMIN=${CUPSADMIN:-print}
      - CUPSPASSWORD=${CUPSPASSWORD:-print}
      - TZ=${TZ:-Asia/Shanghai}
      - AUTH_DISABLED=true          # ← 免登录直接打开
    ports:
      - "631:631"
      - "1180:8080"
    volumes:
      - ./.etc:/etc/cups
      - ./.data:/data
      - ./.uploads:/uploads
      - ./.drivers:/opt/cups-drivers/data
      - /dev/bus/usb:/dev/bus/usb
      - /run/udev:/run/udev:ro
    device_cgroup_rules:
      - 'c 189:* rmw'
    restart: unless-stopped
```

打开 `http://<你的机器IP>:1180` —— 应该**直接进入主界面，没有登录框**。

> **镜像大小说明**：完整镜像约 **665MB**（未压缩）。你在 GitHub 仓库页看到的「200 KB」是**源码仓库**体积，不是镜像——镜像拉下来几百 MB 属正常。
> 国内访问 `ghcr.io` 偶尔会被限速，若 `docker compose pull` 长时间卡住，可参考第六节「本地交叉编译 + 局域网中转部署」。

如果还想保留原版账号密码登录，删掉（或注释掉）`AUTH_DISABLED=true` 即可，行为与原版一致。

---

## 三、图片打印排版（本版重点增强）

上传一张图片（或勾选「合并多张图片」）后，「打印参数」面板会出现一个 **「图片排版」** 区块，包含三组控制：

### 1. 缩放方式
- **自动适应（默认）**：按图片原始比例等比缩放到页边距内最大可放区域，不会变形、不会溢出页面。
- **手动缩放**：在「自动适应」尺寸基础上再乘以 **1%–100%** 的百分比（100% = 与自动适应同等大小，可向下缩小到 1%）。配合右侧的数值输入框或滑块实时调整，预览即时刷新。

> 之前「放一张图就默认铺满全屏、缩放没用」的问题已修复：现在默认是保持比例的「自动适应」，手动缩放让你精确控制最终大小。

### 2. 水平对齐
| 选项 | 效果 |
|---|---|
| 居中 | 图片在可用宽度内水平居中（默认） |
| 居左 | 图片贴左边距 |
| 居右 | 图片贴右边距 |

### 3. 垂直对齐
| 选项 | 效果 |
|---|---|
| 居中 | 图片在可用高度内垂直居中（默认） |
| 靠上 | 图片贴上边距 |
| 靠下 | 图片贴下边距 |

> 纵向图片（如长截图、证件照）现在可以选「靠上 / 靠下」，不再被强制垂直居中。

三组选项任意组合，例如「手动缩放 50% + 居左 + 靠上」可把小图固定到页面左上角。

---

## 四、发票金额修复

**根因**：发票 PDF 里金额本身存在、颜色也是黑色，但金额用的 `STSong-Light` 字体**没有嵌入** PDF 文件。
原版为了性能改成「PDF 原样直接丢给 CUPS 打印」，容器里找不到这个字体 → 金额渲染成空白。
（普通打印、发票 2-up 合成两条路径都中招。）

**修复**：让「普通打印」和「发票 2-up 合成」两条路径在发送前，都先调用项目已有的 `normalizePDF()`
（Ghostscript `-dEmbedAllFonts=true`），把字体强制嵌入后再打印。

> Docker 镜像在构建时已装好 **Ghostscript + CJK 字体 + Windows 中文字体（宋体/黑体/楷体/仿宋）+ cidfmap 映射**，
> 所以未嵌入的 `STSong-Light` 会被正确替换并嵌入真字体，金额即可正常打印。
> **你本地不需要额外装字体**——只要用本仓库构建出来的镜像即可。

---

## 五、怎么验证改动都生效

### 验证 1：免登录
1. 浏览器清掉该站点的 cookie（或用无痕窗口）；
2. 打开 `http://<IP>:1180`；
3. 应直接进入打印主界面，没有登录页、不要求输入账号密码。

### 验证 2：发票金额能打印
1. 上传那张发票 PDF，普通打印或「发票打印（2-up 合成）」；
2. 看容器日志（`docker compose logs cups`）：
   - 出现 `pdf normalized (method=ghostscript) before printing to embed fonts` 代表字体已嵌入修复生效；
   - 打印出来的纸上金额数字应正常显示。

### 验证 3：图片缩放 + 对齐
1. 上传一张图片，确认出现「图片排版」区块；
2. 把「缩放方式」切到「手动缩放」，拖动滑块，预览应实时变大/变小；
3. 切换「水平对齐」（居中/居左/居右）与「垂直对齐」（居中/靠上/靠下），预览中图片落点应随之改变；
4. 左上角版本号应为当前构建的 SHA（如 `2b9c113`），确认部署版本无误。

---

## 六、构建与部署

### 方式 A：用 GitHub Actions 自动构建（推荐，已配好）
代码推到 `master` 分支后，`.github/workflows/build.yml` 会自动：
1. 构建镜像（单平台 `linux/amd64`）；
2. 登录 `ghcr.io` 并推送为 `ghcr.io/gmaitxqqq/cups-web:latest` 以及 `sha-<短哈希>` 标签。

查看构建进度：`https://github.com/gmaitxqqq/cups-web/actions`

### 方式 B：本机手动构建
前置要求：
- **Go 1.26**（由 `go.mod` 指定，`go.dev/dl` 下载）
- **Docker**（构建过程会 `docker build` 出镜像；镜像内会自动装 Ghostscript + 字体，**本机不必单独装 Ghostscript**）

```bash
# 在本仓库根目录
docker build -t cups-web:local .

# 跑起来（免登录）
AUTH_DISABLED=true docker run --rm -p 1180:8080 -p 631:631 \
  -e AUTH_DISABLED=true cups-web:local
```

### 方式 C：本地交叉编译 + 局域网中转部署（ghcr.io 被限速时）
当生产机从 `ghcr.io` 拉取镜像极慢（实测可能只有数 KB/s）时，可绕过镜像仓库，直接把编译好的二进制传过去：

```bash
# 1) 前端构建（node 已装好依赖时）
cd frontend && npm run build && cd ..

# 2) 交叉编译静态 Linux 二进制（前端经 go:embed 打进二进制，无需容器）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w -X main.Version=$(git rev-parse --short HEAD)" \
  -o cups-web-linux ./cmd/server

# 3) 通过 scp 传到生产机，再打一个只替换 /cups-web 的极小镜像
scp cups-web-linux root@<生产机IP>:/tmp/
ssh root@<生产机IP> 'docker cp /tmp/cups-web <容器名>:/cups-web && docker restart <容器名>'
```

> 该方式依赖 `CGO_ENABLED=0`（Dockerfile 中已设置），生成的纯静态二进制不依赖宿主机 libcups，
> 直接在现有容器里替换即可生效，**完全不碰外网镜像仓库**。

### 还原成官方原版
把 `docker-compose.yml` 里的镜像改回 `hanxi/cups-web:latest`，并删掉（或注释掉）`AUTH_DISABLED=true`，再 `docker compose up -d` 即可。

---

## 七、环境变量一览

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AUTH_DISABLED` | 未设置（需登录） | `true` 时免登录直接打开主界面（仅限内网） |
| `CUPSADMIN` | `print` | CUPS 管理员账号 |
| `CUPSPASSWORD` | `print` | CUPS 管理员密码 |
| `TZ` | `Asia/Shanghai` | 容器时区 |
| `COOKIE_SECURE` | `false` | HTTPS 部署时设 `true`，让 cookie 带 Secure 属性 |

> ⚠️ **安全提示**：`AUTH_DISABLED=true` 开启后 `/api/admin`（用户管理 / 驱动管理 / 系统设置）也全部开放。
> 请**只在可信内网**使用，不要把这个端口暴露到公网。
