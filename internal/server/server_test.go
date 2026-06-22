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
	"strings"
	"testing"

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
	      "chunk_id": "anime-builder-001",
	      "title": "二次元角色人设要素",
	      "path": "anime/character/design",
	      "source": "yi-flow-anime",
	      "content": "二次元角色人设通常包含姓名、身份、外观关键词、服装轮廓、标志物、性格反差、目标和弱点。"
	    }
	  ],
	  "prompts": [
	    {
	      "id": "anime-character-check",
	      "title": "验证二次元人设",
	      "question": "二次元角色人设要素有哪些？"
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
	if !bytes.Contains(previewResponse.Body.Bytes(), []byte("二次元角色人设要素")) {
		t.Fatalf("preview missing generated chunk: %s", previewResponse.Body.String())
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
	packageBytes := []byte("signed package bytes")

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

func TestClientsCanListVersionsAndLatestVersion(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), []byte("v1"))
	publishVersion(t, handler, "yi-flow-core", "2026.06.20.002", validManifest("2026.06.20.002"), []byte("v2"))

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
	}
	if err := json.Unmarshal(response.Body.Bytes(), &publishResult); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if publishResult.KBID != "weknora-smoke" || publishResult.Version != "2026.06.22.weknora" || !publishResult.Latest || publishResult.ChunkCount != 1 || publishResult.CitationCount != 1 {
		t.Fatalf("publish result = %+v", publishResult)
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

func TestAdminCanRollbackLatestToExistingVersion(t *testing.T) {
	handler, err := server.NewHandler(server.Options{
		StorageDir: t.TempDir(),
		AdminToken: "test-admin-token",
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	publishVersion(t, handler, "yi-flow-core", "2026.06.20.001", validManifest("2026.06.20.001"), []byte("v1"))
	publishVersion(t, handler, "yi-flow-core", "2026.06.20.002", validManifest("2026.06.20.002"), []byte("v2"))

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
	if !bytes.Contains(response.Body.Bytes(), []byte("/admin/api/kb/")) {
		t.Fatalf("admin page missing admin api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("内容预览")) {
		t.Fatalf("admin page missing knowledge content preview")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/preview")) {
		t.Fatalf("admin page missing preview api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("知识包构建器")) {
		t.Fatalf("admin page missing knowledge pack builder")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/build-publish")) {
		t.Fatalf("admin page missing build publish api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("萌娘百科摘要知识包")) {
		t.Fatalf("admin page missing Moegirl summary pack builder")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/moegirl/build-publish")) {
		t.Fatalf("admin page missing Moegirl build publish api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("CC BY-NC-SA 3.0 CN")) {
		t.Fatalf("admin page missing Moegirl license notice")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("RAG 对比")) {
		t.Fatalf("admin page missing RAG compare")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/rag/compare")) {
		t.Fatalf("admin page missing RAG compare api usage")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("WeKnora 导出发布")) {
		t.Fatalf("admin page missing WeKnora export publisher")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("/weknora/export-publish")) {
		t.Fatalf("admin page missing WeKnora export publish api usage")
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
