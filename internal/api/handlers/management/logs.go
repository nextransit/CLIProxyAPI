package management

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

const (
	defaultLogFileName      = "main.log"
	logScannerInitialBuffer = 64 * 1024
	logScannerMaxBuffer     = 8 * 1024 * 1024
)

var logSearchAliases = map[string][]string{
	"usage_limit_reached": {
		"quota exceeded",
		": quota",
	},
	"token_invalidated": {
		"invalidated",
		"invalid_request_error",
	},
	"claude_code_tool_call_failure": {
		"claude_code_tool_call_failure",
		"no such tool available",
		"tool selection syntax",
		"those tool calls failed",
	},
	"tool_selection": {
		"tool selection syntax",
		"no such tool available",
	},
}

type logFileDescriptor struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	Type     string `json:"type"`
}

// GetLogs returns log lines with optional incremental loading.
func (h *Handler) GetLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if !h.cfg.LoggingToFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logging to file disabled"})
		return
	}

	logDir := h.logDirectory()
	if strings.TrimSpace(logDir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	files, err := h.collectLogFiles(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			cutoff := parseCutoff(c.Query("after"))
			c.JSON(http.StatusOK, gin.H{
				"lines":            []string{},
				"line-count":       0,
				"latest-timestamp": cutoff,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log files: %v", err)})
		return
	}

	limit, errLimit := parseLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}

	cutoff := parseCutoff(c.Query("after"))
	search := parseLogSearch(c)
	acc := newLogAccumulator(cutoff, limit, search)
	for i := range files {
		if errProcess := acc.consumeFile(files[i]); errProcess != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file %s: %v", files[i], errProcess)})
			return
		}
	}
	errorFiles, errErrorFiles := h.collectRequestErrorLogFiles(logDir)
	if errErrorFiles != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list request error logs: %v", errErrorFiles)})
		return
	}
	for i := range errorFiles {
		line, errSummary := summarizeRequestErrorLog(errorFiles[i])
		if errSummary != nil || line == "" {
			continue
		}
		acc.addLine(line)
	}

	lines, total, latest := acc.result()
	if latest == 0 || latest < cutoff {
		latest = cutoff
	}
	c.JSON(http.StatusOK, gin.H{
		"lines":            lines,
		"line-count":       total,
		"latest-timestamp": latest,
	})
}

// GetLogFiles lists searchable log files from the configured log directory.
func (h *Handler) GetLogFiles(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	logDir := h.logDirectory()
	if strings.TrimSpace(logDir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	files, err := h.collectManagedLogFiles(logDir, c.Query("search"))
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"files": []logFileDescriptor{}, "total": 0})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log files: %v", err)})
		return
	}

	limit, errLimit := parseLimit(c.Query("limit"))
	if errLimit != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid limit: %v", errLimit)})
		return
	}
	total := len(files)
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"files": files,
		"total": total,
	})
}

// DownloadLogFile downloads a specific managed log file by name.
func (h *Handler) DownloadLogFile(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	name := strings.TrimSpace(c.Param("name"))
	if !isManagedLogFileName(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
		return
	}

	fullPath, err := resolveManagedLogFilePath(dir, name)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "invalid") {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errStat)})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file"})
		return
	}

	c.FileAttachment(fullPath, name)
}

// DeleteLogs removes all rotated log files and truncates the active log.
func (h *Handler) DeleteLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if !h.cfg.LoggingToFile {
		c.JSON(http.StatusBadRequest, gin.H{"error": "logging to file disabled"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log directory not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log directory: %v", err)})
		return
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		fullPath := filepath.Join(dir, name)
		if name == defaultLogFileName {
			if errTrunc := os.Truncate(fullPath, 0); errTrunc != nil && !os.IsNotExist(errTrunc) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to truncate log file: %v", errTrunc)})
				return
			}
			continue
		}
		if isRotatedLogFile(name) {
			if errRemove := os.Remove(fullPath); errRemove != nil && !os.IsNotExist(errRemove) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to remove %s: %v", name, errRemove)})
				return
			}
			removed++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Logs cleared successfully",
		"removed": removed,
	})
}

// GetRequestErrorLogs lists error request log files when RequestLog is disabled.
// It returns an empty list when RequestLog is enabled.
func (h *Handler) GetRequestErrorLogs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	if h.cfg.RequestLog {
		c.JSON(http.StatusOK, gin.H{"files": []any{}})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"files": []any{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list request error logs: %v", err)})
		return
	}

	type errorLog struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Modified int64  `json:"modified"`
	}

	files := make([]errorLog, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log info for %s: %v", name, errInfo)})
			return
		}
		files = append(files, errorLog{
			Name:     name,
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Modified > files[j].Modified })

	c.JSON(http.StatusOK, gin.H{"files": files})
}

// GetRequestLogByID finds and downloads a request log file by its request ID.
// The ID is matched against the suffix of log file names (format: *-{requestID}.log).
func (h *Handler) GetRequestLogByID(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	requestID := strings.TrimSpace(c.Param("id"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.Query("id"))
	}
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing request ID"})
		return
	}
	if strings.ContainsAny(requestID, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request ID"})
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log directory not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list log directory: %v", err)})
		return
	}

	suffix := "-" + requestID + ".log"
	var matchedFile string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, suffix) {
			matchedFile = name
			break
		}
	}

	if matchedFile == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "log file not found for the given request ID"})
		return
	}

	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve log directory: %v", errAbs)})
		return
	}
	fullPath := filepath.Clean(filepath.Join(dirAbs, matchedFile))
	prefix := dirAbs + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, prefix) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file path"})
		return
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errStat)})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file"})
		return
	}

	c.FileAttachment(fullPath, matchedFile)
}

// DownloadRequestErrorLog downloads a specific error request log file by name.
func (h *Handler) DownloadRequestErrorLog(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}

	dir := h.logDirectory()
	if strings.TrimSpace(dir) == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "log directory not configured"})
		return
	}

	name := strings.TrimSpace(c.Param("name"))
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file name"})
		return
	}
	if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
		c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
		return
	}

	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve log directory: %v", errAbs)})
		return
	}
	fullPath := filepath.Clean(filepath.Join(dirAbs, name))
	prefix := dirAbs + string(os.PathSeparator)
	if !strings.HasPrefix(fullPath, prefix) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file path"})
		return
	}

	info, errStat := os.Stat(fullPath)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			c.JSON(http.StatusNotFound, gin.H{"error": "log file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read log file: %v", errStat)})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log file"})
		return
	}

	c.FileAttachment(fullPath, name)
}

func (h *Handler) logDirectory() string {
	if h == nil {
		return ""
	}
	if h.logDir != "" {
		return h.logDir
	}
	return logging.ResolveLogDirectory(h.cfg)
}

func (h *Handler) collectLogFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path  string
		order int64
	}
	cands := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == defaultLogFileName {
			cands = append(cands, candidate{path: filepath.Join(dir, name), order: 0})
			continue
		}
		if order, ok := rotationOrder(name); ok {
			cands = append(cands, candidate{path: filepath.Join(dir, name), order: order})
		}
	}
	if len(cands) == 0 {
		return []string{}, nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].order < cands[j].order })
	paths := make([]string, 0, len(cands))
	for i := len(cands) - 1; i >= 0; i-- {
		paths = append(paths, cands[i].path)
	}
	return paths, nil
}

func (h *Handler) collectManagedLogFiles(dir string, search string) ([]logFileDescriptor, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	filter := strings.ToLower(strings.TrimSpace(search))
	files := make([]logFileDescriptor, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !isManagedLogFileName(name) {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}

		info, errInfo := entry.Info()
		if errInfo != nil {
			return nil, errInfo
		}
		files = append(files, logFileDescriptor{
			Name:     name,
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
			Type:     classifyManagedLogFile(name),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified == files[j].Modified {
			return files[i].Name < files[j].Name
		}
		return files[i].Modified > files[j].Modified
	})
	return files, nil
}

func (h *Handler) collectRequestErrorLogFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	cands := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			return nil, errInfo
		}
		cands = append(cands, candidate{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].modTime.Equal(cands[j].modTime) {
			return cands[i].path < cands[j].path
		}
		return cands[i].modTime.Before(cands[j].modTime)
	})
	paths := make([]string, 0, len(cands))
	for _, cand := range cands {
		paths = append(paths, cand.path)
	}
	return paths, nil
}

type requestErrorLogSummary struct {
	name      string
	requestID string
	timestamp time.Time
	method    string
	url       string
	status    int
}

func summarizeRequestErrorLog(path string) (string, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return "", errStat
	}
	if info.IsDir() {
		return "", nil
	}

	summary := requestErrorLogSummary{
		name:      filepath.Base(path),
		requestID: requestIDFromLogFilename(filepath.Base(path)),
		timestamp: info.ModTime(),
		method:    "UNKNOWN",
		url:       "/",
		status:    http.StatusInternalServerError,
	}

	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", errOpen
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, logScannerInitialBuffer)
	scanner.Buffer(buf, logScannerMaxBuffer)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "URL:"):
			if value := strings.TrimSpace(strings.TrimPrefix(line, "URL:")); value != "" {
				summary.url = strings.SplitN(value, "?", 2)[0]
			}
		case strings.HasPrefix(line, "Method:"):
			if value := strings.TrimSpace(strings.TrimPrefix(line, "Method:")); value != "" {
				summary.method = strings.ToUpper(value)
			}
		case strings.HasPrefix(line, "Timestamp:"):
			if parsed, ok := parseRequestLogTimestamp(strings.TrimSpace(strings.TrimPrefix(line, "Timestamp:"))); ok {
				summary.timestamp = parsed
			}
		case strings.HasPrefix(line, "HTTP Status:"):
			if status := parseStatusCode(strings.TrimSpace(strings.TrimPrefix(line, "HTTP Status:"))); status > 0 {
				summary.status = status
			}
		case strings.HasPrefix(line, "Status:"):
			if status := parseStatusCode(strings.TrimSpace(strings.TrimPrefix(line, "Status:"))); status > 0 {
				summary.status = status
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return "", errScan
	}

	if summary.requestID == "" {
		summary.requestID = "--------"
	}
	return fmt.Sprintf("[%s] [%s] [error] request_failed | %s %s %d | request_log=%s",
		summary.timestamp.Local().Format("2006-01-02 15:04:05"),
		summary.requestID,
		summary.method,
		summary.url,
		summary.status,
		summary.name,
	), nil
}

func requestIDFromLogFilename(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".log")
	if name == "" {
		return ""
	}
	idx := strings.LastIndexByte(name, '-')
	if idx < 0 || idx == len(name)-1 {
		return ""
	}
	candidate := name[idx+1:]
	if len(candidate) == 8 && isLowerHex(candidate) {
		return candidate
	}
	return ""
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return value != ""
}

func parseRequestLogTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func parseStatusCode(raw string) int {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil || status < 100 || status > 599 {
		return 0
	}
	return status
}

type logAccumulator struct {
	cutoff  int64
	limit   int
	search  []string
	lines   []string
	total   int
	latest  int64
	include bool
}

func newLogAccumulator(cutoff int64, limit int, search string) *logAccumulator {
	capacity := 256
	if limit > 0 && limit < capacity {
		capacity = limit
	}
	return &logAccumulator{
		cutoff: cutoff,
		limit:  limit,
		search: expandLogSearchTerms(search),
		lines:  make([]string, 0, capacity),
	}
}

func (acc *logAccumulator) consumeFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, logScannerInitialBuffer)
	scanner.Buffer(buf, logScannerMaxBuffer)
	for scanner.Scan() {
		acc.addLine(scanner.Text())
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	return nil
}

func (acc *logAccumulator) addLine(raw string) {
	line := strings.TrimRight(raw, "\r")
	ts := parseTimestamp(line)
	if ts > acc.latest {
		acc.latest = ts
	}
	if ts > 0 {
		acc.include = acc.cutoff == 0 || ts > acc.cutoff
		if acc.cutoff == 0 || acc.include {
			acc.append(line)
		}
		return
	}
	if acc.cutoff == 0 || acc.include {
		acc.append(line)
	}
}

func (acc *logAccumulator) append(line string) {
	if !matchesLogSearch(strings.ToLower(line), acc.search) {
		return
	}
	acc.total++
	acc.lines = append(acc.lines, line)
	if acc.limit > 0 && len(acc.lines) > acc.limit {
		acc.lines = acc.lines[len(acc.lines)-acc.limit:]
	}
}

func (acc *logAccumulator) result() ([]string, int, int64) {
	if acc.lines == nil {
		acc.lines = []string{}
	}
	return acc.lines, acc.total, acc.latest
}

func parseCutoff(raw string) int64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	ts, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ts <= 0 {
		return 0
	}
	return ts
}

func parseLogSearch(c *gin.Context) string {
	if c == nil {
		return ""
	}
	for _, key := range []string{"search", "q"} {
		if value := strings.TrimSpace(c.Query(key)); value != "" {
			return value
		}
	}
	return ""
}

func expandLogSearchTerms(search string) []string {
	normalized := strings.ToLower(strings.TrimSpace(search))
	if normalized == "" {
		return nil
	}

	terms := []string{normalized}
	compact := strings.NewReplacer(" ", "", "_", "", "-", "").Replace(normalized)
	if compact != "" && compact != normalized {
		terms = append(terms, compact)
	}

	replaced := strings.NewReplacer("_", " ", "-", " ").Replace(normalized)
	if replaced != "" && replaced != normalized {
		terms = append(terms, replaced)
	}

	for key, aliases := range logSearchAliases {
		if normalized != key && compact != strings.NewReplacer(" ", "", "_", "", "-", "").Replace(key) {
			continue
		}
		terms = append(terms, aliases...)
	}

	return dedupeSearchTerms(terms)
}

func dedupeSearchTerms(terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(terms))
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result
}

func matchesLogSearch(line string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	for _, term := range terms {
		if strings.Contains(line, term) {
			return true
		}
	}
	return false
}

func parseLimit(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("must be a positive integer")
	}
	if limit <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return limit, nil
}

func parseTimestamp(line string) int64 {
	if strings.HasPrefix(line, "[") {
		line = line[1:]
	}
	if len(line) < 19 {
		return 0
	}
	candidate := line[:19]
	t, err := time.ParseInLocation("2006-01-02 15:04:05", candidate, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func isRotatedLogFile(name string) bool {
	if _, ok := rotationOrder(name); ok {
		return true
	}
	return false
}

func rotationOrder(name string) (int64, bool) {
	if order, ok := numericRotationOrder(name); ok {
		return order, true
	}
	if order, ok := timestampRotationOrder(name); ok {
		return order, true
	}
	return 0, false
}

func numericRotationOrder(name string) (int64, bool) {
	if !strings.HasPrefix(name, defaultLogFileName+".") {
		return 0, false
	}
	suffix := strings.TrimPrefix(name, defaultLogFileName+".")
	if suffix == "" {
		return 0, false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}
	return int64(n), true
}

func timestampRotationOrder(name string) (int64, bool) {
	ext := filepath.Ext(defaultLogFileName)
	base := strings.TrimSuffix(defaultLogFileName, ext)
	if base == "" {
		return 0, false
	}
	prefix := base + "-"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	clean := strings.TrimPrefix(name, prefix)
	if strings.HasSuffix(clean, ".gz") {
		clean = strings.TrimSuffix(clean, ".gz")
	}
	if ext != "" {
		if !strings.HasSuffix(clean, ext) {
			return 0, false
		}
		clean = strings.TrimSuffix(clean, ext)
	}
	if clean == "" {
		return 0, false
	}
	if idx := strings.IndexByte(clean, '.'); idx != -1 {
		clean = clean[:idx]
	}
	parsed, err := time.ParseInLocation("2006-01-02T15-04-05", clean, time.Local)
	if err != nil {
		return 0, false
	}
	return math.MaxInt64 - parsed.Unix(), true
}

func isManagedLogFileName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	switch {
	case name == defaultLogFileName:
		return true
	case isRotatedLogFile(name):
		return true
	case strings.HasSuffix(name, ".log"):
		return true
	case strings.HasSuffix(name, ".log.gz"):
		return true
	default:
		return false
	}
}

func classifyManagedLogFile(name string) string {
	switch {
	case name == defaultLogFileName:
		return "main"
	case strings.HasPrefix(name, "error-"):
		return "request-error"
	case isRotatedLogFile(name):
		return "rotated"
	default:
		return "request"
	}
}

func resolveManagedLogFilePath(dir string, name string) (string, error) {
	if !isManagedLogFileName(name) {
		return "", fmt.Errorf("invalid log file name")
	}

	dirAbs, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		return "", fmt.Errorf("failed to resolve log directory: %v", errAbs)
	}

	fullPath := filepath.Clean(filepath.Join(dirAbs, name))
	prefix := dirAbs + string(os.PathSeparator)
	if fullPath != dirAbs && !strings.HasPrefix(fullPath, prefix) {
		return "", fmt.Errorf("invalid log file path")
	}

	return fullPath, nil
}
