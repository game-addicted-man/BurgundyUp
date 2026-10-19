// fetch-engine 은 engine 폴더(upscayl-bin.exe, vcomp140.dll, 기본 모델)를 GitHub 에서 받아 채웁니다.
//
//	go run ./tools/fetch-engine            (저장소 최상위 폴더에서)
//	go run ./tools/fetch-engine -dir 다른/폴더
package main

import (
	"flag"
	"fmt"
	"os"

	"burgundyup/tools/internal/fetch"
)

func main() {
	dir := flag.String("dir", "engine", "엔진을 넣을 폴더")
	flag.Parse()
	if err := fetch.Ensure(*dir, func(f string, a ...any) { fmt.Printf(f+"\n", a...) }); err != nil {
		fmt.Fprintln(os.Stderr, "오류:", err)
		os.Exit(1)
	}
}
