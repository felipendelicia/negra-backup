// internal/api/middleware.go
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const contextKeyUsername contextKey = "username"
const contextKeyAgentID contextKey = "agent_id"

func (s *Server) jwtAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			respondError(w, http.StatusUnauthorized, "missing token")
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			respondError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			respondError(w, http.StatusUnauthorized, "invalid claims")
			return
		}
		ctx := context.WithValue(r.Context(), contextKeyUsername, claims["sub"])
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) agentAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			respondError(w, http.StatusUnauthorized, "missing api key")
			return
		}
		apiKey := strings.TrimPrefix(auth, "Bearer ")

		if s.db == nil {
			respondError(w, http.StatusUnauthorized, "no db")
			return
		}

		rows, err := s.db.Query(`SELECT id, api_key FROM agents`)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		defer rows.Close()

		var agentID string
		found := false
		for rows.Next() {
			var id, hash string
			if err := rows.Scan(&id, &hash); err != nil {
				continue
			}
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(apiKey)) == nil {
				agentID = id
				found = true
				break
			}
		}
		if !found {
			respondError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		ctx := context.WithValue(r.Context(), contextKeyAgentID, agentID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
