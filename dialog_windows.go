//go:build windows

package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 기본 "열기" 창(IFileOpenDialog)을 COM 으로 직접 띄웁니다.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1

	fosNoChangeDir      = 0x00000008
	fosPickFolders      = 0x00000020
	fosForceFileSystem  = 0x00000040
	fosAllowMultiSelect = 0x00000200
	fosPathMustExist    = 0x00000800
	fosFileMustExist    = 0x00001000

	sigdnFileSysPath = 0x80058000
	hrCancelled      = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
)

// 가상 함수 표(vtable) 순서 — ShObjIdl_core.h 기준
const (
	vtRelease = 2
	// IModalWindow
	vtShow = 3
	// IFileDialog
	vtSetFileTypes     = 4
	vtSetFileTypeIndex = 5
	vtSetOptions       = 9
	vtGetOptions       = 10
	vtSetTitle         = 17
	vtSetOkButtonLabel = 18
	vtGetResult        = 20
	// IFileOpenDialog
	vtGetResults = 27
	// IShellItem
	vtItemGetDisplayName = 5
	// IShellItemArray
	vtArrGetCount  = 7
	vtArrGetItemAt = 8
)

type comObject struct {
	vtbl *[32]uintptr
}

//go:uintptrescapes
func (o *comObject) call(idx int, args ...uintptr) uintptr {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(unsafe.Pointer(o)))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[idx], all...)
	return r
}

func (o *comObject) release() {
	if o != nil {
		o.call(vtRelease)
	}
}

type filterSpec struct {
	name *uint16
	spec *uint16
}

const (
	dialogImages = iota
	dialogFolder
)

func pickImageFiles() ([]string, error) {
	var paths []string
	var err error
	onUIThread(func() { paths, err = showFileDialog(dialogImages, "업스케일할 이미지를 고르세요 (여러 장 선택 가능)", "") })
	return paths, err
}

func pickFolder(title string) (string, error) {
	var paths []string
	var err error
	onUIThread(func() { paths, err = showFileDialog(dialogFolder, title, "폴더 선택") })
	if len(paths) == 0 {
		return "", err
	}
	return paths[0], err
}

func showFileDialog(kind int, title, okLabel string) ([]string, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if int32(hr) >= 0 { // S_OK 또는 S_FALSE 면 짝을 맞춰 해제
		defer procCoUninitialize.Call()
	}

	var dlg *comObject
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if int32(hr) < 0 || dlg == nil {
		return nil, fmt.Errorf("CoCreateInstance 실패 (0x%08X)", uint32(hr))
	}
	defer dlg.release()

	var opts uint32
	if int32(dlg.call(vtGetOptions, uintptr(unsafe.Pointer(&opts)))) < 0 {
		opts = 0
	}
	opts |= fosForceFileSystem | fosNoChangeDir | fosPathMustExist
	if kind == dialogFolder {
		opts |= fosPickFolders
	} else {
		opts |= fosAllowMultiSelect | fosFileMustExist
	}
	dlg.call(vtSetOptions, uintptr(opts))

	titlePtr, _ := windows.UTF16PtrFromString(title)
	dlg.call(vtSetTitle, uintptr(unsafe.Pointer(titlePtr)))
	var okPtr *uint16
	if okLabel != "" {
		okPtr, _ = windows.UTF16PtrFromString(okLabel)
		dlg.call(vtSetOkButtonLabel, uintptr(unsafe.Pointer(okPtr)))
	}

	var specs []filterSpec
	if kind == dialogImages {
		n1, _ := windows.UTF16PtrFromString("이미지 파일 (PNG, JPG, WEBP, BMP, TIF, GIF)")
		s1, _ := windows.UTF16PtrFromString("*.png;*.jpg;*.jpeg;*.jfif;*.webp;*.bmp;*.tif;*.tiff;*.gif")
		n2, _ := windows.UTF16PtrFromString("모든 파일 (*.*)")
		s2, _ := windows.UTF16PtrFromString("*.*")
		specs = []filterSpec{{n1, s1}, {n2, s2}}
		dlg.call(vtSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
		dlg.call(vtSetFileTypeIndex, 1)
	}

	hr = dlg.call(vtShow, ownerWindow())
	runtime.KeepAlive(specs)
	runtime.KeepAlive(titlePtr)
	runtime.KeepAlive(okPtr)
	if uint32(hr) == hrCancelled {
		return nil, nil
	}
	if int32(hr) < 0 {
		return nil, fmt.Errorf("선택 창 오류 (0x%08X)", uint32(hr))
	}

	if kind == dialogFolder {
		var item *comObject
		hr = dlg.call(vtGetResult, uintptr(unsafe.Pointer(&item)))
		if int32(hr) < 0 || item == nil {
			return nil, fmt.Errorf("선택 결과를 읽지 못했어요 (0x%08X)", uint32(hr))
		}
		defer item.release()
		p, err := shellItemPath(item)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	}

	var arr *comObject
	hr = dlg.call(vtGetResults, uintptr(unsafe.Pointer(&arr)))
	if int32(hr) < 0 || arr == nil {
		return nil, fmt.Errorf("선택 결과를 읽지 못했어요 (0x%08X)", uint32(hr))
	}
	defer arr.release()
	var count uint32
	if int32(arr.call(vtArrGetCount, uintptr(unsafe.Pointer(&count)))) < 0 {
		return nil, fmt.Errorf("선택 결과를 읽지 못했어요")
	}
	out := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		var item *comObject
		if int32(arr.call(vtArrGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item)))) < 0 || item == nil {
			continue
		}
		if p, err := shellItemPath(item); err == nil {
			out = append(out, p)
		}
		item.release()
	}
	return out, nil
}

func shellItemPath(item *comObject) (string, error) {
	var psz *uint16
	hr := item.call(vtItemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&psz)))
	if int32(hr) < 0 || psz == nil {
		return "", fmt.Errorf("파일 경로를 읽지 못했어요 (0x%08X)", uint32(hr))
	}
	s := windows.UTF16PtrToString(psz)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(psz)))
	return s, nil
}
