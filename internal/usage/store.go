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
	defaultUsageFileName = "usage_statistics.snapshot"
	autoSaveInterval     = 5 * time.Minute
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
type PersistentLoggerPlugin struct {
	*LoggerPlugin
	store    Store
	stopChan chan struct{}
}

// NewPersistentLoggerPlugin creates a new persistent logger plugin.
func NewPersistentLoggerPlugin(store Store) *PersistentLoggerPlugin {
	return &PersistentLoggerPlugin{
		LoggerPlugin: NewLoggerPlugin(),
		store:        store,
		stopChan:     make(chan struct{}),
	}
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

// Save persists current statistics to storage.
func (p *PersistentLoggerPlugin) Save() error {
	if p.store == nil || p.stats == nil {
		return nil
	}

	snapshot := p.stats.Snapshot()
	if err := p.store.Save(snapshot); err != nil {
		return err
	}

	log.Debugf("usage statistics saved to %s", p.store.Path())
	return nil
}

// Load restores statistics from storage.
func (p *PersistentLoggerPlugin) Load() error {
	if p.store == nil || p.stats == nil {
		return nil
	}

	snapshot, err := p.store.Load()
	if err != nil {
		return err
	}

	if snapshot.TotalRequests == 0 && snapshot.TotalTokens == 0 {
		log.Debug("no previous usage statistics found")
		return nil
	}

	// Merge loaded data into current statistics
	result := p.stats.MergeSnapshot(snapshot)
	log.Infof("restored usage statistics: %d requests, %d tokens loaded (%d added, %d skipped)",
		snapshot.TotalRequests, snapshot.TotalTokens, result.Added, result.Skipped)

	return nil
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
func InitializePersistence(baseDir string) error {
	var initErr error
	storeOnce.Do(func() {
		store := NewFileStore(baseDir)
		persistentPlugin = NewPersistentLoggerPlugin(store)

		// Load existing data
		if err := persistentPlugin.Load(); err != nil {
			log.WithError(err).Warn("failed to load usage statistics from storage")
		}

		// Start auto-save
		persistentPlugin.StartAutoSave()

		log.Infof("usage statistics persistence initialized: %s", store.Path())
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
