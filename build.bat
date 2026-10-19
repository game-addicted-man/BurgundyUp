@echo off
chcp 65001 >nul
cd /d "%~dp0"
where go >nul 2>nul
if errorlevel 1 (
  echo Go 가 설치되어 있지 않아요. https://go.dev/dl/ 에서 설치한 뒤 다시 실행하세요.
  pause
  exit /b 1
)
go run ./tools/release %*
if errorlevel 1 (
  echo.
  echo 빌드에 실패했어요. 위의 오류 내용을 확인하세요.
  pause
  exit /b 1
)
echo.
echo dist 폴더에 설치 파일과 압축판을 만들었어요.
start "" "%~dp0dist"
pause
