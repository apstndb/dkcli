//go:build unix

package cmd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	fd, err := syscall.Open(file, syscall.O_WRONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("refusing to write secret to symlink %q", file)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), file), nil
}
