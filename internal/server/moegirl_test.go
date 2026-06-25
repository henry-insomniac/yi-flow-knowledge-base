package server_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yi-flow/knowledge-base/internal/server"
)

func TestAdminCanBuildMoegirlSummaryKnowledgePackFromPageSummaries(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeMoegirlSource(t)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.101",
	  "titles": ["初音未来"]
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl build publish status=%d body=%s", response.Code, response.Body.String())
	}

	var publishResult struct {
		KBID         string `json:"kb_id"`
		Version      string `json:"version"`
		Latest       bool   `json:"latest"`
		ChunkCount   int    `json:"chunk_count"`
		SourcePolicy string `json:"source_policy"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &publishResult); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if publishResult.KBID != "moegirl-acgn-summary" || publishResult.Version != "2026.06.22.101" || !publishResult.Latest {
		t.Fatalf("publish result = %+v", publishResult)
	}
	if publishResult.ChunkCount != 3 {
		t.Fatalf("chunk_count=%d", publishResult.ChunkCount)
	}
	if !strings.Contains(publishResult.SourcePolicy, "summary") {
		t.Fatalf("source policy should describe summary use: %+v", publishResult)
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/latest/preview?limit=3", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{
		"初音未来",
		"Crypton Future Media",
		"萌娘百科 (Moegirlpedia)",
		"CC BY-NC-SA 3.0 CN",
		"faq_overview",
		"faq_identity",
		"faq_facts",
	} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing %q: %s", expected, previewBody)
		}
	}
	if strings.Contains(previewBody, "知识包里关于【摘要】") {
		t.Fatalf("preview suggested question should not expose chunk metadata labels: %s", previewBody)
	}
	var preview struct {
		Chunks []struct {
			ChunkID    string `json:"chunk_id"`
			FAQType    string `json:"faq_type"`
			SourceURL  string `json:"source_url"`
			License    string `json:"license"`
			RevisionID string `json:"revision_id"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(preview.Chunks) != 3 {
		t.Fatalf("preview chunks=%d body=%s", len(preview.Chunks), previewResponse.Body.String())
	}
	for _, chunk := range preview.Chunks {
		if chunk.ChunkID == "" ||
			chunk.FAQType == "" ||
			chunk.SourceURL != "https://zh.moegirl.org.cn/初音未来" ||
			chunk.License != "CC BY-NC-SA 3.0 CN" ||
			chunk.RevisionID != "8535826" {
			t.Fatalf("preview chunk metadata = %+v", chunk)
		}
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/versions/2026.06.22.101/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	citations := readZipEntry(t, packageResponse.Body.Bytes(), "citations.json")
	prompts := readZipEntry(t, packageResponse.Body.Bytes(), "prompts.json")
	var promptFile struct {
		Prompts []struct {
			Question string `json:"question"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(prompts, &promptFile); err != nil {
		t.Fatalf("decode prompts: %v", err)
	}
	if len(promptFile.Prompts) != 3 {
		t.Fatalf("prompt count=%d prompts=%s", len(promptFile.Prompts), string(prompts))
	}
	var citationCountFile struct {
		Citations []struct {
			ChunkID string `json:"chunk_id"`
		} `json:"citations"`
	}
	if err := json.Unmarshal(citations, &citationCountFile); err != nil {
		t.Fatalf("decode citations: %v", err)
	}
	if len(citationCountFile.Citations) != 3 {
		t.Fatalf("citation count=%d citations=%s", len(citationCountFile.Citations), string(citations))
	}
	for _, expected := range []string{
		`"license": "CC BY-NC-SA 3.0 CN"`,
		`"source": "萌娘百科 (Moegirlpedia)"`,
		`"url": "https://zh.moegirl.org.cn/初音未来"`,
		`"revision_id": "8535826"`,
		`"faq_type": "faq_overview"`,
		`"faq_type": "faq_identity"`,
		`"faq_type": "faq_facts"`,
	} {
		if !strings.Contains(string(citations), expected) {
			t.Fatalf("citations missing %q: %s", expected, string(citations))
		}
	}

	var citationFile struct {
		CrawlManifest []struct {
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
		} `json:"crawl_manifest"`
	}
	if err := json.Unmarshal(citations, &citationFile); err != nil {
		t.Fatalf("decode citations crawl manifest: %v", err)
	}
	if len(citationFile.CrawlManifest) != 1 {
		t.Fatalf("crawl manifest len=%d citations=%s", len(citationFile.CrawlManifest), string(citations))
	}
	row := citationFile.CrawlManifest[0]
	if row.KBID != "moegirl-acgn-summary" ||
		row.SourceName != "萌娘百科 (Moegirlpedia)" ||
		row.SourceURL == "" ||
		row.CanonicalURL != "https://zh.moegirl.org.cn/初音未来" ||
		row.PageID != 1399 ||
		row.RevisionID != "8535826" ||
		row.Touched == "" ||
		row.License != "CC BY-NC-SA 3.0 CN" ||
		row.SourcePolicy == "" ||
		len(row.Categories) == 0 ||
		row.FetchedAt == "" {
		t.Fatalf("crawl manifest row = %+v", row)
	}
}

func TestAdminRejectsMoegirlBuildPublishOutsideMoegirlNamespace(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeMoegirlSource(t)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.101",
	  "titles": ["初音未来"]
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/moegirl/build-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("moegirl cross-publish status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"yi-flow-core", "moegirl"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("rejection should mention %q: %s", expected, response.Body.String())
		}
	}
}

func TestAdminCanBuildMoegirlSummaryKnowledgePackFromSitemapLimit(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeMoegirlSource(t)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.102",
	  "limit": 2
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl sitemap build publish status=%d body=%s", response.Code, response.Body.String())
	}

	var publishResult struct {
		ChunkCount int `json:"chunk_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &publishResult); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if publishResult.ChunkCount != 6 {
		t.Fatalf("chunk_count=%d body=%s", publishResult.ChunkCount, response.Body.String())
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/latest/preview?limit=6", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{"初音未来", "东方Project"} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing sitemap-discovered title %q: %s", expected, previewBody)
		}
	}
}

func TestAdminMoegirlBuildReportsBoundedFetchSkipsAndCache(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeLargeMoegirlSource(t, 55)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	titles := make([]string, 0, 61)
	for index := 1; index <= 55; index++ {
		titles = append(titles, fmt.Sprintf("测试角色%03d", index))
	}
	titles = append(titles,
		"测试角色001",
		"测试角色001别名",
		"缺失页面",
		"空摘要页面",
		"非正文页面",
		"短摘要页面",
	)
	body, err := json.Marshal(map[string]any{
		"version": "2026.06.22.103",
		"titles":  titles,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl bounded build status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		ChunkCount  int `json:"chunk_count"`
		CrawlReport struct {
			RequestedTitles  int `json:"requested_titles"`
			UniqueTitles     int `json:"unique_titles"`
			DuplicateTitles  int `json:"duplicate_titles"`
			APIFetchRequests int `json:"api_fetch_requests"`
			MaxBatchSize     int `json:"max_batch_size"`
			CacheHits        int `json:"cache_hits"`
			AcceptedPages    int `json:"accepted_pages"`
			SkippedPages     []struct {
				Title  string `json:"title"`
				Reason string `json:"reason"`
			} `json:"skipped_pages"`
		} `json:"crawl_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode bounded build response: %v", err)
	}
	if decoded.ChunkCount != 165 || decoded.CrawlReport.AcceptedPages != 55 {
		t.Fatalf("accepted count = chunks:%d report:%+v", decoded.ChunkCount, decoded.CrawlReport)
	}
	if decoded.CrawlReport.RequestedTitles != 61 ||
		decoded.CrawlReport.UniqueTitles != 60 ||
		decoded.CrawlReport.DuplicateTitles != 1 {
		t.Fatalf("title counts = %+v", decoded.CrawlReport)
	}
	if decoded.CrawlReport.APIFetchRequests != 3 || decoded.CrawlReport.MaxBatchSize != 20 {
		t.Fatalf("batch report = %+v", decoded.CrawlReport)
	}
	if decoded.CrawlReport.CacheHits != 1 {
		t.Fatalf("cache hits = %+v", decoded.CrawlReport)
	}
	reasons := map[string]bool{}
	for _, skipped := range decoded.CrawlReport.SkippedPages {
		reasons[skipped.Reason] = true
	}
	for _, reason := range []string{"missing", "empty_extract", "non_article_namespace", "too_short_extract", "duplicate_revision"} {
		if !reasons[reason] {
			t.Fatalf("missing skipped reason %q in %+v", reason, decoded.CrawlReport.SkippedPages)
		}
	}
}

func TestAdminMoegirlBuildKeepsAuditMetadataForUncategorizedPages(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeLargeMoegirlSource(t, 1)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish", bytes.NewBufferString(`{
	  "version": "2026.06.25.uncategorized",
	  "titles": ["测试角色001"]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("uncategorized build status=%d body=%s", response.Code, response.Body.String())
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/versions/2026.06.25.uncategorized/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	citations := readZipEntry(t, packageResponse.Body.Bytes(), "citations.json")
	var citationFile struct {
		CrawlManifest []struct {
			Categories []string `json:"categories"`
		} `json:"crawl_manifest"`
	}
	if err := json.Unmarshal(citations, &citationFile); err != nil {
		t.Fatalf("decode citations: %v", err)
	}
	if len(citationFile.CrawlManifest) != 1 || len(citationFile.CrawlManifest[0].Categories) != 1 || citationFile.CrawlManifest[0].Categories[0] != "uncategorized" {
		t.Fatalf("crawl manifest categories = %+v", citationFile.CrawlManifest)
	}
}

func TestAdminMoegirlBuildHandlesThreeHundredPageMVPWithBoundedBatches(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeLargeMoegirlSource(t, 300)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		KnowledgePackSigningSeed:   privateKey.Seed(),
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	titles := make([]string, 0, 300)
	for index := 1; index <= 300; index++ {
		titles = append(titles, fmt.Sprintf("测试角色%03d", index))
	}
	body, err := json.Marshal(map[string]any{
		"version": "2026.06.22.300",
		"titles":  titles,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-summary/moegirl/build-publish", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl 300-page build status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		ChunkCount  int `json:"chunk_count"`
		CrawlReport struct {
			UniqueTitles     int `json:"unique_titles"`
			APIFetchRequests int `json:"api_fetch_requests"`
			MaxBatchSize     int `json:"max_batch_size"`
			AcceptedPages    int `json:"accepted_pages"`
		} `json:"crawl_report"`
		BuildReport struct {
			FetchedPages         int     `json:"fetched_pages"`
			SkippedPages         int     `json:"skipped_pages"`
			ChunkCount           int     `json:"chunk_count"`
			CitationCount        int     `json:"citation_count"`
			DuplicateChunkRate   float64 `json:"duplicate_chunk_rate"`
			MissingMetadataCount int     `json:"missing_metadata_count"`
		} `json:"build_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode 300-page response: %v", err)
	}
	if decoded.ChunkCount != 900 ||
		decoded.CrawlReport.UniqueTitles != 300 ||
		decoded.CrawlReport.AcceptedPages != 300 ||
		decoded.CrawlReport.APIFetchRequests != 15 ||
		decoded.CrawlReport.MaxBatchSize != 20 {
		t.Fatalf("300-page crawl report = %+v chunk_count=%d", decoded.CrawlReport, decoded.ChunkCount)
	}
	if decoded.BuildReport.FetchedPages != 300 ||
		decoded.BuildReport.SkippedPages != 0 ||
		decoded.BuildReport.ChunkCount != 900 ||
		decoded.BuildReport.CitationCount != 900 ||
		decoded.BuildReport.DuplicateChunkRate != 0 ||
		decoded.BuildReport.MissingMetadataCount != 0 {
		t.Fatalf("300-page build report = %+v", decoded.BuildReport)
	}
}

func fakeMoegirlSource(t *testing.T) *httptest.Server {
	t.Helper()

	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			if !strings.Contains(r.URL.Query().Get("titles"), "初音未来") {
				http.Error(w, "unexpected titles query: "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			includeTouhou := strings.Contains(r.URL.Query().Get("titles"), "东方Project")
			touhouPage := ""
			if includeTouhou {
				touhouPage = `,
			      "236": {
			        "pageid": 236,
			        "ns": 0,
			        "title": "东方Project",
			        "extract": "东方Project 是由 ZUN 创作的一系列弹幕射击游戏及其衍生作品。",
			        "fullurl": "https://zh.moegirl.org.cn/东方Project",
			        "lastrevid": 8123456,
			        "touched": "2026-06-21T12:00:00Z",
			        "categories": [
			          {"ns": 14, "title": "Category:东方Project"},
			          {"ns": 14, "title": "Category:弹幕射击游戏"}
			        ]
			      }`
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{
			  "batchcomplete": "",
			  "query": {
			    "pages": {
			      "1399": {
			        "pageid": 1399,
			        "ns": 0,
			        "title": "初音未来",
			        "extract": "初音未来是由 Crypton Future Media 企划、开发、贩售的 VOCALOID 声音库软件及其拟人化形象。",
			        "fullurl": "https://zh.moegirl.org.cn/初音未来",
			        "lastrevid": 8535826,
			        "touched": "2026-06-21T13:21:03Z",
			        "categories": [
			          {"ns": 14, "title": "Category:VOCALOID角色"},
			          {"ns": 14, "title": "Category:双马尾"}
			        ]
			      }`+touhouPage+`
			    }
			  }
			}`)
		case "/sitemap-index.xml":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
			<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
			  <sitemap><loc>`+baseURL+`/sitemap-ns0.xml</loc></sitemap>
			</sitemapindex>`)
		case "/sitemap-ns0.xml":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
			<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
			  <url><loc>https://zh.moegirl.org.cn/初音未来</loc></url>
			  <url><loc>https://zh.moegirl.org.cn/东方Project</loc></url>
			  <url><loc>https://zh.moegirl.org.cn/第三个页面</loc></url>
			</urlset>`)
		default:
			http.NotFound(w, r)
		}
	}))
	baseURL = server.URL
	return server
}

func fakeLargeMoegirlSource(t *testing.T, validPages int) *httptest.Server {
	t.Helper()

	pages := map[string]moegirlFixturePage{}
	for index := 1; index <= validPages; index++ {
		title := fmt.Sprintf("测试角色%03d", index)
		pages[title] = moegirlFixturePage{
			PageID:         10000 + index,
			Namespace:      0,
			Title:          title,
			Extract:        title + "是用于 Moegirl FAQ 构建测试的公开条目摘要，包含足够长的介绍文本。",
			FullURL:        "https://zh.moegirl.org.cn/" + title,
			LastRevID:      int64(9000000 + index),
			Touched:        "2026-06-21T13:21:03Z",
			OmitCategories: index == 1,
		}
	}
	pages["测试角色001别名"] = pages["测试角色001"]
	pages["测试角色001别名"] = moegirlFixturePage{
		PageID:    pages["测试角色001"].PageID,
		Namespace: 0,
		Title:     "测试角色001别名",
		Extract:   pages["测试角色001"].Extract,
		FullURL:   "https://zh.moegirl.org.cn/测试角色001别名",
		LastRevID: pages["测试角色001"].LastRevID,
		Touched:   pages["测试角色001"].Touched,
	}
	pages["空摘要页面"] = moegirlFixturePage{PageID: 20001, Namespace: 0, Title: "空摘要页面", Extract: "", LastRevID: 9100001}
	pages["非正文页面"] = moegirlFixturePage{PageID: 20002, Namespace: 10, Title: "非正文页面", Extract: "这个页面在非正文命名空间。", LastRevID: 9100002}
	pages["短摘要页面"] = moegirlFixturePage{PageID: 20003, Namespace: 0, Title: "短摘要页面", Extract: "太短", LastRevID: 9100003}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			titles := strings.Split(r.URL.Query().Get("titles"), "|")
			pageFragments := make([]string, 0, len(titles))
			for index, title := range titles {
				title = strings.TrimSpace(title)
				if title == "" {
					continue
				}
				if title == "缺失页面" {
					pageFragments = append(pageFragments, fmt.Sprintf(`"%d":{"ns":0,"title":"%s","missing":""}`, 30000+index, title))
					continue
				}
				page, ok := pages[title]
				if !ok {
					http.Error(w, "unexpected title "+title, http.StatusBadRequest)
					return
				}
				pageFragments = append(pageFragments, page.JSONFragment(30000+index))
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{"batchcomplete":"","query":{"pages":{`+strings.Join(pageFragments, ",")+`}}}`)
		case "/sitemap-index.xml":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`)
		default:
			http.NotFound(w, r)
		}
	}))
}

type moegirlFixturePage struct {
	PageID         int
	Namespace      int
	Title          string
	Extract        string
	FullURL        string
	LastRevID      int64
	Touched        string
	OmitCategories bool
}

func (page moegirlFixturePage) JSONFragment(key int) string {
	categories := `,"categories":[{"ns":14,"title":"Category:测试分类"}]`
	if page.OmitCategories {
		categories = ""
	}
	return fmt.Sprintf(
		`"%d":{"pageid":%d,"ns":%d,"title":%q,"extract":%q,"fullurl":%q,"lastrevid":%d,"touched":%q%s}`,
		key,
		page.PageID,
		page.Namespace,
		page.Title,
		page.Extract,
		page.FullURL,
		page.LastRevID,
		page.Touched,
		categories,
	)
}

func readZipEntry(t *testing.T, archive []byte, name string) []byte {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer entry.Close()
		data, err := io.ReadAll(entry)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return data
	}
	t.Fatalf("zip entry %s not found", name)
	return nil
}
