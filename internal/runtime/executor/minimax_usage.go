// Package executor provides runtime execution logic for various LLM providers.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

// MiniMaxUsageResponse represents the response from MiniMax usage API
type MiniMaxUsageResponse struct {
	BaseResp     *MiniMaxBaseResp     `json:"base_resp,omitempty"`
	StatusCode   int                  `json:"status_code,omitempty"`
	StatusMsg    string               `json:"status_msg,omitempty"`
	ModelRemains []MiniMaxModelRemain `json:"model_remains,omitempty"`
}

// MiniMaxBaseResp represents the base response structure
type MiniMaxBaseResp struct {
	StatusCode int    `json:"status_code,omitempty"`
	StatusMsg  string `json:"status_msg,omitempty"`
}

// MiniMaxModelRemain represents usage information for a single model
type MiniMaxModelRemain struct {
	StartTime                 int64  `json:"start_time,omitempty"`
	EndTime                   int64  `json:"end_time,omitempty"`
	RemainsTime               int64  `json:"remains_time,omitempty"`
	CurrentIntervalTotalCount int64  `json:"current_interval_total_count,omitempty"`
	CurrentIntervalUsageCount int64  `json:"current_interval_usage_count,omitempty"`
	ModelName                 string `json:"model_name,omitempty"`
	CurrentWeeklyTotalCount   int64  `json:"current_weekly_total_count,omitempty"`
	CurrentWeeklyUsageCount   int64  `json:"current_weekly_usage_count,omitempty"`
	WeeklyRemainsTime         int64  `json:"weekly_remains_time,omitempty"`
}

const (
	// MiniMaxUsageEndpoint is the API endpoint for querying MiniMax usage/quota
	MiniMaxUsageEndpoint = "https://www.minimaxi.com/v1/api/openplatform/coding_plan/remains"
	// MiniMaxUsageTimeout is the default timeout for usage API calls
	MiniMaxUsageTimeout = 10 * time.Second
)

// FetchMiniMaxUsage fetches the usage information from MiniMax API
func FetchMiniMaxUsage(ctx context.Context, apiKey string) (*MiniMaxUsageResponse, error) {
	return FetchMiniMaxUsageWithTimeout(ctx, apiKey, MiniMaxUsageTimeout)
}

// FetchMiniMaxUsageWithTimeout fetches the usage information with a custom timeout
func FetchMiniMaxUsageWithTimeout(ctx context.Context, apiKey string, timeout time.Duration) (*MiniMaxUsageResponse, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is empty")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", MiniMaxUsageEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var result MiniMaxUsageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// IsWeeklyQuotaExhausted checks if the weekly quota is exhausted for any model
func (r *MiniMaxUsageResponse) IsWeeklyQuotaExhausted() bool {
	if r.ModelRemains == nil || len(r.ModelRemains) == 0 {
		return false
	}
	for _, model := range r.ModelRemains {
		// Check weekly usage: if usage >= total, quota is exhausted
		if model.CurrentWeeklyTotalCount > 0 && model.CurrentWeeklyUsageCount >= model.CurrentWeeklyTotalCount {
			return true
		}
	}
	return false
}

// GetWeeklyUsagePercent returns the usage percentage for the primary model
func (r *MiniMaxUsageResponse) GetWeeklyUsagePercent() float64 {
	if r.ModelRemains == nil || len(r.ModelRemains) == 0 {
		return 0
	}
	m := r.ModelRemains[0]
	if m.CurrentWeeklyTotalCount == 0 {
		return 0
	}
	return float64(m.CurrentWeeklyUsageCount) / float64(m.CurrentWeeklyTotalCount) * 100
}

// GetWeeklyRemainsTime returns the remaining time until weekly quota reset (in milliseconds)
func (r *MiniMaxUsageResponse) GetWeeklyRemainsTime() int64 {
	if r.ModelRemains == nil || len(r.ModelRemains) == 0 {
		return 0
	}
	return r.ModelRemains[0].WeeklyRemainsTime
}

// GetModelWeeklyUsage finds the weekly usage for a specific model
func (r *MiniMaxUsageResponse) GetModelWeeklyUsage(modelName string) *MiniMaxModelRemain {
	if r.ModelRemains == nil {
		return nil
	}
	for i := range r.ModelRemains {
		if r.ModelRemains[i].ModelName == modelName {
			return &r.ModelRemains[i]
		}
	}
	return nil
}

// LogUsageInfo logs the current usage information at debug level
func (r *MiniMaxUsageResponse) LogUsageInfo() {
	if r.ModelRemains == nil || len(r.ModelRemains) == 0 {
		log.Debug("MiniMax usage: no model data available")
		return
	}

	for _, m := range r.ModelRemains {
		usagePercent := float64(m.CurrentWeeklyUsageCount) / float64(m.CurrentWeeklyTotalCount) * 100
		log.Debugf("MiniMax usage for model %s: weekly %d/%d (%.1f%%), reset in %d ms",
			m.ModelName, m.CurrentWeeklyUsageCount, m.CurrentWeeklyTotalCount, usagePercent, m.WeeklyRemainsTime)
	}
}
