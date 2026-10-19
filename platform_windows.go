//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

func init() {
	// WebView2 창은 처음 만든 스레드에서만 다뤄야 하므로 main 을 메인 스레드에 고정합니다.
	runtime.LockOSThread()
}

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	shell32                        = windows.NewLazySystemDLL("shell32.dll")
	shcore                         = windows.NewLazySystemDLL("shcore.dll")
	procSetProcessDpiAwarenessCtx  = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem            = user32.NewProc("GetDpiForSystem")
	procSystemParametersInfoW      = user32.NewProc("SystemParametersInfoW")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procFindWindowW                = user32.NewProc("FindWindowW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procShellExecuteW              = shell32.NewProc("ShellExecuteW")
	procILCreateFromPathW          = shell32.NewProc("ILCreateFromPathW")
	procILFree                     = shell32.NewProc("ILFree")
	procSHOpenFolderAndSelectItems = shell32.NewProc("SHOpenFolderAndSelectItems")
	procSetProcessDpiAwareness     = shcore.NewProc("SetProcessDpiAwareness")
)

const (
	createNoWindow = 0x08000000
	swShowNormal   = 1
	spiGetWorkArea = 0x0030
	mbOK           = 0x00000000
	mbIconError    = 0x00000010
	mbSetForegrnd  = 0x00010000
)

// platformInit 은 고해상도(125%·150% 배율) 화면에서 흐려지지 않도록 DPI 인식을 켭니다.
func platformInit() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		if r, _, _ := procSetProcessDpiAwarenessCtx.Call(^uintptr(3)); r != 0 {
			return
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(2); r == 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

func picturesDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Pictures, 0); err == nil && p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Pictures")
	}
	return "."
}

// configureCmd 는 엔진을 실행할 때 검은 콘솔 창이 뜨지 않게 합니다.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func showErrorBox(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError|mbSetForegrnd)
}

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecuteW.Call(ownerWindow(), uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(t)), 0, 0, swShowNormal)
	if r <= 32 {
		return syscall.Errno(r)
	}
	return nil
}

func openPath(p string) error { return shellOpen(p) }

func openURL(u string) error { return shellOpen(u) }

// revealInFolder 는 탐색기를 열고 해당 파일을 선택해 보여 줍니다.
func revealInFolder(p string) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
		if int32(hr) >= 0 {
			defer procCoUninitialize.Call()
		}
		path, err := windows.UTF16PtrFromString(p)
		if err != nil {
			errc <- err
			return
		}
		pidl, _, _ := procILCreateFromPathW.Call(uintptr(unsafe.Pointer(path)))
		if pidl != 0 {
			r, _, _ := procSHOpenFolderAndSelectItems.Call(pidl, 0, 0, 0)
			procILFree.Call(pidl)
			if int32(r) >= 0 {
				errc <- nil
				return
			}
		}
		// 예비 방법: explorer.exe /select,"경로"
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
		errc <- cmd.Start()
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(15 * time.Second):
		return nil
	}
}

// ───────────────────────── UI 스레드 ─────────────────────────

var (
	uiMu         sync.Mutex
	uiDispatcher func(func())
	uiHWND       uintptr
)

func ownerWindow() uintptr {
	uiMu.Lock()
	h := uiHWND
	uiMu.Unlock()
	if h != 0 {
		return h
	}
	fg, _, _ := procGetForegroundWindow.Call()
	return fg
}

// onUIThread 는 선택 창 같은 작업을 앱 창의 스레드에서 실행합니다.
func onUIThread(f func()) {
	safe := func(done chan struct{}) {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logf("UI 작업 중 오류: %v", r)
			}
		}()
		f()
	}
	uiMu.Lock()
	d := uiDispatcher
	uiMu.Unlock()
	done := make(chan struct{})
	if d != nil {
		d(func() { safe(done) })
	} else {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			safe(done)
		}()
	}
	<-done
}

func canPickDialogs() bool { return true }

// ───────────────────────── 창 띄우기 ─────────────────────────

type winRect struct{ Left, Top, Right, Bottom int32 }

func scaledWindowSize() (w, h, minW, minH uint) {
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
			dpi = d
		}
	}
	scale := func(v int) int { return v * int(dpi) / 96 }
	ww, wh := scale(1280), scale(860)
	var r winRect
	if ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok != 0 {
		maxW := int(r.Right-r.Left) * 94 / 100
		maxH := int(r.Bottom-r.Top) * 94 / 100
		if maxW > 0 && ww > maxW {
			ww = maxW
		}
		if maxH > 0 && wh > maxH {
			wh = maxH
		}
	}
	mw, mh := scale(900), scale(600)
	if mw > ww {
		mw = ww
	}
	if mh > wh {
		mh = wh
	}
	return uint(ww), uint(wh), uint(mw), uint(mh)
}

// runUI 는 앱 창(WebView2)을 띄우고, 안 되면 Edge 앱 모드나 기본 브라우저로 엽니다.
func runUI(a *App, forceBrowser bool) {
	marker := filepath.Join(a.DataDir, "webview2.starting")
	useWebView := !forceBrowser && a.settings.Get().UIMode != "browser"
	if _, err := os.Stat(marker); err == nil {
		// 지난번에 앱 창을 띄우다 프로그램이 멈췄다면 이번에는 브라우저로 엽니다.
		logf("지난 실행에서 WebView2 시작이 끝나지 않았어요 → 브라우저 모드로 전환")
		_ = os.Remove(marker)
		a.settings.Mutate(func(s *Settings) { s.UIMode = "browser" })
		useWebView = false
	}
	if useWebView {
		ver, err := webviewloader.GetInstalledVersion()
		if err != nil || ver == "" {
			logf("WebView2 런타임 없음 (%v) → 브라우저 모드", err)
			useWebView = false
		} else {
			logf("WebView2 런타임 %s", ver)
		}
	}
	if useWebView && runWebView(a, marker) {
		return
	}
	runBrowser(a)
}

func runWebView(a *App, marker string) bool {
	_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)), 0o644)
	go func() {
		<-a.pinged // 화면이 정상적으로 떴으면 흔적을 지웁니다.
		_ = os.Remove(marker)
	}()
	w, h, mw, mh := scaledWindowSize()
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("AIUP_DEBUG") == "1",
		AutoFocus: true,
		DataPath:  filepath.Join(a.DataDir, "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title:  appTitle,
			Width:  w,
			Height: h,
			IconId: 2, // rsrc 로 넣은 아이콘 그룹 ID
			Center: true,
		},
	})
	if wv == nil {
		logf("WebView2 창을 만들지 못했어요 → 브라우저 모드")
		_ = os.Remove(marker)
		destroyStrayWindow()
		return false
	}
	uiMu.Lock()
	uiDispatcher = wv.Dispatch
	uiHWND = uintptr(wv.Window())
	uiMu.Unlock()

	wv.SetSize(int(mw), int(mh), webview2.HintMin)
	wv.Navigate(a.url)
	wv.Run()

	uiMu.Lock()
	uiDispatcher = nil
	uiHWND = 0
	uiMu.Unlock()
	return true
}

// destroyStrayWindow 는 WebView2 를 붙이지 못하고 남은 빈 창을 닫습니다.
func destroyStrayWindow() {
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(appTitle)
	if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title))); h != 0 {
		procShowWindow.Call(h, 0)
		procDestroyWindow.Call(h)
	}
}

func findBrowserExe() string {
	var cands []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func runBrowser(a *App) {
	var procDone chan struct{}
	if exe := findBrowserExe(); exe != "" {
		cmd := exec.Command(exe,
			"--app="+a.url,
			"--user-data-dir="+filepath.Join(a.DataDir, "BrowserProfile"),
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-sync",
			"--window-size=1280,860",
		)
		if err := cmd.Start(); err == nil {
			logf("앱 모드 브라우저로 열기: %s", exe)
			procDone = make(chan struct{})
			go func() {
				start := time.Now()
				_ = cmd.Wait()
				// 바로 끝났다면 이미 떠 있던 브라우저에 넘긴 것이므로 화면 신호로만 판단합니다.
				if time.Since(start) > 5*time.Second {
					close(procDone)
				}
			}()
		} else {
			logf("브라우저 실행 실패: %v", err)
			_ = openURL(a.url)
		}
	} else {
		_ = openURL(a.url)
	}
	a.waitForPageClose(procDone)
}
