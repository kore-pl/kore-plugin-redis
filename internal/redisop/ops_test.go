package redisop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// persistenceInfoSample 是 Redis 7.4 INFO persistence 的真实格式（AOF 开启）。
const persistenceInfoSample = "# Persistence\r\n" +
	"loading:0\r\n" +
	"async_loading:0\r\n" +
	"aof_enabled:1\r\n" +
	"aof_rewrite_in_progress:0\r\n" +
	"aof_last_bgrewrite_status:ok\r\n" +
	"aof_last_write_status:ok\r\n" +
	"aof_current_size:1049063\r\n" +
	"aof_base_size:1048891\r\n" +
	"rdb_changes_since_save:125\r\n" +
	"rdb_bgsave_in_progress:1\r\n" +
	"rdb_last_save_time:1763865600\r\n" +
	"rdb_last_bgsave_status:ok\r\n"

// TestBuildPersistenceResult 按 Redis 7.4 真实 INFO persistence 格式解析。
func TestBuildPersistenceResult(t *testing.T) {
	result := buildPersistenceResult("default", persistenceInfoSample)
	if !result.AOFEnabled || result.AOFLastWriteStatus != "ok" || result.AOFSize != 1049063 {
		t.Fatalf("AOF 解析异常: %+v", result)
	}
	if result.RDBChangesSinceSave != 125 || !result.RDBBgsaveInProgress || result.RDBLastSaveTime != 1763865600 {
		t.Fatalf("RDB 解析异常: %+v", result)
	}
	if result.Instance != "default" {
		t.Fatalf("instance=%q", result.Instance)
	}
}

// TestBuildPersistenceResultAOFOff AOF 关闭时 aof_current_size 缺失，aof_size=0 不报错。
func TestBuildPersistenceResultAOFOff(t *testing.T) {
	raw := "# Persistence\r\nloading:0\r\naof_enabled:0\r\n" +
		"aof_last_write_status:ok\r\nrdb_changes_since_save:0\r\n" +
		"rdb_bgsave_in_progress:0\r\nrdb_last_save_time:1763865600\r\n"
	result := buildPersistenceResult("default", raw)
	if result.AOFEnabled || result.AOFSize != 0 {
		t.Fatalf("AOF 关闭解析异常: %+v", result)
	}
}

// memoryInfoSample / statsInfoSample 是 Redis 7.4 INFO memory / stats 的真实格式。
const memoryInfoSample = "# Memory\r\n" +
	"used_memory:1049240\r\nused_memory_human:1.00M\r\n" +
	"used_memory_peak:1063208\r\nused_memory_peak_human:1.01M\r\n" +
	"mem_fragmentation_ratio:8.78\r\n" +
	"maxmemory:268435456\r\nmaxmemory_human:256.00M\r\n" +
	"maxmemory_policy:noeviction\r\n"

const statsInfoSample = "# Stats\r\ntotal_connections_received:12\r\n" +
	"total_commands_processed:980\r\nkeyspace_hits:640\r\nkeyspace_misses:20\r\n" +
	"evicted_keys:7\r\n"

// TestBuildMemoryResult 按 Redis 7.4 真实 INFO memory/stats 格式解析。
func TestBuildMemoryResult(t *testing.T) {
	result := buildMemoryResult("default", memoryInfoSample, statsInfoSample, "allkeys-lru")
	if result.Used != 1049240 || result.Peak != 1063208 || result.MaxMemory != 268435456 {
		t.Fatalf("内存字段解析异常: %+v", result)
	}
	if result.FragmentationRatio != 8.78 || result.EvictionKeys != 7 || result.MaxMemoryPolicy != "allkeys-lru" {
		t.Fatalf("策略/碎片/逐出解析异常: %+v", result)
	}
}

// TestBuildMemoryResultMissingFields 字段缺失时保持零值，ratio 非法时不失败。
func TestBuildMemoryResultMissingFields(t *testing.T) {
	result := buildMemoryResult("default", "# Memory\r\n", "# Stats\r\n", "")
	if result.Used != 0 || result.Peak != 0 || result.MaxMemory != 0 ||
		result.FragmentationRatio != 0 || result.EvictionKeys != 0 || result.MaxMemoryPolicy != "" {
		t.Fatalf("缺失字段应保持零值: %+v", result)
	}
}

// TestConfigGetString CONFIG GET 的扁平数组回复取值；形状异常与缺键返回空串。
func TestConfigGetString(t *testing.T) {
	pairs := []any{"maxmemory-policy", "noeviction", "maxmemory", "0"}
	if got := configGetString(pairs, "maxmemory-policy"); got != "noeviction" {
		t.Fatalf("configGetString=%q", got)
	}
	if got := configGetString(pairs, "MAXMEMORY-POLICY"); got != "noeviction" {
		t.Fatalf("应大小写不敏感: %q", got)
	}
	if got := configGetString(pairs, "missing"); got != "" {
		t.Fatalf("缺键应返回空串: %q", got)
	}
	if got := configGetString([]any{"odd"}, "x"); got != "" {
		t.Fatalf("奇数长度应返回空串: %q", got)
	}
	if got := configGetString("not-array", "x"); got != "" {
		t.Fatalf("非数组应返回空串: %q", got)
	}
}

// maxMemoryPolicies 是 Redis 全部 8 种淘汰策略枚举；策略值原样透传给调用方。
var maxMemoryPolicies = []string{
	"volatile-lru", "allkeys-lru", "volatile-lfu", "allkeys-lfu",
	"volatile-random", "allkeys-random", "volatile-ttl", "noeviction",
}

// TestMaxMemoryPolicyEnum 八种策略枚举逐一透传，不篡改、不丢值。
func TestMaxMemoryPolicyEnum(t *testing.T) {
	for _, policy := range maxMemoryPolicies {
		result := buildMemoryResult("default", memoryInfoSample, statsInfoSample, policy)
		if result.MaxMemoryPolicy != policy {
			t.Fatalf("策略 %q 透传为 %q", policy, result.MaxMemoryPolicy)
		}
	}
}

// TestTruncateSlowlogArg 单参数按字符数截断并加省略号；短参数原样保留。
func TestTruncateSlowlogArg(t *testing.T) {
	if got := truncateSlowlogArg("short"); got != "short" {
		t.Fatalf("短参数被改动: %q", got)
	}
	long := strings.Repeat("a", 200)
	got := truncateSlowlogArg(long)
	if len([]rune(got)) != maxSlowlogArgRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("200 字符参数应截断为 %d 字符+省略号，实际 %d 字符", maxSlowlogArgRunes, len([]rune(got)))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxSlowlogArgRunes)) {
		t.Fatalf("截断应保留前缀")
	}
	multibyte := strings.Repeat("中", 200)
	got = truncateSlowlogArg(multibyte)
	if len([]rune(got)) != maxSlowlogArgRunes+1 {
		t.Fatalf("多字节字符按字符数截断: %d", len([]rune(got)))
	}
}

// slowlogReplyEntry 构造一条 Redis 7 格式的慢查询记录（含 7 个元素，尾部忽略）。
func slowlogReplyEntry(id int64, argv ...string) []any {
	rawArgv := make([]any, 0, len(argv))
	for _, arg := range argv {
		rawArgv = append(rawArgv, arg)
	}
	return []any{id, int64(1763865600), int64(15234), rawArgv, "127.0.0.1:51234", "kored", "file:line"}
}

// TestParseSlowlogEntry 解析 Redis 7 格式条目：前 4 元素取值，尾部字段忽略。
func TestParseSlowlogEntry(t *testing.T) {
	entry, err := parseSlowlogEntry(slowlogReplyEntry(42, "SET", "kore:key", strings.Repeat("v", 300)))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if entry.ID != 42 || entry.TS != 1763865600 || entry.DurationUS != 15234 || entry.Command != "SET" {
		t.Fatalf("字段解析异常: %+v", entry)
	}
	// args_summary = "kore:key " + 截断值(128 字符 + …)。
	wantSummary := "kore:key " + strings.Repeat("v", maxSlowlogArgRunes) + "…"
	if !strings.HasPrefix(entry.ArgsSummary, "kore:key ") {
		t.Fatalf("args_summary 应含键名: %q", entry.ArgsSummary)
	}
	if entry.ArgsSummary != wantSummary {
		t.Fatalf("大值应被截断: got %d 字符 want %d", len([]rune(entry.ArgsSummary)), len([]rune(wantSummary)))
	}
	if _, err := parseSlowlogEntry([]any{int64(1), int64(2), int64(3)}); err == nil {
		t.Fatal("缺 argv 的条目应报错")
	}
	if _, err := parseSlowlogEntry([]any{int64(1), int64(2), int64(3), []any{}, "x", "y"}); err == nil {
		t.Fatal("空 argv 的条目应报错")
	}
	if _, err := parseSlowlogEntry("not-array"); err == nil {
		t.Fatal("非数组条目应报错")
	}
}

// TestClientPersistence 真实 RESP 流程：INFO persistence 解析为结构化结果。
func TestClientPersistence(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "INFO" && command[1] == "persistence" {
			return persistenceInfoSample, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().Persistence(server.Instance())
	if err != nil {
		t.Fatalf("Persistence 失败: %v", err)
	}
	if !result.AOFEnabled || result.AOFSize != 1049063 || !result.RDBBgsaveInProgress {
		t.Fatalf("结果异常: %+v", result)
	}
}

// TestClientMemory 真实 RESP 流程：INFO memory + INFO stats + CONFIG GET。
func TestClientMemory(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch {
		case command[0] == "INFO" && command[1] == "memory":
			return memoryInfoSample, nil
		case command[0] == "INFO" && command[1] == "stats":
			return statsInfoSample, nil
		case command[0] == "CONFIG" && len(command) == 3 && command[1] == "GET":
			return []any{"maxmemory-policy", "allkeys-lfu"}, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().Memory(server.Instance())
	if err != nil {
		t.Fatalf("Memory 失败: %v", err)
	}
	if result.Used != 1049240 || result.MaxMemoryPolicy != "allkeys-lfu" ||
		result.FragmentationRatio != 8.78 || result.EvictionKeys != 7 {
		t.Fatalf("结果异常: %+v", result)
	}
	sawConfig := false
	for _, cmd := range server.CommandList() {
		if cmd[0] == "CONFIG" && cmd[1] == "GET" && cmd[2] == "maxmemory-policy" {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Fatal("未发送 CONFIG GET maxmemory-policy")
	}
}

// TestClientMemoryConfigDisabled 云托管实例禁用 CONFIG 时策略降级为空串，整体不失败。
func TestClientMemoryConfigDisabled(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch {
		case command[0] == "INFO" && command[1] == "memory":
			return memoryInfoSample, nil
		case command[0] == "INFO" && command[1] == "stats":
			return statsInfoSample, nil
		case command[0] == "CONFIG":
			return nil, fmt.Errorf("ERR unknown command 'CONFIG'")
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().Memory(server.Instance())
	if err != nil {
		t.Fatalf("CONFIG 禁用不应失败: %v", err)
	}
	if result.MaxMemoryPolicy != "" || result.Used != 1049240 {
		t.Fatalf("降级结果异常: %+v", result)
	}
}

// TestClientSlowlog 真实 RESP 流程：SLOWLOG GET 带上限，多级条目解析 + 异常条目跳过。
func TestClientSlowlog(t *testing.T) {
	giant := strings.Repeat("x", 500)
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "SLOWLOG" && command[1] == "GET" {
			if command[2] != "25" {
				return nil, fmt.Errorf("ERR 期望上限 25，实际 %s", command[2])
			}
			return []any{
				slowlogReplyEntry(1, "GET", "kore:key"),
				slowlogReplyEntry(2, "SET", "kore:big", giant),
				[]any{int64(3), int64(3), int64(3)}, // 形状异常条目：跳过
			}, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().Slowlog(server.Instance(), 25)
	if err != nil {
		t.Fatalf("Slowlog 失败: %v", err)
	}
	if result.Count != 2 || len(result.Entries) != 2 {
		t.Fatalf("count=%d entries=%d，期望 2", result.Count, len(result.Entries))
	}
	if result.Entries[0].Command != "GET" || result.Entries[0].ArgsSummary != "kore:key" {
		t.Fatalf("条目 0 异常: %+v", result.Entries[0])
	}
	big := result.Entries[1].ArgsSummary
	wantBig := "kore:big " + strings.Repeat("x", maxSlowlogArgRunes) + "…"
	if big != wantBig {
		t.Fatalf("大值参数未按字符截断: got %d 字符 want %d", len([]rune(big)), len([]rune(wantBig)))
	}
}

// TestClientSlowlogEmpty 空慢日志返回空数组而非 null。
func TestClientSlowlogEmpty(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "SLOWLOG" {
			return []any{}, nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	raw, perr := Call("redis.slowlog", json.RawMessage(`{"limit":10}`), []InstanceConfig{server.Instance()}, nil)
	if perr != nil {
		t.Fatalf("slowlog 失败: %v", perr)
	}
	if !strings.Contains(string(raw), `"entries":[]`) || !strings.Contains(string(raw), `"count":0`) {
		t.Fatalf("空结果异常: %s", raw)
	}
}

// TestClientKeyInfo 真实 RESP 流程：四命令 pipeline 读取单键元信息。
func TestClientKeyInfo(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "hash", nil
		case "TTL":
			return int64(3600), nil
		case "MEMORY":
			return int64(480), nil
		case "OBJECT":
			return "listpack", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().KeyInfo(server.Instance(), "kore:h")
	if err != nil {
		t.Fatalf("KeyInfo 失败: %v", err)
	}
	if result.Type != "hash" || result.Encoding != "listpack" || result.TTLS != 3600 || result.MemoryBytes != 480 {
		t.Fatalf("结果异常: %+v", result)
	}
	commands := server.CommandList()
	if len(commands) != 4 || commands[0][0] != "TYPE" || commands[1][0] != "TTL" ||
		commands[2][0] != "MEMORY" || commands[3][0] != "OBJECT" {
		t.Fatalf("命令序列异常: %v", commands)
	}
}

// TestClientKeyInfoNotFound 键不存在（TYPE=none）返回 E_NOTFOUND。
func TestClientKeyInfoNotFound(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		if command[0] == "TYPE" {
			return "none", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	_, err := NewClient().KeyInfo(server.Instance(), "missing")
	var opErr *OpError
	if err == nil || err.Error() == "" {
		t.Fatalf("缺键应报错: %v", err)
	}
	if !asOpError(err, &opErr) || opErr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("期望 E_NOTFOUND，实际 %v", err)
	}
}

// TestClientKeyInfoOldServer Redis < 4.0：MEMORY USAGE 报错时降级 0/空串，其余字段正常。
func TestClientKeyInfoOldServer(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		switch command[0] {
		case "TYPE":
			return "string", nil
		case "TTL":
			return int64(-1), nil
		case "MEMORY":
			return nil, fmt.Errorf("ERR unknown command 'MEMORY'")
		case "OBJECT":
			return "embstr", nil
		}
		return nil, fmt.Errorf("ERR unexpected %v", command)
	})
	result, err := NewClient().KeyInfo(server.Instance(), "kore:s")
	if err != nil {
		t.Fatalf("老版本降级不应失败: %v", err)
	}
	if result.MemoryBytes != 0 || result.Encoding != "embstr" || result.Type != "string" {
		t.Fatalf("降级结果异常: %+v", result)
	}
}

// asOpError 是 errors.As 的测试包装（保持本文件自包含）。
func asOpError(err error, target **OpError) bool {
	return errors.As(err, target)
}

// TestCallOpsSuccess 四个运维方法经 Call 的成功路径（含 keyInfo 大小写敏感动词）。
func TestCallOpsSuccess(t *testing.T) {
	exec := fakeExecutor{
		persistence: PersistenceResult{Instance: "default", AOFEnabled: true, AOFLastWriteStatus: "ok"},
		memory:      MemoryResult{Instance: "default", Used: 1024, MaxMemoryPolicy: "noeviction", FragmentationRatio: 1.2},
		slowlog:     SlowlogResult{Entries: []SlowlogEntry{{ID: 1, TS: 100, DurationUS: 200, Command: "GET"}}},
		keyInfo:     KeyInfoResult{Key: "foo", Type: "string", Encoding: "embstr", TTLS: -1, MemoryBytes: 48},
	}
	cases := []struct {
		method string
		params string
		want   string
	}{
		{"redis.persistence", `{}`, `"aof_enabled":true`},
		{"redis.memory", `{}`, `"maxmemory_policy":"noeviction"`},
		{"redis.slowlog", `{"limit":10}`, `"command":"GET"`},
		{"redis.keyInfo", `{"key":"foo"}`, `"encoding":"embstr"`},
	}
	for _, c := range cases {
		result, perr := Call(c.method, json.RawMessage(c.params), testInstances(), exec)
		if perr != nil {
			t.Fatalf("%s 意外错误: %v", c.method, perr)
		}
		if !strings.Contains(string(result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, result, c.want)
		}
	}
	// keyInfo 动词大小写敏感：全小写不是方法面成员。
	if _, ok := businessVerb("redis.keyinfo"); ok {
		t.Fatal("redis.keyinfo 不应被接受（大小写敏感）")
	}
}

// TestCallOpsParamValidation 运维方法的上限与参数校验。
func TestCallOpsParamValidation(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"redis.slowlog", `{"limit":1001}`, pluginapi.E_INVALID},
		{"redis.keyInfo", `{"key":""}`, pluginapi.E_INVALID},
		{"redis.keyInfo", `{"key":"` + strings.Repeat("k", 1025) + `"}`, pluginapi.E_INVALID},
	}
	for _, c := range cases {
		_, perr := Call(c.method, json.RawMessage(c.params), testInstances(), fakeExecutor{})
		if perr == nil || perr.Code != c.code {
			t.Fatalf("Call(%s,%s) 错误=%v，期望 %s", c.method, c.params, perr, c.code)
		}
	}
	// 多实例未指定 instance → E_INVALID。
	multiple := []InstanceConfig{
		{Name: "default", Host: "127.0.0.1", Port: 6379},
		{Name: "cache", Host: "127.0.0.1", Port: 6380},
	}
	for _, method := range []string{"redis.persistence", "redis.memory", "redis.slowlog", "redis.keyInfo"} {
		_, perr := Call(method, json.RawMessage(`{}`), multiple, fakeExecutor{})
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 多实例省略 instance 错误=%v，期望 E_INVALID", method, perr)
		}
	}
}
