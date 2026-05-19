package backup

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExportDataDir creates a zip archive containing dataDir files.
func ExportDataDir(dataDir string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)

	if err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if path == dataDir {
			return nil
		}

		relPath, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		zipName := filepath.ToSlash(relPath)

		if entry.IsDir() {
			if zipName == "rollback" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeType != 0 {
			return nil
		}
		if zipName == "backup.zip" {
			return nil
		}

		if err := addFileToZip(archive, path, zipName); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("export data dir %s: %w", dataDir, err)
	}

	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close export zip: %w", err)
	}

	return &buf, nil
}

func addFileToZip(archive *zip.Writer, path, zipName string) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	header, err := zip.FileInfoHeader(fileInfo)
	if err != nil {
		return fmt.Errorf("create zip header for %s: %w", path, err)
	}
	header.Name = zipName
	header.Method = zip.Deflate

	writer, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", zipName, err)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("write zip entry %s: %w", zipName, err)
	}
	return nil
}
