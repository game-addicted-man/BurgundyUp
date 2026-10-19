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
	"strings"
	"time"
)

const (
	AppID        = "AI-Upscaler"      // 폴더·레지스트리에 쓰는 이름
	DisplayName  = "AI 업스케일러"         // 화면에 보이는 이름
	AppExe       = "AI-Upscaler.exe"  // 프로그램
	UninstallExe = "uninstall.exe"    // 삭제 프로그램
	MarkerName   = "install.json"     // 설치 정보 (삭제할 때 우리 폴더가 맞는지 확인)
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

type Marker struct {
	App         string   `json:"app"`
	Version     string   `json:"version"`
	InstalledAt string   `json:"installedAt"`
	Files       []string `json:"files"` // 설치한 파일 (설치 폴더 기준 상대 경로, / 구분)
}

// Extract 는 압축된 프로그램 파일을 dir 에 풉니다. 기존 파일은 새 파일로 바꿉니다.
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
			return nil, fmt.Errorf("%s 파일을 쓸 수 없어요: %w", filepath.ToSlash(rel), err)
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
	tmp := target + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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

// RemoveInstalled 는 설치 폴더에서 우리가 설치한 파일과 내려받은 모델만 지웁니다.
// 사용자가 다른 파일이 있는 폴더에 설치했더라도 그 파일들은 건드리지 않습니다.
func RemoveInstalled(dir string, m *Marker, keep string) {
	abs, _ := filepath.Abs(dir)
	for _, rel := range m.Files {
		p := filepath.Join(abs, filepath.FromSlash(rel))
		if r, err := filepath.Rel(abs, p); err != nil || strings.HasPrefix(r, "..") {
			continue
		}
		if keep != "" && strings.EqualFold(p, keep) {
			continue
		}
		_ = os.Remove(p)
	}
	// 화면에서 내려받았거나 직접 넣은 모델, 내려받다 만 파일
	models := filepath.Join(abs, "engine", "models")
	if entries, err := os.ReadDir(models); err == nil {
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !e.IsDir() && (ext == ".param" || ext == ".bin" || ext == ".part") {
				_ = os.Remove(filepath.Join(models, e.Name()))
			}
		}
	}
	_ = os.Remove(filepath.Join(abs, MarkerName))
	// 비어 있는 폴더만 아래쪽부터 지웁니다.
	var dirs []string
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != abs {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 비어 있지 않으면 실패하고 그대로 둡니다
	}
}
