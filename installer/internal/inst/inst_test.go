package inst

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestExtractAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	files, err := Extract(makeZip(t, map[string]string{"AI-Upscaler.exe": "v1", "engine/models/a.param": "p"}), dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("extract: %v %v", files, err)
	}
	if _, err := Extract(makeZip(t, map[string]string{"AI-Upscaler.exe": "v2"}), dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "AI-Upscaler.exe")); string(b) != "v2" {
		t.Fatalf("overwrite failed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "AI-Upscaler.exe.new")); err == nil {
		t.Fatal("temp file left behind")
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	if _, err := Extract(makeZip(t, map[string]string{"../evil.txt": "x"}), filepath.Join(dir, "in")); err == nil {
		t.Fatal("zip slip not rejected")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("file written outside install dir")
	}
}

func TestMarkerAndSafeRemoval(t *testing.T) {
	dir := t.TempDir()
	files, err := Extract(makeZip(t, map[string]string{
		"AI-Upscaler.exe": "x", "uninstall.exe": "u", "engine/upscayl-bin.exe": "e",
		"engine/models/realesrgan-x4plus.param": "p", "licenses/a.txt": "l",
	}), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteMarker(dir, files); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarker(dir)
	if err != nil || m.App != AppID || len(m.Files) != 5 {
		t.Fatalf("marker: %+v %v", m, err)
	}
	// 사용자가 넣은 파일과 내려받은 모델
	os.WriteFile(filepath.Join(dir, "my-notes.txt"), []byte("keep me"), 0o644)
	os.WriteFile(filepath.Join(dir, "engine", "models", "ultrasharp-4x.bin"), []byte("dl"), 0o644)
	os.WriteFile(filepath.Join(dir, "engine", "models", "remacri-4x.bin.part"), []byte("dl"), 0o644)
	self := filepath.Join(dir, "uninstall.exe")
	RemoveInstalled(dir, m, self)
	left := map[string]bool{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			left[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if !left["my-notes.txt"] || !left["uninstall.exe"] || len(left) != 2 {
		t.Fatalf("unexpected leftovers: %v", left)
	}
}

func TestReadMarkerRejectsForeign(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, MarkerName), []byte(`{"app":"Other"}`), 0o644)
	if _, err := ReadMarker(dir); err == nil {
		t.Fatal("foreign marker accepted")
	}
	if _, err := ReadMarker(t.TempDir()); err == nil {
		t.Fatal("missing marker accepted")
	}
}
