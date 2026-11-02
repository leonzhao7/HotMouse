//go:build !windows

package clientconfig

import "os"

func privatePermissions(path string, dir bool) error {
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	return os.Chmod(path, mode)
}
