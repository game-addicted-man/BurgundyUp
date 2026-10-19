// AI 업스케일러 설치 프로그램.
// 관리자 권한 없이 %LOCALAPPDATA%\Programs\AI-Upscaler 에 설치하고,
// 시작 메뉴·바탕 화면 바로 가기와 '설정 → 앱'의 제거 항목을 만듭니다.
//
// 옵션: /S (조용히 설치), /D=<폴더> (설치 위치 지정)
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aiupscaler/installer/internal/inst"
)

// tools/release 가 payload/payload.zip 을 만든 뒤 이 프로그램을 빌드합니다.
//
//go:embed payload
var payload embed.FS

const title = "AI 업스케일러 설치"

func main() {
	inst.Init()
	silent, dir := false, ""
	for _, a := range os.Args[1:] {
		switch {
		case strings.EqualFold(a, "/S"), a == "--silent", a == "-silent":
			silent = true
		case len(a) > 3 && strings.EqualFold(a[:3], "/D="):
			dir = a[3:]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		}
	}
	fail := func(msg string, code int) {
		if !silent {
			inst.MessageBox(title, msg, inst.MB_OK|inst.MB_ICONERROR)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(code)
	}

	data, err := payload.ReadFile("payload/payload.zip")
	if err != nil || len(data) == 0 {
		fail("설치 파일 안에 프로그램이 들어 있지 않아요.\n소스 폴더에서 build.bat 으로 설치 파일을 다시 만들어 주세요.", 1)
	}
	if dir == "" {
		dir = inst.DefaultInstallDir()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	if !silent {
		msg := fmt.Sprintf("%s %s을(를) 설치할까요?\n\n설치 위치:\n%s\n\n시작 메뉴와 바탕 화면에 바로 가기를 만들어요.\n관리자 권한은 필요 없어요.",
			inst.DisplayName, inst.Version, dir)
		if inst.MessageBox(title, msg, inst.MB_OKCANCEL|inst.MB_ICONQUESTION) != inst.IDOK {
			os.Exit(2)
		}
	}

	exe := filepath.Join(dir, inst.AppExe)
	engine := filepath.Join(dir, "engine", "upscayl-bin.exe")
	for inst.FileInUse(exe) || inst.FileInUse(engine) {
		if silent {
			fail("AI 업스케일러가 실행 중이라 설치할 수 없어요.", 3)
		}
		if inst.MessageBox(title, "AI 업스케일러가 실행 중이에요.\n프로그램 창을 닫은 뒤 '다시 시도'를 눌러 주세요.",
			inst.MB_RETRYCANCEL|inst.MB_ICONWARNING) != inst.IDRETRY {
			os.Exit(2)
		}
	}

	files, err := inst.Extract(data, dir)
	if err != nil {
		fail("설치하지 못했어요.\n\n"+err.Error(), 1)
	}
	if err := inst.WriteMarker(dir, files); err != nil {
		fail("설치 정보를 저장하지 못했어요.\n\n"+err.Error(), 1)
	}

	var warns []string
	startMenu, desktop := inst.ShortcutPaths()
	for _, lnk := range []string{startMenu, desktop} {
		if lnk == "" {
			continue
		}
		if err := inst.CreateShortcut(lnk, exe, "", dir, exe, inst.ShortcutDesc); err != nil {
			warns = append(warns, "바로 가기 ("+filepath.Base(filepath.Dir(lnk))+"): "+err.Error())
		}
	}
	if err := inst.RegisterUninstall(dir, inst.DirSizeKB(dir)); err != nil {
		warns = append(warns, "제거 항목 등록: "+err.Error())
	}

	if silent {
		for _, w := range warns {
			fmt.Fprintln(os.Stderr, w)
		}
		return
	}
	msg := "설치가 끝났어요."
	if len(warns) > 0 {
		msg += "\n\n일부 작업은 하지 못했어요:\n" + strings.Join(warns, "\n")
	}
	msg += "\n\n지금 AI 업스케일러를 실행할까요?"
	if inst.MessageBox(title, msg, inst.MB_YESNO|inst.MB_ICONINFO) == inst.IDYES {
		if err := inst.StartDetached(exe, dir); err != nil {
			inst.MessageBox(title, "프로그램을 실행하지 못했어요:\n"+err.Error(), inst.MB_OK|inst.MB_ICONERROR)
		}
	}
}
