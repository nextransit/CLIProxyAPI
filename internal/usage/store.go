// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// This file contains persistence mechanisms for usage statistics.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultUsageFileName        = "usage_statistics.snapshot"
	defaultAggregatesFileName   = "usage_aggregates.json"
	defaultRecentEventsFileName = "usage_recent_events.json"
	autoSaveInterval            = 5 * time.Minute
)

// Store defines the interface for persisting usage statistics.
type Store interface {
	// Save persists the given statistics snapshot.
	Save(snapshot StatisticsSnapshot) error
	// Load retrieves the persisted statistics snapshot.
	Load() (StatisticsSnapshot, error)
	// Path returns the storage path.
	Path() string
}

// FileStore implements Store using local file storage.
type FileStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewFileStore creates a new file-based usage statistics store.
func NewFileStore(baseDir string) *FileStore {
	if baseDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			baseDir = cwd
		} else {
			baseDir = os.TempDir()
		}
	}
	return &FileStore{
		filePath: filepath.Join(baseDir, defaultUsageFileName),
	}
}

// SetFilePath allows overriding the default file path.
func (s *FileStore) SetFilePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filePath = path
}

// Save persists the statistics snapshot to file.
func (s *FileStore) Save(snapshot StatisticsSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to temporary file first
	tmpFile := s.filePath + ".tmp"
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal statistics: %w", err)
	}

	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary file: %w", err)
	}

	if err := replaceFile(tmpFile, s.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	return nil
}

// Load retrieves the statistics snapshot from file.
func (s *FileStore) Load() (StatisticsSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var snapshot StatisticsSnapshot

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot, nil // Return empty snapshot if file doesn't exist
		}
		return snapshot, fmt.Errorf("failed to read file: %w", err)
	}

	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, fmt.Errorf("failed to unmarshal statistics: %w", err)
	}

	return snapshot, nil
}

// Path returns the storage file path.
func (s *FileStore) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

// PersistentLoggerPlugin extends LoggerPlugin with persistence capabilities.
//
// Persistence uses three on-disk files:
//
//   - aggregates store (usage_aggregates.json): the cheap counters plus
//     day-bucketed aggregates, no per-request Details. KB-scale.
//   - recent-events store (usage_recent_events.json): the small ring buffer
//     that powers SSE replay on reconnect. Bounded by the buffer size.
//   - full snapshot store (usage_statistics.snapshot): retained per-request
//     Details used to restore the management table after a restart.
type PersistentLoggerPlugin struct {
	*LoggerPlugin
	store          Store
	aggregateStore *AggregateFileStore
	recentStore    *RecentEventsFileStore
	stopChan       chan struct{}
}

// NewPersistentLoggerPlugin creates a new persistent logger plugin.
func NewPersistentLoggerPlugin(store Store) *PersistentLoggerPlugin {
	return &PersistentLoggerPlugin{
		LoggerPlugin: NewLoggerPlugin(),
		store:        store,
		stopChan:     make(chan struct{}),
	}
}

// AttachAggregateStores wires the lightweight aggregate + recent-events
// stores. Both are optional.
func (p *PersistentLoggerPlugin) AttachAggregateStores(agg *AggregateFileStore, recent *RecentEventsFileStore) {
	p.aggregateStore = agg
	p.recentStore = recent
}

// StartAutoSave begins automatic periodic saving of statistics.
func (p *PersistentLoggerPlugin) StartAutoSave() {
	go func() {
		ticker := time.NewTicker(autoSaveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := p.Save(); err != nil {
					log.WithError(err).Warn("failed to auto-save usage statistics")
				}
			case <-p.stopChan:
				return
			}
		}
	}()
}

// Stop stops the auto-save goroutine.
func (p *PersistentLoggerPlugin) Stop() {
	close(p.stopChan)
}

// Save persists aggregate counters, the recent-event journal, and the retained
// per-request Details. The full snapshot is intentionally kept because the
// management request table cannot be reconstructed from aggregate counters.
func (p *PersistentLoggerPlugin) Save() error {
	if p.stats == nil {
		return nil
	}

	var saveErrors []error
	if p.aggregateStore != nil {
		agg := p.stats.AggregateSnapshot()
		if err := p.aggregateStore.Save(agg); err != nil {
			saveErrors = append(saveErrors, fmt.Errorf("aggregate save: %w", err))
		}
		if p.recentStore != nil {
			evt := p.stats.RecentEventsSnapshot()
			if err := p.recentStore.Save(evt); err != nil {
				saveErrors = append(saveErrors, fmt.Errorf("recent events save: %w", err))
			}
		}
	}
	if p.store != nil {
		snapshot := p.stats.Snapshot()
		if err := p.store.Save(snapshot); err != nil {
			saveErrors = append(saveErrors, fmt.Errorf("retained details save: %w", err))
		}
	}
	if len(saveErrors) > 0 {
		return errors.Join(saveErrors...)
	}
	if p.aggregateStore != nil {
		log.Debugf("usage statistics saved (aggregates + recent events + retained details) to %s", p.aggregateStore.Path())
	} else if p.store != nil {
		log.Debugf("usage statistics saved to %s", p.store.Path())
	}
	return nil
}

// Load restores statistics from storage. When aggregate data exists, it is
// authoritative for counters and ring seeds; the legacy snapshot contributes
// only per-request Details. This prevents stale or previously double-counted
// legacy totals from replacing the bounded aggregate view.
//
// The legacy path is the safe default for installations upgrading from
// before the aggregate file existed: it costs O(detailCount) at startup
// but restores the dashboard buckets for "today" instead of leaving them
// empty until ingest refills the rings.
func (p *PersistentLoggerPlugin) Load() error {
	if p.stats == nil {
		return nil
	}

	legacySnapshot, legacyErr := p.loadLegacySnapshot()
	if legacyErr != nil {
		log.WithError(legacyErr).Warn("failed to load retained usage details")
		legacySnapshot = nil
	}
	legacyLoaded := legacySnapshot != nil &&
		(legacySnapshot.TotalRequests > 0 || legacySnapshot.TotalTokens > 0 || len(legacySnapshot.APIs) > 0)

	if p.aggregateStore != nil {
		agg, aggregateErr := p.aggregateStore.Load()
		if aggregateErr != nil {
			log.WithError(aggregateErr).Warn("failed to load usage aggregates")
		}
		aggregateLoaded := aggregateErr == nil &&
			(agg.TotalRequests > 0 || agg.TotalTokens > 0 || len(agg.ModelTotals) > 0)
		if aggregateLoaded {
			p.stats.ApplyAggregateSnapshot(agg)
			if legacyLoaded {
				p.stats.RestoreDetailsFromLegacySnapshot(*legacySnapshot, ringsEmpty(p.stats))
			}
			log.Infof("restored usage statistics from aggregates + legacy details: %d requests, %d tokens",
				p.stats.TotalRequests(), p.stats.TotalTokens())
		} else if legacyLoaded {
			p.stats.RestoreFromLegacySnapshot(*legacySnapshot)
			log.Infof("restored usage statistics from legacy snapshot (rings rebuilt): %d requests, %d tokens",
				p.stats.TotalRequests(), p.stats.TotalTokens())
		} else {
			if aggregateErr != nil {
				return aggregateErr
			}
			if legacyErr != nil {
				return legacyErr
			}
			log.Debug("no previous usage statistics found")
		}
		if p.recentStore != nil {
			evt, err := p.recentStore.Load()
			if err != nil {
				log.WithError(err).Warn("failed to load recent usage events")
			} else if len(evt.Events) > 0 {
				p.stats.ApplyRecentEvents(evt.Events)
				p.stats.RestoreDetailsFromRecentEvents(evt.Events)
			}
		}
		return nil
	}

	if !legacyLoaded {
		if legacyErr != nil {
			return legacyErr
		}
		log.Debug("no previous usage statistics found")
		return nil
	}
	p.stats.RestoreFromLegacySnapshot(*legacySnapshot)
	log.Infof("restored usage statistics from legacy snapshot (rings rebuilt): %d requests, %d tokens",
		p.stats.TotalRequests(), p.stats.TotalTokens())
	return nil
}

// loadLegacySnapshot returns the legacy StatisticsSnapshot, or nil if no
// legacy store is attached. Errors propagate to the caller.
func (p *PersistentLoggerPlugin) loadLegacySnapshot() (*StatisticsSnapshot, error) {
	if p.store == nil {
		return nil, nil
	}
	snap, err := p.store.Load()
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// ringsEmpty reports whether both pre-aggregated rings currently carry no
// traffic. It is used to decide whether legacy details need to refill rings
// when the aggregate snapshot predates ring seeds.
func ringsEmpty(s *RequestStatistics) bool {
	if s == nil {
		return true
	}
	for _, ring := range []*BucketRing{s.BucketRing5m(), s.BucketRing1h()} {
		if ring == nil {
			continue
		}
		for _, bucket := range ring.ReadSnapshot().Buckets {
			if bucket.Requests > 0 {
				return false
			}
		}
	}
	return true
}

// RestoreStatisticsFromStore reloads persisted usage statistics into the provided in-memory store
// when the target store is currently empty.
func RestoreStatisticsFromStore(stats *RequestStatistics, store Store) (bool, error) {
	if stats == nil || store == nil {
		return false, nil
	}

	if stats.IsEmpty() {
		snapshot, err := store.Load()
		if err != nil {
			return false, err
		}
		if snapshot.TotalRequests == 0 && snapshot.TotalTokens == 0 && len(snapshot.APIs) == 0 {
			return false, nil
		}

		result := stats.MergeSnapshot(snapshot)
		log.Infof("restored usage statistics on demand: %d requests, %d tokens loaded (%d added, %d skipped)",
			snapshot.TotalRequests, snapshot.TotalTokens, result.Added, result.Skipped)
		return true, nil
	}
	return false, nil
}

var (
	persistentPlugin *PersistentLoggerPlugin
	storeOnce        sync.Once
	eventStoreMu     sync.RWMutex
	usageEventStore  UsageEventStore
)

// InitializePersistence sets up persistent storage for usage statistics.
// It wires aggregate, recent-event, and retained-detail stores.
func InitializePersistence(baseDir string) error {
	var initErr error
	storeOnce.Do(func() {
		store := NewFileStore(baseDir)
		aggStore := NewAggregateFileStore(baseDir)
		recentStore := NewRecentEventsFileStore(baseDir)
		if eventStore, err := NewSQLiteUsageEventStore(baseDir); err != nil {
			log.WithError(err).Warn("failed to initialize usage event database")
		} else {
			setUsageEventStore(eventStore)
		}
		persistentPlugin = NewPersistentLoggerPlugin(store)
		persistentPlugin.AttachAggregateStores(aggStore, recentStore)

		if err := persistentPlugin.Load(); err != nil {
			log.WithError(err).Warn("failed to load usage statistics from storage")
		}
		if eventStore := getUsageEventStore(); eventStore != nil {
			if imported, err := eventStore.ImportSnapshot(context.Background(), persistentPlugin.stats.Snapshot()); err != nil {
				log.WithError(err).Warn("failed to import retained usage details into event database")
			} else if imported > 0 {
				log.Infof("imported %d retained usage details into event database", imported)
			}
		}

		persistentPlugin.StartAutoSave()

		log.Infof("usage statistics persistence initialized: aggregates=%s recent=%s legacy=%s",
			aggStore.Path(), recentStore.Path(), store.Path())
	})
	return initErr
}

func setUsageEventStore(store UsageEventStore) {
	eventStoreMu.Lock()
	defer eventStoreMu.Unlock()
	usageEventStore = store
}

func getUsageEventStore() UsageEventStore {
	eventStoreMu.RLock()
	defer eventStoreMu.RUnlock()
	return usageEventStore
}

func persistUsageEvent(ctx context.Context, record coreusage.Record, evt UsageEvent) {
	store := getUsageEventStore()
	if store == nil {
		return
	}
	if err := store.InsertUsageEvent(ctx, record, evt); err != nil {
		log.WithError(err).Warn("failed to persist usage event")
	}
}

// GetPersistentPlugin returns the persistent plugin instance.
func GetPersistentPlugin() *PersistentLoggerPlugin {
	return persistentPlugin
}

// RestoreStatisticsIfEmpty reloads persisted statistics into the provided in-memory store when needed.
func RestoreStatisticsIfEmpty(stats *RequestStatistics) (bool, error) {
	if persistentPlugin == nil {
		return false, nil
	}
	return RestoreStatisticsFromStore(stats, persistentPlugin.store)
}

// SaveStatistics manually triggers a save of current statistics.
func SaveStatistics() error {
	if persistentPlugin == nil {
		return fmt.Errorf("persistence not initialized")
	}
	return persistentPlugin.Save()
}

// SetUsageStore allows setting a custom store implementation.
func SetUsageStore(store Store) {
	if persistentPlugin != nil {
		persistentPlugin.store = store
	}
}

// AggregateSnapshot is the small, details-free view of usage counters that
// gets written to disk on every auto-save. It excludes the per-model
// Details slice, which keeps the on-disk file KB-scale regardless of how
// long the server has been running.
type AggregateSnapshot struct {
	Version       int                  `json:"version"`
	TotalRequests int64                `json:"total_requests"`
	SuccessCount  int64                `json:"success_count"`
	FailureCount  int64                `json:"failure_count"`
	TotalTokens   int64                `json:"total_tokens"`
	RequestsByDay map[string]int64     `json:"requests_by_day"`
	TokensByDay   map[string]int64     `json:"tokens_by_day"`
	ExportedAt    time.Time            `json:"exported_at"`
	RingSeed5m    *RingSeed            `json:"ring_seed_5m,omitempty"`
	RingSeed1h    *RingSeed            `json:"ring_seed_1h,omitempty"`
	ModelTotals   map[string]APITotals `json:"model_totals,omitempty"`
}

// APITotals is the per-API counter view used to restore the management UI
// "stats per API key" view without scanning Details.
type APITotals struct {
	TotalRequests int64                  `json:"total_requests"`
	TotalTokens   int64                  `json:"total_tokens"`
	Models        map[string]ModelTotals `json:"models,omitempty"`
}

// ModelTotals is the per-(apiKey,model) counter view. The optional window
// fields are populated for ring-bucket persistence.
type ModelTotals struct {
	TotalRequests int64 `json:"total_requests"`
	TotalTokens   int64 `json:"total_tokens"`
	Failures      int64 `json:"failures,omitempty"`
	LatencySum    int64 `json:"latency_sum,omitempty"`
	LatencyN      int64 `json:"latency_n,omitempty"`
}

// RingSeed captures the rotating ring buckets at save time.
type RingSeed struct {
	BucketSizeMs int64            `json:"bucket_size_ms"`
	HeadIndex    int              `json:"head_index"`
	StartTimeMs  int64            `json:"start_time_ms"`
	Buckets      []RingSeedBucket `json:"buckets"`
}

// RingSeedBucket is one bucket of the rotating ring. It holds aggregate
// counters; per-model and per-authIndex breakdowns are kept in JSON objects.
type RingSeedBucket struct {
	StartTimeMs    int64                  `json:"start_time_ms"`
	Requests       int64                  `json:"requests"`
	Tokens         int64                  `json:"tokens"`
	Failures       int64                  `json:"failures"`
	LatencySum     int64                  `json:"latency_sum"`
	LatencyN       int64                  `json:"latency_n"`
	ModelBreakdown map[string]ModelTotals `json:"model_breakdown,omitempty"`
	AuthBreakdown  map[string]AuthTotals  `json:"auth_breakdown,omitempty"`
}

// AuthTotals is one authIndex's per-bucket aggregate.
type AuthTotals struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
	Failures int64 `json:"failures"`
}

// RecentEventsSnapshot captures the small ring buffer that powers the SSE
// replay path. Persisting it lets a freshly started server give new SSE
// subscribers something to replay beyond the most recent batch.
type RecentEventsSnapshot struct {
	Version int          `json:"version"`
	Events  []UsageEvent `json:"events"`
}

// AggregateFileStore is a tiny store dedicated to AggregateSnapshot. It
// keeps writes cheap so the 5-minute auto-save loop does not contend with
// dashboard reads.
type AggregateFileStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewAggregateFileStore constructs an aggregate store at the given base
// directory.
func NewAggregateFileStore(baseDir string) *AggregateFileStore {
	if baseDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			baseDir = cwd
		} else {
			baseDir = os.TempDir()
		}
	}
	return &AggregateFileStore{filePath: filepath.Join(baseDir, defaultAggregatesFileName)}
}

// SetFilePath overrides the on-disk path.
func (s *AggregateFileStore) SetFilePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filePath = path
}

// Save writes the snapshot atomically.
func (s *AggregateFileStore) Save(snap AggregateSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := replaceFile(tmp, s.filePath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Load reads the snapshot. Missing file returns the zero value.
func (s *AggregateFileStore) Load() (AggregateSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var snap AggregateSnapshot
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// Path returns the on-disk path.
func (s *AggregateFileStore) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

// RecentEventsFileStore persists the most-recent UsageEvents ring buffer.
type RecentEventsFileStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewRecentEventsFileStore constructs a recent-events store at baseDir.
func NewRecentEventsFileStore(baseDir string) *RecentEventsFileStore {
	if baseDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			baseDir = cwd
		} else {
			baseDir = os.TempDir()
		}
	}
	return &RecentEventsFileStore{filePath: filepath.Join(baseDir, defaultRecentEventsFileName)}
}

// SetFilePath overrides the on-disk path.
func (s *RecentEventsFileStore) SetFilePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filePath = path
}

// Save writes the events atomically.
func (s *RecentEventsFileStore) Save(snap RecentEventsSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := replaceFile(tmp, s.filePath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Load reads the events. Missing file returns the zero value.
func (s *RecentEventsFileStore) Load() (RecentEventsSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var snap RecentEventsSnapshot
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// Path returns the on-disk path.
func (s *RecentEventsFileStore) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

func replaceFile(tmpPath, targetPath string) error {
	if err := os.Rename(tmpPath, targetPath); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpPath, targetPath)
}
