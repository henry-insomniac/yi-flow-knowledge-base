package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"yi-flow/knowledge-base/internal/server"
)

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
