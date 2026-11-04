package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"smarthome-hub/hub"
	"strconv"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// findUIDir locates the built Vue SPA. UI_DIR wins; otherwise try a few
// locations relative to the working directory and the executable.
func findUIDir() string {
	if d := os.Getenv("UI_DIR"); d != "" {
		return d
	}
	candidates := []string{"../smarthome-ui/dist", "ui/dist", "dist"}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(base, "../smarthome-ui/dist"), filepath.Join(base, "../../smarthome-ui/dist"), filepath.Join(base, "ui"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
			return c
		}
	}
	return candidates[0]
}

func corsOrigins() []string {
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		var out []string
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				out = append(out, o)
			}
		}
		return out
	}
	// Same-origin SPA needs no CORS; allow the Vite dev server by default.
	return []string{"http://localhost:5173", "http://127.0.0.1:5173"}
}

func main() {
	godotenv.Load() // must run before anything reads the environment

	// Initialize MariaDB
	hub.InitDatabase()
	hub.LoadPendingProvisions()

	// Fire up the TCP listener for the WiFi gateway as a background Goroutine
	go hub.StartTCPWorker(":" + env("TCP_PORT", "9010"))

	// Start the online status monitor goroutine
	hub.StartOnlineMonitor()

	// Set up the Web REST API using Gin
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins(),
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Device-Key"},
		AllowCredentials: false,
	}))

	// ── Serve Vue SPA static files ───────────────────────────────────────────
	uiDir := findUIDir()
	log.Printf("Serving UI from %s", uiDir)
	r.Static("/assets", filepath.Join(uiDir, "assets"))
	r.StaticFile("/favicon.ico", filepath.Join(uiDir, "favicon.ico"))

	// ── API routes ────────────────────────────────────────────────────────────

	auth := r.Group("/api/auth")
	{
		auth.POST("/register", hub.Register)
		auth.POST("/login", hub.Login)
		auth.POST("/refresh", hub.RefreshToken)
		auth.POST("/mfa/challenge", hub.MfaLoginChallenge) // step 2 of sign-in: send a number to the phone
		auth.POST("/mfa/poll", hub.MfaLoginPoll)           // the website waits for approval on the phone
		auth.POST("/mfa/verify", hub.MfaLoginVerify)       // authenticator / email / recovery code
	}

	// ── Pairing routes (HMI request/verify are open but code-limited; code lookup needs login) ─

	pair := r.Group("/api/pair")
	{
		pair.POST("/request", hub.HandlePairRequest)
		pair.POST("/verify", hub.HandlePairVerify)
	}

	// ── HMI routes (require the token issued by /api/pair/verify) ───────────
	r.GET("/api/hmi/gateways", hub.HMIListGateways)
	r.POST("/api/hmi/register", hub.HandleHMIRegister)             // display asks for a pairing code
	r.GET("/api/hmi/register/status", hub.HandleHMIRegisterStatus) // ...and collects its token once claimed // needed before pairing (gateway scan)
	hmi := r.Group("/api/hmi")
	hmi.Use(hub.HMIAuthMiddleware())
	{
		hmi.GET("/gateway", hub.HMIGetGateway)
		hmi.POST("/unpair", hub.HMIUnpair)
		hmi.GET("/gateways/:id/all-nodes", hub.HMIListAllNodes)
		hmi.GET("/gateways/:id/nodes", hub.HMIListNodes)
		hmi.GET("/nodes/:deviceId", hub.HMIGetNodeDetail)
		hmi.POST("/nodes/:deviceId/command", hub.HMIControlNode)
	}

	// ── Authenticated user routes ────────────────────────────────────────────

	api := r.Group("/api")
	api.Use(hub.AuthMiddleware())
	{
		api.GET("/pair/code/:gatewayId", hub.HandleGetPairCode)

		// Displays (HMIs)
		api.POST("/hmis/claim", hub.HandleClaimHMI)
		api.GET("/hmis", hub.ListHmis)
		api.GET("/hmis/:id", hub.GetHmi)
		api.PUT("/hmis/:id", hub.RenameHmi)
		api.PUT("/hmis/:id/appearance", hub.SetHmiAppearance)
		api.GET("/audit", hub.ListAudit)
		api.DELETE("/hmis/:id", hub.DeleteHmi)
		api.PUT("/hmis/:id/nodes", hub.SetHmiNodes)
		api.PUT("/nodes/:nodeId/hmi", hub.AssignNodeHmi)
		api.GET("/auth/me", hub.Me)

		// Profile & security
		api.GET("/profile", hub.GetProfile)
		api.PUT("/profile", hub.UpdateProfile)
		api.POST("/profile/password", hub.ChangePassword)
		api.GET("/sessions", hub.ListSessions)
		api.DELETE("/sessions/:id", hub.RevokeSession)
		api.POST("/sessions/revoke-others", hub.RevokeOtherSessions)
		api.GET("/mfa/devices", hub.MfaListDevices)
		api.POST("/mfa/devices", hub.MfaRegisterDevice)
		api.DELETE("/mfa/devices/:id", hub.MfaDeleteDevice)
		api.GET("/mfa/pending", hub.MfaPending)
		api.POST("/mfa/challenges/:id/approve", hub.MfaApprove)
		api.POST("/mfa/challenges/:id/deny", hub.MfaDeny)
		api.POST("/mfa/enable/start", hub.MfaEnableStart)
		api.POST("/mfa/enable/confirm", hub.MfaEnableConfirm)
		api.POST("/mfa/totp/setup", hub.MfaTotpSetup)
		api.POST("/mfa/totp/confirm", hub.MfaTotpConfirm)
		api.POST("/mfa/email/start", hub.MfaEmailStart)
		api.POST("/mfa/email/confirm", hub.MfaEmailConfirm)
		api.POST("/mfa/methods/:method/disable", hub.MfaMethodDisable)
		api.POST("/mfa/disable", hub.MfaDisable)
		api.POST("/mfa/recovery", hub.MfaRegenerateRecovery)
		// Dashboard
		api.GET("/stats", hub.GetDashboardStats)
		// Gateways
		api.GET("/gateways", hub.ListGateways)
		api.POST("/gateways", hub.CreateGateway)
		api.GET("/gateways/:id", hub.GetGateway)
		api.PUT("/gateways/:id", hub.UpdateGateway)
		api.DELETE("/gateways/:id", hub.DeleteGateway)
		api.GET("/gateways/:id/api-key", hub.GetGatewayAPIKey)
		api.GET("/gateways/:id/nodes", hub.ListNodes)

		// WiFi credentials
		api.GET("/gateways/:id/wifi", hub.ListWifiCredentials)
		api.POST("/gateways/:id/wifi", hub.SaveWifiCredential)
		api.PUT("/gateways/:id/wifi/order", hub.ReorderWifiCredentials)
		api.DELETE("/gateways/:id/wifi/:credId", hub.DeleteWifiCredential)

		// Capability bindings (input → output forwarding)
		api.GET("/gateways/:id/bindings", hub.ListBindings)
		api.POST("/gateways/:id/bindings", hub.CreateBinding)
		api.DELETE("/bindings/:bindingId", hub.DeleteBinding)

		// Node discovery & provisioning
		api.POST("/gateways/:id/scan", hub.StartScan)
		api.GET("/gateways/:id/discovered", hub.GetDiscoveredNodes)
		api.POST("/gateways/:id/provision", hub.ProvisionNode)

		// Node disconnect
		api.DELETE("/gateways/:id/nodes/:deviceId", hub.DisconnectNode)

		// Cross-gateway node discovery
		api.POST("/nodes/scan", hub.ScanAllGateways)
		api.GET("/nodes/discovered", hub.GetAllDiscoveredNodesHandler)
		api.POST("/nodes/provision", hub.ProvisionNodeToGateway)

		// Node detail (by hex nodeId)
		api.GET("/nodes/:nodeId", hub.GetNode)
		api.GET("/nodes/by-device/:deviceId", hub.GetNodeByDeviceID)

		// Send command to node (by hex nodeId)
		api.POST("/nodes/:nodeId/command", hub.ControlNode)

		// Pending actions (queued gateway deletion when offline)
		api.GET("/pending-actions", hub.ListPendingActions)
		api.DELETE("/pending-actions/:actionId", hub.DeletePendingAction)

		// Update node capabilities
		api.PUT("/nodes/:nodeId/capabilities", hub.UpdateNodeCapabilities)

		// Device ping (updates LastSeen)
		api.POST("/devices/:id/ping", hub.PingDevice)

		// Real-time device registry (from TCP mesh)
		api.GET("/devices", func(c *gin.Context) {
			c.JSON(http.StatusOK, hub.UserRegistry(c.GetUint("userID")))
		})

		// Send command to a device via the connected gateway
		api.POST("/device/:id/control", func(c *gin.Context) {
			idStr := c.Param("id")
			stateStr := c.Query("state")

			deviceId, err := strconv.ParseUint(idStr, 10, 32)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Device ID format"})
				return
			}

			state, err := strconv.ParseUint(stateStr, 10, 16)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state command value"})
				return
			}

			if state == hub.ResetCommandValue {
				c.JSON(http.StatusBadRequest, gin.H{"error": "state 99 is reserved"})
				return
			}
			gwID, ok := hub.UserOwnedDeviceGateway(c.GetUint("userID"), uint32(deviceId))
			if !ok {
				c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
				return
			}
			err = hub.SendCommandToGateway(gwID, uint32(deviceId), 2, uint16(state))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to pass packet to gateway: " + err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"status":    "transmitted",
				"deviceId":  deviceId,
				"cmd_value": state,
			})
		})
	}

	// ── SPA catch-all: serve index.html for any unmatched route ─────────────
	r.NoRoute(func(c *gin.Context) {
		// Only return the SPA for non-API paths
		if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.File(filepath.Join(uiDir, "index.html"))
	})

	// Start serving HTTP requests
	if err := r.Run(":" + env("SERVER_PORT", "9000")); err != nil {
		log.Fatalf("HTTP server: %v", err)
	}
}
