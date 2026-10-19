package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFriendlyError(t *testing.T) {
	cases := []struct {
		lines []string
		want  string
	}{
		{[]string{"vkCreateInstance failed -9", "🚨 Error: Invalid GPU Device"}, "Vulkan을 지원하는"},
		{[]string{"🚨 Error: Invalid GPU Device"}, "Vulkan을 지원하는"},
		{[]string{"[0 NVIDIA GeForce RTX 3060]  queueC=2[8]  queueG=0[16]", "🚨 Error: Invalid GPU Device"}, "선택한 GPU"},
		{[]string{"[0 X]  queueC=2[8]", "vkAllocateMemory failed -2", "VK_ERROR_OUT_OF_DEVICE_MEMORY"}, "그래픽 메모리가 부족"},
		{[]string{"🚨 Error: Couldn't read the image 'a.png'! (channels: 0)"}, "이미지를 읽지 못했어요"},
		{[]string{"🚨 Error: Couldn't write the image b.png"}, "저장하지 못했어요"},
		{[]string{"fopen models/x.param failed"}, "모델 파일을"},
	}
	for _, c := range cases {
		got, _ := friendlyError(c.lines, nil)
		if !strings.Contains(got, c.want) {
			t.Errorf("lines %q: got %q, want contains %q", c.lines, got, c.want)
		}
	}
	// 종료 코드 매핑은 ExitError 가 필요해서 실제 프로세스로 확인합니다 (Linux 에서는 0~255 만 가능).
	err := exec.Command("sh", "-c", "exit 3").Run()
	if got, detail := friendlyError(nil, err); !strings.Contains(detail, "종료 코드: 3") || got == "" {
		t.Errorf("exit code detail missing: %q %q", got, detail)
	}
}

func TestDetectModelScale(t *testing.T) {
	for name, want := range map[string]int{
		"realesrgan-x4plus": 4, "4x-UltraSharp": 4, "RealESRGAN_x2plus": 2, "upscayl-lite-4x": 4,
		"realesr-animevideov3-x3": 3, "1x_Denoise": 1, "x16-model": 16, "mymodel": 4, "foo_8x_bar": 8,
	} {
		if got := detectModelScale(name); got != want {
			t.Errorf("%s: got %d want %d", name, got, want)
		}
	}
}

func TestEngineArgs(t *testing.T) {
	a := strings.Join(engineArgs(EngineJob{Input: `C:\사진\a b.png`, Output: `C:\out\a.webp`, ModelFile: "realesrgan-x4plus",
		ModelScale: 4, TargetScale: 2, Format: "webp", Compression: 10, GPU: "1", Tile: 128, TTA: true}), "|")
	want := `-i|C:\사진\a b.png|-o|C:\out\a.webp|-m|models|-n|realesrgan-x4plus|-z|4|-f|webp|-c|10|-s|2|-g|1|-t|128|-x`
	if a != want {
		t.Errorf("args\n got %s\nwant %s", a, want)
	}
	b := strings.Join(engineArgs(EngineJob{Input: "i", Output: "o.png", ModelFile: "realesr-animevideov3-x3", ModelScale: 3, TargetScale: 3, Format: "png"}), "|")
	if strings.Contains(b, "-s|") || !strings.Contains(b, "-z|3") {
		t.Errorf("native scale should not pass -s: %s", b)
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("img2.png", "img10.png") || naturalLess("img10.png", "img2.png") {
		t.Error("natural sort broken")
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitizeFileName(`a<b>:c"/d\e|f?g*h. `); got != "a_b__c__d_e_f_g_h" {
		t.Errorf("sanitize: %q", got)
	}
	if got := safeRelDir(`../x/./y\..\z`); got != "x/y/z" && got != `x\y\z` {
		t.Errorf("safeRelDir: %q", got)
	}
}
