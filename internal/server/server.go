package server

import (
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
	StorageDir string
	AdminToken string
}

type Handler struct {
	storageDir string
	adminToken string
}

func NewHandler(options Options) (http.Handler, error) {
	if strings.TrimSpace(options.StorageDir) == "" {
		return nil, errors.New("storage dir is required")
	}
	if err := os.MkdirAll(options.StorageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &Handler{
		storageDir: options.StorageDir,
		adminToken: options.AdminToken,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	case r.Method == http.MethodGet && (r.URL.Path == "/admin" || r.URL.Path == "/admin/"):
		h.handleAdminPage(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/versions"):
		h.handlePublishVersion(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/api/kb/") && strings.HasSuffix(r.URL.Path, "/latest"):
		h.handleSetLatest(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.HasSuffix(r.URL.Path, "/latest/manifest.json"):
		h.handleLatestManifest(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.HasSuffix(r.URL.Path, "/versions"):
		h.handleListVersions(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/kb/") && strings.Contains(r.URL.Path, "/versions/") && strings.HasSuffix(r.URL.Path, "/knowledge-pack.zip"):
		h.handleVersionPackage(w, r)
	default:
		http.NotFound(w, r)
	}
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

	versionDir := h.versionDir(kbID, version)
	if _, err := os.Stat(versionDir); err == nil {
		http.Error(w, "version already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "version lookup failed", http.StatusInternalServerError)
		return
	}

	packageFile, _, err := r.FormFile("package")
	if err != nil {
		http.Error(w, "package file is required", http.StatusBadRequest)
		return
	}
	defer packageFile.Close()

	installingDir := filepath.Join(h.kbDir(kbID), ".installing-"+version)
	_ = os.RemoveAll(installingDir)
	if err := os.MkdirAll(installingDir, 0o755); err != nil {
		http.Error(w, "create version directory failed", http.StatusInternalServerError)
		return
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(installingDir)
		}
	}()

	if err := os.WriteFile(filepath.Join(installingDir, "manifest.json"), manifest, 0o644); err != nil {
		http.Error(w, "write manifest failed", http.StatusInternalServerError)
		return
	}
	if err := writeStream(filepath.Join(installingDir, "knowledge-pack.zip"), packageFile); err != nil {
		http.Error(w, "write package failed", http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(versionDir), 0o755); err != nil {
		http.Error(w, "create versions directory failed", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(installingDir, versionDir); err != nil {
		http.Error(w, "activate version directory failed", http.StatusInternalServerError)
		return
	}
	cleanup = false

	if err := h.writeLatestVersion(kbID, version); err != nil {
		http.Error(w, "write latest version failed", http.StatusInternalServerError)
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
    input, button { font: inherit; }
    input { padding: 10px; border: 1px solid #b9c2b2; border-radius: 6px; background: white; }
    button { border: 0; border-radius: 6px; background: #0f766e; color: white; padding: 10px 14px; cursor: pointer; }
    button.secondary { background: #334155; }
    pre { white-space: pre-wrap; word-break: break-word; background: #0f172a; color: #e2e8f0; padding: 12px; border-radius: 6px; min-height: 80px; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
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
    <h2>发布版本</h2>
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
const servicePrefix = location.pathname.includes("/admin") ? location.pathname.split("/admin")[0] : "";
tokenInput.value = localStorage.getItem("yiFlowKnowledgeAdminToken") || "";
document.querySelector("#saveToken").onclick = () => {
  localStorage.setItem("yiFlowKnowledgeAdminToken", tokenInput.value);
  output.textContent = "token saved locally";
};
function token() { return tokenInput.value || localStorage.getItem("yiFlowKnowledgeAdminToken") || ""; }
function kbID() { return kbIDInput.value.trim() || "yi-flow-core"; }
async function showResponse(response) {
  const text = await response.text();
  output.textContent = response.status + "\n" + text;
  return text;
}
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
  versions.textContent = await response.text();
  output.textContent = "versions refreshed: " + response.status;
};
document.querySelector("#rollback").onclick = async () => {
  const response = await fetch(servicePrefix + "/admin/api/kb/" + encodeURIComponent(kbID()) + "/latest", {
    method: "POST",
    headers: { Authorization: "Bearer " + token(), "Content-Type": "application/json" },
    body: JSON.stringify({ version: document.querySelector("#rollbackVersion").value.trim() })
  });
  await showResponse(response);
};
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
