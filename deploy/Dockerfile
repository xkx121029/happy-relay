# 多阶段构建：前端产物内嵌进 Go 二进制，最终镜像只含一个可执行文件。
#
# 构建（在仓库根目录执行，注意最后的 . 是构建上下文）：
#   docker build -f deploy/Dockerfile -t happy-relay:latest .
# 交叉编译 arm64：
#   docker buildx build --platform linux/arm64 -f deploy/Dockerfile -t happy-relay:arm64 .

# ---------- 阶段一：构建网页面板 ----------
# --platform=$BUILDPLATFORM：构建阶段始终跑在宿主机架构上（x86 机器上做 arm64 镜像，
# 前端与 Go 编译都不会走 QEMU 模拟，速度接近原生）。只有最终运行镜像是目标架构。
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web

# 先只拷贝依赖清单，让依赖层能被缓存复用
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund

COPY web/ ./
# 构建产物由 vite 直接写到 ../internal/api/webdist
RUN npm run build

# ---------- 阶段二：编译后端 ----------
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
# TARGETOS / TARGETARCH 由 buildx 根据 --platform 自动注入
ARG TARGETOS
ARG TARGETARCH
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 用阶段一的真实产物覆盖仓库里的构建结果
COPY --from=web /src/internal/api/webdist ./internal/api/webdist

# CGO 关闭，依赖 modernc.org/sqlite 这个纯 Go 实现，因此可以静态交叉编译
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/relay ./cmd/relay

# ---------- 阶段三：运行 ----------
FROM alpine:3.21
# ca-certificates 用于访问 https 上游，tzdata 用于日志时间正确显示
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 relay

COPY --from=build /out/relay /usr/local/bin/relay

# 数据库与生成的文件都放在这个目录，挂载卷即可持久化
ENV RELAY_ADDR=":8787" \
    RELAY_DATA_DIR="/data" \
    RELAY_DB="/data/relay.db"
VOLUME ["/data"]
EXPOSE 8787

USER relay
ENTRYPOINT ["/usr/local/bin/relay"]