// Package redisdb provides keyspace browsing and command execution over a
// Redis/Valkey client.
package redisdb

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyInfo is one row in the keyspace browser.
type KeyInfo struct {
	Key  string
	Type string
	TTL  string
}

// KeyPage is a page of SCAN results plus the cursor to continue from.
type KeyPage struct {
	Keys   []KeyInfo
	Cursor uint64
}

// Value is a type-aware rendering of a single key.
type Value struct {
	Key   string
	Type  string
	TTL   string
	Text  string      // for string type
	Pairs [][2]string // for hash / zset (member,score)
	List  []string    // for list / set
}

// Scan returns one page of keys (with type + TTL) matching the pattern.
func Scan(ctx context.Context, c *redis.Client, match string, cursor uint64, count int64) (*KeyPage, error) {
	if match == "" {
		match = "*"
	}
	if count <= 0 {
		count = 100
	}
	keys, next, err := c.Scan(ctx, cursor, match, count).Result()
	if err != nil {
		return nil, err
	}
	page := &KeyPage{Cursor: next}
	for _, k := range keys {
		ki := KeyInfo{Key: k}
		ki.Type, _ = c.Type(ctx, k).Result()
		if d, err := c.TTL(ctx, k).Result(); err == nil {
			ki.TTL = ttlString(d)
		}
		page.Keys = append(page.Keys, ki)
	}
	return page, nil
}

// Get renders a key's value according to its type (collections are capped).
func Get(ctx context.Context, c *redis.Client, key string) (*Value, error) {
	typ, err := c.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	v := &Value{Key: key, Type: typ}
	if d, err := c.TTL(ctx, key).Result(); err == nil {
		v.TTL = ttlString(d)
	}
	const cap = 500
	switch typ {
	case "string":
		v.Text, _ = c.Get(ctx, key).Result()
	case "hash":
		m, _ := c.HGetAll(ctx, key).Result()
		for f, val := range m {
			v.Pairs = append(v.Pairs, [2]string{f, val})
		}
	case "list":
		v.List, _ = c.LRange(ctx, key, 0, cap-1).Result()
	case "set":
		v.List, _ = c.SMembers(ctx, key).Result()
	case "zset":
		zs, _ := c.ZRangeWithScores(ctx, key, 0, cap-1).Result()
		for _, z := range zs {
			v.Pairs = append(v.Pairs, [2]string{fmt.Sprintf("%v", z.Member), fmt.Sprintf("%v", z.Score)})
		}
	case "none":
		return nil, fmt.Errorf("key %q does not exist", key)
	default:
		v.Text = fmt.Sprintf("(unsupported type %q — use the command console)", typ)
	}
	return v, nil
}

// reDangerous matches commands that can wipe data, take over the server, or run
// arbitrary code/scripts on it: data flushes, server-side scripting (EVAL/
// FUNCTION/FCALL), module loading (native code → RCE), CONFIG (e.g. dir + SAVE
// to write files), replication/migration takeover, and admin/persistence ops.
// The handler treats these as admin-only AND confirm-gated, so a plain "write"
// user can't reach Redis-host compromise.
var reDangerous = regexp.MustCompile(`(?i)^(flushall|flushdb|shutdown|debug|` +
	`eval|evalsha|eval_ro|evalsha_ro|fcall|fcall_ro|function|script|` +
	`module|config|slaveof|replicaof|migrate|restore|swapdb|` +
	`acl|cluster|failover|save|bgsave|bgrewriteaof|lastsave|reset|latency|monitor)$`)

// NeedsConfirm reports whether a raw command is dangerous enough that the
// handler requires admin + an explicit confirmation before running it.
func NeedsConfirm(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return reDangerous.MatchString(strings.TrimSpace(args[0]))
}

// Command runs an arbitrary command and returns a flat string rendering.
func Command(ctx context.Context, c *redis.Client, args []string) (string, error) {
	ifs := make([]any, len(args))
	for i, a := range args {
		ifs[i] = a
	}
	res, err := c.Do(ctx, ifs...).Result()
	if err != nil {
		return "", err
	}
	return render(res), nil
}

// ParseArgs splits a command line on whitespace (quotes-aware-lite).
func ParseArgs(line string) []string {
	return strings.Fields(line)
}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "(nil)"
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return fmt.Sprintf("%d", x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = render(e)
		}
		return strings.Join(parts, "\n")
	case map[any]any:
		var b strings.Builder
		for k, val := range x {
			fmt.Fprintf(&b, "%s: %s\n", render(k), render(val))
		}
		return b.String()
	default:
		return fmt.Sprintf("%v", x)
	}
}

func ttlString(d time.Duration) string {
	switch {
	case d == -1:
		return "no expiry"
	case d == -2:
		return "—"
	default:
		return d.Truncate(time.Second).String()
	}
}
