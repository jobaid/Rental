package handlers

import (
	"net/http"

	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/middleware"
)

func claimsFromReq(r *http.Request) *auth.Claims {
	return middleware.ClaimsFrom(r.Context())
}
