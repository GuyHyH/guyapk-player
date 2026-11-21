package android

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

// hideConsole stops a console window from flashing. It deliberately avoids
// HideWindow, which would also hide the emulator's own display window.
func hideConsole(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

type memoryStatusEx struct {
	Length, MemoryLoad                        uint32
	TotalPhys, AvailPhys, TotalPageFile       uint64
	AvailPageFile, TotalVirtual, AvailVirtual uint64
	AvailExtendedVirtual                      uint64
}

// TotalRAMMB returns physical memory in MiB.
func TotalRAMMB() int {
	m := memoryStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0
	}
	return int(m.TotalPhys >> 20)
}

// FreeDisk returns free bytes on the volume containing path.
func FreeDisk(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return free, nil
}
