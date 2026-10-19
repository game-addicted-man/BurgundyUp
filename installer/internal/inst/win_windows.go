//go:build windows

package inst

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procSetDpiCtx        = user32.NewProc("SetProcessDpiAwarenessContext")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

const (
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
	uninstallKey           = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + AppID
)

// Init 은 고해상도 화면에서 대화 상자가 흐려지지 않게 합니다 (매니페스트로도 설정됨).
func Init() {
	if procSetDpiCtx.Find() == nil {
		procSetDpiCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2
	}
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
		return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
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
	line := `cmd.exe /d /c ping -n 4 127.0.0.1 >nul & del /f /q "` + self + `" & rmdir "` + installDir + `"`
	for _, d := range dataDirs {
		if d != "" {
			line += ` & rmdir /s /q "` + d + `"`
		}
	}
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
		return start(createNoWindow)
	}
	return nil
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
func ShortcutPaths() (startMenu, desktop string) {
	if d := knownFolder(windows.FOLDERID_Programs); d != "" {
		startMenu = filepath.Join(d, DisplayName+".lnk")
	}
	if d := knownFolder(windows.FOLDERID_Desktop); d != "" {
		desktop = filepath.Join(d, DisplayName+".lnk")
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

func UnregisterUninstall() error {
	err := registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
