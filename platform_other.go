//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Windows 가 아닌 환경(개발·테스트용). 실제 배포 대상은 Windows 입니다.

func platformInit() {}

func ensureSingleInstance() bool { return true }

func picturesDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Pictures")
	}
	return "."
}

func configureCmd(cmd *exec.Cmd) {}

func showErrorBox(title, msg string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openPath(p string) error {
	if os.Getenv("BURGUNDYUP_NO_OPEN") == "1" {
		logf("(테스트) 열기: %s", p)
		return nil
	}
	return exec.Command(opener(), p).Start()
}

func openURL(u string) error { return openPath(u) }

func revealInFolder(p string) error { return openPath(filepath.Dir(p)) }

// 테스트용: BURGUNDYUP_FAKE_PICK="a.png;b.png", BURGUNDYUP_FAKE_PICK_DIR="/폴더" 로 선택 창을 흉내 냅니다.
func canPickDialogs() bool {
	return os.Getenv("BURGUNDYUP_FAKE_PICK") != "" || os.Getenv("BURGUNDYUP_FAKE_PICK_DIR") != ""
}

func pickImageFiles() ([]string, error) {
	v := os.Getenv("BURGUNDYUP_FAKE_PICK")
	if v == "" {
		return nil, errors.New("선택 창을 지원하지 않는 환경이에요")
	}
	return strings.Split(v, ";"), nil
}

func pickFolder(title string) (string, error) {
	v := os.Getenv("BURGUNDYUP_FAKE_PICK_DIR")
	if v == "" {
		return "", errors.New("선택 창을 지원하지 않는 환경이에요")
	}
	return v, nil
}

func runUI(a *App, forceBrowser bool) {
	if err := openURL(a.url); err != nil {
		fmt.Println("브라우저에서 여세요:", a.url)
	}
	a.waitForPageClose(nil)
}
