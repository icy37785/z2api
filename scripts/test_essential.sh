#!/bin/bash

# 颜色
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}====================================="
echo "       核心功能测试"
echo "=====================================${NC}"

# 配置
export USE_OPTIMIZED_HANDLERS=true
export DEBUG_MODE=false
export PORT=8084
API_KEY="sk-tbkFoKzk9a531YyUNNF5"
BASE_URL="http://localhost:8084"

# 清理
killall z2api 2>/dev/null
sleep 1

# 启动服务器
echo "启动服务器..."
./z2api &
SERVER_PID=$!
sleep 2

# 测试结果统计
PASSED=0
FAILED=0

# 测试函数
test_api() {
    local name="$1"
    local cmd="$2"
    local expect="$3"

    echo -n "• $name: "

    result=$(eval "$cmd" 2>/dev/null)

    if echo "$result" | grep -q "$expect"; then
        echo -e "${GREEN}✓${NC}"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}✗${NC}"
        ((FAILED++))
        return 1
    fi
}

echo -e "\n${YELLOW}[基础测试]${NC}"
test_api "健康检查" \
    "curl -s $BASE_URL/health" \
    "healthy"

test_api "模型列表" \
    "curl -s $BASE_URL/v1/models -H 'Authorization: Bearer $API_KEY'" \
    "glm-4.5"

echo -e "\n${YELLOW}[对话测试]${NC}"
test_api "简单对话" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Say OK\"}],\"temperature\":0.1}'" \
    "choices"

test_api "中文对话" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"说你好\"}]}'" \
    "choices"

test_api "流式响应" \
    "timeout 2 curl -N -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"stream\":true}' | head -5" \
    "data:"

echo -e "\n${YELLOW}[模型测试]${NC}"
test_api "GLM-4.5-Air" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.5-air\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"max_tokens\":5}'" \
    "choices"

test_api "GLM-4.6" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"max_tokens\":5}'" \
    "choices"

echo -e "\n${YELLOW}[参数测试]${NC}"
test_api "自定义Temperature" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"temperature\":0.1,\"max_tokens\":10}'" \
    "choices"

test_api "MaxTokens限制" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"max_tokens\":5}'" \
    "choices"

echo -e "\n${YELLOW}[错误处理]${NC}"
test_api "无效密钥(401)" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer wrong' \
        -d '{\"model\":\"glm-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}]}'" \
    "401"

test_api "无消息(400)" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{\"model\":\"glm-4.5\",\"messages\":[]}'" \
    "error"

echo -e "\n${YELLOW}[工具调用]${NC}"
# 简化的工具调用测试 - 只测试请求是否成功
test_api "工具定义请求" \
    "curl -s -X POST $BASE_URL/v1/chat/completions \
        -H 'Content-Type: application/json' \
        -H 'Authorization: Bearer $API_KEY' \
        -d '{
            \"model\":\"glm-4.6\",
            \"messages\":[{\"role\":\"user\",\"content\":\"What is 2+2?\"}],
            \"tools\":[{
                \"type\":\"function\",
                \"function\":{
                    \"name\":\"calculator\",
                    \"description\":\"Calculate math\",
                    \"parameters\":{\"type\":\"object\",\"properties\":{}}
                }
            }],
            \"max_tokens\":50
        }'" \
    "choices"

echo -e "\n${BLUE}====================================="
echo -e "测试结果"
echo -e "=====================================${NC}"
echo -e "${GREEN}通过: $PASSED${NC}"
echo -e "${RED}失败: $FAILED${NC}"

if [ $FAILED -eq 0 ]; then
    echo -e "\n${GREEN}✨ 所有测试通过！${NC}"
else
    echo -e "\n${RED}⚠️  有 $FAILED 个测试失败${NC}"
fi

# 清理
kill $SERVER_PID 2>/dev/null
echo -e "\n服务器已停止"