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
    :root { color-scheme: light; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f7f8f3; color: #14211d; }
    main { max-width: 960px; margin: 0 auto; padding: 28px 18px 48px; }
    h1 { font-size: 32px; margin: 0 0 8px; }
    section { background: #fffef7; border: 1px solid #d9dfd2; border-radius: 8px; padding: 18px; margin-top: 18px; }
    label { display: grid; gap: 6px; margin: 12px 0; font-weight: 650; }
    input, button, textarea { font: inherit; }
    input, textarea { padding: 10px; border: 1px solid #b9c2b2; border-radius: 6px; background: white; }
    textarea { min-height: 140px; resize: vertical; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; line-height: 1.45; }
    button { border: 0; border-radius: 6px; background: #0f766e; color: white; padding: 10px 14px; cursor: pointer; }
    button.secondary { background: #334155; }
    button.copy { background: #ecfccb; color: #25330e; border: 1px solid #c7dca4; margin: 4px 6px 4px 0; }
    pre { white-space: pre-wrap; word-break: break-word; background: #0f172a; color: #e2e8f0; padding: 12px; border-radius: 6px; min-height: 80px; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
    .muted { color: #647066; }
    .chunk-list { display: grid; gap: 12px; margin-top: 14px; }
    .chunk-card { border: 1px solid #d9dfd2; border-radius: 8px; padding: 14px; background: #fffffb; }
    .chunk-card h3 { margin: 0 0 6px; font-size: 18px; }
    .chunk-meta { color: #647066; font-size: 13px; margin-bottom: 8px; }
    .chunk-content { line-height: 1.55; margin: 8px 0 10px; }
    .question-row { display: flex; flex-wrap: wrap; gap: 4px; }
    .compare-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 14px; margin-top: 14px; }
    .compare-column { border: 1px solid #d9dfd2; border-radius: 8px; padding: 12px; background: #fffffb; }
    .compare-status { font-weight: 700; margin: 0 0 10px; }
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
    <div class="grid">
      <label>Draft version <input id="draftVersion" placeholder="2026.06.26.draft"></label>
      <label>Chunk ID <input id="draftChunkID" placeholder="draft-topic-001"></label>
      <label>Title <input id="draftChunkTitle" placeholder="知识点标题"></label>
      <label>Path <input id="draftChunkPath" placeholder="topic/category/name"></label>
      <label>Source <input id="draftChunkSource" value="manual"></label>
    </div>
    <label>Content <textarea id="draftChunkContent" spellcheck="false" placeholder="这里写 chunk 内容。保存后会进入草稿预览，但不会修改 latest。"></textarea></label>
    <button id="saveDraft" type="button">保存草稿</button>
    <button id="loadDraft" class="secondary" type="button">读取草稿</button>
    <button id="previewDraft" class="secondary" type="button">预览草稿 chunk</button>
    <p id="draftStatus" class="muted">Draft workspace status: not saved</p>
    <p id="weknoraStatus" class="muted">Chunk Studio status: direction locked; draft editor is tracked in #42/#43.</p>
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
const servicePrefix = location.pathname.includes("/admin") ? location.pathname.split("/admin")[0] : "";
let lastPreview = null;
let lastWeKnoraDryRun = null;
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
  if (force || !document.querySelector("#draftChunkContent").value.trim()) {
    document.querySelector("#draftChunkContent").value = chunk.content;
  }
}
function draftVersion() {
  return document.querySelector("#draftVersion").value.trim() || todayVersion() + "-draft";
}
function draftPayloadFromForm() {
  return {
    chunks: [
      {
        chunk_id: document.querySelector("#draftChunkID").value.trim(),
        title: document.querySelector("#draftChunkTitle").value.trim(),
        path: document.querySelector("#draftChunkPath").value.trim(),
        source: document.querySelector("#draftChunkSource").value.trim(),
        content: document.querySelector("#draftChunkContent").value.trim()
      }
    ],
    prompts: [],
    citations: defaultCitations
  };
}
function fillDraftFromChunk(chunk) {
  document.querySelector("#draftChunkID").value = chunk.chunk_id || "";
  document.querySelector("#draftChunkTitle").value = chunk.title || "";
  document.querySelector("#draftChunkPath").value = chunk.path || "";
  document.querySelector("#draftChunkSource").value = chunk.source || "";
  document.querySelector("#draftChunkContent").value = chunk.content || "";
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
    const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(version), {
      method: "PUT",
      headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
      body: JSON.stringify(draftPayloadFromForm())
    });
    const text = await showResponse(response);
    if (!response.ok) {
      draftStatus.textContent = "Draft workspace status: save failed";
      return;
    }
    const decoded = JSON.parse(text);
    draftStatus.textContent = "Draft workspace status: saved · " + decoded.version + " · " + decoded.chunk_count + " chunks";
  } catch (error) {
    draftStatus.textContent = "Draft workspace status: save failed";
    output.textContent = "保存草稿失败：\n" + String(error);
  }
};
document.querySelector("#loadDraft").onclick = async () => {
  const version = draftVersion();
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/drafts/" + encodeURIComponent(version), {
    headers: { Authorization: "Bearer " + token() }
  });
  const text = await showResponse(response);
  if (!response.ok) {
    draftStatus.textContent = "Draft workspace status: load failed";
    return;
  }
  const decoded = JSON.parse(text);
  document.querySelector("#draftVersion").value = decoded.version;
  if ((decoded.chunks || [])[0]) fillDraftFromChunk(decoded.chunks[0]);
  draftStatus.textContent = "Draft workspace status: loaded · " + decoded.version + " · " + (decoded.chunks || []).length + " chunks";
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

	writeJSON(w, http.StatusOK, map[string]any{
		"kb_id":   kbID,
		"version": version,
		"latest":  true,
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
