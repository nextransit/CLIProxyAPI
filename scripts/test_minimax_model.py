#!/usr/bin/env python3
"""
Test MiniMax-M2.7-highspeed model availability via decard.cc provider in CPA
"""

import json
import sys
from urllib.request import Request, urlopen
from urllib.error import URLError, HTTPError

# Configuration
CPA_URL = "http://localhost:8085"
MODEL = "MiniMax-M2.7-highspeed"
PROVIDER = "decard.cc"


def test_model(api_key=None):
    """Test MiniMax-M2.7-highspeed availability"""
    print(f"Testing MiniMax-M2.7-highspeed via {PROVIDER}...")
    print(f"URL: {CPA_URL}")
    print(f"Model: {MODEL}")
    print()

    if not api_key:
        print("No API key provided, checking via logs instead...")
        import subprocess
        result = subprocess.run(
            ["grep", "-i", "MiniMax-M2.7-highspeed", 
             "/Users/zhouyong/gitlab/ai/CLIProxyAPI/nohup.out"],
            capture_output=True, text=True
        )
        if result.stdout:
            lines = result.stdout.strip().split('\n')
            print(f"Found {len(lines)} log entries:")
            for line in lines[-10:]:
                print(f"  {line}")
            return True
        else:
            print("No recent logs found")
            return False

    # Make a chat completion request
    url = f"{CPA_URL}/v1/chat/completions"
    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": "Hi"}],
        "max_tokens": 10
    }

    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {api_key}",
        "x-api-provider": PROVIDER
    }

    try:
        req = Request(url, data=json.dumps(payload).encode(), headers=headers, method='POST')
        with urlopen(req, timeout=30) as response:
            http_code = response.getcode()
            body = response.read().decode()
            
            print(f"HTTP Status: {http_code}")
            
            if http_code == 200:
                print("✅ SUCCESS: MiniMax-M2.7-highspeed is available")
                try:
                    data = json.loads(body)
                    if 'choices' in data:
                        print(f"Response: {data['choices'][0].get('message', {}).get('content', 'N/A')}")
                except:
                    pass
                return True
            else:
                print(f"❌ FAILED: HTTP {http_code}")
                print(body)
                return False

    except HTTPError as e:
        print(f"❌ HTTP Error: {e.code} - {e.reason}")
        try:
            error_body = e.read().decode()
            print(f"Error body: {error_body}")
        except:
            pass
        
        if e.code == 401 or e.code == 403:
            print("→ API key may not have access to this model")
        elif e.code == 429:
            print("→ Rate limited")
        
        return False
        
    except URLError as e:
        print(f"❌ URL Error: {e.reason}")
        print("→ Is CPA server running?")
        return False
        
    except Exception as e:
        print(f"❌ Unexpected error: {e}")
        return False


if __name__ == "__main__":
    api_key = sys.argv[1] if len(sys.argv) > 1 else None
    success = test_model(api_key)
    sys.exit(0 if success else 1)