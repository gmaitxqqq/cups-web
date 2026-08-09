# cups-web 定制版（免登录 + 发票金额修复）

本仓库基于 [hanxi/cups-web](https://github.com/hanxi/cups-web)，针对两个需求做了改动，并配好了一键构建+推送镜像的 GitHub Actions 工作流。

---

## 一、这个版本改了什么

### 1. 免登录，直接打开主界面
新增环境变量 **`AUTH_DISABLED`**：

- 设为 `true`/`1`/`on`/`yes` 时，所有登录鉴权中间件（`RequireSession` / `RequireAdmin` / `ValidateCSRF`）直接放行；
- `/api/session` 会返回一个「匿名 admin」会话，前端本来就是靠这个接口判断是否登录，**无需任何前端改动**即可直接进入打印主界面，不再有登录框。

> ⚠️ **安全提示**：开启后 `/api/admin`（用户管理 / 驱动管理 / 系统设置）也全部开放。
> 请**只在可信内网**使用，不要把这个端口暴露到公网。

### 2. 修复发票打印「金额不显示」
**根因**：你的发票 PDF 里金额本身存在、颜色也是黑色的，但金额用的 `STSong-Light` 字体**没有嵌入** PDF 文件。
cups-web 此前为了性能改成「PDF 原样直接丢给 CUPS 打印」，容器里找不到这个字体 → 金额渲染成空白。
（普通打印、发票 2-up 合成两条路径都中招。）

**修复**：让「普通打印」和「发票 2-up 合成」两条路径在发送前，都先调用项目已有的 `normalizePDF()`
（Ghostscript `-dEmbedAllFonts=true`），把字体强制嵌入后再打印。

> Docker 镜像在构建时已装好 **Ghostscript + CJK 字体 + Windows 中文字体（宋体/黑体/楷体/仿宋）+ cidfmap 映射**，
> 所以未嵌入的 `STSong-Light` 会被正确替换并嵌入真字体，金额即可正常打印。
> **你本地不需要额外装字体**——只要用本仓库构建出来的镜像即可。

---

## 二、怎么更新你现在的 Docker 部署

你现在的 `docker-compose.yml` 用的是 `hanxi/cups-web:latest`。改成下面这样即可（关键两点：**换镜像地址** + **加 `AUTH_DISABLED=true`**）：

```yaml
services:
  cups:
    # 换成你自己构建的镜像
    image: ghcr.io/gmaitxqqq/cups-web:latest
    container_name: cups
    user: root
    security_opt:
      - apparmor:unconfined
    environment:
      - CUPSADMIN=${CUPSADMIN:-print}
      - CUPSPASSWORD=${CUPSPASSWORD:-print}
      - TZ=${TZ:-Asia/Shanghai}
      - AUTH_DISABLED=true          # ← 新增：免登录直接打开
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

改完执行：

```bash
docker compose pull     # 拉取新镜像
docker compose up -d    # 重启容器
```

> **镜像大小说明**：完整镜像约 **665MB**（未压缩）。这比官方 `hanxi/cups-web:latest` 的 1.6GB 小，
> 是因为本构建只编**单平台 amd64** 且基础镜像更精简，功能完全一致（CUPS + Ghostscript + CJK/Windows 中文字体都已烤进镜像）。
> 你在 GitHub 仓库页看到的「200 KB」是**源码仓库**的体积，不是镜像——镜像拉下来是几百 MB，属正常。

打开 `http://<你的机器IP>:1180` —— 应该**直接进入主界面，没有登录框**。

> 如果还想保留原来的账号密码登录，把 `AUTH_DISABLED=true` 删掉或直接设为 `false` 即可，行为与原版一致。

---

## 三、怎么验证两处改动都生效

### 验证 1：免登录
1. 浏览器清掉该站点的 cookie（或用无痕窗口）；
2. 打开 `http://<IP>:1180`；
3. 应直接进入打印主界面，没有登录页、不要求输入账号密码。

### 验证 2：发票金额能打印
1. 上传那张发票 PDF，普通打印或「发票打印（2-up 合成）」；
2. 看容器日志（或宿主机 `docker compose logs cups`）：
   - 出现 `pdf normalized (method=ghostscript) before printing to embed fonts` 代表字体已嵌入修复生效；
   - 打印出来的纸上金额数字应正常显示。

---

## 四、想自己重新构建 / 改代码

### 方式 A：用 GitHub Actions 自动构建（推荐，已配好）
代码推到 `master` 分支后，`.github/workflows/build.yml` 会自动：
1. `docker/build-push-action` 构建镜像；
2. 登录 `ghcr.io` 并推送为 `ghcr.io/gmaitxqqq/cups-web:latest`。

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

### 还原成官方原版
把 `docker-compose.yml` 里的镜像改回 `hanxi/cups-web:latest`，并删掉（或注释掉）`AUTH_DISABLED=true`，再 `docker compose up -d` 即可。

---

## 五、环境变量一览

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AUTH_DISABLED` | 未设置（需登录） | `true` 时免登录直接打开主界面（仅限内网） |
| `CUPSADMIN` | `print` | CUPS 管理员账号 |
| `CUPSPASSWORD` | `print` | CUPS 管理员密码 |
| `TZ` | `Asia/Shanghai` | 容器时区 |
| `COOKIE_SECURE` | `false` | HTTPS 部署时设 `true`，让 cookie 带 Secure 属性 |
