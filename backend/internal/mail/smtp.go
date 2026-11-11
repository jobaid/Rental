package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Host     string
	Port     string
	User     string
	Pass     string
	FromAddr string
	FromName string
}

func (c Config) Configured() bool {
	return c.Host != "" && c.Port != "" && c.FromAddr != ""
}

type Sender struct {
	Cfg Config
}

// ErrNotConfigured is returned when the sender has no SMTP config at all.
var ErrNotConfigured = errors.New("SMTP not configured — set SMTP_HOST/SMTP_PORT/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM_ADDRESS")

func ValidateAddress(addr string) bool {
	_, err := mail.ParseAddress(addr)
	return err == nil
}

// deliver is the shared transport. Handles three TLS modes depending on port:
//   - 465             → implicit TLS (tls.Dial first)
//   - 587 or anything → explicit STARTTLS after EHLO (standard)
//   - 25  (no auth)   → plain
// A 20-second total timeout guards against the "EOF after Hello" hang some
// providers produce when the client misconfigures TLS.
func (s *Sender) deliver(from string, to []string, msg []byte) error {
	host := s.Cfg.Host
	port := s.Cfg.Port
	addr := net.JoinHostPort(host, port)

	// Choose dial strategy.
	var conn net.Conn
	var err error
	if port == "465" {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer c.Quit()

	// HELO/EHLO — Go does this automatically on first command but be explicit
	// so STARTTLS detection runs even if hello() hasn't fired.
	if err := c.Hello(clientHostname()); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}

	// STARTTLS on non-465 ports if the server advertises it.
	if port != "465" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}

	// Auth (optional).
	if s.Cfg.User != "" {
		// Prefer LOGIN when server advertises it; some providers reject PLAIN.
		if ok, mechs := c.Extension("AUTH"); ok {
			var auth smtp.Auth
			switch {
			case strings.Contains(mechs, "PLAIN"):
				auth = smtp.PlainAuth("", s.Cfg.User, s.Cfg.Pass, host)
			case strings.Contains(mechs, "LOGIN"):
				auth = &loginAuth{user: s.Cfg.User, pass: s.Cfg.Pass}
			default:
				return fmt.Errorf("server advertised AUTH mechanisms %q — none supported", mechs)
			}
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}
	}

	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return nil
}

// loginAuth is a minimal implementation of the LOGIN mechanism — Office365,
// SendGrid and Mailgun accept it; Go's standard library only provides PLAIN
// and CRAM-MD5.
type loginAuth struct{ user, pass string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte{}, nil
}
func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN server challenge: %q", fromServer)
}

func clientHostname() string {
	if h, err := net.LookupCNAME("localhost"); err == nil && h != "" {
		return strings.TrimRight(h, ".")
	}
	return "localhost"
}

// =============================================================================
// Public send API — unchanged signatures, now using the proper transport.
// =============================================================================

func (s *Sender) Send(to, subject, body string) (messageID string, err error) {
	if !s.Cfg.Configured() {
		return "", ErrNotConfigured
	}
	if !ValidateAddress(to) {
		return "", fmt.Errorf("invalid recipient %q", to)
	}
	msgID := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randHex(6), s.Cfg.Host)
	fromHeader := s.Cfg.FromAddr
	if s.Cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.Cfg.FromName, s.Cfg.FromAddr)
	}
	var buf strings.Builder
	buf.WriteString("From: " + fromHeader + "\r\n")
	buf.WriteString("To: " + to + "\r\n")
	buf.WriteString("Subject: " + encodeSubject(subject) + "\r\n")
	buf.WriteString("Message-ID: " + msgID + "\r\n")
	buf.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	buf.WriteString(body)
	if err := s.deliver(s.Cfg.FromAddr, []string{to}, []byte(buf.String())); err != nil {
		return msgID, err
	}
	return msgID, nil
}

func (s *Sender) SendHTML(to, subject, htmlBody, textBody string) (messageID string, err error) {
	if !s.Cfg.Configured() {
		return "", ErrNotConfigured
	}
	if !ValidateAddress(to) {
		return "", fmt.Errorf("invalid recipient %q", to)
	}
	msgID := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randHex(6), s.Cfg.Host)
	fromHeader := s.Cfg.FromAddr
	if s.Cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.Cfg.FromName, s.Cfg.FromAddr)
	}
	boundary := "mix_" + randHex(10)
	var buf strings.Builder
	buf.WriteString("From: " + fromHeader + "\r\n")
	buf.WriteString("To: " + to + "\r\n")
	buf.WriteString("Subject: " + encodeSubject(subject) + "\r\n")
	buf.WriteString("Message-ID: " + msgID + "\r\n")
	buf.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	buf.WriteString(textBody + "\r\n\r\n")
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	buf.WriteString(htmlBody + "\r\n\r\n")
	buf.WriteString("--" + boundary + "--\r\n")
	if err := s.deliver(s.Cfg.FromAddr, []string{to}, []byte(buf.String())); err != nil {
		return msgID, err
	}
	return msgID, nil
}

func encodeSubject(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func randHex(n int) string {
	const alph = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = alph[time.Now().UnixNano()%int64(len(alph))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
