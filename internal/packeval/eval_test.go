package packeval

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

func TestEvaluateFilesReportsRetrievalCitationAndRefusalMetrics(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")
	goldenPath := filepath.Join(root, "golden.json")

	writeEvalManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.eval")
	writeEvalPackage(t, packagePath, []evalTestChunk{
		{
			ChunkID: "moegirl-page-1399-faq-overview",
			Title:   "初音未来 · faq_overview",
			Path:    "moegirl/faq/初音未来/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "初音未来是由 Crypton Future Media 企划、开发、贩售的 VOCALOID 声音库软件及其拟人化形象。",
		},
		{
			ChunkID: "moegirl-page-236-faq-overview",
			Title:   "东方Project · faq_overview",
			Path:    "moegirl/faq/东方Project/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "东方Project 是由 ZUN 创作的一系列弹幕射击游戏及其衍生作品。",
		},
	})
	writeGolden(t, goldenPath, []GoldenQuestion{
		{
			ID:               "entity-001",
			Category:         "entity_overview",
			Question:         "初音未来是什么？",
			ExpectedChunkIDs: []string{"moegirl-page-1399-faq-overview"},
			Answerable:       true,
		},
		{
			ID:               "relation-001",
			Category:         "relation_list",
			Question:         "东方Project 是谁创作的？",
			ExpectedChunkIDs: []string{"moegirl-page-236-faq-overview"},
			Answerable:       true,
			Regression:       true,
		},
		{
			ID:         "refusal-001",
			Category:   "out_of_domain",
			Question:   "yi-flow 知识包更新路径是什么？",
			Answerable: false,
		},
	})

	report, err := EvaluateFiles(manifestPath, packagePath, goldenPath, Options{TopK: 5})
	if err != nil {
		t.Fatalf("evaluate files: %v", err)
	}
	if report.KBID != "moegirl-acgn-faq" || report.Version != "2026.06.eval" {
		t.Fatalf("report identity = %+v", report)
	}
	if report.TotalQuestions != 3 ||
		report.AnswerableQuestions != 2 ||
		report.RefusalQuestions != 1 ||
		report.RegressionQuestions != 1 {
		t.Fatalf("question counts = %+v", report)
	}
	if report.Top1HitRate != 1 || report.Top5HitRate != 1 || report.CitationRate != 1 || report.RefusalPassRate != 1 {
		t.Fatalf("rates = %+v", report)
	}
	if report.DuplicateAnswerRate != 0 || report.UnsupportedEntityCount != 0 {
		t.Fatalf("quality metrics = %+v", report)
	}
}

func TestEvaluateFilesMatchesExpectedRedirectAliasInChunkContent(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")
	goldenPath := filepath.Join(root, "golden.json")

	writeEvalManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.redirect-eval")
	writeEvalPackage(t, packagePath, []evalTestChunk{
		{
			ChunkID: "moegirl-page-467872-faq-overview",
			Title:   "雷电影 · faq_overview",
			Path:    "moegirl/faq/雷电影/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "【别名/检索词】雷电影、雷电将军\n雷电将军是米哈游研发的游戏《原神》及其衍生作品的登场角色。",
		},
	})
	writeGolden(t, goldenPath, []GoldenQuestion{
		{
			ID:             "entity-redirect-001",
			Category:       "entity_overview",
			Question:       "雷电将军是什么角色？",
			ExpectedTitles: []string{"雷电将军"},
			Answerable:     true,
		},
	})

	report, err := EvaluateFiles(manifestPath, packagePath, goldenPath, Options{TopK: 5})
	if err != nil {
		t.Fatalf("evaluate files: %v", err)
	}
	if report.Top5HitRate != 1 || report.CitationRate != 1 || report.UnsupportedEntityCount != 0 {
		t.Fatalf("redirect alias metrics = %+v", report)
	}
}

func TestEvaluateFilesFallbackPrefersTitleSignalAndRejectsTitlelessNoise(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")
	goldenPath := filepath.Join(root, "golden.json")

	writeEvalManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.title-signal")
	writeEvalPackage(t, packagePath, []evalTestChunk{
		{
			ChunkID: "moegirl-page-437228-faq-overview",
			Title:   "甘雨 · faq_overview",
			Path:    "moegirl/faq/甘雨/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "甘雨是米哈游研发的游戏《原神》及其衍生作品的登场角色。",
		},
		{
			ChunkID: "moegirl-page-288956-faq-overview",
			Title:   "2010版链缘无现里 · faq_overview",
			Path:    "moegirl/faq/2010版链缘无现里/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "该页面包含游戏、角色、配置等常见词，但标题与查询实体无关。",
		},
		{
			ChunkID: "moegirl-page-217361-faq-overview",
			Title:   "2016 -Third cosmic velocity- · faq_overview",
			Path:    "moegirl/faq/2016-Third-cosmic-velocity/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "该页面包含 iOS 查询里常见的 os 字母片段，但不应成为 Moegirl 命中。",
		},
		{
			ChunkID: "moegirl-page-24101-faq-overview",
			Title:   "163更新姬 · faq_overview",
			Path:    "moegirl/faq/163更新姬/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "该页面标题包含更新二字，但不能命中 yi-flow 知识包更新路径问题。",
		},
		{
			ChunkID: "moegirl-page-317368-faq-overview",
			Title:   "(K)NoW NAME · faq_overview",
			Path:    "moegirl/faq/K-NoW-NAME/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "括号内单字母 K 不能命中 OpenAI API key 这类域外问题。",
		},
	})
	writeGolden(t, goldenPath, []GoldenQuestion{
		{
			ID:             "relation-title-001",
			Category:       "relation_list",
			Question:       "甘雨属于哪个游戏？",
			ExpectedTitles: []string{"甘雨"},
			Answerable:     true,
		},
		{
			ID:         "refusal-titleless-001",
			Category:   "out_of_domain",
			Question:   "如何配置 iOS 真机测试脚本？",
			Answerable: false,
		},
		{
			ID:         "refusal-common-title-001",
			Category:   "out_of_domain",
			Question:   "yi-flow 知识包更新路径是什么？",
			Answerable: false,
		},
		{
			ID:         "refusal-single-letter-001",
			Category:   "out_of_domain",
			Question:   "OpenAI API key 应该怎么设置？",
			Answerable: false,
		},
	})

	report, err := EvaluateFiles(manifestPath, packagePath, goldenPath, Options{TopK: 5})
	if err != nil {
		t.Fatalf("evaluate files: %v", err)
	}
	if report.Top1HitRate != 1 || report.Top5HitRate != 1 || report.RefusalPassRate != 1 {
		t.Fatalf("title-signal metrics = %+v", report)
	}
}

func TestEvaluateFilesFallbackAliasSignalOutranksFTSNoise(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	packagePath := filepath.Join(root, "knowledge-pack.zip")
	goldenPath := filepath.Join(root, "golden.json")

	writeEvalManifest(t, manifestPath, "moegirl-acgn-faq", "2026.06.alias-signal")
	writeEvalPackage(t, packagePath, []evalTestChunk{
		{
			ChunkID: "moegirl-page-1399-faq-overview",
			Title:   "初音未来 · faq_overview",
			Path:    "moegirl/faq/初音未来/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "【别名/检索词】初音未来、Miku、MIKU\n初音未来是 Crypton Future Media 的 VOCALOID 声音库及角色形象。",
		},
		{
			ChunkID: "moegirl-page-317368-faq-overview",
			Title:   "MikuMikuDance · faq_overview",
			Path:    "moegirl/faq/MikuMikuDance/是什么",
			Source:  "萌娘百科 (Moegirlpedia)",
			Content: "MikuMikuDance 是包含 Miku 词面的软件条目，但不是这个问题要找的人物条目。",
		},
	})
	writeGolden(t, goldenPath, []GoldenQuestion{
		{
			ID:             "alias-signal-001",
			Category:       "alias_redirect",
			Question:       "Miku 指的是谁？",
			ExpectedTitles: []string{"初音未来"},
			Answerable:     true,
		},
	})

	report, err := EvaluateFiles(manifestPath, packagePath, goldenPath, Options{TopK: 5})
	if err != nil {
		t.Fatalf("evaluate files: %v", err)
	}
	if report.Top1HitRate != 1 || report.CitationRate != 1 || report.UnsupportedEntityCount != 0 {
		t.Fatalf("alias-signal metrics = %+v", report)
	}
}

type evalTestChunk struct {
	ChunkID string
	Title   string
	Path    string
	Source  string
	Content string
}

func writeEvalManifest(t *testing.T, path string, kbID string, version string) {
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

func writeEvalPackage(t *testing.T, path string, chunks []evalTestChunk) {
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
	addEvalZipFile(t, writer, "chunks.sqlite", mustEvalReadFile(t, chunksPath))
	addEvalZipFile(t, writer, "citations.json", citationData)
	addEvalZipFile(t, writer, "prompts.json", []byte(`{"prompts":[]}`))
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
}

func writeGolden(t *testing.T, path string, questions []GoldenQuestion) {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{"questions": questions}, "", "  ")
	if err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

func addEvalZipFile(t *testing.T, writer *zip.Writer, name string, data []byte) {
	t.Helper()

	file, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %s: %v", name, err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("write zip entry %s: %v", name, err)
	}
}

func mustEvalReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
