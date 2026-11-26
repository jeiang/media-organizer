//go:build unix

package library

import (
	"errors"
	"syscall"
)

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// RemoveEmptyDir removes path only if it is an empty directory; rmdir(2) never unlinks a file.
func RemoveEmptyDir(path string) error { return syscall.Rmdir(path) }
