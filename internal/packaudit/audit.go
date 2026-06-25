package packaudit

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"yi-flow/knowledge-base/internal/sourcepolicy"

	_ "modernc.org/sqlite"
)

type Report struct {
	KBID             string         `json:"kb_id"`
	Version          string         `json:"version"`
	Files            FileCoverage   `json:"files"`
	Sources          map[string]int `json:"sources"`
	Domains          map[string]int `json:"domains"`
	ChunkIDPrefixes  map[string]int `json:"chunk_id_prefixes"`
	SourceFamilies   map[string]int `json:"source_families"`
	InternalFamilies map[string]int `json:"internal_families"`
	Violations       []Violation    `json:"violations"`
}

type FileCoverage struct {
	ManifestJSON     bool `json:"manifest_json"`
	KnowledgePackZIP bool `json:"knowledge_pack_zip"`
	ChunksSQLite     bool `json:"chunks_sqlite"`
	PromptsJSON      bool `json:"prompts_json"`
	CitationsJSON    bool `json:"citations_json"`
}

type Violation struct {
	Code    string `json:"code"`
	KBID    string `json:"kb_id"`
	Version string `json:"version"`
	Family  string `json:"family"`
	Field   string `json:"field"`
}

type PolicyViolationError struct {
	Violations []Violation
}

func (e PolicyViolationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, violation := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s:%s:%s", violation.KBID, violation.Family, violation.Field))
	}
	sort.Strings(parts)
	return "knowledge pack policy violation: " + strings.Join(parts, ",")
}

func AuditFiles(manifestPath string, packagePath string) (Report, error) {
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return Report{}, fmt.Errorf("read manifest.json: %w", err)
	}
	packageData, err := os.ReadFile(packagePath)
	if err != nil {
		return Report{}, fmt.Errorf("read knowledge-pack.zip: %w", err)
	}
	return Audit(manifestData, packageData)
}

func Audit(manifestData []byte, packageData []byte) (Report, error) {
	report := Report{
		Files:            FileCoverage{ManifestJSON: true, KnowledgePackZIP: true},
		Sources:          map[string]int{},
		Domains:          map[string]int{},
		ChunkIDPrefixes:  map[string]int{},
		SourceFamilies:   map[string]int{},
		InternalFamilies: map[string]int{},
	}

	var manifest auditManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return report, fmt.Errorf("decode manifest.json: %w", err)
	}
	report.KBID = strings.TrimSpace(manifest.KBID)
	report.Version = strings.TrimSpace(manifest.Version)
	if report.KBID == "" || report.Version == "" {
		return report, errors.New("manifest.json must include kb_id and version")
	}

	archive, err := zip.NewReader(bytes.NewReader(packageData), int64(len(packageData)))
	if err != nil {
		return report, fmt.Errorf("open knowledge-pack.zip: %w", err)
	}
	entries := map[string]*zip.File{}
	for _, file := range archive.File {
		entries[file.Name] = file
	}

	if chunksFile := firstArchiveFile(entries, manifest.Files.Chunks, "chunks.sqlite"); chunksFile != nil {
		report.Files.ChunksSQLite = true
		if err := auditChunksSQLite(chunksFile, &report); err != nil {
			return report, err
		}
	}
	if promptsFile := firstArchiveFile(entries, manifest.Files.Prompts, "prompts.json"); promptsFile != nil {
		report.Files.PromptsJSON = true
		if _, err := readZipFile(promptsFile); err != nil {
			return report, fmt.Errorf("read prompts.json: %w", err)
		}
	}
	if citationsFile := firstArchiveFile(entries, manifest.Files.Citations, "citations.json"); citationsFile != nil {
		report.Files.CitationsJSON = true
		citationsData, err := readZipFile(citationsFile)
		if err != nil {
			return report, fmt.Errorf("read citations.json: %w", err)
		}
		if err := auditCitations(citationsData, &report); err != nil {
			return report, err
		}
	}

	report.Violations = append(report.Violations, policyViolations(report)...)
	sortViolations(report.Violations)
	if len(report.Violations) > 0 {
		return report, PolicyViolationError{Violations: report.Violations}
	}
	return report, nil
}

type auditManifest struct {
	KBID    string `json:"kb_id"`
	Version string `json:"version"`
	Files   struct {
		Chunks    []auditManifestFile `json:"chunks"`
		Citations []auditManifestFile `json:"citations"`
		Prompts   []auditManifestFile `json:"prompts"`
	} `json:"files"`
}

type auditManifestFile struct {
	Path string `json:"path"`
}

func firstArchiveFile(entries map[string]*zip.File, files []auditManifestFile, fallback string) *zip.File {
	for _, file := range files {
		if entry := entries[file.Path]; entry != nil {
			return entry
		}
	}
	return entries[fallback]
}

func auditChunksSQLite(file *zip.File, report *Report) error {
	data, err := readZipFile(file)
	if err != nil {
		return fmt.Errorf("read chunks.sqlite: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "yi-flow-pack-audit-*")
	if err != nil {
		return fmt.Errorf("create audit workspace: %w", err)
	}
	defer os.RemoveAll(tempDir)

	databasePath := filepath.Join(tempDir, "chunks.sqlite")
	if err := os.WriteFile(databasePath, data, 0o600); err != nil {
		return fmt.Errorf("write chunks sqlite: %w", err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return fmt.Errorf("open chunks sqlite: %w", err)
	}
	defer database.Close()

	rows, err := database.Query("SELECT chunk_id, title, path, source FROM chunks")
	if err != nil {
		return fmt.Errorf("query chunks sqlite: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var chunkID, title, path, source string
		if err := rows.Scan(&chunkID, &title, &path, &source); err != nil {
			return fmt.Errorf("scan chunk: %w", err)
		}
		increment(report.Sources, source)
		increment(report.ChunkIDPrefixes, chunkIDPrefix(chunkID))
		classifyIdentityFields(report, []identityField{
			{name: "chunk_id", value: chunkID},
			{name: "title", value: title},
			{name: "path", value: path},
			{name: "source", value: source},
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chunks: %w", err)
	}
	return nil
}

type identityField struct {
	name  string
	value string
}

func auditCitations(data []byte, report *Report) error {
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode citations.json: %w", err)
	}
	validateMoegirlCitationMetadata(decoded, report)
	for _, citation := range citationObjects(decoded) {
		for _, key := range []string{"source", "url", "chunk_id", "title"} {
			value, _ := citation[key].(string)
			switch key {
			case "source":
				increment(report.Sources, value)
			case "url":
				if domain := domainFromURL(value); domain != "" {
					increment(report.Domains, domain)
				}
			}
			classifyIdentityFields(report, []identityField{{name: "citations." + key, value: value}})
		}
	}
	return nil
}

func validateMoegirlCitationMetadata(decoded any, report *Report) {
	if !strings.HasPrefix(report.KBID, "moegirl-") {
		return
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		appendViolation(report, "missing_moegirl_source_metadata", "citations.root", "moegirl")
		return
	}
	for _, field := range []string{"source", "license", "source_policy"} {
		if strings.TrimSpace(stringValue(root[field])) == "" {
			appendViolation(report, "missing_moegirl_source_metadata", "citations."+field, "moegirl")
		}
	}
	rows, ok := root["crawl_manifest"].([]any)
	if !ok || len(rows) == 0 {
		appendViolation(report, "missing_moegirl_source_metadata", "citations.crawl_manifest", "moegirl")
		return
	}
	requiredFields := []string{
		"kb_id",
		"source_name",
		"source_url",
		"canonical_url",
		"page_id",
		"revision_id",
		"touched",
		"license",
		"source_policy",
		"categories",
		"fetched_at",
	}
	for index, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			appendViolation(report, "missing_moegirl_source_metadata", fmt.Sprintf("citations.crawl_manifest[%d]", index), "moegirl")
			continue
		}
		for _, field := range requiredFields {
			if metadataFieldMissing(object[field]) {
				appendViolation(report, "missing_moegirl_source_metadata", fmt.Sprintf("citations.crawl_manifest[%d].%s", index, field), "moegirl")
			}
		}
	}
}

func metadataFieldMissing(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case float64:
		return typed == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func citationObjects(decoded any) []map[string]any {
	switch value := decoded.(type) {
	case []any:
		return mapsFromArray(value)
	case map[string]any:
		if nested, ok := value["citations"].([]any); ok {
			return mapsFromArray(nested)
		}
		return []map[string]any{value}
	default:
		return nil
	}
}

func mapsFromArray(values []any) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func classifyIdentityFields(report *Report, fields []identityField) {
	for _, field := range fields {
		if family, ok := sourcepolicy.ClassifyExternalSourceFamily(field.value); ok {
			increment(report.SourceFamilies, family)
		}
		if family, ok := sourcepolicy.ClassifyInternalYiFlowSourceFamily(field.value); ok {
			increment(report.InternalFamilies, family)
		}
	}
}

func policyViolations(report Report) []Violation {
	violations := []Violation{}
	if report.KBID == "yi-flow-core" {
		for family, count := range report.SourceFamilies {
			if count > 0 {
				violations = append(violations, Violation{
					Code:    "forbidden_external_source_family",
					KBID:    report.KBID,
					Version: report.Version,
					Family:  family,
					Field:   "source_identity",
				})
			}
		}
	}
	if strings.HasPrefix(report.KBID, "moegirl-") {
		for family, count := range report.InternalFamilies {
			if count > 0 {
				violations = append(violations, Violation{
					Code:    "forbidden_internal_source_family",
					KBID:    report.KBID,
					Version: report.Version,
					Family:  family,
					Field:   "source_identity",
				})
			}
		}
	}
	sortViolations(violations)
	return violations
}

func appendViolation(report *Report, code string, field string, family string) {
	report.Violations = append(report.Violations, Violation{
		Code:    code,
		KBID:    report.KBID,
		Version: report.Version,
		Family:  family,
		Field:   field,
	})
}

func sortViolations(violations []Violation) {
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Code != violations[j].Code {
			return violations[i].Code < violations[j].Code
		}
		if violations[i].Family != violations[j].Family {
			return violations[i].Family < violations[j].Family
		}
		return violations[i].Field < violations[j].Field
	})
}

func readZipFile(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func domainFromURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func chunkIDPrefix(chunkID string) string {
	chunkID = strings.TrimSpace(chunkID)
	if chunkID == "" {
		return ""
	}
	parts := strings.Split(chunkID, "-")
	if len(parts) <= 1 {
		return chunkID
	}
	last := parts[len(parts)-1]
	for _, char := range last {
		if char < '0' || char > '9' {
			return chunkID
		}
	}
	return strings.Join(parts[:len(parts)-1], "-")
}

func increment(counts map[string]int, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	counts[value]++
}
