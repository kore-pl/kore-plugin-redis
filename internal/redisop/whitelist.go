package redisop

import (
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 本文件是「命令白名单」的唯一真源（需求硬约束：不能用户传什么就往 Redis 发什么）。
//
// 三层防线，任一层命中都本地拒绝、不建立「先发出去再看服务端」的路径：
//  1. 方法面：只注册 16 个业务方法（redisop.Methods），没有任意命令 / 脚本入口；
//  2. 参数面：新方法用严格解码拒绝未知字段；CONFIG SET 只接受白名单参数，
//     明确列出的危险参数返回 E_UNSUPPORTED + 可操作原因；
//  3. 传输面：respConn.send 在发出前逐条校验命令名与危险子命令（本文件），
//     白名单外的命令无法通过网络层，即使将来新增方法写错参数也不会透传。

// allowedCommands 是本插件允许发往 Redis 的全部命令名（大写）。
// 新增方法必须先在这里登记命令，再在方法实现里使用——不允许把用户输入拼成命令名。
var allowedCommands = map[string]struct{}{
	// 连接与探测
	"AUTH": {}, "PING": {}, "SELECT": {}, "INFO": {},
	// 键空间读取
	"SCAN": {}, "TYPE": {}, "TTL": {}, "GET": {}, "HGETALL": {},
	"LRANGE": {}, "SMEMBERS": {}, "ZRANGE": {}, "DEL": {},
	// 键详情
	"MEMORY": {}, "OBJECT": {},
	// 慢查询
	"SLOWLOG": {},
	// 写入（redis.set / redis.expire / redis.persist）
	"SET": {}, "HSET": {}, "RPUSH": {}, "SADD": {}, "ZADD": {},
	"EXPIRE": {}, "PERSIST": {},
	// 持久化（redis.bgsave）
	"BGSAVE": {},
	// 服务端配置（redis.serverConfig.get/set，参数另行白名单）
	"CONFIG": {},
}

// deniedCommandReasons 是明确拒绝的命令及可操作原因；命中即 E_UNSUPPORTED。
// 这些命令即使被误加入 allowedCommands，也会先在这里被拒绝（测试双向断言）。
var deniedCommandReasons = map[string]string{
	"FLUSHALL":  "清空全部数据库不可恢复；如需按前缀清理，请用 redis.keys（SCAN）逐键确认后调用 redis.delete",
	"FLUSHDB":   "清空当前数据库不可恢复；如需按前缀清理，请用 redis.keys（SCAN）逐键确认后调用 redis.delete",
	"EVAL":      "不允许在服务端执行任意 Lua 脚本（可绕过命令白名单）；派生值请在客户端计算",
	"EVALSHA":   "不允许在服务端执行任意 Lua 脚本（可绕过命令白名单）；派生值请在客户端计算",
	"SCRIPT":    "不允许管理服务端脚本缓存（EVAL 的配套入口）；派生值请在客户端计算",
	"FUNCTION":  "不允许注册或调用服务端函数（可绕过命令白名单）",
	"SHUTDOWN":  "会让 Redis 停止服务；请用系统服务管理器（如 systemctl restart redis）",
	"KEYS":      "KEYS 会阻塞 Redis 主线程；请用 redis.keys（SCAN 游标遍历）",
	"SAVE":      "SAVE 会阻塞主线程直到落盘完成；请用 redis.bgsave 在后台落盘",
	"DEBUG":     "调试命令可读取内存、修改运行时行为；不开放",
	"SLAVEOF":   "复制拓扑属于运维变更；请在 redis.conf / 服务管理器调整",
	"REPLICAOF": "复制拓扑属于运维变更；请在 redis.conf / 服务管理器调整",
	"MONITOR":   "MONITOR 会持续占用连接并回放全部命令（可能含凭据）；不开放",
	"ACL":       "ACL 变更会改账号权限；请在 redis.conf / 服务管理器调整",
	"MODULE":    "加载模块可在服务端执行任意代码；不开放",
	"CLUSTER":   "集群拓扑变更不属于单实例管理范围；不开放",
	"MIGRATE":   "跨实例迁移会改动非本插件管理的键；不开放",
	"SWAPDB":    "换库会波及本插件之外的键；不开放",
}

// allowedServerConfigRead / allowedServerConfigWrite 是 CONFIG 子命令的参数白名单。
// 读写共用同一组（都是可安全在线调整的运行参数）；新增参数必须同时改两处与文档。
var (
	allowedServerConfigRead = map[string]struct{}{
		"maxmemory": {}, "maxmemory-policy": {}, "timeout": {},
	}
	allowedServerConfigWrite = map[string]struct{}{
		"maxmemory": {}, "maxmemory-policy": {}, "timeout": {},
	}
)

// serverConfigNames 是 serverConfig.get/set 的参数顺序（固定顺序保证命令序列可复现）。
var serverConfigNames = []string{"maxmemory", "maxmemory-policy", "timeout"}

// serverConfigJSONKey 把 Redis 参数名映射到对外 JSON 字段名（下划线风格）。
var serverConfigJSONKey = map[string]string{
	"maxmemory":        "maxmemory",
	"maxmemory-policy": "maxmemory_policy",
	"timeout":          "timeout",
}

// deniedServerConfigReasons 是明确拒绝的 CONFIG SET 参数及可操作原因。
// 命中即 E_UNSUPPORTED：不静默忽略、不透传，错误信息说明「为什么」与「怎么办」。
var deniedServerConfigReasons = map[string]string{
	"dir":            "持久化目录变更会让后续落盘写到新路径，可能与现有备份/权限冲突；请改 redis.conf 并重启 Redis",
	"dbfilename":     "RDB 文件名变更会影响现有备份与恢复路径；请改 redis.conf 并重启 Redis",
	"appendonly":     "开关 AOF 会触发重写并改变持久化语义；请改 redis.conf 并在变更窗口内操作",
	"appendfsync":    "AOF 落盘策略影响数据安全等级；请改 redis.conf",
	"save":           "RDB 自动快照规则影响持久化行为；请改 redis.conf 或用 redis.bgsave 手动落盘",
	"requirepass":    "在线改密码会让已连接客户端立即认证失败；请改 redis.conf 并在维护窗口重启",
	"masterauth":     "复制密码变更属于拓扑配置；请改 redis.conf",
	"rename-command": "重命名/禁用命令会改变服务端命令面；请改 redis.conf 并重启 Redis",
	"protected-mode": "保护模式属于网络暴露策略；请改 redis.conf",
	"bind":           "监听地址变更会改变暴露面；请改 redis.conf 并重启 Redis",
	"maxclients":     "连接数上限应结合系统 ulimit 一起调整；请改 redis.conf",
	"databases":      "数据库数量只能在重启后生效；请改 redis.conf",
}

// checkCommandAllowed 是传输面白名单校验：命令名 + 危险子命令都必须命中白名单。
func checkCommandAllowed(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return opErrorf(pluginapi.E_INVALID, "命令为空：本插件只发送方法契约里声明的固定命令")
	}
	command := strings.ToUpper(args[0])
	if reason, denied := deniedCommandReasons[command]; denied {
		return opErrorf(pluginapi.E_UNSUPPORTED, "拒绝执行命令 %s：%s", command, reason)
	}
	if _, ok := allowedCommands[command]; !ok {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"命令 %s 不在本插件白名单内：插件只发送方法契约里声明过的固定命令", command)
	}
	switch command {
	case "CONFIG":
		return checkConfigArgs(args)
	case "SLOWLOG":
		if (len(args) != 2 && len(args) != 3) || !strings.EqualFold(args[1], "GET") {
			return opErrorf(pluginapi.E_UNSUPPORTED,
				"SLOWLOG 只允许 GET [<limit>]（读取慢查询），收到 %v；SLOWLOG RESET 等写操作不开放", args)
		}
		if len(args) == 3 && !allDigits(args[2]) {
			return opErrorf(pluginapi.E_UNSUPPORTED, "SLOWLOG GET 的 limit 必须是整数，收到 %q", args[2])
		}
	case "MEMORY":
		if len(args) != 3 || !strings.EqualFold(args[1], "USAGE") {
			return opErrorf(pluginapi.E_UNSUPPORTED,
				"MEMORY 只允许 USAGE <key>（只读估算），收到 %v；MEMORY DOCTOR/STATS/PURGE 不开放", args)
		}
	case "OBJECT":
		if len(args) != 3 || !strings.EqualFold(args[1], "ENCODING") {
			return opErrorf(pluginapi.E_UNSUPPORTED,
				"OBJECT 只允许 ENCODING <key>（只读编码），收到 %v；OBJECT FREQ/REFCOUNT/IDLETIME 不开放", args)
		}
	case "INFO":
		if len(args) > 2 {
			return opErrorf(pluginapi.E_UNSUPPORTED, "INFO 最多带一个段名，收到 %v", args)
		}
	}
	return nil
}

// checkConfigArgs 校验 CONFIG 子命令：GET 只读白名单参数、SET 只写白名单参数。
func checkConfigArgs(args []string) error {
	if len(args) < 3 {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"CONFIG 只支持 GET <参数> 与 SET <参数> <值>（参数白名单：maxmemory / maxmemory-policy / timeout）")
	}
	subcommand := strings.ToUpper(args[1])
	name := strings.ToLower(args[2])
	switch subcommand {
	case "GET":
		if len(args) != 3 {
			return opErrorf(pluginapi.E_UNSUPPORTED, "CONFIG GET 只允许单个参数，收到 %v", args)
		}
		if _, ok := allowedServerConfigRead[name]; !ok {
			return opErrorf(pluginapi.E_UNSUPPORTED,
				"CONFIG GET %s 不在读取白名单内（只允许 maxmemory / maxmemory-policy / timeout）", name)
		}
		return nil
	case "SET":
		if len(args) != 4 {
			return opErrorf(pluginapi.E_UNSUPPORTED, "CONFIG SET 需要且只需要一个参数值，收到 %v", args)
		}
		if reason, denied := deniedServerConfigReasons[name]; denied {
			return opErrorf(pluginapi.E_UNSUPPORTED, "拒绝 CONFIG SET %s：%s", name, reason)
		}
		if _, ok := allowedServerConfigWrite[name]; !ok {
			return opErrorf(pluginapi.E_UNSUPPORTED,
				"CONFIG SET %s 不在写入白名单内（只允许 maxmemory / maxmemory-policy / timeout）；"+
					"其它参数请改 redis.conf 并重启 Redis", name)
		}
		return nil
	default:
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"CONFIG 只支持 GET / SET，收到 %q；CONFIG REWRITE / RESETSTAT 等不开放", args[1])
	}
}

// allDigits 判断字符串是否非空且全为数字（SLOWLOG GET 的 limit 参数）。
func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
