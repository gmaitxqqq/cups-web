# 更新日志 / Changelog

本文件记录 cups-web 定制版各发布版本的相对改动，作为 GitHub Release Notes 的来源。

---

## v1.1.4（2026-09-20，补丁）

### 修复（Fixes）
- **界面版本号长期显示 `dev`，无法核对本地部署对应哪个 GitHub 版本**：根因为 `cmd/server/version.go` 的 `Version` 默认值是 `dev`，实际版本号依赖编译期 `-ldflags "-X main.Version=<ver>"` 注入；而手工执行的 `go build` 没有带该参数（`Makefile` 与 `Dockerfile` 都会自动注入，手工构建会漏），于是二进制里版本号始终是 `dev`。
  修复后本地构建统一走 `make build`（或显式传 `-ldflags "-s -w -X main.Version=$(git describe --tags --always)"`），生产机已重新部署，`/api/version` 返回 `v1.1.x` 而非 `dev`。
- **GitHub Actions 构建的镜像版本号只有短 commit SHA**：CI 原先把 `${GITHUB_SHA::7}` 作为 `--build-arg VERSION` 传入，且 `actions/checkout` 默认 `fetch-depth: 1` 拿不到 tag，因此镜像内版本号与「发布版本」对不上（看不出是 v1.1.3 还是别的）。
  修复后 CI checkout 改为 `fetch-depth: 0`（取全量 tag 历史），版本号改用与 `Makefile` 同一套约定计算：`HEAD` 恰好落在 tag 上 → `v1.1.4`；领先 tag 若干提交 → `v1.1.4-N-g<sha>`；仓库无 tag 才回退短 SHA。

### 改进（Improvements）
- `.gitignore` 新增 `cups-web-linux`（本地交叉编译产物 ~47MB，部署时由 Docker `COPY` 进镜像，不应入库；此前未忽略还会让 Go build info 出现 `+dirty`）。
- `Dockerfile` 中版本注入说明改为与 `.github/workflows/build.yml` 实际行为一致，并标注手工构建必须传 `-ldflags`。

### 部署说明
- 纯构建链路修复，Go 业务逻辑无改动，前端未改动（前端 hash 仍为 `D48HY_Hb`），无需强刷浏览器。
- 重新部署后左上角版本号应由 `dev` 变为 `v1.1.4`；推 `master` 后 GitHub Actions 构建的 `:latest` 镜像版本号同样为 `v1.1.4`，两边可直接对照。

---

## v1.1.3（2026-09-20，补丁）

### 修复（Fixes）
- **横向打印纸张方向错误（"横向 4 合 1 打成纵向"）**：根因为本部署打印机（Brother/brlaser 系驱动）的 PPD **没有 `*Orientation` 选项**，`orientation-requested-supported` 实际只有 3（portrait）。客户端提交的 `orientation-requested=4` 被 `pdftopdf` 直接忽略，横向页面（842×595pt）被原样塞进纵向纸张（595×842pt），右侧约 30% 内容跑出成像区被裁切（发票第二列消失），而 `print-scaling=none` 又阻止了任何缩放兜底。
  修复后**不再依赖打印机的方向能力**：提交打印前用 Ghostscript 把「横向作业」归一化为「纵向 A4 纸张 + 内容旋转 90° 填满整页」（`cmd/server/pdf_landscape.go` 的 `orientPDFForLandscapePrint`，参数 `pdfwrite -dFIXEDMEDIA -dPDFFitPage -dAutoRotatePages=/None -c "<</Orientation 1>> setpagedevice"`），再按 portrait 提交。预览仍显示横向版面，出纸后横向摆放即得正确横向 2×2 效果。普通图片 / 文档 / 纵向版面的打印均不受影响。

### 部署说明
- 纯后端逻辑修复，前端未改动（前端 hash 仍为 `D48HY_Hb`）。生产机已通过本地交叉编译二进制替换部署验证；若走 GitHub Actions 自动构建，推 `master` 后 `:latest` 会同步更新，强刷浏览器即可。

---

## v1.1.2（2026-08-21，补丁）

### 修复（Fixes）
- **docx / 手动转换的 PDF 预览失败（"PDF 预览加载失败"）**：根因为前端 `convertToPdf` 对 `application/pdf` 文件缺少专门分支，落入 `else` 的「文本转换 + `previewType='pdf'`」路径，强制走 pdf.js 渲染；而受限部署环境中 pdf.js worker 易加载失败，于是报错「PDF 预览加载失败」（打印本身正常）。
  修复后 `convertToPdf` 增加 PDF 专属分支：**打印直接使用原始字节**，**预览走 Ghostscript 光栅化**（`rasterizePdfToPages`，与上传即预览的 `autoRasterizePdf` 完全一致），彻底绕开 pdf.js。Office 文档（如 Word `.docx`）点击「转换为 PDF」时也复用同一 GS 光栅化预览路径。
  实测「2020总队运维情况总结.docx」：后端 libreoffice 转 PDF（该文件实际 8 个真实页面，页面树 `/Count` 被 libreoffice 误写为 13，属源文件瑕疵，GS 渲染 8 页为正确结果）→ GS 逐页预览正确返回 **8 页**；手动转换的 PDF 上传同理返回 8 页 PNG 预览，不再走 pdf.js。

### 部署说明
- 升级后务必浏览器 **Ctrl+Shift+R 强刷**（前端 hash 由 `BQhrJ2Yz` 变更为 `D48HY_Hb`），否则浏览器沿用旧缓存、仍走 pdf.js。

---

## v1.1.0（2026-08-15）

### 新增（Features）
- **发票边距可调**：发票合并新增「边距」滑块，范围 **0–20mm**，默认 **5mm**（原版硬编码 10mm），拖动即实时刷新预览。
- **预览逐页翻页**：发票 / 合成预览改为后端逐页渲染、前端逐页展示，预览区下方出现「‹ 1 / N ›」翻页条，彻底解决「多页被拼成一张长图、看起来像全挤在一页」的视觉误解。图片转 PDF、Ghostscript 标准化、PDF 自动栅格化等预览路径同样走逐页逻辑。
- **预览方向自动跟随版面**：发票模式下预览纸张方向随所选版面自动切换（纵向版面 → 竖版、横向版面 → 横版）；手动「纵向 / 横向」切换按钮在发票模式下自动禁用并显示「自动」。普通图片 / 文档打印仍保留手动切换能力。
- **发票 N 合 1 版面**：支持 **纵向 2 合 1 / 横向 2 合 1 / 纵向 4 合 1 / 横向 4 合 1** 四种版面，每页固定 N 张（2 合 1 → 每页 2 张，4 合 1 → 每页 4 张），超出自动开新页。

### 修复（Fixes）
- **发票分页逻辑**：修正多张发票被错误挤到同一页的问题，严格按版面每页 N 张分页（4 张选 2 合 1 → 2 页，6 张 → 3 页，4 张选 4 合 1 → 1 页）。
- **发票铺不满**：发票合成尊重 PDF `CropBox`，发票正确填满半页 / 四分之一页插槽。
- **预览失败**：上传 PDF 自动栅格化，预览永不因 CJK 字体未嵌入而失败。

### 部署说明
- 镜像：`ghcr.io/gmaitxqqq/cups-web:latest`（推 `master` 后由 GitHub Actions 自动构建）。
- 或本地交叉编译 `cups-web-linux` 静态二进制，替换容器内 `/cups-web` 后 `docker restart`（适用于 `ghcr.io` 被限速的内网环境）。
- 升级后建议浏览器 **Ctrl+Shift+R 强刷** 以加载最新前端。

---

## v1.1.1（2026-08-20，补丁）

### 新增 / 改进（Features）
- **标准模式多页预览翻页**：标准打印 / 图片 / 文档上传后，预览同样**逐页返回并带「‹ 1 / N ›」翻页条**，与发票模式一致，不再只显示首页、且不能翻页。预览上方增加「多页预览 · 点击左右箭头可翻页查看每一页」提示。

### 修复（Fixes）
- **标准打印 / 预览只出 1 页（后端）**：根因为页数统计依赖 unidoc 的 `countPDFPages`，对**带空口令加密**或特殊结构的 PDF 误判为 1 页（如「二年级语文入学测试卷.pdf」实有 5 页），导致预览只显示首页、打印光栅化只打包 1 页。
  修复后预览与打印光栅化均改为由 **Ghostscript 实际渲染产出的图片数**决定页数（glob 收集），不再信任 unidoc 计数；光栅化逐页按各自渲染图尺寸打包，并返回真实页数供打印记录显示。已用该 5 页 PDF 在线上验证预览与打印均正确返回 5 页。
- **标准模式预览不翻页（前端）**：标准模式走 `autoRasterizePdf` 路径，其逐页逻辑虽已写入源码，但此前前端产物未重新构建部署，且浏览器缓存了更早的单页旧前端，导致标准模式只显示首页、没有翻页条（发票模式正常）。
  修复后在标准模式多页预览区增加翻页提示文字，使前端文件名 hash 由 `C-FX4aFQ` 变更为 `BQhrJ2Yz`，强制浏览器加载新版；重新 `vite build` + `go build`（embed 新前端）并部署。现标准模式预览与发票模式一致，均带「‹ 1 / N ›」翻页条。

---

## v1.0.0（定制 fork 首版）

### 新增（Features）
- **免登录**：`AUTH_DISABLED=true` 直接打开主界面，无需账号密码（仅限内网）。
- **发票金额修复**：打印 / 合成前用 Ghostscript 嵌入缺失字体，解决发票 PDF 金额空白。
- **图片手动缩放**：图片支持「自动适应」或「手动缩放 1–100%」。
- **水平对齐**：居中 / 居左 / 居右。
- **垂直对齐**：居中 / 靠上 / 靠下（纵向图片也能选）。
- **服务器侧预览**：预览由后端渲染成 PNG 返回，绕过 pdf.js，所有 PDF / 图片预览稳定。
- **版本号显示**：左上角显示构建版本（git SHA），便于核对部署版本。
