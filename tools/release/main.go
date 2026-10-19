// release 는 배포 파일을 한 번에 만듭니다.
//
//	go run ./tools/release              (저장소 최상위 폴더에서, build.bat 이 이걸 실행)
//	go run ./tools/release -version 1.2.0
//
// 결과 (dist 폴더):
//
//	AI-Upscaler-Setup-<버전>.exe        설치 파일
//	AI-Upscaler-<버전>-portable.zip    설치 없이 쓰는 압축판
//	SHA256SUMS.txt
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"aiupscaler/tools/internal/fetch"
)

func main() {
	version := flag.String("version", "", "버전 (비우면 VERSION 파일 값)")
	outDir := flag.String("out", "dist", "결과를 넣을 폴더")
	noFetch := flag.Bool("no-fetch", false, "engine 폴더가 비어 있어도 내려받지 않기")
	flag.BoolVar(&keepPayload, "keep-payload", false, "(개발용) installer/setup/payload/payload.zip 을 지우지 않기")
	flag.Parse()
	if err := run(*version, *outDir, *noFetch); err != nil {
		fmt.Fprintln(os.Stderr, "\n빌드 실패:", err)
		os.Exit(1)
	}
}

var keepPayload bool

func logf(f string, a ...any) { fmt.Printf(f+"\n", a...) }

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-+]*$`)

func run(version, outDir string, noFetch bool) error {
	root, err := findRoot()
	if err != nil {
		return err
	}
	if version == "" {
		b, err := os.ReadFile(filepath.Join(root, "VERSION"))
		if err != nil {
			return fmt.Errorf("VERSION 파일을 읽을 수 없어요: %w", err)
		}
		version = strings.TrimSpace(string(b))
	}
	version = strings.TrimPrefix(version, "v")
	if !versionRe.MatchString(version) {
		return fmt.Errorf("버전 형식이 이상해요: %q", version)
	}
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(root, outDir)
	}
	logf("AI 업스케일러 %s 빌드를 시작해요", version)

	// 1) 엔진과 모델
	engineDir := filepath.Join(root, "engine")
	if !fetch.Complete(engineDir) {
		if noFetch {
			return errors.New("engine 폴더가 비어 있어요 (-no-fetch 를 빼고 다시 실행하세요)")
		}
		logf("\n[1/4] 엔진과 기본 모델을 받는 중 (처음 한 번만, 약 55MB)")
		if err := fetch.Ensure(engineDir, logf); err != nil {
			return err
		}
	} else {
		logf("\n[1/4] 엔진과 기본 모델: 준비됨")
	}

	// 2) 프로그램 파일 모으기
	logf("\n[2/4] 프로그램 빌드")
	stage := filepath.Join(outDir, "stage", "AI-Upscaler")
	if err := os.RemoveAll(filepath.Join(outDir, "stage")); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := goBuild(root, ".", filepath.Join(stage, "AI-Upscaler.exe"), "-X main.appVersion="+version); err != nil {
		return fmt.Errorf("프로그램 빌드 실패: %w", err)
	}
	if err := goBuild(root, "./installer/uninstall", filepath.Join(stage, "uninstall.exe"), "-X aiupscaler/installer/internal/inst.Version="+version); err != nil {
		return fmt.Errorf("삭제 프로그램 빌드 실패: %w", err)
	}
	for _, rel := range fetch.Files() {
		if err := copyFile(filepath.Join(engineDir, rel), filepath.Join(stage, "engine", rel)); err != nil {
			return err
		}
	}
	if err := copyDir(filepath.Join(root, "licenses"), filepath.Join(stage, "licenses")); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(root, "LICENSE"), filepath.Join(stage, "licenses", "AI-Upscaler-LICENSE.txt")); err != nil {
		return err
	}
	if err := writeWindowsText(filepath.Join(root, "사용법.txt"), filepath.Join(stage, "사용법.txt")); err != nil {
		return err
	}

	// 3) 압축판과 설치 파일 속 묶음
	logf("\n[3/4] 압축판 만들기")
	portable := filepath.Join(outDir, fmt.Sprintf("AI-Upscaler-%s-portable.zip", version))
	if err := zipDir(stage, portable, "AI-Upscaler/", map[string]bool{"uninstall.exe": true}); err != nil {
		return err
	}
	payload := filepath.Join(root, "installer", "setup", "payload", "payload.zip")
	if err := zipDir(stage, payload, "", nil); err != nil {
		return err
	}
	if !keepPayload {
		defer os.Remove(payload)
	}

	// 4) 설치 파일
	logf("\n[4/4] 설치 파일 만들기")
	setup := filepath.Join(outDir, fmt.Sprintf("AI-Upscaler-Setup-%s.exe", version))
	if err := goBuild(root, "./installer/setup", setup, "-X aiupscaler/installer/internal/inst.Version="+version); err != nil {
		return fmt.Errorf("설치 파일 빌드 실패: %w", err)
	}
	_ = os.RemoveAll(filepath.Join(outDir, "stage"))

	// 확인용 해시
	var sums []string
	for _, p := range []string{setup, portable} {
		h, size, err := sha256File(p)
		if err != nil {
			return err
		}
		sums = append(sums, fmt.Sprintf("%s  %s", h, filepath.Base(p)))
		logf("  %-40s %6.1f MB", filepath.Base(p), float64(size)/1e6)
	}
	if err := os.WriteFile(filepath.Join(outDir, "SHA256SUMS.txt"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	logf("\n완료! %s 폴더를 확인하세요.", outDir)
	return nil
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "module aiupscaler") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("저장소 최상위 폴더(go.mod 가 있는 곳)에서 실행해 주세요")
		}
		dir = parent
	}
}

func goBuild(root, pkg, out, ldExtra string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return errors.New("go 명령을 찾을 수 없어요. Go 를 설치해 주세요 (https://go.dev/dl/)")
	}
	cmd := exec.Command(gobin, "build", "-trimpath", "-ldflags", "-H windowsgui -s -w "+ldExtra, "-o", out, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logf("  go build %s → %s", pkg, filepath.Base(out))
	return cmd.Run()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}

// writeWindowsText 는 메모장에서 잘 보이도록 줄바꿈을 CRLF 로, 앞에 BOM 을 붙여 저장합니다.
func writeWindowsText(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	s := strings.TrimPrefix(string(b), "\ufeff")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	return os.WriteFile(dst, []byte("\ufeff"+s), 0o644)
}

func zipDir(src, dst, prefix string, skip map[string]bool) error {
	var files []string
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(src, p)
			if !skip[filepath.ToSlash(rel)] {
				files = append(files, rel)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, rel := range files {
		h := &zip.FileHeader{Name: prefix + filepath.ToSlash(rel), Method: zip.Deflate, Modified: stamp}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			out.Close()
			return err
		}
		in, err := os.Open(filepath.Join(src, rel))
		if err != nil {
			out.Close()
			return err
		}
		_, err = io.Copy(w, in)
		in.Close()
		if err != nil {
			out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sha256File(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
