#!/usr/bin/env python3
"""Test /v1/responses endpoint with tool calls"""

import json
import sys
from urllib.request import Request, urlopen
from urllib.error import URLError, HTTPError

CPA_URL = "http://localhost:8317"

def test_with_tool_call_output():
    """Test with function_call_output in responses format"""

    # This simulates what Codex sends when it uses tools
    payload = {
        "model": "MiniMax-M2.7-highspeed",
        "stream": True,
        "instructions": "You are a helpful assistant.",
        "input": [
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hi"}]},
            {"type": "function_call", "call_id": "call_123", "name": "test_tool", "arguments": "{}"},
            {"type": "function_call_output", "call_id": "call_123", "output": "result"}
        ]
    }

    url = f"{CPA_URL}/v1/responses"
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer sk-0Z1W4TtVGwuDrtEX019dCe29532e7a37Ac301b150c028aF1"
    }

    print(f"Testing {url}")
    print(f"Payload: {json.dumps(payload, indent=2)}")
    print()

    try:
        req = Request(url, data=json.dumps(payload).encode(), headers=headers, method='POST')
        with urlopen(req, timeout=60) as response:
            http_code = response.getcode()
            body = response.read().decode()
            print(f"HTTP Status: {http_code}")
            print(f"Response: {body[:500]}...")
            return True
    except HTTPError as e:
        print(f"HTTP Error: {e.code} - {e.reason}")
        try:
            error_body = e.read().decode()
            print(f"Error body: {error_body}")
        except:
            pass
        return False
    except Exception as e:
        print(f"Error: {e}")
        return False

def test_simple():
    """Test simple request without tools"""

    payload = {
        "model": "MiniMax-M2.7-highspeed",
        "stream": True,
        "instructions": "You are a helpful assistant.",
        "input": [
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hi"}]}
        ]
    }

    url = f"{CPA_URL}/v1/responses"
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer sk-0Z1W4TtVGwuDrtEX019dCe29532e7a37Ac301b150c028aF1"
    }

    print(f"Testing {url} (simple)")
    print()

    try:
        req = Request(url, data=json.dumps(payload).encode(), headers=headers, method='POST')
        with urlopen(req, timeout=60) as response:
            http_code = response.getcode()
            print(f"HTTP Status: {http_code}")
            return True
    except HTTPError as e:
        print(f"HTTP Error: {e.code}")
        try:
            error_body = e.read().decode()
            print(f"Error body: {error_body[:200]}")
        except:
            pass
        return False
    except Exception as e:
        print(f"Error: {e}")
        return False

if __name__ == "__main__":
    print("=== Test 1: Simple request ===")
    test_simple()
    print()
    print("=== Test 2: With tool_call_output ===")
    test_with_tool_call_output()