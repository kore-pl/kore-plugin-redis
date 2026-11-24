//go:build !windows

// 权限不足（判据① 第三类）：用真实 POSIX 权限场景，不用桩。
//  1. 配置文件 chmod 000 → 读取必须返回 EACCES（存在 ≠ 可读）；
//  2. 状态目录去掉写位（0500）→ 原子写必须返回 EACCES，旧配置不被破坏、不留临时文件；
//  3. Redis 6 ACL 拒绝访问库（SELECT 返回 NOPERM）→ 明确 E_UPSTREAM 并带原因，
//     实例探测必须标记为不可达而不是"连上了"。
//
// root 会绕过 mode 位，因此 1/2 在 root 下 skip（CI 以非 root 运行）。
package boundary_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBoundaryPermissionConfigFileUnreadable 把配置文件设为 000：读取必须失败于
// EACCES（而不是被当成"未配置"静默回退默认值），且 stat 仍能看见文件。
func TestBoundaryPermissionConfigFileUnreadable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root 无视文件 mode 位，跳过（CI 以非 root 运行）")
	}
	dir := t.TempDir()
	store := plugincfg.NewStore(dir)
	if err := store.Write(json.RawMessage(`{"host":"127.0.0.1"}`)); err != nil {
		t.Fatalf("准备配置失败: %v", err)
	}
	path := store.Path()
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 000 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, exists, err := store.Read()
	if err == nil {
		t.Fatalf("mode 000 的配置应读取失败，实际成功（exists=%v）", exists)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("应返回 EACCES(permission denied)，实际 %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("错误应带配置文件路径 %s，实际 %q", path, err.Error())
	}
	if !store.Exists() {
		t.Fatalf("mode 000 只影响读取：stat 仍应能看见该配置文件")
	}
}

// TestBoundaryPermissionStateDirUnwritable 把状态目录改为 0500：原子写必须在
// 创建临时文件时失败于 EACCES，且旧配置原样保留（不允许半写坏配置）。
func TestBoundaryPermissionStateDirUnwritable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录 mode 位，跳过（CI 以非 root 运行）")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建状态目录失败: %v", err)
	}
	store := plugincfg.NewStore(dir)
	old := json.RawMessage(`{"host":"127.0.0.1","port":6379}`)
	if err := store.Write(old); err != nil {
		t.Fatalf("准备旧配置失败: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod 0500 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := store.Write(json.RawMessage(`{"host":"10.0.0.1","port":6380}`))
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("不可写目录应返回 EACCES，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "创建临时配置文件") {
		t.Fatalf("错误应说明失败在创建临时配置文件，实际 %q", err.Error())
	}
	raw, exists, readErr := store.Read()
	if readErr != nil || !exists || string(raw) != string(old) {
		t.Fatalf("写入失败后旧配置必须原样保留: raw=%s exists=%v err=%v", raw, exists, readErr)
	}
	entries, readDirErr := os.ReadDir(dir)
	if readDirErr != nil {
		t.Fatalf("读取状态目录失败: %v", readDirErr)
	}
	if len(entries) != 1 || entries[0].Name() != plugincfg.ConfigFileName {
		t.Fatalf("状态目录应只剩 %s，实际 %d 个条目", plugincfg.ConfigFileName, len(entries))
	}
}

// TestBoundaryPermissionRedisACLNoDB 让服务端对 SELECT 返回 NOPERM（Redis 6 ACL
// 拒绝访问该库）：方法必须返回 E_UPSTREAM 且错误里带 NOPERM 与目标库号；
// 实例探测必须标记 reachable=false，不能把 ACL 拒绝当成"连接正常"。
func TestBoundaryPermissionRedisACLNoDB(t *testing.T) {
	t.Parallel()
	server := startKVServer(t, withDenyDB())
	instances := []redisop.InstanceConfig{server.instance(3)}

	_, perr := invoke(instances, "redis.info", jsonObject(t, map[string]any{}))
	if perr == nil {
		t.Fatalf("ACL 拒绝访问 db 3 时 redis.info 应失败")
	}
	if perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("ACL 拒绝应映射为 E_UPSTREAM，实际 %s: %s", perr.Code, perr.Message)
	}
	for _, want := range []string{"NOPERM", "选择数据库 3"} {
		if !strings.Contains(perr.Message, want) {
			t.Fatalf("错误信息应含 %q，实际 %q", want, perr.Message)
		}
	}

	raw := mustInvoke(t, instances, "redis.instances", jsonObject(t, map[string]any{}))
	var result redisop.InstancesResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 instances 结果失败: %v", err)
	}
	if result.Count != 1 || result.Instances[0].Reachable {
		t.Fatalf("ACL 拒绝时应只有 1 个不可达实例，实际 %s", raw)
	}
	if !strings.Contains(result.Instances[0].Error, "NOPERM") {
		t.Fatalf("实例诊断应带 NOPERM 原因，实际 %q", result.Instances[0].Error)
	}
}
