// BurgundyUp 삭제 프로그램 (설치 폴더의 uninstall.exe).
// '설정 → 앱'에서 제거를 누르면 실행됩니다. 옵션: /S (조용히 삭제)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"burgundyup/installer/internal/inst"
)

const title = "BurgundyUp 삭제"

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
	if !inst.SingleInstance() {
		say("설치 또는 삭제 프로그램이 이미 실행 중이에요.", inst.MB_OK|inst.MB_ICONWARNING)
		os.Exit(3)
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
		say("이 폴더에서 BurgundyUp 설치 정보를 찾지 못했어요.\n"+dir, inst.MB_OK|inst.MB_ICONERROR)
		os.Exit(1)
	}

	if !silent && inst.MessageBox(title,
		"BurgundyUp을 삭제할까요?\n\n프로그램, 바로 가기, 설정, 모델(내려받았거나 직접 넣은 것)이 모두 지워져요.\n업스케일한 결과 이미지는 지워지지 않아요.",
		inst.MB_YESNO|inst.MB_ICONQUESTION|inst.MB_DEFBUTTON2) != inst.IDYES {
		os.Exit(2)
	}

	exe := filepath.Join(dir, inst.AppExe)
	engine := filepath.Join(dir, "engine", "upscayl-bin.exe")
	for inst.FileInUse(exe) || inst.FileInUse(engine) {
		if silent {
			fmt.Fprintln(os.Stderr, "BurgundyUp이 실행 중이라 삭제할 수 없어요.")
			os.Exit(3)
		}
		if inst.MessageBox(title, "BurgundyUp이 실행 중이에요.\n프로그램 창을 닫은 뒤 '다시 시도'를 눌러 주세요.",
			inst.MB_RETRYCANCEL|inst.MB_ICONWARNING) != inst.IDRETRY {
			os.Exit(2)
		}
	}

	_ = os.Chdir(os.TempDir()) // 설치 폴더를 지울 수 있게 작업 폴더를 옮깁니다.

	// 1) 설치한 파일부터 지웁니다. 하나라도 못 지우면 '설정 → 앱'에서 다시 삭제할 수 있도록
	//    남은 파일 목록, 제거 항목, 바로 가기, 삭제 프로그램을 그대로 둡니다.
	if failed := inst.RemoveInstalled(dir, m, self); len(failed) > 0 {
		_ = inst.WriteMarker(dir, inst.Union(failed, []string{filepath.Base(self)}))
		shown := failed
		if len(shown) > 8 {
			shown = append(append([]string(nil), shown[:8]...), fmt.Sprintf("… 외 %d개", len(failed)-8))
		}
		say("일부 파일을 지우지 못했어요.\nBurgundyUp이나 이 폴더를 쓰는 프로그램을 닫은 뒤, '설정 → 앱'에서 다시 삭제해 주세요.\n\n"+
			strings.Join(shown, "\n"), inst.MB_OK|inst.MB_ICONWARNING)
		os.Exit(4)
	}

	// 2) 모두 지웠으면 바로 가기, '설정 → 앱'의 제거 항목, 설정·기록 폴더를 지웁니다.
	startMenu, desktop := inst.ShortcutPaths()
	for _, lnk := range []string{startMenu, desktop} {
		if lnk != "" {
			_ = os.Remove(lnk)
		}
	}
	_ = inst.UnregisterUninstall()
	var dataDirs []string
	config, data := inst.AppDataDirs()
	for _, d := range []string{config, data} {
		if d == "" || inst.Overlaps(d, dir) {
			continue // 설치 폴더와 겹치는 폴더는 통째로 지우지 않습니다
		}
		_ = os.RemoveAll(d) // WebView2 가 잠깐 잡고 있으면 남을 수 있어서 끝난 뒤 한 번 더 지웁니다.
		dataDirs = append(dataDirs, d)
	}
	_ = os.Remove(filepath.Join(dir, inst.MarkerName))

	if !silent {
		inst.MessageBox(title, "BurgundyUp을 삭제했어요.", inst.MB_OK|inst.MB_ICONINFO)
	}
	_ = inst.ScheduleCleanup(self, dir, dataDirs...)
}
