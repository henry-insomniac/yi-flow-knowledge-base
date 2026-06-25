package server_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"yi-flow/knowledge-base/internal/server"
)

func TestAdminCanBuildAndPublishKnowledgePackFromPagePayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.001",
	  "chunks": [
	    {
	      "chunk_id": "yi-flow-core-builder-001",
	      "title": "yi-flow 知识包构建流程",
	      "path": "yi-flow/core/builder",
	      "source": "yi-flow-core",
	      "content": "yi-flow 知识包通过 chunks、prompts、citations 生成 chunks.sqlite、vector.index、knowledge-pack.zip 和 manifest.json。"
	    }
	  ],
	  "prompts": [
	    {
	      "id": "yi-flow-core-builder-check",
	      "title": "验证知识包构建流程",
	      "question": "yi-flow 知识包构建会生成哪些文件？"
	    }
	  ]
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/build-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("build publish status=%d body=%s", response.Code, response.Body.String())
	}

	var publishResult struct {
		KBID       string `json:"kb_id"`
		Version    string `json:"version"`
		Latest     bool   `json:"latest"`
		ChunkCount int    `json:"chunk_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &publishResult); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if publishResult.KBID != "yi-flow-core" || publishResult.Version != "2026.06.22.001" || !publishResult.Latest || publishResult.ChunkCount != 1 {
		t.Fatalf("publish result = %+v", publishResult)
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("latest manifest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}

	var manifest struct {
		KBID        string `json:"kb_id"`
		Version     string `json:"version"`
		ContentHash string `json:"content_hash"`
		Signature   string `json:"signature"`
		Files       struct {
			Chunks []struct {
				Path string `json:"path"`
			} `json:"chunks"`
			FTS []struct {
				Path string `json:"path"`
			} `json:"fts"`
			Vector []struct {
				Path string `json:"path"`
			} `json:"vector"`
			Prompts []struct {
				Path string `json:"path"`
			} `json:"prompts"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.KBID != "yi-flow-core" || manifest.Version != "2026.06.22.001" {
		t.Fatalf("manifest header = %+v", manifest)
	}
	if manifest.Files.Chunks[0].Path != "chunks.sqlite" || manifest.Files.FTS[0].Path != "chunks.sqlite" || manifest.Files.Vector[0].Path != "vector.index" || manifest.Files.Prompts[0].Path != "prompts.json" {
		t.Fatalf("manifest files = %+v", manifest.Files)
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.22.001/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	digest := sha256.Sum256(packageResponse.Body.Bytes())
	if manifest.ContentHash != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("content_hash=%s digest=%x", manifest.ContentHash, digest)
	}
	signature := strings.TrimPrefix(manifest.Signature, "ed25519:")
	signatureBytes := mustBase64(t, signature)
	if !ed25519.Verify(publicKey, digest[:], signatureBytes) {
		t.Fatalf("manifest signature does not verify")
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/preview?limit=3", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	if !bytes.Contains(previewResponse.Body.Bytes(), []byte("yi-flow 知识包构建流程")) {
		t.Fatalf("preview missing generated chunk: %s", previewResponse.Body.String())
	}
}

func TestAdminRejectsExternalACGNSourcesForYiFlowCoreBuildPublish(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.002",
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-1399",
	      "title": "初音未来",
	      "path": "moegirl/summary/初音未来",
	      "source": "萌娘百科 (Moegirlpedia)",
	      "content": "SHOULD_NOT_LEAK_FULL_CONTENT"
	    }
	  ],
	  "citations": {
	    "citations": [
	      {
	        "chunk_id": "moegirl-page-1399",
	        "source": "萌娘百科 (Moegirlpedia)",
	        "url": "https://zh.moegirl.org.cn/初音未来"
	      }
	    ]
	  }
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/build-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("external source publish status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"yi-flow-core", "moegirl"} {
		if !strings.Contains(strings.ToLower(response.Body.String()), expected) {
			t.Fatalf("rejection should mention %q: %s", expected, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "SHOULD_NOT_LEAK_FULL_CONTENT") {
		t.Fatalf("rejection leaked chunk content: %s", response.Body.String())
	}
}

func TestAdminCanPublishVersionAndClientsFetchLatestManifestAndPackage(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	manifest := validManifest("2026.06.20.001")
	packageBytes := validKnowledgePackZip(t, "uploaded-core-001")

	publishResponse := publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", manifest, packageBytes)
	if publishResponse.Code != http.StatusCreated {
		t.Fatalf("publish status=%d body=%s", publishResponse.Code, publishResponse.Body.String())
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("latest manifest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}

	var decoded struct {
		KBID    string `json:"kb_id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode latest manifest: %v", err)
	}
	if decoded.KBID != "yi-flow-core" || decoded.Version != "2026.06.20.001" {
		t.Fatalf("latest manifest = %+v", decoded)
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.20.001/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	if !bytes.Equal(packageResponse.Body.Bytes(), packageBytes) {
		t.Fatalf("package bytes = %q", packageResponse.Body.Bytes())
	}
}

func TestAdminRejectsUploadedYiFlowCorePackageWithExternalSources(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	version := "2026.06.20.polluted"
	packageBytes := knowledgePackZip(t, []testChunk{
		{
			ChunkID: "moegirl-page-1399",
			Title:   "初音未来",
			Path:    "moegirl/summary/初音未来",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "污染包不应被设为 latest。",
		},
	})
	publishResponse := publishVersion(t, handler, "yi-flow-core", version, validManifest(version), packageBytes)
	if publishResponse.Code != http.StatusBadRequest {
		t.Fatalf("polluted upload status=%d body=%s", publishResponse.Code, publishResponse.Body.String())
	}
	if !strings.Contains(strings.ToLower(publishResponse.Body.String()), "policy violation") {
		t.Fatalf("polluted upload error should mention policy violation: %s", publishResponse.Body.String())
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusNotFound {
		t.Fatalf("polluted upload should not set latest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}
}

func TestClientsCanListVersionsAndLatestVersion(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), validKnowledgePackZip(t, "version-list-001"))
	publishVersion(t, handler, "yi-flow-core", "2026.06.20.002", validManifest("2026.06.20.002"), validKnowledgePackZip(t, "version-list-002"))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/kb/yi-flow-core/versions", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("versions status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		KBID     string `json:"kb_id"`
		Latest   string `json:"latest"`
		Versions []struct {
			Version string `json:"version"`
			Latest  bool   `json:"latest"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode versions: %v", err)
	}

	if decoded.KBID != "yi-flow-core" || decoded.Latest != "2026.06.20.002" {
		t.Fatalf("decoded header = %+v", decoded)
	}
	if len(decoded.Versions) != 2 {
		t.Fatalf("versions len=%d", len(decoded.Versions))
	}
	if decoded.Versions[0].Version != "2026.06.20.002" || !decoded.Versions[0].Latest {
		t.Fatalf("latest version entry = %+v", decoded.Versions[0])
	}
	if decoded.Versions[1].Version != "2026.06.20.001" || decoded.Versions[1].Latest {
		t.Fatalf("previous version entry = %+v", decoded.Versions[1])
	}
}

func TestClientsCanPreviewPublishedKnowledgePackContent(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	packageBytes := knowledgePackZip(t, []testChunk{
		{
			ChunkID: "expense-policy-00042",
			Title:   "企业报销流程",
			Path:    "handbook/finance/expense.md",
			Source:  "finance-handbook",
			Content: "员工提交发票、审批单和付款信息后，财务会完成企业报销流程复核。",
		},
		{
			ChunkID: "retrieval-00001",
			Title:   "知识包检索验证",
			Path:    "runtime/retrieval/check.md",
			Source:  "runtime-notes",
			Content: "知识包检索验证需要在管理页复制样例问题，再到 App 中确认 chunk 引用。",
		},
	})
	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), packageBytes)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.20.001/preview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		KBID    string `json:"kb_id"`
		Version string `json:"version"`
		Chunks  []struct {
			ChunkID            string   `json:"chunk_id"`
			Title              string   `json:"title"`
			Path               string   `json:"path"`
			Source             string   `json:"source"`
			Content            string   `json:"content"`
			SuggestedQuestions []string `json:"suggested_questions"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if decoded.KBID != "yi-flow-core" || decoded.Version != "2026.06.20.001" {
		t.Fatalf("decoded header = %+v", decoded)
	}
	if len(decoded.Chunks) != 2 {
		t.Fatalf("chunks len=%d body=%s", len(decoded.Chunks), response.Body.String())
	}
	if decoded.Chunks[0].ChunkID != "expense-policy-00042" || decoded.Chunks[0].Title != "企业报销流程" {
		t.Fatalf("first chunk = %+v", decoded.Chunks[0])
	}
	if !strings.Contains(decoded.Chunks[0].Content, "发票") {
		t.Fatalf("first chunk content=%q", decoded.Chunks[0].Content)
	}
	if len(decoded.Chunks[0].SuggestedQuestions) == 0 || !strings.Contains(decoded.Chunks[0].SuggestedQuestions[0], "企业报销流程") {
		t.Fatalf("missing useful suggested question: %+v", decoded.Chunks[0].SuggestedQuestions)
	}
}

func TestClientsCanPreviewLatestKnowledgePackContent(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), knowledgePackZip(t, []testChunk{
		{
			ChunkID: "latest-check-00001",
			Title:   "latest 知识包验证",
			Path:    "runtime/latest.md",
			Source:  "runtime-notes",
			Content: "latest 预览应直接展示当前 App 将会下载的知识包内容。",
		},
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/preview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("latest preview status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("latest 知识包验证")) {
		t.Fatalf("latest preview missing chunk body=%s", response.Body.String())
	}
}

func TestAdminRAGCompareShowsLocalResultsWhenRemoteIsDisabled(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	packageBytes := knowledgePackZip(t, []testChunk{
		{
			ChunkID: "local-rag-001",
			Title:   "知识包更新路径",
			Path:    "runtime/update.md",
			Source:  "yi-flow-core",
			Content: "知识包更新路径通过 manifest.json 和 knowledge-pack.zip 发布，App 校验签名后写入 active_version。",
		},
	})
	publishVersion(t, handler, "yi-flow-core", "2026.06.22.002", validManifest("2026.06.22.002"), packageBytes)

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/rag/compare", bytes.NewBufferString(`{"query":"知识包更新路径是什么？","top_k":3}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rag compare status=%d body=%s", response.Code, response.Body.String())
	}

	var decoded struct {
		Local struct {
			Status  string `json:"status"`
			Version string `json:"version"`
			Chunks  []struct {
				ChunkID string `json:"chunk_id"`
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"chunks"`
		} `json:"local"`
		Remote struct {
			Status string `json:"status"`
			Error  *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"remote"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode rag compare: %v", err)
	}
	if decoded.Local.Status != "ok" || decoded.Local.Version != "2026.06.22.002" || len(decoded.Local.Chunks) != 1 {
		t.Fatalf("local compare = %+v", decoded.Local)
	}
	if decoded.Local.Chunks[0].ChunkID != "local-rag-001" || !strings.Contains(decoded.Local.Chunks[0].Content, "active_version") {
		t.Fatalf("local chunk = %+v", decoded.Local.Chunks[0])
	}
	if decoded.Remote.Status != "disabled" || decoded.Remote.Error == nil || decoded.Remote.Error.Code != "rag_gateway_disabled" {
		t.Fatalf("remote disabled state = %+v", decoded.Remote)
	}
}

func TestAdminCanExportReviewedWeKnoraChunksAndPublishKnowledgePack(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "version": "2026.06.22.weknora",
	  "source": "Tencent WeKnora",
	  "license": "CC BY-NC-SA 3.0 CN",
	  "source_policy": "reviewed summary chunks only; no full article mirror",
	  "chunks": [
	    {
	      "id": "chunk-remote-001",
	      "content": "初音未来是由 Crypton Future Media 企划的虚拟歌手形象。本导出只保留审核后的摘要。",
	      "knowledge_id": "doc-miku",
	      "knowledge_title": "初音未来",
	      "knowledge_filename": "moegirl/summary/初音未来",
	      "knowledge_source": "Moegirl reviewed export",
	      "score": 0.93,
	      "metadata": {"url": "https://zh.moegirl.org.cn/初音未来"},
	      "reviewed": true
	    }
	  ],
	  "prompts": [
	    {"id": "miku-check", "title": "验证初音未来", "question": "初音未来是谁？"}
	  ]
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/weknora-smoke/weknora/export-publish", requestBody)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("weknora export status=%d body=%s", response.Code, response.Body.String())
	}

	var publishResult struct {
		KBID          string `json:"kb_id"`
		Version       string `json:"version"`
		Latest        bool   `json:"latest"`
		ChunkCount    int    `json:"chunk_count"`
		CitationCount int    `json:"citation_count"`
		QualityStatus string `json:"quality_status"`
		QualityReport struct {
			Status string `json:"status"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"quality_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &publishResult); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if publishResult.KBID != "weknora-smoke" || publishResult.Version != "2026.06.22.weknora" || !publishResult.Latest || publishResult.ChunkCount != 1 || publishResult.CitationCount != 1 {
		t.Fatalf("publish result = %+v", publishResult)
	}
	if publishResult.QualityStatus != "passed" || publishResult.QualityReport.Status != "passed" || len(publishResult.QualityReport.Checks) < 4 {
		t.Fatalf("publish quality report = %+v", publishResult.QualityReport)
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/weknora-smoke/latest/preview?limit=3", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{"初音未来", "https://zh.moegirl.org.cn/初音未来", "reviewed summary chunks only"} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing %q: %s", expected, previewBody)
		}
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/weknora-smoke/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}
	var manifest struct {
		ContentHash string `json:"content_hash"`
		Signature   string `json:"signature"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/weknora-smoke/versions/2026.06.22.weknora/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	digest := sha256.Sum256(packageResponse.Body.Bytes())
	if manifest.ContentHash != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("content_hash=%s digest=%x", manifest.ContentHash, digest)
	}
	signature := strings.TrimPrefix(manifest.Signature, "ed25519:")
	if !ed25519.Verify(publicKey, digest[:], mustBase64(t, signature)) {
		t.Fatalf("manifest signature does not verify")
	}
	packageBody := packageResponse.Body.String()
	if !strings.Contains(packageBody, "chunk-remote-001") || !strings.Contains(packageBody, "CC BY-NC-SA 3.0 CN") {
		t.Fatalf("package missing citation metadata")
	}
}

func TestAdminCanDryRunWeKnoraExportWithoutPublishingLatest(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/weknora-smoke/weknora/export-dry-run", bytes.NewBufferString(`{
	  "version": "2026.06.25.weknora-dry-run",
	  "source": "Tencent WeKnora",
	  "license": "reviewed internal knowledge",
	  "source_policy": "reviewed chunks only",
	  "chunks": [
	    {
	      "id": "chunk-dry-run-001",
	      "content": "dry-run 只构建并审计，不更新 latest。",
	      "knowledge_id": "doc-dry-run",
	      "knowledge_title": "WeKnora dry-run",
	      "knowledge_filename": "weknora/dry-run.md",
	      "knowledge_source": "manual-review",
	      "metadata": {"url": "https://yi-flow.com/weknora/dry-run"},
	      "reviewed": true
	    }
	  ]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", response.Code, response.Body.String())
	}

	var dryRun struct {
		KBID          string `json:"kb_id"`
		Version       string `json:"version"`
		Latest        bool   `json:"latest"`
		ChunkCount    int    `json:"chunk_count"`
		CitationCount int    `json:"citation_count"`
		PackageHash   string `json:"package_hash"`
		QualityStatus string `json:"quality_status"`
		QualityReport struct {
			Status string `json:"status"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"quality_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &dryRun); err != nil {
		t.Fatalf("decode dry-run result: %v", err)
	}
	if dryRun.KBID != "weknora-smoke" || dryRun.Version != "2026.06.25.weknora-dry-run" || dryRun.Latest || dryRun.ChunkCount != 1 || dryRun.CitationCount != 1 || !strings.HasPrefix(dryRun.PackageHash, "sha256:") {
		t.Fatalf("dry-run result = %+v", dryRun)
	}
	if dryRun.QualityStatus != "passed" || dryRun.QualityReport.Status != "passed" || len(dryRun.QualityReport.Checks) < 4 {
		t.Fatalf("dry-run quality report = %+v", dryRun.QualityReport)
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/weknora-smoke/latest/preview", nil))
	if previewResponse.Code != http.StatusNotFound {
		t.Fatalf("dry-run should not publish latest, preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
}

func TestAdminWeKnoraExportRejectsUnreviewedChunks(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/weknora-smoke/weknora/export-publish", bytes.NewBufferString(`{
	  "version": "2026.06.22.bad",
	  "chunks": [{"id":"draft-001","content":"未经审核的片段","knowledge_title":"草稿","reviewed":false}]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unreviewed export status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "reviewed must be true") {
		t.Fatalf("unreviewed export should explain review gate: %s", response.Body.String())
	}
}

func TestAdminWeKnoraMoegirlExportRequiresSourceURL(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/weknora/export-publish", bytes.NewBufferString(`{
	  "version": "2026.06.25.missing-source-url",
	  "source": "Moegirl reviewed export",
	  "license": "CC BY-NC-SA 3.0 CN",
	  "source_policy": "reviewed summary chunks only; no full article mirror; no AI training",
	  "chunks": [{
	    "id": "moegirl-miku-overview",
	    "content": "初音未来是虚拟歌手形象。本导出只保留审核后的摘要。",
	    "knowledge_id": "moegirl-doc-miku",
	    "knowledge_title": "初音未来",
	    "knowledge_filename": "moegirl/faq/初音未来/overview",
	    "knowledge_source": "Moegirl reviewed export",
	    "reviewed": true
	  }]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing source url status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "source_url") {
		t.Fatalf("missing source url error should mention source_url: %s", response.Body.String())
	}
}

func TestAdminCanExportMoegirlWeKnoraChunksWithCrawlManifest(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/weknora/export-publish", bytes.NewBufferString(`{
	  "version": "2026.06.25.moegirl-weknora",
	  "source": "Moegirl reviewed export",
	  "license": "CC BY-NC-SA 3.0 CN",
	  "source_policy": "reviewed summary chunks only; no full article mirror; no AI training",
	  "chunks": [{
	    "id": "moegirl-miku-overview",
	    "content": "初音未来是虚拟歌手形象。本导出只保留审核后的摘要。",
	    "knowledge_id": "moegirl-doc-miku",
	    "knowledge_title": "初音未来",
	    "knowledge_filename": "moegirl/faq/初音未来/overview",
	    "knowledge_source": "Moegirl reviewed export",
	    "metadata": {
	      "url": "https://zh.moegirl.org.cn/初音未来",
	      "page_id": 1399,
	      "revision_id": "8291001",
	      "touched": "2026-06-24T08:00:00Z",
	      "categories": ["VOCALOID", "虚拟歌手"],
	      "fetched_at": "2026-06-25T04:00:00Z"
	    },
	    "review_status": "reviewed",
	    "reviewed": true
	  }],
	  "prompts": [
	    {"id": "miku-overview-check", "title": "验证初音未来", "question": "初音未来是谁？"}
	  ]
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("moegirl weknora export status=%d body=%s", response.Code, response.Body.String())
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-faq/latest/preview?limit=3", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	previewBody := previewResponse.Body.String()
	for _, expected := range []string{"初音未来", "https://zh.moegirl.org.cn/初音未来", "CC BY-NC-SA 3.0 CN"} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing %q: %s", expected, previewBody)
		}
	}
}

func TestAdminWeKnoraExportRejectsCrossContaminatedKnowledgeBases(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	cases := []struct {
		name         string
		path         string
		body         string
		errorSnippet string
	}{
		{
			name: "core rejects moegirl source",
			path: "/admin/api/kb/yi-flow-core/weknora/export-publish",
			body: `{
			  "version": "2026.06.25.cross-core",
			  "source": "Moegirl reviewed export",
			  "license": "CC BY-NC-SA 3.0 CN",
			  "source_policy": "reviewed summary chunks only; no full article mirror; no AI training",
			  "chunks": [{
			    "id": "moegirl-miku-overview",
			    "content": "初音未来摘要不允许进入 yi-flow-core。",
			    "knowledge_title": "初音未来",
			    "knowledge_filename": "moegirl/faq/初音未来/overview",
			    "knowledge_source": "Moegirl reviewed export",
			    "metadata": {"url": "https://zh.moegirl.org.cn/初音未来"},
			    "review_status": "reviewed"
			  }]
			}`,
			errorSnippet: "moegirl",
		},
		{
			name: "moegirl rejects yi-flow source",
			path: "/admin/api/kb/moegirl-acgn-faq/weknora/export-publish",
			body: `{
			  "version": "2026.06.25.cross-moegirl",
			  "source": "yi-flow-core reviewed export",
			  "license": "reviewed internal knowledge",
			  "source_policy": "reviewed yi-flow product chunks only",
			  "chunks": [{
			    "id": "yi-flow-core-agent-answer-flow-001",
			    "content": "Agent 回答链路不允许进入 Moegirl FAQ。",
			    "knowledge_title": "Agent 回答链路",
			    "knowledge_filename": "docs/architecture/agent-answer-flow.zh.mmd",
			    "knowledge_source": "yi-flow-knowledge-app",
			    "metadata": {
			      "url": "https://github.com/henry-insomniac/yi-flow-knowledge-app/blob/main/docs/architecture/agent-answer-flow.zh.mmd",
			      "page_id": 1399,
			      "revision_id": "8291001",
			      "touched": "2026-06-24T08:00:00Z",
			      "categories": ["internal"],
			      "fetched_at": "2026-06-25T04:00:00Z"
			    },
			    "review_status": "reviewed"
			  }]
			}`,
			errorSnippet: "zh.moegirl.org.cn",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", tc.path, bytes.NewBufferString(tc.body))
			request.Header.Set("Authorization", "Bearer test-admin-token")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("cross-contaminated export status=%d body=%s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), tc.errorSnippet) {
				t.Fatalf("cross-contaminated export should mention %q: %s", tc.errorSnippet, response.Body.String())
			}
		})
	}
}

func TestAdminCanRollbackLatestToExistingVersion(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), validKnowledgePackZip(t, "rollback-001"))
	publishVersion(t, handler, "yi-flow-core", "2026.06.20.002", validManifest("2026.06.20.002"), validKnowledgePackZip(t, "rollback-002"))

	rollback := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/latest", bytes.NewBufferString(`{"version":"2026.06.20.001"}`))
	rollback.Header.Set("Authorization", "Bearer test-admin-token")
	rollback.Header.Set("Content-Type", "application/json")
	rollbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(rollbackResponse, rollback)
	if rollbackResponse.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rollbackResponse.Code, rollbackResponse.Body.String())
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("latest manifest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}

	var decoded struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode latest manifest: %v", err)
	}
	if decoded.Version != "2026.06.20.001" {
		t.Fatalf("latest version=%s", decoded.Version)
	}
}

func TestAdminWriteEndpointsRequireBearerToken(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publish := multipartRequest(t, "POST", "/admin/api/kb/yi-flow-core/versions", map[string]string{
		"version": "2026.06.20.001",
	}, map[string][]byte{
		"manifest": validManifest("2026.06.20.001"),
		"package":  []byte("v1"),
	})
	publishResponse := httptest.NewRecorder()
	handler.ServeHTTP(publishResponse, publish)
	if publishResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated publish status=%d body=%s", publishResponse.Code, publishResponse.Body.String())
	}

	rollback := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/latest", bytes.NewBufferString(`{"version":"2026.06.20.001"}`))
	rollback.Header.Set("Content-Type", "application/json")
	rollbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(rollbackResponse, rollback)
	if rollbackResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rollback status=%d body=%s", rollbackResponse.Code, rollbackResponse.Body.String())
	}

	buildPublish := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/build-publish", bytes.NewBufferString(`{"version":"2026.06.20.001","chunks":[]}`))
	buildPublish.Header.Set("Content-Type", "application/json")
	buildPublishResponse := httptest.NewRecorder()
	handler.ServeHTTP(buildPublishResponse, buildPublish)
	if buildPublishResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated build publish status=%d body=%s", buildPublishResponse.Code, buildPublishResponse.Body.String())
	}

	draftPublish := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.20.001/publish", nil)
	draftPublishResponse := httptest.NewRecorder()
	handler.ServeHTTP(draftPublishResponse, draftPublish)
	if draftPublishResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated draft publish status=%d body=%s", draftPublishResponse.Code, draftPublishResponse.Body.String())
	}

	moegirlImport := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/moegirl/import-draft", bytes.NewBufferString(`{"version":"2026.06.26.moegirl","titles":["初音未来"]}`))
	moegirlImport.Header.Set("Content-Type", "application/json")
	moegirlImportResponse := httptest.NewRecorder()
	handler.ServeHTTP(moegirlImportResponse, moegirlImport)
	if moegirlImportResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated moegirl import status=%d body=%s", moegirlImportResponse.Code, moegirlImportResponse.Body.String())
	}

	draftSave := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.draft", bytes.NewBufferString(`{"chunks":[]}`))
	draftSave.Header.Set("Content-Type", "application/json")
	draftSaveResponse := httptest.NewRecorder()
	handler.ServeHTTP(draftSaveResponse, draftSave)
	if draftSaveResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated draft save status=%d body=%s", draftSaveResponse.Code, draftSaveResponse.Body.String())
	}
}

func TestAdminCanSaveAndPreviewDraftChunkWithoutPublishingLatest(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	requestBody := bytes.NewBufferString(`{
	  "chunks": [
	    {
	      "chunk_id": "draft-topic-001",
	      "title": "Draft Workspace Chunk",
	      "path": "draft/workspace/topic",
	      "source": "manual",
	      "content": "Draft workspace content must be saved and previewed before latest is changed."
	    }
	  ],
	  "prompts": [
	    {
	      "id": "draft-topic-check",
	      "title": "Draft check",
	      "question": "What does the draft workspace save?"
	    }
	  ],
	  "citations": {
	    "citations": [
	      {
	        "chunk_id": "draft-topic-001",
	        "source": "Manual draft",
	        "url": "https://yi-flow.com/knowledge-base/admin/"
	      }
	    ]
	  }
	}`)
	saveDraft := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.draft", requestBody)
	saveDraft.Header.Set("Authorization", "Bearer test-admin-token")
	saveDraft.Header.Set("Content-Type", "application/json")
	saveResponse := httptest.NewRecorder()
	handler.ServeHTTP(saveResponse, saveDraft)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	var saved struct {
		KBID       string `json:"kb_id"`
		Version    string `json:"version"`
		Status     string `json:"status"`
		ChunkCount int    `json:"chunk_count"`
		CreatedAt  string `json:"created_at"`
		UpdatedAt  string `json:"updated_at"`
	}
	if err := json.Unmarshal(saveResponse.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save draft response: %v", err)
	}
	if saved.KBID != "yi-flow-core" || saved.Version != "2026.06.26.draft" || saved.Status != "draft" || saved.ChunkCount != 1 {
		t.Fatalf("saved draft summary = %+v", saved)
	}
	if saved.CreatedAt == "" || saved.UpdatedAt == "" {
		t.Fatalf("saved draft timestamps missing: %+v", saved)
	}

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if manifestResponse.Code != http.StatusNotFound {
		t.Fatalf("draft save should not publish latest, latest status=%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}

	readDraft := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.draft", nil)
	readDraft.Header.Set("Authorization", "Bearer test-admin-token")
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, readDraft)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("read draft status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}

	var draft struct {
		KBID    string `json:"kb_id"`
		Version string `json:"version"`
		Status  string `json:"status"`
		Chunks  []struct {
			ChunkID string `json:"chunk_id"`
			Title   string `json:"title"`
			Path    string `json:"path"`
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"chunks"`
		Prompts []struct {
			ID       string `json:"id"`
			Question string `json:"question"`
		} `json:"prompts"`
		Citations json.RawMessage `json:"citations"`
		CreatedAt string          `json:"created_at"`
		UpdatedAt string          `json:"updated_at"`
	}
	if err := json.Unmarshal(readResponse.Body.Bytes(), &draft); err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	if draft.KBID != "yi-flow-core" || draft.Version != "2026.06.26.draft" || draft.Status != "draft" {
		t.Fatalf("draft header = %+v", draft)
	}
	if len(draft.Chunks) != 1 {
		t.Fatalf("draft chunk count=%d body=%s", len(draft.Chunks), readResponse.Body.String())
	}
	if draft.Chunks[0].Title != "Draft Workspace Chunk" || draft.Chunks[0].Path != "draft/workspace/topic" || draft.Chunks[0].Content == "" {
		t.Fatalf("draft chunk = %+v", draft.Chunks[0])
	}
	if len(draft.Prompts) != 1 || draft.Prompts[0].Question != "What does the draft workspace save?" {
		t.Fatalf("draft prompts = %+v", draft.Prompts)
	}
	if !bytes.Contains(draft.Citations, []byte("Manual draft")) {
		t.Fatalf("draft citations missing source: %s", string(draft.Citations))
	}

	previewDraft := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.draft/preview?limit=1", nil)
	previewDraft.Header.Set("Authorization", "Bearer test-admin-token")
	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, previewDraft)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview draft status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}

	var preview struct {
		KBID    string `json:"kb_id"`
		Version string `json:"version"`
		Status  string `json:"status"`
		Latest  bool   `json:"latest"`
		Chunks  []struct {
			ChunkID string `json:"chunk_id"`
			Title   string `json:"title"`
			Path    string `json:"path"`
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.KBID != "yi-flow-core" || preview.Version != "2026.06.26.draft" || preview.Status != "draft" || preview.Latest {
		t.Fatalf("draft preview header = %+v", preview)
	}
	if len(preview.Chunks) != 1 {
		t.Fatalf("draft preview chunk count=%d body=%s", len(preview.Chunks), previewResponse.Body.String())
	}
	if preview.Chunks[0].Title != "Draft Workspace Chunk" || preview.Chunks[0].Path != "draft/workspace/topic" || preview.Chunks[0].Content != "Draft workspace content must be saved and previewed before latest is changed." {
		t.Fatalf("draft preview chunk = %+v", preview.Chunks[0])
	}
}

func TestAdminDraftSaveLocalLatencySmoke(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	durations := make([]time.Duration, 0, 25)
	for index := 0; index < 25; index++ {
		suffix := string(rune('a' + index))
		body := bytes.NewBufferString(`{
		  "chunks": [
		    {
		      "chunk_id": "draft-latency-` + suffix + `",
		      "title": "Draft latency smoke",
		      "path": "draft/latency/` + suffix + `",
		      "source": "manual",
		      "content": "Draft latency smoke content ` + suffix + `."
		    }
		  ],
		  "prompts": [],
		  "citations": {"citations":[]}
		}`)
		request := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.latency."+suffix, body)
		request.Header.Set("Authorization", "Bearer test-admin-token")
		request.Header.Set("Content-Type", "application/json")

		response := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(response, request)
		durations = append(durations, time.Since(start))
		if response.Code != http.StatusCreated {
			t.Fatalf("save draft %s status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95Index := (len(durations)*95+99)/100 - 1
	if p95 := durations[p95Index]; p95 > 500*time.Millisecond {
		t.Fatalf("draft save p95=%s want <=500ms all=%v", p95, durations)
	}
}

func TestAdminDraftChunkCRUDRoundTripsThroughPublicAPIs(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.crud", `{
	  "chunks": [
	    {
	      "chunk_id": "alpha",
	      "title": "Alpha chunk",
	      "path": "draft/crud/alpha",
	      "source": "manual",
	      "content": "Alpha content for draft CRUD.",
	      "tags": ["core"],
	      "review_status": "draft"
	    }
	  ],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	create := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks", bytes.NewBufferString(`{
	  "chunk_id": "beta",
	  "title": "Beta agent chunk",
	  "path": "draft/crud/beta",
	  "source": "manual",
	  "content": "Beta content mentions agent routing and chunk editing.",
	  "tags": ["agent", "core"],
	  "review_status": "needs_review"
	}`))
	create.Header.Set("Authorization", "Bearer test-admin-token")
	create.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create chunk status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}

	search := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks?q=agent&review_status=needs_review", nil)
	search.Header.Set("Authorization", "Bearer test-admin-token")
	searchResponse := httptest.NewRecorder()
	handler.ServeHTTP(searchResponse, search)
	if searchResponse.Code != http.StatusOK {
		t.Fatalf("search chunks status=%d body=%s", searchResponse.Code, searchResponse.Body.String())
	}
	var searchResult struct {
		Total   int `json:"total"`
		Matched int `json:"matched"`
		Chunks  []struct {
			ChunkID      string   `json:"chunk_id"`
			Title        string   `json:"title"`
			Tags         []string `json:"tags"`
			ReviewStatus string   `json:"review_status"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &searchResult); err != nil {
		t.Fatalf("decode search chunks: %v", err)
	}
	if searchResult.Total != 2 || searchResult.Matched != 1 || len(searchResult.Chunks) != 1 {
		t.Fatalf("search result = %+v body=%s", searchResult, searchResponse.Body.String())
	}
	if searchResult.Chunks[0].ChunkID != "beta" || searchResult.Chunks[0].ReviewStatus != "needs_review" || strings.Join(searchResult.Chunks[0].Tags, ",") != "agent,core" {
		t.Fatalf("search chunk = %+v", searchResult.Chunks[0])
	}

	update := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks/beta", bytes.NewBufferString(`{
	  "chunk_id": "beta",
	  "title": "Beta approved chunk",
	  "path": "draft/crud/beta-approved",
	  "source": "manual",
	  "content": "Updated beta content after review.",
	  "tags": ["approved", "agent"],
	  "review_status": "approved"
	}`))
	update.Header.Set("Authorization", "Bearer test-admin-token")
	update.Header.Set("Content-Type", "application/json")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update chunk status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}

	duplicate := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks/beta/duplicate", bytes.NewBufferString(`{"chunk_id":"beta-copy"}`))
	duplicate.Header.Set("Authorization", "Bearer test-admin-token")
	duplicate.Header.Set("Content-Type", "application/json")
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusCreated {
		t.Fatalf("duplicate chunk status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}

	deleteAlpha := httptest.NewRequest("DELETE", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks/alpha", nil)
	deleteAlpha.Header.Set("Authorization", "Bearer test-admin-token")
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteAlpha)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete chunk status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}

	readDraft := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud", nil)
	readDraft.Header.Set("Authorization", "Bearer test-admin-token")
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, readDraft)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("read draft status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	var draft struct {
		Chunks []struct {
			ChunkID      string   `json:"chunk_id"`
			Title        string   `json:"title"`
			Tags         []string `json:"tags"`
			ReviewStatus string   `json:"review_status"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(readResponse.Body.Bytes(), &draft); err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	if len(draft.Chunks) != 2 {
		t.Fatalf("draft chunks=%+v body=%s", draft.Chunks, readResponse.Body.String())
	}
	ids := []string{draft.Chunks[0].ChunkID, draft.Chunks[1].ChunkID}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "beta,beta-copy" {
		t.Fatalf("draft ids=%v chunks=%+v", ids, draft.Chunks)
	}

	duplicateID := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks", bytes.NewBufferString(`{
	  "chunk_id": "beta",
	  "title": "Duplicate beta",
	  "path": "draft/crud/duplicate",
	  "source": "manual",
	  "content": "Duplicate beta should be rejected."
	}`))
	duplicateID.Header.Set("Authorization", "Bearer test-admin-token")
	duplicateID.Header.Set("Content-Type", "application/json")
	duplicateIDResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateIDResponse, duplicateID)
	if duplicateIDResponse.Code != http.StatusConflict {
		t.Fatalf("duplicate chunk id status=%d body=%s", duplicateIDResponse.Code, duplicateIDResponse.Body.String())
	}
	if !strings.Contains(duplicateIDResponse.Body.String(), "duplicate chunk_id") {
		t.Fatalf("duplicate error should mention chunk_id: %s", duplicateIDResponse.Body.String())
	}

	invalidUpdate := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.crud/chunks/beta", bytes.NewBufferString(`{
	  "chunk_id": "beta",
	  "title": "",
	  "path": "draft/crud/beta",
	  "source": "manual",
	  "content": "content"
	}`))
	invalidUpdate.Header.Set("Authorization", "Bearer test-admin-token")
	invalidUpdate.Header.Set("Content-Type", "application/json")
	invalidUpdateResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidUpdateResponse, invalidUpdate)
	if invalidUpdateResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status=%d body=%s", invalidUpdateResponse.Code, invalidUpdateResponse.Body.String())
	}
	if !strings.Contains(invalidUpdateResponse.Body.String(), "title") {
		t.Fatalf("invalid update should mention title: %s", invalidUpdateResponse.Body.String())
	}
}

func TestAdminDraftChunkSearchLocalLatencySmoke(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		id := "chunk-" + strconv.Itoa(index)
		reviewStatus := "draft"
		content := "General chunk content " + strconv.Itoa(index)
		if index%10 == 0 {
			reviewStatus = "approved"
			content = "Needle topic content for search latency " + strconv.Itoa(index)
		}
		chunks = append(chunks, map[string]any{
			"chunk_id":      id,
			"title":         "Chunk " + strconv.Itoa(index),
			"path":          "draft/search/" + strconv.Itoa(index),
			"source":        "manual",
			"content":       content,
			"tags":          []string{"latency", reviewStatus},
			"review_status": reviewStatus,
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks":    chunks,
		"prompts":   []any{},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.search", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save search draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	durations := make([]time.Duration, 0, 25)
	for index := 0; index < 25; index++ {
		request := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.search/chunks?q=needle&review_status=approved", nil)
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(response, request)
		durations = append(durations, time.Since(start))
		if response.Code != http.StatusOK {
			t.Fatalf("search status=%d body=%s", response.Code, response.Body.String())
		}
		var decoded struct {
			Matched int `json:"matched"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode search response: %v", err)
		}
		if decoded.Matched != 100 {
			t.Fatalf("matched=%d want 100 body=%s", decoded.Matched, response.Body.String())
		}
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95Index := (len(durations)*95+99)/100 - 1
	if p95 := durations[p95Index]; p95 > 800*time.Millisecond {
		t.Fatalf("draft chunk search p95=%s want <=800ms all=%v", p95, durations)
	}
}

func TestAdminDraftChunkUpdateThousandChunkLocalLatencySmoke(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		chunks = append(chunks, map[string]any{
			"chunk_id":      "update-latency-chunk-" + strconv.Itoa(index),
			"title":         "Update Latency Chunk " + strconv.Itoa(index),
			"path":          "draft/update-latency/" + strconv.Itoa(index),
			"source":        "manual",
			"content":       "Update latency baseline content with enough length " + strconv.Itoa(index),
			"review_status": "approved",
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks":    chunks,
		"prompts":   []any{},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode update latency draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.update-latency", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save update latency draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	durations := make([]time.Duration, 0, 25)
	for index := 0; index < 25; index++ {
		payload := map[string]any{
			"chunk_id":      "update-latency-chunk-500",
			"title":         "Update Latency Chunk 500",
			"path":          "draft/update-latency/500",
			"source":        "manual",
			"content":       "Updated latency content for 1000 chunk draft pass " + strconv.Itoa(index),
			"review_status": "approved",
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode update payload: %v", err)
		}
		request := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.update-latency/chunks/update-latency-chunk-500", bytes.NewReader(payloadBytes))
		request.Header.Set("Authorization", "Bearer test-admin-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(response, request)
		durations = append(durations, time.Since(start))
		if response.Code != http.StatusOK {
			t.Fatalf("update chunk status=%d body=%s", response.Code, response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte(`"chunk_count":1000`)) {
			t.Fatalf("update response should preserve 1000 chunks: %s", response.Body.String())
		}
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95Index := (len(durations)*95+99)/100 - 1
	if p95 := durations[p95Index]; p95 > 500*time.Millisecond {
		t.Fatalf("1000 chunk single update p95=%s want <=500ms all=%v", p95, durations)
	}
}

func TestAdminDraftChunkCitationMetadataAndSourceAudit(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveMoegirl := saveDraftJSON(t, handler, "moegirl-acgn-faq", "2026.06.26.citation", `{
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-331116-faq-overview",
	      "title": "原神 FAQ",
	      "path": "moegirl/faq/原神/overview",
	      "source": "萌娘百科",
	      "content": "原神是米哈游开发的开放世界冒险游戏摘要。",
	      "tags": ["moegirl", "faq"],
	      "review_status": "approved",
	      "citation_url": "https://zh.moegirl.org.cn/原神",
	      "citation_title": "原神",
	      "source_name": "萌娘百科",
	      "license": "CC BY-NC-SA 3.0 CN",
	      "source_policy": "summary/FAQ only; no full article mirror; no AI training",
	      "source_revision_id": "123456",
	      "source_page_id": "331116"
	    }
	  ],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if saveMoegirl.Code != http.StatusCreated {
		t.Fatalf("save moegirl draft status=%d body=%s", saveMoegirl.Code, saveMoegirl.Body.String())
	}

	previewDraft := httptest.NewRequest("GET", "/admin/api/kb/moegirl-acgn-faq/drafts/2026.06.26.citation/preview", nil)
	previewDraft.Header.Set("Authorization", "Bearer test-admin-token")
	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, previewDraft)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview draft status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Chunks []struct {
			ChunkID          string `json:"chunk_id"`
			SourceURL        string `json:"source_url"`
			CitationTitle    string `json:"citation_title"`
			SourceName       string `json:"source_name"`
			License          string `json:"license"`
			SourcePolicy     string `json:"source_policy"`
			RevisionID       string `json:"revision_id"`
			SourcePageID     string `json:"source_page_id"`
			SuggestedIgnored string `json:"-"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(preview.Chunks) != 1 {
		t.Fatalf("preview chunks=%d body=%s", len(preview.Chunks), previewResponse.Body.String())
	}
	chunk := preview.Chunks[0]
	if chunk.SourceURL != "https://zh.moegirl.org.cn/原神" || chunk.CitationTitle != "原神" || chunk.SourceName != "萌娘百科" || chunk.License != "CC BY-NC-SA 3.0 CN" || chunk.SourcePolicy == "" || chunk.RevisionID != "123456" || chunk.SourcePageID != "331116" {
		t.Fatalf("preview citation metadata = %+v", chunk)
	}

	audit := httptest.NewRequest("GET", "/admin/api/kb/moegirl-acgn-faq/drafts/2026.06.26.citation/source-audit", nil)
	audit.Header.Set("Authorization", "Bearer test-admin-token")
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, audit)
	if auditResponse.Code != http.StatusOK {
		t.Fatalf("source audit status=%d body=%s", auditResponse.Code, auditResponse.Body.String())
	}
	var auditResult struct {
		KBID               string         `json:"kb_id"`
		ChunkCount         int            `json:"chunk_count"`
		SourceFamilyCounts map[string]int `json:"source_family_counts"`
		Violations         []struct {
			ChunkID string `json:"chunk_id"`
			Family  string `json:"family"`
			Field   string `json:"field"`
		} `json:"violations"`
	}
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &auditResult); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if auditResult.KBID != "moegirl-acgn-faq" || auditResult.ChunkCount != 1 || auditResult.SourceFamilyCounts["moegirl"] != 1 || len(auditResult.Violations) != 0 {
		t.Fatalf("audit result = %+v body=%s", auditResult, auditResponse.Body.String())
	}

	coreContamination := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.contaminated", `{
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-331116",
	      "title": "原神",
	      "path": "moegirl/faq/原神",
	      "source": "萌娘百科",
	      "content": "yi-flow-core must not accept ACG external chunks.",
	      "citation_url": "https://zh.moegirl.org.cn/原神",
	      "license": "CC BY-NC-SA 3.0 CN",
	      "source_policy": "summary/FAQ only"
	    }
	  ],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if coreContamination.Code != http.StatusBadRequest {
		t.Fatalf("core contamination status=%d body=%s", coreContamination.Code, coreContamination.Body.String())
	}
	if !strings.Contains(strings.ToLower(coreContamination.Body.String()), "rejects") || !strings.Contains(strings.ToLower(coreContamination.Body.String()), "moegirl") {
		t.Fatalf("core contamination error should mention rejection and source family: %s", coreContamination.Body.String())
	}

	missingMoegirlMetadata := saveDraftJSON(t, handler, "moegirl-acgn-faq", "2026.06.26.missing-citation", `{
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-331116",
	      "title": "原神",
	      "path": "moegirl/faq/原神",
	      "source": "萌娘百科",
	      "content": "Missing license and source policy should be rejected.",
	      "citation_url": "https://zh.moegirl.org.cn/原神"
	    }
	  ],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if missingMoegirlMetadata.Code != http.StatusBadRequest {
		t.Fatalf("missing moegirl metadata status=%d body=%s", missingMoegirlMetadata.Code, missingMoegirlMetadata.Body.String())
	}
	if !strings.Contains(strings.ToLower(missingMoegirlMetadata.Body.String()), "license") {
		t.Fatalf("missing metadata error should mention license: %s", missingMoegirlMetadata.Body.String())
	}
}

func TestBuildPublishUsesChunkCitationMetadataForPreview(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	missingMetadata := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/build-publish", bytes.NewBufferString(`{
	  "version": "2026.06.26.missing-citation",
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-331116-faq-overview",
	      "title": "原神 FAQ",
	      "path": "moegirl/faq/原神/overview",
	      "source": "萌娘百科",
	      "content": "缺少 license/source_policy 的 Moegirl chunk 不应发布。",
	      "citation_url": "https://zh.moegirl.org.cn/原神"
	    }
	  ],
	  "prompts": []
	}`))
	missingMetadata.Header.Set("Authorization", "Bearer test-admin-token")
	missingMetadata.Header.Set("Content-Type", "application/json")
	missingMetadataResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingMetadataResponse, missingMetadata)
	if missingMetadataResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing metadata publish status=%d body=%s", missingMetadataResponse.Code, missingMetadataResponse.Body.String())
	}
	if !strings.Contains(strings.ToLower(missingMetadataResponse.Body.String()), "license") {
		t.Fatalf("missing metadata publish error should mention license: %s", missingMetadataResponse.Body.String())
	}

	body := bytes.NewBufferString(`{
	  "version": "2026.06.26.citation",
	  "chunks": [
	    {
	      "chunk_id": "moegirl-page-331116-faq-overview",
	      "title": "原神 FAQ",
	      "path": "moegirl/faq/原神/overview",
	      "source": "萌娘百科",
	      "content": "原神是米哈游开发的开放世界冒险游戏摘要。",
	      "citation_url": "https://zh.moegirl.org.cn/原神",
	      "citation_title": "原神",
	      "source_name": "萌娘百科",
	      "license": "CC BY-NC-SA 3.0 CN",
	      "source_policy": "summary/FAQ only; no full article mirror; no AI training",
	      "source_revision_id": "123456",
	      "source_page_id": "331116"
	    }
	  ],
	  "prompts": []
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/moegirl-acgn-faq/build-publish", body)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("build publish status=%d body=%s", response.Code, response.Body.String())
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-faq/latest/preview", nil))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	if !bytes.Contains(previewResponse.Body.Bytes(), []byte(`"source_url":"https://zh.moegirl.org.cn/原神"`)) ||
		!bytes.Contains(previewResponse.Body.Bytes(), []byte(`"license":"CC BY-NC-SA 3.0 CN"`)) ||
		!bytes.Contains(previewResponse.Body.Bytes(), []byte(`"source_policy":"summary/FAQ only; no full article mirror; no AI training"`)) {
		t.Fatalf("preview missing citation metadata generated from chunk fields: %s", previewResponse.Body.String())
	}
}

func TestAdminDraftPromptCRUDAndPromptPreview(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.prompts", `{
	  "chunks": [
	    {
	      "chunk_id": "alpha",
	      "title": "Alpha chunk",
	      "path": "draft/prompts/alpha",
	      "source": "manual",
	      "content": "Alpha content for prompt preview."
	    },
	    {
	      "chunk_id": "beta",
	      "title": "Beta chunk",
	      "path": "draft/prompts/beta",
	      "source": "manual",
	      "content": "Beta content for prompt preview."
	    }
	  ],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	createPrompt := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts", bytes.NewBufferString(`{
	  "id": "prompt-alpha",
	  "title": "Alpha golden",
	  "question": "Where is alpha?",
	  "expected_chunk_ids": ["alpha"],
	  "tags": ["golden", "smoke"],
	  "answerability": "answerable",
	  "answerable": true
	}`))
	createPrompt.Header.Set("Authorization", "Bearer test-admin-token")
	createPrompt.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createPrompt)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create prompt status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}

	invalidPrompt := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts", bytes.NewBufferString(`{
	  "id": "prompt-missing",
	  "title": "Missing expected chunk",
	  "question": "Where is missing?",
	  "expected_chunk_ids": ["missing"],
	  "answerability": "answerable",
	  "answerable": true
	}`))
	invalidPrompt.Header.Set("Authorization", "Bearer test-admin-token")
	invalidPrompt.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidPrompt)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid expected chunk status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
	if !strings.Contains(invalidResponse.Body.String(), "expected_chunk_ids") {
		t.Fatalf("invalid expected chunk error should mention expected_chunk_ids: %s", invalidResponse.Body.String())
	}

	previewPrompt := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts/prompt-alpha/preview", nil)
	previewPrompt.Header.Set("Authorization", "Bearer test-admin-token")
	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, previewPrompt)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("prompt preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var promptPreview struct {
		Prompt struct {
			ID               string   `json:"id"`
			Question         string   `json:"question"`
			ExpectedChunkIDs []string `json:"expected_chunk_ids"`
			Answerable       bool     `json:"answerable"`
			Answerability    string   `json:"answerability"`
		} `json:"prompt"`
		Chunks []struct {
			ChunkID string `json:"chunk_id"`
			Title   string `json:"title"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &promptPreview); err != nil {
		t.Fatalf("decode prompt preview: %v", err)
	}
	if promptPreview.Prompt.ID != "prompt-alpha" || promptPreview.Prompt.Question != "Where is alpha?" || !promptPreview.Prompt.Answerable || promptPreview.Prompt.Answerability != "answerable" {
		t.Fatalf("prompt preview prompt = %+v", promptPreview.Prompt)
	}
	if len(promptPreview.Chunks) != 1 || promptPreview.Chunks[0].ChunkID != "alpha" {
		t.Fatalf("prompt preview chunks=%+v body=%s", promptPreview.Chunks, previewResponse.Body.String())
	}

	updatePrompt := httptest.NewRequest("PUT", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts/prompt-alpha", bytes.NewBufferString(`{
	  "id": "prompt-alpha",
	  "title": "Alpha refusal",
	  "question": "Should alpha refuse?",
	  "expected_chunk_ids": [],
	  "tags": ["golden", "refusal"],
	  "answerability": "refusal",
	  "answerable": false
	}`))
	updatePrompt.Header.Set("Authorization", "Bearer test-admin-token")
	updatePrompt.Header.Set("Content-Type", "application/json")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updatePrompt)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update prompt status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}

	deletePrompt := httptest.NewRequest("DELETE", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts/prompt-alpha", nil)
	deletePrompt.Header.Set("Authorization", "Bearer test-admin-token")
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deletePrompt)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete prompt status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}

	for index := 0; index < 20; index++ {
		promptID := "prompt-" + strconv.Itoa(index)
		body := bytes.NewBufferString(`{
		  "id": "` + promptID + `",
		  "title": "Prompt ` + strconv.Itoa(index) + `",
		  "question": "Question ` + strconv.Itoa(index) + `?",
		  "expected_chunk_ids": ["beta"],
		  "tags": ["batch"],
		  "answerability": "answerable",
		  "answerable": true
		}`)
		request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts", body)
		request.Header.Set("Authorization", "Bearer test-admin-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create prompt %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}

	listPrompts := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.prompts/prompts", nil)
	listPrompts.Header.Set("Authorization", "Bearer test-admin-token")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listPrompts)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list prompts status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var listResult struct {
		Total   int `json:"total"`
		Prompts []struct {
			ID               string   `json:"id"`
			ExpectedChunkIDs []string `json:"expected_chunk_ids"`
			Tags             []string `json:"tags"`
			Answerable       bool     `json:"answerable"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listResult); err != nil {
		t.Fatalf("decode list prompts: %v", err)
	}
	if listResult.Total != 20 || len(listResult.Prompts) != 20 {
		t.Fatalf("list prompts = %+v body=%s", listResult, listResponse.Body.String())
	}
}

func TestBuildPublishExportsPromptMetadata(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	body := bytes.NewBufferString(`{
	  "version": "2026.06.26.prompts",
	  "chunks": [
	    {
	      "chunk_id": "alpha",
	      "title": "Alpha chunk",
	      "path": "draft/prompts/alpha",
	      "source": "manual",
	      "content": "Alpha content for prompt export."
	    }
	  ],
	  "prompts": [
	    {
	      "id": "prompt-alpha",
	      "title": "Alpha golden",
	      "question": "Where is alpha?",
	      "expected_chunk_ids": ["alpha"],
	      "tags": ["golden"],
	      "answerability": "answerable",
	      "answerable": true
	    },
	    {
	      "id": "prompt-ood",
	      "title": "OOD refusal",
	      "question": "What is the weather?",
	      "expected_chunk_ids": [],
	      "tags": ["ood"],
	      "answerability": "ood",
	      "answerable": false
	    }
	  ]
	}`)
	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/build-publish", body)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("build publish status=%d body=%s", response.Code, response.Body.String())
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.26.prompts/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	promptsJSON := readZipEntry(t, packageResponse.Body.Bytes(), "prompts.json")
	if !bytes.Contains(promptsJSON, []byte(`"expected_chunk_ids": [`)) ||
		!bytes.Contains(promptsJSON, []byte(`"answerability": "ood"`)) ||
		!bytes.Contains(promptsJSON, []byte(`"answerable": false`)) {
		t.Fatalf("prompts.json missing prompt metadata: %s", string(promptsJSON))
	}
}

func TestAdminDraftRetrievalPreviewFindsTopKWithoutPublishingLatest(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.retrieval", `{
	  "chunks": [
	    {
	      "chunk_id": "agent-routing",
	      "title": "Agent routing",
	      "path": "draft/retrieval/agent-routing",
	      "source": "yi-flow-core",
	      "content": "Agent routing selects the right tool and retrieval plan before answering.",
	      "tags": ["agent", "routing"],
	      "citation_url": "https://yi-flow.com/docs/agent-routing",
	      "citation_title": "Agent routing design",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    },
	    {
	      "chunk_id": "agent-routing-missing-citation",
	      "title": "Agent routing missing citation",
	      "path": "draft/retrieval/missing-citation",
	      "source": "yi-flow-core",
	      "content": "Agent routing also needs missing citation warnings in preview."
	    },
	    {
	      "chunk_id": "billing",
	      "title": "Billing",
	      "path": "draft/retrieval/billing",
	      "source": "yi-flow-core",
	      "content": "Billing content should not outrank agent routing for routing questions."
	    }
	  ],
	  "prompts": [
	    {
	      "id": "prompt-agent-routing",
	      "title": "Agent routing golden",
	      "question": "How does agent routing choose tools?",
	      "expected_chunk_ids": ["agent-routing"],
	      "tags": ["golden"],
	      "answerability": "answerable",
	      "answerable": true
	    }
	  ],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	latestResponse := httptest.NewRecorder()
	handler.ServeHTTP(latestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if latestResponse.Code != http.StatusNotFound {
		t.Fatalf("draft retrieval should not publish latest, latest status=%d body=%s", latestResponse.Code, latestResponse.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.retrieval/retrieval-preview", bytes.NewBufferString(`{"query":"agent routing tool retrieval","top_k":2}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("retrieval preview status=%d body=%s", response.Code, response.Body.String())
	}
	var preview struct {
		Status  string   `json:"status"`
		Query   string   `json:"query"`
		TopK    int      `json:"top_k"`
		Reasons []string `json:"reasons"`
		Results []struct {
			ChunkID      string   `json:"chunk_id"`
			Title        string   `json:"title"`
			Path         string   `json:"path"`
			Source       string   `json:"source"`
			Score        float64  `json:"score"`
			MatchedTerms []string `json:"matched_terms"`
			Snippet      string   `json:"snippet"`
			Reasons      []string `json:"reasons"`
			Citation     struct {
				URL     string `json:"url"`
				Title   string `json:"title"`
				License string `json:"license"`
			} `json:"citation"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode retrieval preview: %v", err)
	}
	if preview.Status != "ok" || preview.Query != "agent routing tool retrieval" || preview.TopK != 2 || len(preview.Results) != 2 {
		t.Fatalf("retrieval preview header/results = %+v body=%s", preview, response.Body.String())
	}
	if preview.Results[0].ChunkID != "agent-routing" || preview.Results[0].Score <= 0 || !containsString(preview.Results[0].MatchedTerms, "agent") || !strings.Contains(preview.Results[0].Snippet, "Agent routing") {
		t.Fatalf("top result = %+v", preview.Results[0])
	}
	if preview.Results[0].Citation.URL != "https://yi-flow.com/docs/agent-routing" || preview.Results[0].Citation.License != "reviewed internal knowledge" {
		t.Fatalf("top result citation = %+v", preview.Results[0].Citation)
	}
	if !containsString(preview.Results[1].Reasons, "missing_citation") {
		t.Fatalf("second result should report missing_citation: %+v", preview.Results[1])
	}

	promptRequest := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.retrieval/retrieval-preview", bytes.NewBufferString(`{"prompt_id":"prompt-agent-routing","top_k":1}`))
	promptRequest.Header.Set("Authorization", "Bearer test-admin-token")
	promptRequest.Header.Set("Content-Type", "application/json")
	promptResponse := httptest.NewRecorder()
	handler.ServeHTTP(promptResponse, promptRequest)
	if promptResponse.Code != http.StatusOK {
		t.Fatalf("prompt retrieval preview status=%d body=%s", promptResponse.Code, promptResponse.Body.String())
	}
	if !bytes.Contains(promptResponse.Body.Bytes(), []byte(`"prompt_id":"prompt-agent-routing"`)) || !bytes.Contains(promptResponse.Body.Bytes(), []byte(`"query":"How does agent routing choose tools?"`)) {
		t.Fatalf("prompt retrieval preview missing prompt metadata: %s", promptResponse.Body.String())
	}

	emptyRequest := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.retrieval/retrieval-preview", bytes.NewBufferString(`{"query":"zzzz nohit","top_k":3}`))
	emptyRequest.Header.Set("Authorization", "Bearer test-admin-token")
	emptyRequest.Header.Set("Content-Type", "application/json")
	emptyResponse := httptest.NewRecorder()
	handler.ServeHTTP(emptyResponse, emptyRequest)
	if emptyResponse.Code != http.StatusOK {
		t.Fatalf("empty retrieval status=%d body=%s", emptyResponse.Code, emptyResponse.Body.String())
	}
	if !bytes.Contains(emptyResponse.Body.Bytes(), []byte(`"status":"no_answer"`)) || !bytes.Contains(emptyResponse.Body.Bytes(), []byte(`"empty_retrieval"`)) {
		t.Fatalf("empty retrieval should report no_answer and empty_retrieval: %s", emptyResponse.Body.String())
	}

	weakRequest := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.retrieval/retrieval-preview", bytes.NewBufferString(`{"query":"agent unmatchedone unmatchedtwo unmatchedthree","top_k":1}`))
	weakRequest.Header.Set("Authorization", "Bearer test-admin-token")
	weakRequest.Header.Set("Content-Type", "application/json")
	weakResponse := httptest.NewRecorder()
	handler.ServeHTTP(weakResponse, weakRequest)
	if weakResponse.Code != http.StatusOK {
		t.Fatalf("weak retrieval status=%d body=%s", weakResponse.Code, weakResponse.Body.String())
	}
	if !bytes.Contains(weakResponse.Body.Bytes(), []byte(`"status":"weak_score"`)) || !bytes.Contains(weakResponse.Body.Bytes(), []byte(`"weak_score"`)) {
		t.Fatalf("weak retrieval should report weak_score: %s", weakResponse.Body.String())
	}
}

func TestAdminDraftRetrievalPreviewLocalLatencySmoke(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		content := "General draft retrieval content " + strconv.Itoa(index)
		if index%25 == 0 {
			content = "Needle routing retrieval preview content " + strconv.Itoa(index)
		}
		chunks = append(chunks, map[string]any{
			"chunk_id":       "chunk-" + strconv.Itoa(index),
			"title":          "Chunk " + strconv.Itoa(index),
			"path":           "draft/retrieval/" + strconv.Itoa(index),
			"source":         "yi-flow-core",
			"content":        content,
			"citation_url":   "https://yi-flow.com/docs/" + strconv.Itoa(index),
			"citation_title": "Chunk " + strconv.Itoa(index),
			"source_name":    "yi-flow docs",
			"license":        "reviewed internal knowledge",
			"source_policy":  "reviewed yi-flow product chunks only",
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks":    chunks,
		"prompts":   []any{},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.retrieval-latency", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save retrieval draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	durations := make([]time.Duration, 0, 25)
	for index := 0; index < 25; index++ {
		request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.retrieval-latency/retrieval-preview", bytes.NewBufferString(`{"query":"needle routing retrieval","top_k":5}`))
		request.Header.Set("Authorization", "Bearer test-admin-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(response, request)
		durations = append(durations, time.Since(start))
		if response.Code != http.StatusOK {
			t.Fatalf("retrieval preview status=%d body=%s", response.Code, response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte(`"status":"ok"`)) {
			t.Fatalf("retrieval preview should be ok: %s", response.Body.String())
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95Index := (len(durations)*95+99)/100 - 1
	if p95 := durations[p95Index]; p95 > time.Second {
		t.Fatalf("draft retrieval p95=%s want <=1s all=%v", p95, durations)
	}
}

func TestAdminDraftQualityGatesReportFailuresAndMetrics(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.gates", `{
	  "chunks": [
	    {
	      "chunk_id": "good",
	      "title": "Good chunk",
	      "path": "draft/gates/good",
	      "source": "yi-flow-core",
	      "content": "Agent routing uses draft retrieval preview before publishing.",
	      "citation_url": "https://yi-flow.com/docs/good",
	      "citation_title": "Good",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    },
	    {
	      "chunk_id": "missing-citation",
	      "title": "Missing citation",
	      "path": "draft/gates/missing-citation",
	      "source": "yi-flow-core",
	      "content": "Agent routing missing citation should block publish."
	    },
	    {
	      "chunk_id": "near-dup-a",
	      "title": "Near duplicate A",
	      "path": "draft/gates/dup-a",
	      "source": "yi-flow-core",
	      "content": "Duplicate content should be detected before publishing.",
	      "citation_url": "https://yi-flow.com/docs/dup-a",
	      "citation_title": "Dup A",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    },
	    {
	      "chunk_id": "near-dup-b",
	      "title": "Near duplicate B",
	      "path": "draft/gates/dup-b",
	      "source": "yi-flow-core",
	      "content": "Duplicate content should be detected before publishing.",
	      "citation_url": "https://yi-flow.com/docs/dup-b",
	      "citation_title": "Dup B",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    },
	    {
	      "chunk_id": "too-short",
	      "title": "Too short",
	      "path": "draft/gates/too-short",
	      "source": "yi-flow-core",
	      "content": "short",
	      "citation_url": "https://yi-flow.com/docs/short",
	      "citation_title": "Short",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    }
	  ],
	  "prompts": [
	    {
	      "id": "golden-good",
	      "title": "Good golden",
	      "question": "How does agent routing use draft retrieval preview?",
	      "expected_chunk_ids": ["good"],
	      "answerability": "answerable",
	      "answerable": true
	    },
	    {
	      "id": "golden-refusal",
	      "title": "Refusal golden",
	      "question": "What is the weather in Tokyo?",
	      "expected_chunk_ids": [],
	      "answerability": "ood",
	      "answerable": false
	    }
	  ],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save gates draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.gates/quality-gates", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("quality gates status=%d body=%s", response.Code, response.Body.String())
	}
	var report struct {
		Status       string `json:"status"`
		BlockPublish bool   `json:"block_publish"`
		Metrics      struct {
			Top1HitRate            float64 `json:"top1_hit_rate"`
			Top5HitRate            float64 `json:"top5_hit_rate"`
			CitationRate           float64 `json:"citation_rate"`
			DuplicateAnswerRate    float64 `json:"duplicate_answer_rate"`
			RefusalPassRate        float64 `json:"refusal_pass_rate"`
			MissingCitationCount   int     `json:"missing_citation_count"`
			UnsupportedEntityCount int     `json:"unsupported_entity_count"`
		} `json:"metrics"`
		Checks []struct {
			Name        string   `json:"name"`
			Status      string   `json:"status"`
			Severity    string   `json:"severity"`
			Count       int      `json:"count"`
			ChunkIDs    []string `json:"chunk_ids"`
			PromptIDs   []string `json:"prompt_ids"`
			Remediation string   `json:"remediation"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode quality gate report: %v", err)
	}
	if report.Status != "failed" || !report.BlockPublish {
		t.Fatalf("quality gate should fail and block publish: %+v body=%s", report, response.Body.String())
	}
	for _, expected := range []string{"missing_citations", "near_duplicate_content", "invalid_lengths", "golden_eval"} {
		if !qualityCheckFailed(report.Checks, expected) {
			t.Fatalf("expected failing check %s in %+v", expected, report.Checks)
		}
	}
	if report.Metrics.MissingCitationCount != 1 || report.Metrics.Top5HitRate < 0.5 || report.Metrics.RefusalPassRate < 1 {
		t.Fatalf("unexpected quality metrics = %+v", report.Metrics)
	}
}

func TestAdminDraftQualityGatesLocalLatencySmoke(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		chunks = append(chunks, map[string]any{
			"chunk_id":       "chunk-" + strconv.Itoa(index),
			"title":          "Chunk " + strconv.Itoa(index),
			"path":           "draft/gates/" + strconv.Itoa(index),
			"source":         "yi-flow-core",
			"content":        "Quality gate latency content with enough length " + strconv.Itoa(index),
			"citation_url":   "https://yi-flow.com/docs/gates/" + strconv.Itoa(index),
			"citation_title": "Chunk " + strconv.Itoa(index),
			"source_name":    "yi-flow docs",
			"license":        "reviewed internal knowledge",
			"source_policy":  "reviewed yi-flow product chunks only",
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks":    chunks,
		"prompts":   []any{},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.gates-latency", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save gates latency draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.gates-latency/quality-gates", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(response, request)
	elapsed := time.Since(start)
	if response.Code != http.StatusOK {
		t.Fatalf("quality gates status=%d body=%s", response.Code, response.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("quality gates elapsed=%s want <=3s", elapsed)
	}
}

func TestAdminDraftDryRunBuildGeneratesPackagePreviewWithoutPublishingLatest(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 55)
	for index := 0; index < 55; index++ {
		indexText := strconv.Itoa(index)
		chunks = append(chunks, map[string]any{
			"chunk_id":       "dry-run-chunk-" + indexText,
			"title":          "Dry Run Chunk " + indexText,
			"path":           "draft/dry-run/" + indexText,
			"source":         "yi-flow-core",
			"content":        "Draft dry run package preview chunk " + indexText + " keeps signed artifacts previewable before publish with unique wording.",
			"citation_url":   "https://yi-flow.com/docs/dry-run/" + indexText,
			"citation_title": "Dry Run Chunk " + indexText,
			"source_name":    "yi-flow docs",
			"license":        "reviewed internal knowledge",
			"source_policy":  "reviewed yi-flow product chunks only",
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks": chunks,
		"prompts": []map[string]any{
			{
				"id":                 "dry-run-answerable",
				"title":              "Dry-run answerable",
				"question":           "What keeps signed artifacts previewable before publish?",
				"expected_chunk_ids": []string{"dry-run-chunk-0"},
				"answerability":      "answerable",
				"answerable":         true,
			},
			{
				"id":            "dry-run-refusal",
				"title":         "Dry-run refusal",
				"question":      "What is the weather in Tokyo?",
				"answerability": "ood",
				"answerable":    false,
			},
		},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.dry-run", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save dry-run draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.dry-run/build-dry-run?limit=50", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("draft dry-run status=%d body=%s", response.Code, response.Body.String())
	}

	var dryRun struct {
		KBID          string `json:"kb_id"`
		Version       string `json:"version"`
		Latest        bool   `json:"latest"`
		ChunkCount    int    `json:"chunk_count"`
		CitationCount int    `json:"citation_count"`
		PromptCount   int    `json:"prompt_count"`
		PackageHash   string `json:"package_hash"`
		PreviewURL    string `json:"preview_url"`
		QualityStatus string `json:"quality_status"`
		Manifest      struct {
			KBID        string `json:"kb_id"`
			Version     string `json:"version"`
			ContentHash string `json:"content_hash"`
			Files       struct {
				Chunks []struct {
					Path string `json:"path"`
				} `json:"chunks"`
				Vector []struct {
					Path string `json:"path"`
				} `json:"vector"`
				Citations []struct {
					Path string `json:"path"`
				} `json:"citations"`
				Prompts []struct {
					Path string `json:"path"`
				} `json:"prompts"`
			} `json:"files"`
		} `json:"manifest"`
		Files []struct {
			Path string `json:"path"`
			Size uint64 `json:"size"`
		} `json:"files"`
		Preview struct {
			Chunks []struct {
				ChunkID            string   `json:"chunk_id"`
				SuggestedQuestions []string `json:"suggested_questions"`
			} `json:"chunks"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &dryRun); err != nil {
		t.Fatalf("decode dry-run: %v", err)
	}
	if dryRun.KBID != "yi-flow-core" || dryRun.Version != "2026.06.26.dry-run" || dryRun.Latest || dryRun.ChunkCount != 55 || dryRun.CitationCount != 55 || dryRun.PromptCount != 2 {
		t.Fatalf("unexpected dry-run summary = %+v", dryRun)
	}
	if dryRun.QualityStatus != "passed" || !strings.HasPrefix(dryRun.PackageHash, "sha256:") || dryRun.PackageHash != dryRun.Manifest.ContentHash {
		t.Fatalf("unexpected package metadata = %+v", dryRun)
	}
	for _, expectedFile := range []string{"manifest.json", "knowledge-pack.zip", "chunks.sqlite", "citations.json", "prompts.json", "vector.index"} {
		if !fileListContains(dryRun.Files, expectedFile) {
			t.Fatalf("dry-run file list missing %s: %+v", expectedFile, dryRun.Files)
		}
	}
	if dryRun.Manifest.KBID != "yi-flow-core" || dryRun.Manifest.Version != "2026.06.26.dry-run" || dryRun.Manifest.Files.Chunks[0].Path != "chunks.sqlite" || dryRun.Manifest.Files.Vector[0].Path != "vector.index" || dryRun.Manifest.Files.Citations[0].Path != "citations.json" || dryRun.Manifest.Files.Prompts[0].Path != "prompts.json" {
		t.Fatalf("unexpected manifest preview = %+v", dryRun.Manifest)
	}
	if !strings.Contains(dryRun.PreviewURL, "/admin/api/kb/yi-flow-core/drafts/2026.06.26.dry-run/build-dry-run?limit=50") {
		t.Fatalf("preview url = %s", dryRun.PreviewURL)
	}
	if len(dryRun.Preview.Chunks) != 50 || dryRun.Preview.Chunks[0].ChunkID != "dry-run-chunk-0" || len(dryRun.Preview.Chunks[0].SuggestedQuestions) == 0 {
		t.Fatalf("unexpected package preview = %+v", dryRun.Preview)
	}

	latestResponse := httptest.NewRecorder()
	handler.ServeHTTP(latestResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if latestResponse.Code != http.StatusNotFound {
		t.Fatalf("dry-run build should not publish latest, latest status=%d body=%s", latestResponse.Code, latestResponse.Body.String())
	}
	versionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(versionsResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions", nil))
	if versionsResponse.Code != http.StatusNotFound {
		t.Fatalf("dry-run build should not create published versions, versions status=%d body=%s", versionsResponse.Code, versionsResponse.Body.String())
	}
}

func TestAdminDraftDryRunBuildRequiresPassingQualityGates(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.dry-run-fail", `{
	  "chunks": [
	    {
	      "chunk_id": "dry-run-fail",
	      "title": "Dry run fail",
	      "path": "draft/dry-run/fail",
	      "source": "yi-flow-core",
	      "content": "This draft intentionally lacks citation metadata so dry-run build is blocked."
	    }
	  ],
	  "prompts": [
	    {
	      "id": "dry-run-fail-prompt",
	      "title": "Dry-run fail prompt",
	      "question": "Why should dry-run fail?",
	      "expected_chunk_ids": ["dry-run-fail"],
	      "answerability": "answerable",
	      "answerable": true
	    }
	  ],
	  "citations": {"citations":[]}
	}`)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save dry-run failing draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.dry-run-fail/build-dry-run", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("failing quality gates should block dry-run status=%d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Error         string `json:"error"`
		QualityStatus string `json:"quality_status"`
		QualityReport struct {
			Status       string `json:"status"`
			BlockPublish bool   `json:"block_publish"`
			Checks       []struct {
				Name        string `json:"name"`
				Status      string `json:"status"`
				Remediation string `json:"remediation"`
			} `json:"checks"`
		} `json:"quality_report"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode dry-run failure: %v", err)
	}
	if !strings.Contains(failure.Error, "quality gates failed") || failure.QualityStatus != "failed" || !failure.QualityReport.BlockPublish || !qualityGateFailureContains(failure.QualityReport.Checks, "missing_citations") {
		t.Fatalf("unexpected dry-run failure = %+v body=%s", failure, response.Body.String())
	}
}

func TestAdminDraftPublishRequiresSuccessfulDryRunForSameContentHash(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir:               t.TempDir(),
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	savePublishableDraft(t, handler, "yi-flow-core", "2026.06.26.publish-guard", "publish guard original")
	publishWithoutDryRun := publishDraftFromStudio(t, handler, "yi-flow-core", "2026.06.26.publish-guard")
	if publishWithoutDryRun.Code != http.StatusConflict {
		t.Fatalf("publish without dry-run status=%d body=%s", publishWithoutDryRun.Code, publishWithoutDryRun.Body.String())
	}

	dryRun := dryRunDraftBuild(t, handler, "yi-flow-core", "2026.06.26.publish-guard")
	if dryRun.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", dryRun.Code, dryRun.Body.String())
	}
	savePublishableDraft(t, handler, "yi-flow-core", "2026.06.26.publish-guard", "publish guard changed")
	publishChangedDraft := publishDraftFromStudio(t, handler, "yi-flow-core", "2026.06.26.publish-guard")
	if publishChangedDraft.Code != http.StatusConflict {
		t.Fatalf("publish changed draft status=%d body=%s", publishChangedDraft.Code, publishChangedDraft.Body.String())
	}
	if !strings.Contains(publishChangedDraft.Body.String(), "dry-run content hash mismatch") {
		t.Fatalf("changed draft publish should mention hash mismatch: %s", publishChangedDraft.Body.String())
	}
}

func TestAdminDraftPublishLatestRollbackAndAuditLog(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	storageDir := t.TempDir()
	handler, err := server.NewHandler(server.Options{
		StorageDir:               storageDir,
		AdminToken:               "test-admin-token",
		KnowledgePackSigningSeed: privateKey.Seed(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	savePublishableDraft(t, handler, "yi-flow-core", "2026.06.26.publish.001", "publish marker one")
	if response := dryRunDraftBuild(t, handler, "yi-flow-core", "2026.06.26.publish.001"); response.Code != http.StatusOK {
		t.Fatalf("dry-run v1 status=%d body=%s", response.Code, response.Body.String())
	}
	publishV1 := publishDraftFromStudio(t, handler, "yi-flow-core", "2026.06.26.publish.001")
	if publishV1.Code != http.StatusCreated {
		t.Fatalf("publish v1 status=%d body=%s", publishV1.Code, publishV1.Body.String())
	}

	savePublishableDraft(t, handler, "yi-flow-core", "2026.06.26.publish.002", "publish marker two")
	if response := dryRunDraftBuild(t, handler, "yi-flow-core", "2026.06.26.publish.002"); response.Code != http.StatusOK {
		t.Fatalf("dry-run v2 status=%d body=%s", response.Code, response.Body.String())
	}
	publishV2 := publishDraftFromStudio(t, handler, "yi-flow-core", "2026.06.26.publish.002")
	if publishV2.Code != http.StatusCreated {
		t.Fatalf("publish v2 status=%d body=%s", publishV2.Code, publishV2.Body.String())
	}
	var published struct {
		KBID        string `json:"kb_id"`
		Version     string `json:"version"`
		Latest      bool   `json:"latest"`
		ContentHash string `json:"content_hash"`
		GateStatus  string `json:"gate_status"`
	}
	if err := json.Unmarshal(publishV2.Body.Bytes(), &published); err != nil {
		t.Fatalf("decode publish v2: %v", err)
	}
	if published.KBID != "yi-flow-core" || published.Version != "2026.06.26.publish.002" || !published.Latest || !strings.HasPrefix(published.ContentHash, "sha256:") || published.GateStatus != "passed" {
		t.Fatalf("unexpected publish response = %+v", published)
	}

	versionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(versionsResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions", nil))
	if versionsResponse.Code != http.StatusOK {
		t.Fatalf("versions status=%d body=%s", versionsResponse.Code, versionsResponse.Body.String())
	}
	if !strings.Contains(versionsResponse.Body.String(), "2026.06.26.publish.001") || !strings.Contains(versionsResponse.Body.String(), "2026.06.26.publish.002") {
		t.Fatalf("versions list missing published versions: %s", versionsResponse.Body.String())
	}

	latestPreview := httptest.NewRecorder()
	handler.ServeHTTP(latestPreview, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/preview?limit=3", nil))
	if latestPreview.Code != http.StatusOK || !strings.Contains(latestPreview.Body.String(), "publish marker two") {
		t.Fatalf("latest preview status=%d body=%s", latestPreview.Code, latestPreview.Body.String())
	}
	versionedPreview := httptest.NewRecorder()
	handler.ServeHTTP(versionedPreview, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.26.publish.001/preview?limit=3", nil))
	if versionedPreview.Code != http.StatusOK || !strings.Contains(versionedPreview.Body.String(), "publish marker one") {
		t.Fatalf("versioned preview status=%d body=%s", versionedPreview.Code, versionedPreview.Body.String())
	}
	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/yi-flow-core/versions/2026.06.26.publish.002/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK || packageResponse.Body.Len() == 0 {
		t.Fatalf("package status=%d body_len=%d", packageResponse.Code, packageResponse.Body.Len())
	}

	rollback := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/latest", bytes.NewBufferString(`{"version":"2026.06.26.publish.001"}`))
	rollback.Header.Set("Authorization", "Bearer test-admin-token")
	rollback.Header.Set("Content-Type", "application/json")
	rollbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(rollbackResponse, rollback)
	if rollbackResponse.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rollbackResponse.Code, rollbackResponse.Body.String())
	}
	latestManifest := httptest.NewRecorder()
	handler.ServeHTTP(latestManifest, httptest.NewRequest("GET", "/kb/yi-flow-core/latest/manifest.json", nil))
	if latestManifest.Code != http.StatusOK || !strings.Contains(latestManifest.Body.String(), "2026.06.26.publish.001") {
		t.Fatalf("latest manifest after rollback status=%d body=%s", latestManifest.Code, latestManifest.Body.String())
	}

	auditLog, err := os.ReadFile(filepath.Join(storageDir, "kb", "yi-flow-core", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	auditText := string(auditLog)
	for _, expected := range []string{"draft_publish", "rollback_latest", "2026.06.26.publish.001", "2026.06.26.publish.002", "sha256:", "gate_status", "published_at", "rollback_at"} {
		if !strings.Contains(auditText, expected) {
			t.Fatalf("audit log missing %q: %s", expected, auditText)
		}
	}
	if strings.Contains(auditText, "test-admin-token") {
		t.Fatalf("audit log leaked token: %s", auditText)
	}
}

func TestAdminDraftBulkImportExportReviewQueueAndGateBoundary(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	body := `{
	  "chunks": [
	    {
	      "chunk_id": "bulk-alpha",
	      "title": "Bulk Alpha",
	      "path": "bulk/alpha",
	      "source": "yi-flow-core",
	      "content": "Bulk alpha content is ready for review queue operations.",
	      "review_status": "needs_review",
	      "citation_url": "https://yi-flow.com/docs/bulk-alpha",
	      "citation_title": "Bulk Alpha",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    },
	    {
	      "chunk_id": "bulk-missing-citation",
	      "title": "Bulk Missing Citation",
	      "path": "bulk/missing",
	      "source": "yi-flow-core",
	      "content": "Bulk missing citation content should stay blocked by quality gates.",
	      "review_status": "draft"
	    },
	    {
	      "chunk_id": "bulk-approved",
	      "title": "Bulk Approved",
	      "path": "bulk/approved",
	      "source": "yi-flow-core",
	      "content": "Bulk approved content keeps the export path deterministic.",
	      "review_status": "approved",
	      "citation_url": "https://yi-flow.com/docs/bulk-approved",
	      "citation_title": "Bulk Approved",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    }
	  ],
	  "prompts": [
	    {
	      "id": "bulk-alpha-question",
	      "title": "Bulk alpha",
	      "question": "What is ready for review queue operations?",
	      "expected_chunk_ids": ["bulk-alpha"],
	      "answerability": "answerable",
	      "answerable": true
	    }
	  ],
	  "citations": {"citations":[]}
	}`

	dryRun := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk/import?dry_run=1", bytes.NewBufferString(body))
	dryRun.Header.Set("Authorization", "Bearer test-admin-token")
	dryRun.Header.Set("Content-Type", "application/json")
	dryRunResponse := httptest.NewRecorder()
	handler.ServeHTTP(dryRunResponse, dryRun)
	if dryRunResponse.Code != http.StatusOK {
		t.Fatalf("bulk import dry-run status=%d body=%s", dryRunResponse.Code, dryRunResponse.Body.String())
	}
	var validation struct {
		DryRun        bool `json:"dry_run"`
		WouldSave     bool `json:"would_save"`
		ChunkCount    int  `json:"chunk_count"`
		QualityReport struct {
			Status       string `json:"status"`
			BlockPublish bool   `json:"block_publish"`
		} `json:"quality_report"`
	}
	if err := json.Unmarshal(dryRunResponse.Body.Bytes(), &validation); err != nil {
		t.Fatalf("decode dry-run validation: %v", err)
	}
	if !validation.DryRun || validation.WouldSave || validation.ChunkCount != 3 || validation.QualityReport.Status != "failed" || !validation.QualityReport.BlockPublish {
		t.Fatalf("unexpected dry-run validation = %+v", validation)
	}
	getBeforeSave := httptest.NewRecorder()
	requestBeforeSave := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk", nil)
	requestBeforeSave.Header.Set("Authorization", "Bearer test-admin-token")
	handler.ServeHTTP(getBeforeSave, requestBeforeSave)
	if getBeforeSave.Code != http.StatusNotFound {
		t.Fatalf("bulk dry-run should not save draft status=%d body=%s", getBeforeSave.Code, getBeforeSave.Body.String())
	}

	importRequest := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk/import", bytes.NewBufferString(body))
	importRequest.Header.Set("Authorization", "Bearer test-admin-token")
	importRequest.Header.Set("Content-Type", "application/json")
	importResponse := httptest.NewRecorder()
	handler.ServeHTTP(importResponse, importRequest)
	if importResponse.Code != http.StatusCreated {
		t.Fatalf("bulk import status=%d body=%s", importResponse.Code, importResponse.Body.String())
	}

	exportRequest := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk/export", nil)
	exportRequest.Header.Set("Authorization", "Bearer test-admin-token")
	exportResponse := httptest.NewRecorder()
	handler.ServeHTTP(exportResponse, exportRequest)
	if exportResponse.Code != http.StatusOK {
		t.Fatalf("bulk export status=%d body=%s", exportResponse.Code, exportResponse.Body.String())
	}
	for _, expected := range []string{`"chunks"`, `"prompts"`, `"citations"`} {
		if !strings.Contains(exportResponse.Body.String(), expected) {
			t.Fatalf("bulk export missing %s: %s", expected, exportResponse.Body.String())
		}
	}

	queueRequest := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk/review-queue?filter=missing_citation", nil)
	queueRequest.Header.Set("Authorization", "Bearer test-admin-token")
	queueResponse := httptest.NewRecorder()
	handler.ServeHTTP(queueResponse, queueRequest)
	if queueResponse.Code != http.StatusOK {
		t.Fatalf("review queue status=%d body=%s", queueResponse.Code, queueResponse.Body.String())
	}
	if !strings.Contains(queueResponse.Body.String(), "bulk-missing-citation") || strings.Contains(queueResponse.Body.String(), "bulk-approved") {
		t.Fatalf("missing citation queue mismatch: %s", queueResponse.Body.String())
	}

	gateRequest := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.bulk/quality-gates", nil)
	gateRequest.Header.Set("Authorization", "Bearer test-admin-token")
	gateResponse := httptest.NewRecorder()
	handler.ServeHTTP(gateResponse, gateRequest)
	if gateResponse.Code != http.StatusOK || !strings.Contains(gateResponse.Body.String(), `"block_publish":true`) {
		t.Fatalf("bulk import must not bypass quality gates status=%d body=%s", gateResponse.Code, gateResponse.Body.String())
	}
}

func TestAdminDraftReviewReportSamplesThirtyChunksAndCounts(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 35)
	for index := 0; index < 35; index++ {
		content := "Review report sampled content " + strconv.Itoa(index)
		if index == 33 || index == 34 {
			content = "Review report duplicate sampled content"
		}
		chunk := map[string]any{
			"chunk_id":       "review-sample-" + strconv.Itoa(index),
			"title":          "Review Sample " + strconv.Itoa(index),
			"path":           "review/sample/" + strconv.Itoa(index),
			"source":         "yi-flow-core",
			"content":        content,
			"review_status":  "approved",
			"citation_url":   "https://yi-flow.com/docs/review/" + strconv.Itoa(index),
			"citation_title": "Review Sample " + strconv.Itoa(index),
			"source_name":    "yi-flow docs",
			"license":        "reviewed internal knowledge",
			"source_policy":  "reviewed yi-flow product chunks only",
		}
		if index == 1 {
			delete(chunk, "citation_url")
			delete(chunk, "license")
			delete(chunk, "source_policy")
		}
		chunks = append(chunks, chunk)
	}
	body, err := json.Marshal(map[string]any{
		"chunks": chunks,
		"prompts": []map[string]any{{
			"id":                 "review-sample-question",
			"title":              "Review sample",
			"question":           "What does the review report sample?",
			"expected_chunk_ids": []string{"review-sample-0"},
			"answerability":      "answerable",
			"answerable":         true,
		}},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode review report draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.review-report", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save review report draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.review-report/review-report", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("review report status=%d body=%s", response.Code, response.Body.String())
	}
	var report struct {
		ChunkCount           int            `json:"chunk_count"`
		SampleCount          int            `json:"sample_count"`
		MissingCitationCount int            `json:"missing_citation_count"`
		DuplicateCount       int            `json:"duplicate_count"`
		ContaminationCount   int            `json:"contamination_count"`
		GoldenPromptCount    int            `json:"golden_prompt_count"`
		SourceCounts         map[string]int `json:"source_counts"`
		LicenseCounts        map[string]int `json:"license_counts"`
		SampledChunks        []struct {
			ChunkID string `json:"chunk_id"`
		} `json:"sampled_chunks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode review report: %v", err)
	}
	if report.ChunkCount != 35 || report.SampleCount != 30 || len(report.SampledChunks) != 30 || report.SampledChunks[0].ChunkID != "review-sample-0" {
		t.Fatalf("unexpected sample summary = %+v", report)
	}
	if report.MissingCitationCount != 1 || report.DuplicateCount != 1 || report.ContaminationCount != 0 || report.GoldenPromptCount != 1 || report.SourceCounts["yi-flow-core"] != 35 || report.LicenseCounts["reviewed internal knowledge"] != 34 {
		t.Fatalf("unexpected review counts = %+v", report)
	}
}

func TestAdminDraftChunkListPaginatesThousandChunks(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	chunks := make([]map[string]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		chunks = append(chunks, map[string]any{
			"chunk_id":      "page-chunk-" + strconv.Itoa(index),
			"title":         "Page Chunk " + strconv.Itoa(index),
			"path":          "page/chunk/" + strconv.Itoa(index),
			"source":        "yi-flow-core",
			"content":       "Pagination chunk content with enough length " + strconv.Itoa(index),
			"review_status": "approved",
		})
	}
	body, err := json.Marshal(map[string]any{
		"chunks":    chunks,
		"prompts":   []any{},
		"citations": map[string]any{"citations": []any{}},
	})
	if err != nil {
		t.Fatalf("encode paginated draft: %v", err)
	}
	saveResponse := saveDraftJSON(t, handler, "yi-flow-core", "2026.06.26.pagination", string(body))
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save pagination draft status=%d body=%s", saveResponse.Code, saveResponse.Body.String())
	}

	request := httptest.NewRequest("GET", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.pagination/chunks?limit=50&offset=950", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("paginated chunks status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded struct {
		Total      int `json:"total"`
		Matched    int `json:"matched"`
		Limit      int `json:"limit"`
		Offset     int `json:"offset"`
		NextOffset int `json:"next_offset"`
		Chunks     []struct {
			ChunkID string `json:"chunk_id"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode paginated chunks: %v", err)
	}
	if decoded.Total != 1000 || decoded.Matched != 1000 || decoded.Limit != 50 || decoded.Offset != 950 || decoded.NextOffset != -1 || len(decoded.Chunks) != 50 || decoded.Chunks[0].ChunkID != "page-chunk-950" {
		t.Fatalf("unexpected pagination response = %+v", decoded)
	}
}

func TestAdminDraftBulkImportValidationReturnsFieldErrors(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest("POST", "/admin/api/kb/yi-flow-core/drafts/2026.06.26.field-errors/import?dry_run=1", bytes.NewBufferString(`{
	  "chunks": [{
	    "chunk_id": "field-error",
	    "title": "",
	    "path": "field/error",
	    "source": "yi-flow-core",
	    "content": "Field errors should point to the title field."
	  }],
	  "prompts": [],
	  "citations": {"citations":[]}
	}`))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("field error status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded struct {
		Error       string `json:"error"`
		FieldErrors []struct {
			Field       string `json:"field"`
			Remediation string `json:"remediation"`
		} `json:"field_errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode field errors: %v", err)
	}
	if decoded.Error == "" || len(decoded.FieldErrors) != 1 || decoded.FieldErrors[0].Field != "chunks[0].title" || decoded.FieldErrors[0].Remediation == "" {
		t.Fatalf("unexpected field errors = %+v body=%s", decoded, response.Body.String())
	}
}

func TestAdminPageIsServedByTheKnowledgeBaseService(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/admin/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("admin page status=%d body=%s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("Knowledge Pack Admin")) {
		t.Fatalf("admin page missing title: %s", response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("Chunk Studio")) {
		t.Fatalf("admin page missing Chunk Studio mainline")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("自研 chunk 内容创建和管理后台")) {
		t.Fatalf("admin page missing self-hosted chunk authoring description")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("保存草稿")) {
		t.Fatalf("admin page missing draft save control")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("预览草稿 chunk")) {
		t.Fatalf("admin page missing draft preview control")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/drafts/")) {
		t.Fatalf("admin page missing draft api usage")
	}
	for _, expected := range []string{
		"Chunk search",
		"Review status",
		"Review filter",
		"Page size",
		"limit=100",
		"offset=0",
		"next_offset",
		"preserveUnsavedDraftOnError",
		"@media (max-width: 720px)",
		"field_errors",
		"创建 chunk",
		"更新 chunk",
		"复制 chunk",
		"删除 chunk",
		"unsaved changes",
		"Citation URL",
		"Citation title",
		"Source name",
		"License",
		"Source policy",
		"审计 source metadata",
		"Prompts / golden questions",
		"Expected chunk IDs",
		"Answerability",
		"创建 prompt",
		"运行 prompt 预览",
		"Draft retrieval preview",
		"Draft retrieval question",
		"运行 draft retrieval preview",
		"Quality Gates",
		"运行 quality gates",
		"draftQualityGateReport",
		"/quality-gates",
		"block_publish",
		"top5_hit_rate",
		"citation_rate",
		"duplicate_answer_rate",
		"refusal_pass_rate",
		"missing_citation_count",
		"unsupported_entity_count",
		"Dry-run Build",
		"运行 draft dry-run build",
		"draftDryRunBuildReport",
		"/build-dry-run",
		"package_hash",
		"manifest",
		"Generated files",
		"Manifest preview",
		"preview_url",
		"发布 draft 为 latest",
		"publishDraftLatest",
		"/publish",
		"content_hash",
		"gate_status",
		"Moegirl FAQ import",
		"导入 Moegirl FAQ draft",
		"检查 Moegirl draft",
		"moegirlDraftImportReport",
		"/moegirl/import-draft",
		"/moegirl-review",
		"full_mirror_suspect_count",
		"accepted_pages_required",
		"faq_chunks_required",
		"golden_questions_required",
		"Batch review",
		"Canonical draft JSON",
		"验证批量导入",
		"导入 draft JSON",
		"导出 draft JSON",
		"加载 review queue",
		"生成 review report",
		"/import",
		"/export",
		"/review-queue",
		"/review-report",
		"missing_citation_count",
		"duplicate_count",
		"contamination_count",
		"golden_prompt_count",
	} {
		if !bytes.Contains(response.Body.Bytes(), []byte(expected)) {
			t.Fatalf("admin page missing chunk editor control %q", expected)
		}
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/admin/api/kb/")) {
		t.Fatalf("admin page missing admin api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("内容预览")) {
		t.Fatalf("admin page missing knowledge content preview")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/preview")) {
		t.Fatalf("admin page missing preview api usage")
	}
	for _, removed := range []string{
		"RAGFlow 知识包发布",
		"WeKnora 知识包发布",
		"Reviewed WeKnora export JSON",
		"MaxKB",
		"https://rag.yi-flow.com",
		"/ragflow/export-dry-run",
		"/ragflow/export-publish",
	} {
		if bytes.Contains(response.Body.Bytes(), []byte(removed)) {
			t.Fatalf("admin page should not expose external backend primary path %q", removed)
		}
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("最近导出版本")) {
		t.Fatalf("admin page missing last export version status")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("最近质量门禁")) {
		t.Fatalf("admin page missing quality gate status")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("RAG 对比")) {
		t.Fatalf("admin page missing RAG compare")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/rag/compare")) {
		t.Fatalf("admin page missing RAG compare api usage")
	}
	for _, removed := range []string{"chunks JSON", "prompts JSON", "citations JSON"} {
		if bytes.Contains(response.Body.Bytes(), []byte(removed)) {
			t.Fatalf("admin page should not expose raw JSON textarea label %q", removed)
		}
	}
}

func TestAdminPageFollowsProjectDesignSpec(t *testing.T) {
	design, err := os.ReadFile("../../DESIGN.md")
	if err != nil {
		t.Fatalf("project DESIGN.md is required: %v", err)
	}
	for _, expected := range []string{
		"name: Airbnb-design-analysis",
		"primary: \"#ff385c\"",
		"rounded.full",
		"Airbnb Cereal VF",
	} {
		if !bytes.Contains(design, []byte(expected)) {
			t.Fatalf("DESIGN.md missing %q", expected)
		}
	}

	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/admin/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("admin page status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"--color-primary: #ff385c",
		"--color-canvas: #ffffff",
		"--radius-full: 9999px",
		"Airbnb Cereal VF",
		"border-radius: var(--radius-full)",
		"Chunk Studio",
	} {
		if !bytes.Contains(response.Body.Bytes(), []byte(expected)) {
			t.Fatalf("admin page missing design token %q", expected)
		}
	}
}

func TestHealthzReportsOK(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.String() != "ok\n" {
		t.Fatalf("health body=%q", response.Body.String())
	}
}

func validManifest(version string) []byte {
	return []byte(`{
	  "schema_version": "knowledge-pack-manifest/v1",
	  "kb_id": "yi-flow-core",
	  "version": "` + version + `",
	  "content_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "signature": "ed25519:signature",
	  "chunk_schema_version": "chunk-v1",
	  "embedding_model": "Qwen3-Embedding-0.6B",
	  "embedding_dim": 1024,
	  "created_at": "2026-06-20T09:30:00Z",
	  "llm_recommended": ["Qwen3-4B-GGUF"],
	  "files": {
	    "chunks": [{"path": "chunks.sqlite", "sha256": "sha256:chunks", "byte_size": 12}],
	    "fts": [{"path": "fts.sqlite", "sha256": "sha256:fts", "byte_size": 12}],
	    "vector": [{"path": "vector.index", "sha256": "sha256:vector", "byte_size": 12}],
	    "assets": [],
	    "citations": [{"path": "citations.json", "sha256": "sha256:citations", "byte_size": 12}],
	    "prompts": [{"path": "prompts.json", "sha256": "sha256:prompts", "byte_size": 12}]
	  },
	  "security": {
	    "executable_code_allowed": false,
	    "remote_code_policy": "forbidden"
	  }
	}`)
}

func publishVersion(
	t *testing.T,
	handler http.Handler,
	kbID string,
	version string,
	manifest []byte,
	packageBytes []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	publish := multipartRequest(t, "POST", "/admin/api/kb/"+kbID+"/versions", map[string]string{
		"version": version,
	}, map[string][]byte{
		"manifest": manifest,
		"package":  packageBytes,
	})
	publish.Header.Set("Authorization", "Bearer test-admin-token")
	publishResponse := httptest.NewRecorder()
	handler.ServeHTTP(publishResponse, publish)
	return publishResponse
}

func saveDraftJSON(t *testing.T, handler http.Handler, kbID string, version string, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest("PUT", "/admin/api/kb/"+kbID+"/drafts/"+version, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-admin-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func qualityCheckFailed(checks []struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Severity    string   `json:"severity"`
	Count       int      `json:"count"`
	ChunkIDs    []string `json:"chunk_ids"`
	PromptIDs   []string `json:"prompt_ids"`
	Remediation string   `json:"remediation"`
}, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == "failed" && check.Count > 0 && check.Remediation != "" {
			return true
		}
	}
	return false
}

func fileListContains(files []struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}, target string) bool {
	for _, file := range files {
		if file.Path == target && file.Size > 0 {
			return true
		}
	}
	return false
}

func qualityGateFailureContains(checks []struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Remediation string `json:"remediation"`
}, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == "failed" && check.Remediation != "" {
			return true
		}
	}
	return false
}

func savePublishableDraft(t *testing.T, handler http.Handler, kbID string, version string, marker string) {
	t.Helper()
	chunkID := "publishable-" + strings.ReplaceAll(version, ".", "-")
	body := `{
	  "chunks": [
	    {
	      "chunk_id": "` + chunkID + `",
	      "title": "Publishable draft ` + marker + `",
	      "path": "draft/publish/` + version + `",
	      "source": "yi-flow-core",
	      "content": "Publishable draft ` + marker + ` keeps signed artifacts stable after dry run before latest publish.",
	      "citation_url": "https://yi-flow.com/docs/publish/` + version + `",
	      "citation_title": "Publishable draft ` + marker + `",
	      "source_name": "yi-flow docs",
	      "license": "reviewed internal knowledge",
	      "source_policy": "reviewed yi-flow product chunks only"
	    }
	  ],
	  "prompts": [
	    {
	      "id": "publishable-answerable-` + strings.ReplaceAll(version, ".", "-") + `",
	      "title": "Publishable answerable",
	      "question": "What keeps signed artifacts stable after dry run?",
	      "expected_chunk_ids": ["` + chunkID + `"],
	      "answerability": "answerable",
	      "answerable": true
	    },
	    {
	      "id": "publishable-refusal-` + strings.ReplaceAll(version, ".", "-") + `",
	      "title": "Publishable refusal",
	      "question": "What is the weather in Tokyo?",
	      "answerability": "ood",
	      "answerable": false
	    }
	  ],
	  "citations": {"citations":[]}
	}`
	response := saveDraftJSON(t, handler, kbID, version, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("save publishable draft status=%d body=%s", response.Code, response.Body.String())
	}
}

func dryRunDraftBuild(t *testing.T, handler http.Handler, kbID string, version string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("POST", "/admin/api/kb/"+kbID+"/drafts/"+version+"/build-dry-run?limit=50", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func publishDraftFromStudio(t *testing.T, handler http.Handler, kbID string, version string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("POST", "/admin/api/kb/"+kbID+"/drafts/"+version+"/publish", nil)
	request.Header.Set("Authorization", "Bearer test-admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func multipartRequest(
	t *testing.T,
	method string,
	target string,
	fields map[string]string,
	files map[string][]byte,
) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	for key, value := range files {
		part, err := writer.CreateFormFile(key, key)
		if err != nil {
			t.Fatalf("create file %s: %v", key, err)
		}
		if _, err := io.Copy(part, bytes.NewReader(value)); err != nil {
			t.Fatalf("write file %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

type testChunk struct {
	ChunkID string
	Title   string
	Path    string
	Source  string
	Content string
}

func validKnowledgePackZip(t *testing.T, chunkID string) []byte {
	t.Helper()

	return knowledgePackZip(t, []testChunk{
		{
			ChunkID: chunkID,
			Title:   "yi-flow 测试知识",
			Path:    "yi-flow/test/" + chunkID,
			Source:  "yi-flow-core",
			Content: "这是用于测试上传、列表和回滚流程的 yi-flow 内部知识包内容。",
		},
	})
}

func knowledgePackZip(t *testing.T, chunks []testChunk) []byte {
	t.Helper()

	root := t.TempDir()
	databasePath := filepath.Join(root, "chunks.sqlite")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`
		CREATE VIRTUAL TABLE chunks USING fts5(
			chunk_id UNINDEXED,
			title,
			path UNINDEXED,
			source UNINDEXED,
			content,
			tokenize = 'trigram'
		);
	`); err != nil {
		t.Fatalf("create chunks table: %v", err)
	}

	statement, err := database.Prepare("INSERT INTO chunks(chunk_id, title, path, source, content) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}
	defer statement.Close()

	for _, chunk := range chunks {
		if _, err := statement.Exec(chunk.ChunkID, chunk.Title, chunk.Path, chunk.Source, chunk.Content); err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	addZipFile(t, writer, "chunks.sqlite", mustReadFile(t, databasePath))
	addZipFile(t, writer, "citations.json", []byte(`{"citations":[]}`))
	addZipFile(t, writer, "prompts.json", []byte(`{"prompts":[]}`))
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return body.Bytes()
}

func addZipFile(t *testing.T, writer *zip.Writer, name string, data []byte) {
	t.Helper()

	file, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %s: %v", name, err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("write zip entry %s: %v", name, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return data
}

func mustBase64(t *testing.T, value string) []byte {
	t.Helper()

	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return data
}
