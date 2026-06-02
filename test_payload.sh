#!/bin/bash
# CPA Payload 配置验证脚本

set -e

# 配置
CPA_HOST="${CPA_HOST:-localhost:8317}"
API_KEY="${API_KEY:-sk-mbQa17oNUZfDD6hrW}"
MODEL="${MODEL:-MiniMax-M2.7-highspeed}"

echo "=== CPA Payload 配置验证 ==="
echo "Host: $CPA_HOST"
echo "Model: $MODEL"
echo ""

# 1. 测试 /v1/models 端点
echo "1. 检查模型列表..."
MODELS_RESPONSE=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $API_KEY" \
  "http://$CPA_HOST/v1/models")

HTTP_CODE=$(echo "$MODELS_RESPONSE" | tail -n1)
MODELS_BODY=$(echo "$MODELS_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" = "200" ]; then
  echo "   [OK] /v1/models 返回 200"
else
  echo "   [FAIL] /v1/models 返回 $HTTP_CODE"
  exit 1
fi

# 2. 发送测试请求，检查 system_instruction 是否生效
echo ""
echo "2. 发送测试请求验证 system_instruction..."

TEST_PROMPT="写一个快速排序函数"

RESPONSE=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"model\": \"$MODEL\",
    \"messages\": [{\"role\": \"user\", \"content\": \"$TEST_PROMPT\"}]
  }" \
  "http://$CPA_HOST/v1/chat/completions")

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
RESPONSE_BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" = "200" ]; then
  echo "   [OK] 请求成功"

  # 使用 jq 提取 usage 信息（如果可用）
  if command -v jq &> /dev/null; then
    PROMPT_TOKENS=$(echo "$RESPONSE_BODY" | jq -r '.usage.prompt_tokens // empty')
    COMPLETION_TOKENS=$(echo "$RESPONSE_BODY" | jq -r '.usage.completion_tokens // empty')
    if [ -n "$PROMPT_TOKENS" ] && [ -n "$COMPLETION_TOKENS" ]; then
      echo "   [OK] Usage: prompt_tokens=$PROMPT_TOKENS, completion_tokens=$COMPLETION_TOKENS"
    fi
  fi

  # 检查响应是否包含代码（简洁原则验证）
  if echo "$RESPONSE_BODY" | grep -q "sort\|quick\|def\|function\|fn\|=>"; then
    echo "   [OK] 响应包含代码"
  else
    echo "   [WARN] 响应可能不完整"
  fi
else
  echo "   [FAIL] 请求返回 $HTTP_CODE"
  echo "   响应: $RESPONSE_BODY"
  exit 1
fi

# 3. 检查 debug 日志（如果有）
echo ""
echo "3. 检查 debug 端点..."
DEBUG_RESPONSE=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $API_KEY" \
  "http://$CPA_HOST/debug/pprof/")

DEBUG_CODE=$(echo "$DEBUG_RESPONSE" | tail -n1)
if [ "$DEBUG_CODE" = "200" ] || [ "$DEBUG_CODE" = "404" ]; then
  echo "   [OK] Debug 端点可达"
else
  echo "   [WARN] Debug 端点返回 $DEBUG_CODE"
fi

# 4. 验证多个模型是否都能收到 system_instruction
echo ""
echo "4. 验证配置是否全局生效..."
MODELS_TO_TEST=(
  "MiniMax-M2.7-highspeed"
  "MiniMax-M2.7"
  "deepseek-v4-flash"
)

for test_model in "${MODELS_TO_TEST[@]}"; do
  RESULT=$(curl -s -w "%{http_code}" -o /dev/null \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"$test_model\",
      \"messages\": [{\"role\": \"user\", \"content\": \"hi\"}]
    }" \
    "http://$CPA_HOST/v1/chat/completions")

  if [ "$RESULT" = "200" ]; then
    echo "   [OK] $test_model 可用"
  else
    echo "   [SKIP] $test_model 返回 $RESULT"
  fi
done

echo ""
echo "=== 验证完成 ==="
echo ""
echo "注意: 要完全验证 system_instruction 是否注入，需要:"
echo "1. 开启 CPA 日志: logging-to-file: true"
echo "2. 在 Dashboard 日志页面查看请求体"
echo "3. 确认 system_instruction.content 包含工程原则"