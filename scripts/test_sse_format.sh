#!/bin/bash

# 测试SSE流式响应格式是否符合OpenAI标准

echo "🧪 测试SSE流式响应格式"
echo "================================"

# 启动服务器（后台运行）
echo "📦 启动服务器..."
./z2api &
SERVER_PID=$!
sleep 3

# 测试函数
test_sse_format() {
    local test_name=$1
    local model=$2
    
    echo ""
    echo "🔍 测试: $test_name"
    echo "模型: $model"
    echo "---"
    
    # 发送请求并捕获响应
    response=$(curl -s -N http://localhost:8787/v1/chat/completions \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer test-key" \
        -d "{
            \"model\": \"$model\",
            \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}],
            \"stream\": true,
            \"max_tokens\": 50
        }")
    
    # 检查1: 中间chunk不应该有usage字段
    echo "✓ 检查中间chunk是否包含usage字段..."
    middle_chunks=$(echo "$response" | grep "^data: {" | head -n -1)
    if echo "$middle_chunks" | grep -q '"usage"'; then
        echo "❌ 失败: 中间chunk包含usage字段"
        echo "$middle_chunks" | grep '"usage"' | head -n 3
        return 1
    else
        echo "✅ 通过: 中间chunk不包含usage字段"
    fi
    
    # 检查2: 最后一个chunk应该有usage字段（如果有finish_reason）
    echo "✓ 检查最后chunk是否包含usage字段..."
    last_chunk=$(echo "$response" | grep "^data: {" | tail -n 1)
    if echo "$last_chunk" | grep -q '"finish_reason"'; then
        if echo "$last_chunk" | grep -q '"usage"'; then
            echo "✅ 通过: 最后chunk包含usage字段"
        else
            echo "⚠️  警告: 最后chunk有finish_reason但没有usage字段"
        fi
    fi
    
    # 检查3: 所有chunk不应该有message字段
    echo "✓ 检查chunk是否包含message字段..."
    if echo "$response" | grep "^data: {" | grep -q '"message"'; then
        echo "❌ 失败: chunk包含message字段（应该只有delta）"
        echo "$response" | grep "^data: {" | grep '"message"' | head -n 3
        return 1
    else
        echo "✅ 通过: chunk只包含delta字段，不包含message字段"
    fi
    
    # 检查4: 验证delta字段存在
    echo "✓ 检查delta字段..."
    if echo "$response" | grep "^data: {" | grep -q '"delta"'; then
        echo "✅ 通过: chunk包含delta字段"
    else
        echo "❌ 失败: chunk缺少delta字段"
        return 1
    fi
    
    echo "---"
    echo "✅ $test_name 测试通过"
    return 0
}

# 运行测试
test_sse_format "基础流式响应" "glm-4.5"

# 清理
echo ""
echo "🧹 清理..."
kill $SERVER_PID 2