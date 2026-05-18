package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
)

const (
	sessionCookieName  = "session"
	sessionTokenPrefix = "ss_"
	sessionMaxAge      = 7 * 24 * 60 * 60
)

var nowFunc = time.Now

var sessionTTL = time.Duration(sessionMaxAge) * time.Second

type Session struct {
	UserID   string
	Username string
	CreateAt time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
	}
}

func (s *SessionStore) Create(session *Session) string {
	token := generateToken(sessionTokenPrefix)
	s.store(token, session)
	return token
}

func (s *SessionStore) createWithError(session *Session) (string, error) {
	token, err := generateTokenWithError(sessionTokenPrefix)
	if err != nil {
		return "", err
	}

	s.store(token, session)
	return token, nil
}

func (s *SessionStore) store(token string, session *Session) {
	stored := *session
	if stored.CreateAt.IsZero() {
		stored.CreateAt = nowFunc()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[token] = &stored
}

func (s *SessionStore) Get(token string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[token]
	if !ok {
		return nil, false
	}

	if sessionExpired(session) {
		delete(s.sessions, token)
		return nil, false
	}

	clone := *session
	return &clone, true
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, token)
}

func generateToken(prefix string) string {
	token, err := generateTokenWithError(prefix)
	if err != nil {
		panic(fmt.Errorf("generate token: %w", err))
	}
	return token
}

func generateTokenWithError(prefix string) (string, error) {
	tokenBytes := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return "", err
	}

	return prefix + hex.EncodeToString(tokenBytes), nil
}

type AuthHandler struct {
	DB       *db.Database
	Sessions *SessionStore
	initMu   sync.Mutex
}

func NewAuthHandler(database *db.Database) *AuthHandler {
	return &AuthHandler{
		DB:       database,
		Sessions: NewSessionStore(),
	}
}

func (h *AuthHandler) CheckInit(c *gin.Context) {
	count, err := h.userCount()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok": true,
		"data": gin.H{
			"initialized": count > 0,
		},
	})
}

func (h *AuthHandler) InitSetup(c *gin.Context) {
	h.initMu.Lock()
	defer h.initMu.Unlock()

	count, err := h.userCount()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "system already initialized"})
		return
	}

	var request initRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid request"})
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "password hashing failed"})
		return
	}

	user := db.User{
		Username:     request.Username,
		PasswordHash: string(passwordHash),
	}
	if err := h.DB.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	if err := h.createSessionCookie(c, &user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "session creation failed"})
		return
	}
	c.JSON(http.StatusOK, authUserResponse(user.Username))
}

func (h *AuthHandler) Login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid request"})
		return
	}

	var user db.User
	if err := h.DB.DB.First(&user, "username = ?", request.Username).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "invalid credentials"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "invalid credentials"})
		return
	}

	if err := h.createSessionCookie(c, &user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "session creation failed"})
		return
	}
	c.JSON(http.StatusOK, authUserResponse(user.Username))
}

type initRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required,min=6"`
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) createSessionCookie(c *gin.Context, user *db.User) error {
	token, err := h.Sessions.createWithError(&Session{
		UserID:   user.ID,
		Username: user.Username,
	})
	if err != nil {
		return err
	}

	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   sessionMaxAge,
		Expires:  nowFunc().Add(sessionTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(c.Request),
	}
	http.SetCookie(c.Writer, cookie)
	return nil
}

func authUserResponse(username string) gin.H {
	return gin.H{
		"ok": true,
		"data": gin.H{
			"username": username,
		},
	}
}

func (h *AuthHandler) userCount() (int64, error) {
	var count int64
	if err := h.DB.DB.Model(&db.User{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func sessionExpired(session *Session) bool {
	if session == nil || session.CreateAt.IsZero() {
		return false
	}

	return nowFunc().Sub(session.CreateAt) >= sessionTTL
}

func requestIsSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
