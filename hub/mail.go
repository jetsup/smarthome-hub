package hub

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SendMail delivers a multipart (text + HTML) email through the configured SMTP
// server. It supports implicit TLS, STARTTLS and plain (for a local relay).
func SendMail(to, subject, htmlBody, textBody string) error {
	cfg := loadSMTP()
	if cfg.Host == "" || cfg.From == "" {
		return fmt.Errorf("email is not configured on this hub (set SMTP_HOST and SMTP_FROM in .env)")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("invalid recipient")
	}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	var client *smtp.Client
	switch cfg.Security {
	case "tls":
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		client = c
	default:
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		client = c
		if cfg.Security == "starttls" {
			if ok, _ := client.Extension("STARTTLS"); !ok {
				client.Close()
				return fmt.Errorf("the SMTP server does not offer STARTTLS (set SMTP_SECURITY=none only for a trusted local relay)")
			}
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				client.Close()
				return err
			}
		}
	}
	defer client.Close()

	if cfg.User != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(cfg, to, subject, htmlBody, textBody)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func qpBody(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

func buildMessage(cfg smtpConfig, to, subject, htmlBody, textBody string) []byte {
	rb := make([]byte, 8)
	rand.Read(rb)
	boundary := "sh-" + hex.EncodeToString(rb)
	from := (&mail.Address{Name: cfg.FromName, Address: cfg.From}).String()
	domain := "smarthome.local"
	if i := strings.LastIndex(cfg.From, "@"); i >= 0 {
		domain = cfg.From[i+1:]
	}
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", from)
	h("To", to)
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", fmt.Sprintf("<%s@%s>", hex.EncodeToString(rb), domain))
	h("MIME-Version", "1.0")
	h("X-Auto-Response-Suppress", "All")
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, qpBody(textBody))
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, qpBody(htmlBody))
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

// maskEmail turns "george@example.com" into "g•••••@example.com".
func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at < 2 {
		return e
	}
	return e[:1] + strings.Repeat("•", min(at-1, 6)) + e[at:]
}
