package packeval

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type Options struct {
	TopK int
}

type GoldenQuestion struct {
	ID               string   `json:"id"`
	Category         string   `json:"category"`
	Question         string   `json:"question"`
	ExpectedChunkIDs []string `json:"expected_chunk_ids,omitempty"`
	ExpectedTitles   []string `json:"expected_titles,omitempty"`
	Answerable       bool     `json:"answerable"`
	Regression       bool     `json:"regression,omitempty"`
}

type Report struct {
	KBID                   string        `json:"kb_id"`
	Version                string        `json:"version"`
	TotalQuestions         int           `json:"total_questions"`
	AnswerableQuestions    int           `json:"answerable_questions"`
	RefusalQuestions       int           `json:"refusal_questions"`
	RegressionQuestions    int           `json:"regression_questions"`
	Top1HitRate            float64       `json:"top1_hit_rate"`
	Top5HitRate            float64       `json:"top5_hit_rate"`
	CitationRate           float64       `json:"citation_rate"`
	DuplicateAnswerRate    float64       `json:"duplicate_answer_rate"`
	RefusalPassRate        float64       `json:"refusal_pass_rate"`
	UnsupportedEntityCount int           `json:"unsupported_entity_count"`
	Failures               []Failure     `json:"failures,omitempty"`
	Results                []QuestionRun `json:"results,omitempty"`
}

type Failure struct {
	ID                string   `json:"id"`
	Question          string   `json:"question"`
	Reason            string   `json:"reason"`
	ExpectedChunkIDs  []string `json:"expected_chunk_ids,omitempty"`
	ExpectedTitles    []string `json:"expected_titles,omitempty"`
	RetrievedChunkIDs []string `json:"retrieved_chunk_ids,omitempty"`
}

type QuestionRun struct {
	ID                string   `json:"id"`
	Category          string   `json:"category"`
	Question          string   `json:"question"`
	Answerable        bool     `json:"answerable"`
	ExpectedChunkIDs  []string `json:"expected_chunk_ids,omitempty"`
	ExpectedTitles    []string `json:"expected_titles,omitempty"`
	RetrievedChunkIDs []string `json:"retrieved_chunk_ids,omitempty"`
	Top1Hit           bool     `json:"top1_hit"`
	Top5Hit           bool     `json:"top5_hit"`
	CitationHit       bool     `json:"citation_hit"`
	RefusalPass       bool     `json:"refusal_pass"`
}

func EvaluateFiles(manifestPath string, packagePath string, goldenPath string, options Options) (Report, error) {
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
	return Evaluate(manifestData, packageData, goldenData, options)
}

func Evaluate(manifestData []byte, packageData []byte, goldenData []byte, options Options) (Report, error) {
	if options.TopK <= 0 {
		options.TopK = 5
	}

	var manifest evalManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Report{}, fmt.Errorf("decode manifest: %w", err)
	}
	var golden evalGoldenFile
	if err := json.Unmarshal(goldenData, &golden); err != nil {
		return Report{}, fmt.Errorf("decode golden: %w", err)
	}

	archive, err := zip.NewReader(bytes.NewReader(packageData), int64(len(packageData)))
	if err != nil {
		return Report{}, fmt.Errorf("open package: %w", err)
	}
	chunksFile := findEvalZipFile(archive.File, manifest.Files.Chunks, "chunks.sqlite")
	if chunksFile == nil {
		return Report{}, fmt.Errorf("chunks sqlite not found")
	}
	citationsFile := findEvalZipFile(archive.File, manifest.Files.Citations, "citations.json")
	if citationsFile == nil {
		return Report{}, fmt.Errorf("citations json not found")
	}
	citations, err := evalCitationsByChunkID(citationsFile)
	if err != nil {
		return Report{}, err
	}
	searcher, cleanup, err := newPackageSearcher(chunksFile)
	if err != nil {
		return Report{}, err
	}
	defer cleanup()

	report := Report{
		KBID:           strings.TrimSpace(manifest.KBID),
		Version:        strings.TrimSpace(manifest.Version),
		TotalQuestions: len(golden.Questions),
	}
	var top1Hits, top5Hits, citationHits, duplicateHits, refusalPasses int
	for _, question := range golden.Questions {
		if question.Answerable {
			report.AnswerableQuestions++
		} else {
			report.RefusalQuestions++
		}
		if question.Regression {
			report.RegressionQuestions++
		}

		retrieved, err := searcher.Search(question.Question, options.TopK)
		if err != nil {
			return report, err
		}
		run := QuestionRun{
			ID:                question.ID,
			Category:          question.Category,
			Question:          question.Question,
			Answerable:        question.Answerable,
			ExpectedChunkIDs:  question.ExpectedChunkIDs,
			ExpectedTitles:    question.ExpectedTitles,
			RetrievedChunkIDs: retrieved,
		}
		if question.Answerable {
			run.Top1Hit = len(retrieved) > 0 && matchesExpected(question, []string{retrieved[0]}, searcher.titles, searcher.contents)
			run.Top5Hit = matchesExpected(question, retrieved, searcher.titles, searcher.contents)
			run.CitationHit = run.Top5Hit && retrievedHasCitation(question, retrieved, citations, searcher.titles, searcher.contents)
			if run.Top1Hit {
				top1Hits++
			}
			if run.Top5Hit {
				top5Hits++
			} else {
				report.UnsupportedEntityCount++
				report.Failures = append(report.Failures, failureFor(question, retrieved, "expected_chunk_not_retrieved"))
			}
			if run.CitationHit {
				citationHits++
			}
		} else {
			run.RefusalPass = len(retrieved) == 0
			if run.RefusalPass {
				refusalPasses++
			} else {
				report.Failures = append(report.Failures, failureFor(question, retrieved, "out_of_domain_retrieved_chunks"))
			}
		}
		if hasDuplicates(retrieved) {
			duplicateHits++
		}
		report.Results = append(report.Results, run)
	}

	report.Top1HitRate = ratio(top1Hits, report.AnswerableQuestions)
	report.Top5HitRate = ratio(top5Hits, report.AnswerableQuestions)
	report.CitationRate = ratio(citationHits, report.AnswerableQuestions)
	report.DuplicateAnswerRate = ratio(duplicateHits, report.TotalQuestions)
	report.RefusalPassRate = ratio(refusalPasses, report.RefusalQuestions)
	return report, nil
}

type evalManifest struct {
	KBID    string `json:"kb_id"`
	Version string `json:"version"`
	Files   struct {
		Chunks    []evalManifestFile `json:"chunks"`
		Citations []evalManifestFile `json:"citations"`
	} `json:"files"`
}

type evalManifestFile struct {
	Path string `json:"path"`
}

type evalGoldenFile struct {
	Questions []GoldenQuestion `json:"questions"`
}

type evalCitation struct {
	ChunkID string `json:"chunk_id"`
	URL     string `json:"url"`
	Source  string `json:"source"`
	License string `json:"license"`
}

type evalCitationFile struct {
	Citations []evalCitation `json:"citations"`
}

type packageSearcher struct {
	database *sql.DB
	titles   map[string]string
	contents map[string]string
}

func newPackageSearcher(file *zip.File) (*packageSearcher, func(), error) {
	reader, err := file.Open()
	if err != nil {
		return nil, func() {}, fmt.Errorf("open chunks sqlite: %w", err)
	}
	defer reader.Close()

	tempFile, err := os.CreateTemp("", "yi-flow-pack-eval-*.sqlite")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create eval sqlite: %w", err)
	}
	tempPath := tempFile.Name()
	if _, err := io.Copy(tempFile, reader); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return nil, func() {}, fmt.Errorf("copy eval sqlite: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, func() {}, fmt.Errorf("close eval sqlite: %w", err)
	}

	database, err := sql.Open("sqlite", tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, func() {}, fmt.Errorf("open eval sqlite: %w", err)
	}
	cleanup := func() {
		_ = database.Close()
		_ = os.Remove(tempPath)
	}
	titles, contents, err := loadChunkText(database)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return &packageSearcher{database: database, titles: titles, contents: contents}, cleanup, nil
}

func loadChunkText(database *sql.DB) (map[string]string, map[string]string, error) {
	rows, err := database.Query("SELECT COALESCE(chunk_id, ''), COALESCE(title, ''), COALESCE(content, '') FROM chunks")
	if err != nil {
		return nil, nil, fmt.Errorf("load chunk text: %w", err)
	}
	defer rows.Close()
	titles := map[string]string{}
	contents := map[string]string{}
	for rows.Next() {
		var chunkID, title, content string
		if err := rows.Scan(&chunkID, &title, &content); err != nil {
			return nil, nil, fmt.Errorf("scan chunk text: %w", err)
		}
		if chunkID != "" {
			titles[chunkID] = title
			contents[chunkID] = content
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate chunk text: %w", err)
	}
	return titles, contents, nil
}

func (s *packageSearcher) Search(query string, topK int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	fallbackChunkIDs, err := s.FallbackSearch(query, topK)
	if err != nil {
		return nil, err
	}
	if len(fallbackChunkIDs) > 0 {
		return fallbackChunkIDs, nil
	}
	rows, err := s.database.Query(`
		SELECT chunk_id
		FROM chunks
		WHERE chunks MATCH ?
		ORDER BY bm25(chunks) ASC
		LIMIT ?;
	`, evalMatchQuery(query), topK)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	chunkIDs := []string{}
	for rows.Next() {
		var chunkID string
		if err := rows.Scan(&chunkID); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		chunkIDs = append(chunkIDs, chunkID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return chunkIDs, nil
}

func evalMatchQuery(query string) string {
	query = strings.ReplaceAll(strings.TrimSpace(query), `"`, `""`)
	return `"` + query + `"`
}

type scoredChunk struct {
	chunkID string
	score   int
}

func (s *packageSearcher) FallbackSearch(query string, topK int) ([]string, error) {
	grams := queryNGrams(query)
	normalizedQuery := normalizeEvalText(query)
	if len(grams) == 0 && normalizedQuery == "" {
		return nil, nil
	}
	rows, err := s.database.Query(`
		SELECT
			COALESCE(chunk_id, ''),
			COALESCE(title, ''),
			COALESCE(content, '')
		FROM chunks;
	`)
	if err != nil {
		return nil, fmt.Errorf("fallback scan chunks: %w", err)
	}
	defer rows.Close()

	scored := []scoredChunk{}
	for rows.Next() {
		var chunkID, title, content string
		if err := rows.Scan(&chunkID, &title, &content); err != nil {
			return nil, fmt.Errorf("scan fallback chunk: %w", err)
		}
		signalScore := evalTitleSignalScore(normalizedQuery, title) + evalAliasLineSignalScore(normalizedQuery, content)
		if signalScore == 0 {
			continue
		}
		haystack := normalizeEvalText(title + "\n" + content)
		score := signalScore
		for _, gram := range grams {
			if strings.Contains(haystack, gram) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredChunk{chunkID: chunkID, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fallback chunks: %w", err)
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].chunkID < scored[j].chunkID
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	result := make([]string, 0, len(scored))
	for _, chunk := range scored {
		result = append(result, chunk.chunkID)
	}
	return result, nil
}

func evalTitleSignalScore(normalizedQuery string, title string) int {
	score := 0
	for _, alias := range evalTitleAliases(title) {
		normalizedAlias := normalizeEvalText(alias)
		if !evalAliasCanMatch(normalizedAlias) {
			continue
		}
		if strings.Contains(normalizedQuery, normalizedAlias) {
			score = max(score, 100+runeCount(normalizedAlias))
		}
	}
	return score
}

func evalAliasLineSignalScore(normalizedQuery string, content string) int {
	score := 0
	for _, line := range strings.Split(content, "\n") {
		aliasText, ok := strings.CutPrefix(strings.TrimSpace(line), "【别名/检索词】")
		if !ok {
			continue
		}
		for _, alias := range strings.Split(aliasText, "、") {
			normalizedAlias := normalizeEvalText(alias)
			if !evalAliasCanMatch(normalizedAlias) {
				continue
			}
			if strings.Contains(normalizedQuery, normalizedAlias) {
				score = max(score, 120+runeCount(normalizedAlias))
			}
		}
	}
	return score
}

func evalTitleAliases(title string) []string {
	base := strings.TrimSpace(strings.Split(title, " · ")[0])
	if base == "" {
		return nil
	}
	aliases := []string{base}
	for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}} {
		open := strings.Index(base, pair[0])
		close := strings.LastIndex(base, pair[1])
		if open > 0 {
			aliases = append(aliases, strings.TrimSpace(base[:open]))
		}
		if open >= 0 && close > open {
			aliases = append(aliases, strings.TrimSpace(base[open+len(pair[0]):close]))
		}
	}
	return aliases
}

func queryNGrams(query string) []string {
	normalized := normalizeEvalText(query)
	runes := []rune(normalized)
	if len(runes) < 2 {
		return nil
	}
	width := 3
	if len(runes) < width {
		width = 2
	}
	seen := map[string]bool{}
	grams := []string{}
	for index := 0; index+width <= len(runes); index++ {
		gram := string(runes[index : index+width])
		if seen[gram] {
			continue
		}
		seen[gram] = true
		grams = append(grams, gram)
	}
	return grams
}

func runeCount(value string) int {
	return len([]rune(value))
}

func evalAliasCanMatch(normalizedAlias string) bool {
	if normalizedAlias == "" {
		return false
	}
	if runeCount(normalizedAlias) == 1 {
		return containsCJK(normalizedAlias)
	}
	return true
}

func containsCJK(value string) bool {
	for _, r := range value {
		if (r >= '\u4e00' && r <= '\u9fff') ||
			(r >= '\u3400' && r <= '\u4dbf') ||
			(r >= '\uf900' && r <= '\ufaff') {
			return true
		}
	}
	return false
}

func normalizeEvalText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		" ", "",
		"\n", "",
		"\t", "",
		"？", "",
		"?", "",
		"。", "",
		"，", "",
		",", "",
		"！", "",
		"!", "",
		"：", "",
		":", "",
		"·", "",
	)
	return replacer.Replace(value)
}

func findEvalZipFile(files []*zip.File, candidates []evalManifestFile, fallback string) *zip.File {
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

func evalCitationsByChunkID(file *zip.File) (map[string]evalCitation, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open citations: %w", err)
	}
	defer reader.Close()
	var decoded evalCitationFile
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode citations: %w", err)
	}
	result := map[string]evalCitation{}
	for _, citation := range decoded.Citations {
		citation.ChunkID = strings.TrimSpace(citation.ChunkID)
		if citation.ChunkID == "" {
			continue
		}
		result[citation.ChunkID] = citation
	}
	return result, nil
}

func retrievedHasCitation(question GoldenQuestion, retrieved []string, citations map[string]evalCitation, titles map[string]string, contents map[string]string) bool {
	for _, chunkID := range retrieved {
		if !matchesExpected(question, []string{chunkID}, titles, contents) {
			continue
		}
		citation, ok := citations[chunkID]
		if ok && citation.URL != "" && citation.Source != "" && citation.License != "" {
			return true
		}
	}
	return false
}

func matchesExpected(question GoldenQuestion, retrieved []string, titles map[string]string, contents map[string]string) bool {
	if intersects(question.ExpectedChunkIDs, retrieved) {
		return true
	}
	for _, chunkID := range retrieved {
		title := strings.ToLower(titles[chunkID])
		content := strings.ToLower(contents[chunkID])
		for _, expectedTitle := range question.ExpectedTitles {
			normalizedExpectedTitle := strings.ToLower(expectedTitle)
			if expectedTitle != "" &&
				(strings.Contains(title, normalizedExpectedTitle) ||
					strings.Contains(content, normalizedExpectedTitle)) {
				return true
			}
		}
	}
	return false
}

func failureFor(question GoldenQuestion, retrieved []string, reason string) Failure {
	return Failure{
		ID:                question.ID,
		Question:          question.Question,
		Reason:            reason,
		ExpectedChunkIDs:  question.ExpectedChunkIDs,
		ExpectedTitles:    question.ExpectedTitles,
		RetrievedChunkIDs: retrieved,
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func intersects(left []string, right []string) bool {
	for _, value := range right {
		if containsString(left, value) {
			return true
		}
	}
	return false
}

func hasDuplicates(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func ratio(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
