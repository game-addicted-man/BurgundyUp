package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed ui
var uiFiles embed.FS

func init() {
	// Windows 에서는 MIME 형식을 레지스트리에서 읽는데, 잘못 등록된 PC 에서는 .css 가 엉뚱한 형식으로
	// 전달돼 화면 스타일이 통째로 빠질 수 있습니다. 화면 파일 형식은 직접 정해 둡니다.
	for ext, typ := range map[string]string{
		".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
		".js": "text/javascript; charset=utf-8", ".svg": "image/svg+xml",
		".woff2": "font/woff2", ".json": "application/json", ".png": "image/png",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// 화면에서 열 수 있는 외부 링크 (그 외 주소는 열지 않습니다)
var externalLinks = map[string]string{
	"upscayl":    "https://github.com/upscayl/upscayl",
	"realesrgan": "https://github.com/xinntao/Real-ESRGAN",
	"driver":     "https://www.vulkan.org/tools#vulkan-gpu-resources",
	"webview2":   "https://developer.microsoft.com/microsoft-edge/webview2/",
}

func (a *App) routes(port int) http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(uiFiles, "ui")
	mux.HandleFunc("/", a.handleIndex)
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))

	mux.HandleFunc("GET /api/state", a.apiState)
	mux.HandleFunc("GET /api/status", a.apiStatus)
	mux.HandleFunc("POST /api/settings", a.apiSettings)
	mux.HandleFunc("POST /api/pick", a.apiPick)
	mux.HandleFunc("POST /api/add", a.apiAdd)
	mux.HandleFunc("POST /api/upload", a.apiUpload)
	mux.HandleFunc("POST /api/start", a.apiStart)
	mux.HandleFunc("POST /api/cancel", a.apiCancel)
	mux.HandleFunc("POST /api/remove", a.apiRemove)
	mux.HandleFunc("GET /api/image", a.apiImage)
	mux.HandleFunc("GET /api/thumb", a.apiThumb)
	mux.HandleFunc("POST /api/open", a.apiOpen)
	mux.HandleFunc("POST /api/open-dir", a.apiOpenDir)
	mux.HandleFunc("POST /api/open-url", a.apiOpenURL)
	mux.HandleFunc("POST /api/models/download", a.apiModelDownload)
	mux.HandleFunc("POST /api/models/cancel", a.apiModelCancel)
	mux.HandleFunc("POST /api/models/rescan", a.apiModelRescan)
	mux.HandleFunc("POST /api/engine/probe", a.apiProbe)
	mux.HandleFunc("POST /api/ping", a.apiPing)
	mux.HandleFunc("POST /api/bye", a.apiBye)

	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 다른 웹사이트가 이 서버를 건드리지 못하게 막습니다.
		if !hosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-Token")
			if tok == "" {
				tok = r.URL.Query().Get("token")
			}
			if tok != a.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != origin && o != strings.Replace(origin, "127.0.0.1", "localhost", 1) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("t") != a.token {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>AI 업스케일러</title><body style="font-family:sans-serif;padding:40px">이 페이지는 AI 업스케일러 프로그램 안에서만 열 수 있어요. AI-Upscaler.exe 를 실행해 주세요.</body>`)
		return
	}
	b, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui missing", 500)
		return
	}
	html := strings.Replace(string(b), "__TOKEN__", a.token, 1)
	html = strings.ReplaceAll(html, "__VERSION__", appVersion)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, html)
}

// ───────────────────────── 도우미 ─────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

type queueView struct {
	Rev    int64  `json:"rev"`
	Active bool   `json:"active"`
	Items  []Item `json:"items"`
}

func (a *App) queueView() queueView {
	items, rev, active := a.queue.Snapshot()
	if items == nil {
		items = []Item{}
	}
	return queueView{Rev: rev, Active: active, Items: items}
}

// ───────────────────────── API ─────────────────────────

func (a *App) apiState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version":  appVersion,
		"settings": a.settings.Get(),
		"models":   a.models.List(),
		"engine":   a.Engine(),
		"queue":    a.queueView(),
		"download": a.dl.State(),
		"defaults": map[string]string{
			"outputDir": defaultOutputDir(),
			"baseDir":   a.BaseDir,
			"modelsDir": a.ModelsDir,
		},
		"platform": map[string]any{"os": runtime.GOOS, "canPick": canPickDialogs()},
	})
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"engine":   a.Engine(),
		"download": a.dl.State(),
		"models":   a.models.List(),
	}
	var since int64 = -1
	fmt.Sscan(r.URL.Query().Get("rev"), &since)
	if a.queue.Rev() != since {
		resp["queue"] = a.queueView()
	} else {
		_, rev, active := a.queue.Snapshot()
		resp["queue"] = map[string]any{"rev": rev, "active": active, "unchanged": true}
	}
	writeJSON(w, 200, resp)
}

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s, err := a.settings.Update(b)
	if err != nil {
		writeErr(w, 400, "설정 형식이 올바르지 않아요")
		return
	}
	writeJSON(w, 200, map[string]any{"settings": s})
}

func (a *App) apiPick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	if !canPickDialogs() {
		writeErr(w, 501, "이 환경에서는 파일 선택 창을 열 수 없어요. 이미지를 창에 끌어다 놓아 주세요.")
		return
	}
	if !a.dialogMu.TryLock() {
		writeErr(w, 409, "이미 선택 창이 열려 있어요.")
		return
	}
	defer a.dialogMu.Unlock()

	switch req.Kind {
	case "images":
		paths, err := pickImageFiles()
		if err != nil {
			writeErr(w, 500, "파일 선택 창을 열지 못했어요: "+err.Error())
			return
		}
		if len(paths) == 0 {
			writeJSON(w, 200, map[string]any{"canceled": true})
			return
		}
		added, skipped := a.queue.AddPaths(paths)
		writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped})
	case "folder":
		dir, err := pickFolder("업스케일할 이미지가 들어 있는 폴더를 고르세요")
		if err != nil {
			writeErr(w, 500, "폴더 선택 창을 열지 못했어요: "+err.Error())
			return
		}
		if dir == "" {
			writeJSON(w, 200, map[string]any{"canceled": true})
			return
		}
		added, skipped := a.queue.AddPaths([]string{dir})
		writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped, "folder": dir})
	case "output":
		dir, err := pickFolder("결과 이미지를 저장할 폴더를 고르세요")
		if err != nil {
			writeErr(w, 500, "폴더 선택 창을 열지 못했어요: "+err.Error())
			return
		}
		if dir == "" {
			writeJSON(w, 200, map[string]any{"canceled": true})
			return
		}
		s := a.settings.Mutate(func(s *Settings) {
			s.OutputDir = dir
			s.OutputMode = "custom"
		})
		writeJSON(w, 200, map[string]any{"settings": s})
	default:
		writeErr(w, 400, "알 수 없는 선택 종류")
	}
}

func (a *App) apiAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	added, skipped := a.queue.AddPaths(req.Paths)
	writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped})
}

func (a *App) apiUpload(w http.ResponseWriter, r *http.Request) {
	name := sanitizeFileName(filepath.Base(r.URL.Query().Get("name")))
	rel := safeRelDir(r.URL.Query().Get("rel"))
	if !isImageFile(name) {
		writeErr(w, 400, "지원하지 않는 파일 형식이에요: "+name)
		return
	}
	dir, err := os.MkdirTemp(a.UploadDir, "u")
	if err != nil {
		writeErr(w, 500, "임시 폴더를 만들 수 없어요")
		return
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		os.RemoveAll(dir)
		writeErr(w, 500, "임시 파일을 만들 수 없어요")
		return
	}
	const maxUpload = 2 << 30
	n, err := io.Copy(f, io.LimitReader(r.Body, maxUpload+1))
	cerr := f.Close()
	if err != nil || cerr != nil || n == 0 || n > maxUpload {
		os.RemoveAll(dir)
		writeErr(w, 400, "이미지를 받지 못했어요")
		return
	}
	it, err := a.queue.AddUpload(path, name, rel)
	if err != nil {
		os.RemoveAll(dir)
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"item": it})
}

func (a *App) apiStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs  []string `json:"ids"`
		Redo bool     `json:"redo"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	n, err := a.queue.Start(req.IDs, req.Redo)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"started": n})
}

func (a *App) apiCancel(w http.ResponseWriter, r *http.Request) {
	a.queue.Cancel()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		All  bool   `json:"all"`
		Done bool   `json:"done"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	switch {
	case req.All:
		a.queue.Clear(false)
	case req.Done:
		a.queue.Clear(true)
	default:
		if err := a.queue.Remove(req.ID); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

var imageMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".jfif": "image/jpeg",
	".webp": "image/webp", ".gif": "image/gif", ".bmp": "image/bmp", ".tif": "image/tiff", ".tiff": "image/tiff",
}

func (a *App) apiImage(w http.ResponseWriter, r *http.Request) {
	it, ok := a.queue.Get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := it.Src
	if r.URL.Query().Get("which") == "out" {
		path = it.Out
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if ct, ok := imageMIME[strings.ToLower(filepath.Ext(path))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (a *App) apiOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	it, ok := a.queue.Get(req.ID)
	if !ok {
		writeErr(w, 404, "목록에 없는 이미지예요")
		return
	}
	if (req.Target == "file" || req.Target == "folder") && it.Out == "" {
		writeErr(w, 400, "아직 결과 파일이 없어요")
		return
	}
	var err error
	switch req.Target {
	case "file":
		err = openPath(it.Out)
	case "folder":
		err = revealInFolder(it.Out)
	case "source":
		if it.Origin != "path" {
			writeErr(w, 400, "끌어다 놓은 이미지는 원본 위치를 알 수 없어요")
			return
		}
		err = revealInFolder(it.Src)
	default:
		writeErr(w, 400, "잘못된 요청")
		return
	}
	if err != nil {
		writeErr(w, 500, "열지 못했어요: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiOpenDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Which string `json:"which"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	var dir string
	switch req.Which {
	case "output":
		dir = a.settings.Get().OutputDir
		_ = os.MkdirAll(dir, 0o755)
	case "models":
		dir = a.ModelsDir
	case "logs":
		dir = filepath.Dir(a.LogPath)
	case "app":
		dir = a.BaseDir
	default:
		writeErr(w, 400, "잘못된 요청")
		return
	}
	if err := openPath(dir); err != nil {
		writeErr(w, 500, "폴더를 열지 못했어요: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiOpenURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	u, ok := externalLinks[req.Key]
	if !ok {
		writeErr(w, 400, "열 수 없는 주소예요")
		return
	}
	if err := openURL(u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiModelDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "잘못된 요청")
		return
	}
	if err := a.dl.Start(req.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"download": a.dl.State()})
}

func (a *App) apiModelCancel(w http.ResponseWriter, r *http.Request) {
	a.dl.Cancel()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiModelRescan(w http.ResponseWriter, r *http.Request) {
	a.models.Scan()
	writeJSON(w, 200, map[string]any{"models": a.models.List()})
}

func (a *App) apiProbe(w http.ResponseWriter, r *http.Request) {
	go a.ProbeEngine() // 이미 점검 중이면 ProbeEngine 이 알아서 건너뜁니다
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) apiPing(w http.ResponseWriter, r *http.Request) {
	a.markPing()
	writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().Unix()})
}
