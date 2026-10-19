package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// EngineJob 은 upscayl-bin 한 번 실행에 필요한 값입니다.
type EngineJob struct {
	Input       string
	Output      string
	ModelFile   string // models 폴더 안의 파일 이름(확장자 제외)
	ModelScale  int    // 모델 자체 배율 (-z)
	TargetScale int    // 원하는 최종 배율 (-s)
	Format      string // png | jpg | webp
	Compression int    // 0..100 (WebP 에만 의미 있음: 품질 = 100 - 압축)
	GPU         string
	Tile        int
	TTA         bool
}

// engineArgs 는 Upscayl 앱과 같은 방식으로 명령줄 인수를 만듭니다.
// 모델 폴더는 상대 경로 "models" 로 넘깁니다 (작업 폴더 = engine 폴더).
// Windows 판 upscayl-bin 은 모델 이름에서 배율을 추측하지 않으므로 -z 를 항상 넘깁니다.
func engineArgs(j EngineJob) []string {
	args := []string{
		"-i", j.Input,
		"-o", j.Output,
		"-m", "models",
		"-n", j.ModelFile,
		"-z", strconv.Itoa(j.ModelScale),
		"-f", j.Format,
		"-c", strconv.Itoa(j.Compression),
	}
	if j.TargetScale != j.ModelScale {
		args = append(args, "-s", strconv.Itoa(j.TargetScale))
	}
	if j.GPU != "" {
		args = append(args, "-g", j.GPU)
	}
	if j.Tile > 0 {
		args = append(args, "-t", strconv.Itoa(j.Tile))
	}
	if j.TTA {
		args = append(args, "-x")
	}
	return args
}

var (
	progressRe = regexp.MustCompile(`^\s*(\d{1,3}(?:\.\d+)?)\s*%`)
	gpuLineRe  = regexp.MustCompile(`^\s*\[(\d+)\s+(.+?)\]\s+queueC=`)
)

type GPUInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type EngineResult struct {
	Lines []string // 마지막 출력 줄들 (오류 설명용)
	GPUs  []GPUInfo
}

// runEngine 은 엔진을 실행하고 진행률을 onProgress 로 알려 줍니다.
func (a *App) runEngine(ctx context.Context, j EngineJob, onProgress func(float64)) (EngineResult, error) {
	var res EngineResult
	cmd := exec.CommandContext(ctx, a.EnginePath, engineArgs(j)...)
	cmd.Dir = a.EngineDir
	cmd.WaitDelay = 5 * time.Second
	configureCmd(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	logf("엔진 실행: %s %s", filepath.Base(a.EnginePath), strings.Join(quoteArgs(engineArgs(j)), " "))

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return res, err
	}

	var mu sync.Mutex
	var ring []string
	seenGPU := map[int]bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		sc.Split(splitLinesCR)
		for sc.Scan() {
			line := cleanLine(sc.Bytes())
			if line == "" {
				continue
			}
			if m := progressRe.FindStringSubmatch(line); m != nil {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil && onProgress != nil {
					onProgress(v)
				}
				continue
			}
			mu.Lock()
			if m := gpuLineRe.FindStringSubmatch(line); m != nil {
				id, _ := strconv.Atoi(m[1])
				if !seenGPU[id] {
					seenGPU[id] = true
					res.GPUs = append(res.GPUs, GPUInfo{ID: id, Name: strings.TrimSpace(m[2])})
				}
			}
			ring = append(ring, line)
			if len(ring) > 60 {
				ring = ring[len(ring)-60:]
			}
			mu.Unlock()
		}
		io.Copy(io.Discard, pr)
	}()

	err := cmd.Wait()
	pw.Close()
	<-done
	mu.Lock()
	res.Lines = append([]string(nil), ring...)
	mu.Unlock()
	return res, err
}

// splitLinesCR 은 \n 과 \r 모두를 줄바꿈으로 봅니다.
func splitLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// cleanLine 은 엔진 출력(콘솔 코드페이지일 수 있음)을 안전한 UTF-8 문자열로 바꿉니다.
func cleanLine(b []byte) string {
	s := string(b)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	return strings.TrimSpace(s)
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}

const (
	statusDLLNotFound    = 0xC0000135
	statusEntryNotFound  = 0xC0000139
	statusAccessViolate  = 0xC0000005
	statusIllegalInstr   = 0xC000001D
	statusStackOverflow  = 0xC00000FD
	statusHeapCorruption = 0xC0000374
)

// friendlyError 는 엔진 출력과 종료 코드를 보고 이해하기 쉬운 한국어 오류로 바꿉니다.
func friendlyError(lines []string, runErr error) (string, string) {
	all := strings.ToLower(strings.Join(lines, "\n"))
	detail := lastLines(lines, 12)
	var code uint32
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = uint32(exitErr.ExitCode())
		if detail != "" {
			detail += "\n"
		}
		detail += fmt.Sprintf("종료 코드: %d (0x%08X)", exitErr.ExitCode(), code)
	} else if runErr != nil {
		if detail != "" {
			detail += "\n"
		}
		detail += runErr.Error()
	}
	hasGPU := false
	for _, l := range lines {
		if gpuLineRe.MatchString(l) {
			hasGPU = true
			break
		}
	}
	noVulkan := strings.Contains(all, "vkcreateinstance failed") || strings.Contains(all, "no vulkan") ||
		strings.Contains(all, "vulkan device not found") || strings.Contains(all, "failed to create gpu instance")
	switch {
	case code == statusDLLNotFound || code == statusEntryNotFound:
		return "엔진 실행에 필요한 Vulkan 구성 요소(vulkan-1.dll)를 찾지 못했어요. 그래픽 드라이버(NVIDIA·AMD·Intel)를 최신 버전으로 설치해 주세요.", detail
	case noVulkan, strings.Contains(all, "invalid gpu device") && !hasGPU:
		// GPU 목록이 하나도 안 나왔다면 사용자가 고른 GPU 문제가 아니라 Vulkan 자체가 없는 것입니다.
		return "Vulkan을 지원하는 그래픽카드(또는 드라이버)를 찾지 못했어요. 그래픽 드라이버를 최신 버전으로 업데이트해 주세요.", detail
	case strings.Contains(all, "invalid gpu device"):
		return "선택한 GPU를 사용할 수 없어요. 설정에서 GPU를 '자동'으로 바꾸거나 그래픽 드라이버를 업데이트해 주세요.", detail
	case strings.Contains(all, "out_of_device_memory"), strings.Contains(all, "out of memory"),
		strings.Contains(all, "vkallocatememory failed"), strings.Contains(all, "failed to allocate"):
		return "그래픽 메모리가 부족해요. 설정에서 '타일 크기'를 더 작게(예: 128 또는 64) 바꾼 뒤 다시 해 보세요.", detail
	case strings.Contains(all, "couldn't read the image"):
		return "이미지를 읽지 못했어요. 파일이 손상되었거나 지원하지 않는 형식일 수 있어요.", detail
	case strings.Contains(all, "couldn't write the image"):
		return "결과 파일을 저장하지 못했어요. 저장 폴더 권한과 남은 디스크 용량을 확인해 주세요.", detail
	case strings.Contains(all, "unknown model dir"), strings.Contains(all, ".param failed"), strings.Contains(all, "fopen"):
		return "모델 파일을 불러오지 못했어요. 프로그램 폴더의 engine\\models 폴더를 확인해 주세요.", detail
	case code == statusAccessViolate || code == statusIllegalInstr || code == statusStackOverflow || code == statusHeapCorruption:
		return "엔진이 비정상 종료됐어요. 그래픽 드라이버를 업데이트하거나, 설정에서 타일 크기를 줄여 보세요.", detail
	}
	if last := lastMeaningful(lines); last != "" {
		return "업스케일에 실패했어요: " + last, detail
	}
	return "업스케일에 실패했어요. (자세한 내용은 기록 파일을 확인해 주세요)", detail
}

func lastLines(lines []string, n int) string {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func lastMeaningful(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if gpuLineRe.MatchString(l) || strings.HasPrefix(l, "[") {
			continue
		}
		if strings.Contains(strings.ToLower(l), "error") {
			return strings.TrimSpace(strings.TrimLeft(l, "🚨 "))
		}
	}
	return ""
}

// ───────────────────────── 엔진 점검 ─────────────────────────

type EngineStatus struct {
	State   string    `json:"state"` // checking | ok | error | missing
	Message string    `json:"message"`
	Detail  string    `json:"detail"`
	GPUs    []GPUInfo `json:"gpus"`
}

// ProbeEngine 은 작은 이미지를 한 번 업스케일해서 엔진과 GPU 가 동작하는지 확인합니다.
func (a *App) ProbeEngine() {
	a.setEngine(EngineStatus{State: "checking", Message: "엔진과 그래픽카드를 확인하는 중..."})
	if _, err := os.Stat(a.EnginePath); err != nil {
		a.setEngine(EngineStatus{State: "missing",
			Message: "엔진 파일이 없어요: " + a.EnginePath + "\n프로그램 폴더 안의 engine 폴더를 지우거나 옮기지 마세요."})
		return
	}
	var modelID string
	for _, m := range a.models.List() {
		if m.Installed {
			if modelID == "" || m.ID == "upscayl-lite-4x" {
				modelID = m.ID
			}
		}
	}
	if modelID == "" {
		a.setEngine(EngineStatus{State: "error", Message: "사용할 수 있는 모델이 없어요. engine\\models 폴더를 확인해 주세요."})
		return
	}
	file, mscale, err := a.models.Resolve(modelID, 4)
	if err != nil {
		a.setEngine(EngineStatus{State: "error", Message: err.Error()})
		return
	}
	dir := filepath.Join(a.DataDir, "tmp")
	_ = os.MkdirAll(dir, 0o755)
	in := filepath.Join(dir, "probe.png")
	out := filepath.Join(dir, "probe_out.png")
	_ = os.Remove(out)
	if err := writeProbeImage(in); err != nil {
		a.setEngine(EngineStatus{State: "error", Message: "임시 파일을 만들 수 없어요: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, runErr := a.runEngine(ctx, EngineJob{
		Input: in, Output: out, ModelFile: file, ModelScale: mscale, TargetScale: mscale, Format: "png",
	}, nil)
	_ = os.Remove(in)
	st, statErr := os.Stat(out)
	_ = os.Remove(out)
	if ctx.Err() == context.DeadlineExceeded {
		a.setEngine(EngineStatus{State: "error", GPUs: res.GPUs,
			Message: "엔진 확인이 너무 오래 걸려요. 그래픽 드라이버를 업데이트해 보세요.", Detail: lastLines(res.Lines, 12)})
		return
	}
	if runErr != nil || statErr != nil || st.Size() == 0 {
		msg, detail := friendlyError(res.Lines, runErr)
		logf("엔진 확인 실패: %s\n%s", msg, detail)
		a.setEngine(EngineStatus{State: "error", GPUs: res.GPUs, Message: msg, Detail: detail})
		return
	}
	names := make([]string, 0, len(res.GPUs))
	for _, g := range res.GPUs {
		names = append(names, g.Name)
	}
	logf("엔진 정상. GPU: %s", strings.Join(names, ", "))
	a.setEngine(EngineStatus{State: "ok", GPUs: res.GPUs, Message: "준비 완료"})
}

func writeProbeImage(path string) error {
	img := image.NewNRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 10), uint8(y * 10), 128, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
