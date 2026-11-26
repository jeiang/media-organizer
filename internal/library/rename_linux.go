//go:build linux && (amd64 || arm64)

package library

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	atFdcwd             = -100 // AT_FDCWD
	renameNoReplaceFlag = 1    // RENAME_NOREPLACE
)

// renameNoReplace moves src to dst without replacing an existing dst. Unlike link(2), it
// needs no write access to the file itself, which fs.protected_hardlinks would demand.
func renameNoReplace(src, dst string) error {
	s, err := syscall.BytePtrFromString(src)
	if err != nil {
		return err
	}
	d, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return err
	}
	cwd := atFdcwd
	_, _, errno := syscall.Syscall6(sysRenameat2, uintptr(cwd), uintptr(unsafe.Pointer(s)), uintptr(cwd), uintptr(unsafe.Pointer(d)), renameNoReplaceFlag, 0)
	if errno != 0 {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: errno}
	}
	return nil
}
