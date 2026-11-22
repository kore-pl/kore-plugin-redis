// Package redisop 实现 redis 插件的方法面：实例解析、最小 RESP 客户端、
// 信息读取、键浏览、取值与删除。
//
// 依赖边界（G-17）：只依赖 kore/pkg/pluginapi 与 Go 标准库。不引入任何
// Redis 客户端库、不自带 redis-server；连接由用户已有实例提供。
package redisop

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"
)

// 方法面的显式上限与超时，避免魔法值。
const (
	// maxPatternLen 是 SCAN MATCH 模式的最大字节数（需求硬约束）。
	maxPatternLen = 256
	// maxKeyLen 是单个键的最大字节数；Redis 理论上可到 512MB，本插件只管理常规键。
	maxKeyLen = 1024
	// maxDeleteKeys 是一次删除的键数量上限（需求硬约束）。
	maxDeleteKeys = 100
	// defaultKeysLimit 是 redis.keys 的默认返回条数。
	defaultKeysLimit = 200
	// maxKeysLimit 是 redis.keys 的单次返回上限。
	maxKeysLimit = 10000
	// maxValueEntries 是 list/set/zset/hash 取值时返回的元素上限，超出标记 truncated。
	maxValueEntries = 100
	// connectTimeout 是 TCP 建连超时。
	connectTimeout = 5 * time.Second
	// commandTimeout 是单次方法调用（含多轮 SCAN）的读写截止时间。
	commandTimeout = 5 * time.Second
	// maxBulkBytes 是单条 RESP bulk 的读取上限，防止异常/恶意服务撑爆内存。
	maxBulkBytes = 8 << 20
	// maxArrayItems 是单条 RESP 数组的元素上限。
	maxArrayItems = 1 << 20
	// maxErrorRunes 是错误信息回传的最大字符数。
	maxErrorRunes = 500
)

// FaultKind 标识一次故障注入的类别。
type FaultKind string

const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，Call 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口，输出 "<类别>: <说明>"。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误。
func NewFaultError(kind FaultKind, message string) *FaultError {
	return &FaultError{Kind: kind, Message: message}
}

// InstanceConfig 是一条 Redis 实例配置。
// 密码只以环境变量名（PasswordEnv）出现，结构体本身不携带明文密码。
type InstanceConfig struct {
	Name        string
	Host        string
	Port        int
	PasswordEnv string
}

// Addr 返回 host:port 形式的连接地址（IPv6 自动加方括号）。
func (i InstanceConfig) Addr() string {
	return net.JoinHostPort(i.Host, strconv.Itoa(i.Port))
}

// InstanceStatus 是 redis.instances 的单实例结果。Error 为空串表示探测成功。
type InstanceStatus struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
	Version   string `json:"version"`
	UptimeS   int64  `json:"uptime_s"`
	Error     string `json:"error"`
}

// InstancesResult 是 redis.instances 的结构化结果。
type InstancesResult struct {
	Instances []InstanceStatus `json:"instances"`
	Count     int              `json:"count"`
}

// InfoServer 是 INFO server 段的结构化字段。
type InfoServer struct {
	Version string `json:"version"`
	UptimeS int64  `json:"uptime_s"`
	TCPPort int    `json:"tcp_port"`
}

// InfoClients 是 INFO clients 段的结构化字段。
type InfoClients struct {
	Connected int64 `json:"connected"`
}

// InfoMemory 是 INFO memory 段的结构化字段。
type InfoMemory struct {
	UsedMemoryHuman     string `json:"used_memory_human"`
	UsedMemoryPeakHuman string `json:"used_memory_peak_human"`
	MaxMemoryHuman      string `json:"maxmemory_human"`
}

// InfoStats 是 INFO stats 段的结构化字段。
type InfoStats struct {
	TotalConnectionsReceived int64 `json:"total_connections_received"`
	TotalCommandsProcessed   int64 `json:"total_commands_processed"`
	KeyspaceHits             int64 `json:"keyspace_hits"`
	KeyspaceMisses           int64 `json:"keyspace_misses"`
}

// InfoPersistence 是 INFO persistence 段的结构化字段。
type InfoPersistence struct {
	RDBlastSaveTime int64 `json:"rdb_last_save_time"`
	AOFEnabled      bool  `json:"aof_enabled"`
}

// InfoResult 是 redis.info 的结构化结果。
// Keyspace 的键是 dbN，值归一为 "keys=<n>,expires=<n>"（丢弃 avg_ttl，保持稳定形状）。
type InfoResult struct {
	Instance    string            `json:"instance"`
	Server      InfoServer        `json:"server"`
	Clients     InfoClients       `json:"clients"`
	Memory      InfoMemory        `json:"memory"`
	Stats       InfoStats         `json:"stats"`
	Persistence InfoPersistence   `json:"persistence"`
	Keyspace    map[string]string `json:"keyspace"`
}

// KeyEntry 是键扫描结果中的一项：类型来自 TYPE，TTL 来自 TTL（-1 永久、-2 已过期）。
type KeyEntry struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTLS int64  `json:"ttl_s"`
}

// KeysResult 是 redis.keys 的结构化结果。
type KeysResult struct {
	Keys      []KeyEntry `json:"keys"`
	Scanned   int        `json:"scanned"`
	Truncated bool       `json:"truncated"`
}

// ZSetEntry 是有序集合的一个成员；score 用字符串保持 Redis 原值（避免 ±inf 破坏 JSON）。
type ZSetEntry struct {
	Member string `json:"member"`
	Score  string `json:"score"`
}

// GetResult 是 redis.get 的结构化结果。
// Value 是随 Type 变化的 JSON 载荷：string→字符串、hash→对象、list/set→数组、
// zset→[{member,score}]；这是 G-24 允许的「方法结果载荷」受控例外。
type GetResult struct {
	Key       string          `json:"key"`
	Type      string          `json:"type"`
	TTLS      int64           `json:"ttl_s"`
	Value     json.RawMessage `json:"value"`
	Truncated bool            `json:"truncated,omitempty"`
}

// DeleteResult 是 redis.delete 的结构化结果。
type DeleteResult struct {
	Deleted int64    `json:"deleted"`
	Keys    []string `json:"keys"`
}

// Executor 是 Redis 管理的执行面：真实实现走 RESP，测试可注入故障。
type Executor interface {
	Instances(instances []InstanceConfig) ([]InstanceStatus, error)
	Info(instance InstanceConfig) (InfoResult, error)
	Keys(instance InstanceConfig, pattern string, limit int) (KeysResult, error)
	Get(instance InstanceConfig, key string) (GetResult, error)
	Delete(instance InstanceConfig, keys []string) (DeleteResult, error)
}

// 方法入参形状：字段显式，禁止用 any / object 逃避结构（G-24）。
type infoParams struct {
	Instance string `json:"instance"`
}

type keysParams struct {
	Instance string `json:"instance"`
	Pattern  string `json:"pattern"`
	Limit    int    `json:"limit"`
}

type getParams struct {
	Instance string `json:"instance"`
	Key      string `json:"key"`
}

type deleteParams struct {
	Instance string   `json:"instance"`
	Keys     []string `json:"keys"`
}
