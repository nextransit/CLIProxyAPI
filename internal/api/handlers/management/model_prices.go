package management

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
)

type ModelPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CachedInput float64 `json:"cached_input"`
}

type ModelPricesPayload struct {
	Version int                `json:"version"`
	Prices  map[string]ModelPrice `json:"prices"`
}

// modelPricesStore is an in-memory store for model prices, persisted alongside config
var (
	modelPricesMutex sync.RWMutex
	modelPricesData = make(map[string]ModelPrice)
)

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

// saveModelPricesToFile persists model prices to a JSON file alongside config
func saveModelPricesToFile(configPath string, prices map[string]ModelPrice) error {
	type priceConfig struct {
		Prices map[string]ModelPrice `json:"prices"`
	}
	cfg := priceConfig{Prices: prices}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Save alongside config file with .model-prices suffix
	pricesPath := configPath + ".model-prices.json"
	return writeFileAtomic(pricesPath, data)
}

// writeFileAtomic writes data to a file atomically
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
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
