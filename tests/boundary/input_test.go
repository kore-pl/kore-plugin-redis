// 异常输入（判据① 第一类）：非法 key 名、超长 value、非法 Redis 命令参数、
// 格式错误的配置文件。断言的是协议错误码与「参数按字节传输」，
// 不是「调用没崩就算过」。
package boundary_test

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

// TestBoundaryInputKeyNames 覆盖非法键名：NUL / 空 / 超长必须本地拒绝（E_INVALID），
// 且非法键不得落库；空格与控制字符在 Redis 语义下是合法键，必须作为单个 bulk
// 参数传输（伪装成 RESP 帧的 key 也不能变成第二条命令）。
func TestBoundaryInputKeyNames(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()

	invalid := []struct{ name, key string }{
		{"空 key", ""},
		{"NUL 字节", "key\x00tail"},
		{"超过 1024 字节", strings.Repeat("k", 1025)},
	}
	for _, tc := range invalid {
		readParams := jsonObject(t, map[string]any{"key": tc.key})
		if _, perr := invoke(instances, "redis.get", readParams); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 读取应返回 E_INVALID，实际 %v", tc.name, perr)
		}
		writeParams := jsonObject(t, map[string]any{"key": tc.key, "type": "string", "value": "v"})
		if _, perr := invoke(instances, "redis.set", writeParams); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 写入应返回 E_INVALID，实际 %v", tc.name, perr)
		}
	}
	if count := server.keyCount(0); count != 0 {
		t.Fatalf("非法 key 不应落库，服务端键数=%d", count)
	}

	weird := []string{"key with space", "tab\tkey", "line\r\nbreak", "*1\r\n$4\r\nQUIT", "ctrl\x01\x02\x7f"}
	for _, key := range weird {
		setParams := jsonObject(t, map[string]any{"key": key, "type": "string", "value": "v"})
		mustInvoke(t, instances, "redis.set", setParams)
		raw := mustInvoke(t, instances, "redis.get", jsonObject(t, map[string]any{"key": key}))
		var result redisop.GetResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("解析 get 结果失败: %v", err)
		}
		var value string
		if err := json.Unmarshal(result.Value, &value); err != nil || value != "v" {
			t.Fatalf("key %q 回读异常: value=%s err=%v", key, result.Value, err)
		}
		if !server.receivedSingleArg("GET", key) {
			t.Fatalf("key %q 未被当作单个 bulk 参数传输（存在 RESP 帧注入）", key)
		}
	}
}

// TestBoundaryInputOversizeValue 覆盖超长 value：单值 >1 MiB 与总负载 >4 MiB
// 必须在触达服务端之前返回 E_INVALID，且不落库。
func TestBoundaryInputOversizeValue(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()

	tooBig := strings.Repeat("x", (1<<20)+1)
	params := jsonObject(t, map[string]any{"key": "big", "type": "string", "value": tooBig})
	if _, perr := invoke(instances, "redis.set", params); perr == nil || perr.Code != pluginapi.E_INVALID ||
		!strings.Contains(perr.Message, "超过单值上限") {
		t.Fatalf("1 MiB+1 的 value 应 E_INVALID 且说明单值上限，实际 %v", perr)
	}

	items := make([]string, 1000)
	for index := range items {
		items[index] = strings.Repeat("y", 5000)
	}
	params = jsonObject(t, map[string]any{"key": "big-list", "type": "list", "value": items})
	if _, perr := invoke(instances, "redis.set", params); perr == nil || perr.Code != pluginapi.E_INVALID ||
		!strings.Contains(perr.Message, "总字节") {
		t.Fatalf("总负载 >4 MiB 的 value 应 E_INVALID，实际 %v", perr)
	}
	if count := server.keyCount(0); count != 0 {
		t.Fatalf("超限 value 不应落库，服务端键数=%d", count)
	}
}

// TestBoundaryInputIllegalCommandArgs 覆盖非法 Redis 命令参数：危险方法动词、
// 试图塞入任意命令的未知字段、CONFIG SET 危险参数、删除键数 / 库号越界，
// 都必须返回明确错误码而不是被透传到 Redis。
func TestBoundaryInputIllegalCommandArgs(t *testing.T) {
	t.Parallel()
	server := startKVServer(t)
	instances := server.instances()

	tooManyKeys := make([]string, 101)
	for index := range tooManyKeys {
		tooManyKeys[index] = "k" + strings.Repeat("0", index%5) + string(rune('a'+index%26))
	}
	cases := []struct {
		name   string
		method string
		params json.RawMessage
		code   string
		want   string
	}{
		{"FLUSHALL 动词拒绝", "redis.flushall", jsonObject(t, map[string]any{}), pluginapi.E_NOTFOUND, "FLUSHALL 会清空"},
		{"EVAL 动词拒绝", "redis.eval", jsonObject(t, map[string]any{}), pluginapi.E_NOTFOUND, "脚本"},
		{"未知字段（命令注入）", "redis.rename",
			jsonObject(t, map[string]any{"key": "a", "new_key": "b", "command": "FLUSHALL"}),
			pluginapi.E_INVALID, "不接受任意命令"},
		{"CONFIG SET dir 拒绝", "redis.serverConfig.set",
			jsonObject(t, map[string]any{"dir": "/tmp"}), pluginapi.E_UNSUPPORTED, "拒绝 CONFIG SET dir"},
		{"删除键超上限", "redis.delete",
			jsonObject(t, map[string]any{"keys": tooManyKeys}), pluginapi.E_INVALID, "keys 超过上限"},
		{"MOVE 库号越界", "redis.move",
			jsonObject(t, map[string]any{"key": "k", "db": 1025}), pluginapi.E_INVALID, "db 必须在"},
		{"RENAME 覆盖缺 confirm", "redis.rename",
			jsonObject(t, map[string]any{"key": "a", "new_key": "b", "nx": false}),
			pluginapi.E_INVALID, "confirm:true"},
	}
	for _, tc := range cases {
		_, perr := invoke(instances, tc.method, tc.params)
		if perr == nil || perr.Code != tc.code {
			t.Fatalf("%s 应返回 %s，实际 %v", tc.name, tc.code, perr)
		}
		if !strings.Contains(perr.Message, tc.want) {
			t.Fatalf("%s 错误信息应含 %q，实际 %q", tc.name, tc.want, perr.Message)
		}
	}
}

// TestBoundaryInputMalformedConfigFile 覆盖格式错误的配置文件：截断 JSON、
// 顶层非对象、空白文件都必须给出可定位的错误；合法配置必须能读回，
// 证明上面的失败来自格式本身而不是「永远失败」。
func TestBoundaryInputMalformedConfigFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := plugincfg.NewStore(dir)
	path := filepath.Join(dir, plugincfg.ConfigFileName)

	if err := os.WriteFile(path, []byte(`{"host":"127.0.0.1"`), 0o600); err != nil {
		t.Fatalf("写入截断配置失败: %v", err)
	}
	if _, exists, err := store.Read(); err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Fatalf("截断 JSON 应报不是合法 JSON，实际 err=%v exists=%v", err, exists)
	}

	if err := os.WriteFile(path, []byte(`["not","object"]`), 0o600); err != nil {
		t.Fatalf("写入非对象配置失败: %v", err)
	}
	if _, _, err := store.Read(); err == nil || !strings.Contains(err.Error(), "必须是 JSON 对象") {
		t.Fatalf("顶层数组应报必须是 JSON 对象，实际 err=%v", err)
	}

	if err := os.WriteFile(path, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("写入空白配置失败: %v", err)
	}
	if _, _, err := store.Read(); err == nil {
		t.Fatalf("空白配置文件应报错")
	}

	if err := store.Write(json.RawMessage(`[]`)); err == nil || !strings.Contains(err.Error(), "合法 JSON 对象") {
		t.Fatalf("写入非对象配置值应报错，实际 %v", err)
	}

	valid := json.RawMessage(`{"host":"127.0.0.1","port":6379}`)
	if err := store.Write(valid); err != nil {
		t.Fatalf("合法配置应能写入: %v", err)
	}
	raw, exists, err := store.Read()
	if err != nil || !exists || string(raw) != string(valid) {
		t.Fatalf("合法配置应原样读回: raw=%s exists=%v err=%v", raw, exists, err)
	}
}
