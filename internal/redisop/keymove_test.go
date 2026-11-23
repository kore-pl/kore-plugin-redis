package redisop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 本文件覆盖 redis.rename / move / copy 的方法层门控：nx 默认、覆盖需 confirm、
// 大键门控、跨实例拒绝、参数校验与结果字段（全部不连真实 Redis）。

func moveTestInstances() []InstanceConfig {
	return []InstanceConfig{{Name: "default", Host: "127.0.0.1", Port: 6379}}
}

// TestRenameNXDefault 缺省 nx=true：调用 RENAMENX 语义，目标已存在时回 renamed=false + hint。
func TestRenameNXDefault(t *testing.T) {
	exec := fakeExecutor{renamed: RenameResult{Key: "old", NewKey: "new", NX: true, TargetExisted: true,
		Hint: "目标键已存在，未覆盖"}}
	raw, perr := Call("redis.rename", json.RawMessage(`{"instance":"default","key":"old","new_key":"new"}`),
		moveTestInstances(), exec)
	if perr != nil {
		t.Fatalf("意外错误: %v", perr)
	}
	var result RenameResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析结果: %v", err)
	}
	if !result.NX || result.Renamed || !result.TargetExisted || result.Hint == "" {
		t.Fatalf("nx 默认语义异常: %s", raw)
	}
}

// TestRenameOverwriteRequiresConfirm nx:false 必须带 confirm:true 才允许覆盖。
func TestRenameOverwriteRequiresConfirm(t *testing.T) {
	exec := fakeExecutor{}
	_, perr := Call("redis.rename",
		json.RawMessage(`{"instance":"default","key":"old","new_key":"new","nx":false}`),
		moveTestInstances(), exec)
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("nx:false 缺 confirm 应 E_INVALID，实际 %v", perr)
	}
	raw, perr := Call("redis.rename",
		json.RawMessage(`{"instance":"default","key":"old","new_key":"new","nx":false,"confirm":true}`),
		moveTestInstances(), exec)
	if perr != nil {
		t.Fatalf("显式覆盖应放行: %v", perr)
	}
	if !strings.Contains(string(raw), `"nx":false`) {
		t.Fatalf("结果应反映 nx=false: %s", raw)
	}
}

// TestRenameParamsValidated 空 key / 相同 key 一律 E_INVALID。
func TestRenameParamsValidated(t *testing.T) {
	for _, params := range []string{
		`{"instance":"default","key":"","new_key":"new"}`,
		`{"instance":"default","key":"old","new_key":""}`,
		`{"instance":"default","key":"same","new_key":"same"}`,
	} {
		if _, perr := Call("redis.rename", json.RawMessage(params), moveTestInstances(), fakeExecutor{}); perr == nil ||
			perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应 E_INVALID，实际 %v", params, perr)
		}
	}
}

// TestBigKeyGate 超过 8 MiB 的大键：rename/copy 需 confirm，小键不受影响。
func TestBigKeyGate(t *testing.T) {
	big := fakeExecutor{keyBytes: maxBigKeyBytes + 1}
	if _, perr := Call("redis.rename", json.RawMessage(`{"instance":"default","key":"big","new_key":"big2"}`),
		moveTestInstances(), big); perr == nil || perr.Code != pluginapi.E_INVALID ||
		!strings.Contains(perr.Message, "8 MiB") {
		t.Fatalf("大键 rename 应要求 confirm，实际 %v", perr)
	}
	if _, perr := Call("redis.rename",
		json.RawMessage(`{"instance":"default","key":"big","new_key":"big2","confirm":true}`),
		moveTestInstances(), big); perr != nil {
		t.Fatalf("大键 + confirm 应放行: %v", perr)
	}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"big","dest":"big2"}`),
		moveTestInstances(), big); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("大键 copy 应要求 confirm，实际 %v", perr)
	}
	// 取不到大小时放行（老版本 Redis 无 MEMORY USAGE）。
	unknown := fakeExecutor{keyBytesErr: opErrorf(pluginapi.E_UPSTREAM, "unknown command")}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"big","dest":"big2"}`),
		moveTestInstances(), unknown); perr != nil {
		t.Fatalf("大小未知时应放行: %v", perr)
	}
}

// TestMoveContract db 越界拒绝；moved=false 透传 hint。
func TestMoveContract(t *testing.T) {
	for _, params := range []string{
		`{"instance":"default","key":"foo","db":-1}`,
		`{"instance":"default","key":"foo","db":1025}`,
	} {
		if _, perr := Call("redis.move", json.RawMessage(params), moveTestInstances(), fakeExecutor{}); perr == nil ||
			perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应 E_INVALID，实际 %v", params, perr)
		}
	}
	raw, perr := Call("redis.move", json.RawMessage(`{"instance":"default","key":"foo","db":1}`),
		moveTestInstances(), fakeExecutor{moved: MoveResult{Key: "foo", DB: 1, Moved: false, Hint: "目标库已有同名键"}})
	if perr != nil {
		t.Fatalf("意外错误: %v", perr)
	}
	if !strings.Contains(string(raw), `"moved":false`) || !strings.Contains(string(raw), "目标库已有同名键") {
		t.Fatalf("moved=false 语义异常: %s", raw)
	}
}

// TestCopyContract 默认不覆盖（dest_db=-1 同库）；replace 需 confirm；dest 不能等于 key。
func TestCopyContract(t *testing.T) {
	exec := fakeExecutor{copied: CopyResult{Key: "foo", Dest: "bar", DestDB: -1, Copied: true}}
	raw, perr := Call("redis.copy", json.RawMessage(`{"instance":"default","key":"foo","dest":"bar"}`),
		moveTestInstances(), exec)
	if perr != nil {
		t.Fatalf("意外错误: %v", perr)
	}
	if !strings.Contains(string(raw), `"dest_db":-1`) || !strings.Contains(string(raw), `"copied":true`) {
		t.Fatalf("copy 默认语义异常: %s", raw)
	}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"foo","dest":"bar","replace":true}`),
		moveTestInstances(), exec); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("replace 缺 confirm 应 E_INVALID，实际 %v", perr)
	}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"foo","dest":"bar","replace":true,"confirm":true,"db":2}`),
		moveTestInstances(), exec); perr != nil {
		t.Fatalf("replace+confirm 应放行: %v", perr)
	}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"same","dest":"same"}`),
		moveTestInstances(), exec); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("dest==key 应 E_INVALID，实际 %v", perr)
	}
	if _, perr := Call("redis.copy",
		json.RawMessage(`{"instance":"default","key":"foo","dest":"bar","db":-2}`),
		moveTestInstances(), exec); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("非法 db 应 E_INVALID，实际 %v", perr)
	}
}

// TestCrossInstanceRejected 三个方法都拒绝跨实例搬运，并指向导出导入/读值写入路径。
func TestCrossInstanceRejected(t *testing.T) {
	cases := map[string]string{
		"redis.rename": `{"instance":"default","key":"foo","new_key":"bar","target_instance":"dr"}`,
		"redis.move":   `{"instance":"default","key":"foo","db":1,"target_instance":"dr"}`,
		"redis.copy":   `{"instance":"default","key":"foo","dest":"bar","target_instance":"dr"}`,
	}
	for method, params := range cases {
		_, perr := Call(method, json.RawMessage(params), moveTestInstances(), fakeExecutor{})
		if perr == nil || perr.Code != pluginapi.E_UNSUPPORTED {
			t.Fatalf("%s 跨实例应 E_UNSUPPORTED，实际 %v", method, perr)
		}
		if !strings.Contains(perr.Message, "导出") && !strings.Contains(perr.Message, "写入目标实例") {
			t.Fatalf("%s 跨实例错误缺少处置指引: %q", method, perr.Message)
		}
	}
	// target_instance 为空或等于本实例时放行（默认部署不会带该字段）。
	if _, perr := Call("redis.rename",
		json.RawMessage(`{"instance":"default","key":"foo","new_key":"bar","target_instance":"default"}`),
		moveTestInstances(), fakeExecutor{}); perr != nil {
		t.Fatalf("同实例 target_instance 应放行: %v", perr)
	}
}
