package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Settings 는 화면에서 고르는 모든 옵션입니다. settings.json 으로 저장됩니다.
type Settings struct {
	Model       string `json:"model"`       // 모델 ID (models.go 참고)
	Scale       int    `json:"scale"`       // 2, 3, 4
	Format      string `json:"format"`      // png | jpg | webp
	WebpQuality int    `json:"webpQuality"` // 10..100 (100 = 무손실)
	OutputMode  string `json:"outputMode"`  // same(원본 폴더) | custom(지정 폴더)
	OutputDir   string `json:"outputDir"`   // 지정 폴더 (끌어다 놓은 파일도 여기에 저장)
	GPU         string `json:"gpu"`         // "" = 자동, "0", "1", ...
	Tile        int    `json:"tile"`        // 0 = 자동
	TTA         bool   `json:"tta"`         // 고품질(TTA) 모드
	UIMode      string `json:"uiMode"`      // auto(앱 창) | browser(웹 브라우저)
}

var validTiles = []int{0, 32, 64, 128, 256, 512}

func defaultSettings(defaultOut string) Settings {
	return Settings{
		Model:       "realesrgan-x4plus",
		Scale:       4,
		Format:      "png",
		WebpQuality: 90,
		OutputMode:  "same",
		OutputDir:   defaultOut,
		GPU:         "",
		Tile:        0,
		TTA:         false,
		UIMode:      "auto",
	}
}

// sanitize 는 잘못된 값이 들어와도 앱이 정상 동작하도록 값을 바로잡습니다.
func (s *Settings) sanitize(defaultOut string) {
	if s.Model == "" {
		s.Model = "realesrgan-x4plus"
	}
	if s.Scale < 2 || s.Scale > 4 {
		s.Scale = 4
	}
	switch strings.ToLower(s.Format) {
	case "png", "jpg", "webp":
		s.Format = strings.ToLower(s.Format)
	case "jpeg":
		s.Format = "jpg"
	default:
		s.Format = "png"
	}
	// upscayl-bin 은 압축값을 10 단위로 반올림하므로 품질도 10 단위로 맞춥니다.
	q := (s.WebpQuality + 5) / 10 * 10
	if q < 10 {
		q = 10
	}
	if q > 100 {
		q = 100
	}
	s.WebpQuality = q
	if s.OutputMode != "custom" {
		s.OutputMode = "same"
	}
	s.OutputDir = strings.TrimSpace(s.OutputDir)
	if s.OutputDir == "" || !filepath.IsAbs(s.OutputDir) {
		s.OutputDir = defaultOut
	}
	if s.GPU != "" {
		if n, err := strconv.Atoi(s.GPU); err != nil || n < 0 || n > 15 {
			s.GPU = ""
		}
	}
	ok := false
	for _, t := range validTiles {
		if s.Tile == t {
			ok = true
		}
	}
	if !ok {
		s.Tile = 0
	}
	if s.UIMode != "browser" {
		s.UIMode = "auto"
	}
}

type SettingsStore struct {
	mu         sync.Mutex
	path       string
	defaultOut string
	s          Settings
}

func loadSettings(path, defaultOut string) *SettingsStore {
	st := &SettingsStore{path: path, defaultOut: defaultOut, s: defaultSettings(defaultOut)}
	if b, err := os.ReadFile(path); err == nil {
		var s Settings = defaultSettings(defaultOut)
		if json.Unmarshal(b, &s) == nil {
			st.s = s
		}
	}
	st.s.sanitize(defaultOut)
	return st
}

func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

// Update 는 JSON 으로 들어온 일부 값만 덮어쓰고 저장합니다.
func (st *SettingsStore) Update(patch []byte) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.s
	if err := json.Unmarshal(patch, &next); err != nil {
		return st.s, err
	}
	next.sanitize(st.defaultOut)
	st.s = next
	st.saveLocked()
	return st.s, nil
}

func (st *SettingsStore) Mutate(f func(s *Settings)) Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	f(&st.s)
	st.s.sanitize(st.defaultOut)
	st.saveLocked()
	return st.s
}

func (st *SettingsStore) saveLocked() {
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(st.path), 0o755)
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logf("설정 저장 실패: %v", err)
		return
	}
	if err := os.Rename(tmp, st.path); err != nil {
		logf("설정 저장 실패: %v", err)
	}
}
