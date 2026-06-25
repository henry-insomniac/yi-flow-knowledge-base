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
	PageID     int              `json:"pageid"`
	Namespace  int              `json:"ns"`
	Title      string           `json:"title"`
	Extract    string           `json:"extract"`
	FullURL    string           `json:"fullurl"`
	LastRevID  int64            `json:"lastrevid"`
	Touched    string           `json:"touched"`
	Missing    *json.RawMessage `json:"missing"`
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
	Source        string                      `json:"source"`
	License       string                      `json:"license"`
	SourcePolicy  string                      `json:"source_policy"`
	GeneratedAt   string                      `json:"generated_at"`
	CrawlManifest []moegirlCrawlManifestEntry `json:"crawl_manifest"`
	Citations     []moegirlCitation           `json:"citations"`
}

type moegirlCitation struct {
	ChunkID    string `json:"chunk_id"`
	Title      string `json:"title"`
	FAQType    string `json:"faq_type,omitempty"`
	URL        string `json:"url"`
	Source     string `json:"source"`
	License    string `json:"license"`
	RevisionID string `json:"revision_id,omitempty"`
	Touched    string `json:"touched,omitempty"`
}

type moegirlCrawlManifestEntry struct {
	KBID         string   `json:"kb_id"`
	SourceName   string   `json:"source_name"`
	SourceURL    string   `json:"source_url"`
	CanonicalURL string   `json:"canonical_url"`
	PageID       int      `json:"page_id"`
	RevisionID   string   `json:"revision_id"`
	Touched      string   `json:"touched"`
	License      string   `json:"license"`
	SourcePolicy string   `json:"source_policy"`
	Categories   []string `json:"categories"`
	FetchedAt    string   `json:"fetched_at"`
}

type moegirlCrawlReport struct {
	RequestedTitles  int                  `json:"requested_titles"`
	UniqueTitles     int                  `json:"unique_titles"`
	DuplicateTitles  int                  `json:"duplicate_titles"`
	DiscoveredTitles int                  `json:"discovered_titles"`
	APIFetchRequests int                  `json:"api_fetch_requests"`
	MaxBatchSize     int                  `json:"max_batch_size"`
	CacheHits        int                  `json:"cache_hits"`
	AcceptedPages    int                  `json:"accepted_pages"`
	SkippedPages     []moegirlSkippedPage `json:"skipped_pages"`
}

type moegirlBuildReport struct {
	FetchedPages         int     `json:"fetched_pages"`
	SkippedPages         int     `json:"skipped_pages"`
	ChunkCount           int     `json:"chunk_count"`
	CitationCount        int     `json:"citation_count"`
	DuplicateChunkRate   float64 `json:"duplicate_chunk_rate"`
	MissingMetadataCount int     `json:"missing_metadata_count"`
}

type moegirlSkippedPage struct {
	Title      string `json:"title"`
	Reason     string `json:"reason"`
	PageID     int    `json:"page_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
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
	if !isMoegirlKnowledgePackID(kbID) {
		http.Error(w, fmt.Sprintf("kb_id %q cannot use moegirl builder; expected moegirl-* knowledge pack", kbID), http.StatusBadRequest)
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

	buildPayload, crawlReport, err := h.moegirlBuildPayload(r.Context(), kbID, payload)
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
	if err := auditKnowledgePackBeforePublish(manifest, packageBytes); err != nil {
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
		"crawl_report":  crawlReport,
		"build_report":  moegirlBuildReportFromPayload(buildPayload, crawlReport),
	})
}

func isMoegirlKnowledgePackID(kbID string) bool {
	return strings.HasPrefix(strings.TrimSpace(kbID), "moegirl-")
}

func (h *Handler) moegirlBuildPayload(ctx context.Context, kbID string, request moegirlBuildPublishRequest) (buildPublishRequest, moegirlCrawlReport, error) {
	titles, duplicateTitles := normalizeMoegirlTitlesWithReport(request.Titles)
	crawlReport := moegirlCrawlReport{
		RequestedTitles: len(request.Titles),
		DuplicateTitles: duplicateTitles,
	}
	if len(titles) == 0 {
		discovered, err := h.discoverMoegirlTitles(ctx, moegirlSitemapLimit(request.Limit))
		if err != nil {
			return buildPublishRequest{}, crawlReport, err
		}
		titles = discovered
		crawlReport.DiscoveredTitles = len(discovered)
	}
	if len(titles) == 0 {
		return buildPublishRequest{}, crawlReport, fmt.Errorf("no Moegirl article titles were discovered")
	}
	if len(titles) > maxMoegirlPagesPerBuild {
		for _, title := range titles[maxMoegirlPagesPerBuild:] {
			crawlReport.SkippedPages = append(crawlReport.SkippedPages, moegirlSkippedPage{Title: title, Reason: "build_limit"})
		}
		titles = titles[:maxMoegirlPagesPerBuild]
	}
	crawlReport.UniqueTitles = len(titles)

	pages, fetchReport, err := h.fetchMoegirlPages(ctx, titles)
	if err != nil {
		return buildPublishRequest{}, crawlReport, err
	}
	crawlReport.APIFetchRequests += fetchReport.APIFetchRequests
	crawlReport.MaxBatchSize = max(crawlReport.MaxBatchSize, fetchReport.MaxBatchSize)
	crawlReport.CacheHits += fetchReport.CacheHits
	crawlReport.SkippedPages = append(crawlReport.SkippedPages, fetchReport.SkippedPages...)
	crawlReport.AcceptedPages = len(pages)
	sortMoegirlSkippedPages(crawlReport.SkippedPages)
	if len(pages) == 0 {
		return buildPublishRequest{}, crawlReport, fmt.Errorf("no public Moegirl page summaries were returned")
	}

	chunks := make([]knowledgePackBuildChunk, 0, len(pages)*3)
	prompts := make([]knowledgePackBuildPrompt, 0, len(pages)*3)
	citations := make([]moegirlCitation, 0, len(pages)*3)
	crawlManifest := make([]moegirlCrawlManifestEntry, 0, len(pages))
	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	for _, page := range pages {
		pageChunks := moegirlFAQChunksFromPage(page, h.moegirlPublicArticleOrigin)
		publicURL := moegirlPublicURL(page, h.moegirlPublicArticleOrigin)
		categories := moegirlCategoryNames(page)
		chunks = append(chunks, pageChunks...)
		prompts = append(prompts, moegirlPromptsForFAQChunks(page, pageChunks)...)
		for _, chunk := range pageChunks {
			citations = append(citations, moegirlCitation{
				ChunkID:    chunk.ChunkID,
				Title:      page.Title,
				FAQType:    moegirlFAQTypeFromChunkID(chunk.ChunkID),
				URL:        publicURL,
				Source:     moegirlSourceName,
				License:    moegirlLicense,
				RevisionID: revisionIDString(page.LastRevID),
				Touched:    page.Touched,
			})
		}
		crawlManifest = append(crawlManifest, moegirlCrawlManifestEntry{
			KBID:         kbID,
			SourceName:   moegirlSourceName,
			SourceURL:    h.moegirlAPIURL,
			CanonicalURL: publicURL,
			PageID:       page.PageID,
			RevisionID:   revisionIDString(page.LastRevID),
			Touched:      page.Touched,
			License:      moegirlLicense,
			SourcePolicy: moegirlSourcePolicy,
			Categories:   categories,
			FetchedAt:    fetchedAt,
		})
	}

	citationFile := moegirlCitationFile{
		Source:        moegirlSourceName,
		License:       moegirlLicense,
		SourcePolicy:  moegirlSourcePolicy,
		GeneratedAt:   fetchedAt,
		CrawlManifest: crawlManifest,
		Citations:     citations,
	}
	citationData, err := json.MarshalIndent(citationFile, "", "  ")
	if err != nil {
		return buildPublishRequest{}, crawlReport, fmt.Errorf("encode Moegirl citations: %w", err)
	}

	return buildPublishRequest{
		Chunks:    chunks,
		Prompts:   prompts,
		Citations: citationData,
	}, crawlReport, nil
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

func (h *Handler) fetchMoegirlPages(ctx context.Context, titles []string) ([]moegirlAPIPage, moegirlCrawlReport, error) {
	report := moegirlCrawlReport{}
	pages := []moegirlAPIPage{}
	seenRevisions := map[string]bool{}
	for start := 0; start < len(titles); start += maxMoegirlTitlesPerRequest {
		end := start + maxMoegirlTitlesPerRequest
		if end > len(titles) {
			end = len(titles)
		}
		batchTitles := titles[start:end]
		report.APIFetchRequests++
		report.MaxBatchSize = max(report.MaxBatchSize, len(batchTitles))
		batch, skipped, err := h.fetchMoegirlPageBatch(ctx, batchTitles)
		if err != nil {
			return nil, report, err
		}
		report.SkippedPages = append(report.SkippedPages, skipped...)
		for _, page := range batch {
			cacheKey := moegirlPageRevisionCacheKey(page)
			if cacheKey != "" && seenRevisions[cacheKey] {
				report.CacheHits++
				report.SkippedPages = append(report.SkippedPages, moegirlSkippedPage{
					Title:      page.Title,
					Reason:     "duplicate_revision",
					PageID:     page.PageID,
					RevisionID: revisionIDString(page.LastRevID),
				})
				continue
			}
			if cacheKey != "" {
				seenRevisions[cacheKey] = true
			}
			pages = append(pages, page)
		}
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Title < pages[j].Title
	})
	sortMoegirlSkippedPages(report.SkippedPages)
	return pages, report, nil
}

func (h *Handler) fetchMoegirlPageBatch(ctx context.Context, titles []string) ([]moegirlAPIPage, []moegirlSkippedPage, error) {
	apiURL, err := url.Parse(h.moegirlAPIURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid Moegirl API URL: %w", err)
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
		return nil, nil, fmt.Errorf("build Moegirl API request: %w", err)
	}
	request.Header.Set("User-Agent", "yi-flow-knowledge-pack-builder/0.1")

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch Moegirl page summaries: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("fetch Moegirl page summaries: status %d", response.StatusCode)
	}

	var decoded moegirlAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, nil, fmt.Errorf("decode Moegirl API response: %w", err)
	}

	pages := make([]moegirlAPIPage, 0, len(decoded.Query.Pages))
	skipped := make([]moegirlSkippedPage, 0)
	for _, page := range decoded.Query.Pages {
		page.Title = strings.TrimSpace(page.Title)
		page.Extract = strings.TrimSpace(page.Extract)
		if page.Title == "" {
			skipped = append(skipped, moegirlSkippedPage{Reason: "missing_title", PageID: page.PageID, RevisionID: revisionIDString(page.LastRevID)})
			continue
		}
		if page.Missing != nil {
			skipped = append(skipped, moegirlSkippedPage{Title: page.Title, Reason: "missing", PageID: page.PageID, RevisionID: revisionIDString(page.LastRevID)})
			continue
		}
		if page.Namespace != 0 {
			skipped = append(skipped, moegirlSkippedPage{Title: page.Title, Reason: "non_article_namespace", PageID: page.PageID, RevisionID: revisionIDString(page.LastRevID)})
			continue
		}
		if page.Extract == "" {
			skipped = append(skipped, moegirlSkippedPage{Title: page.Title, Reason: "empty_extract", PageID: page.PageID, RevisionID: revisionIDString(page.LastRevID)})
			continue
		}
		if moegirlExtractTooShort(page.Extract) {
			skipped = append(skipped, moegirlSkippedPage{Title: page.Title, Reason: "too_short_extract", PageID: page.PageID, RevisionID: revisionIDString(page.LastRevID)})
			continue
		}
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Title < pages[j].Title
	})
	sortMoegirlSkippedPages(skipped)
	return pages, skipped, nil
}

func normalizeMoegirlTitles(titles []string) []string {
	normalized, _ := normalizeMoegirlTitlesWithReport(titles)
	return normalized
}

func normalizeMoegirlTitlesWithReport(titles []string) ([]string, int) {
	seen := map[string]bool{}
	result := []string{}
	duplicates := 0
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		if seen[title] {
			duplicates++
			continue
		}
		seen[title] = true
		result = append(result, title)
	}
	return result, duplicates
}

func moegirlPageRevisionCacheKey(page moegirlAPIPage) string {
	if page.PageID <= 0 || page.LastRevID <= 0 {
		return ""
	}
	return strconv.Itoa(page.PageID) + ":" + revisionIDString(page.LastRevID)
}

func moegirlExtractTooShort(extract string) bool {
	return len([]rune(strings.TrimSpace(extract))) < 8
}

func sortMoegirlSkippedPages(skipped []moegirlSkippedPage) {
	sort.Slice(skipped, func(i, j int) bool {
		if skipped[i].Reason != skipped[j].Reason {
			return skipped[i].Reason < skipped[j].Reason
		}
		if skipped[i].Title != skipped[j].Title {
			return skipped[i].Title < skipped[j].Title
		}
		return skipped[i].RevisionID < skipped[j].RevisionID
	})
}

func moegirlFAQChunksFromPage(page moegirlAPIPage, articleOrigin string) []knowledgePackBuildChunk {
	categories := moegirlCategoryNames(page)
	publicURL := moegirlPublicURL(page, articleOrigin)
	summary := truncateRunes(page.Extract, maxMoegirlSummaryRunes)
	categoryText := "无明确分类"
	if len(categories) > 0 {
		categoryText = strings.Join(categories, "、")
	}

	return []knowledgePackBuildChunk{
		moegirlFAQChunk(page, "faq_overview", "是什么", []string{
			"【问题】" + page.Title + "是什么？",
			"【回答依据】" + summary,
			"【适用意图】实体概览、是什么、简介。",
			"【分类】" + categoryText,
			moegirlSourceLine(publicURL),
			moegirlLicenseLine(),
		}),
		moegirlFAQChunk(page, "faq_identity", "身份来源", []string{
			"【问题】" + page.Title + "来自哪里，如何定位？",
			"【实体】" + page.Title,
			"【页面ID】" + strconv.Itoa(page.PageID),
			"【修订】" + revisionIDString(page.LastRevID),
			"【分类】" + categoryText,
			"【回答边界】只根据该页面摘要和分类说明身份，不补充页面外设定。",
			moegirlSourceLine(publicURL),
			moegirlLicenseLine(),
		}),
		moegirlFAQChunk(page, "faq_facts", "核心事实", []string{
			"【问题】" + page.Title + "有哪些核心事实？",
			"【事实依据】" + summary,
			"【可回答内容】名称、基本介绍、页面分类中出现的公开信息。",
			"【不可回答内容】未在摘要或分类中出现的角色关系、剧情细节或完整设定列表。",
			"【分类】" + categoryText,
			moegirlSourceLine(publicURL),
			moegirlLicenseLine(),
		}),
	}
}

func moegirlFAQChunk(page moegirlAPIPage, faqType string, pathName string, contentParts []string) knowledgePackBuildChunk {
	content := append([]string{"【FAQ类型】" + faqType}, contentParts...)
	content = append(content, "【引用】chunk_id="+moegirlFAQChunkID(page, faqType))
	return knowledgePackBuildChunk{
		ChunkID: moegirlFAQChunkID(page, faqType),
		Title:   page.Title + " · " + faqType,
		Path:    "moegirl/faq/" + slugComponent(page.Title) + "/" + slugComponent(pathName),
		Source:  moegirlSourceName,
		Content: strings.Join(content, "\n"),
	}
}

func moegirlSourceLine(publicURL string) string {
	return "【来源】" + moegirlSourceName + "：" + publicURL
}

func moegirlLicenseLine() string {
	return "【许可】" + moegirlLicense + "。本知识包仅保存公开条目的高层摘要与 FAQ 引用，不复现完整条目，不用于 AI 训练。"
}

func moegirlPromptsForFAQChunks(page moegirlAPIPage, chunks []knowledgePackBuildChunk) []knowledgePackBuildPrompt {
	prompts := make([]knowledgePackBuildPrompt, 0, len(chunks))
	questionsByType := map[string]string{
		"faq_overview": page.Title + "是什么？",
		"faq_identity": page.Title + "来自哪里？",
		"faq_facts":    page.Title + "有哪些核心事实？",
	}
	for _, chunk := range chunks {
		faqType := moegirlFAQTypeFromChunkID(chunk.ChunkID)
		question := questionsByType[faqType]
		if question == "" {
			question = "请说明" + page.Title
		}
		prompts = append(prompts, knowledgePackBuildPrompt{
			ID:       chunk.ChunkID + "-question",
			Title:    question,
			Question: question,
		})
	}
	return prompts
}

func moegirlFAQTypeFromChunkID(chunkID string) string {
	for _, faqType := range []string{"faq_overview", "faq_identity", "faq_facts", "faq_relation", "faq_disambiguation"} {
		if strings.HasSuffix(chunkID, "-"+strings.ReplaceAll(faqType, "_", "-")) {
			return faqType
		}
	}
	return ""
}

func moegirlFAQChunkID(page moegirlAPIPage, faqType string) string {
	return moegirlChunkID(page) + "-" + strings.ReplaceAll(faqType, "_", "-")
}

func moegirlBuildReportFromPayload(payload buildPublishRequest, crawlReport moegirlCrawlReport) moegirlBuildReport {
	citationCount, missingMetadataCount := moegirlCitationMetrics(payload.Citations)
	return moegirlBuildReport{
		FetchedPages:         crawlReport.AcceptedPages,
		SkippedPages:         len(crawlReport.SkippedPages),
		ChunkCount:           len(payload.Chunks),
		CitationCount:        citationCount,
		DuplicateChunkRate:   duplicateChunkRate(payload.Chunks),
		MissingMetadataCount: missingMetadataCount,
	}
}

func moegirlCitationMetrics(raw json.RawMessage) (int, int) {
	var decoded moegirlCitationFile
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return 0, 1
	}
	missing := 0
	for _, citation := range decoded.Citations {
		if strings.TrimSpace(citation.ChunkID) == "" ||
			strings.TrimSpace(citation.Source) == "" ||
			strings.TrimSpace(citation.License) == "" ||
			strings.TrimSpace(citation.URL) == "" ||
			strings.TrimSpace(citation.RevisionID) == "" ||
			strings.TrimSpace(citation.FAQType) == "" {
			missing++
		}
	}
	for _, row := range decoded.CrawlManifest {
		if strings.TrimSpace(row.KBID) == "" ||
			strings.TrimSpace(row.SourceName) == "" ||
			strings.TrimSpace(row.SourceURL) == "" ||
			strings.TrimSpace(row.CanonicalURL) == "" ||
			row.PageID == 0 ||
			strings.TrimSpace(row.RevisionID) == "" ||
			strings.TrimSpace(row.Touched) == "" ||
			strings.TrimSpace(row.License) == "" ||
			strings.TrimSpace(row.SourcePolicy) == "" ||
			len(row.Categories) == 0 ||
			strings.TrimSpace(row.FetchedAt) == "" {
			missing++
		}
	}
	return len(decoded.Citations), missing
}

func duplicateChunkRate(chunks []knowledgePackBuildChunk) float64 {
	if len(chunks) == 0 {
		return 0
	}
	seen := map[string]bool{}
	duplicates := 0
	for _, chunk := range chunks {
		chunkID := strings.TrimSpace(chunk.ChunkID)
		if chunkID == "" {
			continue
		}
		if seen[chunkID] {
			duplicates++
			continue
		}
		seen[chunkID] = true
	}
	return float64(duplicates) / float64(len(chunks))
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
