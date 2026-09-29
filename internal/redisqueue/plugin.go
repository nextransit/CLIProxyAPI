package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !internalusage.StatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	if requestID == "" {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			requestID = strings.TrimSpace(internallogging.GetGinRequestID(ginCtx))
		}
	}

	tokens := internalusage.TokenStats{
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	// Match the central usage statistics rule: only an observed HTTP 200 is a
	// success. Prefer the upstream status captured by executors, then the Gin
	// response status; keep the explicit failure marker as the final fallback.
	statusCode := resolveStatusCode(ctx, record.StatusCode)
	failed := record.Failed
	if statusCode > 0 {
		failed = statusCode != http.StatusOK
	} else if !failed {
		failed = !resolveSuccess(ctx)
	}

	detail := internalusage.RequestDetail{
		Timestamp:  timestamp,
		LatencyMs:  record.Latency.Milliseconds(),
		Source:     record.Source,
		AuthIndex:  record.AuthIndex,
		StatusCode: statusCode,
		Tokens:     tokens,
		Failed:     failed,
	}

	payload, err := json.Marshal(queuedUsageDetail{
		RequestDetail: detail,
		Provider:      provider,
		Model:         modelName,
		Endpoint:      resolveEndpoint(ctx),
		AuthType:      authType,
		APIKey:        apiKey,
		RequestID:     requestID,
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	internalusage.RequestDetail
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Endpoint  string `json:"endpoint"`
	AuthType  string `json:"auth_type"`
	APIKey    string `json:"api_key"`
	RequestID string `json:"request_id"`
}

// resolveStatusCode returns the best observed HTTP status for a queued usage
// record: the upstream status captured by the executor when available,
// otherwise the Gin response status.
func resolveStatusCode(ctx context.Context, recordStatus int) int {
	if recordStatus > 0 {
		return recordStatus
	}
	if ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Writer == nil {
		return 0
	}
	return ginCtx.Writer.Status()
}

// resolveSuccess reports success only for an explicit HTTP 200 status.
// A zero status is treated as legacy success when no failure marker exists.
func resolveSuccess(ctx context.Context) bool {
	status := resolveStatusCode(ctx, 0)
	if status == 0 {
		return true
	}
	return status == http.StatusOK
}

func resolveEndpoint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return ""
	}

	path := strings.TrimSpace(ginCtx.FullPath())
	if path == "" && ginCtx.Request.URL != nil {
		path = strings.TrimSpace(ginCtx.Request.URL.Path)
	}
	if path == "" {
		return ""
	}

	method := strings.TrimSpace(ginCtx.Request.Method)
	if method == "" {
		return path
	}
	return method + " " + path
}
