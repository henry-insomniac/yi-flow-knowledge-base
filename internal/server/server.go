package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Options struct {
	StorageDir                 string
	AdminToken                 string
	KnowledgePackSigningSeed   []byte
	MoegirlAPIURL              string
	MoegirlSitemapIndexURL     string
	MoegirlPublicArticleOrigin string
	RAGGateway                 RAGGatewayOptions
}

type Handler struct {
	storageDir                 string
	adminToken                 string
	knowledgePackSigningSeed   []byte
	moegirlAPIURL              string
	moegirlSitemapIndexURL     string
	moegirlPublicArticleOrigin string
	ragGateway                 *ragGateway
}

func NewHandler(options Options) (http.Handler, error) {
	if strings.TrimSpace(options.StorageDir) == "" {
		return nil, errors.New("storage dir is required")
	}
	if err := os.MkdirAll(options.StorageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	signingSeed, err := normalizedSigningSeed(options.KnowledgePackSigningSeed)
	if err != nil {
		return nil, err
	}
	ragGateway, err := newRAGGateway(options.RAGGateway)
	if err != nil {
		return nil, err
	}
	return &Handler{
		storageDir:                 options.StorageDir,
		adminToken:                 options.AdminToken,
		knowledgePackSigningSeed:   signingSeed,
		moegirlAPIURL:              defaultString(options.MoegirlAPIURL, defaultMoegirlAPIURL),
		moegirlSitemapIndexURL:     defaultString(options.MoegirlSitemapIndexURL, defaultMoegirlSitemapIndexURL),
		moegirlPublicArticleOrigin: strings.TrimRight(defaultString(options.MoegirlPublicArticleOrigin, defaultMoegirlPublicArticleOrigin), "/"),
		ragGateway:                 ragGateway,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	case r.Method == http.MethodPost && r.URL.Path == "/rag/api/query":
		h.handleRAGQuery(w, r)
	case r.Method == http.MethodGet && (r.URL.Path == "/admin" || r.URL.Path == "/admin/"):
		h.handleAdminPage(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/source-audit"):
		h.handleDraftSourceAudit(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/moegirl-review"):
		h.handleMoegirlDraftReview(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/import"):
		h.handleDraftBulkImport(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/export"):
		h.handleDraftExport(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/review-queue"):
		h.handleDraftReviewQueue(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/review-report"):
		h.handleDraftReviewReport(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/quality-gates"):
		h.handleDraftQualityGates(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/build-dry-run"):
		h.handleDraftBuildDryRun(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/publish"):
		h.handleDraftPublish(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/retrieval-preview"):
		h.handleDraftRetrievalPreview(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/prompts/") && strings.HasSuffix(r.URL.Path, "/preview"):
		h.handleDraftPromptPreview(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/prompts"):
		h.handleListDraftPrompts(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/prompts"):
		h.handleCreateDraftPrompt(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/prompts/"):
		h.handleUpdateDraftPrompt(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/prompts/"):
		h.handleDeleteDraftPrompt(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/chunks"):
		h.handleListDraftChunks(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/chunks"):
		h.handleCreateDraftChunk(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/chunks/") && strings.HasSuffix(r.URL.Path, "/duplicate"):
		h.handleDuplicateDraftChunk(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/chunks/"):
		h.handleUpdateDraftChunk(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.Contains(r.URL.Path, "/chunks/"):
		h.handleDeleteDraftChunk(w, r)
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/"):
		h.handleSaveDraft(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/") && strings.HasSuffix(r.URL.Path, "/preview"):
		h.handleDraftPreview(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.Contains(r.URL.Path, "/drafts/"):
		h.handleGetDraft(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/rag/compare"):
		h.handleAdminRAGCompare(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/weknora/export-dry-run"):
		h.handleWeKnoraExportDryRun(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/weknora/export-publish"):
		h.handleWeKnoraExportPublish(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/moegirl/import-draft"):
		h.handleImportMoegirlDraft(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/moegirl/build-publish"):
		h.handleBuildAndPublishMoegirlSummary(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/build-publish"):
		h.handleBuildAndPublishVersion(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/versions"):
		h.handlePublishVersion(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/latest"):
		h.handleSetLatest(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.HasSuffix(r.URL.Path, "/latest/manifest.json"):
		h.handleLatestManifest(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.HasSuffix(r.URL.Path, "/latest/preview"):
		h.handleLatestPreview(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.HasSuffix(r.URL.Path, "/versions"):
		h.handleListVersions(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.Contains(r.URL.Path, "/versions/") && strings.HasSuffix(r.URL.Path, "/knowledge-pack.zip"):
		h.handleVersionPackage(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.Contains(r.URL.Path, "/versions/") && strings.HasSuffix(r.URL.Path, "/preview"):
		h.handleVersionPreview(w, r)
	default:
		http.NotFound(w, r)
	}
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (h *Handler) handleAdminPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, adminPageHTML)
}

func (h *Handler) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/versions")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart body", http.StatusBadRequest)
		return
	}

	version, err := safeComponent(r.FormValue("version"))
	if err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}

	manifest, err := readFormFile(r, "manifest")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var manifestSummary struct {
		KBID    string `json:"kb_id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &manifestSummary); err != nil {
		http.Error(w, "invalid manifest json", http.StatusBadRequest)
		return
	}
	if manifestSummary.KBID != kbID || manifestSummary.Version != version {
		http.Error(w, "manifest kb_id/version mismatch", http.StatusBadRequest)
		return
	}

	packageFile, _, err := r.FormFile("package")
	if err != nil {
		http.Error(w, "package file is required", http.StatusBadRequest)
		return
	}
	defer packageFile.Close()
	packageBytes, err := io.ReadAll(io.LimitReader(packageFile, 512<<20))
	if err != nil {
		http.Error(w, "read package failed", http.StatusBadRequest)
		return
	}
	if err := auditKnowledgePackBeforePublish(manifest, packageBytes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.storePublishedVersion(kbID, version, manifest, bytes.NewReader(packageBytes)); err != nil {
		writePublishError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"kb_id":   kbID,
		"version": version,
		"latest":  true,
	})
}

const adminPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Knowledge Pack Admin</title>
  <style>
    :root {
      color-scheme: light;
      --font-family: "Airbnb Cereal VF", Circular, -apple-system, BlinkMacSystemFont, system-ui, "Helvetica Neue", sans-serif;
      --color-primary: #ff385c;
      --color-primary-active: #e00b41;
      --color-primary-disabled: #ffd1da;
      --color-error: #c13515;
      --color-ink: #222222;
      --color-body: #3f3f3f;
      --color-muted: #6a6a6a;
      --color-muted-soft: #929292;
      --color-hairline: #dddddd;
      --color-hairline-soft: #ebebeb;
      --color-border-strong: #c1c1c1;
      --color-canvas: #ffffff;
      --color-surface-soft: #f7f7f7;
      --color-surface-strong: #f2f2f2;
      --color-on-primary: #ffffff;
      --radius-xs: 4px;
      --radius-sm: 8px;
      --radius-md: 14px;
      --radius-lg: 20px;
      --radius-xl: 32px;
      --radius-full: 9999px;
      --shadow-float: rgba(0, 0, 0, 0.02) 0 0 0 1px, rgba(0, 0, 0, 0.04) 0 2px 6px, rgba(0, 0, 0, 0.1) 0 4px 8px;
      font-family: var(--font-family);
    }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--color-canvas); color: var(--color-ink); font-family: var(--font-family); font-size: 16px; line-height: 1.5; }
    main { max-width: 1280px; margin: 0 auto; padding: 32px 32px 64px; overflow-x: hidden; }
    h1 { font-size: 28px; line-height: 1.43; font-weight: 700; letter-spacing: 0; margin: 0 0 4px; }
    h2 { font-size: 22px; line-height: 1.18; font-weight: 600; letter-spacing: 0; margin: 0 0 16px; }
    h3 { font-size: 20px; line-height: 1.2; font-weight: 600; letter-spacing: 0; margin: 32px 0 12px; }
    main > p { margin: 0; color: var(--color-muted); font-size: 14px; }
    section { min-width: 0; border-top: 1px solid var(--color-hairline-soft); padding: 32px 0; margin-top: 32px; overflow-x: hidden; overflow-wrap: anywhere; }
    label { display: grid; gap: 8px; margin: 12px 0; color: var(--color-body); font-size: 14px; line-height: 1.29; font-weight: 500; letter-spacing: 0; }
    input, button, textarea, select { font: inherit; letter-spacing: 0; }
    input, textarea, select {
      width: 100%;
      min-height: 56px;
      padding: 14px 12px;
      border: 1px solid var(--color-hairline);
      border-radius: var(--radius-sm);
      background: var(--color-canvas);
      color: var(--color-ink);
      outline: none;
    }
    input:focus, textarea:focus, select:focus { border-color: var(--color-ink); box-shadow: inset 0 0 0 1px var(--color-ink); }
    input::placeholder, textarea::placeholder { color: var(--color-muted-soft); }
    textarea { min-height: 144px; resize: vertical; font-family: var(--font-family); font-size: 14px; line-height: 1.43; }
    button {
      min-height: 48px;
      border: 0;
      border-radius: var(--radius-sm);
      background: var(--color-primary);
      color: var(--color-on-primary);
      padding: 14px 24px;
      cursor: pointer;
      font-size: 16px;
      font-weight: 500;
      line-height: 1.25;
    }
    button:hover { background: var(--color-primary-active); }
    button:disabled { background: var(--color-primary-disabled); cursor: not-allowed; }
    button.secondary { background: var(--color-canvas); color: var(--color-ink); border: 1px solid var(--color-ink); }
    button.secondary:hover { background: var(--color-surface-soft); }
    button.copy {
      min-height: 40px;
      border-radius: var(--radius-full);
      background: var(--color-surface-strong);
      color: var(--color-ink);
      border: 1px solid transparent;
      margin: 4px 8px 4px 0;
      padding: 10px 20px;
      font-size: 14px;
    }
    button.copy:hover { border-color: var(--color-hairline); background: var(--color-canvas); }
    pre { white-space: pre-wrap; word-break: break-word; background: var(--color-surface-soft); color: var(--color-body); padding: 16px; border: 1px solid var(--color-hairline-soft); border-radius: var(--radius-md); min-height: 80px; font-size: 13px; line-height: 1.43; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 16px; align-items: start; }
    .grid > *, .compare-grid > *, .chunk-list > * { min-width: 0; }
    .muted { color: var(--color-muted); }
    .chunk-list { display: grid; gap: 16px; margin-top: 16px; }
    .chunk-card { border: 1px solid var(--color-hairline); border-radius: var(--radius-md); padding: 16px; background: var(--color-canvas); }
    .chunk-card:hover { box-shadow: var(--shadow-float); }
    .chunk-card h3 { margin: 0 0 6px; font-size: 16px; line-height: 1.25; font-weight: 600; }
    .chunk-meta { color: var(--color-muted); font-size: 13px; line-height: 1.23; margin-bottom: 8px; }
    .chunk-content { color: var(--color-body); line-height: 1.5; margin: 8px 0 10px; }
    .question-row { display: flex; flex-wrap: wrap; gap: 8px; }
    .compare-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; margin-top: 16px; }
    .compare-column { border: 1px solid var(--color-hairline); border-radius: var(--radius-md); padding: 16px; background: var(--color-canvas); }
    .compare-status { font-weight: 600; margin: 0 0 10px; }
    .pager {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-top: 12px;
      padding: 8px;
      border: 1px solid var(--color-hairline);
      border-radius: var(--radius-full);
      background: var(--color-canvas);
    }
    .pager button { min-width: 112px; border-radius: var(--radius-full); }
    #draftStatus, #draftChunkPageStatus { font-size: 14px; line-height: 1.29; }
    @media (max-width: 720px) {
      main { padding: 24px 16px 48px; }
      h1 { font-size: 26px; }
      section { padding: 24px 0; margin-top: 24px; }
      .grid, .compare-grid { grid-template-columns: 1fr; }
      button { width: 100%; }
      .pager button { width: auto; flex: 1 1 120px; }
    }
  </style>
</head>
<body>
<main>
  <h1>Knowledge Pack Admin</h1>
  <p>发布、查看和回滚 yi-flow Knowledge Pack。Admin token 只保存在当前浏览器。</p>

  <section>
    <h2>配置</h2>
    <div class="grid">
      <label>Admin token <input id="token" type="password" autocomplete="off"></label>
      <label>Knowledge base ID <input id="kbID" value="yi-flow-core"></label>
    </div>
    <button id="saveToken" class="secondary">保存到本机浏览器</button>
  </section>

  <section>
    <h2>Chunk Studio</h2>
    <p class="muted">自研 chunk 内容创建和管理后台。后续 draft、chunk 编辑、citation、prompts、retrieval preview、quality gates、dry-run、publish 和 rollback 都在这里完成；外部 RAG/知识库项目只作为交互参考，不作为 chunk authoring 后台。</p>
    <div class="grid">
      <p class="muted"><strong>Draft workspace</strong><br>创建知识包草稿，保存未发布 chunks，先预览再发布。</p>
      <p class="muted"><strong>Chunk editor</strong><br>编辑 title、path、source、content、citation、license、source policy 和 review status。</p>
      <p class="muted"><strong>Quality gates</strong><br>发布前检查缺字段、重复、污染、citation 覆盖和 golden eval。</p>
      <p class="muted"><strong>Signed package</strong><br>继续生成 manifest.json、knowledge-pack.zip、chunks.sqlite、citations.json 和 prompts.json。</p>
    </div>
    <h3>Moegirl FAQ import</h3>
    <div class="grid">
      <label>Moegirl draft version <input id="moegirlDraftVersion" placeholder="2026.06.26.moegirl-draft"></label>
      <label>Moegirl import limit <input id="moegirlDraftLimit" type="number" min="1" max="3000" value="50"></label>
    </div>
    <label>Moegirl titles <textarea id="moegirlDraftTitles" spellcheck="false" placeholder="初音未来&#10;东方Project"></textarea></label>
    <button id="importMoegirlDraft" class="secondary" type="button">导入 Moegirl FAQ draft</button>
    <button id="reviewMoegirlDraft" class="secondary" type="button">检查 Moegirl draft</button>
    <div id="moegirlDraftImportReport" class="chunk-list"></div>
    <div class="grid">
      <label>Draft version <input id="draftVersion" placeholder="2026.06.26.draft"></label>
      <label>Chunk ID <input id="draftChunkID" placeholder="draft-topic-001"></label>
      <label>Title <input id="draftChunkTitle" placeholder="知识点标题"></label>
      <label>Path <input id="draftChunkPath" placeholder="topic/category/name"></label>
      <label>Source <input id="draftChunkSource" value="manual"></label>
      <label>Tags <input id="draftChunkTags" placeholder="core, faq, agent"></label>
      <label>Review status
        <select id="draftChunkReviewStatus">
          <option value="draft">draft</option>
          <option value="needs_review">needs_review</option>
          <option value="approved">approved</option>
          <option value="rejected">rejected</option>
        </select>
      </label>
      <label>Citation URL <input id="draftCitationURL" placeholder="https://zh.moegirl.org.cn/..."></label>
      <label>Citation title <input id="draftCitationTitle" placeholder="来源页面标题"></label>
      <label>Source name <input id="draftSourceName" placeholder="萌娘百科 / yi-flow"></label>
      <label>License <input id="draftLicense" placeholder="CC BY-NC-SA 3.0 CN"></label>
      <label>Source policy <input id="draftSourcePolicy" placeholder="summary/FAQ only; no full article mirror"></label>
      <label>Revision ID <input id="draftSourceRevisionID" placeholder="123456"></label>
      <label>Page ID <input id="draftSourcePageID" placeholder="331116"></label>
    </div>
    <label>Content <textarea id="draftChunkContent" spellcheck="false" placeholder="这里写 chunk 内容。保存后会进入草稿预览，但不会修改 latest。"></textarea></label>
    <button id="saveDraft" type="button">保存草稿</button>
    <button id="loadDraft" class="secondary" type="button">读取草稿</button>
    <button id="previewDraft" class="secondary" type="button">预览草稿 chunk</button>
    <button id="createDraftChunk" type="button">创建 chunk</button>
    <button id="updateDraftChunk" class="secondary" type="button">更新 chunk</button>
    <button id="duplicateDraftChunk" class="secondary" type="button">复制 chunk</button>
    <button id="deleteDraftChunk" class="secondary" type="button">删除 chunk</button>
    <div class="grid">
      <label>Chunk search <input id="draftChunkSearch" placeholder="搜索 title/path/source/content/status"></label>
      <label>Review filter
        <select id="draftReviewFilter">
          <option value="">全部</option>
          <option value="draft">draft</option>
          <option value="needs_review">needs_review</option>
          <option value="approved">approved</option>
          <option value="rejected">rejected</option>
        </select>
      </label>
      <label>Page size <input id="draftChunkPageSize" type="number" min="25" max="500" value="100"></label>
    </div>
    <button id="searchDraftChunks" class="secondary" type="button">搜索 chunks</button>
    <button id="auditDraftSources" class="secondary" type="button">审计 source metadata</button>
    <div class="pager">
      <button id="prevDraftChunkPage" class="secondary" type="button">上一页 chunks</button>
      <button id="nextDraftChunkPage" class="secondary" type="button">下一页 chunks</button>
      <span id="draftChunkPageStatus" class="muted">limit=100 · offset=0</span>
    </div>
    <p id="draftStatus" class="muted">Draft workspace status: not saved</p>
    <div id="draftChunks" class="chunk-list"></div>
    <h3>Batch review</h3>
    <label>Canonical draft JSON <textarea id="draftBulkJSON" spellcheck="false" placeholder='{"chunks":[],"prompts":[],"citations":{"citations":[]}}'></textarea></label>
    <div class="grid">
      <label>Review queue filter
        <select id="draftReviewQueueFilter">
          <option value="unreviewed">unreviewed</option>
          <option value="missing_citation">missing_citation</option>
          <option value="failed_gate">failed_gate</option>
          <option value="changed_since_last_publish">changed_since_last_publish</option>
          <option value="">all</option>
        </select>
      </label>
    </div>
    <button id="validateDraftBulkImport" class="secondary" type="button">验证批量导入</button>
    <button id="importDraftBulkJSON" class="secondary" type="button">导入 draft JSON</button>
    <button id="exportDraftBulkJSON" class="secondary" type="button">导出 draft JSON</button>
    <button id="loadDraftReviewQueue" class="secondary" type="button">加载 review queue</button>
    <button id="loadDraftReviewReport" class="secondary" type="button">生成 review report</button>
    <div id="draftReviewMaterials" class="chunk-list"></div>
    <h3>Prompts / golden questions</h3>
    <div class="grid">
      <label>Prompt ID <input id="draftPromptID" placeholder="prompt-alpha"></label>
      <label>Prompt title <input id="draftPromptTitle" placeholder="Alpha golden"></label>
      <label>Expected chunk IDs <input id="draftPromptExpectedChunkIDs" placeholder="alpha,beta"></label>
      <label>Prompt tags <input id="draftPromptTags" placeholder="golden, smoke"></label>
      <label>Answerability
        <select id="draftPromptAnswerability">
          <option value="answerable">answerable</option>
          <option value="refusal">refusal</option>
          <option value="ood">ood</option>
        </select>
      </label>
    </div>
    <label>Question <textarea id="draftPromptQuestion" spellcheck="false" placeholder="这里写 golden question / refusal / OOD 问题。"></textarea></label>
    <button id="createDraftPrompt" type="button">创建 prompt</button>
    <button id="updateDraftPrompt" class="secondary" type="button">更新 prompt</button>
    <button id="deleteDraftPrompt" class="secondary" type="button">删除 prompt</button>
    <button id="listDraftPrompts" class="secondary" type="button">列出 prompts</button>
    <button id="previewDraftPrompt" class="secondary" type="button">运行 prompt 预览</button>
    <div id="draftPrompts" class="chunk-list"></div>
    <h3>Draft retrieval preview</h3>
    <div class="grid">
      <label>Draft retrieval question <input id="draftRetrievalQuery" placeholder="输入问题，发布前检索 draft chunks"></label>
      <label>Draft TopK <input id="draftRetrievalTopK" type="number" min="1" max="12" value="5"></label>
    </div>
    <button id="runDraftRetrievalPreview" class="secondary" type="button">运行 draft retrieval preview</button>
    <div id="draftRetrievalResults" class="chunk-list"></div>
    <h3>Quality Gates</h3>
    <button id="runDraftQualityGates" class="secondary" type="button">运行 quality gates</button>
    <div id="draftQualityGateReport" class="chunk-list"></div>
    <h3>Dry-run Build</h3>
    <button id="runDraftBuildDryRun" class="secondary" type="button">运行 draft dry-run build</button>
    <button id="publishDraftLatest" type="button">发布 draft 为 latest</button>
    <div id="draftDryRunBuildReport" class="chunk-list"></div>
    <p id="weknoraStatus" class="muted">Chunk Studio status: ready for draft editing, quality gates, dry-run and publish.</p>
    <div class="grid">
      <p class="muted">最近导出版本：<strong id="lastWeKnoraExportVersion">-</strong></p>
      <p class="muted">最近质量门禁：<strong id="lastWeKnoraQualityGate">-</strong></p>
    </div>
  </section>

  <div hidden>
    <input id="builderVersion">
    <input id="builderLLM" value="Qwen3-4B-Q4_K_M">
    <textarea id="builderChunks" spellcheck="false"></textarea>
    <textarea id="builderPrompts" spellcheck="false"></textarea>
    <textarea id="builderCitations" spellcheck="false"></textarea>
    <button id="fillBuilderTemplate" type="button"></button>
    <button id="nextBuilderVersion" type="button"></button>
    <button id="importPreviewToBuilder" type="button"></button>
    <button id="buildPublish" type="button"></button>
    <input id="weknoraVersion">
    <input id="weknoraLLM" value="Qwen3-4B-Q4_K_M">
    <textarea id="weknoraExportJSON" spellcheck="false"></textarea>
    <button id="fillWeKnoraExportTemplate" type="button"></button>
    <button id="nextWeKnoraVersion" type="button"></button>
    <button id="dryRunWeKnoraExport" type="button"></button>
    <button id="publishWeKnoraExport" type="button"></button>
    <input id="moegirlVersion">
    <input id="moegirlLimit" type="number" value="50">
    <textarea id="moegirlTitles" spellcheck="false"></textarea>
    <button id="buildMoegirl" type="button"></button>
  </div>

  <section>
    <h2>手动上传版本</h2>
    <p class="muted">保留旧路径：仅当你已经离线生成 manifest.json 和 knowledge-pack.zip 时使用。</p>
    <label>Version <input id="version" placeholder="2026.06.20.001"></label>
    <label>manifest.json <input id="manifest" type="file" accept="application/json,.json"></label>
    <label>knowledge-pack.zip <input id="package" type="file" accept=".zip,application/zip"></label>
    <button id="publish">发布并设为 latest</button>
  </section>

  <section>
    <h2>版本</h2>
    <button id="refresh" class="secondary">刷新版本列表</button>
    <pre id="versions"></pre>
  </section>

  <section>
    <h2>内容预览</h2>
    <p class="muted">查看已发布 Knowledge Pack 里的 chunks，并复制样例问题到 App 中验证检索和引用。</p>
    <div class="grid">
      <label>Preview version <input id="previewVersion" placeholder="留空表示 latest"></label>
      <label>Chunk limit <input id="previewLimit" type="number" min="1" max="50" value="12"></label>
    </div>
    <button id="preview" class="secondary">查看知识包内容</button>
    <p id="previewSummary" class="muted"></p>
    <div id="previewChunks" class="chunk-list"></div>
    <pre id="previewRaw"></pre>
  </section>

  <section>
    <h2>RAG 对比</h2>
    <p class="muted">输入问题后同时查看本地 Knowledge Pack FTS5 和远程 WeKnora 网关结果。远程未配置时只显示状态，不阻断本地检索。</p>
    <div class="grid">
      <label>Query <input id="ragQuery" placeholder="知识包更新路径是什么？"></label>
      <label>TopK <input id="ragTopK" type="number" min="1" max="12" value="5"></label>
    </div>
    <button id="runRagCompare" class="secondary">运行 RAG 对比</button>
    <button id="copyRagQuestion" class="copy">复制问题到 App</button>
    <div class="compare-grid">
      <div class="compare-column">
        <h3>Local FTS5</h3>
        <p id="localRagStatus" class="compare-status muted">No run</p>
        <div id="localRagChunks" class="chunk-list"></div>
      </div>
      <div class="compare-column">
        <h3>Remote WeKnora</h3>
        <p id="remoteRagStatus" class="compare-status muted">No run</p>
        <div id="remoteRagChunks" class="chunk-list"></div>
      </div>
    </div>
    <pre id="ragCompareRaw"></pre>
  </section>

  <section>
    <h2>回滚 latest</h2>
    <label>Version <input id="rollbackVersion" placeholder="2026.06.20.001"></label>
    <button id="rollback">设为 latest</button>
  </section>

  <section>
    <h2>输出</h2>
    <pre id="output"></pre>
  </section>
</main>
<script>
const tokenInput = document.querySelector("#token");
const kbIDInput = document.querySelector("#kbID");
const output = document.querySelector("#output");
const versions = document.querySelector("#versions");
const previewChunks = document.querySelector("#previewChunks");
const previewRaw = document.querySelector("#previewRaw");
const previewSummary = document.querySelector("#previewSummary");
const ragCompareRaw = document.querySelector("#ragCompareRaw");
const localRagStatus = document.querySelector("#localRagStatus");
const remoteRagStatus = document.querySelector("#remoteRagStatus");
const localRagChunks = document.querySelector("#localRagChunks");
const remoteRagChunks = document.querySelector("#remoteRagChunks");
const weknoraStatus = document.querySelector("#weknoraStatus");
const lastWeKnoraExportVersion = document.querySelector("#lastWeKnoraExportVersion");
const lastWeKnoraQualityGate = document.querySelector("#lastWeKnoraQualityGate");
const draftStatus = document.querySelector("#draftStatus");
const draftChunksList = document.querySelector("#draftChunks");
const draftChunkPageStatus = document.querySelector("#draftChunkPageStatus");
const draftPromptsList = document.querySelector("#draftPrompts");
const draftRetrievalResults = document.querySelector("#draftRetrievalResults");
const servicePrefix = location.pathname.includes("/admin") ? location.pathname.split("/admin")[0] : "";
let lastPreview = null;
let lastWeKnoraDryRun = null;
let selectedDraftChunkID = "";
let selectedDraftPromptID = "";
let draftDirty = false;
let draftChunkOffset = 0;
let draftChunkPreviousOffset = 0;
let draftChunkNextOffset = -1;
tokenInput.value = localStorage.getItem("yiFlowKnowledgeAdminToken") || "";
const defaultChunks = [
  {
    chunk_id: "topic-001",
    title: "这里写知识点标题",
    path: "topic/category/name",
    source: "yi-flow-core",
    content: "这里写完整知识内容。你准备在 App 里提问的关键词必须自然出现在 content 里。"
  },
  {
    chunk_id: "topic-test-001",
    title: "知识包验证问题",
    path: "topic/testing/questions",
    source: "yi-flow-core",
    content: "用于验证知识包是否加载成功的问题：这里写你准备在 App 中提问的 3 到 5 个问题。"
  }
];
const defaultPrompts = [
  {
    id: "test-question-001",
    title: "验证知识包",
    question: "请说明这里写知识点标题"
  }
];
const defaultCitations = { citations: [] };
const defaultWeKnoraExport = {
  version: todayVersion(),
  source: "Tencent WeKnora",
  license: "reviewed internal knowledge",
  source_policy: "reviewed chunks only; preserve source URL and license; no unreviewed full-article mirror",
  chunks: [
    {
      id: "chunk-remote-001",
      content: "这里放已审核的 WeKnora chunk 摘要内容。",
      knowledge_id: "doc-001",
      knowledge_title: "WeKnora 导出示例",
      knowledge_filename: "weknora/export/example.md",
      knowledge_source: "manual-review",
      score: 0.9,
      metadata: { url: "https://example.com/source" },
      reviewed: true
    }
  ],
  prompts: [
    { id: "weknora-export-check", title: "验证 WeKnora 导出", question: "请说明 WeKnora 导出示例" }
  ]
};
fillBuilderTemplate(false);
fillWeKnoraExportTemplate(false);
fillDraftTemplate(false);
document.querySelector("#saveToken").onclick = () => {
  localStorage.setItem("yiFlowKnowledgeAdminToken", tokenInput.value);
  output.textContent = "token saved locally";
};
for (const selector of ["#draftChunkID", "#draftChunkTitle", "#draftChunkPath", "#draftChunkSource", "#draftChunkTags", "#draftChunkReviewStatus", "#draftCitationURL", "#draftCitationTitle", "#draftSourceName", "#draftLicense", "#draftSourcePolicy", "#draftSourceRevisionID", "#draftSourcePageID", "#draftChunkContent"]) {
  document.querySelector(selector).addEventListener("input", markDraftDirty);
  document.querySelector(selector).addEventListener("change", markDraftDirty);
}
for (const selector of ["#draftPromptID", "#draftPromptTitle", "#draftPromptExpectedChunkIDs", "#draftPromptTags", "#draftPromptAnswerability", "#draftPromptQuestion"]) {
  document.querySelector(selector).addEventListener("input", markDraftDirty);
  document.querySelector(selector).addEventListener("change", markDraftDirty);
}
function token() { return tokenInput.value || localStorage.getItem("yiFlowKnowledgeAdminToken") || ""; }
function kbID() { return kbIDInput.value.trim() || "yi-flow-core"; }
function moegirlKBID() {
  const current = kbID();
  return current.startsWith("moegirl-") ? current : "moegirl-acgn-faq";
}
function pretty(value) { return JSON.stringify(value, null, 2); }
function fillBuilderTemplate(force) {
  if (force || !document.querySelector("#builderChunks").value.trim()) {
    document.querySelector("#builderChunks").value = pretty(defaultChunks);
  }
  if (force || !document.querySelector("#builderPrompts").value.trim()) {
    document.querySelector("#builderPrompts").value = pretty(defaultPrompts);
  }
  if (force || !document.querySelector("#builderCitations").value.trim()) {
    document.querySelector("#builderCitations").value = pretty(defaultCitations);
  }
  if (force || !document.querySelector("#builderVersion").value.trim()) {
    document.querySelector("#builderVersion").value = todayVersion();
  }
}
function todayVersion() {
  const now = new Date();
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  const prefix = y + "." + m + "." + d + ".";
  let next = 1;
  try {
    const decoded = JSON.parse(versions.textContent || "{}");
    for (const item of decoded.versions || []) {
      if (String(item.version || "").startsWith(prefix)) {
        const serial = Number(String(item.version).slice(prefix.length));
        if (Number.isFinite(serial)) next = Math.max(next, serial + 1);
      }
    }
  } catch (_) {}
  return prefix + String(next).padStart(3, "0");
}
function parseJSONField(selector, fallback) {
  const raw = document.querySelector(selector).value.trim();
  if (!raw) return fallback;
  return JSON.parse(raw);
}
function fillDraftTemplate(force) {
  const chunk = defaultChunks[0];
  if (force || !document.querySelector("#draftVersion").value.trim()) {
    document.querySelector("#draftVersion").value = todayVersion() + "-draft";
  }
  if (force || !document.querySelector("#draftChunkID").value.trim()) {
    document.querySelector("#draftChunkID").value = chunk.chunk_id;
  }
  if (force || !document.querySelector("#draftChunkTitle").value.trim()) {
    document.querySelector("#draftChunkTitle").value = chunk.title;
  }
  if (force || !document.querySelector("#draftChunkPath").value.trim()) {
    document.querySelector("#draftChunkPath").value = chunk.path;
  }
  if (force || !document.querySelector("#draftChunkSource").value.trim()) {
    document.querySelector("#draftChunkSource").value = chunk.source;
  }
  if (force || !document.querySelector("#draftChunkTags").value.trim()) {
    document.querySelector("#draftChunkTags").value = (chunk.tags || []).join(", ");
  }
  if (force || !document.querySelector("#draftChunkReviewStatus").value.trim()) {
    document.querySelector("#draftChunkReviewStatus").value = chunk.review_status || "draft";
  }
  if (force || !document.querySelector("#draftLicense").value.trim()) {
    document.querySelector("#draftLicense").value = chunk.license || "";
  }
  if (force || !document.querySelector("#draftSourcePolicy").value.trim()) {
    document.querySelector("#draftSourcePolicy").value = chunk.source_policy || "";
  }
  if (force || !document.querySelector("#draftChunkContent").value.trim()) {
    document.querySelector("#draftChunkContent").value = chunk.content;
  }
}
function draftVersion() {
  return document.querySelector("#draftVersion").value.trim() || todayVersion() + "-draft";
}
function draftChunkPayloadFromForm() {
  return {
    chunk_id: document.querySelector("#draftChunkID").value.trim(),
    title: document.querySelector("#draftChunkTitle").value.trim(),
    path: document.querySelector("#draftChunkPath").value.trim(),
    source: document.querySelector("#draftChunkSource").value.trim(),
    content: document.querySelector("#draftChunkContent").value.trim(),
    tags: document.querySelector("#draftChunkTags").value.split(",").map((item) => item.trim()).filter(Boolean),
    review_status: document.querySelector("#draftChunkReviewStatus").value.trim() || "draft",
    citation_url: document.querySelector("#draftCitationURL").value.trim(),
    citation_title: document.querySelector("#draftCitationTitle").value.trim(),
    source_name: document.querySelector("#draftSourceName").value.trim(),
    license: document.querySelector("#draftLicense").value.trim(),
    source_policy: document.querySelector("#draftSourcePolicy").value.trim(),
    source_revision_id: document.querySelector("#draftSourceRevisionID").value.trim(),
    source_page_id: document.querySelector("#draftSourcePageID").value.trim()
  };
}
function draftPayloadFromForm() {
  return {
    chunks: [draftChunkPayloadFromForm()],
    prompts: draftPromptPayloadFromForm().id ? [draftPromptPayloadFromForm()] : [],
    citations: defaultCitations
  };
}
function draftPromptPayloadFromForm() {
  const answerability = document.querySelector("#draftPromptAnswerability").value.trim() || "answerable";
  return {
    id: document.querySelector("#draftPromptID").value.trim(),
    title: document.querySelector("#draftPromptTitle").value.trim(),
    question: document.querySelector("#draftPromptQuestion").value.trim(),
    expected_chunk_ids: document.querySelector("#draftPromptExpectedChunkIDs").value.split(",").map((item) => item.trim()).filter(Boolean),
    tags: document.querySelector("#draftPromptTags").value.split(",").map((item) => item.trim()).filter(Boolean),
    answerability,
    answerable: answerability === "answerable"
  };
}
function fillDraftFromChunk(chunk) {
  selectedDraftChunkID = chunk.chunk_id || "";
  document.querySelector("#draftChunkID").value = chunk.chunk_id || "";
  document.querySelector("#draftChunkTitle").value = chunk.title || "";
  document.querySelector("#draftChunkPath").value = chunk.path || "";
  document.querySelector("#draftChunkSource").value = chunk.source || "";
  document.querySelector("#draftChunkTags").value = (chunk.tags || []).join(", ");
  document.querySelector("#draftChunkReviewStatus").value = chunk.review_status || "draft";
  document.querySelector("#draftCitationURL").value = chunk.citation_url || chunk.source_url || "";
  document.querySelector("#draftCitationTitle").value = chunk.citation_title || "";
  document.querySelector("#draftSourceName").value = chunk.source_name || "";
  document.querySelector("#draftLicense").value = chunk.license || "";
  document.querySelector("#draftSourcePolicy").value = chunk.source_policy || "";
  document.querySelector("#draftSourceRevisionID").value = chunk.source_revision_id || chunk.revision_id || "";
  document.querySelector("#draftSourcePageID").value = chunk.source_page_id || "";
  document.querySelector("#draftChunkContent").value = chunk.content || "";
  setDraftClean("Draft workspace status: selected · " + selectedDraftChunkID);
}
function fillDraftFromPrompt(prompt) {
  selectedDraftPromptID = prompt.id || "";
  document.querySelector("#draftPromptID").value = prompt.id || "";
  document.querySelector("#draftPromptTitle").value = prompt.title || "";
  document.querySelector("#draftPromptQuestion").value = prompt.question || prompt.text || "";
  document.querySelector("#draftPromptExpectedChunkIDs").value = (prompt.expected_chunk_ids || []).join(", ");
  document.querySelector("#draftPromptTags").value = (prompt.tags || []).join(", ");
  document.querySelector("#draftPromptAnswerability").value = prompt.answerability || (prompt.answerable === false ? "refusal" : "answerable");
  setDraftClean("Draft workspace status: selected prompt · " + selectedDraftPromptID);
}
function markDraftDirty() {
  draftDirty = true;
  draftStatus.textContent = "Draft workspace status: unsaved changes";
}
function setDraftClean(message) {
  draftDirty = false;
  draftStatus.textContent = message;
}
function preserveUnsavedDraftOnError(message) {
  draftStatus.textContent = message + " · unsaved editor content preserved";
}
function confirmDiscardDraftChanges() {
  if (!draftDirty) return true;
  return window.confirm("当前 chunk 有未保存修改，继续会丢失这些修改。");
}
async function showResponse(response) {
  const text = await response.text();
  output.textContent = response.status + "\n" + text;
  return text;
}
document.querySelector("#fillBuilderTemplate").onclick = () => fillBuilderTemplate(true);
document.querySelector("#fillWeKnoraExportTemplate").onclick = () => fillWeKnoraExportTemplate(true);
document.querySelector("#nextBuilderVersion").onclick = () => {
  document.querySelector("#builderVersion").value = todayVersion();
};
document.querySelector("#nextWeKnoraVersion").onclick = () => {
  const version = todayVersion();
  document.querySelector("#weknoraVersion").value = version;
  const body = parseJSONField("#weknoraExportJSON", defaultWeKnoraExport);
  body.version = version;
  document.querySelector("#weknoraExportJSON").value = pretty(body);
};
document.querySelector("#dryRunWeKnoraExport").onclick = async () => {
  await runWeKnoraExport("/weknora/export-dry-run", false);
};
document.querySelector("#publishWeKnoraExport").onclick = async () => {
  await runWeKnoraExport("/weknora/export-publish", true);
};
document.querySelector("#saveDraft").onclick = async () => {
  try {
    const version = draftVersion();
    document.querySelector("#draftVersion").value = version;
    const payload = draftPayloadFromForm();
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(version), {
      method: "PUT",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    const text = await showResponse(response);
    if (!response.ok) {
      preserveUnsavedDraftOnError("Draft workspace status: save failed");
      return;
    }
    const decoded = JSON.parse(text);
    selectedDraftChunkID = payload.chunks[0].chunk_id;
    setDraftClean("Draft workspace status: saved · " + decoded.version + " · " + decoded.chunk_count + " chunks");
    await loadDraftChunkList();
  } catch (error) {
    preserveUnsavedDraftOnError("Draft workspace status: save failed");
    output.textContent = "保存草稿失败：\n" + String(error);
  }
};
document.querySelector("#loadDraft").onclick = async () => {
  if (!confirmDiscardDraftChanges()) return;
  const version = draftVersion();
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(version), {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    preserveUnsavedDraftOnError("Draft workspace status: load failed");
    return;
  }
  const decoded = JSON.parse(text);
  document.querySelector("#draftVersion").value = decoded.version;
  if ((decoded.chunks || [])[0]) fillDraftFromChunk(decoded.chunks[0]);
  if ((decoded.prompts || [])[0]) fillDraftFromPrompt(decoded.prompts[0]);
  renderDraftChunkList(decoded.chunks || [], (decoded.chunks || []).length, (decoded.chunks || []).length);
  renderDraftPromptList(decoded.prompts || [], (decoded.prompts || []).length);
  setDraftClean("Draft workspace status: loaded · " + decoded.version + " · " + (decoded.chunks || []).length + " chunks");
};
document.querySelector("#previewDraft").onclick = async () => {
  const version = draftVersion();
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(version) + "/preview?limit=12", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await response.text();
  previewRaw.textContent = response.status + "\n" + text;
  if (!response.ok) {
    previewSummary.textContent = "草稿预览失败";
    previewChunks.replaceChildren();
    draftStatus.textContent = "Draft workspace status: preview failed";
    output.textContent = "draft preview failed: " + response.status;
    return;
  }
  lastPreview = JSON.parse(text);
  renderPreview(lastPreview);
  draftStatus.textContent = "Draft workspace status: preview loaded · " + version;
  output.textContent = "draft preview loaded: " + response.status;
};
document.querySelector("#createDraftChunk").onclick = async () => {
  try {
    const payload = draftChunkPayloadFromForm();
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/chunks", {
      method: "POST",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    const text = await showResponse(response);
    if (!response.ok) {
      preserveUnsavedDraftOnError("Draft workspace status: create failed");
      return;
    }
    selectedDraftChunkID = payload.chunk_id;
    setDraftClean("Draft workspace status: created · " + selectedDraftChunkID);
    await loadDraftChunkList();
  } catch (error) {
    preserveUnsavedDraftOnError("Draft workspace status: create failed");
    output.textContent = "创建 chunk 失败：\n" + String(error);
  }
};
document.querySelector("#updateDraftChunk").onclick = async () => {
  try {
    const originalID = selectedDraftChunkID || document.querySelector("#draftChunkID").value.trim();
    if (!originalID) {
      output.textContent = "请选择或输入要更新的 chunk_id。";
      return;
    }
    const payload = draftChunkPayloadFromForm();
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/chunks/" + encodeURIComponent(originalID), {
      method: "PUT",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    const text = await showResponse(response);
    if (!response.ok) {
      preserveUnsavedDraftOnError("Draft workspace status: update failed");
      return;
    }
    selectedDraftChunkID = payload.chunk_id;
    setDraftClean("Draft workspace status: updated · " + selectedDraftChunkID);
    await loadDraftChunkList();
  } catch (error) {
    preserveUnsavedDraftOnError("Draft workspace status: update failed");
    output.textContent = "更新 chunk 失败：\n" + String(error);
  }
};
document.querySelector("#duplicateDraftChunk").onclick = async () => {
  const originalID = selectedDraftChunkID || document.querySelector("#draftChunkID").value.trim();
  if (!originalID) {
    output.textContent = "请选择或输入要复制的 chunk_id。";
    return;
  }
  const nextID = originalID + "-copy";
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/chunks/" + encodeURIComponent(originalID) + "/duplicate", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify({ chunk_id: nextID })
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: duplicate failed";
    return;
  }
  const decoded = JSON.parse(text);
  if (decoded.chunk) fillDraftFromChunk(decoded.chunk);
  setDraftClean("Draft workspace status: duplicated · " + (decoded.chunk || {}).chunk_id);
  await loadDraftChunkList();
};
document.querySelector("#deleteDraftChunk").onclick = async () => {
  const chunkID = selectedDraftChunkID || document.querySelector("#draftChunkID").value.trim();
  if (!chunkID) {
    output.textContent = "请选择或输入要删除的 chunk_id。";
    return;
  }
  if (!window.confirm("删除 draft chunk：" + chunkID + "？")) return;
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/chunks/" + encodeURIComponent(chunkID), {
    method: "DELETE",
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: delete failed";
    return;
  }
  selectedDraftChunkID = "";
  setDraftClean("Draft workspace status: deleted · " + chunkID);
  await loadDraftChunkList();
};
document.querySelector("#searchDraftChunks").onclick = async () => {
  draftChunkOffset = 0;
  draftChunkPreviousOffset = 0;
  await loadDraftChunkList();
};
document.querySelector("#prevDraftChunkPage").onclick = async () => {
  draftChunkOffset = draftChunkPreviousOffset;
  await loadDraftChunkList();
};
document.querySelector("#nextDraftChunkPage").onclick = async () => {
  if (draftChunkNextOffset < 0) return;
  draftChunkPreviousOffset = draftChunkOffset;
  draftChunkOffset = draftChunkNextOffset;
  await loadDraftChunkList();
};
document.querySelector("#auditDraftSources").onclick = async () => {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/source-audit", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: source audit failed";
    return;
  }
  const decoded = JSON.parse(text);
  const violations = (decoded.violations || []).length;
  draftStatus.textContent = "Draft workspace status: source audit · " + violations + " violations";
};
document.querySelector("#createDraftPrompt").onclick = async () => {
  const payload = draftPromptPayloadFromForm();
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/prompts", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: create prompt failed";
    return;
  }
  selectedDraftPromptID = payload.id;
  setDraftClean("Draft workspace status: prompt created · " + selectedDraftPromptID);
  await loadDraftPromptList();
};
document.querySelector("#updateDraftPrompt").onclick = async () => {
  const originalID = selectedDraftPromptID || document.querySelector("#draftPromptID").value.trim();
  if (!originalID) {
    output.textContent = "请选择或输入要更新的 prompt id。";
    return;
  }
  const payload = draftPromptPayloadFromForm();
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/prompts/" + encodeURIComponent(originalID), {
    method: "PUT",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: update prompt failed";
    return;
  }
  selectedDraftPromptID = payload.id;
  setDraftClean("Draft workspace status: prompt updated · " + selectedDraftPromptID);
  await loadDraftPromptList();
};
document.querySelector("#deleteDraftPrompt").onclick = async () => {
  const promptID = selectedDraftPromptID || document.querySelector("#draftPromptID").value.trim();
  if (!promptID) {
    output.textContent = "请选择或输入要删除的 prompt id。";
    return;
  }
  if (!window.confirm("删除 prompt：" + promptID + "？")) return;
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/prompts/" + encodeURIComponent(promptID), {
    method: "DELETE",
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: delete prompt failed";
    return;
  }
  selectedDraftPromptID = "";
  setDraftClean("Draft workspace status: prompt deleted · " + promptID);
  await loadDraftPromptList();
};
document.querySelector("#listDraftPrompts").onclick = async () => {
  await loadDraftPromptList();
};
document.querySelector("#previewDraftPrompt").onclick = async () => {
  const promptID = selectedDraftPromptID || document.querySelector("#draftPromptID").value.trim();
  if (!promptID) {
    output.textContent = "请选择或输入要预览的 prompt id。";
    return;
  }
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/prompts/" + encodeURIComponent(promptID) + "/preview", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: prompt preview failed";
    return;
  }
  const decoded = JSON.parse(text);
  renderPreview({ kb_id: decoded.kb_id, version: decoded.version, chunks: decoded.chunks || [] });
  document.querySelector("#ragQuery").value = (decoded.prompt || {}).question || "";
  document.querySelector("#draftRetrievalQuery").value = (decoded.prompt || {}).question || "";
  draftStatus.textContent = "Draft workspace status: prompt preview · " + promptID;
};
document.querySelector("#runDraftRetrievalPreview").onclick = async () => {
  await runDraftRetrievalPreview("");
};
document.querySelector("#runDraftQualityGates").onclick = async () => {
  await runDraftQualityGates();
};
document.querySelector("#runDraftBuildDryRun").onclick = async () => {
  await runDraftBuildDryRun();
};
document.querySelector("#publishDraftLatest").onclick = async () => {
  await publishDraftLatest();
};
document.querySelector("#importMoegirlDraft").onclick = async () => {
  await importMoegirlDraft();
};
document.querySelector("#reviewMoegirlDraft").onclick = async () => {
  await reviewMoegirlDraft();
};
document.querySelector("#validateDraftBulkImport").onclick = async () => {
  await runDraftBulkImport(true);
};
document.querySelector("#importDraftBulkJSON").onclick = async () => {
  await runDraftBulkImport(false);
};
document.querySelector("#exportDraftBulkJSON").onclick = async () => {
  await exportDraftBulkJSON();
};
document.querySelector("#loadDraftReviewQueue").onclick = async () => {
  await loadDraftReviewQueue();
};
document.querySelector("#loadDraftReviewReport").onclick = async () => {
  await loadDraftReviewReport();
};
document.querySelector("#importPreviewToBuilder").onclick = () => {
  if (!lastPreview) {
    output.textContent = "请先查看知识包内容，再导入到构建器。";
    return;
  }
  document.querySelector("#builderVersion").value = todayVersion();
  document.querySelector("#builderChunks").value = pretty(lastPreview.chunks.map((chunk) => ({
    chunk_id: chunk.chunk_id,
    title: chunk.title,
    path: chunk.path,
    source: chunk.source,
    content: chunk.content
  })));
  document.querySelector("#builderPrompts").value = pretty(lastPreview.chunks.flatMap((chunk) => chunk.suggested_questions || []).slice(0, 8).map((question, index) => ({
    id: "preview-question-" + String(index + 1).padStart(2, "0"),
    title: question,
    question
  })));
  output.textContent = "已从当前预览导入构建器，请修改后发布新版本。";
};
function fillWeKnoraExportTemplate(force) {
  if (force || !document.querySelector("#weknoraExportJSON").value.trim()) {
    const template = { ...defaultWeKnoraExport, version: todayVersion() };
    document.querySelector("#weknoraExportJSON").value = pretty(template);
    document.querySelector("#weknoraVersion").value = template.version;
  }
}
async function runWeKnoraExport(path, publish) {
  try {
    const body = parseJSONField("#weknoraExportJSON", defaultWeKnoraExport);
    body.version = document.querySelector("#weknoraVersion").value.trim() || body.version || todayVersion();
    body.llm_recommended = document.querySelector("#weknoraLLM").value.split(",").map((item) => item.trim()).filter(Boolean);
    document.querySelector("#weknoraExportJSON").value = pretty(body);
    if (publish && (!lastWeKnoraDryRun || lastWeKnoraDryRun.version !== body.version || lastWeKnoraDryRun.quality_status !== "passed")) {
      output.textContent = "请先运行同版本 reviewed export 的 dry-run，并确认质量门禁通过。";
      weknoraStatus.textContent = "Exporter status: waiting for dry-run";
      return;
    }
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + path, {
      method: "POST",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    const text = await showResponse(response);
    if (!response.ok) {
      weknoraStatus.textContent = "Exporter status: failed";
      lastWeKnoraQualityGate.textContent = "failed";
      return;
    }
    const decoded = JSON.parse(text);
    lastWeKnoraDryRun = decoded;
    weknoraStatus.textContent = "Exporter status: " + (publish ? "published" : "dry-run passed") + " · " + decoded.chunk_count + " chunks";
    lastWeKnoraExportVersion.textContent = decoded.version || body.version;
    lastWeKnoraQualityGate.textContent = decoded.quality_status || "passed";
    document.querySelector("#previewVersion").value = decoded.version || body.version;
    document.querySelector("#rollbackVersion").value = decoded.version || body.version;
  } catch (error) {
    weknoraStatus.textContent = "Exporter status: failed";
    lastWeKnoraQualityGate.textContent = "failed";
    output.textContent = "WeKnora 导出失败：\n" + String(error);
  }
}
document.querySelector("#buildPublish").onclick = async () => {
  try {
    const chunksValue = parseJSONField("#builderChunks", []);
    const promptsValue = parseJSONField("#builderPrompts", []);
    const citationsValue = parseJSONField("#builderCitations", defaultCitations);
    const chunks = Array.isArray(chunksValue) ? chunksValue : chunksValue.chunks;
    const prompts = Array.isArray(promptsValue) ? promptsValue : promptsValue.prompts;
    const body = {
      version: document.querySelector("#builderVersion").value.trim(),
      chunks: chunks || [],
      prompts: prompts || [],
      citations: citationsValue,
      llm_recommended: document.querySelector("#builderLLM").value.split(",").map((item) => item.trim()).filter(Boolean)
    };
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/build-publish", {
      method: "POST",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    const text = await showResponse(response);
    if (response.ok) {
      const decoded = JSON.parse(text);
      document.querySelector("#previewVersion").value = decoded.version;
      document.querySelector("#rollbackVersion").value = decoded.version;
    }
  } catch (error) {
    output.textContent = "构建发布失败：\n" + String(error);
  }
};
document.querySelector("#buildMoegirl").onclick = async () => {
  try {
    const rawTitles = document.querySelector("#moegirlTitles").value;
    const titles = rawTitles.split(/[\n,，]/).map((item) => item.trim()).filter(Boolean);
    const body = {
      version: document.querySelector("#moegirlVersion").value.trim() || todayVersion(),
      titles,
      limit: Math.max(1, Math.min(3000, Number(document.querySelector("#moegirlLimit").value || 50))),
      llm_recommended: document.querySelector("#builderLLM").value.split(",").map((item) => item.trim()).filter(Boolean)
    };
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(moegirlKBID()) + "/moegirl/build-publish", {
      method: "POST",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    const text = await showResponse(response);
    if (response.ok) {
      const decoded = JSON.parse(text);
      document.querySelector("#previewVersion").value = decoded.version;
      document.querySelector("#rollbackVersion").value = decoded.version;
      document.querySelector("#moegirlVersion").value = todayVersion();
    }
  } catch (error) {
    output.textContent = "萌娘百科摘要包构建发布失败：\n" + String(error);
  }
};
document.querySelector("#publish").onclick = async () => {
  const form = new FormData();
  form.set("version", document.querySelector("#version").value.trim());
  form.set("manifest", document.querySelector("#manifest").files[0]);
  form.set("package", document.querySelector("#package").files[0]);
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/versions", {
    method: "POST",
    headers: { Authorization: "Bearer " + token() },
    body: form
  });
  await showResponse(response);
};
document.querySelector("#refresh").onclick = async () => {
  const response = await fetch(servicePrefix + "/kb/" + encodeURIComponent(kbID()) + "/versions");
  const text = await response.text();
  versions.textContent = text;
  try {
    const decoded = JSON.parse(text);
    if (decoded.latest) {
      document.querySelector("#rollbackVersion").value = decoded.latest;
      document.querySelector("#previewVersion").value = decoded.latest;
    }
  } catch (_) {}
  output.textContent = "versions refreshed: " + response.status;
};
document.querySelector("#preview").onclick = async () => {
  const version = document.querySelector("#previewVersion").value.trim();
  const limit = Math.max(1, Math.min(50, Number(document.querySelector("#previewLimit").value || 12)));
  const path = version
    ? "/kb/" + encodeURIComponent(kbID()) + "/versions/" + encodeURIComponent(version) + "/preview"
    : "/kb/" + encodeURIComponent(kbID()) + "/latest/preview";
  const response = await fetch(servicePrefix + path + "?limit=" + encodeURIComponent(String(limit)));
  const text = await response.text();
  previewRaw.textContent = response.status + "\n" + text;
  if (!response.ok) {
    previewSummary.textContent = "内容预览失败";
    previewChunks.replaceChildren();
    lastPreview = null;
    output.textContent = "preview failed: " + response.status;
    return;
  }
  lastPreview = JSON.parse(text);
  renderPreview(lastPreview);
  output.textContent = "preview loaded: " + response.status;
};
document.querySelector("#runRagCompare").onclick = async () => {
  const query = document.querySelector("#ragQuery").value.trim();
  const topK = Math.max(1, Math.min(12, Number(document.querySelector("#ragTopK").value || 5)));
  if (!query) {
    output.textContent = "请输入 RAG 对比问题。";
    return;
  }
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/rag/compare", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify({ query, top_k: topK })
  });
  const text = await response.text();
  ragCompareRaw.textContent = response.status + "\n" + text;
  if (!response.ok) {
    localRagStatus.textContent = "failed";
    remoteRagStatus.textContent = "failed";
    localRagChunks.replaceChildren();
    remoteRagChunks.replaceChildren();
    output.textContent = "RAG compare failed: " + response.status;
    return;
  }
  const decoded = JSON.parse(text);
  renderRagCompare(decoded);
  output.textContent = "RAG compare loaded: " + response.status;
};
document.querySelector("#copyRagQuestion").onclick = async () => {
  const query = document.querySelector("#ragQuery").value.trim();
  if (!query) {
    output.textContent = "没有可复制的问题。";
    return;
  }
  await navigator.clipboard.writeText(query);
  output.textContent = "已复制问题，可到 App 中提问：\n" + query;
};
document.querySelector("#rollback").onclick = async () => {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/latest", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify({ version: document.querySelector("#rollbackVersion").value.trim() })
  });
  await showResponse(response);
};
async function loadDraftChunkList() {
  const params = new URLSearchParams();
  const query = document.querySelector("#draftChunkSearch").value.trim();
  const reviewStatus = document.querySelector("#draftReviewFilter").value.trim();
  const limit = draftChunkLimit();
  if (query) params.set("q", query);
  if (reviewStatus) params.set("review_status", reviewStatus);
  params.set("limit", String(limit));
  params.set("offset", String(draftChunkOffset));
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/chunks" + (params.toString() ? "?" + params.toString() : ""), {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await response.text();
  output.textContent = response.status + "\n" + text;
  if (!response.ok) {
    preserveUnsavedDraftOnError("Draft workspace status: list failed");
    return;
  }
  const decoded = JSON.parse(text);
  draftChunkNextOffset = typeof decoded.next_offset === "number" ? decoded.next_offset : -1;
  renderDraftChunkList(decoded.chunks || [], decoded.total || 0, decoded.matched || 0, decoded.limit || limit, decoded.offset || 0, draftChunkNextOffset);
  draftStatus.textContent = "Draft workspace status: listed · " + decoded.matched + "/" + decoded.total + " chunks";
}
function draftChunkLimit() {
  return Math.max(25, Math.min(500, Number(document.querySelector("#draftChunkPageSize").value || 100)));
}
function renderDraftChunkList(chunks, total, matched, limit, offset, nextOffset) {
  offset = Number(offset || 0);
  limit = Number(limit || chunks.length);
  if (typeof nextOffset !== "number") nextOffset = -1;
  draftChunksList.replaceChildren(...chunks.map(renderDraftListChunk));
  draftChunkPageStatus.textContent = "limit=" + String(limit) + " · offset=" + String(offset) + " · shown=" + String(chunks.length) + " · matched=" + String(matched);
  document.querySelector("#prevDraftChunkPage").disabled = offset <= 0;
  document.querySelector("#nextDraftChunkPage").disabled = nextOffset < 0;
  if (chunks.length === 0) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = "No draft chunks · " + matched + "/" + total;
    draftChunksList.append(empty);
  }
}
function renderDraftListChunk(chunk) {
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = chunk.title || chunk.chunk_id;
  card.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [chunk.chunk_id, chunk.review_status || "draft", chunk.source, chunk.path, (chunk.tags || []).join(", ")].filter(Boolean).join(" · ");
  card.append(meta);

  const content = document.createElement("p");
  content.className = "chunk-content";
  content.textContent = chunk.content || "";
  card.append(content);

  const edit = document.createElement("button");
  edit.className = "copy";
  edit.type = "button";
  edit.textContent = "编辑";
  edit.onclick = () => {
    if (!confirmDiscardDraftChanges()) return;
    fillDraftFromChunk(chunk);
  };
  card.append(edit);
  return card;
}
async function loadDraftPromptList() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/prompts", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await response.text();
  output.textContent = response.status + "\n" + text;
  if (!response.ok) {
    draftPromptsList.replaceChildren();
    draftStatus.textContent = "Draft workspace status: prompt list failed";
    return;
  }
  const decoded = JSON.parse(text);
  renderDraftPromptList(decoded.prompts || [], decoded.total || 0);
  draftStatus.textContent = "Draft workspace status: prompts listed · " + decoded.total;
}
function renderDraftPromptList(prompts, total) {
  draftPromptsList.replaceChildren(...prompts.map(renderDraftPromptCard));
  if (prompts.length === 0) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = "No prompts · " + total;
    draftPromptsList.append(empty);
  }
}
function renderDraftPromptCard(prompt) {
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = prompt.title || prompt.id;
  card.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [prompt.id, prompt.answerability || (prompt.answerable === false ? "refusal" : "answerable"), (prompt.expected_chunk_ids || []).join(", "), (prompt.tags || []).join(", ")].filter(Boolean).join(" · ");
  card.append(meta);

  const question = document.createElement("p");
  question.className = "chunk-content";
  question.textContent = prompt.question || prompt.text || "";
  card.append(question);

  const edit = document.createElement("button");
  edit.className = "copy";
  edit.type = "button";
  edit.textContent = "编辑";
  edit.onclick = () => {
    if (!confirmDiscardDraftChanges()) return;
    fillDraftFromPrompt(prompt);
  };
  card.append(edit);

  const preview = document.createElement("button");
  preview.className = "copy";
  preview.type = "button";
  preview.textContent = "预览";
  preview.onclick = async () => {
    fillDraftFromPrompt(prompt);
    document.querySelector("#draftRetrievalQuery").value = prompt.question || prompt.text || "";
    await runDraftRetrievalPreview(prompt.id || "");
  };
  card.append(preview);
  return card;
}
async function runDraftRetrievalPreview(promptID) {
  const topK = Math.max(1, Math.min(12, Number(document.querySelector("#draftRetrievalTopK").value || 5)));
  const body = {
    query: document.querySelector("#draftRetrievalQuery").value.trim(),
    prompt_id: promptID || "",
    top_k: topK
  };
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/retrieval-preview", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify(body)
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftRetrievalResults.replaceChildren();
    draftStatus.textContent = "Draft workspace status: draft retrieval failed";
    return;
  }
  const decoded = JSON.parse(text);
  renderDraftRetrievalResults(decoded);
  draftStatus.textContent = "Draft workspace status: draft retrieval · " + decoded.status + " · " + (decoded.results || []).length + " chunks";
}
async function runDraftQualityGates() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/quality-gates", {
    method: "POST",
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    document.querySelector("#draftQualityGateReport").replaceChildren();
    draftStatus.textContent = "Draft workspace status: quality gates failed";
    return;
  }
  const decoded = JSON.parse(text);
  renderDraftQualityGateReport(decoded);
  lastWeKnoraQualityGate.textContent = decoded.status || "unknown";
  draftStatus.textContent = "Draft workspace status: quality gates · " + decoded.status + " · block_publish=" + String(decoded.block_publish);
}
function renderDraftQualityGateReport(report) {
  const container = document.querySelector("#draftQualityGateReport");
  const metrics = report.metrics || {};
  const thresholds = report.thresholds || {};
  const summary = document.createElement("article");
  summary.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = "Quality Gates · " + (report.status || "unknown");
  summary.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    report.kb_id,
    report.version,
    "block_publish=" + String(report.block_publish),
    "latency_ms=" + String(report.latency_ms || 0)
  ].filter(Boolean).join(" · ");
  summary.append(meta);

  const metricText = document.createElement("p");
  metricText.className = "chunk-content";
  metricText.textContent = [
    "top1_hit_rate=" + qualityRate(metrics.top1_hit_rate),
    "top5_hit_rate=" + qualityRate(metrics.top5_hit_rate) + " / min " + qualityRate(thresholds.top5_hit_rate),
    "citation_rate=" + qualityRate(metrics.citation_rate) + " / min " + qualityRate(thresholds.citation_rate),
    "duplicate_answer_rate=" + qualityRate(metrics.duplicate_answer_rate) + " / max " + qualityRate(thresholds.duplicate_answer_rate),
    "refusal_pass_rate=" + qualityRate(metrics.refusal_pass_rate) + " / min " + qualityRate(thresholds.refusal_pass_rate),
    "missing_citation_count=" + String(metrics.missing_citation_count || 0),
    "unsupported_entity_count=" + String(metrics.unsupported_entity_count || 0)
  ].join(" · ");
  summary.append(metricText);

  const checks = (report.checks || []).map(renderDraftQualityCheck);
  container.replaceChildren(summary, ...checks);
}
function renderDraftQualityCheck(check) {
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = [check.name, check.status, check.severity].filter(Boolean).join(" · ");
  card.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    "count=" + String(check.count || 0),
    (check.chunk_ids || []).length ? "chunk_ids=" + (check.chunk_ids || []).join(", ") : "",
    (check.prompt_ids || []).length ? "prompt_ids=" + (check.prompt_ids || []).join(", ") : ""
  ].filter(Boolean).join(" · ");
  card.append(meta);

  const remediation = document.createElement("p");
  remediation.className = "chunk-content";
  remediation.textContent = check.remediation || "";
  card.append(remediation);
  return card;
}
function qualityRate(value) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0.0%";
  return (value * 100).toFixed(1) + "%";
}
async function runDraftBuildDryRun() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/build-dry-run?limit=50", {
    method: "POST",
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  const container = document.querySelector("#draftDryRunBuildReport");
  if (!response.ok) {
    container.replaceChildren();
    draftStatus.textContent = "Draft workspace status: dry-run build failed";
    try {
      const decoded = JSON.parse(text);
      if (decoded.quality_report) renderDraftQualityGateReport(decoded.quality_report);
    } catch (_) {}
    return;
  }
  const decoded = JSON.parse(text);
  renderDraftDryRunBuildReport(decoded);
  if (decoded.preview) renderPreview(decoded.preview);
  lastWeKnoraExportVersion.textContent = decoded.version || "-";
  lastWeKnoraQualityGate.textContent = decoded.quality_status || "unknown";
  draftStatus.textContent = "Draft workspace status: dry-run build · " + decoded.quality_status + " · " + decoded.chunk_count + " chunks";
}
function renderDraftDryRunBuildReport(result) {
  const container = document.querySelector("#draftDryRunBuildReport");
  const summary = document.createElement("article");
  summary.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = "Dry-run Build · " + (result.version || "draft");
  summary.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    result.kb_id,
    "latest=" + String(result.latest),
    "quality_status=" + (result.quality_status || "unknown"),
    "package_hash=" + (result.package_hash || "")
  ].filter(Boolean).join(" · ");
  summary.append(meta);

  const counts = document.createElement("p");
  counts.className = "chunk-content";
  counts.textContent = [
    "chunk_count=" + String(result.chunk_count || 0),
    "citation_count=" + String(result.citation_count || 0),
    "prompt_count=" + String(result.prompt_count || 0),
    "preview_url=" + (result.preview_url || "")
  ].join(" · ");
  summary.append(counts);

  const files = document.createElement("article");
  files.className = "chunk-card";
  const filesTitle = document.createElement("h3");
  filesTitle.textContent = "Generated files";
  files.append(filesTitle);
  const fileText = document.createElement("p");
  fileText.className = "chunk-content";
  fileText.textContent = (result.files || []).map((file) => file.path + " (" + String(file.size || 0) + " bytes)").join(" · ");
  files.append(fileText);

  const manifest = document.createElement("article");
  manifest.className = "chunk-card";
  const manifestTitle = document.createElement("h3");
  manifestTitle.textContent = "Manifest preview";
  manifest.append(manifestTitle);
  const manifestMeta = document.createElement("div");
  manifestMeta.className = "chunk-meta";
  const manifestData = result.manifest || {};
  manifestMeta.textContent = [
    manifestData.schema_version,
    manifestData.kb_id,
    manifestData.version,
    manifestData.content_hash
  ].filter(Boolean).join(" · ");
  manifest.append(manifestMeta);

  container.replaceChildren(summary, files, manifest);
}
async function publishDraftLatest() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/publish", {
    method: "POST",
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: publish failed";
    return;
  }
  const decoded = JSON.parse(text);
  lastWeKnoraExportVersion.textContent = decoded.version || "-";
  lastWeKnoraQualityGate.textContent = decoded.gate_status || "unknown";
  document.querySelector("#previewVersion").value = decoded.version || "";
  document.querySelector("#rollbackVersion").value = decoded.version || "";
  draftStatus.textContent = "Draft workspace status: published latest · " + decoded.version + " · " + (decoded.content_hash || "");
}
async function importMoegirlDraft() {
  const version = document.querySelector("#moegirlDraftVersion").value.trim() || draftVersion();
  const rawTitles = document.querySelector("#moegirlDraftTitles").value;
  const titles = rawTitles.split(/[\n,，]/).map((item) => item.trim()).filter(Boolean);
  const body = {
    version,
    titles,
    limit: Math.max(1, Math.min(3000, Number(document.querySelector("#moegirlDraftLimit").value || 50)))
  };
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(moegirlKBID()) + "/moegirl/import-draft", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify(body)
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: moegirl import failed";
    return;
  }
  const decoded = JSON.parse(text);
  document.querySelector("#kbID").value = decoded.kb_id || moegirlKBID();
  document.querySelector("#draftVersion").value = decoded.version || version;
  renderMoegirlDraftReport(decoded.review_report || {});
  await loadDraftChunkList();
  await loadDraftPromptList();
  draftStatus.textContent = "Draft workspace status: moegirl draft imported · " + decoded.chunk_count + " chunks";
}
async function reviewMoegirlDraft() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(moegirlKBID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/moegirl-review", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: moegirl review failed";
    return;
  }
  renderMoegirlDraftReport(JSON.parse(text));
  draftStatus.textContent = "Draft workspace status: moegirl review loaded";
}
function renderMoegirlDraftReport(report) {
  const container = document.querySelector("#moegirlDraftImportReport");
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = "Moegirl Draft Review";
  card.append(title);

  const target = report.target || {};
  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    report.kb_id,
    report.version,
    "chunks=" + String(report.chunk_count || 0),
    "prompts=" + String(report.prompt_count || 0),
    "citations=" + String(report.citation_count || 0),
    "full_mirror_suspect_count=" + String(report.full_mirror_suspect_count || 0),
    "missing_metadata_count=" + String(report.missing_metadata_count || 0)
  ].filter(Boolean).join(" · ");
  card.append(meta);

  const content = document.createElement("p");
  content.className = "chunk-content";
  content.textContent = [
    "accepted_pages=" + String(target.accepted_pages || 0) + "/" + String(target.accepted_pages_required || 300),
    "faq_chunks=" + String(target.faq_chunks || 0) + "/" + String(target.faq_chunks_required || 900),
    "golden_questions=" + String(target.golden_questions || 0) + "/" + String(target.golden_questions_required || 50),
    "ready_for_hitl=" + String(Boolean(target.ready_for_hitl)),
    "full_mirror_suspect_chunk_ids=" + (report.full_mirror_suspect_chunk_ids || []).join(", ")
  ].join(" · ");
  card.append(content);
  container.replaceChildren(card);
}
async function runDraftBulkImport(dryRun) {
  let body;
  try {
    body = JSON.parse(document.querySelector("#draftBulkJSON").value || "{}");
  } catch (error) {
    output.textContent = "Draft JSON 解析失败：\n" + String(error);
    return;
  }
  const path = "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/import" + (dryRun ? "?dry_run=1" : "");
  const response = await fetch(servicePrefix + path, {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify(body)
  });
  const text = await showResponse(response);
  if (!response.ok) {
    preserveUnsavedDraftOnError("Draft workspace status: bulk import failed");
    try {
      const decoded = JSON.parse(text);
      if (Array.isArray(decoded.field_errors)) {
        output.textContent = response.status + "\n" + decoded.error + "\n" + decoded.field_errors.map((item) => item.field + ": " + item.remediation).join("\n");
      }
    } catch (_) {}
    return;
  }
  renderDraftReviewMaterials(JSON.parse(text), dryRun ? "Bulk import validation" : "Bulk import saved");
  if (!dryRun) await loadDraftChunkList();
  draftStatus.textContent = "Draft workspace status: " + (dryRun ? "bulk import validated" : "bulk import saved");
}
async function exportDraftBulkJSON() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/export", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: export failed";
    return;
  }
  document.querySelector("#draftBulkJSON").value = pretty(JSON.parse(text));
  draftStatus.textContent = "Draft workspace status: canonical draft exported";
}
async function loadDraftReviewQueue() {
  const filter = document.querySelector("#draftReviewQueueFilter").value;
  const params = new URLSearchParams();
  if (filter) params.set("filter", filter);
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/review-queue?" + params.toString(), {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: review queue failed";
    return;
  }
  renderDraftReviewQueue(JSON.parse(text));
  draftStatus.textContent = "Draft workspace status: review queue loaded";
}
async function loadDraftReviewReport() {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(draftVersion()) + "/review-report", {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: review report failed";
    return;
  }
  renderDraftReviewMaterials(JSON.parse(text), "Review report");
  draftStatus.textContent = "Draft workspace status: review report loaded";
}
function renderDraftReviewQueue(queue) {
  const cards = (queue.items || []).map((item) => {
    const card = renderDraftListChunk(item.chunk || {});
    const meta = document.createElement("div");
    meta.className = "chunk-meta";
    meta.textContent = "review_reasons=" + (item.reasons || []).join(", ");
    card.append(meta);
    return card;
  });
  document.querySelector("#draftReviewMaterials").replaceChildren(...cards);
}
function renderDraftReviewMaterials(report, titleText) {
  const card = document.createElement("article");
  card.className = "chunk-card";
  const title = document.createElement("h3");
  title.textContent = titleText;
  card.append(title);
  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    report.kb_id,
    report.version,
    "chunk_count=" + String(report.chunk_count || 0),
    "sample_count=" + String(report.sample_count || 0),
    "missing_citation_count=" + String(report.missing_citation_count || 0),
    "duplicate_count=" + String(report.duplicate_count || 0),
    "contamination_count=" + String(report.contamination_count || 0),
    "golden_prompt_count=" + String(report.golden_prompt_count || 0),
    "quality_status=" + (report.quality_status || "")
  ].filter(Boolean).join(" · ");
  card.append(meta);
  document.querySelector("#draftReviewMaterials").replaceChildren(card);
}
function renderDraftRetrievalResults(preview) {
  draftRetrievalResults.replaceChildren(...(preview.results || []).map(renderDraftRetrievalResult));
  if ((preview.results || []).length === 0) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = [preview.status, ...(preview.reasons || [])].filter(Boolean).join(" · ");
    draftRetrievalResults.append(empty);
  }
}
function renderDraftRetrievalResult(result) {
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = result.title || result.chunk_id;
  card.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [
    result.chunk_id,
    result.path,
    result.source,
    "score=" + Number(result.score || 0).toFixed(3),
    "terms=" + (result.matched_terms || []).join(", "),
    (result.reasons || []).join(", ")
  ].filter(Boolean).join(" · ");
  card.append(meta);

  const snippet = document.createElement("p");
  snippet.className = "chunk-content";
  snippet.textContent = result.snippet || "";
  card.append(snippet);

  if (result.citation && (result.citation.url || result.citation.license || result.citation.source_policy)) {
    const citation = document.createElement("div");
    citation.className = "chunk-meta";
    citation.textContent = [
      result.citation.title ? "引用：" + result.citation.title : "",
      result.citation.source_name ? "来源：" + result.citation.source_name : "",
      result.citation.license ? "许可：" + result.citation.license : "",
      result.citation.source_policy ? "策略：" + result.citation.source_policy : ""
    ].filter(Boolean).join(" · ");
    if (result.citation.url) {
      const link = document.createElement("a");
      link.href = result.citation.url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "打开来源";
      citation.append(" · ", link);
    }
    card.append(citation);
  }
  return card;
}
function renderPreview(preview) {
  previewSummary.textContent = preview.kb_id + " · " + preview.version + " · " + preview.chunks.length + " chunks";
  previewChunks.replaceChildren(...preview.chunks.map(renderChunk));
}
function renderChunk(chunk) {
  const card = document.createElement("article");
  card.className = "chunk-card";

  const title = document.createElement("h3");
  title.textContent = chunk.title || chunk.chunk_id;
  card.append(title);

  const meta = document.createElement("div");
  meta.className = "chunk-meta";
  meta.textContent = [chunk.chunk_id, chunk.source, chunk.path].filter(Boolean).join(" · ");
  card.append(meta);

  const content = document.createElement("p");
  content.className = "chunk-content";
  content.textContent = chunk.content;
  card.append(content);

  if (chunk.source_url || chunk.license || chunk.source_policy || chunk.citation_title || chunk.source_name) {
    const citation = document.createElement("div");
    citation.className = "chunk-meta";
    const citationParts = [
      chunk.citation_title ? "引用：" + chunk.citation_title : "",
      chunk.source_name ? "来源：" + chunk.source_name : "",
      chunk.license ? "许可：" + chunk.license : "",
      chunk.source_policy ? "策略：" + chunk.source_policy : "",
      chunk.revision_id ? "revision：" + chunk.revision_id : "",
      chunk.source_page_id ? "page：" + chunk.source_page_id : ""
    ].filter(Boolean);
    citation.textContent = citationParts.join(" · ");
    if (chunk.source_url) {
      const link = document.createElement("a");
      link.href = chunk.source_url;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "打开来源";
      citation.append(" · ", link);
    }
    card.append(citation);
  }

  const questions = document.createElement("div");
  questions.className = "question-row";
  for (const question of chunk.suggested_questions || []) {
    const button = document.createElement("button");
    button.className = "copy";
    button.type = "button";
    button.textContent = question;
    button.onclick = async () => {
      await navigator.clipboard.writeText(question);
      output.textContent = "已复制问题，可到 App 中提问：\n" + question;
    };
    questions.append(button);
  }
  card.append(questions);
  return card;
}
function renderRagCompare(compare) {
  const local = compare.local || {};
  const remote = compare.remote || {};
  localRagStatus.textContent = [local.status, local.version, (local.chunks || []).length + " chunks"].filter(Boolean).join(" · ");
  if (local.error) localRagStatus.textContent += " · " + local.error.code;
  remoteRagStatus.textContent = [remote.status, remote.provider, remote.knowledge_version, remote.latency_ms ? remote.latency_ms + "ms" : "", (remote.chunks || []).length + " chunks"].filter(Boolean).join(" · ");
  if (remote.error) remoteRagStatus.textContent += " · " + remote.error.code;
  localRagChunks.replaceChildren(...(local.chunks || []).map(renderChunk));
  remoteRagChunks.replaceChildren(...(remote.chunks || []).map(renderRemoteChunk));
}
function renderRemoteChunk(chunk) {
  return renderChunk({
    chunk_id: chunk.chunk_id,
    title: chunk.title,
    path: chunk.path,
    source: [chunk.source, chunk.score != null ? "score=" + chunk.score : ""].filter(Boolean).join(" · "),
    content: chunk.content,
    suggested_questions: []
  });
}
</script>
</body>
</html>`

func (h *Handler) handleSetLatest(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	kbID, ok := strings.CutPrefix(r.URL.Path, "/admin/api/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/latest")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	version, err := safeComponent(payload.Version)
	if err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(h.versionDir(kbID, version)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		http.Error(w, "version lookup failed", http.StatusInternalServerError)
		return
	}
	if err := h.writeLatestVersion(kbID, version); err != nil {
		http.Error(w, "write latest version failed", http.StatusInternalServerError)
		return
	}
	rollbackAt := time.Now().UTC().Format(time.RFC3339Nano)
	contentHash := h.publishedVersionContentHash(kbID, version)
	if err := h.appendAuditLog(kbID, map[string]any{
		"event":        "rollback_latest",
		"version":      version,
		"content_hash": contentHash,
		"gate_status":  "not_applicable",
		"rollback_at":  rollbackAt,
		"actor":        "admin",
	}); err != nil {
		http.Error(w, "write audit log failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":        kbID,
		"version":      version,
		"latest":       true,
		"content_hash": contentHash,
		"rollback_at":  rollbackAt,
	})
}

func (h *Handler) handleLatestManifest(w http.ResponseWriter, r *http.Request) {
	kbID, ok := strings.CutPrefix(r.URL.Path, "/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/latest/manifest.json")
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
	http.ServeFile(w, r, filepath.Join(h.versionDir(kbID, version), "manifest.json"))
}

func (h *Handler) handleListVersions(w http.ResponseWriter, r *http.Request) {
	kbID, ok := strings.CutPrefix(r.URL.Path, "/kb/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, ok = strings.CutSuffix(kbID, "/versions")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kbID, err := safeComponent(kbID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	latest, err := h.latestVersion(kbID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	entries, err := os.ReadDir(filepath.Join(h.kbDir(kbID), "versions"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	versions := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		versions = append(versions, map[string]any{
			"version": version,
			"latest":  version == latest,
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i]["version"].(string) > versions[j]["version"].(string)
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":    kbID,
		"latest":   latest,
		"versions": versions,
	})
}

func (h *Handler) handleVersionPackage(w http.ResponseWriter, r *http.Request) {
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
	version, ok := strings.CutSuffix(rest, "/knowledge-pack.zip")
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

	w.Header().Set("Content-Type", "application/zip")
	http.ServeFile(w, r, filepath.Join(h.versionDir(kbID, version), "knowledge-pack.zip"))
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.adminToken == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+h.adminToken
}

func (h *Handler) kbDir(kbID string) string {
	return filepath.Join(h.storageDir, "kb", kbID)
}

func (h *Handler) versionDir(kbID string, version string) string {
	return filepath.Join(h.kbDir(kbID), "versions", version)
}

func (h *Handler) latestVersion(kbID string) (string, error) {
	data, err := os.ReadFile(filepath.Join(h.kbDir(kbID), "latest"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (h *Handler) writeLatestVersion(kbID string, version string) error {
	kbDir := h.kbDir(kbID)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		return err
	}
	latestPath := filepath.Join(kbDir, "latest")
	tmpPath := latestPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, latestPath)
}

var errVersionAlreadyExists = errors.New("version already exists")

func (h *Handler) storePublishedVersion(kbID string, version string, manifest []byte, packageReader io.Reader) error {
	versionDir := h.versionDir(kbID, version)
	if _, err := os.Stat(versionDir); err == nil {
		return errVersionAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("version lookup failed: %w", err)
	}

	installingDir := filepath.Join(h.kbDir(kbID), ".installing-"+version)
	_ = os.RemoveAll(installingDir)
	if err := os.MkdirAll(installingDir, 0o755); err != nil {
		return fmt.Errorf("create version directory failed: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(installingDir)
		}
	}()

	if err := os.WriteFile(filepath.Join(installingDir, "manifest.json"), manifest, 0o644); err != nil {
		return fmt.Errorf("write manifest failed: %w", err)
	}
	if err := writeStream(filepath.Join(installingDir, "knowledge-pack.zip"), packageReader); err != nil {
		return fmt.Errorf("write package failed: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(versionDir), 0o755); err != nil {
		return fmt.Errorf("create versions directory failed: %w", err)
	}
	if err := os.Rename(installingDir, versionDir); err != nil {
		return fmt.Errorf("activate version directory failed: %w", err)
	}
	cleanup = false

	if err := h.writeLatestVersion(kbID, version); err != nil {
		return fmt.Errorf("write latest version failed: %w", err)
	}
	return nil
}

func writePublishError(w http.ResponseWriter, err error) {
	if errors.Is(err, errVersionAlreadyExists) {
		http.Error(w, "version already exists", http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func normalizedSigningSeed(seed []byte) ([]byte, error) {
	if len(seed) == 0 {
		return nil, nil
	}
	if len(seed) == 32 {
		return append([]byte(nil), seed...), nil
	}
	if len(seed) == 64 {
		return append([]byte(nil), seed[:32]...), nil
	}
	return nil, fmt.Errorf("knowledge pack signing key must be 32-byte seed or 64-byte private key")
}

func readFormFile(r *http.Request, field string) ([]byte, error) {
	file, _, err := r.FormFile(field)
	if err != nil {
		return nil, fmt.Errorf("%s file is required", field)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s failed", field)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s file is empty", field)
	}
	return data, nil
}

func writeStream(path string, reader io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, reader)
	return err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var safeComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func safeComponent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !safeComponentPattern.MatchString(value) {
		return "", fmt.Errorf("unsafe path component")
	}
	return value, nil
}
