package packaudit

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAuditFilesRejectsMoegirlContaminationInYiFlowCore(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")

	writeManifest(t, manifestPath, "yi-flow-core", "2026.06.audit")
	writePackage(t, packagePath, []auditTestChunk{
		{
			ChunkID: "moegirl-page-1399",
			Title:   "初音未来",
			Path:    "moegirl/summary/初音未来",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "污染正文不应出现在错误信息里",
		},
	}, []byte(`{
	  "citations": [
	    {
	      "chunk_id": "moegirl-page-1399",
	      "source": "萌娘百科 (Moegirlpedia)",
	      "url": "https://zh.moegirl.org.cn/初音未来"
	    }
	  ]
	}`))

	report, err := AuditFiles(manifestPath, packagePath)
	if err == nil {
		t.Fatalf("expected contamination audit to fail")
	}
	if report.KBID != "yi-flow-core" || report.Version != "2026.06.audit" {
		t.Fatalf("report identity = %+v", report)
	}
	if !report.Files.ManifestJSON || !report.Files.KnowledgePackZIP || !report.Files.ChunksSQLite || !report.Files.PromptsJSON || !report.Files.CitationsJSON {
		t.Fatalf("file coverage = %+v", report.Files)
	}
	if report.SourceFamilies["moegirl"] == 0 {
		t.Fatalf("source family counts = %+v", report.SourceFamilies)
	}
	if len(report.Violations) == 0 {
		t.Fatalf("expected at least one violation: %+v", report)
	}
	if bytes.Contains([]byte(err.Error()), []byte("污染正文")) {
		t.Fatalf("audit error leaked chunk content: %v", err)
	}
}

func TestAuditFilesRejectsYiFlowInternalDocsInMoegirlPack(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")

	writeManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.audit")
	writePackage(t, packagePath, []auditTestChunk{
		{
			ChunkID: "yi-flow-core-agent-answer-flow-001",
			Title:   "yi-flow Agent 回答链路",
			Path:    "yi-flow/core/agent-answer-flow",
			Source:  "yi-flow-core",
			Content: "yi-flow 内部文档不应进入 Moegirl 包。",
		},
	}, []byte(`{"citations":[{"chunk_id":"yi-flow-core-agent-answer-flow-001","source":"yi-flow-core"}]}`))

	report, err := AuditFiles(manifestPath, packagePath)
	if err == nil {
		t.Fatalf("expected moegirl pack audit to reject internal yi-flow docs")
	}
	if report.InternalFamilies["yi_flow"] == 0 {
		t.Fatalf("internal family counts = %+v", report.InternalFamilies)
	}
	if len(report.Violations) == 0 || report.Violations[0].Code != "forbidden_internal_source_family" {
		t.Fatalf("violations = %+v", report.Violations)
	}
}

func TestAuditFilesRejectsMoegirlPackMissingSourceMetadata(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")

	writeManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.audit")
	writePackage(t, packagePath, []auditTestChunk{
		{
			ChunkID: "moegirl-page-1399",
			Title:   "初音未来",
			Path:    "moegirl/summary/初音未来",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "摘要内容。",
		},
	}, []byte(`{"citations":[{"chunk_id":"moegirl-page-1399","source":"萌娘百科 (Moegirlpedia)"}]}`))

	report, err := AuditFiles(manifestPath, packagePath)
	if err == nil {
		t.Fatalf("expected moegirl metadata audit to fail")
	}
	if len(report.Violations) == 0 {
		t.Fatalf("expected metadata violations: %+v", report)
	}
	found := false
	for _, violation := range report.Violations {
		if violation.Code == "missing_moegirl_source_metadata" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v", report.Violations)
	}
}

func TestAuditFilesReportsCleanYiFlowCoreCounts(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")

	writeManifest(t, manifestPath, "yi-flow-core", "2026.06.audit")
	writePackage(t, packagePath, []auditTestChunk{
		{
			ChunkID: "yi-flow-core-builder-001",
			Title:   "yi-flow 知识包构建流程",
			Path:    "yi-flow/core/builder",
			Source:  "yi-flow-core",
			Content: "服务端生成 chunks.sqlite、vector.index、knowledge-pack.zip 和 manifest.json。",
		},
	}, []byte(`{"citations":[{"chunk_id":"yi-flow-core-builder-001","source":"yi-flow-core","url":"https://example.com/yi-flow/core"}]}`))

	report, err := AuditFiles(manifestPath, packagePath)
	if err != nil {
		t.Fatalf("expected clean core audit to pass: %v report=%+v", err, report)
	}
	if report.Sources["yi-flow-core"] != 2 {
		t.Fatalf("source counts = %+v", report.Sources)
	}
	if report.Domains["example.com"] != 1 {
		t.Fatalf("domain counts = %+v", report.Domains)
	}
	if report.ChunkIDPrefixes["yi-flow-core-builder"] != 1 {
		t.Fatalf("chunk_id prefix counts = %+v", report.ChunkIDPrefixes)
	}
	if len(report.Violations) != 0 {
		t.Fatalf("violations = %+v", report.Violations)
	}
}

type auditTestChunk struct {
	ChunkID string
	Title   string
	Path    string
	Source  string
	Content string
}

func writeManifest(t *testing.T, path string, kbID string, version string) {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{
		"schema_version": "knowledge-pack-manifest/v1",
		"kb_id":          kbID,
		"version":        version,
		"files": map[string]any{
			"chunks":    []map[string]any{{"path": "chunks.sqlite"}},
			"fts":       []map[string]any{{"path": "chunks.sqlite"}},
			"vector":    []map[string]any{{"path": "vector.index"}},
			"assets":    []map[string]any{},
			"citations": []map[string]any{{"path": "citations.json"}},
			"prompts":   []map[string]any{{"path": "prompts.json"}},
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writePackage(t *testing.T, path string, chunks []auditTestChunk, citations []byte) {
	t.Helper()

	root := t.TempDir()
	chunksPath := filepath.Join(root, "chunks.sqlite")
	database, err := sql.Open("sqlite", chunksPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
		t.Fatalf("prepare chunk insert: %v", err)
	}
	for _, chunk := range chunks {
		if _, err := statement.Exec(chunk.ChunkID, chunk.Title, chunk.Path, chunk.Source, chunk.Content); err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("close statement: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	addAuditZipFile(t, writer, "chunks.sqlite", mustAuditReadFile(t, chunksPath))
	addAuditZipFile(t, writer, "citations.json", citations)
	addAuditZipFile(t, writer, "prompts.json", []byte(`{"prompts":[]}`))
	addAuditZipFile(t, writer, "vector.index", []byte("vector"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
}

func addAuditZipFile(t *testing.T, writer *zip.Writer, name string, data []byte) {
	t.Helper()

	file, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %s: %v", name, err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("write zip entry %s: %v", name, err)
	}
}

func mustAuditReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return data
}
