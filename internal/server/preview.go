package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	defaultPreviewChunkLimit = 12
	maxPreviewChunkLimit     = 50
	maxPreviewContentRunes   = 520
	maxPreviewSQLiteBytes    = 256 << 20
)

type knowledgePackPreview struct {
	KBID     string                      `json:"kb_id"`
	Version  string                      `json:"version"`
	Latest   bool                        `json:"latest"`
	Chunks   []knowledgePackPreviewChunk `json:"chunks"`
	Warnings []string                    `json:"warnings,omitempty"`
	Files    []knowledgePackPreviewFile  `json:"files,omitempty"`
}

type knowledgePackPreviewChunk struct {
	ChunkID            string   `json:"chunk_id"`
	Title              string   `json:"title"`
	Path               string   `json:"path"`
	Source             string   `json:"source"`
	Content            string   `json:"content"`
	SuggestedQuestions []string `json:"suggested_questions"`
}

type knowledgePackPreviewFile struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}

type previewManifest struct {
	Files struct {
		Chunks []struct {
			Path string `json:"path"`
		} `json:"chunks"`
		FTS []struct {
			Path string `json:"path"`
		} `json:"fts"`
	} `json:"files"`
}

func (h *Handler) handleLatestPreview(w http.ResponseWriter, r *http.Request) {
	kbID, ok := strings.CutPrefix(r.URL.Path, "/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/latest/preview")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	version, err := h.latestVersion(kbID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.writePreview(w, r, kbID, version, true)
}

func (h *Handler) handleVersionPreview(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, rest, ok := strings.Cut(rest, "/versions/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	version, ok := strings.CutSuffix(rest, "/preview")
	if !ok {
		http.NotFound(w, r)
		return
	}

	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	version, err = safeComponent(version)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	latest, _ := h.latestVersion(kbID)
	h.writePreview(w, r, kbID, version, version == latest)
}

func (h *Handler) writePreview(w http.ResponseWriter, r *http.Request, kbID string, version string, latest bool) {
	limit := previewLimit(r)
	preview, err := h.previewKnowledgePack(kbID, version, latest, limit)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		if errors.Is(err, errPreviewUnavailable) {
			status = http.StatusUnprocessableEntity
		}
		http.Error(w, err.Error(), status)
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) previewKnowledgePack(kbID string, version string, latest bool, limit int) (knowledgePackPreview, error) {
	versionDir := h.versionDir(kbID, version)
	manifestPath := filepath.Join(versionDir, "manifest.json")
	packagePath := filepath.Join(versionDir, "knowledge-pack.zip")

	manifest, err := readPreviewManifest(manifestPath)
	if err != nil {
		return knowledgePackPreview{}, err
	}

	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return knowledgePackPreview{}, err
		}
		return knowledgePackPreview{}, fmt.Errorf("open knowledge package: %w", err)
	}
	defer archive.Close()

	preview := knowledgePackPreview{
		KBID:    kbID,
		Version: version,
		Latest:  latest,
		Files:   zipFileSummaries(archive.File),
	}

	chunksFile := findChunkSQLiteFile(archive.File, manifest)
	if chunksFile == nil {
		return knowledgePackPreview{}, fmt.Errorf("%w: chunks sqlite file not found", errPreviewUnavailable)
	}

	chunks, err := previewChunksFromSQLite(chunksFile, limit)
	if err != nil {
		return knowledgePackPreview{}, err
	}
	preview.Chunks = chunks
	if len(chunks) == 0 {
		preview.Warnings = append(preview.Warnings, "chunks 表存在，但没有可预览内容")
	}

	return preview, nil
}

func previewLimit(r *http.Request) int {
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	if rawLimit == "" {
		return defaultPreviewChunkLimit
	}

	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit < 1 {
		return defaultPreviewChunkLimit
	}
	if limit > maxPreviewChunkLimit {
		return maxPreviewChunkLimit
	}
	return limit
}

func readPreviewManifest(manifestPath string) (previewManifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return previewManifest{}, err
	}

	var manifest previewManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return previewManifest{}, fmt.Errorf("decode manifest file list: %w", err)
	}
	return manifest, nil
}

func zipFileSummaries(files []*zip.File) []knowledgePackPreviewFile {
	summaries := make([]knowledgePackPreviewFile, 0, len(files))
	for _, file := range files {
		summaries = append(summaries, knowledgePackPreviewFile{
			Path: file.Name,
			Size: file.UncompressedSize64,
		})
	}
	return summaries
}

func findChunkSQLiteFile(files []*zip.File, manifest previewManifest) *zip.File {
	for _, candidate := range previewChunkPathCandidates(manifest) {
		if file := findZipFile(files, candidate); file != nil {
			return file
		}
	}
	return nil
}

func previewChunkPathCandidates(manifest previewManifest) []string {
	candidates := []string{}
	for _, file := range manifest.Files.Chunks {
		candidates = append(candidates, file.Path)
	}
	for _, file := range manifest.Files.FTS {
		candidates = append(candidates, file.Path)
	}
	candidates = append(candidates, "chunks.sqlite", "fts.sqlite")
	return candidates
}

func findZipFile(files []*zip.File, candidate string) *zip.File {
	candidate = cleanZipPath(candidate)
	if candidate == "" || candidate == "." {
		return nil
	}

	for _, file := range files {
		if cleanZipPath(file.Name) == candidate {
			return file
		}
	}
	return nil
}

func cleanZipPath(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func previewChunksFromSQLite(file *zip.File, limit int) ([]knowledgePackPreviewChunk, error) {
	if file.UncompressedSize64 > maxPreviewSQLiteBytes {
		return nil, fmt.Errorf("%w: chunks sqlite is too large for admin preview", errPreviewUnavailable)
	}

	tempFile, err := os.CreateTemp("", "yi-flow-knowledge-preview-*.sqlite")
	if err != nil {
		return nil, fmt.Errorf("create preview sqlite temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	reader, err := file.Open()
	if err != nil {
		_ = tempFile.Close()
		return nil, fmt.Errorf("open chunks sqlite from package: %w", err)
	}
	_, copyErr := io.Copy(tempFile, io.LimitReader(reader, maxPreviewSQLiteBytes+1))
	closeErr := reader.Close()
	tempCloseErr := tempFile.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("copy chunks sqlite for preview: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close chunks sqlite entry: %w", closeErr)
	}
	if tempCloseErr != nil {
		return nil, fmt.Errorf("close preview sqlite temp file: %w", tempCloseErr)
	}

	database, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return nil, fmt.Errorf("open preview sqlite: %w", err)
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
		ORDER BY rowid ASC
		LIMIT ?;
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: read chunks table: %v", errPreviewUnavailable, err)
	}
	defer rows.Close()

	chunks := []knowledgePackPreviewChunk{}
	for rows.Next() {
		var chunk knowledgePackPreviewChunk
		if err := rows.Scan(&chunk.ChunkID, &chunk.Title, &chunk.Path, &chunk.Source, &chunk.Content); err != nil {
			return nil, fmt.Errorf("scan preview chunk: %w", err)
		}
		chunk.Content = truncateRunes(strings.TrimSpace(chunk.Content), maxPreviewContentRunes)
		chunk.SuggestedQuestions = suggestedQuestions(chunk)
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate preview chunks: %w", err)
	}

	return chunks, nil
}

func suggestedQuestions(chunk knowledgePackPreviewChunk) []string {
	title := strings.TrimSpace(chunk.Title)
	content := strings.TrimSpace(chunk.Content)

	questions := []string{}
	if title != "" {
		questions = append(questions, "请说明"+title)
		questions = append(questions, title+"的关键内容是什么？")
	}
	if content != "" {
		keyword := firstSentence(content)
		if keyword != "" && keyword != title {
			questions = append(questions, "知识包里关于"+keyword+"怎么说？")
		}
	}
	return uniqueStrings(questions, 3)
}

func firstSentence(content string) string {
	content = stripLeadingMetadataLabels(content)
	for _, separator := range []string{"。", "？", "?", "！", "!", "\n"} {
		if before, _, ok := strings.Cut(content, separator); ok {
			return truncateRunes(strings.TrimSpace(before), 48)
		}
	}
	return truncateRunes(content, 48)
}

func stripLeadingMetadataLabels(content string) string {
	content = strings.TrimSpace(content)
	for strings.HasPrefix(content, "【") {
		_, after, ok := strings.Cut(content, "】")
		if !ok {
			return content
		}
		content = strings.TrimSpace(after)
	}
	return content
}

func uniqueStrings(values []string, limit int) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) >= limit {
			return result
		}
	}
	return result
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

var errPreviewUnavailable = errors.New("knowledge pack preview unavailable")
