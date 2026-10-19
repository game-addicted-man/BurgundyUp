// AI 업스케일러 삭제 프로그램 (설치 폴더의 uninstall.exe).
// '설정 → 앱'에서 제거를 누르면 실행됩니다. 옵션: /S (조용히 삭제)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aiupscaler/installer/internal/inst"
)

const title = "AI 업스케일러 삭제"

func main() {
	inst.Init()
	silent := false
	for _, a := range os.Args[1:] {
		if strings.EqualFold(a, "/S") || a == "--silent" || a == "-silent" {
			silent = true
		}
	}
	say := func(msg string, flags uint32) int {
		if silent {
			fmt.Fprintln(os.Stderr, msg)
			return inst.IDOK
		}
		return inst.MessageBox(title, msg, flags)
	}

	self, err := os.Executable()
	if err != nil {
		say("삭제 프로그램 위치를 알 수 없어요: "+err.Error(), inst.MB_OK|inst.MB_ICONERROR)
		os.Exit(1)
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	dir := filepath.Dir(self)
	m, err := inst.ReadMarker(dir)
	if err != nil {
		// 설치 정보가 없으면 엉뚱한 폴더일 수 있으니 아무것도 지우지 않습니다.
		say("이 폴더에서 AI 업스케일러 설치 정보를 찾지 못했어요.\n"+dir, inst.MB_OK|inst.MB_ICONERROR)
		os.Exit(1)
	}

	if !silent && inst.MessageBox(title,
		"AI 업스케일러를 삭제할까요?\n\n프로그램, 바로 가기, 설정, 내려받은 모델이 모두 지워져요.\n업스케일한 결과 이미지는 지워지지 않아요.",
		inst.MB_YESNO|inst.MB_ICONQUESTION|inst.MB_DEFBUTTON2) != inst.IDYES {
		os.Exit(2)
	}

	exe := filepath.Join(dir, inst.AppExe)
	engine := filepath.Join(dir, "engine", "upscayl-bin.exe")
	for inst.FileInUse(exe) || inst.FileInUse(engine) {
		if silent {
			fmt.Fprintln(os.Stderr, "AI 업스케일러가 실행 중이라 삭제할 수 없어요.")
			os.Exit(3)
		}
		if inst.MessageBox(title, "AI 업스케일러가 실행 중이에요.\n프로그램 창을 닫은 뒤 '다시 시도'를 눌러 주세요.",
			inst.MB_RETRYCANCEL|inst.MB_ICONWARNING) != inst.IDRETRY {
			os.Exit(2)
		}
	}

	_ = os.Chdir(os.TempDir()) // 설치 폴더를 지울 수 있게 작업 폴더를 옮깁니다.

	startMenu, desktop := inst.ShortcutPaths()
	for _, lnk := range []string{startMenu, desktop} {
		if lnk != "" {
			_ = os.Remove(lnk)
		}
	}
	_ = inst.UnregisterUninstall()

	config, data := inst.AppDataDirs()
	if config != "" {
		_ = os.RemoveAll(config)
	}
	if data != "" {
		_ = os.RemoveAll(data) // WebView2 가 잠깐 잡고 있으면 남을 수 있어서 끝난 뒤 한 번 더 지웁니다.
	}
	inst.RemoveInstalled(dir, m, self)

	if !silent {
		inst.MessageBox(title, "AI 업스케일러를 삭제했어요.", inst.MB_OK|inst.MB_ICONINFO)
	}
	_ = inst.ScheduleCleanup(self, dir, data)
}
