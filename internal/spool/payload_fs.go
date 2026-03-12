package spool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func ensureDirectoryAt(parentFD int, parentPath, name string) (int, error) {
	created := false
	if err := unix.Mkdirat(parentFD, name, spoolDirMode); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("create spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
	} else {
		created = true
	}

	fd, err := openDirectoryAtNoFollow(parentFD, name)
	if err != nil {
		return -1, fmt.Errorf("open spool directory %s: %w", filepath.Join(parentPath, name), err)
	}
	if err := unix.Fchmod(fd, spoolDirMode); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("chmod spool directory %s: %w", filepath.Join(parentPath, name), err)
	}
	if err := syncFD(fd, filepath.Join(parentPath, name)); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if created {
		if err := syncFD(parentFD, parentPath); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

func ensureDirectoryPathNoFollow(path string) (int, string, error) {
	return walkDirectoryPathNoFollow(path, true)
}

func openDirectoryPathNoFollow(path string) (int, string, error) {
	return walkDirectoryPathNoFollow(path, false)
}

func walkDirectoryPathNoFollow(path string, create bool) (int, string, error) {
	cleanPath := filepath.Clean(path)
	if cleanPath == "" {
		return -1, "", fmt.Errorf("directory path cannot be empty")
	}

	startPath := "."
	if filepath.IsAbs(cleanPath) {
		startPath = string(filepath.Separator)
	}

	currentFD, err := openDirectoryNoFollow(startPath)
	if err != nil {
		return -1, "", fmt.Errorf("open spool directory %s: %w", startPath, err)
	}
	currentPath := startPath

	components := strings.Split(cleanPath, string(filepath.Separator))
	if filepath.IsAbs(cleanPath) && len(components) > 0 {
		components = components[1:]
	}

	for _, component := range components {
		switch component {
		case "", ".":
			continue
		case "..":
			_ = unix.Close(currentFD)
			return -1, "", fmt.Errorf("directory path %q must not contain parent traversal", path)
		}

		nextPath := filepath.Join(currentPath, component)
		var (
			nextFD  int
			created bool
		)
		if create {
			if err = unix.Mkdirat(currentFD, component, spoolDirMode); err != nil {
				if !errors.Is(err, unix.EEXIST) {
					_ = unix.Close(currentFD)
					return -1, "", fmt.Errorf("create spool directory %s: %w", nextPath, err)
				}
			} else {
				created = true
			}
			nextFD, err = openDirectoryAtNoFollow(currentFD, component)
			if err != nil {
				_ = unix.Close(currentFD)
				return -1, "", fmt.Errorf("open spool directory %s: %w", nextPath, err)
			}
			if created {
				if err := unix.Fchmod(nextFD, spoolDirMode); err != nil {
					_ = unix.Close(currentFD)
					_ = unix.Close(nextFD)
					return -1, "", fmt.Errorf("chmod spool directory %s: %w", nextPath, err)
				}
				if err := syncFD(nextFD, nextPath); err != nil {
					_ = unix.Close(currentFD)
					_ = unix.Close(nextFD)
					return -1, "", err
				}
				if err := syncFD(currentFD, currentPath); err != nil {
					_ = unix.Close(currentFD)
					_ = unix.Close(nextFD)
					return -1, "", err
				}
			}
		} else {
			nextFD, err = openDirectoryAtNoFollow(currentFD, component)
			if err != nil {
				_ = unix.Close(currentFD)
				return -1, "", fmt.Errorf("open spool directory %s: %w", nextPath, err)
			}
		}
		if err := unix.Close(currentFD); err != nil {
			_ = unix.Close(nextFD)
			return -1, "", fmt.Errorf("close spool directory %s: %w", currentPath, err)
		}
		currentFD = nextFD
		currentPath = nextPath
	}

	return currentFD, cleanPath, nil
}

func createDirectoryAt(parentFD int, parentPath, name string) (int, error) {
	if err := unix.Mkdirat(parentFD, name, spoolDirMode); err != nil {
		return -1, fmt.Errorf("create spool directory %s: %w", filepath.Join(parentPath, name), err)
	}
	fd, err := openDirectoryAtNoFollow(parentFD, name)
	if err != nil {
		return -1, fmt.Errorf("open spool directory %s: %w", filepath.Join(parentPath, name), err)
	}
	if err := unix.Fchmod(fd, spoolDirMode); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("chmod spool directory %s: %w", filepath.Join(parentPath, name), err)
	}
	if err := syncFD(fd, filepath.Join(parentPath, name)); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err := syncFD(parentFD, parentPath); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func createStagingDirectoryAt(parentFD int, parentPath, recordID string) (string, int, error) {
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := newUUIDv4()
		if err != nil {
			return "", -1, fmt.Errorf("generate staging suffix for %s: %w", recordID, err)
		}
		name := fmt.Sprintf(".stage-%s-%s", recordID, suffix)
		if err := unix.Mkdirat(parentFD, name, spoolDirMode); err != nil {
			if errors.Is(err, unix.EEXIST) || errors.Is(err, syscall.EEXIST) {
				continue
			}
			return "", -1, fmt.Errorf("create spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		fd, err := openDirectoryAtNoFollow(parentFD, name)
		if err != nil {
			return "", -1, fmt.Errorf("open spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := unix.Fchmod(fd, spoolDirMode); err != nil {
			_ = unix.Close(fd)
			return "", -1, fmt.Errorf("chmod spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := syncFD(fd, filepath.Join(parentPath, name)); err != nil {
			_ = unix.Close(fd)
			return "", -1, err
		}
		if err := syncFD(parentFD, parentPath); err != nil {
			_ = unix.Close(fd)
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, fmt.Errorf("create staging directory for %s: exhausted unique names", recordID)
}

func createPublishTempDirectoryAt(parentFD int, parentPath, recordID string) (string, int, error) {
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := newUUIDv4()
		if err != nil {
			return "", -1, fmt.Errorf("generate publish suffix for %s: %w", recordID, err)
		}
		name := fmt.Sprintf("%s%s-%s", publishTempPrefix, recordID, suffix)
		if err := unix.Mkdirat(parentFD, name, spoolDirMode); err != nil {
			if errors.Is(err, unix.EEXIST) || errors.Is(err, syscall.EEXIST) {
				continue
			}
			return "", -1, fmt.Errorf("create spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		fd, err := openDirectoryAtNoFollow(parentFD, name)
		if err != nil {
			return "", -1, fmt.Errorf("open spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := unix.Fchmod(fd, spoolDirMode); err != nil {
			_ = unix.Close(fd)
			return "", -1, fmt.Errorf("chmod spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := syncFD(fd, filepath.Join(parentPath, name)); err != nil {
			_ = unix.Close(fd)
			return "", -1, err
		}
		if err := syncFD(parentFD, parentPath); err != nil {
			_ = unix.Close(fd)
			return "", -1, err
		}
		return name, fd, nil
	}
	return "", -1, fmt.Errorf("create publish temp directory for %s: exhausted unique names", recordID)
}

func writeRegularFileAt(parentFD int, parentPath, name string, data []byte) error {
	tmpName, err := uniqueTempName(parentFD, ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp payload file in %s: %w", parentPath, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlinkat(parentFD, tmpName, 0)
		}
	}()

	fd, err := unix.Openat(parentFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, spoolFileMode)
	if err != nil {
		return fmt.Errorf("create temp payload file %s: %w", filepath.Join(parentPath, tmpName), err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parentPath, tmpName))
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("wrap temp payload file %s", filepath.Join(parentPath, tmpName))
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write payload file %s: %w", filepath.Join(parentPath, tmpName), err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("fsync payload file %s: %w", filepath.Join(parentPath, tmpName), err)
	}
	if err := file.Chmod(spoolFileMode); err != nil {
		return fmt.Errorf("chmod payload file %s: %w", filepath.Join(parentPath, tmpName), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close payload file %s: %w", filepath.Join(parentPath, tmpName), err)
	}
	closed = true
	if err := unix.Renameat(parentFD, tmpName, parentFD, name); err != nil {
		return fmt.Errorf("rename payload file %s -> %s: %w", filepath.Join(parentPath, tmpName), filepath.Join(parentPath, name), err)
	}
	cleanup = false
	return syncFD(parentFD, parentPath)
}

func removeTreeContentsFD(dirFD int, dirPath string) error {
	entries, err := readDirectoryEntriesFD(dirFD, dirPath)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", dirPath, err)
	}
	for _, entry := range entries {
		if err := removeEntryTreeAt(dirFD, dirPath, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func removeEntryTreeAt(parentFD int, parentPath, name string) error {
	mode, exists, err := entryModeAt(parentFD, name)
	if err != nil {
		return fmt.Errorf("stat spool entry %s: %w", filepath.Join(parentPath, name), err)
	}
	if !exists {
		return nil
	}

	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		dirFD, err := openDirectoryAtNoFollow(parentFD, name)
		if err != nil {
			return fmt.Errorf("open spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := removeTreeContentsFD(dirFD, filepath.Join(parentPath, name)); err != nil {
			_ = unix.Close(dirFD)
			return err
		}
		if err := unix.Close(dirFD); err != nil {
			return fmt.Errorf("close spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
		if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove spool directory %s: %w", filepath.Join(parentPath, name), err)
		}
	case unix.S_IFREG:
		if err := unix.Unlinkat(parentFD, name, 0); err != nil {
			return fmt.Errorf("remove spool file %s: %w", filepath.Join(parentPath, name), err)
		}
	default:
		return fmt.Errorf("unexpected file type at %s: %w", filepath.Join(parentPath, name), errUnexpectedFileType)
	}
	return nil
}

func copyDirectoryTreeAt(sourceParentFD int, sourceParentPath, sourceName string, targetParentFD int, targetParentPath, targetName string) error {
	targetFD, err := createDirectoryAt(targetParentFD, targetParentPath, targetName)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(targetParentPath, targetName)
	cleanup := true
	defer func() {
		if cleanup {
			_ = removeEntryTreeAt(targetParentFD, targetParentPath, targetName)
			_ = syncFD(targetParentFD, targetParentPath)
		}
	}()
	defer unix.Close(targetFD)

	sourceFD, err := openDirectoryAtNoFollow(sourceParentFD, sourceName)
	if err != nil {
		return fmt.Errorf("open spool directory %s: %w", filepath.Join(sourceParentPath, sourceName), err)
	}
	defer unix.Close(sourceFD)

	if err := copyDirectoryEntries(sourceFD, filepath.Join(sourceParentPath, sourceName), targetFD, targetPath); err != nil {
		return err
	}

	if err := syncFD(targetFD, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func copyDirectoryContentsAt(sourceParentFD int, sourceParentPath, sourceName string, targetParentFD int, targetParentPath, targetName string) error {
	sourceFD, err := openDirectoryAtNoFollow(sourceParentFD, sourceName)
	if err != nil {
		return fmt.Errorf("open spool directory %s: %w", filepath.Join(sourceParentPath, sourceName), err)
	}
	defer unix.Close(sourceFD)

	targetFD, err := openDirectoryAtNoFollow(targetParentFD, targetName)
	if err != nil {
		return fmt.Errorf("open spool directory %s: %w", filepath.Join(targetParentPath, targetName), err)
	}
	defer unix.Close(targetFD)

	return copyDirectoryEntries(sourceFD, filepath.Join(sourceParentPath, sourceName), targetFD, filepath.Join(targetParentPath, targetName))
}

func copyDirectoryEntries(sourceFD int, sourcePath string, targetFD int, targetPath string) error {
	entries, err := readDirectoryEntriesFD(sourceFD, sourcePath)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", sourcePath, err)
	}
	for _, entry := range entries {
		mode, exists, err := entryModeAt(sourceFD, entry.Name())
		if err != nil {
			return fmt.Errorf("stat spool entry %s: %w", filepath.Join(sourcePath, entry.Name()), err)
		}
		if !exists {
			return fmt.Errorf("spool entry disappeared during copy: %s", filepath.Join(sourcePath, entry.Name()))
		}
		switch mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := copyDirectoryTreeAt(sourceFD, sourcePath, entry.Name(), targetFD, targetPath, entry.Name()); err != nil {
				return err
			}
		case unix.S_IFREG:
			data, err := readRegularFileAtNoFollow(sourceFD, entry.Name())
			if err != nil {
				return fmt.Errorf("read spool file %s: %w", filepath.Join(sourcePath, entry.Name()), err)
			}
			if err := writeRegularFileAt(targetFD, targetPath, entry.Name(), data); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected file type at %s: %w", filepath.Join(sourcePath, entry.Name()), errUnexpectedFileType)
		}
	}
	return nil
}

func entryModeAt(parentFD int, name string) (uint32, bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return stat.Mode & unix.S_IFMT, true, nil
}

func uniqueTempName(parentFD int, prefix string) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := newUUIDv4()
		if err != nil {
			return "", err
		}
		name := prefix + suffix
		var stat unix.Stat_t
		err = unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == nil:
			continue
		case errors.Is(err, unix.ENOENT):
			return name, nil
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("exhausted unique temp names")
}

var errUnexpectedFileType = errors.New("unexpected file type")

func openDirectoryNoFollow(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	if err := requireFileType(fd, unix.S_IFDIR); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openDirectoryAtNoFollow(parentFD int, name string) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	if err := requireFileType(fd, unix.S_IFDIR); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func readRegularFileAtNoFollow(parentFD int, name string) ([]byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(name))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap file descriptor for %s", name)
	}
	defer file.Close()

	if err := requireFileType(int(file.Fd()), unix.S_IFREG); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func readDirectoryEntriesFD(fd int, name string) ([]os.DirEntry, error) {
	dupFD, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dupFD), name)
	if dir == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("wrap directory file descriptor for %s", name)
	}
	defer dir.Close()

	return dir.ReadDir(-1)
}

func requireFileType(fd int, expected uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != expected {
		return errUnexpectedFileType
	}
	return nil
}

func syncFD(fd int, path string) error {
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("fsync directory %s: %w", path, err)
	}
	return nil
}

func uniqueQuarantineNameAt(parentFD int, name string) (string, error) {
	target := name
	for i := 1; ; i++ {
		var stat unix.Stat_t
		err := unix.Fstatat(parentFD, target, &stat, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == nil:
			target = fmt.Sprintf("%s-%d", name, i)
		case errors.Is(err, unix.ENOENT):
			return target, nil
		default:
			return "", err
		}
	}
}

func renameDirectoryAt(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error {
	if err := requireEntryTypeAt(sourceParentFD, sourceName, unix.S_IFDIR); err != nil {
		return err
	}
	return unix.Renameat(sourceParentFD, sourceName, targetParentFD, targetName)
}

func requireEntryTypeAt(parentFD int, name string, expected uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != expected {
		return errUnexpectedFileType
	}
	return nil
}

func isCrossDeviceError(err error) bool {
	return errors.Is(err, unix.EXDEV) || errors.Is(err, syscall.EXDEV)
}

func unexpectedEntryError(rootPath, name string) error {
	return fmt.Errorf("unexpected spool entry %s: %w", filepath.Join(rootPath, name), errUnexpectedFileType)
}

func classifyRecordPathError(id, path string, err error, missingMessage string) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOENT):
		return &PayloadCorruptionError{ID: id, Path: path, Err: fmt.Errorf("%s", missingMessage)}
	case errors.Is(err, unix.ELOOP):
		return &PayloadCorruptionError{ID: id, Path: path, Err: fmt.Errorf("path must not be a symlink")}
	case errors.Is(err, unix.ENOTDIR), errors.Is(err, errUnexpectedFileType):
		return &PayloadCorruptionError{ID: id, Path: path, Err: fmt.Errorf("unexpected file type")}
	default:
		return fmt.Errorf("read payload path %s: %w", path, err)
	}
}
