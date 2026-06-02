package management

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	textOpsAnalyticsDefaultSchema         = "textops"
	textOpsAnalyticsDefaultRollupInterval = 10 * time.Minute
	textOpsAnalyticsDefaultConnLifetime   = 30 * time.Minute
	textOpsAnalyticsDefaultMaxOpenConns   = 30
	textOpsAnalyticsDefaultMaxIdleConns   = 10
)

type textOpsAnalyticsStore interface {
	Query(
		ctx context.Context,
		intent string,
		filters textOpsFilters,
		groupBy []string,
		window textOpsResolvedWindow,
		now time.Time,
	) (textOpsDataResult, textOpsExecutionSnapshot, error)
	RefreshDailyRollup(ctx context.Context, start, end time.Time) error
	Close() error
}

type textOpsPGAnalyticsStore struct {
	db             *sql.DB
	schema         string
	rollupInterval time.Duration

	mu           sync.Mutex
	lastRollupAt time.Time
}

type textOpsAnalyticsUsagePlugin struct {
	store *textOpsPGAnalyticsStore
}

var textOpsAnalyticsBootstrap struct {
	once sync.Once
	err  error
	impl textOpsAnalyticsStore
}

func initTextOpsAnalyticsStoreFromEnv() (textOpsAnalyticsStore, error) {
	textOpsAnalyticsBootstrap.once.Do(func() {
		disabled := parseTextOpsBoolEnv("TEXT_OPS_ANALYTICS_DISABLED")
		if disabled {
			return
		}
		dsn := strings.TrimSpace(firstTextOpsEnv("TEXT_OPS_ANALYTICS_PG_DSN", "TEXTOPS_ANALYTICS_PG_DSN", "PGSTORE_DSN"))
		if dsn == "" {
			return
		}

		schema := strings.TrimSpace(firstTextOpsEnv("TEXT_OPS_ANALYTICS_SCHEMA", "TEXTOPS_ANALYTICS_SCHEMA"))
		if schema == "" {
			schema = textOpsAnalyticsDefaultSchema
		}
		rollupInterval := parseTextOpsDurationEnv("TEXT_OPS_ANALYTICS_ROLLUP_INTERVAL", textOpsAnalyticsDefaultRollupInterval)

		db, errOpen := sql.Open("pgx", dsn)
		if errOpen != nil {
			textOpsAnalyticsBootstrap.err = fmt.Errorf("text_ops analytics: open postgres failed: %w", errOpen)
			return
		}
		db.SetMaxOpenConns(parseTextOpsIntEnv("TEXT_OPS_ANALYTICS_MAX_OPEN_CONNS", textOpsAnalyticsDefaultMaxOpenConns))
		db.SetMaxIdleConns(parseTextOpsIntEnv("TEXT_OPS_ANALYTICS_MAX_IDLE_CONNS", textOpsAnalyticsDefaultMaxIdleConns))
		db.SetConnMaxLifetime(parseTextOpsDurationEnv("TEXT_OPS_ANALYTICS_CONN_MAX_LIFETIME", textOpsAnalyticsDefaultConnLifetime))

		ctxPing, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelPing()
		if errPing := db.PingContext(ctxPing); errPing != nil {
			_ = db.Close()
			textOpsAnalyticsBootstrap.err = fmt.Errorf("text_ops analytics: ping postgres failed: %w", errPing)
			return
		}

		store := &textOpsPGAnalyticsStore{
			db:             db,
			schema:         schema,
			rollupInterval: rollupInterval,
		}
		ctxInit, cancelInit := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelInit()
		if errSchema := store.ensureSchema(ctxInit); errSchema != nil {
			_ = db.Close()
			textOpsAnalyticsBootstrap.err = errSchema
			return
		}

		coreusage.RegisterPlugin(&textOpsAnalyticsUsagePlugin{store: store})
		textOpsAnalyticsBootstrap.impl = store
		log.Infof(
			"text_ops analytics store enabled: schema=%s rollup_interval=%s",
			schema,
			rollupInterval.String(),
		)
	})
	return textOpsAnalyticsBootstrap.impl, textOpsAnalyticsBootstrap.err
}

func (p *textOpsAnalyticsUsagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctxWrite, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := p.store.insertUsageRecord(ctxWrite, record); err != nil {
		log.WithError(err).Debug("text_ops analytics usage ingest failed")
	}
}

func (s *textOpsPGAnalyticsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *textOpsPGAnalyticsStore) ensureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("text_ops analytics: store not initialized")
	}
	if strings.TrimSpace(s.schema) == "" {
		s.schema = textOpsAnalyticsDefaultSchema
	}
	createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteTextOpsIdentifier(s.schema))
	if _, err := s.db.ExecContext(ctx, createSchema); err != nil {
		return fmt.Errorf("text_ops analytics: create schema failed: %w", err)
	}

	usersTable := s.tableName("users")
	tokenLogsTable := s.tableName("token_logs")
	finCyclesTable := s.tableName("financial_cycles")
	dailyStatsTable := s.tableName("daily_model_stats")

	usersDDL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			user_id BIGINT PRIMARY KEY,
			org_id BIGINT NOT NULL DEFAULT 0,
			department TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'customer',
			name TEXT NOT NULL DEFAULT '',
			balance_credits NUMERIC(20,6) NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, usersTable)
	if _, err := s.db.ExecContext(ctx, usersDDL); err != nil {
		return fmt.Errorf("text_ops analytics: create users table failed: %w", err)
	}

	tokenLogsDDL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			request_id TEXT PRIMARY KEY,
			user_id BIGINT NOT NULL DEFAULT 0,
			model_name TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			prompt_tokens BIGINT NOT NULL DEFAULT 0,
			completion_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			cost_credits NUMERIC(20,6) NOT NULL DEFAULT 0,
			cache_status TEXT NOT NULL DEFAULT 'NONE',
			status_code INTEGER NOT NULL DEFAULT 200,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, tokenLogsTable)
	if _, err := s.db.ExecContext(ctx, tokenLogsDDL); err != nil {
		return fmt.Errorf("text_ops analytics: create token_logs table failed: %w", err)
	}

	dailyStatsDDL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			stat_date DATE NOT NULL,
			model_name TEXT NOT NULL,
			user_id BIGINT NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			cache_status TEXT NOT NULL DEFAULT 'NONE',
			total_requests BIGINT NOT NULL DEFAULT 0,
			success_requests BIGINT NOT NULL DEFAULT 0,
			failed_requests BIGINT NOT NULL DEFAULT 0,
			prompt_tokens BIGINT NOT NULL DEFAULT 0,
			completion_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			total_cost_credits NUMERIC(20,6) NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (stat_date, model_name, user_id, source, cache_status)
		)
	`, dailyStatsTable)
	if _, err := s.db.ExecContext(ctx, dailyStatsDDL); err != nil {
		return fmt.Errorf("text_ops analytics: create daily_model_stats table failed: %w", err)
	}

	finCyclesDDL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			cycle_id TEXT PRIMARY KEY,
			cycle_name TEXT NOT NULL,
			start_time TIMESTAMPTZ NOT NULL,
			end_time TIMESTAMPTZ NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, finCyclesTable)
	if _, err := s.db.ExecContext(ctx, finCyclesDDL); err != nil {
		return fmt.Errorf("text_ops analytics: create financial_cycles table failed: %w", err)
	}

	indexStatements := []string{
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (created_at)", quoteTextOpsIdentifier("idx_token_logs_created_at"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (model_name, created_at)", quoteTextOpsIdentifier("idx_token_logs_model_time"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (user_id, created_at)", quoteTextOpsIdentifier("idx_token_logs_user_time"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (cache_status, created_at)", quoteTextOpsIdentifier("idx_token_logs_cache_time"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (source, created_at)", quoteTextOpsIdentifier("idx_token_logs_source_time"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (status_code, created_at)", quoteTextOpsIdentifier("idx_token_logs_status_time"), tokenLogsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (model_name, stat_date)", quoteTextOpsIdentifier("idx_daily_model_stats_model_date"), dailyStatsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (user_id, stat_date)", quoteTextOpsIdentifier("idx_daily_model_stats_user_date"), dailyStatsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (cache_status, stat_date)", quoteTextOpsIdentifier("idx_daily_model_stats_cache_date"), dailyStatsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (source, stat_date)", quoteTextOpsIdentifier("idx_daily_model_stats_source_date"), dailyStatsTable),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (start_time, end_time)", quoteTextOpsIdentifier("idx_financial_cycles_window"), finCyclesTable),
	}
	for _, statement := range indexStatements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("text_ops analytics: create index failed: %w", err)
		}
	}
	return nil
}

func (s *textOpsPGAnalyticsStore) insertUsageRecord(ctx context.Context, record coreusage.Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	ts := record.RequestedAt.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	source := strings.TrimSpace(record.Source)
	if source == "" {
		source = strings.TrimSpace(record.Provider)
	}
	userID := parseTextOpsInt64(strings.TrimSpace(record.AuthIndex))
	if userID <= 0 {
		userID = parseTextOpsInt64(strings.TrimSpace(record.AuthID))
	}

	promptTokens := positiveToken(record.Detail.InputTokens)
	completionTokens := positiveToken(record.Detail.OutputTokens) + positiveToken(record.Detail.ReasoningTokens)
	cachedTokens := positiveToken(record.Detail.CachedTokens)
	totalTokens := positiveToken(record.Detail.TotalTokens)
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens + cachedTokens
	}
	cacheStatus := inferTextOpsCacheStatus(promptTokens, cachedTokens)
	statusCode := 200
	if record.Failed {
		statusCode = 500
	}
	requestID := buildTextOpsRecordID(record, ts)

	query := fmt.Sprintf(`
		INSERT INTO %s (
			request_id, user_id, model_name, source,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			cost_credits, cache_status, status_code, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW())
		ON CONFLICT (request_id) DO NOTHING
	`, s.tableName("token_logs"))
	_, err := s.db.ExecContext(
		ctx,
		query,
		requestID,
		userID,
		modelName,
		source,
		promptTokens,
		completionTokens,
		totalTokens,
		cachedTokens,
		0.0,
		cacheStatus,
		statusCode,
		ts,
	)
	if err != nil {
		return fmt.Errorf("text_ops analytics: insert token log failed: %w", err)
	}
	return nil
}

func (s *textOpsPGAnalyticsStore) RefreshDailyRollup(ctx context.Context, start, end time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("text_ops analytics: store not initialized")
	}
	startUTC := start.UTC()
	endUTC := end.UTC()
	if endUTC.Before(startUTC) {
		startUTC, endUTC = endUTC, startUTC
	}
	startDay := time.Date(startUTC.Year(), startUTC.Month(), startUTC.Day(), 0, 0, 0, 0, time.UTC)
	endDay := time.Date(endUTC.Year(), endUTC.Month(), endUTC.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)

	s.mu.Lock()
	lastRollup := s.lastRollupAt
	allowByInterval := lastRollup.IsZero() || time.Since(lastRollup) >= s.rollupInterval
	if !allowByInterval {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("text_ops analytics: begin rollup tx failed: %w", errBegin)
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			return
		}
		_ = tx.Rollback()
	}()

	deleteSQL := fmt.Sprintf(
		"DELETE FROM %s WHERE stat_date >= $1::date AND stat_date < $2::date",
		s.tableName("daily_model_stats"),
	)
	if _, err := tx.ExecContext(ctx, deleteSQL, startDay, endDay); err != nil {
		rolledBack = true
		return fmt.Errorf("text_ops analytics: clear daily rollup window failed: %w", err)
	}

	insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			stat_date, model_name, user_id, source, cache_status,
			total_requests, success_requests, failed_requests,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens, total_cost_credits, updated_at
		)
		SELECT
			DATE(created_at) AS stat_date,
			model_name,
			user_id,
			source,
			cache_status,
			COUNT(*) AS total_requests,
			COUNT(*) FILTER (WHERE status_code < 400) AS success_requests,
			COUNT(*) FILTER (WHERE status_code >= 400) AS failed_requests,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
			COALESCE(SUM(cost_credits), 0) AS total_cost_credits,
			NOW()
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY DATE(created_at), model_name, user_id, source, cache_status
	`, s.tableName("daily_model_stats"), s.tableName("token_logs"))
	if _, err := tx.ExecContext(ctx, insertSQL, startDay, endDay); err != nil {
		rolledBack = true
		return fmt.Errorf("text_ops analytics: insert daily rollup failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		rolledBack = true
		return fmt.Errorf("text_ops analytics: commit daily rollup failed: %w", err)
	}
	s.mu.Lock()
	s.lastRollupAt = time.Now().UTC()
	s.mu.Unlock()
	return nil
}

func (s *textOpsPGAnalyticsStore) Query(
	ctx context.Context,
	intent string,
	filters textOpsFilters,
	groupBy []string,
	window textOpsResolvedWindow,
	now time.Time,
) (textOpsDataResult, textOpsExecutionSnapshot, error) {
	if s == nil || s.db == nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, fmt.Errorf("text_ops analytics: store not initialized")
	}
	if err := s.RefreshDailyRollup(ctx, window.Start, window.End); err != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, err
	}

	whereClause, args := s.buildDailyStatsWhereClause(filters, window)
	overview, errOverview := s.queryOverview(ctx, whereClause, args)
	if errOverview != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errOverview
	}
	modelRows, errModels := s.queryModelRows(ctx, whereClause, args)
	if errModels != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errModels
	}
	tokensByDay, errTokens := s.queryTokensByDay(ctx, whereClause, args, false)
	if errTokens != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errTokens
	}
	inputByDay, errInput := s.queryTokensByDayColumn(ctx, whereClause, args, "prompt_tokens")
	if errInput != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errInput
	}
	cachedByDay, errCached := s.queryTokensByDay(ctx, whereClause, args, true)
	if errCached != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errCached
	}
	dailyMetrics, errDaily := s.queryDailyMetrics(ctx, whereClause, args)
	if errDaily != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errDaily
	}
	dailyModelSeries, errDailyModel := s.queryDailyModelSeries(ctx, whereClause, args)
	if errDailyModel != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errDailyModel
	}
	network, errNetwork := s.queryNetworkSummary(ctx, filters, window)
	if errNetwork != nil {
		return textOpsDataResult{}, textOpsExecutionSnapshot{}, errNetwork
	}

	execSnapshot := textOpsExecutionSnapshot{
		Overview:         overview,
		ModelRows:        modelRows,
		TokensByDay:      tokensByDay,
		InputByDay:       inputByDay,
		CachedByDay:      cachedByDay,
		DailyMetrics:     dailyMetrics,
		DailyModelSeries: dailyModelSeries,
		Network:          network,
		Warnings:         []string{"data source: postgres analytics store"},
	}

	priceWarnings := make([]string, 0)
	costsByModel, totalCost := calculateTextOpsCosts(modelRows, &priceWarnings)
	execSnapshot.Warnings = append(execSnapshot.Warnings, priceWarnings...)

	summary := map[string]any{
		"start_time":       window.Start.Format(time.RFC3339),
		"end_time":         window.End.Format(time.RFC3339),
		"total_requests":   overview.TotalRequests,
		"success_requests": overview.SuccessRequests,
		"failed_requests":  overview.FailedRequests,
		"success_rate":     overview.SuccessRate,
		"total_tokens":     overview.TotalTokens,
		"input_tokens":     overview.InputTokens,
		"output_tokens":    overview.OutputTokens,
		"reasoning_tokens": overview.ReasoningTokens,
		"cached_tokens":    overview.CachedTokens,
		"cache_hit_rate":   overview.CacheHitRate,
		"total_cost":       roundTextOpsCost(totalCost),
		"group_by":         groupBy,
		"requested_intent": intent,
		"generated_at":     now.Format(time.RFC3339),
		"financial_cycle":  "",
		"financial_status": "",
		"data_source":      "postgres_analytics",
	}
	if filters.ModelName != nil {
		summary["model_filter"] = strings.TrimSpace(*filters.ModelName)
	}
	if filters.Source != nil {
		summary["source_filter"] = strings.TrimSpace(*filters.Source)
	}
	if filters.FinancialCycleID != nil {
		summary["financial_cycle"] = *filters.FinancialCycleID
		summary["financial_status"] = s.resolveFinancialCycleStatus(ctx, *filters.FinancialCycleID, window, now)
	}

	rows := make([]map[string]any, 0, len(modelRows))
	for _, item := range modelRows {
		switch intent {
		case textOpsIntentCacheMetrics:
			rows = append(rows, map[string]any{
				"model_name":      item.Model,
				"total_tokens":    item.TotalTokens,
				"total_requests":  item.TotalRequests,
				"cached_tokens":   item.CachedTokens,
				"input_tokens":    item.InputTokens,
				"cache_hit_rate":  item.CacheHitRate,
				"success_rate":    item.SuccessRate,
				"failed_requests": item.FailedRequests,
				"total_cost":      roundTextOpsCost(costsByModel[item.Model]),
			})
		case textOpsIntentFinancialStatus:
			rows = append(rows, map[string]any{
				"model_name":      item.Model,
				"total_tokens":    item.TotalTokens,
				"total_requests":  item.TotalRequests,
				"success_rate":    item.SuccessRate,
				"cached_tokens":   item.CachedTokens,
				"cache_hit_rate":  item.CacheHitRate,
				"failed_requests": item.FailedRequests,
				"total_cost":      roundTextOpsCost(costsByModel[item.Model]),
			})
		default:
			rows = append(rows, map[string]any{
				"model_name":       item.Model,
				"total_tokens":     item.TotalTokens,
				"input_tokens":     item.InputTokens,
				"output_tokens":    item.OutputTokens,
				"reasoning_tokens": item.ReasoningTokens,
				"cached_tokens":    item.CachedTokens,
				"cache_hit_rate":   item.CacheHitRate,
				"total_requests":   item.TotalRequests,
				"success_rate":     item.SuccessRate,
				"total_cost":       roundTextOpsCost(costsByModel[item.Model]),
			})
		}
	}

	raw := map[string]any{
		"tokens_by_day":        tokensByDay,
		"input_tokens_by_day":  inputByDay,
		"cached_tokens_by_day": cachedByDay,
		"daily_metrics":        dailyMetrics,
		"daily_model_series":   dailyModelSeries,
		"network":              network,
		"registrations":        aiOpsRegistrationSummary{},
	}
	return textOpsDataResult{
		Intent:  intent,
		Summary: summary,
		Rows:    rows,
		Raw:     raw,
	}, execSnapshot, nil
}

func (s *textOpsPGAnalyticsStore) queryOverview(ctx context.Context, where string, args []any) (aiOpsOverview, error) {
	sqlText := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(total_requests), 0),
			COALESCE(SUM(success_requests), 0),
			COALESCE(SUM(failed_requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(cached_tokens), 0)
		FROM %s
		%s
	`, s.tableName("daily_model_stats"), where)

	var totalRequests int64
	var successRequests int64
	var failedRequests int64
	var totalTokens int64
	var promptTokens int64
	var completionTokens int64
	var cachedTokens int64
	if err := s.db.QueryRowContext(ctx, sqlText, args...).Scan(
		&totalRequests,
		&successRequests,
		&failedRequests,
		&totalTokens,
		&promptTokens,
		&completionTokens,
		&cachedTokens,
	); err != nil {
		return aiOpsOverview{}, fmt.Errorf("text_ops analytics: query overview failed: %w", err)
	}
	return aiOpsOverview{
		TotalRequests:   totalRequests,
		SuccessRequests: successRequests,
		FailedRequests:  failedRequests,
		SuccessRate:     roundAIOpsRate(ratioPercent(successRequests, totalRequests)),
		TotalTokens:     totalTokens,
		InputTokens:     promptTokens,
		OutputTokens:    completionTokens,
		ReasoningTokens: 0,
		CachedTokens:    cachedTokens,
		CacheHitRate:    roundAIOpsRate(ratioPercent(cachedTokens, promptTokens+cachedTokens)),
	}, nil
}

func (s *textOpsPGAnalyticsStore) queryModelRows(ctx context.Context, where string, args []any) ([]aiOpsModelMetric, error) {
	sqlText := fmt.Sprintf(`
		SELECT
			model_name,
			COALESCE(SUM(total_requests), 0) AS total_requests,
			COALESCE(SUM(success_requests), 0) AS success_requests,
			COALESCE(SUM(failed_requests), 0) AS failed_requests,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens
		FROM %s
		%s
		GROUP BY model_name
		ORDER BY total_tokens DESC, model_name ASC
		LIMIT %d
	`, s.tableName("daily_model_stats"), where, aiOpsDefaultTopModelLimit)

	rows, errQuery := s.db.QueryContext(ctx, sqlText, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("text_ops analytics: query model rows failed: %w", errQuery)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]aiOpsModelMetric, 0)
	for rows.Next() {
		var item aiOpsModelMetric
		if err := rows.Scan(
			&item.Model,
			&item.TotalRequests,
			&item.SuccessRequests,
			&item.FailedRequests,
			&item.TotalTokens,
			&item.InputTokens,
			&item.OutputTokens,
			&item.CachedTokens,
		); err != nil {
			return nil, fmt.Errorf("text_ops analytics: scan model row failed: %w", err)
		}
		item.SuccessRate = roundAIOpsRate(ratioPercent(item.SuccessRequests, item.TotalRequests))
		item.CacheHitRate = roundAIOpsRate(ratioPercent(item.CachedTokens, item.InputTokens+item.CachedTokens))
		item.ReasoningTokens = 0
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("text_ops analytics: iterate model rows failed: %w", err)
	}
	return result, nil
}

func (s *textOpsPGAnalyticsStore) queryTokensByDay(ctx context.Context, where string, args []any, cached bool) (map[string]int64, error) {
	column := "total_tokens"
	if cached {
		column = "cached_tokens"
	}
	return s.queryTokensByDayColumn(ctx, where, args, column)
}

func (s *textOpsPGAnalyticsStore) queryTokensByDayColumn(ctx context.Context, where string, args []any, column string) (map[string]int64, error) {
	switch column {
	case "total_tokens", "prompt_tokens", "completion_tokens", "cached_tokens":
	default:
		return nil, fmt.Errorf("text_ops analytics: unsupported daily token column %q", column)
	}
	sqlText := fmt.Sprintf(`
		SELECT stat_date::text, COALESCE(SUM(%s), 0)
		FROM %s
		%s
		GROUP BY stat_date
		ORDER BY stat_date ASC
	`, column, s.tableName("daily_model_stats"), where)

	rows, errQuery := s.db.QueryContext(ctx, sqlText, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("text_ops analytics: query tokens by day failed: %w", errQuery)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make(map[string]int64)
	for rows.Next() {
		var day string
		var value int64
		if err := rows.Scan(&day, &value); err != nil {
			return nil, fmt.Errorf("text_ops analytics: scan tokens by day failed: %w", err)
		}
		result[day] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("text_ops analytics: iterate tokens by day failed: %w", err)
	}
	return result, nil
}

func (s *textOpsPGAnalyticsStore) queryDailyMetrics(ctx context.Context, where string, args []any) ([]aiOpsDailyMetric, error) {
	sqlText := fmt.Sprintf(`
		SELECT
			stat_date::text,
			COALESCE(SUM(total_requests), 0),
			COALESCE(SUM(success_requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(cached_tokens), 0)
		FROM %s
		%s
		GROUP BY stat_date
		ORDER BY stat_date ASC
	`, s.tableName("daily_model_stats"), where)

	rows, errQuery := s.db.QueryContext(ctx, sqlText, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("text_ops analytics: query daily metrics failed: %w", errQuery)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]aiOpsDailyMetric, 0)
	for rows.Next() {
		var item aiOpsDailyMetric
		var successRequests int64
		if err := rows.Scan(
			&item.Date,
			&item.TotalRequests,
			&successRequests,
			&item.TotalTokens,
			&item.InputTokens,
			&item.CachedTokens,
		); err != nil {
			return nil, fmt.Errorf("text_ops analytics: scan daily metrics failed: %w", err)
		}
		item.SuccessRate = roundAIOpsRate(ratioPercent(successRequests, item.TotalRequests))
		item.CacheHitRate = roundAIOpsRate(ratioPercent(item.CachedTokens, item.InputTokens+item.CachedTokens))
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("text_ops analytics: iterate daily metrics failed: %w", err)
	}
	return result, nil
}

func (s *textOpsPGAnalyticsStore) queryDailyModelSeries(ctx context.Context, where string, args []any) ([]aiOpsDailyModelMetric, error) {
	sqlText := fmt.Sprintf(`
		SELECT
			stat_date::text,
			model_name,
			COALESCE(SUM(total_requests), 0),
			COALESCE(SUM(success_requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(cached_tokens), 0)
		FROM %s
		%s
		GROUP BY stat_date, model_name
		ORDER BY stat_date ASC, model_name ASC
	`, s.tableName("daily_model_stats"), where)

	rows, errQuery := s.db.QueryContext(ctx, sqlText, args...)
	if errQuery != nil {
		return nil, fmt.Errorf("text_ops analytics: query daily model series failed: %w", errQuery)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]aiOpsDailyModelMetric, 0)
	for rows.Next() {
		var item aiOpsDailyModelMetric
		var successRequests int64
		if err := rows.Scan(
			&item.Date,
			&item.Model,
			&item.TotalRequests,
			&successRequests,
			&item.TotalTokens,
			&item.InputTokens,
			&item.CachedTokens,
		); err != nil {
			return nil, fmt.Errorf("text_ops analytics: scan daily model series failed: %w", err)
		}
		item.SuccessRate = roundAIOpsRate(ratioPercent(successRequests, item.TotalRequests))
		item.CacheHitRate = roundAIOpsRate(ratioPercent(item.CachedTokens, item.InputTokens+item.CachedTokens))
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("text_ops analytics: iterate daily model series failed: %w", err)
	}
	return result, nil
}

func (s *textOpsPGAnalyticsStore) queryNetworkSummary(
	ctx context.Context,
	filters textOpsFilters,
	window textOpsResolvedWindow,
) (aiOpsNetworkSummary, error) {
	summary := aiOpsNetworkSummary{
		FailedBySource: make([]aiOpsNamedCount, 0),
		FailedByModel:  make([]aiOpsNamedCount, 0),
		FailureSamples: make([]aiOpsFailureSample, 0),
	}
	whereClause, args := s.buildTokenLogsWhereClause(filters, window)
	baseTable := s.tableName("token_logs")

	totalSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s %s", baseTable, whereClause)
	if err := s.db.QueryRowContext(ctx, totalSQL, args...).Scan(&summary.WindowRequestCount); err != nil {
		return summary, fmt.Errorf("text_ops analytics: query network total failed: %w", err)
	}
	var failedCount int64
	failedSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s %s AND status_code >= 400", baseTable, whereClause)
	if err := s.db.QueryRowContext(ctx, failedSQL, args...).Scan(&failedCount); err != nil {
		return summary, fmt.Errorf("text_ops analytics: query network failed count failed: %w", err)
	}
	summary.SuccessRate = roundAIOpsRate(ratioPercent(summary.WindowRequestCount-failedCount, summary.WindowRequestCount))

	failedBySourceSQL := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(TRIM(source), ''), 'unknown') AS source_key, COUNT(*)
		FROM %s
		%s AND status_code >= 400
		GROUP BY source_key
		ORDER BY COUNT(*) DESC, source_key ASC
		LIMIT %d
	`, baseTable, whereClause, aiOpsDefaultTopFailureLimit)
	sourceRows, errSource := s.db.QueryContext(ctx, failedBySourceSQL, args...)
	if errSource != nil {
		return summary, fmt.Errorf("text_ops analytics: query failed_by_source failed: %w", errSource)
	}
	for sourceRows.Next() {
		var row aiOpsNamedCount
		if err := sourceRows.Scan(&row.Name, &row.Count); err != nil {
			_ = sourceRows.Close()
			return summary, fmt.Errorf("text_ops analytics: scan failed_by_source failed: %w", err)
		}
		summary.FailedBySource = append(summary.FailedBySource, row)
	}
	if err := sourceRows.Err(); err != nil {
		_ = sourceRows.Close()
		return summary, fmt.Errorf("text_ops analytics: iterate failed_by_source failed: %w", err)
	}
	_ = sourceRows.Close()

	failedByModelSQL := fmt.Sprintf(`
		SELECT model_name, COUNT(*)
		FROM %s
		%s AND status_code >= 400
		GROUP BY model_name
		ORDER BY COUNT(*) DESC, model_name ASC
		LIMIT %d
	`, baseTable, whereClause, aiOpsDefaultTopFailureLimit)
	modelRows, errModel := s.db.QueryContext(ctx, failedByModelSQL, args...)
	if errModel != nil {
		return summary, fmt.Errorf("text_ops analytics: query failed_by_model failed: %w", errModel)
	}
	for modelRows.Next() {
		var row aiOpsNamedCount
		if err := modelRows.Scan(&row.Name, &row.Count); err != nil {
			_ = modelRows.Close()
			return summary, fmt.Errorf("text_ops analytics: scan failed_by_model failed: %w", err)
		}
		summary.FailedByModel = append(summary.FailedByModel, row)
	}
	if err := modelRows.Err(); err != nil {
		_ = modelRows.Close()
		return summary, fmt.Errorf("text_ops analytics: iterate failed_by_model failed: %w", err)
	}
	_ = modelRows.Close()

	failureSampleSQL := fmt.Sprintf(`
		SELECT created_at, model_name, source, user_id
		FROM %s
		%s AND status_code >= 400
		ORDER BY created_at DESC
		LIMIT %d
	`, baseTable, whereClause, aiOpsDefaultFailureSampleLimit)
	sampleRows, errSamples := s.db.QueryContext(ctx, failureSampleSQL, args...)
	if errSamples != nil {
		return summary, fmt.Errorf("text_ops analytics: query failure samples failed: %w", errSamples)
	}
	for sampleRows.Next() {
		var sample aiOpsFailureSample
		var source string
		var userID int64
		if err := sampleRows.Scan(&sample.Timestamp, &sample.Model, &source, &userID); err != nil {
			_ = sampleRows.Close()
			return summary, fmt.Errorf("text_ops analytics: scan failure sample failed: %w", err)
		}
		sample.Timestamp = sample.Timestamp.UTC()
		sample.Source = strings.TrimSpace(source)
		sample.AuthIndex = strconv.FormatInt(userID, 10)
		summary.FailureSamples = append(summary.FailureSamples, sample)
	}
	if err := sampleRows.Err(); err != nil {
		_ = sampleRows.Close()
		return summary, fmt.Errorf("text_ops analytics: iterate failure samples failed: %w", err)
	}
	_ = sampleRows.Close()

	return summary, nil
}

func (s *textOpsPGAnalyticsStore) resolveFinancialCycleStatus(
	ctx context.Context,
	cycleID string,
	window textOpsResolvedWindow,
	now time.Time,
) string {
	id := strings.TrimSpace(strings.ToUpper(cycleID))
	if id == "" {
		return inferFinancialCycleStatus(cycleID, window, now)
	}
	sqlText := fmt.Sprintf("SELECT status FROM %s WHERE UPPER(cycle_id) = UPPER($1) LIMIT 1", s.tableName("financial_cycles"))
	var status sql.NullString
	if err := s.db.QueryRowContext(ctx, sqlText, id).Scan(&status); err != nil {
		return inferFinancialCycleStatus(cycleID, window, now)
	}
	trimmed := strings.TrimSpace(status.String)
	if !status.Valid || trimmed == "" {
		return inferFinancialCycleStatus(cycleID, window, now)
	}
	return strings.ToLower(trimmed)
}

func (s *textOpsPGAnalyticsStore) buildDailyStatsWhereClause(filters textOpsFilters, window textOpsResolvedWindow) (string, []any) {
	clauses := []string{
		"stat_date >= $1::date",
		"stat_date <= $2::date",
	}
	args := []any{window.Start.UTC(), window.End.UTC()}
	index := 3
	if filters.ModelName != nil && strings.TrimSpace(*filters.ModelName) != "" {
		clauses = append(clauses, fmt.Sprintf("model_name ILIKE $%d", index))
		args = append(args, "%"+strings.TrimSpace(*filters.ModelName)+"%")
		index++
	}
	if filters.Source != nil && strings.TrimSpace(*filters.Source) != "" {
		clauses = append(clauses, fmt.Sprintf("source ILIKE $%d", index))
		args = append(args, "%"+strings.TrimSpace(*filters.Source)+"%")
		index++
	}
	if filters.UserID != nil {
		clauses = append(clauses, fmt.Sprintf("user_id = $%d", index))
		args = append(args, *filters.UserID)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (s *textOpsPGAnalyticsStore) buildTokenLogsWhereClause(filters textOpsFilters, window textOpsResolvedWindow) (string, []any) {
	clauses := []string{
		"created_at >= $1",
		"created_at <= $2",
	}
	args := []any{window.Start.UTC(), window.End.UTC()}
	index := 3
	if filters.ModelName != nil && strings.TrimSpace(*filters.ModelName) != "" {
		clauses = append(clauses, fmt.Sprintf("model_name ILIKE $%d", index))
		args = append(args, "%"+strings.TrimSpace(*filters.ModelName)+"%")
		index++
	}
	if filters.Source != nil && strings.TrimSpace(*filters.Source) != "" {
		clauses = append(clauses, fmt.Sprintf("source ILIKE $%d", index))
		args = append(args, "%"+strings.TrimSpace(*filters.Source)+"%")
		index++
	}
	if filters.UserID != nil {
		clauses = append(clauses, fmt.Sprintf("user_id = $%d", index))
		args = append(args, *filters.UserID)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (s *textOpsPGAnalyticsStore) tableName(name string) string {
	return fmt.Sprintf("%s.%s", quoteTextOpsIdentifier(s.schema), quoteTextOpsIdentifier(name))
}

func quoteTextOpsIdentifier(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return `""`
	}
	return `"` + strings.ReplaceAll(trimmed, `"`, `""`) + `"`
}

func inferTextOpsCacheStatus(promptTokens, cachedTokens int64) string {
	if cachedTokens <= 0 {
		return "NONE"
	}
	if promptTokens <= 0 || cachedTokens >= promptTokens {
		return "COMPLETE"
	}
	return "CONTEXT"
}

func parseTextOpsBoolEnv(name string) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return false
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

func parseTextOpsIntEnv(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseTextOpsDurationEnv(name string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func firstTextOpsEnv(names ...string) string {
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseTextOpsInt64(raw string) int64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func positiveToken(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func buildTextOpsRecordID(record coreusage.Record, ts time.Time) string {
	base := strings.Join([]string{
		ts.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(record.AuthID),
		strings.TrimSpace(record.AuthIndex),
		strings.TrimSpace(record.Provider),
		strings.TrimSpace(record.Source),
		strings.TrimSpace(record.Model),
		strconv.FormatInt(record.Detail.InputTokens, 10),
		strconv.FormatInt(record.Detail.OutputTokens, 10),
		strconv.FormatInt(record.Detail.ReasoningTokens, 10),
		strconv.FormatInt(record.Detail.CachedTokens, 10),
		strconv.FormatInt(record.Detail.TotalTokens, 10),
		strconv.FormatBool(record.Failed),
	}, "|")
	sum := sha1.Sum([]byte(base))
	return hex.EncodeToString(sum[:])
}
