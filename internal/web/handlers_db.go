package web

// Shared constants and helpers for the Postgres and Redis query paths. The JSON
// API handlers in api.go are the only callers; the legacy server-rendered pages
// that once lived here have been removed in favour of the React SPA.

const browseLimit = 100

// redisReadAllow is the set of Redis commands permitted on read-only
// connections (or for users without write access).
var redisReadAllow = map[string]bool{
	"get": true, "mget": true, "type": true, "ttl": true, "pttl": true, "scan": true,
	"hget": true, "hgetall": true, "hkeys": true, "hlen": true, "lrange": true, "llen": true,
	"smembers": true, "scard": true, "zrange": true, "zcard": true, "exists": true,
	"strlen": true, "info": true, "dbsize": true, "ping": true, "memory": true, "object": true,
}

// orStar defaults an empty key-match pattern to "*" (match all).
func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// truncate clips a string to n runes for audit detail, appending an ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
