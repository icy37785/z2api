#!/bin/bash

# SSE格式修复验证脚本
# 验证两个问题是否已修复：
# 1. 流式响应中不应包含message字段
# 2. 不应单独出现</think>闭合标签

echo "🔍 SSE格式修复验证"
echo "===================="
echo ""

# 启动服务器
echo "📦 启动服务器..."
go build -o z2api . && ./z2api &
SERVER_PID=$!
sleep 3

# 等待服务器就绪
echo "⏳ 等待服务器就绪..."
for i in {1..10}; do
    if curl -s http://localhost:8787/health > /dev/null 2>&1; then
        echo "✅ 服务器已就绪"
        break
    fi
    sleep 1
done

echo ""
echo "🧪 测试1: 检查流式响应中是否包含message字段"
echo "================================================"

# 发送流式请求并捕获响应
RESPONSE=$(curl -s -N http://localhost:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }' | head -20)

echo "📝 响应示例（前20行）："
echo "$RESPONSE" | head -5
echo ""

# 检查是否包含message字段
if echo "$RESPONSE" | grep -q '"message"'; then
    echo "❌ 失败: 流式响应中仍然包含message字段"
    MESSAGE_COUNT=$(echo "$RESPONSE" | grep -c '"message"')
    echo "   发现 $MESSAGE_COUNT 处message字段"
    echo ""
    echo "   示例："
    echo "$RESPONSE" | grep '"message"' | head -3
else
    echo "✅ 通过: 流式响应中不包含message字段"
fi

echo ""
echo "🧪 测试2: 检查是否单独出现</think>闭合标签"
echo "=============================================="

# 检查是否有单独的</think>标签
if echo "$RESPONSE" | grep -q 'reasoning_content":"</think>"'; then
    echo "❌ 失败: 发现单独的</think>闭合标签"
    echo ""
    echo "   示例："
    echo "$RESPONSE" | grep 'reasoning_content":"</think>"'
else
    echo "✅ 通过: 没有单独的</think>闭合标签"
fi

echo ""
echo "🧪 测试3: 验证delta字段存在"
echo "============================"

if echo "$RESPONSE" | grep -q '"delta"'; then
    echo "✅ 通过: 流式响应包含delta字段"
    DELTA_COUNT=$(echo "$RESPONSE" | grep -c '"delta"')
    echo "   发现 $DELTA_COUNT 个delta字段"
else
    echo "❌ 失败: 流式响应中缺少delta字段"
fi

echo ""
echo "🧪 测试4: 验证非流式响应格式"
echo "=============================="

NON_STREAM_RESPONSE=$(curl -s http://localhost:8787/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }')

if echo "$NON_STREAM_RESPONSE" | jq -e '.choices[0].message' > /dev/null 2>&1; then
    echo "✅ 通过: 非流式响应包含message字段"
else
    echo "❌ 失败: 非流式响应缺少message字段"
fi

if echo "$NON_STREAM_RESPONSE" | jq -e '.choices[0].delta' > /dev/null 2>&1; then
    echo "❌ 失败: 非流式响应不应包含delta字段"
else
    echo "✅ 通过: 非流式响应不包含delta字段"
fi

echo ""
echo "🧹 清理..."
kill $SERVER_PID 2>/dev/null
wait $SERVER_PID 2>/dev/null
rm -f z2api

echo ""
echo "✨ 验证完成！"