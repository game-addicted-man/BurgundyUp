package main

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 엔진(upscayl-bin)이 읽을 수 있는 입력 확장자.
// Windows 판 엔진은 WIC 로 디코딩하므로 bmp/tif/gif 도 읽습니다.
var inputExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".jfif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true, ".gif": true,
}

func isImageFile(name string) bool {
	return inputExts[strings.ToLower(filepath.Ext(name))]
}

// imageSize 는 파일 머리 부분만 읽어서 가로·세로 크기를 알아냅니다.
// 모르는 형식이면 0, 0 을 돌려줍니다.
func imageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n < 30 {
		return 0, 0
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		w := int(binary.BigEndian.Uint32(head[16:20]))
		h := int(binary.BigEndian.Uint32(head[20:24]))
		return w, h
	case bytes.HasPrefix(head, []byte("RIFF")) && string(head[8:12]) == "WEBP":
		return webpSize(head)
	case bytes.HasPrefix(head, []byte("BM")):
		w := int(int32(binary.LittleEndian.Uint32(head[18:22])))
		h := int(int32(binary.LittleEndian.Uint32(head[22:26])))
		if h < 0 {
			h = -h
		}
		return w, h
	case bytes.HasPrefix(head, []byte("\xff\xd8")), bytes.HasPrefix(head, []byte("GIF8")):
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, 0
		}
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			return 0, 0
		}
		if jpegOrientation(path) >= 5 { // 90도 돌려서 보여 주는 사진은 가로·세로가 바뀝니다
			return cfg.Height, cfg.Width
		}
		return cfg.Width, cfg.Height
	}
	return 0, 0
}

func webpSize(b []byte) (int, int) {
	if len(b) < 30 {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8 ": // 손실 압축
		w := int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return w, h
	case "VP8L": // 무손실
		if b[20] != 0x2f {
			return 0, 0
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		w := int(bits&0x3fff) + 1
		h := int((bits>>14)&0x3fff) + 1
		return w, h
	case "VP8X": // 확장 형식
		w := int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h := int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		return w, h
	}
	return 0, 0
}
