package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelPricesSavePathUsesWritablePathWhenSidecarMissing(t *testing.T) {
	base := t.TempDir()
	writable := filepath.Join(base, "writable")
	t.Setenv("WRITABLE_PATH", writable)
	t.Setenv("writable_path", "")

	configPath := filepath.Join(base, "config.yaml")
	got := resolveModelPricesSavePath(configPath)
	want := filepath.Join(writable, "model-prices.json")
	if got != want {
		t.Fatalf("resolveModelPricesSavePath() = %q, want %q", got, want)
	}
}

func TestLoadModelPricesFromWritablePathFallback(t *testing.T) {
	base := t.TempDir()
	writable := filepath.Join(base, "writable")
	t.Setenv("WRITABLE_PATH", writable)
	t.Setenv("writable_path", "")

	if err := os.MkdirAll(writable, 0o755); err != nil {
		t.Fatalf("mkdir writable: %v", err)
	}
	payload := []byte(`{"prices":{"deepseek-v4-pro":{"input":0.5,"output":1.5,"cached_input":0.0004}}}`)
	if err := os.WriteFile(filepath.Join(writable, "model-prices.json"), payload, 0o600); err != nil {
		t.Fatalf("write model prices: %v", err)
	}

	modelPricesMutex.Lock()
	oldPrices := modelPricesData
	modelPricesData = map[string]ModelPrice{}
	modelPricesMutex.Unlock()
	t.Cleanup(func() {
		modelPricesMutex.Lock()
		modelPricesData = oldPrices
		modelPricesMutex.Unlock()
	})

	if err := loadModelPricesFromFile(filepath.Join(base, "config.yaml")); err != nil {
		t.Fatalf("loadModelPricesFromFile: %v", err)
	}

	modelPricesMutex.RLock()
	price, ok := modelPricesData["deepseek-v4-pro"]
	modelPricesMutex.RUnlock()
	if !ok {
		t.Fatalf("deepseek-v4-pro price was not loaded")
	}
	if price.Input != 0.5 || price.Output != 1.5 || price.CachedInput != 0.0004 {
		t.Fatalf("loaded price = %+v", price)
	}
}
