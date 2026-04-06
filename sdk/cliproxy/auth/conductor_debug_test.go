package auth

import (
	"bytes"
	"io"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestDebugLogAuthSelectionIncludesAuthIDForAPIKeys(t *testing.T) {
	logger := log.StandardLogger()
	originalOut := logger.Out
	originalLevel := logger.Level
	originalFormatter := logger.Formatter

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLevel(log.DebugLevel)
	logger.SetFormatter(&log.TextFormatter{
		DisableTimestamp:       true,
		DisableLevelTruncation: true,
		DisableQuote:           true,
	})
	t.Cleanup(func() {
		logger.SetOutput(originalOut)
		logger.SetLevel(originalLevel)
		logger.SetFormatter(originalFormatter)
	})

	auth := &Auth{
		ID:       "codex:apikey:84a38e5e7bde",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "yls-key-0005-0317",
		},
	}

	debugLogAuthSelection(log.NewEntry(logger), auth, "codex", "gpt-5.4")

	output, err := io.ReadAll(&buf)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	logLine := string(output)
	if !strings.Contains(logLine, "auth=codex:apikey:84a38e5e7bde") {
		t.Fatalf("debug log = %q, want auth id", logLine)
	}
	if !strings.Contains(logLine, "for model gpt-5.4") {
		t.Fatalf("debug log = %q, want model", logLine)
	}
}
