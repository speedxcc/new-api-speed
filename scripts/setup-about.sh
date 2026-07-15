#!/bin/bash
# ===========================================================================
# 生产环境一键配置脚本：设置 Claude Code 教程 + 导航
#
# 用法：
#   1. 把这个脚本和 tutorial-about.md 传到 VPS 同一目录
#   2. 修改下面的 NEWAPI_URL 和 ADMIN_TOKEN
#   3. chmod +x setup-about.sh && ./setup-about.sh
# ===========================================================================

# ===== 改成你的实际信息 =====
NEWAPI_URL="https://你的站点地址"
ADMIN_TOKEN="你的admin访问令牌"
# ============================

set -e

echo "=========================================="
echo "  new-api 教程配置脚本"
echo "=========================================="
echo "  站点: $NEWAPI_URL"
echo ""

# 1. 设置 About 教程内容
echo "▶ 配置教程内容 (About)..."
ABOUT_CONTENT=$(cat tutorial-about.md)
curl -s -X PUT "$NEWAPI_URL/api/option/" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "New-Api-User: 1" \
  -d "$(python3 -c "import json; print(json.dumps({'key':'About','value':open('tutorial-about.md').read()},ensure_ascii=False))")" \
  | grep -o '"success":[a-z]*'

# 2. 设置文档链接指向关于页
echo "▶ 配置文档链接 (docs_link -> /about)..."
curl -s -X PUT "$NEWAPI_URL/api/option/" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "New-Api-User: 1" \
  -d '{"key":"general_setting.docs_link","value":"/about"}' \
  | grep -o '"success":[a-z]*'

# 3. 设置顶栏导航
echo "▶ 配置顶栏导航 (HeaderNavModules)..."
curl -s -X PUT "$NEWAPI_URL/api/option/" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "New-Api-User: 1" \
  -d '{"key":"HeaderNavModules","value":"home,console,pricing,about"}' \
  | grep -o '"success":[a-z]*'

echo ""
echo "=========================================="
echo "  ✅ 配置完成！"
echo "=========================================="
echo ""
echo "  验证:"
echo "    curl -s $NEWAPI_URL/api/about | head -c 100"
echo "    curl -s $NEWAPI_URL/api/status | grep docs_link"
echo ""
echo "  注意: 浏览器要用无痕窗口或清缓存才能看到效果"
