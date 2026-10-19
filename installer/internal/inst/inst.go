// Package inst 는 설치 프로그램(setup)과 삭제 프로그램(uninstall)이 함께 쓰는 기능입니다.
package inst

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	AppID        = "AI-Upscaler"     // 폴더·레지스트리에 쓰는 이름
	DisplayName  = "AI 업스케일러"        // 화면에 보이는 이름
	AppExe       = "AI-Upscaler.exe" // 프로그램
	UninstallExe = "uninstall.exe"   // 삭제 프로그램
	MarkerName   = "install.json"    // 설치 정보 (삭제할 때 우리 폴더가 맞는지 확인)
	Publisher    = "AI-Upscaler"
	ShortcutDesc = "저해상도 이미지를 AI로 선명하게 키워요"
)

// Version 은 빌드할 때 -ldflags "-X aiupscaler/installer/internal/inst.Version=1.2.3" 으로 정해집니다.
var Version = "1.0.0"

// DefaultInstallDir 은 관리자 권한 없이 설치할 수 있는 사용자별 프로그램 폴더입니다.
// Windows: %LOCALAPPDATA%\Programs\AI-Upscaler
func DefaultInstallDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "Programs", AppID)
}

// AppDataDirs 는 프로그램이 쓰는 설정 폴더와 데이터(기록·임시) 폴더입니다. 프로그램(app.go)과 같은 규칙입니다.
func AppDataDirs() (config, data string) {
	if d, err := os.UserConfigDir(); err == nil {
		config = filepath.Join(d, AppID)
	}
	if d, err := os.UserCacheDir(); err == nil {
		data = filepath.Join(d, AppID)
	}
	return
}

// OverlapsDataDirs 는 dir 가 프로그램 설정·기록 폴더와 같거나, 그 안에 있거나, 그 폴더를 품고 있는지 확인합니다.
// 그런 곳에 설치하면 삭제할 때 설정·기록 폴더를 지우면서 다른 파일까지 지울 수 있어서 막습니다.
func OverlapsDataDirs(dir string) bool {
	config, data := AppDataDirs()
	return Overlaps(dir, config) || Overlaps(dir, data)
}

// Overlaps 는 두 경로가 같거나 한쪽이 다른 쪽 안에 있는지 확인합니다 (Windows 처럼 대소문자는 무시).
func Overlaps(a, b string) bool {
	a, b = cleanAbs(a), cleanAbs(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || within(a, b) || within(b, a)
}

func cleanAbs(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return strings.ToLower(filepath.Clean(p))
}

func within(child, parent string) bool {
	sep := string(filepath.Separator)
	if !strings.HasSuffix(parent, sep) {
		parent += sep
	}
	return strings.HasPrefix(child, parent)
}

type Marker struct {
	App         string   `json:"app"`
	Version     string   `json:"version"`
	InstalledAt string   `json:"installedAt"`
	Files       []string `json:"files"` // 설치한 파일 (설치 폴더 기준 상대 경로, / 구분)
}

// Extract 는 압축된 프로그램 파일을 dir 에 풉니다. 기존 파일은 새 파일로 바꿉니다.
// 중간에 실패해도 그때까지 쓴 파일 목록을 함께 돌려줍니다 (나중에 지울 수 있게).
func Extract(payload []byte, dir string) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("설치 파일이 손상되었어요: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("설치 폴더를 만들 수 없어요: %w", err)
	}
	var files []string
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		target := filepath.Join(abs, filepath.FromSlash(f.Name))
		rel, err := filepath.Rel(abs, target)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("설치 파일 안에 잘못된 경로가 있어요: %s", f.Name)
		}
		if err := writeEntry(f, target); err != nil {
			return files, fmt.Errorf("%s 파일을 쓸 수 없어요: %w", filepath.ToSlash(rel), err)
		}
		files = append(files, filepath.ToSlash(rel))
	}
	return files, nil
}

func writeEntry(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	// 같은 폴더에 겹치지 않는 임시 이름으로 다 쓴 뒤 바꿔치기합니다 (중간에 실패해도 기존 파일이 망가지지 않게).
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.new")
	if err != nil {
		return err
	}
	tmp := out.Name()
	_ = out.Chmod(0o644)
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := retry(func() error { return os.Rename(tmp, target) }); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// retry 는 백신·색인 프로그램이 방금 쓴 파일을 잠깐 잡고 있을 때를 대비해 몇 번 다시 시도합니다.
func retry(f func() error) error {
	var err error
	for i := 0; i < 4; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return err
}

func WriteMarker(dir string, files []string) error {
	m := Marker{App: AppID, Version: Version, InstalledAt: time.Now().Format(time.RFC3339), Files: files}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, MarkerName), b, 0o644)
}

func ReadMarker(dir string) (*Marker, error) {
	b, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.App != AppID {
		return nil, errors.New("다른 프로그램의 설치 정보예요")
	}
	return &m, nil
}

// DirSizeKB 는 폴더 크기를 KB 단위로 돌려줍니다 ('앱 및 기능'에 표시).
func DirSizeKB(dir string) uint32 {
	var total int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return uint32(total / 1024)
}

// RemoveInstalled 는 우리가 설치한 파일과 engine\models 의 모델(내려받았거나 직접 넣은 것)·내려받다 만 파일을
// 지우고, 지우지 못한 파일을 돌려줍니다. 설치 정보(install.json)와 keep(삭제 프로그램 자신)은 남깁니다.
// 사용자가 다른 파일이 있는 폴더에 설치했더라도 그 파일과 폴더는 건드리지 않습니다.
func RemoveInstalled(dir string, m *Marker, keep string) []string {
	rels := append([]string(nil), m.Files...)
	if abs, err := filepath.Abs(dir); err == nil && isRealDir(filepath.Join(abs, "engine")) && isRealDir(filepath.Join(abs, "engine", "models")) {
		if entries, err := os.ReadDir(filepath.Join(abs, "engine", "models")); err == nil {
			for _, e := range entries {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if !e.IsDir() && (ext == ".param" || ext == ".bin" || ext == ".part") {
					rels = append(rels, "engine/models/"+e.Name())
				}
			}
		}
	}
	return RemoveFiles(dir, Union(rels), keep)
}

// RemoveFiles 는 설치 폴더 기준 상대 경로로 적힌 파일을 지우고, 그 파일이 있던 폴더가 비면 그 폴더도 지웁니다.
// 설치 폴더 밖을 가리키는 경로와 keep 은 건드리지 않고, 지우지 못한 파일을 돌려줍니다.
func RemoveFiles(dir string, rels []string, keep string) (failed []string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return rels
	}
	dirs := map[string]bool{} // 폴더 → 진짜 폴더인지 (링크·정션이면 false)
	for _, rel := range rels {
		p, ok := insideDir(abs, rel)
		if !ok {
			continue
		}
		// 사용자가 중간 폴더를 다른 곳으로 연결(링크·정션)해 뒀다면 그 너머의 파일은 지우지 않습니다.
		allReal := true
		for d := filepath.Dir(p); len(d) > len(abs); d = filepath.Dir(d) {
			isReal, seen := dirs[d]
			if !seen {
				isReal = isRealDir(d)
				dirs[d] = isReal
			}
			allReal = allReal && isReal
		}
		if !allReal {
			continue
		}
		if keep != "" && strings.EqualFold(p, keep) {
			continue
		}
		if err := retry(func() error {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}); err != nil {
			failed = append(failed, rel)
		}
	}
	// 우리 파일이 있던 폴더 중 비어 있는 것만 깊은 곳부터 지웁니다 (다른 폴더는 건드리지 않음).
	list := make([]string, 0, len(dirs))
	for d, isReal := range dirs {
		if isReal {
			list = append(list, d)
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, d := range list {
		_ = os.Remove(d) // 비어 있지 않으면 실패하고 그대로 둡니다
	}
	return failed
}

// isRealDir 은 p 가 링크나 정션이 아닌 진짜 폴더인지 확인합니다 (없는 폴더도 지울 것이 없으니 true).
func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return true
	}
	return fi.IsDir() && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0
}

// insideDir 은 rel 이 설치 폴더 안의 경로일 때만 전체 경로를 돌려줍니다.
func insideDir(abs, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(filepath.FromSlash(rel)) || filepath.VolumeName(filepath.FromSlash(rel)) != "" {
		return "", false
	}
	p := filepath.Join(abs, filepath.FromSlash(rel))
	r, err := filepath.Rel(abs, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// StaleFiles 는 이전 설치에는 있었지만 새 버전에는 없는 파일입니다 (업데이트할 때 지움).
func StaleFiles(old, cur []string) []string {
	have := map[string]bool{}
	for _, f := range cur {
		have[strings.ToLower(f)] = true
	}
	var out []string
	for _, f := range old {
		if !have[strings.ToLower(f)] {
			out = append(out, f)
		}
	}
	return out
}

// Union 은 여러 파일 목록을 겹치지 않게 합칩니다 (대소문자 무시, 처음 나온 순서 유지).
func Union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, f := range l {
			if k := strings.ToLower(f); !seen[k] {
				seen[k] = true
				out = append(out, f)
			}
		}
	}
	return out
}
