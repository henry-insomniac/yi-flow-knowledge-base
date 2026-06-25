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
