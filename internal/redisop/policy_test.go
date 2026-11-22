package redisop

import "testing"

// TestValidatePolicyAccept 八种官方策略 + 空值归一全部接受。
func TestValidatePolicyAccept(t *testing.T) {
	cases := map[string]string{
		"noeviction": "noeviction", "allkeys-lru": "allkeys-lru",
		"allkeys-lfu": "allkeys-lfu", "allkeys-random": "allkeys-random",
		"volatile-lru": "volatile-lru", "volatile-lfu": "volatile-lfu",
		"volatile-random": "volatile-random", "volatile-ttl": "volatile-ttl",
		"": "noeviction",
	}
	for raw, want := range cases {
		got, perr := validatePolicy(raw)
		if perr != nil {
			t.Fatalf("validatePolicy(%q) 意外拒绝: %v", raw, perr)
		}
		if got != want {
			t.Fatalf("validatePolicy(%q)=%q，期望 %q", raw, got, want)
		}
	}
}

// TestValidatePolicyReject 未列举的策略一律 E_INVALID。
func TestValidatePolicyReject(t *testing.T) {
	for _, raw := range []string{"bogus", "ALLKEYS-LRU", "allkeys-lru-x", "-"} {
		got, perr := validatePolicy(raw)
		if perr == nil || got != "" {
			t.Fatalf("validatePolicy(%q) 未拒绝（返回 %q, %v）", raw, got, perr)
		}
	}
}
