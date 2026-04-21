//go:build windows

package cmd

import "os"

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_WRONLY, 0)
}
