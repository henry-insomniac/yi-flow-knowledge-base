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

func TestAdminCanImportMoegirlFAQDraftForManualReviewWithoutPublishing(t *testing.T) {
	moegirl := fakeMoegirlSource(t)
	defer moegirl.Close()

	handler, err := server.NewHandler(server.Options{
		StorageDir:                 t.TempDir(),
		AdminToken:                 "test-admin-token",
		MoegirlAPIURL:              moegirl.URL + "/api.php",
		MoegirlSitemapIndexURL:     moegirl.URL + "/sitemap-index.xml",
		MoegirlPublicArticleOrigin: "https://zh.moegirl.org.cn",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveCore := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.core-clean", `{
	  "chunks": [{
	    "chunk_id": "core-clean",
	    "title": "Core clean",
	    "path": "core/clean",
	    "source": "yi-flow-core",
	    "content": "Core draft stays isolated while Moegirl FAQ draft import runs."
	  }],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if saveCore.Code != http.StatusCreated {
		t.Fatalf("save core draft status=%d body=%s", saveCore.Code, saveCore.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/moegirl/import-draft", bytes.NewBufferString(`{
	  "version": "2026.06.26.moegirl-draft",
	  "titles": ["初音未来"]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl import draft status=%d body=%s", response.Code, response.Body.String())
	}
	var imported struct {
		KBID          string `json:"kb_id"`
		Version       string `json:"version"`
		Latest        bool   `json:"latest"`
		ChunkCount    int    `json:"chunk_count"`
		PromptCount   int    `json:"prompt_count"`
		CitationCount int    `json:"citation_count"`
		ReviewReport  struct {
			FullMirrorSuspectCount int `json:"full_mirror_suspect_count"`
			Target                 struct {
				AcceptedPagesRequired   int  `json:"accepted_pages_required"`
				FAQChunksRequired       int  `json:"faq_chunks_required"`
				GoldenQuestionsRequired int  `json:"golden_questions_required"`
				ReadyForHITL            bool `json:"ready_for_hitl"`
			} `json:"target"`
		} `json:"review_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if imported.KBID != "moegirl-acgn-faq" || imported.Version != "2026.06.26.moegirl-draft" || imported.Latest || imported.ChunkCount != 3 || imported.PromptCount != 3 || imported.CitationCount != 3 {
		t.Fatalf("unexpected import response = %+v body=%s", imported, response.Body.String())
	}
	if imported.ReviewReport.FullMirrorSuspectCount != 0 ||
		imported.ReviewReport.Target.AcceptedPagesRequired != 300 ||
		imported.ReviewReport.Target.FAQChunksRequired != 900 ||
		imported.ReviewReport.Target.GoldenQuestionsRequired != 50 ||
		imported.ReviewReport.Target.ReadyForHITL {
		t.Fatalf("unexpected review target = %+v", imported.ReviewReport)
	}

	listChunks := httptest.NewRequest("GET", "/admin/api/kb/moegirl-acgn-faq/drafts/2026.06.26.moegirl-draft/chunks", nil)
	listChunks.Header.Set("Authorization", "Bearer test-admin-token")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listChunks)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list imported chunks status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Chunks []struct {
			ChunkID          string `json:"chunk_id"`
			ReviewStatus     string `json:"review_status"`
			CitationURL      string `json:"citation_url"`
			License          string `json:"license"`
			SourcePolicy     string `json:"source_policy"`
			SourceRevisionID string `json:"source_revision_id"`
			SourcePageID     string `json:"source_page_id"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode imported chunks: %v", err)
	}
	if len(listed.Chunks) != 3 || listed.Chunks[0].ReviewStatus != "needs_review" || listed.Chunks[0].CitationURL != "https://zh.moegirl.org.cn/初音未来" || listed.Chunks[0].License != "CC BY-NC-SA 3.0 CN" || !strings.Contains(listed.Chunks[0].SourcePolicy, "no full-article mirror") || listed.Chunks[0].SourceRevisionID != "8535826" || listed.Chunks[0].SourcePageID != "1399" {
		t.Fatalf("imported chunk metadata = %+v", listed.Chunks)
	}

	update := httptest.NewRequest("PUT", "/admin/api/kb/moegirl-acgn-faq/drafts/2026.06.26.moegirl-draft/chunks/"+listed.Chunks[0].ChunkID, bytes.NewBufferString(`{
	  "chunk_id": "`+listed.Chunks[0].ChunkID+`",
	  "title": "初音未来 · 人工审核 FAQ",
	  "path": "moegirl/faq/初音未来/manual",
	  "source": "萌娘百科 (Moegirlpedia)",
	  "content": "人工审核后的初音未来 FAQ 摘要，保留来源、许可和 no full-article mirror 边界。",
	  "review_status": "approved",
	  "citation_url": "https://zh.moegirl.org.cn/初音未来",
	  "citation_title": "初音未来",
	  "source_name": "萌娘百科 (Moegirlpedia)",
	  "license": "CC BY-NC-SA 3.0 CN",
	  "source_policy": "summary-only with visible attribution; no full-article mirror; no AI training",
	  "source_revision_id": "8535826",
	  "source_page_id": "1399"
	}`))
	update.Header.Set("Authorization", "Bearer test-admin-token")
	update.Header.Set("Content-Type", "application/json")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update imported chunk status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}

	latestPreview := httptest.NewRecorder()
	handler.ServeHTTP(latestPreview, httptest.NewRequest("GET", "/kb/moegirl-acgn-faq/latest/preview", nil))
	if latestPreview.Code != http.StatusNotFound {
		t.Fatalf("import draft should not publish latest, status=%d body=%s", latestPreview.Code, latestPreview.Body.String())
	}

	auditCore := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.core-clean/source-audit", nil)
	auditCore.Header.Set("Authorization", "Bearer test-admin-token")
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, auditCore)
	if auditResponse.Code != http.StatusOK {
		t.Fatalf("core source audit status=%d body=%s", auditResponse.Code, auditResponse.Body.String())
	}
	if strings.Contains(auditResponse.Body.String(), `"violations":[{`) {
		t.Fatalf("core source audit should stay clean after moegirl import: %s", auditResponse.Body.String())
	}
}

func TestAdminMoegirlDraftReviewFlagsFullArticleMirrorSuspects(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	longMirror := strings.Repeat("疑似完整条目内容，包含大量连续正文。", 120)
	saveResponse := saveDraftJSON(t, handler, "moegirl-acgn-faq", "2026.06.26.moegirl-review", `{
	  "chunks": [{
	    "chunk_id": "moegirl-review-suspect",
	    "title": "疑似全文镜像",
	    "path": "moegirl/faq/疑似全文镜像",
	    "source": "萌娘百科 (Moegirlpedia)",
	    "content": "`+longMirror+`",
	    "citation_url": "https://zh.moegirl.org.cn/疑似全文镜像",
	    "citation_title": "疑似全文镜像",
	    "source_name": "萌娘百科 (Moegirlpedia)",
	    "license": "CC BY-NC-SA 3.0 CN",
	    "source_policy": "summary/FAQ only; no full article mirror; no AI training",
	    "source_revision_id": "8888",
	    "source_page_id": "9999"
	  }],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save review draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("GET", "/admin/api/kb/moegirl-acgn-faq/drafts/2026.06.26.moegirl-review/moegirl-review", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("moegirl review status=%d body=%s", response.Code, response.Body.String())
	}
	var report struct {
		FullMirrorSuspectCount int      `json:"full_mirror_suspect_count"`
		FullMirrorSuspectIDs   []string `json:"full_mirror_suspect_chunk_ids"`
		MissingMetadataCount   int      `json:"missing_metadata_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode moegirl review: %v", err)
	}
	if report.FullMirrorSuspectCount != 1 || len(report.FullMirrorSuspectIDs) != 1 || report.FullMirrorSuspectIDs[0] != "moegirl-review-suspect" || report.MissingMetadataCount != 0 {
		t.Fatalf("unexpected moegirl review report = %+v body=%s", report, response.Body.String())
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

func TestAdminMoegirlBuildPrioritizesGoldenTitlesBeforeSitemapBackfill(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeMoegirlPrioritySource(t)
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
	  "version": "2026.06.25.priority",
	  "priority_titles": ["核心角色"],
	  "limit": 2
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("priority build status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		ChunkCount  int `json:"chunk_count"`
		CrawlReport struct {
			PriorityTitles   int `json:"priority_titles"`
			DiscoveredTitles int `json:"discovered_titles"`
			UniqueTitles     int `json:"unique_titles"`
			AcceptedPages    int `json:"accepted_pages"`
		} `json:"crawl_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode priority build response: %v", err)
	}
	if decoded.ChunkCount != 9 ||
		decoded.CrawlReport.PriorityTitles != 1 ||
		decoded.CrawlReport.DiscoveredTitles != 2 ||
		decoded.CrawlReport.UniqueTitles != 3 ||
		decoded.CrawlReport.AcceptedPages != 3 {
		t.Fatalf("priority crawl report = %+v chunk_count=%d", decoded.CrawlReport, decoded.ChunkCount)
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/latest/preview?limit=9", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{"核心角色", "补充角色001", "补充角色002"} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing priority/backfill title %q: %s", expected, previewBody)
		}
	}
}

func TestAdminMoegirlBuildFollowsMediaWikiRedirectsForPriorityTitles(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	moegirl := fakeMoegirlRedirectSource(t)
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
	  "version": "2026.06.25.redirect",
	  "priority_titles": ["镜音铃"],
	  "title_aliases": {"镜音铃": ["铃和连"]},
	  "limit": 0
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("redirect build status=%d body=%s", response.Code, response.Body.String())
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/latest/preview?limit=3", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{"镜音铃·连", "Kagamine Rin", "Kagamine Len", "铃和连"} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing redirected content %q: %s", expected, previewBody)
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

func fakeMoegirlPrioritySource(t *testing.T) *httptest.Server {
	t.Helper()

	pages := map[string]moegirlFixturePage{
		"核心角色": {
			PageID:    41001,
			Namespace: 0,
			Title:     "核心角色",
			Extract:   "核心角色是用于验证 golden priority titles 会优先进入 Moegirl FAQ 首包的公开条目摘要。",
			FullURL:   "https://zh.moegirl.org.cn/核心角色",
			LastRevID: 9600001,
			Touched:   "2026-06-25T10:00:00Z",
		},
		"补充角色001": {
			PageID:    41002,
			Namespace: 0,
			Title:     "补充角色001",
			Extract:   "补充角色001是通过 sitemap backfill 加入 Moegirl FAQ 首包的公开条目摘要。",
			FullURL:   "https://zh.moegirl.org.cn/补充角色001",
			LastRevID: 9600002,
			Touched:   "2026-06-25T10:00:00Z",
		},
		"补充角色002": {
			PageID:    41003,
			Namespace: 0,
			Title:     "补充角色002",
			Extract:   "补充角色002是通过 sitemap backfill 加入 Moegirl FAQ 首包的公开条目摘要。",
			FullURL:   "https://zh.moegirl.org.cn/补充角色002",
			LastRevID: 9600003,
			Touched:   "2026-06-25T10:00:00Z",
		},
	}

	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			titles := strings.Split(r.URL.Query().Get("titles"), "|")
			pageFragments := make([]string, 0, len(titles))
			for index, title := range titles {
				title = strings.TrimSpace(title)
				if title == "" {
					continue
				}
				page, ok := pages[title]
				if !ok {
					http.Error(w, "unexpected title "+title, http.StatusBadRequest)
					return
				}
				pageFragments = append(pageFragments, page.JSONFragment(50000+index))
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{"batchcomplete":"","query":{"pages":{`+strings.Join(pageFragments, ",")+`}}}`)
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
			  <url><loc>https://zh.moegirl.org.cn/补充角色001</loc></url>
			  <url><loc>https://zh.moegirl.org.cn/补充角色002</loc></url>
			</urlset>`)
		default:
			http.NotFound(w, r)
		}
	}))
	baseURL = server.URL
	return server
}

func fakeMoegirlRedirectSource(t *testing.T) *httptest.Server {
	t.Helper()

	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if r.URL.Query().Get("redirects") != "1" {
				_, _ = io.WriteString(w, `{
				  "batchcomplete": "",
				  "query": {
				    "pages": {
				      "11926": {
				        "pageid": 11926,
				        "ns": 0,
				        "title": "镜音铃",
				        "extract": "",
				        "fullurl": "https://zh.moegirl.org.cn/镜音铃",
				        "lastrevid": 1634620,
				        "touched": "2026-06-25T10:00:00Z"
				      }
				    }
				  }
				}`)
				return
			}
			_, _ = io.WriteString(w, `{
			  "batchcomplete": "",
			  "query": {
			    "redirects": [{"from": "镜音铃", "to": "镜音铃·连"}],
			    "pages": {
			      "3955": {
			        "pageid": 3955,
			        "ns": 0,
			        "title": "镜音铃·连",
			        "extract": "镜音铃·连，即镜音铃（Kagamine Rin）与镜音连（Kagamine Len）的合称，是 Crypton Future Media 开发的 VOCALOID 声音库及角色形象。",
			        "fullurl": "https://zh.moegirl.org.cn/镜音铃·连",
			        "lastrevid": 8535999,
			        "touched": "2026-06-25T10:00:00Z",
			        "categories": [
			          {"ns": 14, "title": "Category:VOCALOID角色"}
			        ]
			      }
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
			<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`)
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
