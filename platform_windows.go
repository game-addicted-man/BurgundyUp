//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procIsIconic                   = user32.NewProc("IsIconic")
	procSetProcessDpiAwareness     = shcore.NewProc("SetProcessDpiAwareness")
)

const (
	createNoWindow = 0x08000000
	swShowNormal   = 1
	spiGetWorkArea = 0x0030
	mbOK           = 0x00000000
	mbIconError    = 0x00000010
	mbIconInfo     = 0x00000040
	mbSetForegrnd  = 0x00010000
	swRestore      = 9
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

func messageBox(title, msg string, icon uintptr) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|icon|mbSetForegrnd)
}

func showErrorBox(title, msg string) { messageBox(title, msg, mbIconError) }

var instanceMutex windows.Handle

// ensureSingleInstance 는 이미 실행 중이면 그 창을 앞으로 가져오고 false 를 돌려줍니다.
// 두 개가 동시에 돌면 서로의 임시 파일을 지우거나 창 설정이 꼬일 수 있습니다.
func ensureSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\AI-Upscaler-SingleInstance`)
	h, err := windows.CreateMutex(nil, false, name)
	if err == nil {
		instanceMutex = h
		return true
	}
	if h != 0 {
		windows.CloseHandle(h)
	}
	if err != windows.ERROR_ALREADY_EXISTS {
		return true // 확인할 수 없으면 그냥 실행합니다
	}
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(appTitle)
	// 아이콘을 두 번 눌렀다면 먼저 실행한 쪽이 아직 창을 만드는 중일 수 있어서 잠깐 기다립니다.
	for i := 0; i < 15; i++ {
		if w, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title))); w != 0 {
			if iconic, _, _ := procIsIconic.Call(w); iconic != 0 {
				procShowWindow.Call(w, swRestore) // 최소화돼 있을 때만 (최대화한 창은 그대로)
			}
			procSetForegroundWindow.Call(w)
			return false
		}
		// 브라우저 모드로 실행 중이면 그 화면을 다시 엽니다.
		if b, err := os.ReadFile(instanceURLPath()); err == nil {
			if u := strings.TrimSpace(string(b)); strings.HasPrefix(u, "http://127.0.0.1:") && shellOpen(u) == nil {
				return false
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	messageBox(appTitle, "AI 업스케일러가 이미 실행 중이에요.\n열려 있는 창이나 브라우저 탭을 확인해 주세요.", mbIconInfo)
	return false
}

var shellErrors = map[uintptr]string{
	0: "메모리가 부족해요", 2: "파일을 찾을 수 없어요", 3: "폴더를 찾을 수 없어요", 5: "접근이 거부됐어요",
	8: "메모리가 부족해요", 26: "공유 위반이에요", 27: "파일 연결이 올바르지 않아요",
	31: "이 파일을 열 프로그램이 없어요", 32: "필요한 구성 요소가 없어요",
}

// shellOpen 은 파일·폴더·주소를 기본 프로그램으로 엽니다.
// ShellExecute 는 COM 이 준비된 스레드에서 불러야 하므로 전용 스레드에서 실행합니다.
func shellOpen(target string) error {
	if target == "" {
		return errors.New("열 대상이 없어요")
	}
	owner := ownerWindow()
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded|0x4) // COINIT_DISABLE_OLE1DDE
		if int32(hr) >= 0 {
			defer procCoUninitialize.Call()
		}
		verb, _ := windows.UTF16PtrFromString("open")
		t, err := windows.UTF16PtrFromString(target)
		if err != nil {
			errc <- err
			return
		}
		r, _, _ := procShellExecuteW.Call(owner, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(t)), 0, 0, swShowNormal)
		if r <= 32 {
			if msg, ok := shellErrors[r]; ok {
				errc <- errors.New(msg)
			} else {
				errc <- fmt.Errorf("열 수 없어요 (코드 %d)", r)
			}
			return
		}
		errc <- nil
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(30 * time.Second):
		return nil
	}
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
	if d == nil {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			safe(done)
		}()
		<-done
		return
	}
	// Dispatch 는 스레드 메시지(WM_APP)를 한 번 보내는데, 그 순간 창을 끌고 있는 등
	// 다른 모달 루프가 돌고 있으면 메시지가 사라질 수 있습니다. 시작될 때까지 다시 깨웁니다.
	started := make(chan struct{})
	d(func() {
		close(started)
		safe(done)
	})
	for {
		select {
		case <-started:
			<-done
			return
		case <-time.After(300 * time.Millisecond):
			d(func() {})
		}
	}
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
	_ = os.Remove(marker) // 정상적으로 닫혔으면 다음에도 앱 창으로 엽니다

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
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	if h == 0 {
		return
	}
	// 다른 프로그램(또는 다른 실행)의 창은 건드리지 않습니다.
	if tid, _, _ := procGetWindowThreadProcessId.Call(h, 0); uint32(tid) != windows.GetCurrentThreadId() {
		return
	}
	procShowWindow.Call(h, 0)
	procDestroyWindow.Call(h)
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
	urlFile := filepath.Join(a.DataDir, "instance.url")
	_ = os.WriteFile(urlFile, []byte(a.url), 0o600)
	defer os.Remove(urlFile)
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
