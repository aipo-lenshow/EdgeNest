package api

import (
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// AuthMiddleware verifies the Bearer JWT and stores the username in context.
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		hdr := c.GetHeader("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			core.Fail(c, 401, "UNAUTHORIZED", "missing bearer token")
			return
		}
		tokenStr := strings.TrimPrefix(hdr, "Bearer ")
		claims, err := auth.ParseToken(h.jwtSecret, tokenStr)
		if err != nil {
			core.Fail(c, 401, "UNAUTHORIZED", "invalid or expired token")
			return
		}
		c.Set("username", claims.Username)
		c.Next()
	}
}

// rateLimiter is a tiny fixed-window limiter keyed by client IP, used to slow
// down login brute force (5 attempts / minute / IP).
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]int64
	limit  int
	window int64 // seconds
}

func newRateLimiter(limit int, windowSeconds int64) *rateLimiter {
	return &rateLimiter{hits: map[string][]int64{}, limit: limit, window: windowSeconds}
}

func (r *rateLimiter) allow(key string) bool {
	now := time.Now().Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now - r.window
	kept := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

// LoginRateLimit limits login attempts per IP.
func (h *Handler) LoginRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.loginLimiter.allow(c.ClientIP()) {
			core.Fail(c, 429, "RATE_LIMITED", "too many login attempts, slow down")
			return
		}
		c.Next()
	}
}
