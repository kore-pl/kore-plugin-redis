package redisop

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestCommandWhitelist 传输面白名单：契约内命令放行，危险命令与危险子命令本地拒绝。
func TestCommandWhitelist(t *testing.T) {
	allowed := [][]string{
		{"PING"},
		{"AUTH", "secret"},
		{"SELECT", "1"},
		{"INFO"},
		{"INFO", "server"},
		{"SCAN", "0", "MATCH", "*", "COUNT", "100"},
		{"TYPE", "k"},
		{"TTL", "k"},
		{"GET", "k"},
		{"HGETALL", "k"},
		{"LRANGE", "k", "0", "100"},
		{"SMEMBERS", "k"},
		{"ZRANGE", "k", "0", "100", "WITHSCORES"},
		{"DEL", "k"},
		{"MEMORY", "USAGE", "k"},
		{"OBJECT", "ENCODING", "k"},
		{"SLOWLOG", "GET"},
		{"SLOWLOG", "GET", "50"},
		{"SET", "k", "v"},
		{"SET", "k", "v", "EX", "60"},
		{"HSET", "k", "f", "v"},
		{"RPUSH", "k", "v"},
		{"SADD", "k", "v"},
		{"ZADD", "k", "1", "m"},
		{"EXPIRE", "k", "60"},
		{"PERSIST", "k"},
		{"BGSAVE"},
		{"CONFIG", "GET", "maxmemory"},
		{"CONFIG", "SET", "maxmemory", "256mb"},
		{"CONFIG", "SET", "maxmemory-policy", "allkeys-lru"},
		{"CONFIG", "SET", "timeout", "0"},
	}
	for _, args := range allowed {
		if err := checkCommandAllowed(args); err != nil {
			t.Fatalf("白名单命令 %v 被误拒: %v", args, err)
		}
	}

	denied := []struct {
		args []string
		want string
	}{
		{[]string{"FLUSHALL"}, "不可恢复"},
		{[]string{"FLUSHDB"}, "不可恢复"},
		{[]string{"EVAL", "return 1", "0"}, "任意 Lua"},
		{[]string{"EVALSHA", "abc", "0"}, "任意 Lua"},
		{[]string{"SCRIPT", "FLUSH"}, "脚本"},
		{[]string{"FUNCTION", "LOAD", "x"}, "函数"},
		{[]string{"SHUTDOWN"}, "停止服务"},
		{[]string{"KEYS", "*"}, "阻塞"},
		{[]string{"SAVE"}, "redis.bgsave"},
		{[]string{"DEBUG", "SEGFAULT"}, "调试"},
		{[]string{"CONFIG", "SET", "dir", "/tmp"}, "持久化目录"},
		{[]string{"CONFIG", "SET", "dbfilename", "x.rdb"}, "备份"},
		{[]string{"CONFIG", "SET", "appendonly", "yes"}, "AOF"},
		{[]string{"CONFIG", "SET", "requirepass", "x"}, "密码"},
		{[]string{"CONFIG", "SET", "maxclients", "100"}, "连接数"},
		{[]string{"CONFIG", "SET", "unknown-param", "1"}, "写入白名单"},
		{[]string{"CONFIG", "GET", "requirepass"}, "读取白名单"},
		{[]string{"CONFIG", "REWRITE"}, "只支持 GET"},
		{[]string{"SLOWLOG", "RESET"}, "SLOWLOG 只允许 GET"},
		{[]string{"MEMORY", "STATS"}, "MEMORY 只允许 USAGE"},
		{[]string{"OBJECT", "FREQ", "k"}, "OBJECT 只允许 ENCODING"},
		{[]string{"FOO"}, "不在本插件白名单内"},
	}
	for _, c := range denied {
		err := checkCommandAllowed(c.args)
		if err == nil {
			t.Fatalf("危险命令 %v 未被拒绝", c.args)
		}
		var opErr *OpError
		if !errors.As(err, &opErr) || opErr.Code != pluginapi.E_UNSUPPORTED {
			t.Fatalf("危险命令 %v 错误码=%v，期望 E_UNSUPPORTED", c.args, err)
		}
		if !strings.Contains(opErr.Message, c.want) {
			t.Fatalf("危险命令 %v 错误信息 %q 未说明原因（期望含 %q）", c.args, opErr.Message, c.want)
		}
	}
}

// TestDangerousMethodsDenied 危险方法名一律拒绝，错误信息说明原因与替代做法（E_NOTFOUND 保持未知方法语义）。
func TestDangerousMethodsDenied(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{"redis.flushall", "不可恢复"},
		{"redis.flushdb", "不可恢复"},
		{"redis.eval", "任意脚本"},
		{"redis.evalsha", "任意脚本"},
		{"redis.script", "脚本管理"},
		{"redis.function", "函数"},
		{"redis.save", "redis.bgsave"},
		{"redis.shutdown", "systemctl"},
		{"redis.debug", "不开放"},
		{"redis.config", "serverConfig"},
	}
	for _, c := range cases {
		_, perr := Call(c.method, json.RawMessage(`{}`), testInstances(), nil)
		if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("%s 错误=%v，期望 E_NOTFOUND（未知方法）", c.method, perr)
		}
		if !strings.Contains(perr.Message, "未知方法") || !strings.Contains(perr.Message, c.want) {
			t.Fatalf("%s 错误信息 %q 未说明拒绝原因（期望含 %q）", c.method, perr.Message, c.want)
		}
	}
}

// TestServerConfigSetParamWhitelist CONFIG SET 参数级白名单：危险参数 E_UNSUPPORTED 带原因，未知参数 E_INVALID。
func TestServerConfigSetParamWhitelist(t *testing.T) {
	cases := []struct {
		name   string
		params string
		code   string
		want   string
	}{
		{"dir", `{"dir":"/tmp"}`, pluginapi.E_UNSUPPORTED, "持久化目录"},
		{"appendonly", `{"appendonly":"yes"}`, pluginapi.E_UNSUPPORTED, "AOF"},
		{"save", `{"save":"60 1"}`, pluginapi.E_UNSUPPORTED, "快照"},
		{"requirepass", `{"requirepass":"x"}`, pluginapi.E_UNSUPPORTED, "密码"},
		{"dbfilename", `{"dbfilename":"x.rdb"}`, pluginapi.E_UNSUPPORTED, "RDB"},
		{"unknown", `{"command":"FLUSHALL"}`, pluginapi.E_INVALID, "未知参数"},
		{"bad_maxmemory", `{"maxmemory":"-1"}`, pluginapi.E_INVALID, "maxmemory"},
		{"bad_policy", `{"maxmemory_policy":"bogus"}`, pluginapi.E_INVALID, "maxmemory_policy"},
		{"bad_timeout", `{"timeout":-1}`, pluginapi.E_INVALID, "timeout"},
		{"empty", `{}`, pluginapi.E_INVALID, "至少提供一个"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// exec=nil：校验必须发生在建连之前，否则会尝试连接并返回 E_UPSTREAM。
			_, perr := Call("redis.serverConfig.set", json.RawMessage(c.params), testInstances(), nil)
			if perr == nil || perr.Code != c.code {
				t.Fatalf("serverConfig.set(%s) 错误=%v，期望 %s", c.params, perr, c.code)
			}
			if !strings.Contains(perr.Message, c.want) {
				t.Fatalf("serverConfig.set(%s) 错误信息 %q 未含 %q", c.params, perr.Message, c.want)
			}
		})
	}
}

// TestNewMethodsRejectArbitraryCommand 新方法严格解码：夹带 command/argv 等字段必须显式失败。
func TestNewMethodsRejectArbitraryCommand(t *testing.T) {
	cases := []struct {
		method string
		params string
	}{
		{"redis.set", `{"key":"k","value":"v","command":"FLUSHALL"}`},
		{"redis.set", `{"key":"k","value":"v","argv":["FLUSHALL"]}`},
		{"redis.ttl", `{"key":"k","command":"FLUSHALL"}`},
		{"redis.expire", `{"key":"k","ttl_s":60,"script":"return 1"}`},
		{"redis.persist", `{"key":"k","eval":"return 1"}`},
		{"redis.bgsave", `{"command":"FLUSHALL"}`},
		{"redis.serverConfig.get", `{"command":"CONFIG SET dir /tmp"}`},
	}
	for _, c := range cases {
		_, perr := Call(c.method, json.RawMessage(c.params), testInstances(), nil)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s(%s) 错误=%v，期望 E_INVALID（未知字段必须显式失败）", c.method, c.params, perr)
		}
		if !strings.Contains(perr.Message, "不接受任意命令") {
			t.Fatalf("%s 错误信息 %q 未说明不接受任意命令", c.method, perr.Message)
		}
	}
}

// TestTransportWhitelistBlocksFlushall 端到端证明：白名单外命令到不了网络层（假服务端收不到）。
func TestTransportWhitelistBlocksFlushall(t *testing.T) {
	server := startFakeRedis(t, func(command []string) (any, error) {
		return "OK", nil
	})
	client := NewClient()
	conn, err := client.dial(server.Instance())
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.send("FLUSHALL"); err == nil {
		t.Fatal("FLUSHALL 应被传输层拒绝")
	}
	if commands := server.CommandList(); len(commands) != 0 {
		t.Fatalf("被拒绝的命令不应到达服务端，实际收到: %v", commands)
	}
}
