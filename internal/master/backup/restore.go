package backup

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckAndRestore restores dataDir from dataDir/backup.zip when it exists.
func CheckAndRestore(dataDir string) (bool, error) {
	backupPath := filepath.Join(dataDir, "backup.zip")
	if _, err := os.Stat(backupPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat backup zip %s: %w", backupPath, err)
	}

	rollbackPath, err := createRollback(dataDir)
	if err != nil {
		return false, err
	}

	if err := extractZip(backupPath, dataDir); err != nil {
		return false, fmt.Errorf("restore backup zip %s after rollback %s: %w", backupPath, rollbackPath, err)
	}

	if err := os.Remove(backupPath); err != nil {
		return false, fmt.Errorf("remove backup zip %s: %w", backupPath, err)
	}

	return true, nil
}

func createRollback(dataDir string) (string, error) {
	rollbackDir := filepath.Join(dataDir, "rollback")
	if err := os.MkdirAll(rollbackDir, 0755); err != nil {
		return "", fmt.Errorf("create rollback dir %s: %w", rollbackDir, err)
	}

	rollbackZip, err := ExportDataDir(dataDir)
	if err != nil {
		return "", fmt.Errorf("create rollback archive: %w", err)
	}

	rollbackPath := filepath.Join(rollbackDir, time.Now().Format("20060102-150405")+".zip")
	if err := os.WriteFile(rollbackPath, rollbackZip.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write rollback zip %s: %w", rollbackPath, err)
	}
	return rollbackPath, nil
}

func extractZip(zipPath, dataDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer reader.Close()

	cleanDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("absolute data dir %s: %w", dataDir, err)
	}

	for _, file := range reader.File {
		targetPath, err := safeRestorePath(cleanDataDir, file.Name)
		if err != nil {
			return err
		}

		mode := file.Mode()
		if file.FileInfo().IsDir() {
			perm := mode.Perm()
			if perm == 0 {
				perm = 0755
			}
			if err := os.MkdirAll(targetPath, perm); err != nil {
				return fmt.Errorf("create restored dir %s: %w", targetPath, err)
			}
			continue
		}
		if mode&os.ModeType != 0 {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("create restored parent dir %s: %w", filepath.Dir(targetPath), err)
		}
		if err := extractFile(file, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func safeRestorePath(dataDir, zipName string) (string, error) {
	if zipName == "" {
		return "", fmt.Errorf("invalid zip entry path %q", zipName)
	}
	if filepath.IsAbs(zipName) {
		return "", fmt.Errorf("unsafe zip entry path %q", zipName)
	}

	cleanName := filepath.Clean(filepath.FromSlash(zipName))
	if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe zip entry path %q", zipName)
	}

	targetPath := filepath.Join(dataDir, cleanName)
	rel, err := filepath.Rel(dataDir, targetPath)
	if err != nil {
		return "", fmt.Errorf("validate zip entry path %q: %w", zipName, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe zip entry path %q", zipName)
	}
	return targetPath, nil
}

func extractFile(file *zip.File, targetPath string) error {
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", file.Name, err)
	}
	defer rc.Close()

	mode := file.Mode().Perm()
	if mode == 0 {
		mode = 0644
	}
	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create restored file %s: %w", targetPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write restored file %s: %w", targetPath, err)
	}
	return nil
}
