// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// This file contains persistence mechanisms for usage statistics.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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

	// Atomic rename
	if err := os.Rename(tmpFile, s.filePath); err != nil {
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
// Persistence is split into two on-disk files for cheap, bounded saves:
//
//   - aggregates store (usage_aggregates.json): the cheap counters plus
//     day-bucketed aggregates, no per-request Details. KB-scale.
//   - recent-events store (usage_recent_events.json): the small ring buffer
//     that powers SSE replay on reconnect. Bounded by the buffer size.
//
// A legacy file-backed Store (FileStore, usage_statistics.snapshot) is
// still supported for reads so existing installations keep working, but
// new writes go through the aggregate path. Loading from the legacy
// snapshot falls back to MergeSnapshot which scans Details and is slow on
// large histories; new installations skip this path entirely.
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
// stores. Both are optional; when set, Save/Load prefer them over the
// legacy full snapshot path.
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
					log.WithError(err).Debug("failed to auto-save usage statistics")
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

// Save persists current statistics to storage using the cheapest available
// path. When aggregate + recent-events stores are attached (the common
// case), Save writes only the aggregate counters and the small recent-
// events ring. The legacy full snapshot is still written as a best-effort
// fallback when no aggregate store is attached.
func (p *PersistentLoggerPlugin) Save() error {
	if p.stats == nil {
		return nil
	}

	if p.aggregateStore != nil {
		agg := p.stats.AggregateSnapshot()
		if err := p.aggregateStore.Save(agg); err != nil {
			return fmt.Errorf("aggregate save: %w", err)
		}
		if p.recentStore != nil {
			evt := p.stats.RecentEventsSnapshot()
			if err := p.recentStore.Save(evt); err != nil {
				return fmt.Errorf("recent events save: %w", err)
			}
		}
	}
	// Also save the legacy full snapshot when a legacy store is attached.
	// This keeps the existing ImportUsageStatistics test contract (which
	// injects a stub store via SetUsageStore) and provides a recovery path
	// for installations that still depend on the legacy file.
	if p.store != nil {
		snapshot := p.stats.Snapshot()
		if err := p.store.Save(snapshot); err != nil {
			return err
		}
	}
	if p.aggregateStore != nil {
		log.Debugf("usage statistics saved (aggregates + recent events) to %s", p.aggregateStore.Path())
	} else if p.store != nil {
		log.Debugf("usage statistics saved to %s", p.store.Path())
	}
	return nil
}

// Load restores statistics from storage. Order of preference:
//  1. AggregateFileStore (cheap counters + RingSeed + ModelTotals).
//  2. RecentEventsFileStore (SSE replay ring).
//  3. Legacy StatisticsSnapshot (apis/models/Details). Used both as a
//     counter source and as the source of per-request timestamps that
//     refill the rotating rings when no RingSeed is available.
//
// The legacy path is the safe default for installations upgrading from
// before the aggregate file existed: it costs O(detailCount) at startup
// but restores the dashboard buckets for "today" instead of leaving them
// empty until ingest refills the rings.
func (p *PersistentLoggerPlugin) Load() error {
	if p.stats == nil {
		return nil
	}

	legacySnapshot, err := p.loadLegacySnapshot()
	if err != nil {
		return err
	}

	if p.aggregateStore != nil {
		agg, err := p.aggregateStore.Load()
		if err != nil {
			return err
		}
		if agg.TotalRequests > 0 || agg.TotalTokens > 0 {
			p.stats.ApplyAggregateSnapshot(agg)
		}
		// When the aggregate snapshot lacks RingSeed data (older files)
		// or the rings end up empty after restore, fall back to the
		// legacy details to repopulate the dashboard buckets.
		if ringsEmpty(p.stats) {
			if legacySnapshot != nil {
				p.stats.RestoreFromLegacySnapshot(*legacySnapshot)
				log.Infof("restored usage statistics from aggregates + legacy details: %d requests, %d tokens",
					agg.TotalRequests, agg.TotalTokens)
			} else {
				log.Infof("restored usage statistics from aggregates (no ring data): %d requests, %d tokens",
					agg.TotalRequests, agg.TotalTokens)
			}
		} else {
			log.Infof("restored usage statistics from aggregates (rings seeded): %d requests, %d tokens",
				agg.TotalRequests, agg.TotalTokens)
		}
		if p.recentStore != nil {
			evt, err := p.recentStore.Load()
			if err == nil && len(evt.Events) > 0 {
				p.stats.ApplyRecentEvents(evt.Events)
			}
		}
		return nil
	}

	if legacySnapshot == nil {
		return nil
	}
	if legacySnapshot.TotalRequests == 0 && legacySnapshot.TotalTokens == 0 {
		log.Debug("no previous usage statistics found")
		return nil
	}
	// No aggregate file: apply legacy snapshot directly. This rebuilds
	// rings from the per-request details and also rehydrates apis/models.
	p.stats.RestoreFromLegacySnapshot(*legacySnapshot)
	log.Infof("restored usage statistics from legacy snapshot (rings rebuilt): %d requests, %d tokens",
		legacySnapshot.TotalRequests, legacySnapshot.TotalTokens)
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
// traffic. It is used to decide whether the legacy details need to refill
// the rings after the aggregate restore.
func ringsEmpty(s *RequestStatistics) bool {
	if s == nil {
		return true
	}
	if ring := s.BucketRing5m(); ring != nil {
		snap := ring.ReadSnapshot()
		for _, b := range snap.Buckets {
			if b.Requests > 0 {
				return false
			}
		}
	}
	if ring := s.BucketRing1h(); ring != nil {
		snap := ring.ReadSnapshot()
		for _, b := range snap.Buckets {
			if b.Requests > 0 {
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

	current := stats.Snapshot()
	if current.TotalRequests > 0 || current.TotalTokens > 0 || len(current.APIs) > 0 {
		return false, nil
	}

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

var (
	persistentPlugin *PersistentLoggerPlugin
	storeOnce        sync.Once
)

// InitializePersistence sets up persistent storage for usage statistics.
// It wires the cheap aggregate + recent-events stores so periodic saves do
// not pay the cost of serializing per-request Details. The legacy
// full-snapshot store is still constructed for backward-compatible reads
// from older installations.
func InitializePersistence(baseDir string) error {
	var initErr error
	storeOnce.Do(func() {
		store := NewFileStore(baseDir)
		aggStore := NewAggregateFileStore(baseDir)
		recentStore := NewRecentEventsFileStore(baseDir)
		persistentPlugin = NewPersistentLoggerPlugin(store)
		persistentPlugin.AttachAggregateStores(aggStore, recentStore)

		if err := persistentPlugin.Load(); err != nil {
			log.WithError(err).Warn("failed to load usage statistics from storage")
		}

		persistentPlugin.StartAutoSave()

		log.Infof("usage statistics persistence initialized: aggregates=%s recent=%s legacy=%s",
			aggStore.Path(), recentStore.Path(), store.Path())
	})
	return initErr
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

// ModelTotals is the per-(apiKey,model) counter view.
type ModelTotals struct {
	TotalRequests int64 `json:"total_requests"`
	TotalTokens   int64 `json:"total_tokens"`
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
	if err := os.Rename(tmp, s.filePath); err != nil {
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
	if err := os.Rename(tmp, s.filePath); err != nil {
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
