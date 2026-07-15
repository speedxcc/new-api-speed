#!/bin/bash
# ===========================================================================
# 本地一键构建脚本：构建前端 → 打 Docker 镜像 → 启动容器
#
# 用法：
#   bash build-local.sh          # 构建并重启容器
#   bash build-local.sh --no-frontend  # 跳过前端构建(只重新编译后端)
# ===========================================================================

set -e
cd "$(dirname "$0")"

IMAGE_NAME="new-api-bindgroup:local"
CONTAINER_NAME="new-api-test"
BUILD_FRONTEND=true

if [ "$1" = "--no-frontend" ]; then
  BUILD_FRONTEND=false
fi

echo "=========================================="
echo "  new-api 本地构建脚本"
echo "=========================================="

# ---------- 1. 构建前端 ----------
if [ "$BUILD_FRONTEND" = true ]; then
  echo ""
  echo "▶ [1/4] 构建前端(default + classic)..."

  cd web
  echo "  安装依赖..."
  bun install --frozen-lockfile 2>/dev/null || bun install

  echo "  构建 default..."
  cd default && bun run build && cd ..

  echo "  构建 classic..."
  cd classic && bun run build && cd ..

  cd ..
  echo "  ✅ 前端构建完成"
else
  echo ""
  echo "▶ [1/4] 跳过前端构建(--no-frontend)"
fi

# ---------- 2. 构建 Docker 镜像 ----------
echo ""
echo "▶ [2/4] 构建 Docker 镜像..."

# .dockerignore 排除了 dist，临时移除让 dist 进入构建上下文
DOCKERIGNORE_BACKUP=""
if grep -q "dist" .dockerignore 2>/dev/null; then
  DOCKERIGNORE_BACKUP=".dockerignore.bak"
  cp .dockerignore "$DOCKERIGNORE_BACKUP"
  sed -i '/dist/d' .dockerignore
  echo "  (临时放行 dist 进入构建上下文)"
fi

docker build -f Dockerfile.localtest -t "$IMAGE_NAME" .

# 恢复 .dockerignore
if [ -n "$DOCKERIGNORE_BACKUP" ]; then
  mv "$DOCKERIGNORE_BACKUP" .dockerignore
  echo "  (.dockerignore 已恢复)"
fi

echo "  ✅ 镜像构建完成"

# ---------- 3. 停止旧容器 ----------
echo ""
echo "▶ [3/4] 重启容器..."
docker stop "$CONTAINER_NAME" 2>/dev/null || true
docker rm "$CONTAINER_NAME" 2>/dev/null || true

# ---------- 4. 启动新容器 ----------
MSYS_NO_PATHCONV=1 docker run -d \
  --name "$CONTAINER_NAME" \
  -p 3000:3000 \
  --add-host host.docker.internal:host-gateway \
  -v "$(pwd)/data:/data" \
  -v "$(pwd)/logs:/app/logs" \
  -e TZ=Asia/Shanghai \
  -e ERROR_LOG_ENABLED=true \
  "$IMAGE_NAME" --log-dir /app/logs

sleep 8

if docker ps --filter "name=$CONTAINER_NAME" --format "{{.Status}}" | grep -q "Up"; then
  echo "  ✅ 容器已启动: $(docker ps --filter name=$CONTAINER_NAME --format '{{.Status}}')"
else
  echo "  ✗ 容器启动失败，查看日志:"
  docker logs "$CONTAINER_NAME" --tail 20
  exit 1
fi

echo ""
echo "=========================================="
echo "  ✅ 全部完成！"
echo "=========================================="
echo ""
echo "  访问: http://localhost:3000"
echo "  登录: root / Test123456"
echo "  日志: docker logs -f $CONTAINER_NAME"
echo ""
echo "  以后改了后端代码，只需:"
echo "    bash build-local.sh --no-frontend  # 跳过前端，只重编后端(快)"
echo "  改了前端代码:"
echo "    bash build-local.sh               # 重新构建前端+后端"
