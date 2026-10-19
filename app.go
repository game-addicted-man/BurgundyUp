package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	appName  = "AI-Upscaler"
	appTitle = "AI 업스케일러"
)

// appVersion 은 빌드할 때 -ldflags "-X main.appVersion=1.2.3" 으로 바꿀 수 있습니다 (tools/release 가 VERSION 파일 값을 넣음).
var appVersion = "1.0.0"

// App 은 프로그램 전체 상태를 담습니다.
type App struct {
	BaseDir    string // 실행 파일이 있는 폴더
	EngineDir  string // BaseDir\engine
	EnginePath string // BaseDir\engine\upscayl-bin.exe
	ModelsDir  string // BaseDir\engine\models
	ConfigDir  string // 설정 (%APPDATA%\AI-Upscaler)
	DataDir    string // 기록·임시 파일 (%LOCALAPPDATA%\AI-Upscaler)
	UploadDir  string
	LogPath    string

	settings *SettingsStore
	models   *ModelRegistry
	queue    *Queue
	dl       *Downloader

	engMu sync.Mutex
	eng   EngineStatus

	token    string
	url      string
	server   *http.Server
	lastPing atomic.Int64
	pingOnce sync.Once
	pinged   chan struct{}
	dialogMu sync.Mutex
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "upscayl-bin.exe"
	}
	return "upscayl-bin"
}

// findBaseDir 는 engine 폴더가 있는 위치를 찾습니다.
// 보통은 exe 옆이고, 개발 중(go run)에는 현재 폴더나 그 상위 폴더입니다.
func findBaseDir() string {
	if d := os.Getenv("AIUP_BASEDIR"); d != "" {
		return d
	}
	var cands []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		cands = append(cands, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, wd, filepath.Dir(wd))
	}
	for _, c := range cands {
		if st, err := os.Stat(filepath.Join(c, "engine")); err == nil && st.IsDir() {
			return c
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return "."
}

func NewApp() (*App, error) {
	a := &App{pinged: make(chan struct{})}
	a.BaseDir = findBaseDir()
	a.EngineDir = filepath.Join(a.BaseDir, "engine")
	a.EnginePath = filepath.Join(a.EngineDir, exeName())
	if p := os.Getenv("AIUP_ENGINE"); p != "" { // 테스트용 가짜 엔진
		a.EnginePath = p
	}
	a.ModelsDir = filepath.Join(a.EngineDir, "models")

	if d := os.Getenv("AIUP_DATADIR"); d != "" {
		a.ConfigDir, a.DataDir = d, d
	} else {
		cfg, err := os.UserConfigDir()
		if err != nil {
			cfg = a.BaseDir
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = cfg
		}
		a.ConfigDir = filepath.Join(cfg, appName)
		a.DataDir = filepath.Join(cache, appName)
	}
	for _, d := range []string{a.ConfigDir, a.DataDir, filepath.Join(a.DataDir, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("데이터 폴더를 만들 수 없어요: %s\n%v", d, err)
		}
	}
	a.LogPath = filepath.Join(a.DataDir, "logs", "app.log")
	openLog(a.LogPath)
	logf("%s %s 시작 (%s/%s) 프로그램 폴더: %s", appTitle, appVersion, runtime.GOOS, runtime.GOARCH, a.BaseDir)

	if _, err := os.Stat(a.EnginePath); err != nil {
		msg := "엔진 파일을 찾을 수 없어요:\n" + a.EnginePath + "\n\n" +
			"AI-Upscaler 폴더 안의 engine 폴더를 지우거나 옮기지 말고, exe 파일과 같은 위치에 두세요."
		if looksLikeZipTemp(a.BaseDir) {
			msg = "압축 파일 안에서 바로 실행한 것 같아요.\n\n먼저 압축 파일을 마우스 오른쪽 버튼 → '압축 풀기'로 풀고,\n풀린 폴더 안의 AI-Upscaler.exe 를 실행해 주세요."
		}
		return nil, errors.New(msg)
	}

	// 지난번에 남은 업로드 임시 파일 정리
	a.UploadDir = filepath.Join(a.DataDir, "uploads")
	_ = os.RemoveAll(a.UploadDir)
	_ = os.RemoveAll(filepath.Join(a.DataDir, "tmp"))
	if err := os.MkdirAll(a.UploadDir, 0o755); err != nil {
		return nil, err
	}

	a.settings = loadSettings(filepath.Join(a.ConfigDir, "settings.json"), defaultOutputDir())
	a.models = NewModelRegistry(a.ModelsDir)
	a.dl = NewDownloader(a.ModelsDir, func() { a.models.Scan() })
	a.queue = NewQueue(a)
	a.eng = EngineStatus{State: "checking", Message: "엔진을 확인하는 중..."}
	return a, nil
}

func looksLikeZipTemp(dir string) bool {
	d := strings.ToLower(dir)
	tmp := strings.ToLower(os.TempDir())
	return tmp != "" && strings.HasPrefix(d, tmp)
}

func defaultOutputDir() string {
	return filepath.Join(picturesDir(), appName)
}

func (a *App) setEngine(s EngineStatus) {
	a.engMu.Lock()
	a.eng = s
	a.engMu.Unlock()
}

func (a *App) Engine() EngineStatus {
	a.engMu.Lock()
	defer a.engMu.Unlock()
	return a.eng
}

// StartServer 는 이 PC 안에서만 접속할 수 있는 화면용 서버를 엽니다.
func (a *App) StartServer(port int) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	a.token = hex.EncodeToString(b)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	addr := ln.Addr().(*net.TCPAddr)
	a.url = fmt.Sprintf("http://127.0.0.1:%d/?t=%s", addr.Port, a.token)
	a.server = &http.Server{
		Handler:           a.routes(addr.Port),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(logWriter{}, "http: ", 0),
	}
	go func() {
		if err := a.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("서버 오류: %v", err)
		}
	}()
	logf("화면 서버: 127.0.0.1:%d", addr.Port)
	return a.url, nil
}

func (a *App) markPing() {
	a.lastPing.Store(time.Now().UnixNano())
	a.pingOnce.Do(func() { close(a.pinged) })
}

// Shutdown 은 실행 중인 엔진을 멈추고 임시 파일을 정리합니다.
func (a *App) Shutdown() {
	logf("종료합니다")
	a.queue.Shutdown()
	a.dl.Cancel()
	time.Sleep(300 * time.Millisecond)
	if a.server != nil {
		_ = a.server.Close()
	}
	_ = os.RemoveAll(a.UploadDir)
	closeLog()
}

// ───────────────────────── 기록(log) ─────────────────────────

var (
	logMu   sync.Mutex
	logFile *os.File
	logEcho = false // 개발 모드에서는 콘솔에도 출력
)

func openLog(path string) {
	logMu.Lock()
	defer logMu.Unlock()
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(path, strings.TrimSuffix(path, ".log")+".old.log")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		logFile = f
	}
}

func closeLog() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

func logf(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		_, _ = logFile.WriteString(line)
	}
	if logEcho {
		_, _ = os.Stderr.WriteString(line)
	}
}

type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
