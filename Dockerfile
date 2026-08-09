# syntax=docker/dockerfile:1
#
# Combined Dockerfile: CUPS print server + cups-web management UI
#
# This is an all-in-one (AIO) image that merges the standalone CUPS print server
# (cups/Dockerfile) with the cups-web Go backend and Vue frontend into a single
# container. The entrypoint starts cupsd in background, then launches cups-web
# as the foreground PID-1 process.
#
# Build stages:
#   1. frontend-build  — Node.js: npm ci + npm run build (Vite)
#   2. java-builder    — OpenJDK 21 + Maven: OFD converter JAR
#   3. builder         — Go 1.26: cups-web binary with embedded frontend
#   4. cups-builder    — Debian: compile CUPS from OpenPrinting source
#   5. runtime         — Debian trixie-slim: all runtime packages + compiled CUPS overlay

# ═══════════════════════════════════════════════════════════════
# Stage 1: frontend-build
# ═══════════════════════════════════════════════════════════════
# 使用 node:20-slim 替代 oven/bun：Bun 官方不支持 32-bit ARM（#5060 Closed as not planned），
# 会导致 linux/arm/v7 构建直接找不到 manifest。node:20-slim 官方镜像覆盖 amd64/arm32v7/arm64v8，
# 而 frontend/package.json 里 scripts 全是标准 Vite/Node 命令，完全不依赖 bun 专有 API，
# 用 npm ci 替换 bun install 即可获得跨三架构的一致构建产物。
FROM node:20-slim AS frontend-build
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci --no-audit --no-fund --prefer-offline
COPY frontend ./
RUN npm run build

# ═══════════════════════════════════════════════════════════════
# Stage 2: java-builder (OFD converter)
# ═══════════════════════════════════════════════════════════════
#
# 关键点：`FROM --platform=$BUILDPLATFORM ...` 把本阶段锁在 **host 本地架构**（CI 上是 amd64），
# 不跟随 buildx 的 TARGETPLATFORM 走进 QEMU armhf 模拟。这么做的核心理由：
#
#   QEMU 用户态模拟 armhf 下，OpenJDK 17 不稳定。Maven 无论是用 Debian 的 `apt install maven`
#   还是 Apache 官方 Maven 3.9.x 二进制 tarball，启动时都会随机抛
#   `java.lang.ClassNotFoundException: org.apache.maven.cli.MavenCli`（堆栈完全一致，只差
#   classworlds 版本行号），这说明问题在 JVM 层——具体是 QEMU 模拟下的 ClassLoader /
#   JIT 稳定性问题，而非 Maven 安装方式。侧面佐证：Adoptium Temurin JDK 17/21/25 对 Linux
#   ARM 32-bit Hard-Float **官方不支持**（仅 JDK 8/11 有 armhf 二进制），说明 ARM 32-bit 上的
#   现代 JVM 本来就是薄弱环节，在 QEMU 下更是雪上加霜。
#
# 由于 ofd-converter 是纯 Java 项目（maven.compiler.source=1.8 → target JVM bytecode），
# 产物 `.jar` 跨架构通吃，让 java-builder 固定跑在 amd64 上构建一次、各架构的 runtime
# 统一 `COPY --from=java-builder` 这份纯字节码 jar，是 Docker 官方推荐的多架构 Java
# 最佳实践，也是 `BUILDPLATFORM` 这个自动变量最典型的用法。
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS java-builder
ENV DEBIAN_FRONTEND=noninteractive
ENV MAVEN_VERSION=3.9.9
ENV MAVEN_HOME=/opt/maven
ENV PATH=/opt/maven/bin:$PATH
# Maven tarball 来源策略：
# 1) 优先 dlcdn.apache.org（Apache CDN，快）—— 但它只保留 current release，
#    一旦官方发布 3.9.10+，3.9.9 会立即 404，CI 会挂（exit code 22）。
# 2) Fallback 到 archive.apache.org/dist/maven/...（永久归档，所有历史版本都在）。
# 这样日常走 CDN 快，被 dlcdn 抛弃后自动用归档也能跑通，升级 Maven 时只需改 MAVEN_VERSION。
RUN apt-get update && apt-get install -y --no-install-recommends \
      openjdk-21-jdk-headless ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && ( \
        curl -fsSL "https://dlcdn.apache.org/maven/maven-3/${MAVEN_VERSION}/binaries/apache-maven-${MAVEN_VERSION}-bin.tar.gz" -o /tmp/maven.tar.gz \
        || curl -fsSL "https://archive.apache.org/dist/maven/maven-3/${MAVEN_VERSION}/binaries/apache-maven-${MAVEN_VERSION}-bin.tar.gz" -o /tmp/maven.tar.gz \
       ) \
    && mkdir -p "${MAVEN_HOME}" \
    && tar -xzf /tmp/maven.tar.gz -C "${MAVEN_HOME}" --strip-components=1 \
    && rm -f /tmp/maven.tar.gz \
    && mvn -version
WORKDIR /src/ofd-converter
COPY ofd-converter/pom.xml ./
RUN mvn dependency:go-offline -q
COPY ofd-converter/src ./src
RUN mvn clean package -q -DskipTests

# ═══════════════════════════════════════════════════════════════
# Stage 3: builder (Go binary)
# ═══════════════════════════════════════════════════════════════
FROM golang:1.26 AS builder
WORKDIR /src

# 构建期注入版本号（Issue #26）：
#   - CI (docker-publish.yml) 会通过 `--build-arg VERSION=${{ github.ref_name }}` 传入
#     形如 `v1.2.3` 的 tag 名；push master 分支时会传入分支名 `master`。
#   - 本地 `make docker-build` 会把 `git describe --tags --always --dirty` 透传进来。
#   - 未指定时保持空字符串，让 main.Version 保持默认 "dev"，便于区分"未注入"与"注入失败"。
ARG VERSION=""

# copy go modules and source
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY . .
# Copy built frontend assets into expected location for go:embed
COPY --from=frontend-build /src/frontend/dist ./frontend/dist

# Build the Go binary (frontend must be built before this step in CI/local)
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
      -ldflags="-s -w -X main.Version=$VERSION" \
      -o /out/cups-web ./cmd/server

# ═══════════════════════════════════════════════════════════════
# Stage 4: cups-builder (compile CUPS from OpenPrinting source)
# ═══════════════════════════════════════════════════════════════
# 从 OpenPrinting/cups 源码编译，覆盖安装到 /usr，然后打包成 tar 供 runtime 阶段提取。
# cups-filters 会把 apt 版 cups 作为依赖拉进来，由它负责创建 lp/lpadmin 用户组、
# /etc/cups 目录骨架和 systemd unit 文件等；随后用源码编译出的二进制（同样
# --prefix=/usr）覆盖掉 apt 版的 libcups.so.2 / cupsd / cups-client 等文件，
# 既保留 Debian 侧的集成脚手架，又替换成 OpenPrinting 上游的最新版本，且
# libcups2 ABI 兼容让 cups-filters 和所有 printer-driver-* 可以继续工作。
FROM debian:trixie-slim AS cups-builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      autoconf \
      automake \
      libtool \
      pkg-config \
      libavahi-client-dev \
      libavahi-common-dev \
      libdbus-1-dev \
      libgnutls28-dev \
      libkrb5-dev \
      libldap-dev \
      libpam0g-dev \
      libssl-dev \
      libsystemd-dev \
      libusb-1.0-0-dev \
      zlib1g-dev \
      wget \
      # install-cups.sh 用 wget 从 GitHub Releases 下载 CUPS tarball，必须有 CA 根证书才能
      # 校验 TLS；debian:trixie-slim 默认不带 ca-certificates，缺失时 wget 直接以退出码 5
      # （SSL verification failure）失败，脚本的 set -euo pipefail 会把整个构建带崩（CI 报
      # "install-cups.sh ... exit code: 5"）。旧的单阶段 cups/Dockerfile 因为运行时依赖里
      # 已经包含它才没暴露这个问题，拆成独立 builder 阶段后必须显式声明——请勿删除。
      ca-certificates \
      cups-daemon \
      cups-filters \
      dpkg-dev \
    && rm -rf /var/lib/apt/lists/*
COPY scripts/build/install-cups.sh /tmp/install-cups.sh
RUN chmod +x /tmp/install-cups.sh && bash /tmp/install-cups.sh
# 将源码编译出的 CUPS 二进制打包成 tar，运行时阶段解包覆盖 apt 版。
# 使用 dpkg-architecture 获取 multiarch triplet，确保 libcups*.so 路径正确。
RUN MULTIARCH=$(dpkg-architecture -qDEB_HOST_MULTIARCH) && \
    tar cf /tmp/cups-compiled.tar \
      /usr/sbin/cupsd \
      /usr/bin/cups* \
      /usr/bin/lp* \
      /usr/bin/cancel \
      /usr/bin/ipptool \
      /usr/bin/ippfind \
      /usr/lib/cups/ \
      /usr/lib/${MULTIARCH}/libcups* \
      /usr/share/cups/ \
      /usr/share/doc/cups/ \
      2>/dev/null || true

# ═══════════════════════════════════════════════════════════════
# Stage 5: runtime (all-in-one)
# ═══════════════════════════════════════════════════════════════
FROM debian:trixie-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai
ENV CUPSADMIN=print
ENV CUPSPASSWORD=print

# ────────────────────────────────────────────────────────────────
# apt: CUPS runtime + printer drivers + cups-web dependencies
# ────────────────────────────────────────────────────────────────
# CUPS 打印生态包（from cups/Dockerfile）+ cups-web 运行时依赖（LibreOffice, GS, fonts）
# 合并到单个 apt-get install 减少层数。
RUN apt-get update && apt-get install -y --no-install-recommends \
      # ── CUPS 打印生态（cups-filters 会拉入 apt 版 cups 作为依赖） ──
      cups-filters \
      cups-client \
      cups-daemon \
      cups-ipp-utils \
      printer-driver-all \
      printer-driver-cups-pdf \
      printer-driver-escpr \
      printer-driver-foo2zjs \
      printer-driver-splix \
      printer-driver-postscript-hp \
      printer-driver-hpijs \
      foomatic-db-engine \
      cups-browsed \
      ipp-usb \
      printer-driver-brlaser \
      foomatic-db-compressed-ppds \
      openprinting-ppds \
      hpijs-ppds \
      hp-ppd \
      hplip \
      avahi-daemon \
      dbus \
      # ── 通用工具（驱动安装脚本需要） ──
      curl \
      wget \
      unzip \
      rsync \
      ca-certificates \
      # ── LibreOffice headless（Office 文档转换） ──
      libreoffice-core \
      libreoffice-writer \
      libreoffice-calc \
      libreoffice-impress \
      # ── Java 运行时（OFD converter） ──
      openjdk-21-jre \
      # ── Ghostscript（PDF 处理） ──
      ghostscript \
      # ── 字体：CJK 中文 ──
      fonts-droid-fallback \
      fonts-noto-cjk \
      fonts-arphic-uming \
      fonts-arphic-ukai \
      fonts-wqy-zenhei \
      fonts-wqy-microhei \
      # ── 字体：西文基础 ──
      fonts-dejavu-core \
      fonts-liberation2 \
      fonts-urw-base35 \
      gsfonts \
      fontconfig \
    && fc-cache -f \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# ────────────────────────────────────────────────────────────────
# HOME / LibreOffice user profile 目录
# ────────────────────────────────────────────────────────────────
# 为什么要显式声明而不是依赖容器隐式的 HOME=/root：
#   1. LibreOffice headless 启动时必须有一个**可写的 HOME** 来落 user profile
#      （~/.config/libreoffice/4/user），拿不到就静默退出、`--convert-to pdf`
#      返回 0 但不产出 PDF，排查起来非常隐蔽；dconf 同理会往 ~/.cache/dconf 写。
#   2. Docker 只在 USER 指令指向 /etc/passwd 里的用户时才隐式给出 HOME。如果部署方
#      用 k8s `securityContext.runAsUser` 换成任意 uid（或 `docker run -u`），该 uid
#      不在 /etc/passwd 里，HOME 会退化成 `/`，转换就开始莫名失败。写死 ENV 后
#      至少路径是确定的，只需保证挂载/权限可写即可，故障可诊断。
# 目录预建同理：让首次转换不必等 LibreOffice 自己去创建目录树。
ENV HOME=/root
ENV XDG_CACHE_HOME=/root/.cache
ENV DCONF_USER_CONFIG_DIR=/root/.config/dconf
RUN mkdir -p /root/.cache/dconf /root/.config/libreoffice /root/.local/share/libreoffice \
    && chmod 700 /root/.cache/dconf

# ────────────────────────────────────────────────────────────────
# Overlay: 用源码编译的 CUPS 二进制覆盖 apt 版
# ────────────────────────────────────────────────────────────────
# apt 版 cups-daemon 已创建好 lp/lpadmin 用户组、/etc/cups 目录骨架等，
# 这里只用源码编译版的二进制和库文件覆盖，获得 OpenPrinting 上游最新版本。
COPY --from=cups-builder /tmp/cups-compiled.tar /tmp/
RUN tar xf /tmp/cups-compiled.tar -C / && rm /tmp/cups-compiled.tar && ldconfig

# ────────────────────────────────────────────────────────────────
# CUPS configuration: 放开监听 + 浏览，备份默认配置供 entrypoint 还原
# ────────────────────────────────────────────────────────────────
RUN sed -i 's/Listen localhost:631/Listen 0.0.0.0:631/' /etc/cups/cupsd.conf && \
    sed -i 's/Browsing Off/Browsing On/' /etc/cups/cupsd.conf && \
    sed -i 's/<Location \/>/<Location \/>\n  Allow All/' /etc/cups/cupsd.conf && \
    sed -i 's/<Location \/admin>/<Location \/admin>\n  Allow All\n  Require user @SYSTEM/' /etc/cups/cupsd.conf && \
    sed -i 's/<Location \/admin\/conf>/<Location \/admin\/conf>\n  Allow All/' /etc/cups/cupsd.conf && \
    echo "ServerAlias *" >> /etc/cups/cupsd.conf && \
    echo "DefaultEncryption Never" >> /etc/cups/cupsd.conf
RUN cp -rp /etc/cups /etc/cups-bak

# ────────────────────────────────────────────────────────────────
# Custom fonts: docker-fonts/ 目录中的用户字体
# ────────────────────────────────────────────────────────────────
# 自定义字体：用户可将 SimSun/SimHei 等 Windows 字体放入 docker-fonts/ 目录，
# 构建时自动安装到镜像中，以获得更接近原始 PDF 的字体渲染效果。
# 如果 docker-fonts/ 目录为空（只有 README.md），此步骤不会报错。
COPY docker-fonts/ /tmp/docker-fonts/
RUN mkdir -p /usr/share/fonts/truetype/custom && \
    find /tmp/docker-fonts -type f \( -iname '*.ttf' -o -iname '*.ttc' -o -iname '*.otf' \) \
      -exec cp {} /usr/share/fonts/truetype/custom/ \; && \
    fc-cache -f /usr/share/fonts/truetype/custom 2>/dev/null || true

# 安装 fontconfig 中文字体别名配置（从 docker-fonts/fontconfig-chinese.conf 复制）
# 原因：LibreOffice fallback 渲染路径依赖 fontconfig 查找"宋体""黑体"等中文字体名
# simsun.ttf 用于宋体映射（已从 TTC 替换为 TTF 单体格式，解决 gs 10.x 和 fontconfig 兼容性问题）
RUN cp /tmp/docker-fonts/fontconfig-chinese.conf /etc/fonts/conf.d/05-custom-chinese-fonts.conf 2>/dev/null || true
RUN fc-cache -f 2>/dev/null || true

# ────────────────────────────────────────────────────────────────
# Ghostscript cidfmap.local: GBK 字体名映射
# ────────────────────────────────────────────────────────────────
# 针对 Acrobat/WPS 导出的"空壳 Type0 + UniGB-UCS2-H + GBK 字节 BaseFont"这类 PDF
# （/BaseFont /#ba#da#cc#e5 即"黑体"的 GBK 字节，准考证/国标表格最常见），由我们
# 手动写入 /etc/ghostscript/cidfmap.local，把 8 个 GBK 字节名显式映射到本镜像自带
# 的真实 TrueType 字体。
RUN mkdir -p /etc/ghostscript && \
    cp /tmp/docker-fonts/cidfmap.local /etc/ghostscript/cidfmap.local && \
    rm -rf /tmp/docker-fonts
# 构建期自检：确保文件写入成功、条目数对得上。
RUN test -s /etc/ghostscript/cidfmap.local \
  && echo "[dockerfile] cidfmap.local size: $(wc -c < /etc/ghostscript/cidfmap.local) bytes" \
  && entries=$(grep -cE '^/#' /etc/ghostscript/cidfmap.local) \
  && echo "[dockerfile] cidfmap.local entries: $entries (expect 8)" \
  && test "$entries" = "8"

# 如果用户提供了 SimSun/SimHei/SimKai/SimFang 字体，更新 cidfmap.local 映射，
# 用真实 Windows 字体替换 arphic/wqy 的 fallback 映射，获得更精确的渲染效果。
RUN if [ -f /usr/share/fonts/truetype/custom/simsun.ttf ]; then \
      sed -i 's|/usr/share/fonts/truetype/arphic/uming.ttc) /SubfontID 0|/usr/share/fonts/truetype/custom/simsun.ttf) /SubfontID 0|g' /etc/ghostscript/cidfmap.local; \
      echo "[dockerfile] cidfmap: simsun.ttf mapped (宋体 Regular+Bold, 仿宋 pending)"; \
    fi && \
    if [ -f /usr/share/fonts/truetype/custom/simhei.ttf ]; then \
      sed -i 's|/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc) /SubfontID 0|/usr/share/fonts/truetype/custom/simhei.ttf) /SubfontID 0|g' /etc/ghostscript/cidfmap.local; \
      echo "[dockerfile] cidfmap: simhei.ttf mapped (黑体 Regular+Bold)"; \
    fi && \
    if [ -f /usr/share/fonts/truetype/custom/simkai.ttf ]; then \
      sed -i 's|/usr/share/fonts/truetype/arphic/ukai.ttc) /SubfontID 0|/usr/share/fonts/truetype/custom/simkai.ttf) /SubfontID 0|g' /etc/ghostscript/cidfmap.local; \
      echo "[dockerfile] cidfmap: simkai.ttf mapped (楷体 Regular+Bold)"; \
    fi && \
    if [ -f /usr/share/fonts/truetype/custom/simfang.ttf ]; then \
      sed -i '/^\/#b7#c2#cb#ce[, ]/s|/usr/share/fonts/truetype/custom/simsun.ttf) /SubfontID 0|/usr/share/fonts/truetype/custom/simfang.ttf) /SubfontID 0|' /etc/ghostscript/cidfmap.local; \
      sed -i '/^\/#b7#c2#cb#ce[, ]/s|/usr/share/fonts/truetype/arphic/uming.ttc) /SubfontID 0|/usr/share/fonts/truetype/custom/simfang.ttf) /SubfontID 0|' /etc/ghostscript/cidfmap.local; \
      echo "[dockerfile] cidfmap: simfang.ttf mapped (仿宋 Regular+Bold)"; \
    fi

# 将 cidfmap.local 作为 gs 的默认 cidfmap 安装到 Resource/Init/
# trixie 的 gs 10.05.1 默认不存在 cidfmap（只有 FAPIcidfmap），直接创建即可。
# gs 启动时自动加载 Resource/Init/cidfmap，无需额外命令行参数。
RUN GS_INIT_DIR=$(find /usr/share/ghostscript -path "*/Resource/Init" -type d | head -1) && \
    if [ -n "$GS_INIT_DIR" ] && [ -f /etc/ghostscript/cidfmap.local ]; then \
        cp /etc/ghostscript/cidfmap.local "$GS_INIT_DIR/cidfmap" && \
        echo "[dockerfile] cidfmap.local -> $GS_INIT_DIR/cidfmap"; \
    fi

# ────────────────────────────────────────────────────────────────
# Driver management: install scripts + management commands
# ────────────────────────────────────────────────────────────────
# Copy driver install scripts (NOT executed at build time — users install on demand)
COPY scripts/driver/install-*.sh /opt/cups-drivers/scripts/
# Copy driver management commands
COPY scripts/driver/driver-install.sh /usr/local/bin/driver-install
COPY scripts/driver/driver-list.sh /usr/local/bin/driver-list
COPY scripts/driver/driver-remove.sh /usr/local/bin/driver-remove
COPY scripts/driver/restore-drivers.sh /usr/local/bin/restore-drivers
# 预建 /opt/cups-drivers/data：driver-install 把驱动文件持久化到这里、driver-list 靠
# 其下的 <name>/manifest.txt 判断驱动是否已装。正常部署会用 volume 挂载覆盖它，但不挂卷
# 直接 `docker run` 时目录也得存在，驱动安装/列表才不会因缺目录而行为异常（此时数据随容器
# 销毁而丢失，属预期行为）。
RUN chmod +x /usr/local/bin/driver-* /usr/local/bin/restore-drivers /opt/cups-drivers/scripts/*.sh && \
    mkdir -p /opt/cups-drivers/data

# ────────────────────────────────────────────────────────────────
# Copy application binaries from build stages
# ────────────────────────────────────────────────────────────────
COPY --from=builder /out/cups-web /cups-web
COPY --from=java-builder /src/ofd-converter/target/ofd-converter.jar /ofd-converter.jar

# ────────────────────────────────────────────────────────────────
# Entrypoint
# ────────────────────────────────────────────────────────────────
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 631 8080
ENTRYPOINT ["/entrypoint.sh"]
