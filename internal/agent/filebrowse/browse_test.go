package filebrowse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowseDepthLimit(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, filepath.Join(root, "home"), 1)

	require.NoError(t, err)
	assertContainsEntry(t, entries, filepath.Join(root, "home", "app"), EntryTypeDir)
	assertNotContainsPath(t, entries, filepath.Join(root, "home", "app", "config"))
	assertNotContainsPath(t, entries, filepath.Join(root, "home", "app", "config", "settings.json"))
}

func TestBrowseExcludedTopLevelDirs(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, root, 3)

	require.NoError(t, err)
	assertContainsEntry(t, entries, filepath.Join(root, "home"), EntryTypeDir)
	for _, excluded := range []string{"proc", "sys", "dev", "run", "tmp", "snap"} {
		assertNotContainsPath(t, entries, filepath.Join(root, excluded))
		assertNotContainsPath(t, entries, filepath.Join(root, excluded, "hidden.txt"))
	}
}

func TestBrowseSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions are not guaranteed on Windows")
	}
	root := setupBrowseTree(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "etc", "nginx.conf"), filepath.Join(root, "etc", "nginx-link.conf")))
	require.NoError(t, os.Symlink(filepath.Join(root, "home", "app"), filepath.Join(root, "app-link")))

	entries, err := Browse(root, root, 3)

	require.NoError(t, err)
	assertNotContainsPath(t, entries, filepath.Join(root, "etc", "nginx-link.conf"))
	assertNotContainsPath(t, entries, filepath.Join(root, "app-link"))
	assertNotContainsPath(t, entries, filepath.Join(root, "app-link", "config"))
}

func TestBrowseCompletesSmallTree(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, filepath.Join(root, "etc"), 3)

	require.NoError(t, err)
	assert.NotEmpty(t, entries)
	assertContainsEntry(t, entries, filepath.Join(root, "etc", "nginx.conf"), EntryTypeFile)
}

func TestBrowseReturnsErrorForMissingScanRoot(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, filepath.Join(root, "missing"), 2)

	require.Error(t, err)
	assert.Empty(t, entries)
}

func TestBrowseReturnsErrorForFileScanRoot(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, filepath.Join(root, "etc", "nginx.conf"), 2)

	require.Error(t, err)
	assert.Empty(t, entries)
}

func TestBrowseRejectsScanPathOutsideRoot(t *testing.T) {
	root := setupBrowseTree(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0644))

	entries, err := Browse(root, outside, 2)

	require.Error(t, err)
	assert.Empty(t, entries)
	assertNotContainsPath(t, entries, filepath.Join(outside, "outside.txt"))
}

func TestBrowseRejectsScanPathThroughSymlinkAncestorOutsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions are not guaranteed on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(outside, "secret"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret", "leak.txt"), []byte("secret"), 0644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	entries, err := Browse(root, filepath.Join(root, "link", "secret"), 2)

	require.Error(t, err)
	require.Empty(t, entries)
}

func TestBrowseResolvesRelativeScanPathUnderRoot(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, "etc", 2)

	require.NoError(t, err)
	assertContainsEntry(t, entries, filepath.Join(root, "etc", "nginx.conf"), EntryTypeFile)
}

func TestBrowseTimeout(t *testing.T) {
	root := setupBrowseTree(t)
	originalTimeout := browseTimeout
	browseTimeout = 0
	t.Cleanup(func() {
		browseTimeout = originalTimeout
	})

	_, err := Browse(root, root, 3)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestBrowseReturnsFileInfo(t *testing.T) {
	root := setupBrowseTree(t)

	entries, err := Browse(root, filepath.Join(root, "etc"), 3)

	require.NoError(t, err)
	entry := findEntry(t, entries, filepath.Join(root, "etc", "nginx.conf"))
	assert.Equal(t, EntryTypeFile, entry.Type)
	assert.Equal(t, int64(len("server {}\n")), entry.Size)
}

func setupBrowseTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for _, dir := range []string{
		"etc",
		"home/app/config",
		"proc",
		"sys",
		"dev",
		"run",
		"tmp",
		"snap",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "nginx.conf"), []byte("server {}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "home", "app", "config", "settings.json"), []byte("{}"), 0644))
	for _, excluded := range []string{"proc", "sys", "dev", "run", "tmp", "snap"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, excluded, "hidden.txt"), []byte("hidden"), 0644))
	}
	return root
}

func assertContainsEntry(t *testing.T, entries []DirEntry, wantPath string, wantType string) {
	t.Helper()

	entry := findEntry(t, entries, wantPath)
	assert.Equal(t, wantType, entry.Type)
}

func assertNotContainsPath(t *testing.T, entries []DirEntry, blockedPath string) {
	t.Helper()

	cleanBlocked := filepath.Clean(blockedPath)
	for _, entry := range entries {
		cleanEntry := filepath.Clean(entry.Path)
		if cleanEntry == cleanBlocked || strings.HasPrefix(cleanEntry, cleanBlocked+string(os.PathSeparator)) {
			t.Fatalf("unexpected path %q in entries: %#v", blockedPath, entries)
		}
	}
}

func findEntry(t *testing.T, entries []DirEntry, wantPath string) DirEntry {
	t.Helper()

	cleanWant := filepath.Clean(wantPath)
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == cleanWant {
			return entry
		}
	}
	t.Fatalf("entry %q not found in %#v", wantPath, entries)
	return DirEntry{}
}
