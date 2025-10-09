#!/bin/bash

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "====================================="
echo "    快速功能测试"
echo "====================================="

# 设置环境变量
export USE_OPTIMIZED_HANDLERS=true
export DEBUG_MODE=false  # 关闭调试减少输出
export PORT=8083
export API_KEY="sk-tbkFoKzk9a531YyUNNF5"

# 杀掉可能存在的进程
killall z2api 2>/dev/null

echo "启动服务器（端口 8083）..."
./z2api &
SERVER_PID=$!

# 等待服务器启动
sleep 2

BASE_URL="http://localhost:8083"

echo -e "\n${YELLOW}测试基础功能...${NC}"

# 1. 健康检查
echo -n "1. 健康检查: "
if curl -s $BASE_URL/health | grep -q "healthy"; then
    echo -e "${GREEN}✓${NC}"
else
    echo -e "${RED}✗${NC}"
fi

# 2. 基础对话
echo -n "2. 基础对话（非流式）: "
RESPONSE=$(curl -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [{"role": "user", "content": "Say OK"}],
    "stream": false,
    "temperature": 0.1
  }' 2>/dev/null)

if echo "$RESPONSE" | grep -q "choices"; then
    echo -e "${GREEN}✓${NC}"
    echo "   响应示例: $(echo "$RESPONSE" | jq -r '.choices[0].message.content' 2>/dev/null | head -c 50)"
else
    echo -e "${RED}✗${NC}"
    echo "   错误: $(echo "$RESPONSE" | jq -r '.error.message' 2>/dev/null)"
fi

# 3. 流式响应
echo -n "3. 流式响应: "
STREAM_OUTPUT=$(timeout 3 curl -N -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [{"role": "user", "content": "Count 1 2 3"}],
    "stream": true
  }' 2>/dev/null | head -10)

if echo "$STREAM_OUTPUT" | grep -q "data:"; then
    echo -e "${GREEN}✓${NC}"
    echo "   收到 $(echo "$STREAM_OUTPUT" | grep -c "data:") 个数据块"
else
    echo -e "${RED}✗${NC}"
fi

# 4. 工具调用测试
echo -e "\n${YELLOW}测试工具调用...${NC}"
echo -n "4. 工具调用（天气查询）: "
TOOL_RESPONSE=$(curl -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [{"role": "user", "content": "What is the weather in Beijing?"}],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "get_weather",
          "description": "Get weather for a location",
          "parameters": {
            "type": "object",
            "properties": {
              "location": {"type": "string"}
            }
          }
        }
      }
    ],
    "stream": false
  }' 2>/dev/null)

if echo "$TOOL_RESPONSE" | jq -e '.choices[0].message.tool_calls' 2>/dev/null | grep -q "get_weather"; then
    echo -e "${GREEN}✓ 检测到工具调用${NC}"
    echo "   工具: $(echo "$TOOL_RESPONSE" | jq -r '.choices[0].message.tool_calls[0].function.name' 2>/dev/null)"
elif echo "$TOOL_RESPONSE" | grep -q "choices"; then
    echo -e "${YELLOW}⚠ 返回了普通文本响应（可能模型不支持工具）${NC}"
else
    echo -e "${RED}✗ 请求失败${NC}"
    echo "   错误: $(echo "$TOOL_RESPONSE" | jq -r '.error' 2>/dev/null)"
fi

# 5. 多轮对话
echo -n "5. 多轮对话上下文: "
MULTI_RESPONSE=$(curl -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [
      {"role": "user", "content": "Remember this number: 42"},
      {"role": "assistant", "content": "I will remember the number 42."},
      {"role": "user", "content": "What number did I ask you to remember? Just say the number."}
    ],
    "stream": false,
    "temperature": 0.1
  }' 2>/dev/null)

if echo "$MULTI_RESPONSE" | jq -r '.choices[0].message.content' 2>/dev/null | grep -q "42"; then
    echo -e "${GREEN}✓ 上下文保持正确${NC}"
else
    echo -e "${RED}✗ 上下文可能丢失${NC}"
fi

# 6. 不同模型测试
echo -e "\n${YELLOW}测试不同模型...${NC}"
for model in "glm-4.5" "glm-4.5-air" "glm-4.6"; do
    echo -n "6. 模型 $model: "
    MODEL_RESP=$(curl -s -X POST $BASE_URL/v1/chat/completions \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer $API_KEY" \
      -d "{
        \"model\": \"$model\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Hi\"}],
        \"stream\": false,
        \"max_tokens\": 10
      }" 2>/dev/null)

    if echo "$MODEL_RESP" | grep -q "choices"; then
        echo -e "${GREEN}✓${NC}"
    else
        echo -e "${RED}✗${NC}"
    fi
done

# 7. 参数测试
echo -e "\n${YELLOW}测试参数处理...${NC}"
echo -n "7. 自定义参数（temperature=0.1, max_tokens=5）: "
PARAM_RESP=$(curl -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.6",
    "messages": [{"role": "user", "content": "Say one word"}],
    "temperature": 0.1,
    "max_tokens": 5,
    "stream": false
  }' 2>/dev/null)

if echo "$PARAM_RESP" | grep -q "choices"; then
    echo -e "${GREEN}✓${NC}"
    CONTENT=$(echo "$PARAM_RESP" | jq -r '.choices[0].message.content' 2>/dev/null)
    echo "   响应长度: ${#CONTENT} 字符"
else
    echo -e "${RED}✗${NC}"
fi

# 8. 错误处理
echo -e "\n${YELLOW}测试错误处理...${NC}"
echo -n "8. 无效API密钥: "
ERROR_RESP=$(curl -s -X POST $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer wrong-key" \
  -d '{"model": "glm-4.5", "messages": [{"role": "user", "content": "Test"}]}' 2>/dev/null)

if echo "$ERROR_RESP" | jq -e '.error.code' 2>/dev/null | grep -q "401"; then
    echo -e "${GREEN}✓ 正确返回401错误${NC}"
else
    echo -e "${RED}✗${NC}"
fi

# 9. 并发测试
echo -n "9. 并发请求（3个）: "
(
  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d '{"model": "glm-4.6", "messages": [{"role": "user", "content": "1"}], "max_tokens": 5}' > /tmp/test1.json &

  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d '{"model": "glm-4.6", "messages": [{"role": "user", "content": "2"}], "max_tokens": 5}' > /tmp/test2.json &

  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d '{"model": "glm-4.6", "messages": [{"role": "user", "content": "3"}], "max_tokens": 5}' > /tmp/test3.json &

  wait
) 2>/dev/null

SUCCESS_COUNT=0
for i in 1 2 3; do
    if grep -q "choices" /tmp/test$i.json 2>/dev/null; then
        ((SUCCESS_COUNT++))
    fi
done

if [ $SUCCESS_COUNT -eq 3 ]; then
    echo -e "${GREEN}✓ 全部成功${NC}"
else
    echo -e "${YELLOW}⚠ $SUCCESS_COUNT/3 成功${NC}"
fi

# 清理临时文件
rm -f /tmp/test*.json

echo -e "\n====================================="
echo "测试完成"
echo "====================================="

# 停止服务器
kill $SERVER_PID 2>/dev/null
wait $SERVER_PID 2>/dev/null

echo "服务器已停止"