package server

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMoegirlAPIURL              = "https://zh.moegirl.org.cn/api.php"
	defaultMoegirlSitemapIndexURL     = "https://zh.moegirl.org.cn/sitemap/sitemap-index-zhmoegirl.xml"
	defaultMoegirlPublicArticleOrigin = "https://zh.moegirl.org.cn"
	moegirlSourceName                 = "萌娘百科 (Moegirlpedia)"
	moegirlLicense                    = "CC BY-NC-SA 3.0 CN"
	moegirlSourcePolicy               = "summary-only with visible attribution; no full-article mirror; no AI training"
	maxMoegirlTitlesPerRequest        = 50
	maxMoegirlPagesPerBuild           = 3000
	defaultMoegirlSitemapLimit        = 50
	maxMoegirlSummaryRunes            = 260
)

type moegirlBuildPublishRequest struct {
	Version        string   `json:"version"`
	Titles         []string `json:"titles"`
	Limit          int      `json:"limit"`
	LLMRecommended []string `json:"llm_recommended"`
}

type moegirlAPIResponse struct {
	Query struct {
		Pages map[string]moegirlAPIPage `json:"pages"`
	} `json:"query"`
}

type moegirlAPIPage struct {
	PageID     int    `json:"pageid"`
	Namespace  int    `json:"ns"`
	Title      string `json:"title"`
	Extract    string `json:"extract"`
	FullURL    string `json:"fullurl"`
	LastRevID  int64  `json:"lastrevid"`
	Touched    string `json:"touched"`
	Missing    string `json:"missing"`
	Categories []struct {
		Title string `json:"title"`
	} `json:"categories"`
}

type sitemapIndexXML struct {
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

type sitemapURLSetXML struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

type moegirlCitationFile struct {
	Source       string            `json:"source"`
	License      string            `json:"license"`
	SourcePolicy string            `json:"source_policy"`
	GeneratedAt  string            `json:"generated_at"`
	Citations    []moegirlCitation `json:"citations"`
}

type moegirlCitation struct {
	ChunkID    string `json:"chunk_id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Source     string `json:"source"`
	License    string `json:"license"`
	RevisionID string `json:"revision_id,omitempty"`
	Touched    string `json:"touched,omitempty"`
}

func (h *Handler) handleBuildAndPublishMoegirlSummary(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if len(h.knowledgePackSigningSeed) == 0 {
		http.Error(w, "knowledge pack signing key is not configured", http.StatusServiceUnavailable)
		return
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/moegirl/build-publish")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var payload moegirlBuildPublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	version, err := safeComponent(payload.Version)
	if err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}

	buildPayload, err := h.moegirlBuildPayload(r.Context(), payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	buildPayload.Version = version
	buildPayload.LLMRecommended = payload.LLMRecommended

	packageBytes, manifest, err := buildKnowledgePack(kbID, version, buildPayload, h.knowledgePackSigningSeed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.storePublishedVersion(kbID, version, manifest, bytes.NewReader(packageBytes)); err != nil {
		writePublishError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":         kbID,
		"version":       version,
		"latest":        true,
		"chunk_count":   len(buildPayload.Chunks),
		"source":        moegirlSourceName,
		"license":       moegirlLicense,
		"source_policy": moegirlSourcePolicy,
	})
}

func (h *Handler) moegirlBuildPayload(ctx context.Context, request moegirlBuildPublishRequest) (buildPublishRequest, error) {
	titles := normalizeMoegirlTitles(request.Titles)
	if len(titles) == 0 {
		discovered, err := h.discoverMoegirlTitles(ctx, moegirlSitemapLimit(request.Limit))
		if err != nil {
			return buildPublishRequest{}, err
		}
		titles = discovered
	}
	if len(titles) == 0 {
		return buildPublishRequest{}, fmt.Errorf("no Moegirl article titles were discovered")
	}
	if len(titles) > maxMoegirlPagesPerBuild {
		titles = titles[:maxMoegirlPagesPerBuild]
	}

	pages, err := h.fetchMoegirlPages(ctx, titles)
	if err != nil {
		return buildPublishRequest{}, err
	}
	if len(pages) == 0 {
		return buildPublishRequest{}, fmt.Errorf("no public Moegirl page summaries were returned")
	}

	chunks := make([]knowledgePackBuildChunk, 0, len(pages))
	prompts := make([]knowledgePackBuildPrompt, 0, len(pages)*2)
	citations := make([]moegirlCitation, 0, len(pages))
	for _, page := range pages {
		chunk := moegirlChunkFromPage(page, h.moegirlPublicArticleOrigin)
		chunks = append(chunks, chunk)
		prompts = append(prompts,
			knowledgePackBuildPrompt{
				ID:       chunk.ChunkID + "-what-is",
				Title:    page.Title + "是什么？",
				Question: page.Title + "是什么？",
			},
			knowledgePackBuildPrompt{
				ID:       chunk.ChunkID + "-summary",
				Title:    "请说明" + page.Title,
				Question: "请说明" + page.Title,
			},
		)
		citations = append(citations, moegirlCitation{
			ChunkID:    chunk.ChunkID,
			Title:      page.Title,
			URL:        moegirlPublicURL(page, h.moegirlPublicArticleOrigin),
			Source:     moegirlSourceName,
			License:    moegirlLicense,
			RevisionID: revisionIDString(page.LastRevID),
			Touched:    page.Touched,
		})
	}

	citationFile := moegirlCitationFile{
		Source:       moegirlSourceName,
		License:      moegirlLicense,
		SourcePolicy: moegirlSourcePolicy,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Citations:    citations,
	}
	citationData, err := json.MarshalIndent(citationFile, "", "  ")
	if err != nil {
		return buildPublishRequest{}, fmt.Errorf("encode Moegirl citations: %w", err)
	}

	return buildPublishRequest{
		Chunks:    chunks,
		Prompts:   prompts,
		Citations: citationData,
	}, nil
}

func (h *Handler) discoverMoegirlTitles(ctx context.Context, limit int) ([]string, error) {
	indexBody, err := h.fetchMoegirlBytes(ctx, h.moegirlSitemapIndexURL)
	if err != nil {
		return nil, err
	}

	var index sitemapIndexXML
	if err := xml.Unmarshal(indexBody, &index); err != nil {
		return nil, fmt.Errorf("decode Moegirl sitemap index: %w", err)
	}

	sitemapURLs := moegirlNamespaceZeroSitemaps(index.Sitemaps)
	if len(sitemapURLs) == 0 {
		var direct sitemapURLSetXML
		if err := xml.Unmarshal(indexBody, &direct); err == nil && len(direct.URLs) > 0 {
			return moegirlTitlesFromURLSet(direct, limit), nil
		}
		return nil, fmt.Errorf("Moegirl sitemap index did not include namespace 0 sitemaps")
	}

	titles := []string{}
	seen := map[string]bool{}
	for _, sitemapURL := range sitemapURLs {
		body, err := h.fetchMoegirlBytes(ctx, sitemapURL)
		if err != nil {
			return nil, err
		}
		var urlSet sitemapURLSetXML
		if err := xml.Unmarshal(body, &urlSet); err != nil {
			return nil, fmt.Errorf("decode Moegirl sitemap %s: %w", sitemapURL, err)
		}
		for _, title := range moegirlTitlesFromURLSet(urlSet, limit) {
			if seen[title] {
				continue
			}
			seen[title] = true
			titles = append(titles, title)
			if len(titles) >= limit {
				return titles, nil
			}
		}
	}
	return titles, nil
}

func (h *Handler) fetchMoegirlBytes(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build Moegirl request: %w", err)
	}
	request.Header.Set("User-Agent", "yi-flow-knowledge-pack-builder/0.1")

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch Moegirl source %s: %w", rawURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch Moegirl source %s: status %d", rawURL, response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read Moegirl source %s: %w", rawURL, err)
	}
	return body, nil
}

func moegirlNamespaceZeroSitemaps(sitemaps []struct {
	Loc string `xml:"loc"`
}) []string {
	all := []string{}
	namespaceZero := []string{}
	for _, sitemap := range sitemaps {
		loc := strings.TrimSpace(sitemap.Loc)
		if loc == "" {
			continue
		}
		all = append(all, loc)
		if strings.Contains(loc, "NS_0") {
			namespaceZero = append(namespaceZero, loc)
		}
	}
	if len(namespaceZero) > 0 {
		return namespaceZero
	}
	return all
}

func moegirlTitlesFromURLSet(urlSet sitemapURLSetXML, limit int) []string {
	titles := []string{}
	seen := map[string]bool{}
	for _, item := range urlSet.URLs {
		title := titleFromMoegirlURL(item.Loc)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		titles = append(titles, title)
		if len(titles) >= limit {
			return titles
		}
	}
	return titles
}

func titleFromMoegirlURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	title, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return ""
	}
	parts := strings.Split(title, "/")
	if len(parts) > 1 && strings.HasPrefix(parts[0], "zh") {
		title = strings.Join(parts[1:], "/")
	}
	title = strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
	if title == "" || strings.Contains(title, ":") {
		return ""
	}
	return title
}

func moegirlSitemapLimit(limit int) int {
	if limit <= 0 {
		return defaultMoegirlSitemapLimit
	}
	if limit > maxMoegirlPagesPerBuild {
		return maxMoegirlPagesPerBuild
	}
	return limit
}

func (h *Handler) fetchMoegirlPages(ctx context.Context, titles []string) ([]moegirlAPIPage, error) {
	pages := []moegirlAPIPage{}
	for start := 0; start < len(titles); start += maxMoegirlTitlesPerRequest {
		end := start + maxMoegirlTitlesPerRequest
		if end > len(titles) {
			end = len(titles)
		}
		batch, err := h.fetchMoegirlPageBatch(ctx, titles[start:end])
		if err != nil {
			return nil, err
		}
		pages = append(pages, batch...)
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Title < pages[j].Title
	})
	return pages, nil
}

func (h *Handler) fetchMoegirlPageBatch(ctx context.Context, titles []string) ([]moegirlAPIPage, error) {
	apiURL, err := url.Parse(h.moegirlAPIURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Moegirl API URL: %w", err)
	}
	query := apiURL.Query()
	query.Set("action", "query")
	query.Set("format", "json")
	query.Set("prop", "extracts|info|categories")
	query.Set("inprop", "url")
	query.Set("exintro", "1")
	query.Set("explaintext", "1")
	query.Set("cllimit", "20")
	query.Set("titles", strings.Join(titles, "|"))
	apiURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build Moegirl API request: %w", err)
	}
	request.Header.Set("User-Agent", "yi-flow-knowledge-pack-builder/0.1")

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch Moegirl page summaries: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch Moegirl page summaries: status %d", response.StatusCode)
	}

	var decoded moegirlAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Moegirl API response: %w", err)
	}

	pages := make([]moegirlAPIPage, 0, len(decoded.Query.Pages))
	for _, page := range decoded.Query.Pages {
		page.Title = strings.TrimSpace(page.Title)
		page.Extract = strings.TrimSpace(page.Extract)
		if page.Missing != "" || page.Namespace != 0 || page.Title == "" || page.Extract == "" {
			continue
		}
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Title < pages[j].Title
	})
	return pages, nil
}

func normalizeMoegirlTitles(titles []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		result = append(result, title)
	}
	return result
}

func moegirlChunkFromPage(page moegirlAPIPage, articleOrigin string) knowledgePackBuildChunk {
	categories := moegirlCategoryNames(page)
	contentParts := []string{
		"【摘要】" + truncateRunes(page.Extract, maxMoegirlSummaryRunes),
	}
	if len(categories) > 0 {
		contentParts = append(contentParts, "【分类】"+strings.Join(categories, "、"))
	}
	contentParts = append(contentParts,
		"【来源】"+moegirlSourceName+"："+moegirlPublicURL(page, articleOrigin),
		"【许可】"+moegirlLicense+"。本知识包仅保存公开条目的高层摘要与引用，不复现完整条目，不用于 AI 训练。",
	)

	return knowledgePackBuildChunk{
		ChunkID: moegirlChunkID(page),
		Title:   page.Title,
		Path:    "moegirl/summary/" + slugComponent(page.Title),
		Source:  moegirlSourceName,
		Content: strings.Join(contentParts, "\n"),
	}
}

func moegirlCategoryNames(page moegirlAPIPage) []string {
	categories := []string{}
	for _, category := range page.Categories {
		title := strings.TrimPrefix(strings.TrimSpace(category.Title), "Category:")
		if title == "" {
			continue
		}
		categories = append(categories, title)
		if len(categories) >= 8 {
			break
		}
	}
	return categories
}

func moegirlChunkID(page moegirlAPIPage) string {
	if page.PageID > 0 {
		return "moegirl-page-" + strconv.Itoa(page.PageID)
	}
	return "moegirl-title-" + slugComponent(page.Title)
}

func moegirlPublicURL(page moegirlAPIPage, articleOrigin string) string {
	if strings.TrimSpace(page.FullURL) != "" {
		return strings.TrimSpace(page.FullURL)
	}
	return strings.TrimRight(articleOrigin, "/") + "/" + url.PathEscape(strings.ReplaceAll(page.Title, " ", "_"))
}

func revisionIDString(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

var slugUnsafePattern = regexp.MustCompile(`[^0-9A-Za-z._~\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}-]+`)

func slugComponent(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, " ", "_"))
	value = slugUnsafePattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "untitled"
	}
	return value
}
