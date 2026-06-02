package management

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

type ModelPrice struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input"`
}

type ModelPricesPayload struct {
	Version int                   `json:"version"`
	Prices  map[string]ModelPrice `json:"prices"`
}

// modelPricesStore is an in-memory store for model prices, persisted to a config sidecar or writable path.
var (
	modelPricesMutex sync.RWMutex
	modelPricesData  = make(map[string]ModelPrice)
)

func loadModelPricesFromFile(configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil
	}
	var data []byte
	for _, pricesPath := range modelPricesFilePaths(configPath) {
		candidateData, err := os.ReadFile(pricesPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		data = candidateData
		break
	}
	if len(data) == 0 {
		return nil
	}

	var payload ModelPricesPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	modelPricesMutex.Lock()
	defer modelPricesMutex.Unlock()

	modelPricesData = make(map[string]ModelPrice, len(payload.Prices))
	for model, price := range payload.Prices {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		modelPricesData[model] = price
	}
	return nil
}

// GetModelPrices returns all stored model prices
func (h *Handler) GetModelPrices(c *gin.Context) {
	modelPricesMutex.RLock()
	defer modelPricesMutex.RUnlock()
	c.JSON(http.StatusOK, gin.H{
		"version": 1,
		"prices":  modelPricesData,
	})
}

// PutModelPrices replaces all stored model prices
func (h *Handler) PutModelPrices(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload ModelPricesPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	modelPricesMutex.Lock()
	defer modelPricesMutex.Unlock()

	// Clear and replace with new data
	modelPricesData = make(map[string]ModelPrice)
	for model, price := range payload.Prices {
		modelPricesData[model] = price
	}

	// Persist to disk
	if h != nil && h.configFilePath != "" {
		if err := saveModelPricesToFile(h.configFilePath, modelPricesData); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"version": 1,
		"prices":  modelPricesData,
	})
}

// PatchModelPrices updates individual model prices (partial update)
func (h *Handler) PatchModelPrices(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload ModelPricesPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	modelPricesMutex.Lock()
	defer modelPricesMutex.Unlock()

	// Update only the models that are in the payload
	for model, price := range payload.Prices {
		modelPricesData[model] = price
	}

	// Persist to disk
	if h != nil && h.configFilePath != "" {
		if err := saveModelPricesToFile(h.configFilePath, modelPricesData); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"version": 1,
		"prices":  modelPricesData,
	})
}

// saveModelPricesToFile persists model prices to a JSON file.
func saveModelPricesToFile(configPath string, prices map[string]ModelPrice) error {
	type priceConfig struct {
		Prices map[string]ModelPrice `json:"prices"`
	}
	cfg := priceConfig{Prices: prices}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	pricesPath := resolveModelPricesSavePath(configPath)
	if pricesPath == "" {
		return nil
	}
	return writeFileAtomic(pricesPath, data)
}

func modelPricesFilePaths(configPath string) []string {
	paths := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(configPath); trimmed != "" {
		paths = append(paths, trimmed+".model-prices.json")
	}
	if writablePath := strings.TrimSpace(util.WritablePath()); writablePath != "" {
		paths = append(paths, filepath.Join(writablePath, "model-prices.json"))
	}
	return dedupeModelPricesPaths(paths)
}

func resolveModelPricesSavePath(configPath string) string {
	paths := modelPricesFilePaths(configPath)
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if writablePath := strings.TrimSpace(util.WritablePath()); writablePath != "" {
		return filepath.Join(writablePath, "model-prices.json")
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func dedupeModelPricesPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(filepath.Clean(path))
		if path == "." || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

// writeFileAtomic writes data to a file atomically
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "model-prices-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
