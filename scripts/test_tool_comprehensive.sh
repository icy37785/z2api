#!/bin/bash

# 综合工具调用测试脚本
# 测试多种场景，验证工具调用功能

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}=====================================${NC}"
echo -e "${BLUE}  工具调用综合测试${NC}"
echo -e "${BLUE}=====================================${NC}"

export USE_OPTIMIZED_HANDLERS=true
export DEBUG_MODE=true
export PORT=8085
export API_KEY="sk-tbkFoKzk9a531YyUNNF5"

# 启动服务器
echo -e "\n${YELLOW}正在启动服务器...${NC}"
killall z2api 2>/dev/null
./z2api > /tmp/z2api_test.log 2>&1 &
SERVER_PID=$!
sleep 3

# 检查服务器是否启动成功
if ! curl -s http://localhost:8085/health > /dev/null; then
    echo -e "${RED}❌ 服务器启动失败${NC}"
    exit 1
fi
echo -e "${GREEN}✓ 服务器已启动 (PID: $SERVER_PID)${NC}"

BASE_URL="http://localhost:8085/v1/chat/completions"

# ========================================
# 测试1: 基础工具调用测试
# ========================================
echo -e "\n${BLUE}测试1: 基础工具调用${NC}"
RESPONSE=$(curl -s -X POST $BASE_URL \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "What is the weather in Beijing?"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get current weather in a location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string", "description": "City name"}
          },
          "required": ["location"]
        }
      }
    }],
    "stream": false
  }')

echo "完整响应："
echo "$RESPONSE" | jq '.'

echo ""
echo "tool_calls 字段："
TOOL_CALLS=$(echo "$RESPONSE" | jq '.choices[0].message.tool_calls')
echo "$TOOL_CALLS"

if [ "$TOOL_CALLS" != "null" ] && [ "$TOOL_CALLS" != "" ]; then
    echo -e "${GREEN}✓ 检测到工具调用${NC}"

    # 验证格式
    echo -e "\n${YELLOW}验证格式...${NC}"

    # 检查是否有index字段（不应该有）
    if echo "$TOOL_CALLS" | jq '.[0]' | grep -q '"index"'; then
        echo -e "${RED}❌ 错误: 包含非标准的 index 字段${NC}"
    else
        echo -e "${GREEN}✓ 正确: 不包含 index 字段${NC}"
    fi

    # 检查必需字段
    ID=$(echo "$TOOL_CALLS" | jq -r '.[0].id')
    TYPE=$(echo "$TOOL_CALLS" | jq -r '.[0].type')
    FUNC_NAME=$(echo "$TOOL_CALLS" | jq -r '.[0].function.name')

    echo "  id: $ID"
    echo "  type: $TYPE"
    echo "  function.name: $FUNC_NAME"

    if [ "$ID" != "null" ] && [ "$ID" != "" ]; then
        echo -e "${GREEN}✓ id 字段存在${NC}"
    else
        echo -e "${RED}❌ id 字段缺失${NC}"
    fi

    if [ "$TYPE" == "function" ]; then
        echo -e "${GREEN}✓ type 字段正确${NC}"
    else
        echo -e "${RED}❌ type 字段错误: $TYPE${NC}"
    fi

    if [ "$FUNC_NAME" != "null" ] && [ "$FUNC_NAME" != "" ]; then
        echo -e "${GREEN}✓ function.name 字段存在${NC}"
    else
        echo -e "${RED}❌ function.name 字段缺失${NC}"
    fi
else
    echo -e "${YELLOW}⚠ 模型未调用工具（返回了文本响应）${NC}"
    echo "这可能是正常的，因为模型可以选择不使用工具。"
    echo ""
    echo "模型的文本响应："
    echo "$RESPONSE" | jq -r '.choices[0].message.content' | head -c 200
fi

# ========================================
# 测试2: 强制工具调用
# ========================================
echo -e "\n\n${BLUE}测试2: 使用 tool_choice 强制调用工具${NC}"
RESPONSE2=$(curl -s -X POST $BASE_URL \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "Beijing weather"}],
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
    "tool_choice": {"type": "function", "function": {"name": "get_weather"}},
    "stream": false
  }')

echo "tool_calls 字段："
TOOL_CALLS2=$(echo "$RESPONSE2" | jq '.choices[0].message.tool_calls')
echo "$TOOL_CALLS2"

if [ "$TOOL_CALLS2" != "null" ]; then
    echo -e "${GREEN}✓ tool_choice 成功触发工具调用${NC}"
else
    echo -e "${YELLOW}⚠ tool_choice 未能触发工具调用${NC}"
    echo "finish_reason: $(echo "$RESPONSE2" | jq -r '.choices[0].finish_reason')"
fi

# ========================================
# 测试3: 流式工具调用
# ========================================
echo -e "\n\n${BLUE}测试3: 流式工具调用${NC}"
STREAM_RESPONSE=$(timeout 5 curl -N -s -X POST $BASE_URL \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "glm-4.5",
    "messages": [{"role": "user", "content": "Call get_weather for Beijing"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather",
        "parameters": {"type": "object", "properties": {"location": {"type": "string"}}}
      }
    }],
    "stream": true
  }')

echo "流式响应片段（前20行）："
echo "$STREAM_RESPONSE" | head -20

# 检查是否有 tool_calls
if echo "$STREAM_RESPONSE" | grep -q "tool_calls"; then
    echo -e "\n${GREEN}✓ 流式响应包含 tool_calls${NC}"

    # 提取并显示 tool_calls 数据
    echo "$STREAM_RESPONSE" | grep "tool_calls" | head -3
else
    echo -e "\n${YELLOW}⚠ 流式响应不包含 tool_calls${NC}"
fi

# ========================================
# 测试4: 查看服务器日志
# ========================================
echo -e "\n\n${BLUE}测试4: 服务器日志（最后30行）${NC}"
echo -e "${YELLOW}查找工具调用相关日志...${NC}"
tail -30 /tmp/z2api_test.log | grep -i "tool\|function" || echo "无工具相关日志"

# ========================================
# 总结
# ========================================
echo -e "\n${BLUE}=====================================${NC}"
echo -e "${BLUE}  测试总结${NC}"
echo -e "${BLUE}=====================================${NC}"

echo -e "\n${YELLOW}注意事项：${NC}"
echo "1. 如果所有测试都返回 null，可能原因："
echo "   - 上游模型选择不使用工具（这是正常行为）"
echo "   - 模型版本不支持工具调用"
echo "   - 需要有效的 UPSTREAM_TOKEN"
echo ""
echo "2. 验证 Claude Code 兼容性的关键："
echo "   - 确保没有 'index' 字段"
echo "   - 确保 'id', 'type', 'function' 都存在"
echo "   - 确保字段不为空"

# 清理
echo -e "\n${YELLOW}正在停止服务器...${NC}"
kill $SERVER_PID 2>/dev/null
wait $SERVER_PID 2>/dev/null
echo -e "${GREEN}✓ 测试完成${NC}"
