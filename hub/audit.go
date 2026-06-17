package hub

import (
	"github.com/gin-gonic/gin"
)

func LogAudit(c *gin.Context, action, entityType string, entityID *string, details string) {
	audit := AuditLog{
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Details:    details,
	}

	if c != nil {
		userID := c.GetUint("userID")
		audit.UserID = &userID
		audit.IPAddress = c.ClientIP()
	}

	DB.Create(&audit)
}
