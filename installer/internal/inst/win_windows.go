//go:build windows

package inst

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procSetDllDirs       = kernel32.NewProc("SetDefaultDllDirectories")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procSetDpiCtx        = user32.NewProc("SetProcessDpiAwarenessContext")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

const (
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
	uninstallRoot          = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`
	uninstallKey           = uninstallRoot + AppID
	runOnceKey             = `Software\Microsoft\Windows\CurrentVersion\RunOnce`
	cleanupValue           = AppID + "-Cleanup"
)

// Init 은 설치·삭제 프로그램이 시작할 때 부릅니다.
func Init() {
	// 설치 파일은 보통 '다운로드' 폴더에서 실행되므로, 거기 놓인 엉뚱한 DLL 을 읽지 않도록
	// 시스템 폴더(System32)에서만 DLL 을 찾게 합니다.
	if procSetDllDirs.Find() == nil {
		procSetDllDirs.Call(windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	}
	// 고해상도 화면에서 대화 상자가 흐려지지 않게 합니다 (매니페스트로도 설정됨).
	if procSetDpiCtx.Find() == nil {
		procSetDpiCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2
	}
}

var setupMutex windows.Handle

// SingleInstance 는 설치·삭제 프로그램이 동시에 두 개 돌지 않게 합니다. 이미 실행 중이면 false 입니다.
func SingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\` + AppID + `-Setup`)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return false
	}
	setupMutex = h // 프로그램이 끝날 때까지 쥐고 있습니다
	return true
}

func MessageBox(title, text string, flags uint32) int {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(flags|MB_SETFOREGROUND))
	return int(r)
}

// FileInUse 는 실행 중인 프로그램처럼 다른 곳에서 쓰고 있어 덮어쓸 수 없는 파일인지 확인합니다.
func FileInUse(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		// 실행 중인 exe 는 공유 위반으로 열리지 않습니다. 읽기 전용 같은 '접근 거부'는 실행 중이 아니므로 제외합니다.
		return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
	}
	windows.CloseHandle(h)
	return false
}

// StartDetached 는 프로그램을 띄우고 기다리지 않습니다.
func StartDetached(exe, dir string) error {
	cmd := exec.Command(exe)
	cmd.Dir = dir
	return cmd.Start()
}

// ScheduleCleanup 은 삭제 프로그램이 끝난 뒤 자기 자신과 빈 설치 폴더, 데이터 폴더를 지우도록 예약합니다.
// installDir 은 비어 있을 때만 지우고(rmdir), dataDirs 는 프로그램 전용 폴더라 통째로 지웁니다.
func ScheduleCleanup(self, installDir string, dataDirs ...string) error {
	// cmd 는 따옴표 안에서도 %이름% 을 바꿔 버리므로, 그런 경로는 예약하지 않습니다 (삭제 프로그램만 남음).
	for _, p := range append([]string{self, installDir}, dataDirs...) {
		if strings.ContainsAny(p, `%"`) {
			return errors.New("경로에 % 가 있어서 정리를 예약하지 않았어요: " + p)
		}
	}
	cleanup := `del /f /q "` + self + `" & rmdir "` + installDir + `"`
	for _, d := range dataDirs {
		if d != "" {
			cleanup += ` & rmdir /s /q "` + d + `"`
		}
	}
	line := `cmd.exe /d /v:off /c ping -n 4 127.0.0.1 >nul & ` + cleanup
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if _, err := os.Stat(cmdExe); err != nil {
		cmdExe = "cmd.exe"
	}
	start := func(flags uint32) error {
		cmd := exec.Command(cmdExe)
		cmd.Dir = os.TempDir()
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true, CreationFlags: flags}
		return cmd.Start()
	}
	if err := start(createNoWindow | createBreakawayFromJob); err != nil {
		// '설정 → 앱'처럼 작업 개체(job) 안에서 실행돼 따로 떼어 실행할 수 없으면, 삭제 프로그램이 끝날 때
		// 정리 작업도 같이 끝날 수 있습니다. 그래서 다음 로그인 때 한 번 더 정리하도록 RunOnce 에도 적어 둡니다.
		if once := `cmd.exe /d /v:off /c ` + cleanup; len(once) < 260 {
			if k, _, err := registry.CreateKey(registry.CURRENT_USER, runOnceKey, registry.SET_VALUE); err == nil {
				_ = k.SetStringValue(cleanupValue, once)
				k.Close()
			}
		}
		return start(createNoWindow)
	}
	return nil
}

// CancelCleanup 은 지난번 삭제 때 다음 로그인으로 미뤄 둔 정리를 취소합니다.
// 다시 설치했는데 로그인할 때 새 삭제 프로그램과 설정 폴더가 지워지면 안 되니까요.
func CancelCleanup() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runOnceKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(cleanupValue)
		k.Close()
	}
}

// ───────────────────────── 바로 가기 (.lnk) ─────────────────────────

var (
	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// 가상 함수 표 순서 (ShObjIdl_core.h, ObjIdl.h)
const (
	vtQueryInterface      = 0
	vtRelease             = 2
	vtSetDescription      = 7  // IShellLinkW
	vtSetWorkingDirectory = 9  // IShellLinkW
	vtSetArguments        = 11 // IShellLinkW
	vtSetIconLocation     = 17 // IShellLinkW
	vtSetPath             = 20 // IShellLinkW
	vtSave                = 6  // IPersistFile
)

type comObject struct {
	vtbl *[32]uintptr
}

//go:uintptrescapes
func (o *comObject) call(idx int, args ...uintptr) uintptr {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(unsafe.Pointer(o)))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[idx], all...)
	return r
}

func (o *comObject) release() {
	if o != nil {
		o.call(vtRelease)
	}
}

func hrErr(what string, hr uintptr) error {
	return fmt.Errorf("%s 실패 (0x%08X)", what, uint32(hr))
}

// CreateShortcut 은 바로 가기 파일(.lnk)을 만듭니다.
func CreateShortcut(lnk, target, args, workDir, icon, desc string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED
	if int32(hr) >= 0 {
		defer procCoUninitialize.Call()
	}
	var sl *comObject
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, 0x1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&sl)))
	if int32(hr) < 0 || sl == nil {
		return hrErr("ShellLink 만들기", hr)
	}
	defer sl.release()

	pTarget, _ := windows.UTF16PtrFromString(target)
	pDir, _ := windows.UTF16PtrFromString(workDir)
	pArgs, _ := windows.UTF16PtrFromString(args)
	pIcon, _ := windows.UTF16PtrFromString(icon)
	pDesc, _ := windows.UTF16PtrFromString(desc)
	pLnk, err := windows.UTF16PtrFromString(lnk)
	if err != nil {
		return err
	}
	if hr := sl.call(vtSetPath, uintptr(unsafe.Pointer(pTarget))); int32(hr) < 0 {
		return hrErr("SetPath", hr)
	}
	sl.call(vtSetWorkingDirectory, uintptr(unsafe.Pointer(pDir)))
	if args != "" {
		sl.call(vtSetArguments, uintptr(unsafe.Pointer(pArgs)))
	}
	sl.call(vtSetIconLocation, uintptr(unsafe.Pointer(pIcon)), 0)
	sl.call(vtSetDescription, uintptr(unsafe.Pointer(pDesc)))

	var pf *comObject
	hr = sl.call(vtQueryInterface, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf)))
	if int32(hr) < 0 || pf == nil {
		return hrErr("IPersistFile", hr)
	}
	defer pf.release()
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return err
	}
	if hr := pf.call(vtSave, uintptr(unsafe.Pointer(pLnk)), 1); int32(hr) < 0 {
		return hrErr("바로 가기 저장", hr)
	}
	runtime.KeepAlive(pTarget)
	runtime.KeepAlive(pDir)
	runtime.KeepAlive(pArgs)
	runtime.KeepAlive(pIcon)
	runtime.KeepAlive(pDesc)
	runtime.KeepAlive(pLnk)
	return nil
}

func knownFolder(id *windows.KNOWNFOLDERID) string {
	p, err := windows.KnownFolderPath(id, 0)
	if err != nil {
		return ""
	}
	return p
}

// ShortcutPaths 는 시작 메뉴와 바탕 화면의 바로 가기 경로입니다.
func ShortcutPaths() (startMenu, desktop string) { return shortcutPathsFor(DisplayName) }

func shortcutPathsFor(name string) (startMenu, desktop string) {
	if d := knownFolder(windows.FOLDERID_Programs); d != "" {
		startMenu = filepath.Join(d, name+".lnk")
	}
	if d := knownFolder(windows.FOLDERID_Desktop); d != "" {
		desktop = filepath.Join(d, name+".lnk")
	}
	return
}

// ───────────────────────── 앱 및 기능(제거) 등록 ─────────────────────────

func RegisterUninstall(dir string, sizeKB uint32) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	exe := filepath.Join(dir, AppExe)
	un := filepath.Join(dir, UninstallExe)
	sets := []struct{ name, val string }{
		{"DisplayName", DisplayName},
		{"DisplayVersion", Version},
		{"Publisher", Publisher},
		{"InstallLocation", dir},
		{"DisplayIcon", exe + ",0"},
		{"UninstallString", `"` + un + `"`},
		{"QuietUninstallString", `"` + un + `" /S`},
		{"InstallDate", time.Now().Format("20060102")},
	}
	for _, s := range sets {
		if err := k.SetStringValue(s.name, s.val); err != nil {
			return err
		}
	}
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	_ = k.SetDWordValue("EstimatedSize", sizeKB)
	return nil
}

func UnregisterUninstall() error { return unregisterApp(AppID) }

func unregisterApp(id string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, uninstallRoot+id)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// legacyRegisteredDir 는 예전 이름으로 설치할 때 '설정 → 앱'에 등록한 설치 위치입니다.
func legacyRegisteredDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallRoot+LegacyAppID, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	d, _, err := k.GetStringValue("InstallLocation")
	if err != nil {
		return ""
	}
	return d
}
