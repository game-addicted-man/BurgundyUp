// Package fetch 는 업스케일 엔진(upscayl-bin)과 기본 AI 모델을 원래 배포처(GitHub)에서 받아
// engine 폴더를 채웁니다. 모든 파일은 SHA-256 으로 검증합니다.
// 엔진·모델은 크기가 커서 Git 저장소에 넣지 않고 빌드할 때 이렇게 받습니다.
package fetch

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type file struct {
	Dest   string // engine 폴더 기준 위치
	SHA256 string
}

type zipSource struct {
	Name   string
	URL    string
	SHA256 string
	Files  map[string]file // zip 안의 경로 → 받을 위치
}

// upscaylCommit 은 Upscayl 저장소에서 'Upscayl Lite' 모델과 vcomp140.dll 을 받을 고정 커밋입니다.
const upscaylCommit = "a00d55fee90e0f9435d5eaa86e76700df8199af8"

var zips = []zipSource{
	{
		Name:   "upscayl-bin (Upscayl 엔진)",
		URL:    "https://github.com/upscayl/upscayl-ncnn/releases/download/20251207-174704/upscayl-bin-20251207-174704-windows.zip",
		SHA256: "1f0f65c5d2ade866555e2ac467d35952c35a47080a4060fe56a1ab028f67258a",
		Files: map[string]file{
			"upscayl-bin-20251207-174704-windows/upscayl-bin.exe": {"upscayl-bin.exe", "294d31be8f29d047c0d91a8dcd5e739616ece56bce3188ac688f9a52d70abe60"},
		},
	},
	{
		Name:   "Real-ESRGAN 모델",
		URL:    "https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-windows.zip",
		SHA256: "abc02804e17982a3be33675e4d471e91ea374e65b70167abc09e31acb412802d",
		Files: map[string]file{
			"models/realesrgan-x4plus.param":       {"models/realesrgan-x4plus.param", "35330ececcea33b6c397a72548e788d5d53becee4734c50b7fada36e89f10a86"},
			"models/realesrgan-x4plus.bin":         {"models/realesrgan-x4plus.bin", "713ee713b0353afaa27976f0563a64a5043bd70b9bd8936c2e26e25ebcdbcddf"},
			"models/realesrgan-x4plus-anime.param": {"models/realesrgan-x4plus-anime.param", "2b8fb6e0ae4d2d85704ca08c119a2f5ea40add4f2ecd512eb7f4cd44b6127ed4"},
			"models/realesrgan-x4plus-anime.bin":   {"models/realesrgan-x4plus-anime.bin", "fe01c269cfd10cdef8e018ab66ebe750cf79c7af4d1f9c16c737e1295229bacc"},
			"models/realesr-animevideov3-x2.param": {"models/realesr-animevideov3-x2.param", "b88ff4f00ebf019a7fdac17fdd45a7fd3665d37509efc5baf2e4da2e24420a04"},
			"models/realesr-animevideov3-x2.bin":   {"models/realesr-animevideov3-x2.bin", "548a36f9c3f4ab8da56cd3b13badf23968bee207b396dad14d04b830e5f2ab2d"},
			"models/realesr-animevideov3-x3.param": {"models/realesr-animevideov3-x3.param", "d1a5755008791d09b57e3425fc9dd0bd26b00fdf79c606210bc0e693f8230881"},
			"models/realesr-animevideov3-x3.bin":   {"models/realesr-animevideov3-x3.bin", "548a36f9c3f4ab8da56cd3b13badf23968bee207b396dad14d04b830e5f2ab2d"},
			"models/realesr-animevideov3-x4.param": {"models/realesr-animevideov3-x4.param", "850a248e7c14c27e5bd8cf7265113a9441036a7db63963bb8aa5169d788a435e"},
			"models/realesr-animevideov3-x4.bin":   {"models/realesr-animevideov3-x4.bin", "548a36f9c3f4ab8da56cd3b13badf23968bee207b396dad14d04b830e5f2ab2d"},
		},
	},
}

// Upscayl 저장소에서 바로 받는 파일
var raws = []struct {
	Path string
	file
}{
	{"resources/models/upscayl-lite-4x.param", file{"models/upscayl-lite-4x.param", "22174924330297357434ad21ed0af7f4b820008d2a502b492754d130d4142714"}},
	{"resources/models/upscayl-lite-4x.bin", file{"models/upscayl-lite-4x.bin", "85ee266b632a765a725425ba6a5620c088c8aa2939a03063b2d83b3462724cc1"}},
	{"resources/win/bin/vcomp140.dll", file{"vcomp140.dll", vcompUpscayl}},
}

// vcomp140.dll(OpenMP 런타임)은 Upscayl 판을 쓰고, 받지 못하면 Real-ESRGAN 압축 파일에 든 판을 씁니다(같은 기능).
const (
	vcompUpscayl   = "d49b394de1154176b39611c37c669ebff50aa5a818dbd5ff3d2214a299368ddd"
	vcompRealesrgn = "8f72ef2e483465444b2059fc6744d6cb22cd8d8a27f6fa56befd2a42dcd0f78b"
)

type Logger func(format string, args ...any)

// Complete 는 engine 폴더에 필요한 파일이 모두 올바르게 들어 있는지 확인합니다.
func Complete(engineDir string) bool {
	for _, z := range zips {
		for _, f := range z.Files {
			if !hasHash(filepath.Join(engineDir, f.Dest), f.SHA256) {
				return false
			}
		}
	}
	for _, r := range raws {
		p := filepath.Join(engineDir, r.Dest)
		if r.Dest == "vcomp140.dll" {
			if !hasHash(p, vcompUpscayl) && !hasHash(p, vcompRealesrgn) {
				return false
			}
			continue
		}
		if !hasHash(p, r.SHA256) {
			return false
		}
	}
	return true
}

// Ensure 는 빠진 파일을 받아 engine 폴더를 채웁니다.
func Ensure(engineDir string, log Logger) error {
	if Complete(engineDir) {
		log("엔진과 모델이 이미 준비돼 있어요: %s", engineDir)
		return nil
	}
	cache := filepath.Join(os.TempDir(), "burgundyup-fetch-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(engineDir, "models"), 0o755); err != nil {
		return err
	}
	var realesrganZip string
	for _, z := range zips {
		local := filepath.Join(cache, filepath.Base(z.URL))
		if !hasHash(local, z.SHA256) {
			log("받는 중: %s", z.Name)
			if err := download([]string{z.URL}, local, z.SHA256, log); err != nil {
				return fmt.Errorf("%s 받기 실패: %w", z.Name, err)
			}
		}
		if err := extract(local, engineDir, z.Files); err != nil {
			return fmt.Errorf("%s 풀기 실패: %w", z.Name, err)
		}
		if strings.Contains(z.URL, "realesrgan") {
			realesrganZip = local
		}
	}
	for _, r := range raws {
		dest := filepath.Join(engineDir, r.Dest)
		if hasHash(dest, r.SHA256) || (r.Dest == "vcomp140.dll" && hasHash(dest, vcompRealesrgn)) {
			continue
		}
		var urls []string
		if m := strings.TrimSpace(os.Getenv("BURGUNDYUP_RAW_MIRROR")); m != "" { // 테스트·사내 미러용
			urls = append(urls, strings.TrimRight(m, "/")+"/"+r.Path)
		}
		urls = append(urls,
			"https://raw.githubusercontent.com/upscayl/upscayl/"+upscaylCommit+"/"+r.Path,
			"https://github.com/upscayl/upscayl/raw/"+upscaylCommit+"/"+r.Path,
		)
		log("받는 중: %s", filepath.Base(r.Path))
		err := download(urls, dest, r.SHA256, log)
		if err != nil && r.Dest == "vcomp140.dll" && realesrganZip != "" {
			log("Upscayl 판 vcomp140.dll 을 받지 못해 Real-ESRGAN 판을 씁니다")
			err = extract(realesrganZip, engineDir, map[string]file{"vcomp140.dll": {"vcomp140.dll", vcompRealesrgn}})
		}
		if err != nil {
			return fmt.Errorf("%s 받기 실패: %w", r.Path, err)
		}
	}
	if !Complete(engineDir) {
		return errors.New("받은 파일을 확인하지 못했어요")
	}
	log("엔진과 모델 준비 완료: %s", engineDir)
	return nil
}

func download(urls []string, dest, want string, log Logger) error {
	var last error
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}}
	for _, u := range urls {
		last = func() error {
			resp, err := client.Get(u)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("HTTP %d (%s)", resp.StatusCode, u)
			}
			tmp := dest + ".part"
			out, err := os.Create(tmp)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(io.MultiWriter(out, h), resp.Body)
			out.Close()
			if err != nil {
				os.Remove(tmp)
				return err
			}
			if got := hex.EncodeToString(h.Sum(nil)); got != want {
				os.Remove(tmp)
				return fmt.Errorf("검증 실패 (%s, %d바이트)", u, n)
			}
			return os.Rename(tmp, dest)
		}()
		if last == nil {
			return nil
		}
		log("  실패: %v", last)
	}
	return last
}

func extract(zipPath, engineDir string, files map[string]file) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := map[string]bool{}
	for _, f := range zr.File {
		want, ok := files[f.Name]
		if !ok {
			continue
		}
		dest := filepath.Join(engineDir, filepath.FromSlash(want.Dest))
		if hasHash(dest, want.SHA256) {
			found[f.Name] = true
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		tmp := dest + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, h), rc)
		rc.Close()
		out.Close()
		if err != nil {
			os.Remove(tmp)
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			os.Remove(tmp)
			return fmt.Errorf("%s 검증 실패", f.Name)
		}
		if err := os.Rename(tmp, dest); err != nil {
			return err
		}
		found[f.Name] = true
	}
	for name := range files {
		if !found[name] {
			return fmt.Errorf("압축 파일 안에 %s 이(가) 없어요", name)
		}
	}
	return nil
}

func hasHash(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}

// Files 는 engine 폴더 기준으로 배포에 넣을 파일 목록입니다 (직접 받아 둔 추가 모델은 빼고).
func Files() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.FromSlash(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, z := range zips {
		for _, f := range z.Files {
			add(f.Dest)
		}
	}
	for _, r := range raws {
		add(r.Dest)
	}
	sort.Strings(out)
	return out
}
