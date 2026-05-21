package backup

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const MaxImportSize = 100 << 20 // 100 MB

type ValidationResult struct {
	Valid  bool     `json:"valid"`
	Files  []string `json:"files"`
	Errors []string `json:"errors"`
}

var requiredFiles = []string{"vaultfleet.db", "master.key"}

func ValidateBackupZip(data []byte) ValidationResult {
	result := ValidationResult{Valid: true}

	if len(data) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "备份文件为空")
		return result
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("无法解析 zip 文件: %v", err))
		return result
	}

	fileSet := make(map[string]bool)
	for _, f := range reader.File {
		name := filepath.ToSlash(f.Name)
		if name == "" || filepath.IsAbs(name) {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("不安全的路径: %q", f.Name))
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("不安全的路径: %q", f.Name))
			continue
		}
		if clean == "rollback" || strings.HasPrefix(clean, "rollback"+string(filepath.Separator)) {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("包含保留路径: %q", f.Name))
			continue
		}
		fileSet[name] = true
		result.Files = append(result.Files, name)
	}

	for _, req := range requiredFiles {
		if !fileSet[req] {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("缺少必需文件: %s", req))
		}
	}

	return result
}

func ValidateBackupFile(path string, maxSize int64) (ValidationResult, error) {
	if maxSize <= 0 {
		maxSize = MaxImportSize
	}

	info, err := os.Stat(path)
	if err != nil {
		return ValidationResult{}, err
	}
	if info.Size() > maxSize {
		return ValidationResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("文件大小 %d 字节超过限制 %d 字节", info.Size(), maxSize)},
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ValidationResult{}, err
	}

	return ValidateBackupZip(data), nil
}
