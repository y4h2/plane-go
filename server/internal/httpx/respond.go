// Package httpx holds small HTTP response helpers shared by all handlers.
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Error mirrors the Django app views' error envelope: {"error": "..."}.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// Detail mirrors DRF's auth envelope: {"detail": "..."} (used for 401/403).
func Detail(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"detail": msg})
}
