package management

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	textOpsNewsBodyLimit = 2 << 20
	textOpsNewsCacheTTL  = 2 * time.Minute
	textOpsNewsMaxItems  = 6
)

var (
	textOpsNewsFeedSources = []textOpsNewsFeedSource{
		{Name: "Google News", Endpoint: "https://news.google.com/rss/search"},
		{Name: "Bing News", Endpoint: "https://www.bing.com/news/search"},
	}
	textOpsNewsCache = struct {
		sync.RWMutex
		entries map[string]textOpsNewsCacheEntry
	}{entries: make(map[string]textOpsNewsCacheEntry)}
)

type textOpsNewsFeedSource struct {
	Name     string
	Endpoint string
}

type textOpsNewsCacheEntry struct {
	result    textOpsNewsResult
	expiresAt time.Time
}

type textOpsNewsItem struct {
	Title       string
	Link        string
	Source      string
	PublishedAt time.Time
}

type textOpsNewsResult struct {
	Topic      string
	DataSource string
	Items      []textOpsNewsItem
}

type textOpsNewsFeedResult struct {
	result textOpsNewsResult
	err    error
}

type textOpsRSSDocument struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			PublishedAt string `xml:"pubDate"`
			Source      struct {
				Name string `xml:",chardata"`
			} `xml:"source"`
			NewsSource string `xml:"Source"`
		} `xml:"item"`
	} `xml:"channel"`
}

func isTextOpsNewsQuery(userQuery string) bool {
	normalized := strings.ToLower(strings.TrimSpace(userQuery))
	if normalized == "" {
		return false
	}

	hasNewsCue := false
	for _, cue := range []string{"新闻", "资讯", "热点", "头条", "news"} {
		if strings.Contains(normalized, cue) {
			hasNewsCue = true
			break
		}
	}
	if !hasNewsCue {
		return false
	}

	for _, cue := range []string{
		"今天", "今日", "最新", "最近", "现在", "当前", "实时", "有什么", "有哪些", "有啥",
		"ai", "人工智能", "大模型", "生成式", "llm", "openai", "anthropic", "claude", "gemini", "deepseek",
		"today", "latest", "recent",
	} {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
}

func extractTextOpsNewsTopic(userQuery string) string {
	replacer := strings.NewReplacer(
		"今天", "", "今日", "", "最新", "", "最近", "", "现在", "", "当前", "", "实时", "",
		"有什么", "", "有哪些", "", "有啥", "", "给我看看", "", "帮我查", "", "查询", "", "查一下", "",
		"新闻", "", "资讯", "", "消息", "", "动态", "", "热点", "", "头条", "",
		"请问", "", "告诉我", "", "today", "", "Today", "", "latest", "", "Latest", "",
		"recent", "", "Recent", "", "news", "", "News", "", "about", "", "About", "",
		"的", "", "吗", "", "呢", "",
	)
	topic := replacer.Replace(strings.TrimSpace(userQuery))
	topic = strings.TrimSpace(strings.Trim(topic, "，。！？,.!?;；:：()（）[]【】"))
	if topic == "" {
		return "人工智能"
	}
	if strings.EqualFold(topic, "ai") {
		return "AI"
	}
	return topic
}

func (h *Handler) fetchTextOpsNews(ctx context.Context, topic string, currentTime time.Time) (textOpsNewsResult, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(topic))
	if cached, ok := getCachedTextOpsNews(cacheKey, time.Now()); ok {
		return cached, nil
	}
	if len(textOpsNewsFeedSources) == 0 {
		return textOpsNewsResult{}, fmt.Errorf("no news feed sources configured")
	}

	transport := http.DefaultTransport
	if h != nil {
		transport = h.apiCallTransport(nil)
	}
	client := &http.Client{Transport: transport}
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan textOpsNewsFeedResult, len(textOpsNewsFeedSources))
	for _, source := range textOpsNewsFeedSources {
		source := source
		go func() {
			result, err := fetchTextOpsNewsSource(fetchCtx, client, source, topic, currentTime)
			results <- textOpsNewsFeedResult{result: result, err: err}
		}()
	}

	errorsBySource := make([]string, 0, len(textOpsNewsFeedSources))
	for range textOpsNewsFeedSources {
		select {
		case <-ctx.Done():
			return textOpsNewsResult{}, ctx.Err()
		case result := <-results:
			if result.err == nil {
				cancel()
				cacheTextOpsNews(cacheKey, result.result, time.Now().Add(textOpsNewsCacheTTL))
				return result.result, nil
			}
			errorsBySource = append(errorsBySource, result.err.Error())
		}
	}
	return textOpsNewsResult{}, fmt.Errorf("all news feeds failed: %s", strings.Join(errorsBySource, "; "))
}

func fetchTextOpsNewsSource(
	ctx context.Context,
	client *http.Client,
	source textOpsNewsFeedSource,
	topic string,
	currentTime time.Time,
) (textOpsNewsResult, error) {
	feedURL, errURL := buildTextOpsNewsFeedURL(source, topic)
	if errURL != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s URL failed: %w", source.Name, errURL)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if errRequest != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s request failed: %w", source.Name, errRequest)
	}
	request.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	request.Header.Set("User-Agent", "CLIProxyAPI/AI-Workspace")

	response, errDo := client.Do(request)
	if errDo != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s request failed: %w", source.Name, errDo)
	}
	payload, errRead := io.ReadAll(io.LimitReader(response.Body, textOpsNewsBodyLimit))
	errClose := response.Body.Close()
	if errRead != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s response failed: %w", source.Name, errRead)
	}
	if errClose != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s response close failed: %w", source.Name, errClose)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return textOpsNewsResult{}, fmt.Errorf("%s returned status %d", source.Name, response.StatusCode)
	}

	var document textOpsRSSDocument
	if errDecode := xml.Unmarshal(payload, &document); errDecode != nil {
		return textOpsNewsResult{}, fmt.Errorf("%s decode failed: %w", source.Name, errDecode)
	}

	items := make([]textOpsNewsItem, 0, textOpsNewsMaxItems)
	seenTitles := make(map[string]struct{}, textOpsNewsMaxItems)
	for _, feedItem := range document.Channel.Items {
		title := strings.TrimSpace(feedItem.Title)
		link := sanitizeTextOpsNewsLink(feedItem.Link)
		if title == "" || link == "" {
			continue
		}
		titleKey := strings.ToLower(title)
		if _, exists := seenTitles[titleKey]; exists {
			continue
		}
		publishedAt := parseTextOpsNewsTime(feedItem.PublishedAt)
		if publishedAt.IsZero() ||
			publishedAt.Before(currentTime.Add(-24*time.Hour)) ||
			publishedAt.After(currentTime.Add(5*time.Minute)) {
			continue
		}
		seenTitles[titleKey] = struct{}{}
		items = append(items, textOpsNewsItem{
			Title:       title,
			Link:        link,
			Source:      firstNonEmptyTextOpsValue(feedItem.Source.Name, feedItem.NewsSource, source.Name),
			PublishedAt: publishedAt,
		})
		if len(items) >= textOpsNewsMaxItems {
			break
		}
	}
	if len(items) == 0 {
		return textOpsNewsResult{}, fmt.Errorf("%s returned no usable news", source.Name)
	}
	sort.SliceStable(items, func(left, right int) bool {
		return items[left].PublishedAt.After(items[right].PublishedAt)
	})
	return textOpsNewsResult{Topic: topic, DataSource: source.Name, Items: items}, nil
}

func buildTextOpsNewsFeedURL(source textOpsNewsFeedSource, topic string) (string, error) {
	feedURL, errURL := url.Parse(source.Endpoint)
	if errURL != nil {
		return "", errURL
	}
	query := feedURL.Query()
	searchTopic := strings.TrimSpace(topic)
	if strings.EqualFold(searchTopic, "AI") {
		searchTopic = "AI 人工智能"
	}
	query.Set("q", searchTopic)
	switch source.Name {
	case "Google News":
		query.Set("q", searchTopic+" when:1d")
		query.Set("hl", "zh-CN")
		query.Set("gl", "CN")
		query.Set("ceid", "CN:zh-Hans")
	case "Bing News":
		query.Set("format", "rss")
		query.Set("setlang", "zh-cn")
	}
	feedURL.RawQuery = query.Encode()
	return feedURL.String(), nil
}

func parseTextOpsNewsTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func sanitizeTextOpsNewsLink(value string) string {
	parsed, errURL := url.Parse(strings.TrimSpace(value))
	if errURL != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

func buildTextOpsNewsSummary(news textOpsNewsResult, currentTime time.Time) map[string]any {
	items := make([]map[string]any, 0, len(news.Items))
	for _, item := range news.Items {
		publishedAt := ""
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt.Format(time.RFC3339)
		}
		items = append(items, map[string]any{
			"title":        item.Title,
			"url":          item.Link,
			"source":       item.Source,
			"published_at": publishedAt,
		})
	}
	return map[string]any{
		"response_kind": textOpsQueryKindNews,
		"data_source":   news.DataSource,
		"topic":         news.Topic,
		"item_count":    len(news.Items),
		"items":         items,
		"generated_at":  currentTime.Format(time.RFC3339),
	}
}

func buildTextOpsNewsMarkdown(news textOpsNewsResult, currentTime time.Time) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "### 今日 %s 新闻（近24小时）\n\n", escapeTextOpsNewsMarkdown(news.Topic))
	for index, item := range news.Items {
		fmt.Fprintf(&builder, "%d. [%s](%s)\n", index+1, escapeTextOpsNewsMarkdown(item.Title), item.Link)
		details := []string{item.Source}
		if !item.PublishedAt.IsZero() {
			details = append(details, item.PublishedAt.In(currentTime.Location()).Format("01-02 15:04"))
		}
		fmt.Fprintf(&builder, "   - %s\n", strings.Join(details, " · "))
	}
	fmt.Fprintf(
		&builder,
		"\n数据源：%s RSS · 获取时间：%s",
		news.DataSource,
		currentTime.Format("2006-01-02 15:04:05 MST"),
	)
	return builder.String()
}

func escapeTextOpsNewsMarkdown(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return replacer.Replace(strings.TrimSpace(value))
}

func getCachedTextOpsNews(key string, now time.Time) (textOpsNewsResult, bool) {
	textOpsNewsCache.RLock()
	entry, ok := textOpsNewsCache.entries[key]
	textOpsNewsCache.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		return textOpsNewsResult{}, false
	}
	return entry.result, true
}

func cacheTextOpsNews(key string, result textOpsNewsResult, expiresAt time.Time) {
	textOpsNewsCache.Lock()
	textOpsNewsCache.entries[key] = textOpsNewsCacheEntry{result: result, expiresAt: expiresAt}
	textOpsNewsCache.Unlock()
}

func clearTextOpsNewsCache() {
	textOpsNewsCache.Lock()
	textOpsNewsCache.entries = make(map[string]textOpsNewsCacheEntry)
	textOpsNewsCache.Unlock()
}
