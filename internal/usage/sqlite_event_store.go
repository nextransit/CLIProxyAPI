package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	_ "modernc.org/sqlite"
)

const defaultUsageSQLiteFileName = "usage_events.sqlite"

// UsageEventStore is the append-only durable event sink for usage accounting.
type UsageEventStore interface {
	InsertUsageEvent(ctx context.Context, record coreusage.Record, evt UsageEvent) error
	ImportSnapshot(ctx context.Context, snapshot StatisticsSnapshot) (int64, error)
	Path() string
	Close() error
}

// SQLiteUsageEventStore stores per-request usage facts and rollups in SQLite.
type SQLiteUsageEventStore struct {
	mu       sync.RWMutex
	db       *sql.DB
	filePath string
}

// NewSQLiteUsageEventStore creates or opens the SQLite usage event store.
func NewSQLiteUsageEventStore(baseDir string) (*SQLiteUsageEventStore, error) {
	if baseDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			baseDir = cwd
		} else {
			baseDir = os.TempDir()
		}
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("create usage db directory: %w", err)
	}
	if err := os.Chmod(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("secure usage db directory: %w", err)
	}
	path := filepath.Join(baseDir, defaultUsageSQLiteFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open usage sqlite store: %w", err)
	}
	store := &SQLiteUsageEventStore{db: db, filePath: path}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure usage sqlite store: %w", err)
	}
	return store, nil
}

func (s *SQLiteUsageEventStore) init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("usage sqlite store is closed")
	}
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS usage_events (
			event_id INTEGER PRIMARY KEY,
			requested_at_unix_ms INTEGER NOT NULL,
			requested_at TEXT NOT NULL,
			api_key_hash TEXT NOT NULL,
			api_key_mask TEXT NOT NULL,
			provider TEXT,
			model TEXT NOT NULL,
			source TEXT,
			auth_id_hash TEXT,
			auth_index TEXT,
			auth_type TEXT,
			request_id TEXT,
			status_code INTEGER NOT NULL,
			failed INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			token_source TEXT NOT NULL,
			request_json TEXT,
			model_info_json TEXT,
			thinking_json TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_requested_at ON usage_events(requested_at_unix_ms DESC, event_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_model_time ON usage_events(model, requested_at_unix_ms DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_api_time ON usage_events(api_key_hash, requested_at_unix_ms DESC)`,
		`CREATE TABLE IF NOT EXISTS usage_rollups (
			period TEXT NOT NULL,
			bucket_start_unix_ms INTEGER NOT NULL,
			api_key_hash TEXT NOT NULL,
			api_key_mask TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL,
			auth_index TEXT NOT NULL,
			requests INTEGER NOT NULL,
			failures INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			latency_sum_ms INTEGER NOT NULL,
			latency_count INTEGER NOT NULL,
			PRIMARY KEY (period, bucket_start_unix_ms, api_key_hash, model, source, auth_index)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_rollups_period_bucket ON usage_rollups(period, bucket_start_unix_ms DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize usage sqlite store: %w", err)
		}
	}
	return nil
}

// InsertUsageEvent appends one usage event and updates minute/hour/day rollups.
func (s *SQLiteUsageEventStore) InsertUsageEvent(ctx context.Context, record coreusage.Record, evt UsageEvent) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("usage sqlite store is closed")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage event transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	inserted := false
	if inserted, err = insertUsageEventTx(ctx, tx, record, evt); err != nil {
		return err
	}
	if inserted {
		err = upsertRollupsTx(ctx, tx, record, evt)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit usage event transaction: %w", err)
	}
	return nil
}

// ImportSnapshot imports retained legacy details. Existing event IDs are skipped.
func (s *SQLiteUsageEventStore) ImportSnapshot(ctx context.Context, snapshot StatisticsSnapshot) (int64, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return 0, fmt.Errorf("usage sqlite store is closed")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin usage import transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var imported int64
	for apiKey, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if detail.Timestamp.IsZero() {
					continue
				}
				eventID := detail.EventID
				if eventID == 0 {
					eventID = detailEventID(detail, apiKey, modelName)
				}
				record := coreusage.Record{
					Model:       modelName,
					APIKey:      apiKey,
					Source:      detail.Source,
					AuthIndex:   detail.AuthIndex,
					RequestID:   detail.RequestID,
					StatusCode:  detail.StatusCode,
					RequestedAt: detail.Timestamp,
					Latency:     time.Duration(detail.LatencyMs) * time.Millisecond,
					Failed:      detail.Failed,
					Detail: coreusage.Detail{
						InputTokens:     detail.Tokens.InputTokens,
						OutputTokens:    detail.Tokens.OutputTokens,
						ReasoningTokens: detail.Tokens.ReasoningTokens,
						CachedTokens:    detail.Tokens.CachedTokens,
						TotalTokens:     detail.Tokens.TotalTokens,
						Thinking:        toCoreThinking(detail.Thinking),
					},
				}
				evt := UsageEvent{
					ID:        eventID,
					APIKey:    apiKey,
					Model:     modelName,
					Source:    detail.Source,
					AuthIndex: detail.AuthIndex,
					RequestID: detail.RequestID,
					Failed:    detail.Failed,
					Tokens: TokenSummary{
						Input:     detail.Tokens.InputTokens,
						Output:    detail.Tokens.OutputTokens,
						Reasoning: detail.Tokens.ReasoningTokens,
						Cached:    detail.Tokens.CachedTokens,
						Total:     detail.Tokens.TotalTokens,
					},
					Thinking:    detail.Thinking,
					RequestedAt: detail.Timestamp,
					DurationMs:  detail.LatencyMs,
					StatusCode:  detail.StatusCode,
				}
				exists := false
				if exists, err = usageEventExistsTx(ctx, tx, evt.ID); err != nil {
					return imported, err
				}
				if exists {
					continue
				}
				inserted := false
				if inserted, err = insertUsageEventTx(ctx, tx, record, evt); err != nil {
					return imported, err
				}
				if inserted {
					if err = upsertRollupsTx(ctx, tx, record, evt); err != nil {
						return imported, err
					}
					imported++
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return imported, fmt.Errorf("commit usage import transaction: %w", err)
	}
	return imported, nil
}

func usageEventExistsTx(ctx context.Context, tx *sql.Tx, eventID uint64) (bool, error) {
	var existing int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM usage_events WHERE event_id = ?`, int64(eventID)).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("check usage event existence: %w", err)
}

func insertUsageEventTx(ctx context.Context, tx *sql.Tx, record coreusage.Record, evt UsageEvent) (bool, error) {
	requestJSON, err := jsonString(record.Request)
	if err != nil {
		return false, err
	}
	modelInfoJSON, err := jsonString(record.ModelInfo)
	if err != nil {
		return false, err
	}
	thinkingJSON, err := jsonString(evt.Thinking)
	if err != nil {
		return false, err
	}
	apiKey := record.APIKey
	if apiKey == "" {
		apiKey = evt.APIKey
	}
	requestedAt := evt.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = record.RequestedAt
	}
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = strings.TrimSpace(evt.Model)
	}
	if model == "" {
		model = "unknown"
	}
	statusCode := evt.StatusCode
	if statusCode == 0 {
		statusCode = record.StatusCode
	}
	if statusCode == 0 && evt.Failed {
		statusCode = 500
	} else if statusCode == 0 {
		statusCode = 200
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO usage_events (
		event_id, requested_at_unix_ms, requested_at, api_key_hash, api_key_mask,
		provider, model, source, auth_id_hash, auth_index, auth_type, request_id,
		status_code, failed, latency_ms, input_tokens, output_tokens, reasoning_tokens,
		cached_tokens, total_tokens, token_source, request_json, model_info_json,
		thinking_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(evt.ID),
		requestedAt.UnixMilli(),
		requestedAt.UTC().Format(time.RFC3339Nano),
		stableUsageHash(apiKey),
		maskUsageSecret(apiKey),
		record.Provider,
		model,
		firstNonEmpty(record.Source, evt.Source),
		stableUsageHash(record.AuthID),
		firstNonEmpty(record.AuthIndex, evt.AuthIndex),
		record.AuthType,
		firstNonEmpty(record.RequestID, evt.RequestID),
		statusCode,
		boolInt(evt.Failed),
		evt.DurationMs,
		evt.Tokens.Input,
		evt.Tokens.Output,
		evt.Tokens.Reasoning,
		evt.Tokens.Cached,
		evt.Tokens.Total,
		"reported_or_estimated",
		requestJSON,
		modelInfoJSON,
		thinkingJSON,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, fmt.Errorf("insert usage event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check usage event insert result: %w", err)
	}
	return affected > 0, nil
}

func upsertRollupsTx(ctx context.Context, tx *sql.Tx, record coreusage.Record, evt UsageEvent) error {
	apiKey := record.APIKey
	if apiKey == "" {
		apiKey = evt.APIKey
	}
	model := strings.TrimSpace(firstNonEmpty(record.Model, evt.Model))
	if model == "" {
		model = "unknown"
	}
	source := strings.TrimSpace(firstNonEmpty(record.Source, evt.Source))
	authIndex := strings.TrimSpace(firstNonEmpty(record.AuthIndex, evt.AuthIndex))
	for _, bucket := range usageRollupBuckets(evt.RequestedAt) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_rollups (
			period, bucket_start_unix_ms, api_key_hash, api_key_mask, model, source,
			auth_index, requests, failures, input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, total_tokens, latency_sum_ms, latency_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(period, bucket_start_unix_ms, api_key_hash, model, source, auth_index)
		DO UPDATE SET
			requests = requests + 1,
			failures = failures + excluded.failures,
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			reasoning_tokens = reasoning_tokens + excluded.reasoning_tokens,
			cached_tokens = cached_tokens + excluded.cached_tokens,
			total_tokens = total_tokens + excluded.total_tokens,
			latency_sum_ms = latency_sum_ms + excluded.latency_sum_ms,
			latency_count = latency_count + excluded.latency_count`,
			bucket.period,
			bucket.start.UnixMilli(),
			stableUsageHash(apiKey),
			maskUsageSecret(apiKey),
			model,
			source,
			authIndex,
			boolInt(evt.Failed),
			evt.Tokens.Input,
			evt.Tokens.Output,
			evt.Tokens.Reasoning,
			evt.Tokens.Cached,
			evt.Tokens.Total,
			positiveLatency(evt.DurationMs),
			boolInt(evt.DurationMs > 0),
		); err != nil {
			return fmt.Errorf("upsert usage %s rollup: %w", bucket.period, err)
		}
	}
	return nil
}

type usageRollupBucket struct {
	period string
	start  time.Time
}

func usageRollupBuckets(t time.Time) []usageRollupBucket {
	if t.IsZero() {
		t = time.Now()
	}
	utc := t.UTC()
	return []usageRollupBucket{
		{period: "minute", start: utc.Truncate(time.Minute)},
		{period: "hour", start: utc.Truncate(time.Hour)},
		{period: "day", start: time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)},
	}
}

// QueryRollupTotals returns aggregate totals from persisted rollups.
func (s *SQLiteUsageEventStore) QueryRollupTotals(ctx context.Context, period string, start, end time.Time) (requests, tokens, failures int64, err error) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return 0, 0, 0, fmt.Errorf("usage sqlite store is closed")
	}
	row := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests), 0), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(failures), 0)
		FROM usage_rollups
		WHERE period = ? AND bucket_start_unix_ms >= ? AND bucket_start_unix_ms < ?`,
		period, start.UTC().UnixMilli(), end.UTC().UnixMilli())
	if err := row.Scan(&requests, &tokens, &failures); err != nil {
		return 0, 0, 0, fmt.Errorf("query usage rollup totals: %w", err)
	}
	return requests, tokens, failures, nil
}

func (s *SQLiteUsageEventStore) Path() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

func (s *SQLiteUsageEventStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func jsonString(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal usage metadata: %w", err)
	}
	if string(data) == "null" {
		return "", nil
	}
	return string(data), nil
}

func stableUsageHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func maskUsageSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func positiveLatency(v int64) int64 {
	if v > 0 {
		return v
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func toCoreThinking(thinking *Thinking) *coreusage.Thinking {
	if thinking == nil {
		return nil
	}
	return &coreusage.Thinking{
		Intensity: thinking.Intensity,
		Mode:      thinking.Mode,
		Level:     thinking.Level,
		Budget:    thinking.Budget,
	}
}
