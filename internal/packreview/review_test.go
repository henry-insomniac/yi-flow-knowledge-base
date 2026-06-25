package packreview

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBuildFilesCreatesHITLReviewMaterial(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")
	goldenPath := filepath.Join(root, "golden.json")

	writeReviewManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.review")
	chunks := make([]reviewTestChunk, 0, 35)
	for index := 1; index <= 35; index++ {
		chunks = append(chunks, reviewTestChunk{
			ChunkID: fmt.Sprintf("moegirl-page-%04d-faq-overview", index),
			Title:   fmt.Sprintf("测试角色%03d · faq_overview", index),
			Path:    fmt.Sprintf("moegirl/faq/测试角色%03d/是什么", index),
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: fmt.Sprintf("【FAQ类型】faq_overview\n【回答依据】测试角色%03d 是用于人工验收抽样的摘要型 FAQ chunk。\n【来源】萌娘百科 (Moegirlpedia)：https://zh.moegirl.org.cn/测试角色%03d\n【许可】CC BY-NC-SA 3.0 CN。", index, index),
		})
	}
	writeReviewPackage(t, packagePath, chunks)
	writeReviewGolden(t, goldenPath, 25)

	report, err := BuildFiles(manifestPath, packagePath, goldenPath, Options{
		SampleSize:         30,
		GoldenQuestionSize: 20,
	})
	if err != nil {
		t.Fatalf("build review report: %v", err)
	}
	if report.KBID != "moegirl-acgn-faq" || report.Version != "2026.06.review" {
		t.Fatalf("report identity = %+v", report)
	}
	if report.TotalChunks != 35 || len(report.SampleChunks) != 30 {
		t.Fatalf("sample counts total=%d sample=%d", report.TotalChunks, len(report.SampleChunks))
	}
	if len(report.GoldenQuestions) != 20 {
		t.Fatalf("golden sample len=%d", len(report.GoldenQuestions))
	}
	if report.Attribution.SourceCount["萌娘百科 (Moegirlpedia)"] != 35 ||
		report.Attribution.LicenseCount["CC BY-NC-SA 3.0 CN"] != 35 ||
		report.Attribution.MissingCitationCount != 0 {
		t.Fatalf("attribution = %+v", report.Attribution)
	}
	if report.ChunkTypeCounts["faq_overview"] != 35 {
		t.Fatalf("chunk type counts = %+v", report.ChunkTypeCounts)
	}
	if report.FullMirrorSuspectCount != 0 {
		t.Fatalf("full mirror suspects = %d", report.FullMirrorSuspectCount)
	}
	if report.NextExpansionRecommendation != "stay_at_300_until_hitl_approved" {
		t.Fatalf("next expansion recommendation=%q", report.NextExpansionRecommendation)
	}
}

type reviewTestChunk struct {
	ChunkID string
	Title   string
	Path    string
	Source  string
	Content string
}

func writeReviewManifest(t *testing.T, path string, kbID string, version string) {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{
		"schema_version": "knowledge-pack-manifest/v1",
		"kb_id":          kbID,
		"version":        version,
		"files": map[string]any{
			"chunks":    []map[string]any{{"path": "chunks.sqlite"}},
			"fts":       []map[string]any{{"path": "chunks.sqlite"}},
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

func writeReviewPackage(t *testing.T, path string, chunks []reviewTestChunk) {
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
		t.Fatalf("prepare insert: %v", err)
	}
	citations := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		if _, err := statement.Exec(chunk.ChunkID, chunk.Title, chunk.Path, chunk.Source, chunk.Content); err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
		citations = append(citations, map[string]any{
			"chunk_id":    chunk.ChunkID,
			"faq_type":    "faq_overview",
			"source":      "萌娘百科 (Moegirlpedia)",
			"license":     "CC BY-NC-SA 3.0 CN",
			"url":         "https://zh.moegirl.org.cn/" + chunk.Title,
			"revision_id": "1",
		})
	}
	if err := statement.Close(); err != nil {
		t.Fatalf("close statement: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	citationData, err := json.Marshal(map[string]any{"citations": citations})
	if err != nil {
		t.Fatalf("encode citations: %v", err)
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	addReviewZipFile(t, writer, "chunks.sqlite", mustReviewReadFile(t, chunksPath))
	addReviewZipFile(t, writer, "citations.json", citationData)
	addReviewZipFile(t, writer, "prompts.json", []byte(`{"prompts":[]}`))
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
}

func writeReviewGolden(t *testing.T, path string, count int) {
	t.Helper()

	questions := make([]map[string]any, 0, count)
	for index := 1; index <= count; index++ {
		questions = append(questions, map[string]any{
			"id":              fmt.Sprintf("hitl-%03d", index),
			"category":        "entity_overview",
			"question":        fmt.Sprintf("测试角色%03d是什么？", index),
			"expected_titles": []string{fmt.Sprintf("测试角色%03d", index)},
			"answerable":      true,
		})
	}
	data, err := json.MarshalIndent(map[string]any{"questions": questions}, "", "  ")
	if err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

func addReviewZipFile(t *testing.T, writer *zip.Writer, name string, data []byte) {
	t.Helper()

	file, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %s: %v", name, err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("write zip entry %s: %v", name, err)
	}
}

func mustReviewReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
