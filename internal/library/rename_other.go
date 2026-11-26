//go:build !(linux && (amd64 || arm64))

package library

import "os"

// renameNoReplace moves src to dst without replacing an existing dst, by link then unlink.
func renameNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
