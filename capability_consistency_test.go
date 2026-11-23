package main

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// manifestDoc 是 info.json / manifest.json 在本测试里用到的字段（显式形状，不用 any）。
type manifestDoc struct {
	ID                 string            `json:"id"`
	Version            string            `json:"version"`
	Protocol           string            `json:"protocol"`
	RequiresKore       string            `json:"requires_kore"`
	Capabilities       []string          `json:"capabilities"`
	MethodCapabilities map[string]string `json:"method_capabilities"`
	DepthLevel         string            `json:"depth_level"`
}

// expectedMethodCapabilities 是本次评审通过的完整方法级能力表（18 条 = 16 业务 + config 两条）。
//
// 口径（PLUGIN-MATURITY §2 redis 行 + §3.5 #12）：
//   - 只读方法一律 system:read——未声明会回退插件级 system:admin，导致"看个配置也进 admin 审计"；
//   - 写方法一律 system:admin；
//   - redis.config.get / redis.config.set 必须显式声明（本条是 §3.5 #12 的验收点）。
var expectedMethodCapabilities = map[string]string{
	// 只读（11）
	"redis.config.get":       "system:read",
	"redis.instances":        "system:read",
	"redis.info":             "system:read",
	"redis.keys":             "system:read",
	"redis.get":              "system:read",
	"redis.keyInfo":          "system:read",
	"redis.ttl":              "system:read",
	"redis.memory":           "system:read",
	"redis.persistence":      "system:read",
	"redis.slowlog":          "system:read",
	"redis.serverConfig.get": "system:read",
	// 写（7）
	"redis.config.set":       "system:admin",
	"redis.delete":           "system:admin",
	"redis.set":              "system:admin",
	"redis.expire":           "system:admin",
	"redis.persist":          "system:admin",
	"redis.bgsave":           "system:admin",
	"redis.serverConfig.set": "system:admin",
}

// loadManifestFile 读取并解析清单文件；缺失或 JSON 非法直接失败。
func loadManifestFile(t *testing.T, path string) manifestDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	return doc
}

// implementedMethods 返回方法面全部完整方法名（业务方法 + dispatch 层处理的 config 两条）。
func implementedMethods() map[string]struct{} {
	methods := make(map[string]struct{})
	for _, method := range redisop.Methods() {
		methods[method] = struct{}{}
	}
	// config.get / config.set 由 main.dispatch 直接处理，不在 redisop 的方法表里。
	methods["redis.config.get"] = struct{}{}
	methods["redis.config.set"] = struct{}{}
	return methods
}

// TestMethodCapabilitiesConsistency 能力声明 vs 实现双向断言（表驱动）。
//
// 双向含义：
//   - 实现 → 声明：每个已实现方法都必须显式声明能力，否则回退插件级 admin；
//   - 声明 → 实现：每条声明都必须对应真实方法，禁止幽灵声明（会让审计表与代码漂移）。
func TestMethodCapabilitiesConsistency(t *testing.T) {
	info := loadManifestFile(t, "info.json")
	implemented := implementedMethods()

	// 方向 1：实现 → 声明。
	methods := make([]string, 0, len(implemented))
	for method := range implemented {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	for _, method := range methods {
		capability, declared := info.MethodCapabilities[method]
		if !declared {
			t.Errorf("方法 %s 已实现，但 info.json 未声明 method_capabilities："+
				"未声明会回退插件级 system:admin，只读方法被推进 admin 审计（§3.5 #12）", method)
			continue
		}
		if want, ok := expectedMethodCapabilities[method]; ok && capability != want {
			t.Errorf("方法 %s 的能力=%q，期望 %q", method, capability, want)
		}
	}

	// 方向 2：声明 → 实现。
	for method := range info.MethodCapabilities {
		if _, ok := implemented[method]; !ok {
			t.Errorf("info.json 声明了 %s，但方法面没有该实现（幽灵声明）", method)
		}
	}

	// 数量：18 条 = 16 业务 + config 两条；两边集合大小必须一致（双向覆盖）。
	if len(info.MethodCapabilities) != len(expectedMethodCapabilities) {
		t.Errorf("method_capabilities 条数=%d，期望 %d", len(info.MethodCapabilities), len(expectedMethodCapabilities))
	}
	if len(info.MethodCapabilities) != len(implemented) {
		t.Errorf("声明条数=%d 与实现方法数=%d 不一致", len(info.MethodCapabilities), len(implemented))
	}

	// 能力值必须是 6 项特权能力之一（与内核 validateMethodCapabilities 同口径）。
	privileged := map[string]struct{}{
		"system:read": {}, "system:admin": {},
		"net:read": {}, "net:write": {},
		"proc:read": {}, "proc:write": {},
	}
	for method, capability := range info.MethodCapabilities {
		if _, ok := privileged[capability]; !ok {
			t.Errorf("%s 的能力=%q 不是 6 项特权能力之一", method, capability)
		}
	}

	// config 两条单独断言：本任务的核心验收点。
	if got := info.MethodCapabilities["redis.config.get"]; got != "system:read" {
		t.Errorf("redis.config.get=%q，期望 system:read（否则看配置会回退 admin 审计）", got)
	}
	if got := info.MethodCapabilities["redis.config.set"]; got != "system:admin" {
		t.Errorf("redis.config.set=%q，期望 system:admin", got)
	}

	// 插件级 capabilities 仍保留 system:admin 作为未声明方法的兜底；但只读方法必须已显式声明，
	// 不能依赖兜底（否则等于没修 §3.5 #12）。
	pluginLevel := make(map[string]struct{}, len(info.Capabilities))
	for _, capability := range info.Capabilities {
		pluginLevel[capability] = struct{}{}
	}
	if _, ok := pluginLevel["system:admin"]; !ok {
		t.Errorf("插件级 capabilities=%v 缺少 system:admin（未声明方法的兜底会失效）", info.Capabilities)
	}
	readCount := 0
	for method, capability := range expectedMethodCapabilities {
		if capability != "system:read" {
			continue
		}
		readCount++
		if got, ok := info.MethodCapabilities[method]; !ok || got != "system:read" {
			t.Errorf("只读方法 %s 必须显式声明 system:read（当前 %q, 声明=%v）", method, got, ok)
		}
	}
	if readCount != 11 {
		t.Errorf("只读方法数=%d，期望 11", readCount)
	}
}

// TestManifestSync manifest.json（内嵌握手清单）必须与 info.json 的握手字段一致，version 同步 bump。
func TestManifestSync(t *testing.T) {
	info := loadManifestFile(t, "info.json")
	manifest := loadManifestFile(t, "manifest.json")
	if info.ID != "redis" || manifest.ID != info.ID {
		t.Fatalf("id 不一致: info=%q manifest=%q", info.ID, manifest.ID)
	}
	if manifest.Version != info.Version {
		t.Errorf("version 不一致: info=%q manifest=%q（G-17 要求同步 bump）", info.Version, manifest.Version)
	}
	if manifest.Protocol != info.Protocol || manifest.Protocol != pluginapi.Protocol {
		t.Errorf("protocol 不一致: info=%q manifest=%q 期望 %q", info.Protocol, manifest.Protocol, pluginapi.Protocol)
	}
	if manifest.RequiresKore != info.RequiresKore {
		t.Errorf("requires_kore 不一致: info=%q manifest=%q", info.RequiresKore, manifest.RequiresKore)
	}
	if len(manifest.Capabilities) != len(info.Capabilities) {
		t.Fatalf("capabilities 数量不一致: info=%v manifest=%v", info.Capabilities, manifest.Capabilities)
	}
	for index := range info.Capabilities {
		if manifest.Capabilities[index] != info.Capabilities[index] {
			t.Errorf("capabilities[%d] 不一致: info=%q manifest=%q",
				index, info.Capabilities[index], manifest.Capabilities[index])
		}
	}
	if info.DepthLevel != "production" {
		t.Errorf("depth_level=%q，期望 production（G-20：有 tests/fault/ 才可标生产级）", info.DepthLevel)
	}
}
