package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
)

type systemTestSetup struct {
	database *db.Database
	router   *gin.Engine
}

func setupSystemTestRouter(t *testing.T) systemTestSetup {
	return setupSystemTestRouterWithMiddleware(t, nil)
}

func setupSystemTestRouterWithMiddleware(t *testing.T, middleware gin.HandlerFunc) systemTestSetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	router := gin.New()
	if middleware != nil {
		router.Use(middleware)
	}
	handler := NewSystemHandler(database)
	RegisterSystemRoutes(router.Group("/api/system"), handler)

	return systemTestSetup{
		database: database,
		router:   router,
	}
}

func TestExportEndpoint(t *testing.T) {
	setup := setupSystemTestRouter(t)
	createSystemTestAdmin(t, setup.database, "secret123")

	req := httptest.NewRequest(http.MethodGet, "/api/system/export", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
	assertContentDispositionBackupFilename(t, w.Header().Get("Content-Disposition"))
	assert.NotZero(t, w.Body.Len())

	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	require.NoError(t, err)
	assert.NotEmpty(t, reader.File)
}

func TestExportEndpoint_CheckpointsSQLiteBeforeExport(t *testing.T) {
	setup := setupSystemTestRouter(t)
	createSystemTestAdmin(t, setup.database, "secret123")

	req := httptest.NewRequest(http.MethodGet, "/api/system/export", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	extractedDir := t.TempDir()
	extractZipEntry(t, w.Body.Bytes(), "vaultfleet.db", filepath.Join(extractedDir, "vaultfleet.db"))

	exportedDB, err := gorm.Open(sqlite.Open(filepath.Join(extractedDir, "vaultfleet.db")), &gorm.Config{})
	require.NoError(t, err)

	var count int64
	require.NoError(t, exportedDB.Model(&db.User{}).Where("username = ?", "admin").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestChangePassword(t *testing.T) {
	setup := setupSystemTestRouter(t)
	createSystemTestAdmin(t, setup.database, "secret123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var user db.User
	require.NoError(t, setup.database.DB.First(&user, "username = ?", "admin").Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("newsecret123")))
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("secret123")))
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	setup := setupSystemTestRouter(t)
	createSystemTestAdmin(t, setup.database, "secret123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "wrong",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	var user db.User
	require.NoError(t, setup.database.DB.First(&user, "username = ?", "admin").Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("secret123")))
}

func TestChangePassword_UsesAuthenticatedUserID(t *testing.T) {
	var owner db.User
	setup := setupSystemTestRouterWithMiddleware(t, func(c *gin.Context) {
		c.Set("user_id", owner.ID)
		c.Next()
	})
	owner = createSystemTestUser(t, setup.database, "11111111-1111-1111-1111-111111111111", "owner", "secret123")
	other := createSystemTestUser(t, setup.database, "22222222-2222-2222-2222-222222222222", "operator", "operator123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var storedOwner db.User
	require.NoError(t, setup.database.DB.First(&storedOwner, "id = ?", owner.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOwner.PasswordHash), []byte("newsecret123")))

	var storedOther db.User
	require.NoError(t, setup.database.DB.First(&storedOther, "id = ?", other.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOther.PasswordHash), []byte("operator123")))
}

func TestChangePassword_UsesAuthenticatedUsername(t *testing.T) {
	const ownerUsername = "owner"
	setup := setupSystemTestRouterWithMiddleware(t, func(c *gin.Context) {
		c.Set("username", ownerUsername)
		c.Next()
	})
	owner := createSystemTestUser(t, setup.database, "", "owner", "secret123")
	other := createSystemTestUser(t, setup.database, "22222222-2222-2222-2222-222222222222", "operator", "operator123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var storedOwner db.User
	require.NoError(t, setup.database.DB.First(&storedOwner, "id = ?", owner.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOwner.PasswordHash), []byte("newsecret123")))

	var storedOther db.User
	require.NoError(t, setup.database.DB.First(&storedOther, "id = ?", other.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOther.PasswordHash), []byte("operator123")))
}

func TestChangePassword_UpdatesAuthenticatedUserWhenMultipleUsersExist(t *testing.T) {
	var owner db.User
	setup := setupSystemTestRouterWithMiddleware(t, func(c *gin.Context) {
		c.Set("user_id", owner.ID)
		c.Set("username", owner.Username)
		c.Next()
	})
	owner = createSystemTestUser(t, setup.database, "11111111-1111-1111-1111-111111111111", "owner", "secret123")
	other := createSystemTestUser(t, setup.database, "22222222-2222-2222-2222-222222222222", "operator", "operator123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var storedOwner db.User
	require.NoError(t, setup.database.DB.First(&storedOwner, "id = ?", owner.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOwner.PasswordHash), []byte("newsecret123")))

	var storedOther db.User
	require.NoError(t, setup.database.DB.First(&storedOther, "id = ?", other.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOther.PasswordHash), []byte("operator123")))
}

func TestChangePassword_NoAuthenticatedUserWithMultipleUsersReturnsBadRequest(t *testing.T) {
	setup := setupSystemTestRouter(t)
	other := createSystemTestUser(t, setup.database, "00000000-0000-0000-0000-000000000001", "operator", "operator123")
	admin := createSystemTestUser(t, setup.database, "ffffffff-ffff-ffff-ffff-ffffffffffff", "admin", "secret123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "newsecret123",
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var storedAdmin db.User
	require.NoError(t, setup.database.DB.First(&storedAdmin, "id = ?", admin.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedAdmin.PasswordHash), []byte("secret123")))

	var storedOther db.User
	require.NoError(t, setup.database.DB.First(&storedOther, "id = ?", other.ID).Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedOther.PasswordHash), []byte("operator123")))
}

func TestChangePassword_TooShort(t *testing.T) {
	setup := setupSystemTestRouter(t)
	createSystemTestAdmin(t, setup.database, "secret123")

	w := putSystemJSON(t, setup.router, "/api/system/password", map[string]string{
		"current_password": "secret123",
		"new_password":     "short",
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var user db.User
	require.NoError(t, setup.database.DB.First(&user, "username = ?", "admin").Error)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("secret123")))
}

func createSystemTestAdmin(t *testing.T, database *db.Database, password string) db.User {
	t.Helper()

	return createSystemTestUser(t, database, "", "admin", password)
}

func createSystemTestUser(t *testing.T, database *db.Database, id, username, password string) db.User {
	t.Helper()

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	user := db.User{
		ID:           id,
		Username:     username,
		PasswordHash: string(passwordHash),
	}
	require.NoError(t, database.DB.Create(&user).Error)
	return user
}

func putSystemJSON(t *testing.T, router http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func extractZipEntry(t *testing.T, zipBytes []byte, entryName, destination string) {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)

	for _, file := range reader.File {
		if file.Name != entryName {
			continue
		}

		rc, err := file.Open()
		require.NoError(t, err)
		defer rc.Close()

		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(destination, data, 0644))
		return
	}

	t.Fatalf("zip entry %q not found", entryName)
}

func assertContentDispositionBackupFilename(t *testing.T, value string) {
	t.Helper()

	require.NotEmpty(t, value)
	assert.Contains(t, strings.ToLower(value), "attachment")
	assert.Contains(t, value, "vaultfleet-backup-")
	assert.Contains(t, value, ".zip")
}
