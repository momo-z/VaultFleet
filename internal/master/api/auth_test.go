package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
)

type testAuthSetup struct {
	database *db.Database
	handler  *AuthHandler
	router   *gin.Engine
}

func setupTestAuth(t *testing.T) testAuthSetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	handler := NewAuthHandler(database)
	router := gin.New()

	router.GET("/api/auth/check", handler.CheckInit)
	router.POST("/api/auth/init", handler.InitSetup)
	router.POST("/api/auth/login", handler.Login)

	protected := router.Group("/api")
	protected.Use(RequireInit(database), RequireAuth(handler.Sessions))
	protected.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"ok":   true,
			"user": c.GetString("username"),
		})
	})

	return testAuthSetup{
		database: database,
		handler:  handler,
		router:   router,
	}
}

func postJSON(t *testing.T, router http.Handler, path string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func postJSONNoT(router http.Handler, path string, body map[string]string) (*httptest.ResponseRecorder, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w, nil
}

func parseJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func getSessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "session" {
			return cookie
		}
	}
	t.Fatalf("session cookie not found in response cookies: %v", w.Result().Cookies())
	return nil
}

func assertSessionCookieAttributes(t *testing.T, w *httptest.ResponseRecorder, cookie *http.Cookie) {
	t.Helper()

	assert.Equal(t, "session", cookie.Name)
	assert.True(t, strings.HasPrefix(cookie.Value, "ss_"))
	assert.False(t, cookie.Secure)

	setCookie := w.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, "Path=/")
	assert.Contains(t, setCookie, "Max-Age=604800")
	assert.Contains(t, setCookie, "HttpOnly")
	assert.Contains(t, setCookie, "SameSite=Lax")
	assert.NotContains(t, setCookie, "Secure")
}

func TestSessionCookie_SecureWhenForwardedProtoHTTPS(t *testing.T) {
	setup := setupTestAuth(t)

	reqBody, err := json.Marshal(map[string]string{
		"username": "admin",
		"password": "secret123",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/init", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	cookie := getSessionCookie(t, w)
	assert.Equal(t, "session", cookie.Name)
	assert.True(t, strings.HasPrefix(cookie.Value, "ss_"))

	setCookie := w.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, "Path=/")
	assert.Contains(t, setCookie, "Max-Age=604800")
	assert.Contains(t, setCookie, "HttpOnly")
	assert.Contains(t, setCookie, "SameSite=Lax")
	assert.Contains(t, setCookie, "Secure")
}

func initAdmin(t *testing.T, router http.Handler) *httptest.ResponseRecorder {
	t.Helper()

	return postJSON(t, router, "/api/auth/init", map[string]string{
		"username": "admin",
		"password": "secret123",
	})
}

func TestCheckInit_NoUsers(t *testing.T) {
	setup := setupTestAuth(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := body["data"].(map[string]any)
	assert.Equal(t, false, data["initialized"])
}

func TestCheckInit_WithUsers(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
	w = httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	data := body["data"].(map[string]any)
	assert.Equal(t, true, data["initialized"])
}

func TestInitSetup(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)

	require.Equal(t, http.StatusOK, w.Code)
	cookie := getSessionCookie(t, w)
	assertSessionCookieAttributes(t, w, cookie)

	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "admin", data["username"])

	var user db.User
	require.NoError(t, setup.database.DB.First(&user, "username = ?", "admin").Error)
	assert.NotEmpty(t, user.PasswordHash)
	assert.NotEqual(t, "secret123", user.PasswordHash)
}

func TestInitSetup_BlockedAfterFirstUser(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	w = postJSON(t, setup.router, "/api/auth/init", map[string]string{
		"username": "other",
		"password": "secret123",
	})

	require.Equal(t, http.StatusBadRequest, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "system already initialized", body["error"])
}

func TestInitSetup_ConcurrentFirstAdminAtomic(t *testing.T) {
	setup := setupTestAuth(t)

	usernames := []string{"admin-one", "admin-two"}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, len(usernames))
	errs := make(chan error, len(usernames))
	var wg sync.WaitGroup

	for _, username := range usernames {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			<-start
			w, err := postJSONNoT(setup.router, "/api/auth/init", map[string]string{
				"username": username,
				"password": "secret123",
			})
			if err != nil {
				errs <- err
				return
			}
			responses <- w
		}(username)
	}

	close(start)
	wg.Wait()
	close(responses)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	successes := 0
	blocked := 0
	for w := range responses {
		switch w.Code {
		case http.StatusOK:
			successes++
		case http.StatusBadRequest:
			body := parseJSON(t, w)
			assert.Equal(t, "system already initialized", body["error"])
			blocked++
		default:
			t.Fatalf("unexpected status %d with body %s", w.Code, w.Body.String())
		}
	}

	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, blocked)

	var count int64
	require.NoError(t, setup.database.DB.Model(&db.User{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestInitSetup_PasswordTooShort(t *testing.T) {
	setup := setupTestAuth(t)

	w := postJSON(t, setup.router, "/api/auth/init", map[string]string{
		"username": "admin",
		"password": "short",
	})

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestLogin(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	w = postJSON(t, setup.router, "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "secret123",
	})

	require.Equal(t, http.StatusOK, w.Code)
	cookie := getSessionCookie(t, w)
	assertSessionCookieAttributes(t, w, cookie)

	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "admin", data["username"])
}

func TestLogin_InvalidPassword(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	w = postJSON(t, setup.router, "/api/auth/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	})

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "invalid credentials", body["error"])
}

func TestLogin_NonexistentUser(t *testing.T) {
	setup := setupTestAuth(t)

	w := postJSON(t, setup.router, "/api/auth/login", map[string]string{
		"username": "missing",
		"password": "secret123",
	})

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "invalid credentials", body["error"])
}

func TestRequireAuth_ValidSession(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)
	cookie := getSessionCookie(t, w)

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	assert.Equal(t, "admin", body["user"])
}

func TestRequireAuth_NoSession(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	w = httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "unauthorized", body["error"])
}

func TestRequireAuth_InvalidSession(t *testing.T) {
	setup := setupTestAuth(t)

	w := initAdmin(t, setup.router)
	require.Equal(t, http.StatusOK, w.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "ss_invalid"})
	w = httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "unauthorized", body["error"])
}

func TestRequireAuth_ExpiredSession(t *testing.T) {
	setup := setupTestAuth(t)

	user := db.User{Username: "admin", PasswordHash: "hash"}
	require.NoError(t, setup.database.DB.Create(&user).Error)

	token := setup.handler.Sessions.Create(&Session{
		UserID:   user.ID,
		Username: user.Username,
		CreateAt: time.Now().Add(-(time.Duration(sessionMaxAge)*time.Second + time.Second)),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "unauthorized", body["error"])

	_, ok := setup.handler.Sessions.Get(token)
	assert.False(t, ok)
}

func TestRequireInit_NoUsers(t *testing.T) {
	setup := setupTestAuth(t)

	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "init_required", body["error"])
}

func TestSessionStore_CreateAndGet(t *testing.T) {
	store := NewSessionStore()
	session := &Session{
		UserID:   "user-1",
		Username: "admin",
	}

	token := store.Create(session)

	assert.True(t, strings.HasPrefix(token, "ss_"))
	got, ok := store.Get(token)
	require.True(t, ok)
	assert.Equal(t, session.UserID, got.UserID)
	assert.Equal(t, session.Username, got.Username)
	assert.False(t, got.CreateAt.IsZero())
}

func TestSessionStore_GetExpiredDeletesSession(t *testing.T) {
	store := NewSessionStore()
	token := store.Create(&Session{
		UserID:   "user-1",
		Username: "admin",
		CreateAt: time.Now().Add(-(time.Duration(sessionMaxAge)*time.Second + time.Second)),
	})

	_, ok := store.Get(token)
	assert.False(t, ok)

	_, ok = store.Get(token)
	assert.False(t, ok)
}

func TestSessionStore_GetReturnsCopy(t *testing.T) {
	store := NewSessionStore()
	token := store.Create(&Session{
		UserID:   "user-1",
		Username: "admin",
	})

	got, ok := store.Get(token)
	require.True(t, ok)
	got.Username = "mutated"

	got, ok = store.Get(token)
	require.True(t, ok)
	assert.Equal(t, "admin", got.Username)
}

func TestSessionStore_Delete(t *testing.T) {
	store := NewSessionStore()
	token := store.Create(&Session{
		UserID:   "user-1",
		Username: "admin",
	})

	store.Delete(token)

	_, ok := store.Get(token)
	assert.False(t, ok)
}

func TestSessionStore_GetNonexistent(t *testing.T) {
	store := NewSessionStore()

	_, ok := store.Get("ss_missing")

	assert.False(t, ok)
}
