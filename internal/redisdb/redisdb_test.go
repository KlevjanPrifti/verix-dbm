package redisdb

import "testing"

func TestNeedsConfirm(t *testing.T) {
	confirm := []string{
		"flushall", "FLUSHDB", "shutdown", "debug",
		"eval", "evalsha", "fcall", "function", "script",
		"module", "config", "slaveof", "replicaof", "migrate", "acl", "cluster",
	}
	for _, c := range confirm {
		if !NeedsConfirm([]string{c, "arg"}) {
			t.Errorf("expected %q to require confirm/admin", c)
		}
	}
	safe := []string{"get", "set", "hget", "scan", "ping", "info", "del", "ttl", "configuration"}
	for _, c := range safe {
		if NeedsConfirm([]string{c}) {
			t.Errorf("did not expect %q to require confirm/admin", c)
		}
	}
	if NeedsConfirm(nil) {
		t.Error("empty args should not require confirm")
	}
}
