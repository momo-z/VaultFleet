package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"vaultfleet/internal/master/backup"
	"vaultfleet/internal/master/db"
)

type SystemHandler struct {
	DB *db.Database
}

func NewSystemHandler(database *db.Database) *SystemHandler {
	return &SystemHandler{DB: database}
}

func RegisterSystemRoutes(rg *gin.RouterGroup, h *SystemHandler) {
	rg.GET("/export", h.Export)
	rg.PUT("/password", h.ChangePassword)
}

func (h *SystemHandler) Export(c *gin.Context) {
	if err := h.DB.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "checkpoint database"})
		return
	}

	buf, err := backup.ExportDataDir(h.DB.DataDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export data"})
		return
	}

	filename := "vaultfleet-backup-" + time.Now().Format("20060102-150405") + ".zip"
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=6"`
}

func (h *SystemHandler) ChangePassword(c *gin.Context) {
	var request changePasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	var user db.User
	if err := h.findPasswordChangeUser(c, &user); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		if errors.Is(err, errPasswordUserAmbiguous) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "authenticated user required"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.CurrentPassword)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid current password"})
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password hashing failed"})
		return
	}

	user.PasswordHash = string(passwordHash)
	if err := h.DB.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

var errPasswordUserAmbiguous = errors.New("password change user ambiguous")

func (h *SystemHandler) findPasswordChangeUser(c *gin.Context, user *db.User) error {
	if userID := c.GetString("user_id"); userID != "" {
		return h.DB.DB.First(user, "id = ?", userID).Error
	}
	if username := c.GetString("username"); username != "" {
		return h.DB.DB.First(user, "username = ?", username).Error
	}

	var count int64
	if err := h.DB.DB.Model(&db.User{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errPasswordUserAmbiguous
	}
	return h.DB.DB.First(user).Error
}
