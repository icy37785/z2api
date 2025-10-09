#!/bin/bash

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 测试函数
run_test() {
    local test_name=$1
    local command=$2
    ((TOTAL_TESTS++))

    echo -e "\n${BLUE}[TEST $TOTAL_TESTS]${NC} $test_name"
    echo "----------------------------------------"

    if eval "$command"; then
        ((PASSED_TESTS++))
        echo -e "${GREEN}✓ PASSED${NC}"
    else
        ((FAILED_TESTS++))
        echo -e "${RED}✗ FAILED${NC}"
    fi
}

echo "====================================="
echo "    综合测试 - API 功能全覆盖"
echo "====================================="

# 设置环境变量
export USE_OPTIMIZED_HANDLERS=true
export DEBUG_MODE=true
export PORT=8082
export API_KEY="sk-tbkFoKzk9a531YyUNNF5"
BASE_URL="http://localhost:8082"

# 启动服务器
echo -e "${YELLOW}启动服务器（端口 8082）...${NC}"
./z2api &
SERVER_PID=$!

# 等待服务器启动
sleep 3

echo -e "\n${BLUE}开始测试...${NC}\n"

# 1. 健康检查
run_test "健康检查端点" \
"curl -s $BASE_URL/health | jq -e '.status == \"healthy\"' > /dev/null"

# 2. 模型列表
run_test "模型列表端点" \
"curl -s $BASE_URL/v1/models -H 'Authorization: Bearer $API_KEY' | jq -e '.data | length > 0' > /dev/null"

# 3. 基础非流式请求
run_test "基础非流式聊天（中文）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"说你好\"}],
    \"stream\": false
  }' | jq -e '.choices[0].message.content' > /dev/null"

# 4. 英文请求
run_test "非流式聊天（英文）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Say hello\"}],
    \"stream\": false
  }' | jq -e '.choices[0].message.content' > /dev/null"

# 5. 多轮对话
run_test "多轮对话上下文" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [
      {\"role\": \"user\", \"content\": \"我叫小明\"},
      {\"role\": \"assistant\", \"content\": \"你好小明，很高兴认识你！\"},
      {\"role\": \"user\", \"content\": \"我叫什么名字？\"}
    ],
    \"stream\": false
  }' | jq -e '.choices[0].message.content' > /dev/null"

# 6. 系统消息
run_test "包含系统消息的请求" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [
      {\"role\": \"system\", \"content\": \"You are a helpful assistant. Always respond in JSON format.\"},
      {\"role\": \"user\", \"content\": \"What is 2+2?\"}
    ],
    \"stream\": false,
    \"temperature\": 0.1
  }' | jq -e '.choices[0].message.content' > /dev/null"

# 7. 工具调用测试
echo -e "\n${YELLOW}测试工具调用功能...${NC}"
run_test "工具调用（天气查询）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"What is the weather in Beijing?\"}],
    \"tools\": [
      {
        \"type\": \"function\",
        \"function\": {
          \"name\": \"get_weather\",
          \"description\": \"Get the current weather in a given location\",
          \"parameters\": {
            \"type\": \"object\",
            \"properties\": {
              \"location\": {
                \"type\": \"string\",
                \"description\": \"The city and state, e.g. San Francisco, CA\"
              }
            },
            \"required\": [\"location\"]
          }
        }
      }
    ],
    \"tool_choice\": \"auto\",
    \"stream\": false
  }' | jq -e '.choices[0]' > /dev/null"

# 8. 工具调用（计算器）
run_test "工具调用（数学计算）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Calculate 15 * 23 + 47\"}],
    \"tools\": [
      {
        \"type\": \"function\",
        \"function\": {
          \"name\": \"calculator\",
          \"description\": \"Perform mathematical calculations\",
          \"parameters\": {
            \"type\": \"object\",
            \"properties\": {
              \"expression\": {
                \"type\": \"string\",
                \"description\": \"The mathematical expression to evaluate\"
              }
            },
            \"required\": [\"expression\"]
          }
        }
      }
    ],
    \"stream\": false
  }' | jq -e '.choices[0]' > /dev/null"

# 9. 流式响应测试
echo -e "\n${YELLOW}测试流式响应...${NC}"
run_test "基础流式响应" \
"timeout 3 curl -N -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Count to 5\"}],
    \"stream\": true
  }' 2>/dev/null | grep -q 'data:' && echo 'Stream working'"

# 10. 不同模型测试
echo -e "\n${YELLOW}测试不同模型...${NC}"

# GLM-4.5 Air
run_test "GLM-4.5-Air 模型" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5-air\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}],
    \"stream\": false
  }' | jq -e '.model' > /dev/null"

# GLM-4.6
run_test "GLM-4.6 模型" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}],
    \"stream\": false
  }' | jq -e '.model' > /dev/null"

# 11. 参数测试
echo -e "\n${YELLOW}测试各种参数配置...${NC}"

run_test "自定义 temperature 参数" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Generate a random word\"}],
    \"temperature\": 1.5,
    \"stream\": false
  }' | jq -e '.choices[0].message' > /dev/null"

run_test "自定义 max_tokens 参数" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Tell me a story\"}],
    \"max_tokens\": 50,
    \"stream\": false
  }' | jq -e '.choices[0].message' > /dev/null"

run_test "自定义 top_p 参数" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}],
    \"top_p\": 0.5,
    \"stream\": false
  }' | jq -e '.choices[0].message' > /dev/null"

# 12. 错误处理测试
echo -e "\n${YELLOW}测试错误处理...${NC}"

run_test "无效API密钥（应该失败）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer invalid-key' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}]
  }' | jq -e '.error.code == 401' > /dev/null"

run_test "缺少消息（应该失败）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5\",
    \"messages\": []
  }' | jq -e '.error' > /dev/null"

run_test "无效JSON格式（应该失败）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d 'invalid json' | jq -e '.error.code == 400' > /dev/null"

# 13. 多模态测试（如果支持）
echo -e "\n${YELLOW}测试多模态功能...${NC}"

run_test "图像URL（base64）" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.5v\",
    \"messages\": [
      {
        \"role\": \"user\",
        \"content\": [
          {\"type\": \"text\", \"text\": \"What is in this image?\"},
          {\"type\": \"image_url\", \"image_url\": {\"url\": \"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==\"}}
        ]
      }
    ],
    \"stream\": false
  }' | jq -e '.choices[0]' > /dev/null"

# 14. 并发测试
echo -e "\n${YELLOW}测试并发处理...${NC}"

run_test "并发请求（3个同时）" \
"(
  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer $API_KEY' \
    -d '{\"model\": \"glm-4.6\", \"messages\": [{\"role\": \"user\", \"content\": \"Test 1\"}]}' &
  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer $API_KEY' \
    -d '{\"model\": \"glm-4.6\", \"messages\": [{\"role\": \"user\", \"content\": \"Test 2\"}]}' &
  curl -s -X POST $BASE_URL/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer $API_KEY' \
    -d '{\"model\": \"glm-4.6\", \"messages\": [{\"role\": \"user\", \"content\": \"Test 3\"}]}' &
  wait
) && echo 'Concurrent requests completed'"

# 15. 特殊字符测试
echo -e "\n${YELLOW}测试特殊字符处理...${NC}"

run_test "Emoji 和 Unicode" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Respond with: 😊 你好 世界 🌍\"}],
    \"stream\": false
  }' | jq -e '.choices[0].message.content' > /dev/null"

run_test "转义字符" \
"curl -s -X POST $BASE_URL/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer $API_KEY' \
  -d '{
    \"model\": \"glm-4.6\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Print: \\\"Hello\\\" and \\n newline\"}],
    \"stream\": false
  }' | jq -e '.choices[0].message' > /dev/null"

# 16. Dashboard 测试
echo -e "\n${YELLOW}测试 Dashboard 端点...${NC}"

run_test "Dashboard 页面" \
"curl -s $BASE_URL/dashboard | grep -q '<title>' && echo 'Dashboard HTML OK'"

run_test "Dashboard 统计 API" \
"curl -s $BASE_URL/dashboard/stats | jq -e '.totalRequests >= 0' > /dev/null"

run_test "Dashboard 实时请求 API" \
"curl -s $BASE_URL/dashboard/requests | jq -e '. | type == \"array\"' > /dev/null"

# 结果汇总
echo -e "\n====================================="
echo -e "${BLUE}测试完成${NC}"
echo "====================================="
echo -e "总测试数: $TOTAL_TESTS"
echo -e "${GREEN}通过: $PASSED_TESTS${NC}"
echo -e "${RED}失败: $FAILED_TESTS${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "\n${GREEN}✨ 所有测试通过！${NC}"
    EXIT_CODE=0
else
    echo -e "\n${RED}⚠️  有 $FAILED_TESTS 个测试失败${NC}"
    EXIT_CODE=1
fi

# 停止服务器
echo -e "\n${YELLOW}停止服务器...${NC}"
kill $SERVER_PID 2>/dev/null
wait $SERVER_PID 2>/dev/null

echo "====================================="
exit $EXIT_CODE