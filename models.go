package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type modelFile struct {
	Name   string
	Size   int64
	SHA256 string
}

// modelDef 는 앱이 알고 있는 모델 하나를 설명합니다.
type modelDef struct {
	ID       string
	Name     string         // 화면에 보이는 이름
	Sub      string         // 원래 모델 이름
	Desc     string         // 설명
	Tag      string         // 추천 / 빠름 같은 꼬리표
	Short    string         // 결과 파일 이름에 붙는 짧은 이름
	Variants map[int]string // 배율 → 모델 파일 이름(확장자 제외)
	Download []modelFile    // 비어 있으면 기본 포함 모델
}

// 추가 모델은 Upscayl 저장소의 고정된 커밋에서 내려받고 SHA-256 으로 검증합니다.
const upscaylModelCommit = "a00d55fee90e0f9435d5eaa86e76700df8199af8"

var builtinModels = []modelDef{
	{
		ID: "realesrgan-x4plus", Name: "일반 사진", Sub: "Real-ESRGAN x4plus (Upscayl Standard)",
		Desc: "사진·실사 이미지에 가장 무난한 기본 모델이에요.", Tag: "추천", Short: "standard",
		Variants: map[int]string{4: "realesrgan-x4plus"},
	},
	{
		ID: "upscayl-lite-4x", Name: "빠른 모드", Sub: "Upscayl Lite",
		Desc: "조금 덜 선명하지만 훨씬 빨라요. 저사양 PC나 많은 이미지에 좋아요.", Tag: "빠름", Short: "lite",
		Variants: map[int]string{4: "upscayl-lite-4x"},
	},
	{
		ID: "realesrgan-x4plus-anime", Name: "디지털 아트·일러스트", Sub: "Real-ESRGAN Anime",
		Desc: "애니메이션, 웹툰, 일러스트, 그림에 잘 맞아요.", Short: "art",
		Variants: map[int]string{4: "realesrgan-x4plus-anime"},
	},
	{
		ID: "realesr-animevideov3", Name: "애니메이션 (가벼움)", Sub: "Real-ESRGAN AnimeVideo v3",
		Desc: "애니 영상 캡처용 가벼운 모델이에요. 2·3·4배를 각각 직접 지원해요.", Short: "anime",
		Variants: map[int]string{2: "realesr-animevideov3-x2", 3: "realesr-animevideov3-x3", 4: "realesr-animevideov3-x4"},
	},
	{
		ID: "high-fidelity-4x", Name: "고화질 디테일", Sub: "High Fidelity",
		Desc: "원본의 질감과 디테일을 최대한 살려요.", Short: "hifi",
		Variants: map[int]string{4: "high-fidelity-4x"},
		Download: []modelFile{
			{Name: "high-fidelity-4x.param", Size: 108039, SHA256: "4576ed5c2fc5fa250d3c3d585ef02248f26abdfc1867088078f501fe71e5d61e"},
			{Name: "high-fidelity-4x.bin", Size: 33424520, SHA256: "8a135402b4f39286121b76abb47601a6b7b7e8d4f3e999a5aaa45ed277824fb4"},
		},
	},
	{
		ID: "ultrasharp-4x", Name: "아주 선명하게", Sub: "UltraSharp",
		Desc: "윤곽을 또렷하게 살려요. 깨끗한 원본에 잘 맞아요.", Short: "ultrasharp",
		Variants: map[int]string{4: "ultrasharp-4x"},
		Download: []modelFile{
			{Name: "ultrasharp-4x.param", Size: 116029, SHA256: "0136ca83686809a8f17f7111f11b951e8db93610e24b7f4137c9ffe4dbc4a806"},
			{Name: "ultrasharp-4x.bin", Size: 33424520, SHA256: "fb3e279d40d4cddb44db4e684d59e68d0aa39852c8cc14dc3f23ccc7e6eee9c1"},
		},
	},
	{
		ID: "remacri-4x", Name: "자연스러운 복원", Sub: "Remacri",
		Desc: "과하지 않게 자연스러운 디테일을 되살려요.", Short: "remacri",
		Variants: map[int]string{4: "remacri-4x"},
		Download: []modelFile{
			{Name: "remacri-4x.param", Size: 140295, SHA256: "859ecba5b3592ecf3e76c93bed65e9f627b5236dd696aae5a84ecf8c93ab65ce"},
			{Name: "remacri-4x.bin", Size: 33424520, SHA256: "a43be595c0d743314c30b50fe7ef188be0c61cc55c46ce81adb79ba4b3c3fb7a"},
		},
	},
	{
		ID: "ultramix-balanced-4x", Name: "균형 잡힌 선명함", Sub: "UltraMix Balanced",
		Desc: "선명함과 자연스러움 사이의 균형을 맞춰요.", Short: "ultramix",
		Variants: map[int]string{4: "ultramix-balanced-4x"},
		Download: []modelFile{
			{Name: "ultramix-balanced-4x.param", Size: 140295, SHA256: "859ecba5b3592ecf3e76c93bed65e9f627b5236dd696aae5a84ecf8c93ab65ce"},
			{Name: "ultramix-balanced-4x.bin", Size: 33424520, SHA256: "171cae5968485d366b4fb575d232f98d117d94b766b15f22849cfccde40d2050"},
		},
	},
}

// ModelView 는 화면에 보내는 모델 정보입니다.
type ModelView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Sub          string `json:"sub"`
	Desc         string `json:"desc"`
	Tag          string `json:"tag"`
	Native       []int  `json:"native"`
	Installed    bool   `json:"installed"`
	Downloadable bool   `json:"downloadable"`
	Size         int64  `json:"size"`
	Custom       bool   `json:"custom"`
}

type customModel struct {
	file  string
	scale int
}

type ModelRegistry struct {
	dir    string
	mu     sync.Mutex
	views  []ModelView
	custom map[string]customModel
}

func NewModelRegistry(dir string) *ModelRegistry {
	r := &ModelRegistry{dir: dir}
	r.Scan()
	return r
}

func (r *ModelRegistry) hasModelFile(base string) bool {
	p, err1 := os.Stat(filepath.Join(r.dir, base+".param"))
	b, err2 := os.Stat(filepath.Join(r.dir, base+".bin"))
	return err1 == nil && err2 == nil && p.Size() > 0 && b.Size() > 0
}

// Scan 은 models 폴더를 다시 살펴봅니다. 사용자가 넣은 .param/.bin 모델도 찾아냅니다.
func (r *ModelRegistry) Scan() {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	var views []ModelView
	for _, d := range builtinModels {
		v := ModelView{ID: d.ID, Name: d.Name, Sub: d.Sub, Desc: d.Desc, Tag: d.Tag}
		for scale, base := range d.Variants {
			known[strings.ToLower(base)] = true
			if r.hasModelFile(base) {
				v.Native = append(v.Native, scale)
			}
		}
		sort.Ints(v.Native)
		v.Installed = len(v.Native) > 0
		if len(d.Download) > 0 {
			v.Downloadable = true
			for _, f := range d.Download {
				v.Size += f.Size
			}
		}
		if !v.Installed && !v.Downloadable {
			continue // 기본 모델 파일이 지워졌으면 목록에서 뺍니다
		}
		views = append(views, v)
	}
	r.custom = map[string]customModel{}
	entries, _ := os.ReadDir(r.dir)
	var customs []ModelView
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".param") {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if known[strings.ToLower(base)] || !r.hasModelFile(base) {
			continue
		}
		scale := detectModelScale(base)
		id := "custom:" + base
		r.custom[id] = customModel{file: base, scale: scale}
		customs = append(customs, ModelView{
			ID: id, Name: base, Sub: "직접 추가한 모델",
			Desc:      fmt.Sprintf("models 폴더에 넣은 사용자 모델이에요 (%d배 모델로 인식).", scale),
			Native:    []int{scale},
			Installed: true, Custom: true,
		})
	}
	sort.Slice(customs, func(i, j int) bool { return strings.ToLower(customs[i].Name) < strings.ToLower(customs[j].Name) })
	r.views = append(views, customs...)
}

func (r *ModelRegistry) List() []ModelView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModelView, len(r.views))
	copy(out, r.views)
	return out
}

func (r *ModelRegistry) IsInstalled(id string) bool {
	for _, v := range r.List() {
		if v.ID == id {
			return v.Installed
		}
	}
	return false
}

func findBuiltin(id string) (modelDef, bool) {
	for _, d := range builtinModels {
		if d.ID == id {
			return d, true
		}
	}
	return modelDef{}, false
}

// Resolve 는 모델 ID 와 원하는 배율로 실제 모델 파일과 그 모델의 기본 배율을 정합니다.
func (r *ModelRegistry) Resolve(id string, target int) (file string, modelScale int, err error) {
	if d, ok := findBuiltin(id); ok {
		// 원하는 배율 그대로 지원하는 파일이 있으면 그것을, 없으면 더 큰 배율로 만든 뒤 줄입니다.
		scales := make([]int, 0, len(d.Variants))
		for s := range d.Variants {
			scales = append(scales, s)
		}
		sort.Ints(scales)
		pick := 0
		if base, ok := d.Variants[target]; ok && r.hasModelFile(base) {
			pick = target
		} else {
			for _, s := range scales {
				if s > target && r.hasModelFile(d.Variants[s]) {
					pick = s
					break
				}
			}
			if pick == 0 {
				for i := len(scales) - 1; i >= 0; i-- {
					if r.hasModelFile(d.Variants[scales[i]]) {
						pick = scales[i]
						break
					}
				}
			}
		}
		if pick == 0 {
			return "", 0, fmt.Errorf("'%s' 모델 파일이 없어요. 모델을 내려받거나 다른 모델을 골라 주세요.", d.Name)
		}
		return d.Variants[pick], pick, nil
	}
	r.mu.Lock()
	c, ok := r.custom[id]
	r.mu.Unlock()
	if ok && r.hasModelFile(c.file) {
		return c.file, c.scale, nil
	}
	return "", 0, errors.New("선택한 모델을 찾을 수 없어요. 다른 모델을 골라 주세요.")
}

func modelShort(id string) string {
	if d, ok := findBuiltin(id); ok {
		return d.Short
	}
	s := strings.TrimPrefix(id, "custom:")
	return sanitizeFileName(s)
}

func modelDisplayName(id string) string {
	if d, ok := findBuiltin(id); ok {
		return d.Name
	}
	return strings.TrimPrefix(id, "custom:")
}

var scaleRe = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x|x(\d{1,2})(?:[^0-9]|$)`)

// detectModelScale 은 모델 이름(예: 4x-UltraSharp, RealESRGAN_x2plus)에서 배율을 찾습니다.
func detectModelScale(name string) int {
	for _, m := range scaleRe.FindAllStringSubmatch(name, -1) {
		s := m[1]
		if s == "" {
			s = m[2]
		}
		n, _ := strconv.Atoi(s)
		switch n {
		case 1, 2, 3, 4, 8, 16:
			return n
		}
	}
	return 4
}

// ───────────────────────── 추가 모델 내려받기 ─────────────────────────

type DownloadState struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
	State   string `json:"state"` // idle | running | done | error | canceled
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error"`
}

type Downloader struct {
	mu     sync.Mutex
	st     DownloadState
	cancel context.CancelFunc
	dir    string
	onDone func()
}

func NewDownloader(dir string, onDone func()) *Downloader {
	return &Downloader{dir: dir, onDone: onDone, st: DownloadState{State: "idle"}}
}

func (d *Downloader) State() DownloadState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

func (d *Downloader) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
}

func modelURLs(file string) []string {
	var urls []string
	if base := strings.TrimSpace(os.Getenv("AIUP_MODEL_BASEURL")); base != "" { // 테스트용
		urls = append(urls, strings.TrimRight(base, "/")+"/"+file)
	}
	urls = append(urls,
		"https://raw.githubusercontent.com/upscayl/upscayl/"+upscaylModelCommit+"/resources/models/"+file,
		"https://github.com/upscayl/upscayl/raw/"+upscaylModelCommit+"/resources/models/"+file,
	)
	return urls
}

func (d *Downloader) Start(id string) error {
	def, ok := findBuiltin(id)
	if !ok || len(def.Download) == 0 {
		return errors.New("내려받을 수 없는 모델이에요.")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.State == "running" {
		return errors.New("다른 모델을 내려받는 중이에요. 끝난 뒤에 다시 시도해 주세요.")
	}
	var total int64
	for _, f := range def.Download {
		total += f.Size
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.st = DownloadState{ModelID: id, Name: def.Name, State: "running", Total: total}
	go d.run(ctx, def)
	return nil
}

func (d *Downloader) addDone(n int64) {
	d.mu.Lock()
	d.st.Done += n
	d.mu.Unlock()
}

func (d *Downloader) finish(state, msg string) {
	d.mu.Lock()
	d.st.State = state
	d.st.Error = msg
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.mu.Unlock()
	if d.onDone != nil {
		d.onDone()
	}
}

func (d *Downloader) run(ctx context.Context, def modelDef) {
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		d.finish("error", "models 폴더를 만들 수 없어요: "+err.Error())
		return
	}
	for _, f := range def.Download {
		final := filepath.Join(d.dir, f.Name)
		if fileHasHash(final, f.Size, f.SHA256) {
			d.addDone(f.Size)
			continue
		}
		var lastErr error
		for _, u := range modelURLs(f.Name) {
			if ctx.Err() != nil {
				break
			}
			lastErr = d.fetch(ctx, u, final, f)
			if lastErr == nil {
				break
			}
			logf("모델 내려받기 실패 (%s): %v", u, lastErr)
		}
		if ctx.Err() != nil {
			d.finish("canceled", "")
			return
		}
		if lastErr != nil {
			d.finish("error", "내려받지 못했어요. 인터넷 연결을 확인해 주세요. ("+lastErr.Error()+")")
			return
		}
	}
	logf("모델 설치 완료: %s", def.ID)
	d.finish("done", "")
}

func (d *Downloader) fetch(ctx context.Context, url, final string, f modelFile) error {
	part := final + ".part"
	start := d.State().Done
	defer func() {
		// 실패하면 진행률을 이 파일 시작 시점으로 되돌립니다.
		if _, err := os.Stat(final); err != nil {
			d.mu.Lock()
			d.st.Done = start
			d.mu.Unlock()
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AI-Upscaler/"+appVersion)
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, 256*1024)
	var written int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(part)
				return werr
			}
			h.Write(buf[:n])
			written += int64(n)
			d.addDone(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(part)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if written != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		os.Remove(part)
		return errors.New("받은 파일이 손상되었어요 (검증 실패)")
	}
	return os.Rename(part, final)
}

func fileHasHash(path string, size int64, want string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() != size {
		return false
	}
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
