#!/bin/bash

echo "====================================="
echo "测试优化版本的API"
echo "====================================="

# 设置环境变量，启用优化版本
export USE_OPTIMIZED_HANDLERS=true
export DEBUG_MODE=true
export PORT=8081

echo "启动服务器（端口 8081）..."
./z2api &
SERVER_PID=$!

# 等待服务器启动
sleep 3

echo -e "\n测试健康检查端点..."
curl -s http://localhost:8081/health | jq .

echo -e "\n测试模型列表..."
curl -s http://localhost:8081/v1/models \
  -H "Authorization: Bearer sk-tbkFoKzk9a531YyUNNF5" | jq .

echo -e "\n测试非流式聊天完成（简单请求）..."
curl -s -X POST http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-tbkFoKzk9a531YyUNNF5" \
  -d '{
    "model": "glm-4.6",
    "messages": [
      {"role": "user", "content": "说你好"}
    ],
    "stream": false
  }' | jq .

echo -e "\n测试流式聊天完成..."
echo "发送流式请求..."
curl -N -X POST http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-tbkFoKzk9a531YyUNNF5" \
  -d '{
    "model": "glm-4.6",
    "messages": [
      {"role": "user", "content": "计数到3"}
    ],
    "stream": true
  }' 2>/dev/null | head -20

echo -e "\n\n测试完成，停止服务器..."
kill $SERVER_PID

echo "====================================="
echo "优化版本测试完成"
echo "====================================="