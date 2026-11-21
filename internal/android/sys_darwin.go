package android

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

func hideConsole(*exec.Cmd) {}

// TotalRAMMB returns physical memory in MiB.
func TotalRAMMB() int {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int(v >> 20)
}

// FreeDisk returns free bytes on the volume containing path.
func FreeDisk(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
