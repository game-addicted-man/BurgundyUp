# BurgundyUp 에서 고친 부분

원본: https://github.com/jchv/go-webview2 (커밋 56598839c808)

1. `pkg/edge/chromium.go` — `EnvironmentCompleted`, `CreateCoreWebView2ControllerCompleted`
   - amd64 에서 HRESULT 가 0 으로 확장돼 `int64(res) < 0` 검사가 항상 거짓이었습니다.
     WebView2 환경·컨트롤러 생성이 실패하면 nil 포인터를 써서 프로그램이 조용히 꺼졌습니다.
   - `int32(res) < 0 || 포인터 == nil` 로 검사하고, 실패하면 `log.Fatalf` 대신 실패를 표시해
     `Embed` 가 false 를 돌려주게 했습니다 (앱은 같은 실행에서 브라우저 모드로 넘어감).
2. `pkg/edge/chromium.go` — `PermissionRequested`
   - `GetPermissionKind` 출력 인수에 NULL 이 넘어가던 것을 `&kind` 로 고쳤습니다.
