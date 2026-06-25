package packreview

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type Options struct {
	SampleSize         int
	GoldenQuestionSize int
}

type Report struct {
	KBID                        string            `json:"kb_id"`
	Version                     string            `json:"version"`
	TotalChunks                 int               `json:"total_chunks"`
	SampleChunks                []SampleChunk     `json:"sample_chunks"`
	GoldenQuestions             []GoldenQuestion  `json:"golden_questions"`
	Attribution                 AttributionReport `json:"attribution"`
	ChunkTypeCounts             map[string]int    `json:"chunk_type_counts"`
	FullMirrorSuspectCount      int               `json:"full_mirror_suspect_count"`
	NextExpansionRecommendation string            `json:"next_expansion_recommendation"`
}

type SampleChunk struct {
	ChunkID        string `json:"chunk_id"`
	Title          string `json:"title"`
	Path           string `json:"path"`
	Source         string `json:"source"`
	FAQType        string `json:"faq_type,omitempty"`
	SourceURL      string `json:"source_url,omitempty"`
	License        string `json:"license,omitempty"`
	RevisionID     string `json:"revision_id,omitempty"`
	ContentPreview string `json:"content_preview"`
}

type GoldenQuestion struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Question string `json:"question"`
}

type AttributionReport struct {
	SourceCount          map[string]int `json:"source_count"`
	LicenseCount         map[string]int `json:"license_count"`
	MissingCitationCount int            `json:"missing_citation_count"`
}

func BuildFiles(manifestPath string, packagePath string, goldenPath string, options Options) (Report, error) {
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return Report{}, fmt.Errorf("read manifest: %w", err)
	}
	packageData, err := os.ReadFile(packagePath)
	if err != nil {
		return Report{}, fmt.Errorf("read package: %w", err)
	}
	goldenData, err := os.ReadFile(goldenPath)
	if err != nil {
		return Report{}, fmt.Errorf("read golden: %w", err)
	}
	return Build(manifestData, packageData, goldenData, options)
}

func Build(manifestData []byte, packageData []byte, goldenData []byte, options Options) (Report, error) {
	if options.SampleSize <= 0 {
		options.SampleSize = 30
	}
	if options.GoldenQuestionSize <= 0 {
		options.GoldenQuestionSize = 20
	}

	var manifest reviewManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Report{}, fmt.Errorf("decode manifest: %w", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(packageData), int64(len(packageData)))
	if err != nil {
		return Report{}, fmt.Errorf("open package: %w", err)
	}
	chunksFile := findReviewZipFile(archive.File, manifest.Files.Chunks, "chunks.sqlite")
	if chunksFile == nil {
		return Report{}, fmt.Errorf("chunks sqlite not found")
	}
	citationsFile := findReviewZipFile(archive.File, manifest.Files.Citations, "citations.json")
	if citationsFile == nil {
		return Report{}, fmt.Errorf("citations json not found")
	}

	chunks, err := reviewChunksFromSQLite(chunksFile)
	if err != nil {
		return Report{}, err
	}
	citations, attribution, err := reviewCitationsByChunkID(citationsFile)
	if err != nil {
		return Report{}, err
	}
	goldenQuestions, err := reviewGoldenQuestions(goldenData, options.GoldenQuestionSize)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		KBID:                        strings.TrimSpace(manifest.KBID),
		Version:                     strings.TrimSpace(manifest.Version),
		TotalChunks:                 len(chunks),
		Attribution:                 attribution,
		ChunkTypeCounts:             map[string]int{},
		NextExpansionRecommendation: "stay_at_300_until_hitl_approved",
		GoldenQuestions:             goldenQuestions,
	}

	for _, chunk := range chunks {
		citation, ok := citations[chunk.ChunkID]
		if !ok {
			report.Attribution.MissingCitationCount++
		}
		faqType := faqTypeFromChunk(chunk, citation)
		if faqType != "" {
			report.ChunkTypeCounts[faqType]++
		}
		if fullMirrorSuspect(chunk.Content) {
			report.FullMirrorSuspectCount++
		}
	}

	for _, chunk := range deterministicSample(chunks, options.SampleSize) {
		citation := citations[chunk.ChunkID]
		report.SampleChunks = append(report.SampleChunks, SampleChunk{
			ChunkID:        chunk.ChunkID,
			Title:          chunk.Title,
			Path:           chunk.Path,
			Source:         chunk.Source,
			FAQType:        faqTypeFromChunk(chunk, citation),
			SourceURL:      citation.URL,
			License:        citation.License,
			RevisionID:     citation.RevisionID,
			ContentPreview: truncateReviewRunes(strings.TrimSpace(chunk.Content), 220),
		})
	}

	return report, nil
}

type reviewManifest struct {
	KBID    string `json:"kb_id"`
	Version string `json:"version"`
	Files   struct {
		Chunks    []reviewManifestFile `json:"chunks"`
		Citations []reviewManifestFile `json:"citations"`
	} `json:"files"`
}

type reviewManifestFile struct {
	Path string `json:"path"`
}

type reviewChunk struct {
	ChunkID string
	Title   string
	Path    string
	Source  string
	Content string
}

type reviewCitation struct {
	ChunkID    string `json:"chunk_id"`
	FAQType    string `json:"faq_type"`
	URL        string `json:"url"`
	Source     string `json:"source"`
	License    string `json:"license"`
	RevisionID string `json:"revision_id"`
}

type reviewCitationFile struct {
	Citations []reviewCitation `json:"citations"`
}

type reviewGoldenFile struct {
	Questions []GoldenQuestion `json:"questions"`
}

func reviewChunksFromSQLite(file *zip.File) ([]reviewChunk, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open chunks sqlite: %w", err)
	}
	defer reader.Close()

	tempFile, err := os.CreateTemp("", "yi-flow-pack-review-*.sqlite")
	if err != nil {
		return nil, fmt.Errorf("create review sqlite: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(tempFile, reader); err != nil {
		_ = tempFile.Close()
		return nil, fmt.Errorf("copy review sqlite: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return nil, fmt.Errorf("close review sqlite: %w", err)
	}
	database, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return nil, fmt.Errorf("open review sqlite: %w", err)
	}
	defer database.Close()

	rows, err := database.Query(`
		SELECT
			COALESCE(chunk_id, ''),
			COALESCE(title, ''),
			COALESCE(path, ''),
			COALESCE(source, ''),
			COALESCE(content, '')
		FROM chunks
		ORDER BY rowid ASC;
	`)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	chunks := []reviewChunk{}
	for rows.Next() {
		var chunk reviewChunk
		if err := rows.Scan(&chunk.ChunkID, &chunk.Title, &chunk.Path, &chunk.Source, &chunk.Content); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}
	return chunks, nil
}

func reviewCitationsByChunkID(file *zip.File) (map[string]reviewCitation, AttributionReport, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, AttributionReport{}, fmt.Errorf("open citations: %w", err)
	}
	defer reader.Close()
	var decoded reviewCitationFile
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return nil, AttributionReport{}, fmt.Errorf("decode citations: %w", err)
	}
	result := map[string]reviewCitation{}
	attribution := AttributionReport{
		SourceCount:  map[string]int{},
		LicenseCount: map[string]int{},
	}
	for _, citation := range decoded.Citations {
		citation.ChunkID = strings.TrimSpace(citation.ChunkID)
		if citation.ChunkID == "" {
			continue
		}
		result[citation.ChunkID] = citation
		incrementReviewCount(attribution.SourceCount, citation.Source)
		incrementReviewCount(attribution.LicenseCount, citation.License)
	}
	return result, attribution, nil
}

func reviewGoldenQuestions(data []byte, limit int) ([]GoldenQuestion, error) {
	var decoded reviewGoldenFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode golden: %w", err)
	}
	if len(decoded.Questions) > limit {
		return decoded.Questions[:limit], nil
	}
	return decoded.Questions, nil
}

func deterministicSample(chunks []reviewChunk, limit int) []reviewChunk {
	if len(chunks) <= limit {
		return append([]reviewChunk{}, chunks...)
	}
	result := make([]reviewChunk, 0, limit)
	step := float64(len(chunks)) / float64(limit)
	seen := map[int]bool{}
	for index := 0; len(result) < limit; index++ {
		pick := int(float64(index) * step)
		if pick >= len(chunks) {
			pick = len(chunks) - 1
		}
		if seen[pick] {
			continue
		}
		seen[pick] = true
		result = append(result, chunks[pick])
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ChunkID < result[j].ChunkID
	})
	return result
}

func faqTypeFromChunk(chunk reviewChunk, citation reviewCitation) string {
	if strings.TrimSpace(citation.FAQType) != "" {
		return strings.TrimSpace(citation.FAQType)
	}
	for _, faqType := range []string{"faq_overview", "faq_identity", "faq_facts", "faq_relation", "faq_disambiguation"} {
		if strings.Contains(chunk.Content, "【FAQ类型】"+faqType) {
			return faqType
		}
	}
	return ""
}

func fullMirrorSuspect(content string) bool {
	return len([]rune(strings.TrimSpace(content))) > 1200
}

func findReviewZipFile(files []*zip.File, candidates []reviewManifestFile, fallback string) *zip.File {
	for _, candidate := range candidates {
		if file := findZipFile(files, candidate.Path); file != nil {
			return file
		}
	}
	return findZipFile(files, fallback)
}

func findZipFile(files []*zip.File, candidate string) *zip.File {
	candidate = path.Clean(strings.TrimSpace(strings.TrimPrefix(candidate, "/")))
	if candidate == "" || candidate == "." {
		return nil
	}
	for _, file := range files {
		if path.Clean(file.Name) == candidate {
			return file
		}
	}
	return nil
}

func truncateReviewRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func incrementReviewCount(counts map[string]int, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	counts[value]++
}

var _ = filepath.Clean
