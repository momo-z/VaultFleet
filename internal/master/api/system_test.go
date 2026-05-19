package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"vaultfleet/internal/master/db"
)

type systemTestSetup struct {
	database *db.Database
	router   *gin.Engine
}

func setupSystemTestRouter(t *testing.T) systemTestSetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	router := gin.New()
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

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	user := db.User{
		Username:     "admin",
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

func assertContentDispositionBackupFilename(t *testing.T, value string) {
	t.Helper()

	require.NotEmpty(t, value)
	assert.Contains(t, strings.ToLower(value), "attachment")
	assert.Contains(t, value, "vaultfleet-backup-")
	assert.Contains(t, value, ".zip")
}
