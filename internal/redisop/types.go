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
	// defaultSlowlogLimit 是 redis.slowlog 的默认返回条数（需求硬约束：SLOWLOG GET 50）。
	defaultSlowlogLimit = 50
	// maxSlowlogLimit 是 redis.slowlog 单次请求的上限；服务端还会受 slowlog-max-len 约束。
	maxSlowlogLimit = 1000
	// maxSlowlogArgRunes 是慢查询单个参数保留的最大字符数，超出截断加省略号（防大值刷屏）。
	maxSlowlogArgRunes = 128
	// maxSlowlogArgs 是慢查询 args_summary 保留的最大参数个数，超出合并为 +N 提示。
	maxSlowlogArgs = 16
	// maxWriteItems 是 redis.set 单次写入的元素个数上限（hash/list/set/zset）。
	maxWriteItems = 1000
	// maxWriteValueBytes 是 redis.set 单个值/字段/成员的字节上限。
	maxWriteValueBytes = 1 << 20
	// maxWriteTotalBytes 是 redis.set 单次写入载荷的总字节上限（防一次请求撑爆连接缓冲）。
	maxWriteTotalBytes = 4 << 20
	// maxTTLSeconds 是 redis.expire 允许的最大 TTL（10 年），防误写成巨大值。
	maxTTLSeconds = 10 * 365 * 24 * 3600
	// maxServerConfigTimeout 是 CONFIG SET timeout 的允许上限（秒，客户端空闲断开）。
	maxServerConfigTimeout = 86400
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
// 密码可来自配置契约（Password，仅默认实例，json 不序列化）或环境变量名
// （PasswordEnv）；两者都不会进入 argv 或任何响应。
type InstanceConfig struct {
	Name        string
	Host        string
	Port        int
	PasswordEnv string
	Password    string
	// DB 是连接后 SELECT 的库号（0 表示默认库，不额外发 SELECT）；来自配置契约的 db。
	DB int
	// TimeoutS 是本次连接与命令的超时秒数（0 表示沿用客户端默认值）；来自配置契约的 timeout_s。
	TimeoutS int
}

// Addr 返回 host:port 形式的连接地址（IPv6 自动加方括号）。
func (i InstanceConfig) Addr() string {
	return net.JoinHostPort(i.Host, strconv.Itoa(i.Port))
}

// InstanceStatus 是 redis.instances 的单实例结果。Error 为空串表示探测成功。
// CredentialSource 是只读诊断：kernel_env / plugin_config / none（不含任何口令内容）。
type InstanceStatus struct {
	Name             string `json:"name"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Reachable        bool   `json:"reachable"`
	Version          string `json:"version"`
	UptimeS          int64  `json:"uptime_s"`
	Error            string `json:"error"`
	CredentialSource string `json:"credential_source"`
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

// PersistenceResult 是 redis.persistence 的结构化结果（INFO persistence）。
// AOFSize 取 INFO 的 aof_current_size（Redis 官方字段名；AOF 关闭时为 0）。
type PersistenceResult struct {
	Instance            string `json:"instance"`
	AOFEnabled          bool   `json:"aof_enabled"`
	AOFLastWriteStatus  string `json:"aof_last_write_status"`
	AOFSize             int64  `json:"aof_size"`
	RDBLastSaveTime     int64  `json:"rdb_last_save_time"`
	RDBChangesSinceSave int64  `json:"rdb_changes_since_save"`
	RDBBgsaveInProgress bool   `json:"rdb_bgsave_in_progress"`
}

// MemoryResult 是 redis.memory 的结构化结果（INFO memory + INFO stats + CONFIG GET）。
// MaxMemory 为 0 表示不设上限；MaxMemoryPolicy 在实例禁用 CONFIG 时降级为空串。
type MemoryResult struct {
	Instance           string  `json:"instance"`
	Used               int64   `json:"used"`
	Peak               int64   `json:"peak"`
	MaxMemory          int64   `json:"maxmemory"`
	MaxMemoryPolicy    string  `json:"maxmemory_policy"`
	FragmentationRatio float64 `json:"fragmentation_ratio"`
	EvictionKeys       int64   `json:"eviction_keys"`
}

// SlowlogEntry 是一条慢查询记录；ArgsSummary 逐参截断（maxSlowlogArgRunes）。
type SlowlogEntry struct {
	ID          int64  `json:"id"`
	TS          int64  `json:"ts"`
	DurationUS  int64  `json:"duration_us"`
	Command     string `json:"command"`
	ArgsSummary string `json:"args_summary"`
}

// SlowlogResult 是 redis.slowlog 的结构化结果；Count 与 Entries 长度一致。
type SlowlogResult struct {
	Entries []SlowlogEntry `json:"entries"`
	Count   int            `json:"count"`
}

// KeyInfoResult 是 redis.keyInfo 的结构化结果（TYPE/TTL/MEMORY USAGE/OBJECT ENCODING）。
// Redis < 4.0 不支持 MEMORY USAGE：MemoryBytes 降级为 0，Encoding 降级为空串。
type KeyInfoResult struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Encoding    string `json:"encoding"`
	TTLS        int64  `json:"ttl_s"`
	MemoryBytes int64  `json:"memory_bytes"`
}

// HashField 是 hash 写入的一个字段（用有序切片而不是 map，保证命令参数顺序可复现）。
type HashField struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// ZSetWrite 是 zset 写入的一个成员；Score 用字符串承载，保留 Redis 原值精度与 inf。
type ZSetWrite struct {
	Member string `json:"member"`
	Score  string `json:"score"`
}

// SetRequest 是 redis.set 的一次写入请求。
// Type 为空表示沿用键既有类型；Value 是由 Type 定形的 JSON 载荷（调用方已做严格解码）。
type SetRequest struct {
	Key   string
	Type  string
	TTLs  int64
	Value json.RawMessage
}

// SetResult 是 redis.set 的结构化结果。
// Written 是 Redis 命令返回的计数：string=1（SET 成功）；hash/set/zset=新增元素数；
// list=写入后的列表长度（RPUSH 语义，如实透传不二次加工）。
type SetResult struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Written int64  `json:"written"`
	TTLs    int64  `json:"ttl_s"`
}

// TTLResult 是 redis.ttl 的结构化结果。
// TTLs 原样透传 Redis TTL：-1=永久（Persistent=true）、0=即将过期、>0=剩余秒。
type TTLResult struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	TTLs       int64  `json:"ttl_s"`
	Persistent bool   `json:"persistent"`
	ExpiresAt  int64  `json:"expires_at"`
}

// ExpireResult 是 redis.expire 的结构化结果；TTLs 是设置后的实际剩余秒数。
type ExpireResult struct {
	Key  string `json:"key"`
	TTLs int64  `json:"ttl_s"`
}

// PersistResult 是 redis.persist 的结构化结果；Persisted=false 表示键本来就没有 TTL。
// TTLs 是执行后的 TTL（持久化成功即 -1）。
type PersistResult struct {
	Key       string `json:"key"`
	Persisted bool   `json:"persisted"`
	TTLs      int64  `json:"ttl_s"`
}

// BgsaveResult 是 redis.bgsave 的结构化结果；Message 原样透传 Redis 状态行。
type BgsaveResult struct {
	Started bool   `json:"started"`
	Message string `json:"message"`
}

// ServerConfig 是允许读写的 Redis 服务端参数白名单（值是 Redis 原样字符串）。
type ServerConfig struct {
	MaxMemory       string `json:"maxmemory"`
	MaxMemoryPolicy string `json:"maxmemory_policy"`
	Timeout         string `json:"timeout"`
}

// ServerConfigResult 是 redis.serverConfig.get 的结构化结果。
type ServerConfigResult struct {
	Instance string       `json:"instance"`
	Config   ServerConfig `json:"config"`
}

// ServerConfigPatch 是 redis.serverConfig.set 的变更集；nil 表示该参数不改。
type ServerConfigPatch struct {
	MaxMemory       *string
	MaxMemoryPolicy *string
	Timeout         *string
}

// ServerConfigSetResult 是 redis.serverConfig.set 的结构化结果：
// Changed 是本次提交的参数（JSON 字段名），Config 是回读后的实际生效值。
type ServerConfigSetResult struct {
	Instance string       `json:"instance"`
	Changed  []string     `json:"changed"`
	Config   ServerConfig `json:"config"`
}

// Executor 是 Redis 管理的执行面：真实实现走 RESP，测试可注入故障。
type Executor interface {
	Instances(instances []InstanceConfig) ([]InstanceStatus, error)
	Info(instance InstanceConfig) (InfoResult, error)
	Keys(instance InstanceConfig, pattern string, limit int) (KeysResult, error)
	Get(instance InstanceConfig, key string) (GetResult, error)
	Delete(instance InstanceConfig, keys []string) (DeleteResult, error)
	Persistence(instance InstanceConfig) (PersistenceResult, error)
	Memory(instance InstanceConfig) (MemoryResult, error)
	Slowlog(instance InstanceConfig, limit int) (SlowlogResult, error)
	KeyInfo(instance InstanceConfig, key string) (KeyInfoResult, error)
	Set(instance InstanceConfig, request SetRequest) (SetResult, error)
	TTL(instance InstanceConfig, key string) (TTLResult, error)
	Expire(instance InstanceConfig, key string, ttlS int64) (ExpireResult, error)
	Persist(instance InstanceConfig, key string) (PersistResult, error)
	Bgsave(instance InstanceConfig) (BgsaveResult, error)
	ServerConfigGet(instance InstanceConfig) (ServerConfigResult, error)
	ServerConfigSet(instance InstanceConfig, patch ServerConfigPatch) (ServerConfigSetResult, error)
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

type slowlogParams struct {
	Instance string `json:"instance"`
	Limit    int    `json:"limit"`
}

// setParams 是 redis.set 的入参；TTLs 用指针区分「不传」与「传 0」，负数一律拒绝。
type setParams struct {
	Instance string          `json:"instance"`
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	TTLs     *int64          `json:"ttl_s"`
	Value    json.RawMessage `json:"value"`
}

// ttlParams / persistParams 与 get 同形（instance + key）。
type ttlParams struct {
	Instance string `json:"instance"`
	Key      string `json:"key"`
}

type persistParams struct {
	Instance string `json:"instance"`
	Key      string `json:"key"`
}

// expireParams 要求显式 ttl_s：只接受正整数（去 TTL 用 redis.persist）。
type expireParams struct {
	Instance string `json:"instance"`
	Key      string `json:"key"`
	TTLs     *int64 `json:"ttl_s"`
}

// keyInfo 与 get 的入参形状一致（instance + key），复用 getParams。
