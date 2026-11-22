package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// dispatchOnce 构造请求并调用 dispatch，返回响应。
func dispatchOnce(t *testing.T, cfg *config, method, params string) *pluginapi.Response {
	t.Helper()
	request := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
		Method: method, Params: json.RawMessage(params),
	}
	response, shutdown := dispatch(request, cfg, testLogger())
	if shutdown {
		t.Fatalf("%s 不应触发 shutdown", method)
	}
	return response
}

// TestConfigGet 返回 host/port/password_env 等字段的 schema 与当前值。
func TestConfigGet(t *testing.T) {
	cfg := testConfig()
	response := dispatchOnce(t, &cfg, "redis.config.get", `{}`)
	if response.Error != nil {
		t.Fatalf("config.get 失败: %v", response.Error)
	}
	result := string(response.Result)
	for _, want := range []string{
		`"key":"host"`, `"key":"port"`, `"key":"password_env"`,
		`"key":"instances"`, `"key":"password_envs"`,
		`"source":"default"`, `"host":"127.0.0.1"`, `"port":6379`,
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("config.get 结果未含 %s: %s", want, result)
		}
	}
}

// TestConfigSetHotReload 保存后同一进程内实例表立即更新，配置文件 0600。
func TestConfigSetHotReload(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)

	params := `{"values":{"host":"127.0.0.1","port":16379,"password_env":"KORE_REDIS_TEST_PASSWORD"}}`
	response := dispatchOnce(t, &cfg, "redis.config.set", params)
	if response.Error != nil {
		t.Fatalf("config.set 失败: %v", response.Error)
	}
	if !strings.Contains(string(response.Result), `"saved":true`) ||
		!strings.Contains(string(response.Result), `"restart_required":false`) {
		t.Fatalf("config.set 结果异常: %s", response.Result)
	}
	if cfg.source != plugincfg.SourceFile {
		t.Fatalf("保存后 source=%q，期望 file", cfg.source)
	}
	if len(cfg.instances) != 1 || cfg.instances[0].Port != 16379 ||
		cfg.instances[0].PasswordEnv != "KORE_REDIS_TEST_PASSWORD" {
		t.Fatalf("内存实例表未热更新: %+v", cfg.instances)
	}
	path := filepath.Join(cfg.stateDir, plugincfg.ConfigFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("配置文件未落盘: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限=%v，期望 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	if strings.Contains(string(raw), "s3cr3t") {
		t.Fatal("配置文件不应包含密码值")
	}

	// config.get 的 source 必须变为 file，且值来自磁盘。
	response = dispatchOnce(t, &cfg, "redis.config.get", `{}`)
	if response.Error != nil || !strings.Contains(string(response.Result), `"source":"file"`) {
		t.Fatalf("config.get 热更新后异常: %v %s", response.Error, response.Result)
	}
}

// TestConfigSetReject 非法配置返回 E_INVALID，且不改内存、不落盘。
func TestConfigSetReject(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)

	cases := []string{
		`{"values":{"port":70000}}`,
		`{"values":{"host":"-bad"}}`,
		`{"values":{"instances":["no-equals"]}}`,
		`{"values":{"instances":["default=127.0.0.1:6380"]}}`,
		`{"values":{"password_envs":["missing=REDIS_PASSWORD"]}}`,
		`{"values":{"unknown_field":1}}`,
		`{"values":[]}`,
	}
	for _, params := range cases {
		response := dispatchOnce(t, &cfg, "redis.config.set", params)
		if response.Error == nil || response.Error.Code != pluginapi.E_INVALID {
			t.Fatalf("config.set(%s) 错误=%v，期望 E_INVALID", params, response.Error)
		}
	}
	if len(cfg.instances) != 0 {
		t.Fatalf("非法保存改动了内存实例表: %+v", cfg.instances)
	}
	if _, err := os.Stat(filepath.Join(cfg.stateDir, plugincfg.ConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("非法保存不应落盘: %v", err)
	}
}

// TestConfigSetExtraInstances 附加实例与密码环境变量映射生效，未知实例被拒绝。
func TestConfigSetExtraInstances(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)

	params := `{"values":{"host":"127.0.0.1","port":6379,"instances":["cache=10.0.0.5:6380"],"password_envs":["cache=REDIS_CACHE_PASSWORD"]}}`
	response := dispatchOnce(t, &cfg, "redis.config.set", params)
	if response.Error != nil {
		t.Fatalf("config.set 失败: %v", response.Error)
	}
	if len(cfg.instances) != 2 {
		t.Fatalf("实例数=%d，期望 2", len(cfg.instances))
	}
	cache := cfg.instances[1]
	if cache.Name != "cache" || cache.Host != "10.0.0.5" || cache.Port != 6380 ||
		cache.PasswordEnv != "REDIS_CACHE_PASSWORD" {
		t.Fatalf("附加实例解析异常: %+v", cache)
	}
}

// TestBusinessMethodsWithoutInstances 未配置实例时 instances 返回空表，其余方法 E_NOTFOUND。
func TestBusinessMethodsWithoutInstances(t *testing.T) {
	cfg := testConfig()
	cfg.instances = []redisop.InstanceConfig{}
	cfg.values = fileValues{}
	response := dispatchOnce(t, &cfg, "redis.instances", `{}`)
	if response.Error != nil || !strings.Contains(string(response.Result), `"count":0`) {
		t.Fatalf("空实例 instances 异常: %v %s", response.Error, response.Result)
	}
	for _, method := range []string{"redis.info", "redis.keys", "redis.get", "redis.delete"} {
		response := dispatchOnce(t, &cfg, method, `{}`)
		if response.Error == nil || response.Error.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("%s 错误=%v，期望 E_NOTFOUND", method, response.Error)
		}
	}
}

// TestDangerousMethodsAbsent FLUSHALL/FLUSHDB/CONFIG SHUTDOWN 不在方法面。
func TestDangerousMethodsAbsent(t *testing.T) {
	cfg := testConfig()
	for _, method := range []string{
		"redis.flushall", "redis.flushdb", "redis.config", "redis.shutdown", "redis.scan",
	} {
		response := dispatchOnce(t, &cfg, method, `{}`)
		if response.Error == nil || response.Error.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("%s 错误=%v，期望 E_NOTFOUND", method, response.Error)
		}
	}
}

// TestPingResult kore.ping 返回 ready 状态。
func TestPingResult(t *testing.T) {
	cfg := testConfig()
	response := dispatchOnce(t, &cfg, "kore.ping", `{}`)
	if !strings.Contains(string(response.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", response.Result)
	}
}

// TestConfigSetDefaultPasswordFromList password_envs 也能描述主实例 default。
func TestConfigSetDefaultPasswordFromList(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)
	params := `{"values":{"host":"127.0.0.1","port":6379,"password_env":"","password_envs":["default=REDIS_PASSWORD"]}}`
	response := dispatchOnce(t, &cfg, "redis.config.set", params)
	if response.Error != nil {
		t.Fatalf("config.set 失败: %v", response.Error)
	}
	if len(cfg.instances) != 1 || cfg.instances[0].PasswordEnv != "REDIS_PASSWORD" {
		t.Fatalf("主实例密码环境变量未生效: %+v", cfg.instances)
	}
}
