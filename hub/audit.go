package hub

import (
	"github.com/gin-gonic/gin"
)

func LogAudit(c *gin.Context, action, entityType string, entityID *string, details string) {
	userID := c.GetUint("userID")

	audit := AuditLog{
		UserID:     &userID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Details:    details,
		IPAddress:  c.ClientIP(),
	}

	DB.Create(&audit)
}
