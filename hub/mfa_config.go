package hub

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ── Two-step verification & email settings (all from the environment / .env) ──
//
//   MFA_EMAIL_CODE_TTL_MINUTES  how long an emailed code stays valid (default 10, 1-60)
//   SMTP_HOST / SMTP_PORT       mail server (email codes are disabled without a host)
//   SMTP_USER / SMTP_PASS       credentials (optional for an open relay / local sink)
//   SMTP_SECURITY               starttls (default) | tls (implicit, usually port 465) | none
//   SMTP_FROM                   sender address, e.g. no-reply@example.com
//   SMTP_FROM_NAME              sender display name (default "SmartHome")
//   PUBLIC_URL                  where the web UI is reachable; used for the button in emails

func envInt(key string, def, min, max int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// mfaEmailTTL is the lifetime of an emailed code.
func mfaEmailTTL() time.Duration {
	return time.Duration(envInt("MFA_EMAIL_CODE_TTL_MINUTES", 10, 1, 60)) * time.Minute
}

type smtpConfig struct {
	Host, Port, User, Pass, From, FromName, Security string
}

func loadSMTP() smtpConfig {
	c := smtpConfig{
		Host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		Port:     strings.TrimSpace(os.Getenv("SMTP_PORT")),
		User:     os.Getenv("SMTP_USER"),
		Pass:     os.Getenv("SMTP_PASS"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
		FromName: strings.TrimSpace(os.Getenv("SMTP_FROM_NAME")),
		Security: strings.ToLower(strings.TrimSpace(os.Getenv("SMTP_SECURITY"))),
	}
	if c.Port == "" {
		if c.Security == "tls" {
			c.Port = "465"
		} else {
			c.Port = "587"
		}
	}
	if c.Security == "" {
		c.Security = "starttls"
	}
	if c.FromName == "" {
		c.FromName = "SmartHome"
	}
	if c.From == "" && c.User != "" && strings.Contains(c.User, "@") {
		c.From = c.User
	}
	return c
}

// mailConfigured reports whether emails can be sent at all.
func mailConfigured() bool {
	c := loadSMTP()
	return c.Host != "" && c.From != ""
}
