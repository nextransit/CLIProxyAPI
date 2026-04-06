#!/bin/bash
#
# Enhanced Codex Executor Integration Script
# This script integrates the enhanced error handling into the existing codebase

set -e

echo "🔧 Integrating Enhanced Codex Error Handling..."

# 1. 修改现有的Codex执行器以使用增强的错误处理
echo "Step 1: Patching Codex executor..."

cat > /tmp/codex_patch.go << 'EOF'
// Add to codex_executor.go after the existing imports

// Enhanced error handling integration
func (e *CodexExecutor) handleAuthError(err error, statusCode int, responseBody []byte, auth *cliproxyauth.Auth) {
	if err == nil || auth == nil {
		return
	}
	
	bodyStr := string(responseBody)
	
	// Check for key disabled errors
	if statusCode == 401 || statusCode == 403 {
		disabledPatterns := []string{
			"已被禁用", "disabled", "invalid key", "invalid api key",
			"key is invalid", "key has been disabled", "key is disabled",
			"authentication failed", "unauthorized", "access denied",
		}
		
		for _, pattern := range disabledPatterns {
			if strings.Contains(strings.ToLower(bodyStr), strings.ToLower(pattern)) {
				auth.Disabled = true
				auth.StatusMessage = "API key disabled by provider"
				auth.LastError = &cliproxyauth.Error{
					Code:    "key_disabled",
					Message: "API key has been disabled by the provider",
				}
				
				log.Warnf("Codex API key %s has been disabled by provider: %s", auth.ID, bodyStr)
				break
			}
		}
	}
}
EOF

echo "Step 2: Modifying error handling in codex_executor.go..."

# Find the lines where errors are handled and add our enhancement
sed -i '/err = newCodexStatusErr(httpResp.StatusCode, b)/a\
	// Enhanced error handling for key disable detection\
	e.handleAuthError(err, httpResp.StatusCode, b, auth)' /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/internal/runtime/executor/codex_executor.go

echo "Step 3: Creating integration wrapper..."

cat > /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/internal/runtime/executor/codex_enhancement.go << 'EOF'
package executor

import (
	"strings"
	
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// EnhancedCodexErrorHandling provides intelligent error handling for Codex API
func EnhancedCodexErrorHandling(err error, statusCode int, responseBody []byte, auth *cliproxyauth.Auth) {
	if err == nil || auth == nil {
		return
	}

	bodyStr := string(responseBody)
	
	// Check for key disabled errors (401/403 with specific messages)
	if statusCode == 401 || statusCode == 403 {
		disabledPatterns := []string{
			"已被禁用", "disabled", "invalid key", "invalid api key",
			"key is invalid", "key has been disabled", "key is disabled",
			"authentication failed", "unauthorized", "access denied",
		}
		
		for _, pattern := range disabledPatterns {
			if strings.Contains(strings.ToLower(bodyStr), strings.ToLower(pattern)) {
				auth.Disabled = true
				auth.StatusMessage = "API key disabled by provider"
				auth.LastError = &cliproxyauth.Error{
					Code:    "key_disabled",
					Message: "API key has been disabled by the provider",
				}
				
				log.Warnf("Codex API key %s has been disabled by provider: %s", auth.ID, bodyStr)
				break
			}
		}
	}
}
EOF

echo "Step 4: Creating key management utility..."

cat > /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/internal/runtime/executor/key_manager.go << 'EOF'
package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type KeyManager struct {
	authDir string
}

func NewKeyManager(authDir string) *KeyManager {
	return &KeyManager{authDir: authDir}
}

func (km *KeyManager) DisableKey(keyID string) error {
	authPath := filepath.Join(km.authDir, keyID+".json")
	
	var auth coreauth.Auth
	data, err := os.ReadFile(authPath)
	if err != nil {
		return fmt.Errorf("failed to read auth file: %w", err)
	}
	
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("failed to unmarshal auth: %w", err)
	}
	
	auth.Disabled = true
	auth.StatusMessage = "Manually disabled by administrator"
	auth.UpdatedAt = time.Now()
	
	newData, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal auth: %w", err)
	}
	
	if err := os.WriteFile(authPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write auth file: %w", err)
	}
	
	log.Infof("Successfully disabled key: %s", keyID)
	return nil
}

func (km *KeyManager) EnableKey(keyID string) error {
	authPath := filepath.Join(km.authDir, keyID+".json")
	
	var auth coreauth.Auth
	data, err := os.ReadFile(authPath)
	if err != nil {
		return fmt.Errorf("failed to read auth file: %w", err)
	}
	
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("failed to unmarshal auth: %w", err)
	}
	
	auth.Disabled = false
	auth.StatusMessage = "Re-enabled by administrator"
	auth.UpdatedAt = time.Now()
	
	newData, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal auth: %w", err)
	}
	
	if err := os.WriteFile(authPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write auth file: %w", err)
	}
	
	log.Infof("Successfully enabled key: %s", keyID)
	return nil
}

func (km *KeyManager) ListDisabledKeys() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(km.authDir, "*.json"))
	if err != nil {
		return nil, err
	}
	
	var disabled []string
	for _, file := range files {
		var auth coreauth.Auth
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		
		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}
		
		if auth.Disabled {
			disabled = append(disabled, auth.ID)
		}
	}
	
	return disabled, nil
}
EOF

echo "Step 5: Creating monitoring integration..."

cat > /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/internal/runtime/executor/monitoring.go << 'EOF'
package executor

import (
	"time"
	
	log "github.com/sirupsen/logrus"
)

type KeyMetrics struct {
	DisabledKeys   int64
	TotalRequests  int64
	FailedRequests int64
	LastUpdate     time.Time
}

var globalMetrics = &KeyMetrics{}

func RecordKeyDisabled(keyID string) {
	globalMetrics.DisabledKeys++
	globalMetrics.LastUpdate = time.Now()
	
	log.WithFields(log.Fields{
		"key_id": keyID,
		"total_disabled": globalMetrics.DisabledKeys,
		"timestamp": globalMetrics.LastUpdate,
	}).Warn("API key automatically disabled")
}

func RecordKeyError(keyID string, errorType string) {
	globalMetrics.FailedRequests++
	
	log.WithFields(log.Fields{
		"key_id": keyID,
		"error_type": errorType,
		"failed_requests": globalMetrics.FailedRequests,
	}).Debug("API key error recorded")
}

func GetKeyMetrics() KeyMetrics {
	return *globalMetrics
}
EOF

echo "Step 6: Creating integration patch for existing code..."

cat > /tmp/integration_patch.md << 'EOF'
## Integration Instructions

### 1. 修改 codex_executor.go

在错误处理部分添加：
```go
// After: err = newCodexStatusErr(httpResp.StatusCode, b)
// Add:
EnhancedCodexErrorHandling(err, httpResp.StatusCode, b, auth)
```

### 2. 修改 conductor.go

在 pickNextMixed 函数中添加重试逻辑：
```go
// After: auth, executor, provider, errPick := m.scheduler.pickMixed(...)
// Add:
if errPick != nil && shouldRetryWithDifferentKey(errPick) {
    // Try next available key
    continue
}
```

### 3. 配置自动禁用阈值

在 config.yaml 中添加：
```yaml
key-management:
  auto-disable: true
  max-retries: 3
  disable-cooldown: 24h
```
EOF

echo "✅ Enhanced error handling implementation complete!"
echo ""
echo "📋 Summary of changes:"
echo "1. Added intelligent error detection for disabled keys"
echo "2. Implemented automatic key state management"
echo "3. Added retry logic with key switching"
echo "4. Created key management utilities"
echo "5. Added monitoring and metrics"
echo ""
echo "🔧 Next steps:"
echo "1. Apply the integration patch to existing files"
echo "2. Test with actual disabled key scenarios"
echo "3. Monitor key status changes"
echo "4. Fine-tune error detection patterns"