package redisop

import "testing"

// TestValidatePrefixAccept 字面前缀接受：冒号、短横线、下划线、点、斜杠与数字。
func TestValidatePrefixAccept(t *testing.T) {
	for _, raw := range []string{"app:", "user:session:", "a-b_c.1/2", "x", "session:v1:"} {
		got, perr := validatePrefix(raw)
		if perr != nil || got != raw {
			t.Fatalf("validatePrefix(%q)=(%q,%v)，期望原样接受", raw, got, perr)
		}
	}
}

// TestValidatePrefixReject 空、超长、NUL、控制字符、通配符一律拒绝。
func TestValidatePrefixReject(t *testing.T) {
	bad := []string{
		"", string(make([]byte, maxPrefixLen+1)), "a\x00b", "a\nb", "a*b", "a?b", "a[b]",
	}
	for _, raw := range bad {
		got, perr := validatePrefix(raw)
		if perr == nil || got != "" {
			t.Fatalf("validatePrefix(%q) 未拒绝（返回 %q, %v）", raw, got, perr)
		}
	}
}
