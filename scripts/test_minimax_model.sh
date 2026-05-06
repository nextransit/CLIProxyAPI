#!/bin/bash
# Test MiniMax-M2.7-highspeed model availability via decard.cc provider in CPA

set -e

# Configuration
CPA_URL="${CPA_URL:-http://localhost:8085}"
MODEL="MiniMax-M2.7-highspeed"
PROVIDER="decard.cc"

# Test API key - use a test key or skip if not provided
TEST_API_KEY="${1:-}"

if [ -z "$TEST_API_KEY" ]; then
    echo "Usage: $0 <api-key>"
    echo "Testing MiniMax-M2.7-highspeed via decard.cc provider..."
    echo ""
    # Check if we can use the management API instead
    echo "Attempting to check via management API..."
    
    # Try to get model list from management API
    RESPONSE=$(curl -s -w "\n%{http_code}" "$CPA_URL/v0/management/config" 2>/dev/null || echo "000")
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    
    if [ "$HTTP_CODE" = "200" ]; then
        echo "✅ CPA is running"
        echo ""
        echo "Checking logs for MiniMax-M2.7-highspeed availability..."
        grep -i "MiniMax-M2.7-highspeed" /Users/zhouyong/gitlab/ai/CLIProxyAPI/nohup.out 2>/dev/null | tail -10 || echo "No recent logs found"
        exit 0
    else
        echo "❌ CPA not accessible (HTTP $HTTP_CODE)"
        exit 1
    fi
fi

echo "Testing MiniMax-M2.7-highspeed via $PROVIDER..."
echo "URL: $CPA_URL"
echo "Model: $MODEL"
echo ""

# Make a simple chat completion request
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$CPA_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TEST_API_KEY" \
    -H "x-api-provider: $PROVIDER" \
    -d "{
        \"model\": \"$MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Hi\"}],
        \"max_tokens\": 10
    }" 2>&1)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

echo "HTTP Status: $HTTP_CODE"
echo "Response: $BODY"

if [ "$HTTP_CODE" = "200" ]; then
    echo ""
    echo "✅ SUCCESS: MiniMax-M2.7-highspeed is available via $PROVIDER"
    exit 0
elif [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ]; then
    echo ""
    echo "❌ UNAUTHORIZED: API key may not have access to $MODEL"
    exit 1
elif [ "$HTTP_CODE" = "429" ]; then
    echo ""
    echo "⚠️  RATE LIMITED: Too many requests"
    exit 2
else
    echo ""
    echo "❌ FAILED: HTTP $HTTP_CODE"
    echo "$BODY"
    exit 1
fi