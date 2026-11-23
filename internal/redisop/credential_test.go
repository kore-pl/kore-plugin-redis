package redisop

import (
	"encoding/json"
	"strings"
	"testing"
)

// countConfigPasswordReads 替换 config 口令读取入口并计数，用于断言 env 优先时零调用。
func countConfigPasswordReads(t *testing.T, password string) *int {
	t.Helper()
	old := readConfigPassword
	calls := 0
	readConfigPassword = func(InstanceConfig) string {
		calls++
		return password
	}
	t.Cleanup(func() { readConfigPassword = old })
	return &calls
}

// TestResolveCredentialPrefersKernelEnv 内核注入 REDISCLI_AUTH 存在时直接用，且不读 config 口令。
func TestResolveCredentialPrefersKernelEnv(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "env-secret")
	calls := countConfigPasswordReads(t, "config-secret")
	password, source := resolveCredential(InstanceConfig{Name: DefaultInstanceName, Password: "config-secret"})
	if password != "env-secret" || source != CredentialSourceKernelEnv {
		t.Fatalf("应优先内核 env: password=%q source=%q", password, source)
	}
	if *calls != 0 {
		t.Fatalf("env 存在时不得读取 config 口令，实际读取 %d 次", *calls)
	}
}

// TestResolveCredentialFallbackConfig env 缺失（空）时回落 config 口令，保持向后兼容。
func TestResolveCredentialFallbackConfig(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "")
	calls := countConfigPasswordReads(t, "config-secret")
	password, source := resolveCredential(InstanceConfig{Name: DefaultInstanceName, Password: "config-secret"})
	if password != "config-secret" || source != CredentialSourcePluginConfig {
		t.Fatalf("应回落 config: password=%q source=%q", password, source)
	}
	if *calls != 1 {
		t.Fatalf("env 缺失时应读取一次 config 口令，实际 %d 次", *calls)
	}
}

// TestResolveCredentialNone 两边都没有时 source=none（无密码 Redis 是合法配置）。
func TestResolveCredentialNone(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "")
	calls := countConfigPasswordReads(t, "")
	password, source := resolveCredential(InstanceConfig{Name: DefaultInstanceName})
	if password != "" || source != CredentialSourceNone {
		t.Fatalf("应为空/none: password=%q source=%q", password, source)
	}
	if *calls != 1 {
		t.Fatalf("无 env 时允许读 config 一次，实际 %d", *calls)
	}
}

// TestResolveCredentialPasswordEnvFallback config 直连口令为空时，回落到实例声明的环境变量。
func TestResolveCredentialPasswordEnvFallback(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "")
	t.Setenv("KORE_TEST_REDIS_PASSWORD", "declared-env-secret")
	calls := countConfigPasswordReads(t, "")
	password, source := resolveCredential(InstanceConfig{Name: DefaultInstanceName, PasswordEnv: "KORE_TEST_REDIS_PASSWORD"})
	if password != "declared-env-secret" || source != CredentialSourcePluginConfig {
		t.Fatalf("应回落 password_env: password=%q source=%q", password, source)
	}
	if *calls != 1 {
		t.Fatalf("应读一次 config，实际 %d", *calls)
	}
}

// TestResolveCredentialNonDefaultIgnoresKernelEnv 多实例的非默认实例不被默认实例的内核凭据覆盖。
func TestResolveCredentialNonDefaultIgnoresKernelEnv(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "env-secret")
	calls := countConfigPasswordReads(t, "primary-secret")
	password, source := resolveCredential(InstanceConfig{Name: "primary", Password: "primary-secret"})
	if password != "primary-secret" || source != CredentialSourcePluginConfig {
		t.Fatalf("非默认实例应使用自身 config: password=%q source=%q", password, source)
	}
	if *calls != 1 {
		t.Fatalf("非默认实例应读自己的 config，实际 %d 次", *calls)
	}

	// 非默认实例没有自己的口令时是 none，绝不能拿到内核注入的默认实例口令。
	countConfigPasswordReads(t, "")
	none, noneSource := resolveCredential(InstanceConfig{Name: "primary"})
	if none != "" || noneSource != CredentialSourceNone {
		t.Fatalf("非默认实例不得继承内核凭据: password=%q source=%q", none, noneSource)
	}
}

// TestInstancesCredentialSourceNoPlaintext 诊断字段有来源、响应无明文口令。
func TestInstancesCredentialSourceNoPlaintext(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "env-secret")
	password, source := resolveCredential(InstanceConfig{Name: DefaultInstanceName, Password: "config-secret"})
	result := InstancesResult{Instances: []InstanceStatus{{
		Name: DefaultInstanceName, Host: "127.0.0.1", Port: 6379, CredentialSource: source,
	}}, Count: 1}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"credential_source":"kernel_env"`) {
		t.Fatalf("缺少 credential_source: %s", text)
	}
	if password == "" || strings.Contains(text, password) || strings.Contains(text, "config-secret") {
		t.Fatalf("响应泄漏口令: %s", text)
	}
}

// TestInstancePasswordKernelEnvNoConfigRead 连接路径同样优先内核 env（instancePassword 零读 config）。
func TestInstancePasswordKernelEnvNoConfigRead(t *testing.T) {
	t.Setenv(KernelPasswordEnv, "env-secret")
	calls := countConfigPasswordReads(t, "config-secret")
	password, err := instancePassword(InstanceConfig{Name: DefaultInstanceName, Password: "config-secret"})
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if password != "env-secret" || *calls != 0 {
		t.Fatalf("连接路径应直接用内核 env: password=%q calls=%d", password, *calls)
	}
}
