package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vaultfleet/pkg/protocol"
)

const (
	EntryTypeDir  = "dir"
	EntryTypeFile = "file"
)

type DirEntry = protocol.DirEntry

var browseTimeout = 10 * time.Second

var excludedTopLevelDirs = map[string]struct{}{
	"proc": {},
	"sys":  {},
	"dev":  {},
	"run":  {},
	"tmp":  {},
	"snap": {},
}

func Browse(fsRoot string, scanPath string, maxDepth int) ([]DirEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), browseTimeout)
	defer cancel()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cleanRoot, cleanScanPath, err := resolveScanPath(fsRoot, scanPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Lstat(cleanScanPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("scan path %q is a symlink, not a directory", cleanScanPath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan path %q is not a directory", cleanScanPath)
	}

	baseDepth := pathDepth(cleanScanPath)
	entries := []DirEntry{}

	err = filepath.WalkDir(cleanScanPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cleanPath := filepath.Clean(path)
		if walkErr != nil {
			if cleanPath == cleanScanPath {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if cleanPath == cleanScanPath {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if isExcludedTopLevel(cleanRoot, cleanPath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if pathDepth(cleanPath)-baseDepth > maxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entryType := EntryTypeFile
		if entry.IsDir() {
			entryType = EntryTypeDir
		}
		entries = append(entries, DirEntry{
			Path: cleanPath,
			Type: entryType,
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return entries, err
		}
		return entries, err
	}
	return entries, nil
}

func resolveScanPath(fsRoot string, scanPath string) (string, string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(fsRoot))
	if err != nil {
		return "", "", err
	}

	cleanScanPath := filepath.Clean(scanPath)
	if filepath.IsAbs(cleanScanPath) {
		cleanScanPath, err = filepath.Abs(cleanScanPath)
	} else {
		cleanScanPath, err = filepath.Abs(filepath.Join(cleanRoot, cleanScanPath))
	}
	if err != nil {
		return "", "", err
	}
	if !isWithinRoot(cleanRoot, cleanScanPath) {
		return "", "", fmt.Errorf("scan path %q is outside root %q", cleanScanPath, cleanRoot)
	}

	evaluatedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", "", err
	}
	evaluatedScanPath, err := filepath.EvalSymlinks(cleanScanPath)
	if err != nil {
		return "", "", err
	}
	if !isWithinRoot(evaluatedRoot, evaluatedScanPath) {
		return "", "", fmt.Errorf("scan path %q is outside root %q", cleanScanPath, cleanRoot)
	}
	return cleanRoot, cleanScanPath, nil
}

func isWithinRoot(fsRoot string, path string) bool {
	rel, err := filepath.Rel(fsRoot, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func isExcludedTopLevel(fsRoot string, path string) bool {
	rel, err := filepath.Rel(fsRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return false
	}
	first := rel
	if idx := strings.IndexRune(rel, os.PathSeparator); idx >= 0 {
		first = rel[:idx]
	}
	_, excluded := excludedTopLevelDirs[first]
	return excluded
}

func pathDepth(path string) int {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	clean = strings.TrimPrefix(clean, volume)
	clean = strings.Trim(clean, string(os.PathSeparator))
	if clean == "" {
		return 0
	}
	return len(strings.Split(clean, string(os.PathSeparator)))
}
