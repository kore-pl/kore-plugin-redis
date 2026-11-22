package redisop

import (
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestValidateInstance 覆盖实例名、主机、端口与密码环境变量校验。
func TestValidateInstance(t *testing.T) {
	valid := []InstanceConfig{
		{Name: "default", Host: "127.0.0.1", Port: 6379},
		{Name: "cache-1", Host: "redis.example.com", Port: 6380, PasswordEnv: "REDIS_CACHE_PASSWORD"},
		{Name: "v6", Host: "::1", Port: 6379},
		{Name: "zone", Host: "fe80::1%eth0", Port: 6379},
	}
	for _, instance := range valid {
		if err := ValidateInstance(instance); err != nil {
			t.Fatalf("实例 %+v 应合法: %v", instance, err)
		}
	}
	invalid := []InstanceConfig{
		{Name: "", Host: "127.0.0.1", Port: 6379},
		{Name: "Bad", Host: "127.0.0.1", Port: 6379},
		{Name: "default", Host: "", Port: 6379},
		{Name: "default", Host: "-oops", Port: 6379},
		{Name: "default", Host: "127.0.0.1", Port: 0},
		{Name: "default", Host: "127.0.0.1", Port: 70000},
		{Name: "default", Host: "127.0.0.1", Port: 6379, PasswordEnv: "1BAD"},
	}
	for _, instance := range invalid {
		if err := ValidateInstance(instance); err == nil {
			t.Fatalf("实例 %+v 应被拒绝", instance)
		}
	}
}

// TestParseInstanceSpec 校验附加实例 "<名称>=<主机>:<端口>" 解析，含 IPv6 方括号写法。
func TestParseInstanceSpec(t *testing.T) {
	spec, err := ParseInstanceSpec("cache=10.0.0.5:6380")
	if err != nil || spec.Name != "cache" || spec.Host != "10.0.0.5" || spec.Port != 6380 {
		t.Fatalf("解析结果=%+v err=%v", spec, err)
	}
	spec, err = ParseInstanceSpec("v6=[::1]:6379")
	if err != nil || spec.Host != "::1" || spec.Port != 6379 {
		t.Fatalf("IPv6 解析结果=%+v err=%v", spec, err)
	}
	bad := []string{
		"no-equals-sign",
		"cache=",
		"cache=127.0.0.1",
		"cache=127.0.0.1:0",
		"cache=127.0.0.1:70000",
		"cache=127.0.0.1:port",
		"Bad=127.0.0.1:6379",
		"cache=-bad:6379",
	}
	for _, item := range bad {
		if _, err := ParseInstanceSpec(item); err == nil {
			t.Fatalf("%q 应解析失败", item)
		}
	}
}

// TestParsePasswordEnv 校验 "<名称>=<环境变量名>" 解析。
func TestParsePasswordEnv(t *testing.T) {
	name, env, err := ParsePasswordEnv("default=REDIS_PASSWORD")
	if err != nil || name != "default" || env != "REDIS_PASSWORD" {
		t.Fatalf("解析=(%q,%q,%v)", name, env, err)
	}
	for _, item := range []string{"default", "default=", "=REDIS_PASSWORD", "default=1BAD", "Bad=REDIS_PASSWORD"} {
		if _, _, err := ParsePasswordEnv(item); err == nil {
			t.Fatalf("%q 应解析失败", item)
		}
	}
}

// TestValidatePattern 模式上限 256 字节且禁止 NUL。
func TestValidatePattern(t *testing.T) {
	if perr := validatePattern("*"); perr != nil {
		t.Fatalf("通配符 * 应合法: %v", perr)
	}
	if perr := validatePattern("user:*:session"); perr != nil {
		t.Fatalf("常规模式应合法: %v", perr)
	}
	for _, pattern := range []string{"", strings.Repeat("a", maxPatternLen+1), "a\x00b"} {
		if perr := validatePattern(pattern); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("模式 %q 错误=%v，期望 E_INVALID", pattern, perr)
		}
	}
}

// TestValidateKey 键上限 1024 字节且禁止空键/NUL。
func TestValidateKey(t *testing.T) {
	if perr := validateKey("user:1"); perr != nil {
		t.Fatalf("常规键应合法: %v", perr)
	}
	for _, key := range []string{"", strings.Repeat("k", maxKeyLen+1), "a\x00b"} {
		if perr := validateKey(key); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("键 %q 错误=%v，期望 E_INVALID", key, perr)
		}
	}
}
