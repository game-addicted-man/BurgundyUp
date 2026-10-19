package inst

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	files, err := Extract(makeZip(t, map[string]string{"BurgundyUp.exe": "v1", "engine/models/a.param": "p"}), dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("extract: %v %v", files, err)
	}
	if _, err := Extract(makeZip(t, map[string]string{"BurgundyUp.exe": "v2"}), dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "BurgundyUp.exe")); string(b) != "v2" {
		t.Fatalf("overwrite failed: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.new")); len(left) > 0 {
		t.Fatalf("temp file left behind: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.new")); len(left) > 0 {
		t.Fatalf("temp file left behind: %v", left)
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
		"BurgundyUp.exe": "x", "uninstall.exe": "u", "engine/upscayl-bin.exe": "e",
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
	// 사용자가 넣은 파일·빈 폴더와 내려받은 모델
	os.WriteFile(filepath.Join(dir, "my-notes.txt"), []byte("keep me"), 0o644)
	os.MkdirAll(filepath.Join(dir, "my-empty", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "engine", "models", "ultrasharp-4x.bin"), []byte("dl"), 0o644)
	os.WriteFile(filepath.Join(dir, "engine", "models", "remacri-4x.bin.part"), []byte("dl"), 0o644)
	self := filepath.Join(dir, "uninstall.exe")
	if failed := RemoveInstalled(dir, m, self); len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	left := listTree(dir)
	want := []string{"install.json", "my-empty", "my-empty/sub", "my-notes.txt", "uninstall.exe"}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected leftovers: %v", left)
	}
}

func listTree(dir string) []string {
	var left []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			left = append(left, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(left)
	return left
}

func TestRemoveFilesReportsFailuresAndStaysInside(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	os.MkdirAll(filepath.Join(dir, "busy"), 0o755)
	os.WriteFile(filepath.Join(dir, "busy", "x"), []byte("x"), 0o644) // 비어 있지 않은 폴더라 지우기 실패
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(root, "outside.txt"), []byte("o"), 0o644)
	failed := RemoveFiles(dir, []string{"a.txt", "busy", "../outside.txt", "missing.txt"}, "")
	if len(failed) != 1 || failed[0] != "busy" {
		t.Fatalf("failed = %v", failed)
	}
	if _, err := os.Stat(filepath.Join(root, "outside.txt")); err != nil {
		t.Fatal("file outside install dir was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Fatal("a.txt not removed")
	}
}

func TestLinkedFoldersAreNotFollowed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	lib := filepath.Join(root, "my-model-library") // 사용자가 engine\models 를 연결해 둔 곳
	os.MkdirAll(filepath.Join(dir, "engine"), 0o755)
	os.MkdirAll(lib, 0o755)
	os.WriteFile(filepath.Join(lib, "realesrgan-x4plus.bin"), []byte("m"), 0o644)
	os.WriteFile(filepath.Join(lib, "mine.param"), []byte("m"), 0o644)
	if err := os.Symlink(lib, filepath.Join(dir, "engine", "models")); err != nil {
		t.Skip("symlink not supported:", err)
	}
	os.WriteFile(filepath.Join(dir, "BurgundyUp.exe"), []byte("x"), 0o644)
	m := &Marker{App: AppID, Files: []string{"BurgundyUp.exe", "engine/models/realesrgan-x4plus.bin"}}
	if failed := RemoveInstalled(dir, m, ""); len(failed) != 0 {
		t.Fatal(failed)
	}
	for _, f := range []string{"realesrgan-x4plus.bin", "mine.param"} {
		if _, err := os.Stat(filepath.Join(lib, f)); err != nil {
			t.Fatalf("file behind a linked folder was removed: %s", f)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "engine", "models")); err != nil {
		t.Fatal("the link itself was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "BurgundyUp.exe")); err == nil {
		t.Fatal("normal file not removed")
	}
}

func TestUpgradeRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	v1, _ := Extract(makeZip(t, map[string]string{"BurgundyUp.exe": "1", "engine/old.dll": "o", "licenses/a.txt": "l"}), dir)
	v2, _ := Extract(makeZip(t, map[string]string{"BurgundyUp.exe": "2", "licenses/a.txt": "l"}), dir)
	stale := StaleFiles(v1, v2)
	if len(stale) != 1 || stale[0] != "engine/old.dll" {
		t.Fatalf("stale = %v", stale)
	}
	if failed := RemoveFiles(dir, stale, ""); len(failed) != 0 {
		t.Fatal(failed)
	}
	if got := listTree(dir); strings.Join(got, ",") != "BurgundyUp.exe,licenses,licenses/a.txt" {
		t.Fatalf("after upgrade: %v", got)
	}
	if u := Union([]string{"a", "B"}, []string{"b", "c"}); strings.Join(u, ",") != "a,B,c" {
		t.Fatalf("union = %v", u)
	}
}

func TestOverlaps(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "AppData", "BurgundyUp")
	cases := []struct {
		dir  string
		want bool
	}{
		{data, true},
		{filepath.Join(data, "app"), true},
		{filepath.Join(base, "AppData"), true},
		{strings.ToUpper(data), true}, // 대소문자 무시
		{filepath.Join(base, "AppData", "BurgundyUp-2"), false},
		{filepath.Join(base, "Programs", "BurgundyUp"), false},
	}
	for _, c := range cases {
		if got := Overlaps(c.dir, data); got != c.want {
			t.Errorf("Overlaps(%q) = %v, want %v", c.dir, got, c.want)
		}
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
