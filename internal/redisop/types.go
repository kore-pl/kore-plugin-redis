// Package redisop 实现 redis 插件的方法面：参数校验、内存策略/前缀约束与结构化结果。
// 本包只做逻辑校验与执行面调用，不连接任何 Redis 实例（骨架口径 / 内核零依赖）。
package redisop

import "fmt"

// 骨架口径下的约束上限，均为显式常量，避免魔法值。
const (
	maxPrefixLen = 256
	maxScanLimit = 10000
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

// Instance 是实例列表中的一项逻辑条目；addr_hint 只是占位提示，不含真实连接信息。
type Instance struct {
	ID         string `json:"id"`
	AddrHint   string `json:"addr_hint"`
	Policy     string `json:"policy"`
	Status     string `json:"status"`
	Externally bool   `json:"externally_managed"`
}

// KeyEntry 是键扫描结果中的一项逻辑条目。
type KeyEntry struct {
	Name     string `json:"name"`
	TypeHint string `json:"type_hint"`
	TTLHintS int64  `json:"ttl_hint_s"`
}

// Executor 是 Redis 管理的执行面；骨架实现为 noop，测试可注入故障执行器。
// 骨架口径下不发起任何网络连接，方法只返回结构化占位。
type Executor interface {
	Instances(defaultPrefix string) ([]Instance, error)
	Scan(prefix, policy string, limit, cursor int) ([]KeyEntry, error)
}

// instancesParams / scanParams 是两个方法的显式入参形状。
type instancesParams struct {
	Policy string `json:"policy,omitempty"`
}

type scanParams struct {
	Prefix string `json:"prefix"`
	Policy string `json:"policy"`
	Limit  int    `json:"limit,omitempty"`
	Cursor int    `json:"cursor,omitempty"`
}

// InstancesResult / ScanResult 是两个方法的结构化结果。
type InstancesResult struct {
	Instances         []Instance `json:"instances"`
	Count             int        `json:"count"`
	Policy            string     `json:"policy"`
	ManagedExternally bool       `json:"managed_externally"`
}

type ScanResult struct {
	Prefix  string     `json:"prefix"`
	Policy  string     `json:"policy"`
	Cursor  int        `json:"cursor"`
	Scanned int        `json:"scanned"`
	Keys    []KeyEntry `json:"keys"`
	HasMore bool       `json:"has_more"`
}
