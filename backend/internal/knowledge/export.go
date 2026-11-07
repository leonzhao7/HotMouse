package knowledge

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Export materializes the embedded bundle for agents that can read local files.
// Existing identical files are reused; user-edited files are never overwritten.
func (c *Catalog) Export(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return fs.WalkDir(resources, ".", func(name string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		destination := filepath.Join(root, filepath.FromSlash(name))
		if item.IsDir() {
			return ensureDirectory(destination)
		}
		data, err := resources.ReadFile(name)
		if err != nil {
			return err
		}
		info, err := os.Lstat(destination)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("knowledge destination is not a regular file: %s", destination)
			}
			existing, err := os.ReadFile(destination)
			if err == nil && !bytes.Equal(existing, data) {
				return fmt.Errorf("knowledge destination has different content: %s", destination)
			}
			return err
		}
		if !os.IsNotExist(err) {
			return err
		}
		// No two HotMouse invocations write the same worker directory. O_EXCL
		// also prevents accidentally replacing an existing file or symlink.
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil {
			_ = os.Remove(destination)
			return err
		}
		if closeErr != nil {
			_ = os.Remove(destination)
		}
		return closeErr
	})
}

func ensureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("knowledge destination is not a directory: %s", directory)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(directory)
	if parent != directory {
		if err := ensureDirectory(parent); err != nil {
			return err
		}
	}
	return os.Mkdir(directory, 0o700)
}
