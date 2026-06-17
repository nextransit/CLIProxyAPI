package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetModelRoutingDiagnostics reports the current credential pool for a model
// without executing an upstream request.
func (h *Handler) GetModelRoutingDiagnostics(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	var providers []string
	for _, raw := range c.QueryArray("provider") {
		for _, part := range strings.Split(raw, ",") {
			if provider := strings.TrimSpace(part); provider != "" {
				providers = append(providers, provider)
			}
		}
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager is not available"})
		return
	}

	c.JSON(http.StatusOK, manager.DiagnoseModelRouting(model, providers))
}
