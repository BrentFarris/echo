//go:build windows

package broker

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var revealShell = windows.NewLazySystemDLL("shell32.dll")
var revealCOM = windows.NewLazySystemDLL("ole32.dll")

// Use shell item identifiers rather than a command string: filenames containing
// spaces, commas and Unicode cannot become Explorer options or shell commands.
// https://learn.microsoft.com/en-us/windows/win32/api/shlobj_core/nf-shlobj_core-shopenfolderandselectitems
func RevealInFileBrowser(target RevealTarget) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := revealCOM.NewProc("CoInitializeEx").Call(0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("initialize File Explorer: HRESULT 0x%08x", uint32(hr))
	}
	defer revealCOM.NewProc("CoUninitialize").Call()
	path, err := windows.UTF16PtrFromString(target.Path)
	if err != nil {
		return err
	}
	if !target.Select {
		verb, _ := windows.UTF16PtrFromString("explore")
		result, _, _ := revealShell.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(path)), 0, 0, 1)
		if result <= 32 {
			return fmt.Errorf("open containing folder in File Explorer: error %d", result)
		}
		return nil
	}
	var item uintptr
	hr, _, _ = revealShell.NewProc("SHParseDisplayName").Call(uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&item)), 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("locate changed file: HRESULT 0x%08x", uint32(hr))
	}
	defer revealCOM.NewProc("CoTaskMemFree").Call(item)
	hr, _, _ = revealShell.NewProc("SHOpenFolderAndSelectItems").Call(item, 0, 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("reveal changed file in File Explorer: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}
