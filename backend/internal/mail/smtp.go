package mail

import (
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

// Send delivers a text/plain email through the configured SMTP server.
// Returns the server-reported Message-ID (we synthesize one) + any error.
// If SMTP is not configured, returns ErrNotConfigured.
var ErrNotConfigured = errors.New("SMTP not configured — set SMTP_HOST/SMTP_PORT/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM_ADDRESS")

func ValidateAddress(addr string) bool {
	_, err := mail.ParseAddress(addr)
	return err == nil
}

func (s *Sender) Send(to, subject, body string) (messageID string, err error) {
	if !s.Cfg.Configured() {
		return "", ErrNotConfigured
	}
	if !ValidateAddress(to) {
		return "", fmt.Errorf("invalid recipient %q", to)
	}

	msgID := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randHex(6), s.Cfg.Host)
	from := s.Cfg.FromAddr
	fromHeader := from
	if s.Cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.Cfg.FromName, from)
	}

	var buf strings.Builder
	buf.WriteString("From: " + fromHeader + "\r\n")
	buf.WriteString("To: " + to + "\r\n")
	buf.WriteString("Subject: " + encodeSubject(subject) + "\r\n")
	buf.WriteString("Message-ID: " + msgID + "\r\n")
	buf.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)

	addr := net.JoinHostPort(s.Cfg.Host, s.Cfg.Port)
	var auth smtp.Auth
	if s.Cfg.User != "" {
		auth = smtp.PlainAuth("", s.Cfg.User, s.Cfg.Pass, s.Cfg.Host)
	}
	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(buf.String())); err != nil {
		return msgID, err
	}
	return msgID, nil
}

// SendHTML sends an HTML email (multipart/alternative with a plain-text fallback).
func (s *Sender) SendHTML(to, subject, htmlBody, textBody string) (messageID string, err error) {
	if !s.Cfg.Configured() {
		return "", ErrNotConfigured
	}
	if !ValidateAddress(to) {
		return "", fmt.Errorf("invalid recipient %q", to)
	}
	msgID := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randHex(6), s.Cfg.Host)
	from := s.Cfg.FromAddr
	fromHeader := from
	if s.Cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", s.Cfg.FromName, from)
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

	addr := net.JoinHostPort(s.Cfg.Host, s.Cfg.Port)
	var auth smtp.Auth
	if s.Cfg.User != "" {
		auth = smtp.PlainAuth("", s.Cfg.User, s.Cfg.Pass, s.Cfg.Host)
	}
	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(buf.String())); err != nil {
		return msgID, err
	}
	return msgID, nil
}

func encodeSubject(s string) string {
	// Simple ASCII pass-through; non-ASCII subjects would need =?UTF-8?B?...?=
	// Keep it safe: strip CR/LF.
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
