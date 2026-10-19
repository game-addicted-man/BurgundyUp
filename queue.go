package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Item 은 작업 목록의 이미지 한 장입니다.
type Item struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Src      string  `json:"src"`
	Origin   string  `json:"origin"` // path(파일 선택) | upload(끌어다 놓기·붙여넣기)
	SrcDir   string  `json:"srcDir"` // 원본 폴더 ("" 이면 알 수 없음)
	RelDir   string  `json:"relDir"` // 폴더째 넣었을 때의 하위 폴더
	Size     int64   `json:"size"`
	W        int     `json:"w"`
	H        int     `json:"h"`
	Status   string  `json:"status"` // ready | queued | running | done | error | canceled
	Progress float64 `json:"progress"`
	Out      string  `json:"out"`
	OutW     int     `json:"outW"`
	OutH     int     `json:"outH"`
	OutSize  int64   `json:"outSize"`
	Note     string  `json:"note"`
	Error    string  `json:"error"`
	Detail   string  `json:"detail"`
	Model    string  `json:"model"`
	Scale    int     `json:"scale"`
	Elapsed  float64 `json:"elapsed"`

	started time.Time
	snap    *Settings
}

type Queue struct {
	app       *App
	mu        sync.Mutex
	items     []*Item
	byID      map[string]*Item
	rev       int64
	seq       int64
	active    bool // 작업 실행 중
	runSnap   *Settings
	cancelCur context.CancelFunc
	wake      chan struct{}
}

const maxItems = 10000

func NewQueue(app *App) *Queue {
	q := &Queue{app: app, byID: map[string]*Item{}, wake: make(chan struct{}, 1)}
	go q.worker()
	return q
}

func (q *Queue) bumpLocked() { q.rev++ }

func (q *Queue) newIDLocked() string {
	q.seq++
	return "i" + strconv.FormatInt(q.seq, 10)
}

// Snapshot 은 화면에 보낼 목록 사본을 돌려줍니다.
func (q *Queue) Snapshot() (items []Item, rev int64, active bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items = make([]Item, len(q.items))
	for i, it := range q.items {
		items[i] = *it
		items[i].snap = nil
		if it.Status == "running" {
			items[i].Elapsed = time.Since(it.started).Seconds()
		}
	}
	return items, q.rev, q.active
}

func (q *Queue) Rev() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rev
}

func (q *Queue) Get(id string) (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.byID[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

var ownOutputRe = regexp.MustCompile(`_upscaled_\d+x_`)

func (q *Queue) addLocked(it *Item) bool {
	if len(q.items) >= maxItems {
		return false
	}
	// 아직 처리하지 않은 같은 파일이 이미 있으면 다시 넣지 않습니다.
	if it.Origin == "path" {
		for _, e := range q.items {
			if (e.Status == "ready" || e.Status == "queued") && strings.EqualFold(e.Src, it.Src) {
				return false
			}
		}
	}
	it.ID = q.newIDLocked()
	it.Status = "ready"
	if q.active && q.runSnap != nil {
		// 작업 중에 넣은 이미지는 지금 작업에 이어서 처리합니다.
		it.Status = "queued"
		it.snap = q.runSnap
	}
	q.items = append(q.items, it)
	q.byID[it.ID] = it
	q.bumpLocked()
	if it.Status == "queued" {
		q.signal()
	}
	return true
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// AddPaths 는 파일이나 폴더 경로를 목록에 넣습니다. 폴더는 하위 폴더까지 이미지를 찾습니다.
func (q *Queue) AddPaths(paths []string) (added, skipped int) {
	type cand struct{ path, srcDir, relDir string }
	var cands []cand
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			skipped++
			continue
		}
		if !st.IsDir() {
			if isImageFile(p) {
				cands = append(cands, cand{p, filepath.Dir(p), ""})
			} else {
				skipped++
			}
			continue
		}
		rootName := filepath.Base(p)
		var found []cand
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != p && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if len(found)+len(cands) >= maxItems {
				return filepath.SkipAll
			}
			if !isImageFile(d.Name()) || ownOutputRe.MatchString(d.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(p, filepath.Dir(path))
			relDir := rootName
			if rel != "." && rel != "" {
				relDir = filepath.Join(rootName, rel)
			}
			found = append(found, cand{path, filepath.Dir(path), relDir})
			return nil
		})
		sort.Slice(found, func(i, j int) bool { return naturalLess(found[i].path, found[j].path) })
		cands = append(cands, found...)
	}
	for _, c := range cands {
		it := &Item{Name: filepath.Base(c.path), Src: c.path, Origin: "path", SrcDir: c.srcDir, RelDir: c.relDir}
		if st, err := os.Stat(c.path); err == nil {
			it.Size = st.Size()
		}
		it.W, it.H = imageSize(c.path)
		q.mu.Lock()
		ok := q.addLocked(it)
		q.mu.Unlock()
		if ok {
			added++
		} else {
			skipped++
		}
	}
	return
}

// AddUpload 는 끌어다 놓거나 붙여넣은 이미지(이미 임시 폴더에 저장됨)를 목록에 넣습니다.
func (q *Queue) AddUpload(path, name, relDir string) (*Item, error) {
	it := &Item{Name: name, Src: path, Origin: "upload", RelDir: relDir}
	if st, err := os.Stat(path); err == nil {
		it.Size = st.Size()
	}
	it.W, it.H = imageSize(path)
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.addLocked(it) {
		return nil, errors.New("목록이 가득 찼어요")
	}
	cp := *it
	cp.snap = nil
	return &cp, nil
}

// Start 는 대기 중인 이미지(또는 지정한 이미지)를 현재 설정으로 처리하기 시작합니다.
func (q *Queue) Start(ids []string, redo bool) (int, error) {
	snap := q.app.settings.Get()
	if !q.app.models.IsInstalled(snap.Model) {
		return 0, errors.New("선택한 모델이 설치되어 있지 않아요. 모델을 내려받거나 다른 모델을 골라 주세요.")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active {
		return 0, errors.New("이미 작업 중이에요.")
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	n := 0
	s := snap
	for _, it := range q.items {
		ok := false
		if len(ids) > 0 {
			ok = want[it.ID] && it.Status != "running"
		} else {
			ok = it.Status == "ready" || (redo && (it.Status == "done" || it.Status == "error" || it.Status == "canceled"))
		}
		if !ok {
			continue
		}
		it.Status = "queued"
		it.Progress = 0
		it.Error, it.Detail, it.Note = "", "", ""
		it.snap = &s
		n++
	}
	if n == 0 {
		return 0, errors.New("업스케일할 이미지가 없어요. 먼저 이미지를 넣어 주세요.")
	}
	q.active = true
	q.runSnap = &s
	q.bumpLocked()
	q.signal()
	return n, nil
}

// Cancel 은 지금 작업을 멈추고, 남은 이미지는 대기 상태로 돌려놓습니다.
func (q *Queue) Cancel() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		if it.Status == "queued" {
			it.Status = "ready"
			it.snap = nil
		}
	}
	if q.cancelCur != nil {
		q.cancelCur()
	}
	q.runSnap = nil
	q.bumpLocked()
}

func (q *Queue) Remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.byID[id]
	if !ok {
		return nil
	}
	if it.Status == "running" {
		return errors.New("처리 중인 이미지는 뺄 수 없어요. 먼저 멈춰 주세요.")
	}
	q.removeLocked(it)
	q.bumpLocked()
	return nil
}

// Clear 는 처리 중이 아닌 이미지를 모두 목록에서 뺍니다. onlyDone 이면 끝난 것만 뺍니다.
func (q *Queue) Clear(onlyDone bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var keep []*Item
	for _, it := range q.items {
		remove := it.Status != "running" && it.Status != "queued"
		if onlyDone {
			remove = it.Status == "done"
		}
		if remove {
			q.cleanupLocked(it)
			delete(q.byID, it.ID)
		} else {
			keep = append(keep, it)
		}
	}
	q.items = keep
	q.bumpLocked()
}

func (q *Queue) removeLocked(it *Item) {
	for i, e := range q.items {
		if e == it {
			q.items = append(q.items[:i], q.items[i+1:]...)
			break
		}
	}
	delete(q.byID, it.ID)
	q.cleanupLocked(it)
}

func (q *Queue) cleanupLocked(it *Item) {
	if it.Origin == "upload" && strings.HasPrefix(it.Src, q.app.UploadDir) {
		_ = os.RemoveAll(filepath.Dir(it.Src))
	}
}

// Shutdown 은 프로그램을 닫을 때 실행 중인 엔진을 멈춥니다.
func (q *Queue) Shutdown() {
	q.Cancel()
}

func (q *Queue) worker() {
	for range q.wake {
		for {
			q.mu.Lock()
			var next *Item
			for _, it := range q.items {
				if it.Status == "queued" {
					next = it
					break
				}
			}
			if next == nil {
				q.active = false
				q.runSnap = nil
				q.bumpLocked()
				q.mu.Unlock()
				break
			}
			q.mu.Unlock()
			q.process(next)
		}
	}
}

func (q *Queue) process(it *Item) {
	q.mu.Lock()
	if it.Status != "queued" || it.snap == nil {
		q.mu.Unlock()
		return
	}
	snap := *it.snap
	it.Status = "running"
	it.Progress = 0
	it.started = time.Now()
	it.Model = modelDisplayName(snap.Model)
	it.Scale = snap.Scale
	ctx, cancel := context.WithCancel(context.Background())
	q.cancelCur = cancel
	q.bumpLocked()
	q.mu.Unlock()

	r := q.app.upscaleOne(ctx, it, snap, func(p float64) {
		q.mu.Lock()
		if p > 99.5 {
			p = 99.5
		}
		if p > it.Progress {
			it.Progress = p
			q.bumpLocked()
		}
		q.mu.Unlock()
	})
	canceled := ctx.Err() != nil
	cancel()

	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelCur = nil
	it.Elapsed = time.Since(it.started).Seconds()
	it.snap = nil
	switch {
	case canceled:
		it.Status = "canceled"
		it.Progress = 0
	case r.err != nil:
		it.Status = "error"
		it.Error = r.msg
		it.Detail = r.detail
		logf("실패: %s → %s\n%s", it.Src, r.msg, r.detail)
	default:
		it.Status = "done"
		it.Progress = 100
		it.Out, it.OutW, it.OutH, it.OutSize = r.out, r.outW, r.outH, r.outSize
		it.Note = r.note
		logf("완료: %s → %s (%.1f초)", it.Src, r.out, it.Elapsed)
	}
	q.bumpLocked()
}

type upscaleResult struct {
	out        string
	outW, outH int
	outSize    int64
	note       string
	err        error
	msg        string
	detail     string
}

func fail(msg string) upscaleResult {
	return upscaleResult{err: errors.New(msg), msg: msg}
}

// upscaleOne 은 이미지 한 장을 실제로 처리합니다.
func (a *App) upscaleOne(ctx context.Context, it *Item, s Settings, onProgress func(float64)) upscaleResult {
	if _, err := os.Stat(it.Src); err != nil {
		return fail("원본 파일을 찾을 수 없어요. 옮겨졌거나 삭제된 것 같아요.")
	}
	file, mscale, err := a.models.Resolve(s.Model, s.Scale)
	if err != nil {
		return fail(err.Error())
	}
	if it.W > 0 && it.H > 0 {
		ow, oh := it.W*s.Scale, it.H*s.Scale
		work := mscale
		if s.Scale > work {
			work = s.Scale
		}
		mp := float64(it.W*work) * float64(it.H*work) / 1e6
		switch {
		case s.Format == "webp" && (ow > 16383 || oh > 16383):
			return fail(fmt.Sprintf("결과 크기(%d×%d)가 WebP 한계(16383픽셀)를 넘어요. 저장 형식을 PNG나 JPG로 바꾸거나 배율을 낮춰 주세요.", ow, oh))
		case s.Format == "jpg" && (ow > 65500 || oh > 65500):
			return fail(fmt.Sprintf("결과 크기(%d×%d)가 JPG 한계를 넘어요. PNG로 저장하거나 배율을 낮춰 주세요.", ow, oh))
		case mp > 600:
			return fail(fmt.Sprintf("이미지가 너무 커요 (처리 중 %.0f메가픽셀). 배율을 낮추거나 더 작은 이미지를 써 주세요.", mp))
		}
	}

	// 저장 폴더 정하기
	var note string
	dir := ""
	if s.OutputMode == "same" && it.SrcDir != "" {
		if dirWritable(it.SrcDir) {
			dir = it.SrcDir
		} else {
			note = "원본 폴더에 저장할 수 없어서 기본 저장 폴더에 저장했어요."
		}
	}
	if dir == "" {
		dir = s.OutputDir
		if it.RelDir != "" {
			dir = filepath.Join(dir, safeRelDir(it.RelDir))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail("저장 폴더를 만들 수 없어요: " + dir)
	}
	base := strings.TrimSuffix(it.Name, filepath.Ext(it.Name))
	outName := fmt.Sprintf("%s_upscaled_%dx_%s.%s", sanitizeFileName(base), s.Scale, modelShort(s.Model), s.Format)
	outPath := uniquePath(filepath.Join(dir, outName))

	comp := 0
	if s.Format == "webp" {
		comp = 100 - s.WebpQuality
	}
	input := it.Src
	if o := jpegOrientation(it.Src); o > 1 {
		// 휴대폰 사진의 회전 정보를 반영한 사본을 만들어 엔진에 넘깁니다.
		tmpDir := filepath.Join(a.DataDir, "tmp")
		_ = os.MkdirAll(tmpDir, 0o755)
		upright := filepath.Join(tmpDir, "upright-"+it.ID+".png")
		if err := writeUprightCopy(it.Src, upright, o); err == nil {
			input = upright
			defer os.Remove(upright)
		} else {
			logf("회전 사본 만들기 실패 (%s): %v", it.Src, err)
		}
	}
	job := EngineJob{
		Input: input, Output: outPath, ModelFile: file, ModelScale: mscale, TargetScale: s.Scale,
		Format: s.Format, Compression: comp, GPU: s.GPU, Tile: s.Tile, TTA: s.TTA,
	}
	res, runErr := a.runEngine(ctx, job, onProgress)
	if ctx.Err() != nil {
		_ = os.Remove(outPath)
		return upscaleResult{err: ctx.Err()}
	}
	st, statErr := os.Stat(outPath)
	ok := statErr == nil && st.Size() > 0
	var ow, oh int
	if ok {
		ow, oh = imageSize(outPath)
		if runErr != nil {
			// 저장 후 정리 단계에서 엔진이 죽는 경우가 있어서, 결과 크기가 맞으면 성공으로 봅니다.
			ok = it.W > 0 && ow == it.W*s.Scale && oh == it.H*s.Scale
		}
	}
	if !ok {
		_ = os.Remove(outPath)
		msg, detail := friendlyError(res.Lines, runErr)
		return upscaleResult{err: errors.New(msg), msg: msg, detail: detail}
	}
	return upscaleResult{out: outPath, outW: ow, outH: oh, outSize: st.Size(), note: note}
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".aiup-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// uniquePath 는 같은 이름의 파일이 있으면 " (2)", " (3)" 을 붙입니다.
func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 2; i < 10000; i++ {
		c := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
	return p
}

var badNameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func sanitizeFileName(s string) string {
	s = badNameChars.ReplaceAllString(s, "_")
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	if s == "" {
		s = "image"
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}

func safeRelDir(rel string) string {
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' })
	var out []string
	for _, p := range parts {
		if p == "." || p == ".." || p == "" {
			continue
		}
		out = append(out, sanitizeFileName(p))
	}
	return filepath.Join(out...)
}

// naturalLess 는 "img2" 가 "img10" 보다 앞에 오도록 정렬합니다.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			ia, ib := 0, 0
			for ia < len(a) && isDigit(a[ia]) {
				ia++
			}
			for ib < len(b) && isDigit(b[ib]) {
				ib++
			}
			na, _ := strconv.ParseUint(a[:ia], 10, 64)
			nb, _ := strconv.ParseUint(b[:ib], 10, 64)
			if na != nb {
				return na < nb
			}
			a, b = a[ia:], b[ib:]
			continue
		}
		if ca != cb {
			return ca < cb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
