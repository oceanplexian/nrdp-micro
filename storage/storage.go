package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"nrdp_micro/logger"
)

// Manager handles storage-related operations
type Manager struct {
	outputDir     string
	maxFiles      int
	minDiskSpace  float64
}

// NewManager creates a new storage manager
func NewManager(outputDir string, maxFiles int, minDiskSpace float64) *Manager {
	return &Manager{
		outputDir:    outputDir,
		maxFiles:     maxFiles,
		minDiskSpace: minDiskSpace,
	}
}

// CheckSpace checks if there's enough disk space
func (m *Manager) CheckSpace() error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(m.outputDir, &stat); err != nil {
		return fmt.Errorf("failed to get filesystem stats: %v", err)
	}

	// Calculate free space percentage
	totalSpace := float64(stat.Blocks) * float64(stat.Bsize)
	freeSpace := float64(stat.Bavail) * float64(stat.Bsize)
	freePercent := (freeSpace / totalSpace) * 100

	if freePercent < m.minDiskSpace {
		return fmt.Errorf("insufficient disk space: %.1f%% free, minimum required: %.1f%%", 
			freePercent, m.minDiskSpace)
	}

	logger.Logf(logger.LevelDebug, "storage: disk space check passed: %.1f%% free", freePercent)
	return nil
}

// CheckFiles checks if there are too many files in the directory
func (m *Manager) CheckFiles() (bool, error) {
	dir, err := os.Open(m.outputDir)
	if err != nil {
		return false, fmt.Errorf("failed to read directory: %v", err)
	}
	defer dir.Close()

	entries, err := dir.ReadDir(0)
	if err != nil {
		return false, fmt.Errorf("failed to read directory entries: %v", err)
	}

	// Only count regular files
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			count++
		}
	}

	if count >= m.maxFiles {
		logger.Logf(logger.LevelDebug, "storage: too many files: %d (max: %d)", count, m.maxFiles)
		return true, nil
	}

	logger.Logf(logger.LevelDebug, "storage: file count check passed: %d files", count)
	return false, nil
}

// EnsureWritable checks if the directory is writable
func (m *Manager) EnsureWritable() error {
	// Try to create a temporary file
	testFile := filepath.Join(m.outputDir, ".write_test")
	if err := os.WriteFile(testFile, []byte{}, 0644); err != nil {
		return fmt.Errorf("directory is not writable: %v", err)
	}
	os.Remove(testFile)
	return nil
}

// GetStats returns storage statistics
func (m *Manager) GetStats() (map[string]interface{}, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(m.outputDir, &stat); err != nil {
		return nil, fmt.Errorf("failed to get filesystem stats: %v", err)
	}

	dir, err := os.Open(m.outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %v", err)
	}
	defer dir.Close()

	entries, err := dir.ReadDir(0)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory entries: %v", err)
	}

	// Only count regular files
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			count++
		}
	}

	totalSpace := float64(stat.Blocks) * float64(stat.Bsize)
	freeSpace := float64(stat.Bavail) * float64(stat.Bsize)
	freePercent := (freeSpace / totalSpace) * 100

	return map[string]interface{}{
		"total_space_bytes": totalSpace,
		"free_space_bytes": freeSpace,
		"free_space_percent": freePercent,
		"file_count": count,
		"max_files": m.maxFiles,
	}, nil
} 