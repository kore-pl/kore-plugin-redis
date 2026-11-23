package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestConfigGetIncludesDBAndTimeout 配置契约覆盖面：连接（host/port）+ 默认库（db）+ 超时（timeout_s）。
func TestConfigGetIncludesDBAndTimeout(t *testing.T) {
	cfg := testConfig()
	response := dispatchOnce(t, &cfg, "redis.config.get", `{}`)
	if response.Error != nil {
		t.Fatalf("config.get 失败: %v", response.Error)
	}
	result := string(response.Result)
	for _, want := range []string{
		`"key":"db"`, `"key":"timeout_s"`,
		`"db":0`, `"timeout_s":5`,
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("config.get 结果未含 %s: %s", want, result)
		}
	}
}

// TestConfigSetDBTimeoutHotReload db/timeout_s 保存后热生效，并传播到全部实例。
func TestConfigSetDBTimeoutHotReload(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)

	params := `{"values":{"host":"127.0.0.1","port":6379,"db":3,"timeout_s":9,"instances":["cache=127.0.0.1:6380"]}}`
	response := dispatchOnce(t, &cfg, "redis.config.set", params)
	if response.Error != nil {
		t.Fatalf("config.set 失败: %v", response.Error)
	}
	if cfg.values.DB != 3 || cfg.values.TimeoutS != 9 {
		t.Fatalf("内存配置未更新: %+v", cfg.values)
	}
	if len(cfg.instances) != 2 {
		t.Fatalf("实例数=%d，期望 2", len(cfg.instances))
	}
	for _, instance := range cfg.instances {
		if instance.DB != 3 || instance.TimeoutS != 9 {
			t.Fatalf("实例 %q 未继承 db/timeout: %+v", instance.Name, instance)
		}
	}

	// 落盘后重新加载应得到同样的归一化值（timeout 0 → 默认 5 的兼容路径见 validateValues 测试）。
	if _, err := os.Stat(filepath.Join(cfg.stateDir, plugincfg.ConfigFileName)); err != nil {
		t.Fatalf("配置文件未落盘: %v", err)
	}
}

// TestConfigSetRejectsBadDBTimeout db/timeout_s 越界返回 E_INVALID，且不落盘。
func TestConfigSetRejectsBadDBTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.stateDir = t.TempDir()
	cfg.store = plugincfg.NewStore(cfg.stateDir)

	for _, params := range []string{
		`{"values":{"db":256}}`,
		`{"values":{"db":-1}}`,
		`{"values":{"timeout_s":0.5}}`,
		`{"values":{"timeout_s":31}}`,
	} {
		response := dispatchOnce(t, &cfg, "redis.config.set", params)
		if response.Error == nil || response.Error.Code != pluginapi.E_INVALID {
			t.Fatalf("config.set(%s) 错误=%v，期望 E_INVALID", params, response.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.stateDir, plugincfg.ConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("越界保存不应落盘: %v", err)
	}
}

// TestValidateValuesDBTimeoutBounds 归一化规则：db 0–255、timeout 1–30、timeout 0 归一为默认 5。
func TestValidateValuesDBTimeoutBounds(t *testing.T) {
	base := fileValues{Host: "127.0.0.1", Port: 6379, Instances: []string{}, PasswordEnvs: []string{}}

	valid := []struct {
		name       string
		db         int
		timeout    int
		wantDB     int
		wantTimout int
	}{
		{"default", 0, 0, 0, defaultTimeoutS},
		{"db15", 15, 1, 15, 1},
		{"db255", 255, 30, 255, 30},
	}
	for _, c := range valid {
		values := base
		values.DB = c.db
		values.TimeoutS = c.timeout
		normalized, err := validateValues(values)
		if err != nil {
			t.Fatalf("%s 应通过: %v", c.name, err)
		}
		if normalized.DB != c.wantDB || normalized.TimeoutS != c.wantTimout {
			t.Fatalf("%s 归一化=(db=%d,timeout=%d)，期望 (%d,%d)",
				c.name, normalized.DB, normalized.TimeoutS, c.wantDB, c.wantTimout)
		}
	}

	invalid := []struct {
		name    string
		db      int
		timeout int
	}{
		{"db_negative", -1, 5},
		{"db_too_big", 256, 5},
		{"timeout_too_small", 0, -1},
		{"timeout_too_big", 0, 31},
	}
	for _, c := range invalid {
		values := base
		values.DB = c.db
		values.TimeoutS = c.timeout
		if _, err := validateValues(values); err == nil {
			t.Fatalf("%s（db=%d timeout=%d）应被拒绝", c.name, c.db, c.timeout)
		}
	}
}

// TestBuildInstancesPropagatesDBTimeout buildInstances 把连接级设置传播到主实例与附加实例。
func TestBuildInstancesPropagatesDBTimeout(t *testing.T) {
	values := fileValues{
		Host: "127.0.0.1", Port: 6379,
		DB: 2, TimeoutS: 8,
		Instances:    []string{"cache=127.0.0.1:6380"},
		PasswordEnvs: []string{},
	}
	instances := buildInstances(values)
	if len(instances) != 2 {
		t.Fatalf("实例数=%d，期望 2", len(instances))
	}
	if instances[0].DB != 2 || instances[0].TimeoutS != 8 {
		t.Fatalf("主实例未继承设置: %+v", instances[0])
	}
	if instances[1].Name != "cache" || instances[1].DB != 2 || instances[1].TimeoutS != 8 {
		t.Fatalf("附加实例未继承设置: %+v", instances[1])
	}
}
