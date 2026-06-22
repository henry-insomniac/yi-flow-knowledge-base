package server_test

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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
	if publishResult.ChunkCount != 1 {
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
	} {
		if !strings.Contains(previewBody, expected) {
			t.Fatalf("preview missing %q: %s", expected, previewBody)
		}
	}
	if strings.Contains(previewBody, "知识包里关于【摘要】") {
		t.Fatalf("preview suggested question should not expose chunk metadata labels: %s", previewBody)
	}

	packageResponse := httptest.NewRecorder()
	handler.ServeHTTP(packageResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/versions/2026.06.22.101/knowledge-pack.zip", nil))
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	citations := readZipEntry(t, packageResponse.Body.Bytes(), "citations.json")
	for _, expected := range []string{
		`"license": "CC BY-NC-SA 3.0 CN"`,
		`"source": "萌娘百科 (Moegirlpedia)"`,
		`"url": "https://zh.moegirl.org.cn/初音未来"`,
		`"revision_id": "8535826"`,
	} {
		if !strings.Contains(string(citations), expected) {
			t.Fatalf("citations missing %q: %s", expected, string(citations))
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
	if publishResult.ChunkCount != 2 {
		t.Fatalf("chunk_count=%d body=%s", publishResult.ChunkCount, response.Body.String())
	}

	previewResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewResponse, httptest.NewRequest("GET", "/kb/moegirl-acgn-summary/latest/preview?limit=3", nil))
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
