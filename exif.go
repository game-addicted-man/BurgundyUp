package main

import (
	"bufio"
	"encoding/binary"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 휴대폰 사진은 픽셀은 눕혀 두고 "회전 정보(EXIF Orientation)"만 붙여 두는 경우가 많습니다.
// 엔진은 이 정보를 무시하므로, 필요하면 똑바로 세운 사본을 만들어 엔진에 넘깁니다.

// jpegOrientation 은 JPEG 의 EXIF 방향 값(1~8)을 돌려줍니다. 없으면 1.
func jpegOrientation(path string) int {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".jfif" {
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return 1
	}
	for i := 0; i < 64; i++ {
		var m [4]byte
		if _, err := io.ReadFull(r, m[:2]); err != nil {
			return 1
		}
		for m[0] == 0xFF && m[1] == 0xFF { // 채움 바이트
			m[1], _ = r.ReadByte()
		}
		if m[0] != 0xFF {
			return 1
		}
		marker := m[1]
		if marker == 0xDA || marker == 0xD9 { // 영상 데이터 시작 → 더 이상 EXIF 없음
			return 1
		}
		if _, err := io.ReadFull(r, m[2:4]); err != nil {
			return 1
		}
		size := int(binary.BigEndian.Uint16(m[2:4])) - 2
		if size < 0 {
			return 1
		}
		if marker != 0xE1 {
			if _, err := r.Discard(size); err != nil {
				return 1
			}
			continue
		}
		seg := make([]byte, size)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		if o := exifOrientation(seg); o > 0 {
			return o
		}
	}
	return 1
}

func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := seg[6:]
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off : off+2]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

// applyOrientation 은 EXIF 방향 값대로 이미지를 돌리거나 뒤집습니다.
func applyOrientation(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	// RGBA 로 옮길 때 draw 패키지의 빠른 경로(JPEG 의 YCbCr → RGBA)를 씁니다.
	s := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(s, s.Bounds(), src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	d := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		for dx := 0; dx < dw; dx++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-dx, dy
			case 3:
				sx, sy = w-1-dx, h-1-dy
			case 4:
				sx, sy = dx, h-1-dy
			case 5:
				sx, sy = dy, dx
			case 6:
				sx, sy = dy, h-1-dx
			case 7:
				sx, sy = w-1-dy, h-1-dx
			case 8:
				sx, sy = w-1-dy, dx
			}
			si := sy*s.Stride + sx*4
			di := dy*d.Stride + dx*4
			copy(d.Pix[di:di+4], s.Pix[si:si+4])
		}
	}
	return d
}

// writeUprightCopy 는 똑바로 세운 PNG 사본을 dst 에 저장합니다.
func writeUprightCopy(srcPath, dstPath string, o int) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	img = applyOrientation(img, o)
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(out, img); err != nil {
		out.Close()
		os.Remove(dstPath)
		return err
	}
	return out.Close()
}
