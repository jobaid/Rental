package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/db"
)

type Deps struct {
	DB     *db.DB
	Signer *auth.Signer
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
