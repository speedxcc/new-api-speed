#!/bin/bash
# ===========================================================================
# VPS 上更新 new-api 镜像的脚本
#
# 用法:
#   chmod +x update-vps.sh
#   ./update-vps.sh              # 拉取 latest 并重启
#   ./update-vps.sh v1.0.0       # 拉取指定版本
#
# 前提:
#   1. 镜像需设为 public（GitHub 仓库 → Packages → Package settings → public）
#   2. 或配置了 docker login ghcr.io（私有镜像时需要）
# ===========================================================================

set -e

# ===== 配置（按你的实际环境修改）=====
IMAGE="ghcr.io/speedxcc/new-api-speed"
COMPOSE_DIR="/www/dk_project/dk_app/newapi/newapi_xjh4"
COMPOSE_FILE="docker-compose.yml"
CONTAINER_NAME="newapi_xjh4-new-api-1"
TAG="${1:-latest}"

echo "=========================================="
echo "  new-api 镜像更新脚本"
echo "=========================================="
echo "  镜像: ${IMAGE}:${TAG}"
echo "  目录: ${COMPOSE_DIR}"
echo ""

# 1. 备份当前 compose 配置
cd "$COMPOSE_DIR"
if [ -f "$COMPOSE_FILE" ]; then
    cp "$COMPOSE_FILE" "${COMPOSE_FILE}.bak.$(date +%Y%m%d%H%M)"
    echo "  ✓ 已备份 ${COMPOSE_FILE}"
fi

# 2. 拉取新镜像
echo ""
echo "▶ 拉取镜像 ${IMAGE}:${TAG} ..."
docker pull "${IMAGE}:${TAG}"

# 3. 更新 compose 文件的 image 字段
echo ""
echo "▶ 更新 compose 配置 ..."
if grep -q "image:.*${IMAGE}" "$COMPOSE_FILE" 2>/dev/null; then
    # 已有该镜像配置，更新 tag
    sed -i "s|image: ${IMAGE}:.*|image: ${IMAGE}:${TAG}|" "$COMPOSE_FILE"
else
    # 替换官方镜像为我们的镜像
    sed -i "s|image: calciumion/new-api:.*|image: ${IMAGE}:${TAG}|" "$COMPOSE_FILE"
    sed -i "s|image: calciumion/new-api|image: ${IMAGE}:${TAG}|" "$COMPOSE_FILE"
fi
echo "  ✓ compose 文件已更新"

# 4. 重启容器
echo ""
echo "▶ 重启容器 ..."
docker compose up -d

# 5. 等待并检查
echo ""
echo "▶ 等待启动 ..."
sleep 10
if docker ps --filter "name=${CONTAINER_NAME}" --format "{{.Status}}" | grep -q "Up"; then
    echo "  ✓ 容器已启动: $(docker ps --filter name=${CONTAINER_NAME} --format '{{.Status}}')"
else
    echo "  ✗ 容器可能未正常启动，请检查日志:"
    echo "    docker logs ${CONTAINER_NAME} --tail 30"
    exit 1
fi

echo ""
echo "=========================================="
echo "  ✅ 更新完成！"
echo "=========================================="
echo ""
echo "  验证: docker logs ${CONTAINER_NAME} --tail 20"
echo "  回滚: cp ${COMPOSE_FILE}.bak.* ${COMPOSE_FILE} && docker compose up -d"
