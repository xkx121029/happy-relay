#!/usr/bin/env bash
# 在 x86 机器上交叉编译出 arm64 镜像，并导出成 tar 包，方便拷到 NAS 上加载。
#
#   ./deploy/build-arm64.sh              # 只导出 tar
#   ./deploy/build-arm64.sh --push       # 直接推到镜像仓库
#   IMAGE=registry.example.com/happy-relay TAG=1.0.0 ./deploy/build-arm64.sh --push
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-happy-relay}"
TAG="${TAG:-arm64}"
PLATFORM="${PLATFORM:-linux/arm64}"
OUTFILE="${OUTFILE:-happy-relay-${TAG}.tar}"

# buildx 容器驱动才能跨架构构建；没有的话先用默认驱动试试
if ! docker buildx inspect relay-builder >/dev/null 2>&1; then
  echo "==> 创建 buildx 构建器 relay-builder"
  docker buildx create --name relay-builder --use >/dev/null
fi
docker buildx use relay-builder >/dev/null

if [[ "${1:-}" == "--push" ]]; then
  echo "==> 构建并推送 ${IMAGE}:${TAG} (${PLATFORM})"
  docker buildx build \
    --platform "${PLATFORM}" \
    -f deploy/Dockerfile \
    -t "${IMAGE}:${TAG}" \
    --push .
  echo "==> 已推送到 ${IMAGE}:${TAG}"
else
  echo "==> 构建 ${IMAGE}:${TAG} (${PLATFORM}) 并导出到 ${OUTFILE}"
  docker buildx build \
    --platform "${PLATFORM}" \
    -f deploy/Dockerfile \
    -t "${IMAGE}:${TAG}" \
    -o "type=docker,dest=${OUTFILE}" .
  echo
  echo "==> 完成：${OUTFILE}"
  echo "    拷到目标机器后执行： docker load -i ${OUTFILE}"
  echo "    然后：             docker run -d --name happy-relay -p 8787:8787 -v /your/data:/data ${IMAGE}:${TAG}"
fi