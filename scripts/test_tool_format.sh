#!/bin/bash

# 测试工具调用的格式
echo "测试工具调用返回格式..."

export USE_OPTIMIZED_HANDLERS=true
export PORT=8084
export API_KEY="sk-tbkFoKzk9a531YyUNNF5"

# 启动服务器
killall z2api 2>/dev/null
./z2api &
SERVER_PID=$!
sleep 2

# 测试工具调用
RESPONSE=$(curl -s -X POST http://localhost:8084/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [{"role": "user", "content": "What is the weather in Beijing?"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string"}
          }
        }
      }
    }],
    "stream": false
  }')

echo "完整响应："
echo "$RESPONSE" | jq '.'

echo ""
echo "tool_calls格式："
echo "$RESPONSE" | jq '.choices[0].message.tool_calls'

# 停止服务器
kill $SERVER_PID 2>/dev/null
