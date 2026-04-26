//go:build unix

// The unix build constraint name was introduced in Go 1.19, but this repository
// itself targets the toolchain declared in go.mod.

package cmd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	// Keep the low-level open here: we need no-follow plus non-blocking and
	// close-on-exec behavior before we can stat/reject the opened descriptor.
	// os.OpenFile cannot express the same syscall.O_NOFOLLOW/O_CLOEXEC/
	// O_NONBLOCK combination used here. Clear O_NONBLOCK immediately after a
	// successful open so the returned descriptor behaves like a normal file.
	fd, err := syscall.Open(file, syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("refusing to write secret to symlink %q", file)
		}
		return nil, err
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), file), nil
}
