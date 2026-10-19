//go:build !windows

package inst

// Windows 가 아닌 곳에서는 테스트용으로 흉내만 냅니다.
// AIUP_INST_ANSWER 로 대화 상자의 답(예: 1=확인, 6=예, 2=취소)을 정할 수 있습니다.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

func Init() {}

func MessageBox(title, text string, flags uint32) int {
	fmt.Printf("[%s] %s\n", title, text)
	if v, err := strconv.Atoi(os.Getenv("AIUP_INST_ANSWER")); err == nil {
		return v
	}
	switch flags & 0xF {
	case MB_YESNO:
		return IDNO
	case MB_RETRYCANCEL:
		return IDCANCEL
	}
	return IDOK
}

func FileInUse(path string) bool { return false }

func SingleInstance() bool { return os.Getenv("AIUP_INST_BUSY") == "" }

func CancelCleanup() {}

func StartDetached(exe, dir string) error {
	cmd := exec.Command(exe)
	cmd.Dir = dir
	return cmd.Start()
}

func ScheduleCleanup(self, installDir string, dataDirs ...string) error {
	_ = os.Remove(self)
	_ = os.Remove(installDir)
	for _, d := range dataDirs {
		if d != "" {
			_ = os.RemoveAll(d)
		}
	}
	return nil
}

func CreateShortcut(lnk, target, args, workDir, icon, desc string) error {
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return err
	}
	return os.WriteFile(lnk, []byte("shortcut -> "+target+"\n"), 0o644)
}

func ShortcutPaths() (startMenu, desktop string) {
	base := os.Getenv("AIUP_INST_SHORTCUTS")
	if base == "" {
		return "", ""
	}
	return filepath.Join(base, "StartMenu", DisplayName+".lnk"), filepath.Join(base, "Desktop", DisplayName+".lnk")
}

func RegisterUninstall(dir string, sizeKB uint32) error { return nil }

func UnregisterUninstall() error { return nil }
