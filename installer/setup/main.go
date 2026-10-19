// BurgundyUp 설치 프로그램.
// 관리자 권한 없이 %LOCALAPPDATA%\Programs\BurgundyUp 에 설치하고,
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

	"burgundyup/installer/internal/inst"
)

// tools/release 가 payload/payload.zip 을 만든 뒤 이 프로그램을 빌드합니다.
//
//go:embed payload
var payload embed.FS

const title = "BurgundyUp 설치"

func main() {
	inst.Init()
	silent, dir := false, ""
	args := os.Args[1:]
parse:
	for i, a := range args {
		switch {
		case strings.EqualFold(a, "/S"), a == "--silent", a == "-silent":
			silent = true
		case len(a) > 3 && strings.EqualFold(a[:3], "/D="):
			// NSIS 처럼 /D= 는 맨 끝에 두고, 띄어쓰기가 있어도 따옴표 없이 쓸 수 있습니다.
			dir = strings.Join(append([]string{a[3:]}, args[i+1:]...), " ")
			break parse
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
	if !inst.SingleInstance() {
		fail("설치 또는 삭제 프로그램이 이미 실행 중이에요.", 3)
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
	if inst.OverlapsDataDirs(dir) {
		fail("이 폴더에는 설치할 수 없어요:\n"+dir+"\n\n프로그램의 설정·기록 폴더와 겹쳐요. 다른 폴더를 골라 주세요.", 1)
	}

	// 예전 이름(AI 업스케일러)으로 설치한 것이 있으면 새로 설치하면서 정리합니다.
	legacy := inst.FindLegacy()

	if !silent {
		msg := fmt.Sprintf("%s %s을(를) 설치할까요?\n\n설치 위치:\n%s\n\n시작 메뉴와 바탕 화면에 바로 가기를 만들어요.\n관리자 권한은 필요 없어요.",
			inst.DisplayName, inst.Version, dir)
		if legacy != nil {
			msg += "\n\n예전 이름(" + inst.LegacyDisplayName + ")으로 설치된 프로그램은 지우고 바꿔요.\n내려받은 모델은 옮겨 오고, 결과 이미지는 그대로 둬요."
		}
		if inst.MessageBox(title, msg, inst.MB_OKCANCEL|inst.MB_ICONQUESTION) != inst.IDOK {
			os.Exit(2)
		}
	}

	exe := filepath.Join(dir, inst.AppExe)
	busy := []string{exe, filepath.Join(dir, "engine", "upscayl-bin.exe")}
	if legacy != nil {
		busy = append(busy, filepath.Join(legacy.Dir, inst.LegacyAppExe), filepath.Join(legacy.Dir, "engine", "upscayl-bin.exe"))
	}
	for inUse(busy) {
		if silent {
			fail("BurgundyUp(또는 예전 "+inst.LegacyDisplayName+")이 실행 중이라 설치할 수 없어요.", 3)
		}
		if inst.MessageBox(title, "BurgundyUp(또는 예전 "+inst.LegacyDisplayName+")이 실행 중이에요.\n프로그램 창을 닫은 뒤 '다시 시도'를 눌러 주세요.",
			inst.MB_RETRYCANCEL|inst.MB_ICONWARNING) != inst.IDRETRY {
			os.Exit(2)
		}
	}

	inst.CancelCleanup()
	var oldFiles []string // 이전에 설치한 버전(예전 이름 포함)이 같은 폴더에 있으면 그 파일 목록
	if old, err := inst.ReadPreviousMarker(dir); err == nil {
		oldFiles = old.Files
	}
	files, err := inst.Extract(data, dir)
	if err != nil {
		// 일부만 풀렸어도 다시 설치하거나 삭제할 때 정리할 수 있게 목록을 남깁니다.
		if len(files) > 0 || oldFiles != nil {
			_ = inst.WriteMarker(dir, inst.Union(oldFiles, files))
		}
		fail("설치하지 못했어요.\n\n"+err.Error(), 1)
	}
	// 업데이트: 이전 버전에만 있던 파일은 지우고, 지우지 못한 것은 나중에 삭제할 때 지우도록 목록에 남깁니다.
	files = inst.Union(files, inst.RemoveFiles(dir, inst.StaleFiles(oldFiles, files), ""))
	if err := inst.WriteMarker(dir, files); err != nil {
		fail("설치 정보를 저장하지 못했어요.\n\n"+err.Error(), 1)
	}

	var warns []string
	if legacy != nil {
		if failed := inst.RemoveLegacy(legacy, dir); len(failed) > 0 {
			warns = append(warns, "예전 "+inst.LegacyDisplayName+"의 일부 파일을 지우지 못했어요. '설정 → 앱'에서 "+inst.LegacyDisplayName+"을(를) 제거해 주세요.")
		}
	}
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
	msg += "\n\n지금 BurgundyUp을 실행할까요?"
	if inst.MessageBox(title, msg, inst.MB_YESNO|inst.MB_ICONINFO) == inst.IDYES {
		if err := inst.StartDetached(exe, dir); err != nil {
			inst.MessageBox(title, "프로그램을 실행하지 못했어요:\n"+err.Error(), inst.MB_OK|inst.MB_ICONERROR)
		}
	}
}

func inUse(paths []string) bool {
	for _, p := range paths {
		if inst.FileInUse(p) {
			return true
		}
	}
	return false
}
