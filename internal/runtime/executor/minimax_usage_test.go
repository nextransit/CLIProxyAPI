package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchMiniMaxUsage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		if r.Header.Get("Authorization") == "" {
			t.Error("Authorization header is missing")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", r.Header.Get("Content-Type"))
		}

		response := MiniMaxUsageResponse{
			StatusCode: 0,
			ModelRemains: []MiniMaxModelRemain{
				{
					ModelName:               "MiniMax-M2.7",
					CurrentWeeklyTotalCount: 15000,
					CurrentWeeklyUsageCount: 10000,
					WeeklyRemainsTime:       86400000, // 24 hours in ms
				},
			},
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Temporarily change endpoint for testing
	originalEndpoint := MiniMaxUsageEndpoint
	MiniMaxUsageEndpoint = server.URL + "/usage"
	defer func() { MiniMaxUsageEndpoint = originalEndpoint }()

	ctx := context.Background()
	result, err := FetchMiniMaxUsage(ctx, "test-api-key")
	if err != nil {
		t.Fatalf("FetchMiniMaxUsage() error = %v", err)
	}

	if result == nil {
		t.Fatal("FetchMiniMaxUsage() returned nil")
	}

	if len(result.ModelRemains) != 1 {
		t.Errorf("len(ModelRemains) = %d, want 1", len(result.ModelRemains))
	}

	if result.ModelRemains[0].ModelName != "MiniMax-M2.7" {
		t.Errorf("ModelName = %s, want MiniMax-M2.7", result.ModelRemains[0].ModelName)
	}
}

func TestMiniMaxUsageResponse_IsWeeklyQuotaExhausted(t *testing.T) {
	tests := []struct {
		name     string
		response MiniMaxUsageResponse
		want     bool
	}{
		{
			name: "quota not exhausted",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 10000},
				},
			},
			want: false,
		},
		{
			name: "quota exhausted (100% used)",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 15000},
				},
			},
			want: true,
		},
		{
			name: "quota exhausted (over limit)",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 16000},
				},
			},
			want: true,
		},
		{
			name:     "no model data",
			response: MiniMaxUsageResponse{},
			want:     false,
		},
		{
			name: "zero total count",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 0, CurrentWeeklyUsageCount: 0},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.response.IsWeeklyQuotaExhausted(); got != tt.want {
				t.Errorf("IsWeeklyQuotaExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMiniMaxUsageResponse_GetWeeklyUsagePercent(t *testing.T) {
	tests := []struct {
		name     string
		response MiniMaxUsageResponse
		want     float64
	}{
		{
			name: "66.67% used",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 10000},
				},
			},
			want: 66.67,
		},
		{
			name: "100% used",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 15000},
				},
			},
			want: 100.0,
		},
		{
			name: "0% used",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 15000, CurrentWeeklyUsageCount: 0},
				},
			},
			want: 0.0,
		},
		{
			name:     "no model data",
			response: MiniMaxUsageResponse{},
			want:     0,
		},
		{
			name: "zero total count",
			response: MiniMaxUsageResponse{
				ModelRemains: []MiniMaxModelRemain{
					{CurrentWeeklyTotalCount: 0, CurrentWeeklyUsageCount: 0},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.response.GetWeeklyUsagePercent()
			// Allow small floating point difference
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.1 {
				t.Errorf("GetWeeklyUsagePercent() = %v, want ~%v", got, tt.want)
			}
		})
	}
}

func TestMiniMaxUsageResponse_GetModelWeeklyUsage(t *testing.T) {
	response := MiniMaxUsageResponse{
		ModelRemains: []MiniMaxModelRemain{
			{ModelName: "MiniMax-M2.7", CurrentWeeklyTotalCount: 15000},
			{ModelName: "MiniMax-M2.5", CurrentWeeklyTotalCount: 10000},
		},
	}

	tests := []struct {
		modelName string
		wantCount int64
		wantFound bool
	}{
		{"MiniMax-M2.7", 15000, true},
		{"MiniMax-M2.5", 10000, true},
		{"NonExistent", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			m := response.GetModelWeeklyUsage(tt.modelName)
			if tt.wantFound {
				if m == nil {
					t.Errorf("GetModelWeeklyUsage(%s) returned nil, want non-nil", tt.modelName)
				} else if m.CurrentWeeklyTotalCount != tt.wantCount {
					t.Errorf("CurrentWeeklyTotalCount = %d, want %d", m.CurrentWeeklyTotalCount, tt.wantCount)
				}
			} else {
				if m != nil {
					t.Errorf("GetModelWeeklyUsage(%s) returned non-nil, want nil", tt.modelName)
				}
			}
		})
	}
}

func TestFetchMiniMaxUsage_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status_code": 0}`))
	}))
	defer server.Close()

	originalEndpoint := MiniMaxUsageEndpoint
	MiniMaxUsageEndpoint = server.URL
	defer func() { MiniMaxUsageEndpoint = originalEndpoint }()

	ctx := context.Background()
	_, err := FetchMiniMaxUsageWithTimeout(ctx, "test-key", 100*time.Millisecond)
	if err == nil {
		t.Error("FetchMiniMaxUsageWithTimeout() expected timeout error, got nil")
	}
}
