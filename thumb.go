package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 목록에 보이는 작은 미리보기를 만듭니다. 큰 사진을 그대로 띄우면 화면이 느려지기 때문입니다.

var thumbSem = make(chan struct{}, 2)

const thumbMax = 200

func (a *App) apiThumb(w http.ResponseWriter, r *http.Request) {
	it, ok := a.queue.Get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(it.Src))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".jfif", ".gif":
	case ".webp", ".bmp":
		a.apiImage(w, r) // 브라우저가 직접 그릴 수 있는 형식은 원본을 그대로
		return
	default:
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(a.DataDir, "tmp", "thumbs")
	cache := filepath.Join(dir, it.ID+".jpg")
	if b, err := os.ReadFile(cache); err == nil {
		serveJPEG(w, r, b)
		return
	}
	if it.W > 0 && it.H > 0 && it.W*it.H > 80_000_000 {
		http.NotFound(w, r)
		return
	}
	thumbSem <- struct{}{}
	b, err := makeThumb(it.Src)
	<-thumbSem
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(cache, b, 0o644)
	serveJPEG(w, r, b)
}

func serveJPEG(w http.ResponseWriter, r *http.Request, b []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
}

func makeThumb(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return nil, os.ErrInvalid
	}
	tw, th := sw, sh
	if sw > thumbMax || sh > thumbMax {
		if sw >= sh {
			tw, th = thumbMax, max(1, sh*thumbMax/sw)
		} else {
			tw, th = max(1, sw*thumbMax/sh), thumbMax
		}
	}
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// 칸마다 원본 픽셀을 평균 내는 방식(박스 필터)으로 줄입니다.
	for ty := 0; ty < th; ty++ {
		y0, y1 := ty*sh/th, (ty+1)*sh/th
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for tx := 0; tx < tw; tx++ {
			x0, x1 := tx*sw/tw, (tx+1)*sw/tw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, n uint32
			for y := y0; y < y1; y++ {
				row := rgba.Pix[y*rgba.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					// 투명한 부분은 어두운 회색 바탕 위에 올린 것처럼 섞습니다 (RGBA 는 알파가 미리 곱해진 값).
					inv := 255 - uint32(p[3])
					r += uint32(p[0]) + 40*inv/255
					g += uint32(p[1]) + 41*inv/255
					bl += uint32(p[2]) + 45*inv/255
					n++
				}
			}
			di := ty*dst.Stride + tx*4
			dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2], dst.Pix[di+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	out := applyOrientation(dst, jpegOrientation(path))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
