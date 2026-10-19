#!/bin/sh
# dist/ 에 설치 파일과 압축판을 만듭니다 (Linux·macOS 에서도 Windows 용으로 빌드).
set -e
cd "$(dirname "$0")"
go run ./tools/release "$@"
