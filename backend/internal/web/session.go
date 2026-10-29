package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const CookieName = "rsess"

type Session struct {
	UserID    uuid.UUID  `json:"uid"`
	CompanyID *uuid.UUID `json:"cid,omitempty"`
	Role      string     `json:"role"`
	Name      string     `json:"name"`
	Exp       int64      `json:"exp"`
}

func (s Session) Valid() bool { return s.Exp > time.Now().Unix() }

func (s Session) IsAdmin() bool {
	switch s.Role {
	case "platform_owner", "company_owner", "rental_manager", "branch_manager",
		"reservation_agent", "fleet_manager", "technician", "accountant", "auditor":
		return true
	}
	return false
}

func sign(secret []byte, payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func Encode(secret []byte, s Session) (string, error) {
	j, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	payload := hex.EncodeToString(j)
	return payload + "." + sign(secret, payload), nil
}

func Decode(secret []byte, cookie string) (Session, error) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return Session{}, errors.New("bad cookie")
	}
	want := sign(secret, parts[0])
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return Session{}, errors.New("bad signature")
	}
	j, err := hex.DecodeString(parts[0])
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(j, &s); err != nil {
		return Session{}, err
	}
	if !s.Valid() {
		return Session{}, errors.New("expired")
	}
	return s, nil
}

func SetCookie(w http.ResponseWriter, secret []byte, s Session, maxAgeHours int) error {
	s.Exp = time.Now().Add(time.Duration(maxAgeHours) * time.Hour).Unix()
	tok, err := Encode(secret, s)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAgeHours * 3600,
	})
	return nil
}

func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", HttpOnly: true,
		MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

type ctxKey string

const sessionKey ctxKey = "session"

// Attach reads the cookie (if any) and stores a Session on the context.
func Attach(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(CookieName); err == nil {
				if s, err := Decode(secret, c.Value); err == nil {
					ctx := context.WithValue(r.Context(), sessionKey, s)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func From(r *http.Request) (Session, bool) {
	v, ok := r.Context().Value(sessionKey).(Session)
	return v, ok
}

// RequireAdmin: redirects to /admin/login if no admin session.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := From(r)
		if !ok || !s.IsAdmin() {
			http.Redirect(w, r, "/admin/login?next="+r.URL.Path, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCustomer: redirects to /portal/login.
func RequireCustomer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := From(r)
		if !ok || s.Role != "customer" {
			http.Redirect(w, r, "/portal/login?next="+r.URL.Path, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
