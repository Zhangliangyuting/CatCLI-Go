package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile publishes a complete file. The caller holds fileLocks and
// checks its overwrite/version policy before calling. External writers are not
// coordinated by that lock. Replacement changes the inode and breaks hard-link
// sharing; permission bits are retained, but ownership and extended attributes
// are not copied.
func atomicWriteFile(ctx context.Context, path string, data []byte, createOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if !createOnly {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("expected a regular file: %s", path)
		}
		mode = info.Mode().Perm()
		// Preserve the existing follow-symlink behavior of the file handlers.
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".catcli-write-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !createOnly {
		err = os.Rename(tmp.Name(), path)
	} else {
		// Publish without overwriting a file created after the absence check.
		err = os.Link(tmp.Name(), path)
	}
	if err != nil {
		return err
	}
	if createOnly {
		if err := os.Remove(tmp.Name()); err != nil {
			return fmt.Errorf("file published, temporary file cleanup failed: %w", err)
		}
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("file published, directory sync failed: %w", err)
	}
	return nil
}
