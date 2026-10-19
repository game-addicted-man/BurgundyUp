// BurgundyUp — 저해상도 이미지를 AI(Real-ESRGAN)로 선명하게 키워 주는 Windows 프로그램.
// 실제 업스케일은 Upscayl 의 엔진(upscayl-bin, ncnn + Vulkan)이 GPU 로 처리하고,
// 이 프로그램은 화면(WebView2)과 작업 관리를 맡습니다.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	headless := flag.Bool("headless", false, "창 없이 화면 서버만 실행 (개발·테스트용)")
	browser := flag.Bool("browser", false, "앱 창(WebView2) 대신 웹 브라우저로 열기")
	port := flag.Int("port", 0, "화면 서버 포트 (0 = 자동)")
	flag.Parse()
	if *headless {
		logEcho = true
	}

	platformInit()
	if !*headless && !ensureSingleInstance() {
		return
	}

	app, err := NewApp()
	if err != nil {
		showErrorBox(appTitle, err.Error())
		os.Exit(1)
	}
	url, err := app.StartServer(*port)
	if err != nil {
		showErrorBox(appTitle, "화면 서버를 시작하지 못했어요:\n"+err.Error())
		os.Exit(1)
	}
	go app.ProbeEngine()

	if *headless {
		fmt.Println(url)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		<-ch
		app.Shutdown()
		return
	}
	runUI(app, *browser)
	app.Shutdown()
}
