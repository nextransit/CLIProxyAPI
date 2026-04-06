package executor

import (
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type AuthErrorHandler struct {
	provider string
}

func NewAuthErrorHandler(provider string) *AuthErrorHandler {
	return &AuthErrorHandler{provider: provider}
}

func (h *AuthErrorHandler) HandleAuthError(err error, statusCode int, responseBody []byte, auth *cliproxyauth.Auth) error {
	if err == nil || auth == nil {
		return err
	}

	bodyStr := string(responseBody)

	if h.isKeyDisabledError(statusCode, bodyStr) {
		auth.Disabled = true
		auth.StatusMessage = "API key disabled by provider"
		auth.LastError = &cliproxyauth.Error{
			Code:    "key_disabled",
			Message: "API key has been disabled by the provider",
		}

		log.Warnf("%s API key %s has been disabled by provider: %s", h.provider, auth.ID, bodyStr)
		h.recordKeyDisabled(auth.ID)
	}

	if h.isQuotaExceededError(statusCode, bodyStr) {
		auth.Quota.Exceeded = true
		auth.Quota.NextRecoverAt = time.Now().Add(time.Hour)

		log.Warnf("%s API key %s quota exceeded: %s", h.provider, auth.ID, bodyStr)
	}

	return err
}

func (h *AuthErrorHandler) isKeyDisabledError(statusCode int, bodyStr string) bool {
	if statusCode == 401 || statusCode == 403 {
		disabledPatterns := []string{
			"已被禁用",
			"disabled",
			"invalid key",
			"invalid api key",
			"key is invalid",
			"key has been disabled",
			"key is disabled",
			"authentication failed",
			"unauthorized",
			"access denied",
		}

		for _, pattern := range disabledPatterns {
			if strings.Contains(strings.ToLower(bodyStr), strings.ToLower(pattern)) {
				return true
			}
		}
	}

	return false
}

func (h *AuthErrorHandler) isQuotaExceededError(statusCode int, bodyStr string) bool {
	if statusCode == 429 {
		quotaPatterns := []string{
			"quota exceeded",
			"rate limit",
			"too many requests",
			"usage limit",
			"limit exceeded",
		}

		for _, pattern := range quotaPatterns {
			if strings.Contains(strings.ToLower(bodyStr), strings.ToLower(pattern)) {
				return true
			}
		}
	}

	return false
}

func (h *AuthErrorHandler) recordKeyDisabled(authID string) {
	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": h.provider,
		"event":    "key_disabled",
	}).Info("Key disabled event recorded")
}

func (h *AuthErrorHandler) ShouldRetryWithDifferentKey(err error, statusCode int, bodyStr string) bool {
	if statusCode == 401 || statusCode == 403 {
		return h.isKeyDisabledError(statusCode, bodyStr)
	}

	if statusCode == 429 && strings.Contains(strings.ToLower(bodyStr), "key") {
		return true
	}

	return false
}
