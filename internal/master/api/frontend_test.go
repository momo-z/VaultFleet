package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrontendRootServesSPA(t *testing.T) {
	router := newFrontendPlaceholderTestRouter()

	w := getFrontendPlaceholder(t, router, "/")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), "VaultFleet")
	assert.Contains(t, w.Body.String(), "id=\"root\"")
}

func TestFrontendAcceptancePaths(t *testing.T) {
	router := newFrontendPlaceholderTestRouter()

	for _, path := range []string{"/", "/dashboard", "/nodes", "/nodes/agent-1", "/settings"} {
		t.Run(path, func(t *testing.T) {
			w := getFrontendPlaceholder(t, router, path)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), "VaultFleet")
		})
	}
}

func TestFrontendDoesNotServeBackendRoutes(t *testing.T) {
	router := newFrontendPlaceholderTestRouter()

	for _, path := range []string{"/api/missing", "/ws/missing"} {
		t.Run(path, func(t *testing.T) {
			w := getFrontendPlaceholder(t, router, path)

			require.Equal(t, http.StatusNotFound, w.Code)
			assert.NotContains(t, w.Header().Get("Content-Type"), "text/html")
		})
	}
}

func newFrontendPlaceholderTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	RegisterFrontendRoutes(router)
	return router
}

func getFrontendPlaceholder(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
