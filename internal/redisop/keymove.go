package redisop

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 键重命名 / 同实例跨库迁移 / 复制（redis.rename / move / copy）。
//
// 三道门控，全部在触达 Redis 之前判定：
//  1. 覆盖默认禁止：rename 默认 nx=true（RENAMENX），copy 默认不 REPLACE；
//     显式覆盖必须 nx:false（或 replace:true）**且** confirm:true；
//  2. 大键保护：MEMORY USAGE > maxBigKeyBytes 时 rename/copy 必须 confirm:true，
//     提示大键操作可能阻塞（COPY 还要复制整个对象，比 RENAME 更重）；
//  3. 跨实例拒绝：本插件不做实例间搬运（MIGRATE 在拒绝清单里），
//     跨实例需求要用导出/读值再写入——错误里直接说清。

// maxBigKeyBytes 是大键门控阈值（8 MiB，与查询结果总预算同量级）。
const maxBigKeyBytes = 8 * 1024 * 1024

// bigKeyHint 是大键门控命中时给用户的说明。
const bigKeyHint = "目标键超过 8 MiB：RENAME 本身是 O(1)，但 COPY 会复制整个对象、且大键的删除/复制可能阻塞主线程；确认后请传 confirm:true"

// rejectCrossInstance 拒绝跨实例操作：本插件只管理同一实例内的键空间。
func rejectCrossInstance(source, target string) *pluginapi.Error {
	target = strings.TrimSpace(target)
	if target == "" || target == source {
		return nil
	}
	return &pluginapi.Error{
		Code: pluginapi.E_UNSUPPORTED,
		Message: "不支持跨实例搬运键（" + source + " → " + target + "）：本插件不开放 MIGRATE，" +
			"请用 redis.keys / redis.get 读出后写入目标实例（大批量请走导出导入）",
	}
}

// bigKeyGate 读取键内存占用；超阈值且未确认时拒绝。取不到大小时放行——
// 老版本 Redis 没有 MEMORY USAGE，不能因为拿不到诊断信息就把可用的 RENAME 锁死。
func bigKeyGate(exec Executor, instance InstanceConfig, key string, confirm bool) *pluginapi.Error {
	size, err := exec.KeyBytes(instance, key)
	if err != nil || size <= maxBigKeyBytes {
		return nil
	}
	if confirm {
		return nil
	}
	return invalid(bigKeyHint)
}

// callRename 处理 redis.rename：默认 RENAMENX（目标已存在则 renamed=false，不覆盖）。
func callRename(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p renameParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := rejectCrossInstance(instance.Name, p.TargetInstance); perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.NewKey); perr != nil {
		return nil, perr
	}
	if p.Key == p.NewKey {
		return nil, invalid("new_key 不能与 key 相同（改名目标必须是不同的键）")
	}
	nx := true
	if p.NX != nil {
		nx = *p.NX
	}
	if !nx && !p.Confirm {
		return nil, invalid("覆盖已有键不可恢复：nx:false 必须同时传 confirm:true")
	}
	if perr := bigKeyGate(exec, instance, p.Key, p.Confirm); perr != nil {
		return nil, perr
	}
	result, err := exec.Rename(instance, p.Key, p.NewKey, nx)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// callMove 处理 redis.move：同实例内跨 db 移动；目标库已有同名键时 moved=false。
func callMove(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p moveParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := rejectCrossInstance(instance.Name, p.TargetInstance); perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	if p.DB < 0 || p.DB > maxMoveDBIndex {
		return nil, invalid("db 必须在 0-" + strconv.Itoa(maxMoveDBIndex) + " 之间（Redis 默认 16 个库）")
	}
	result, err := exec.Move(instance, p.Key, p.DB)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// maxMoveDBIndex 是本地允许的最大库号；Redis 实际库数由 databases 配置决定，
// 超出的库号由服务端返回 ERR DB index is out of range（映射为 E_UPSTREAM）。
const maxMoveDBIndex = 1024

// callCopy 处理 redis.copy：默认不覆盖（不带 REPLACE），目标已存在时 copied=false。
func callCopy(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p copyParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	if perr := rejectCrossInstance(instance.Name, p.TargetInstance); perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Key); perr != nil {
		return nil, perr
	}
	if perr := validateKey(p.Dest); perr != nil {
		return nil, perr
	}
	if p.Key == p.Dest {
		return nil, invalid("dest 不能与 key 相同（复制目标是另一个键）")
	}
	destDB := -1
	if p.DB != nil {
		if *p.DB < 0 || *p.DB > maxMoveDBIndex {
			return nil, invalid("db 必须在 0-" + strconv.Itoa(maxMoveDBIndex) + " 之间")
		}
		destDB = *p.DB
	}
	if p.Replace && !p.Confirm {
		return nil, invalid("replace:true 会覆盖目标键且不可恢复：必须同时传 confirm:true")
	}
	if perr := bigKeyGate(exec, instance, p.Key, p.Confirm); perr != nil {
		return nil, perr
	}
	result, err := exec.Copy(instance, p.Key, p.Dest, destDB, p.Replace)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// KeyBytes 读取键的内存占用（MEMORY USAGE）；Redis < 4.0 或命令不可用时返回 0，
// 由调用方按「取不到大小就放行」处理。
func (c *Client) KeyBytes(instance InstanceConfig, key string) (int64, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	reply, err := conn.do("MEMORY", "USAGE", key)
	if err != nil {
		if _, isRedisErr := err.(redisError); isRedisErr {
			return 0, nil
		}
		return 0, upstreamError(err, "MEMORY USAGE 读取失败")
	}
	if size, ok := asInt(reply); ok {
		return size, nil
	}
	return 0, nil
}

// Rename 执行 RENAME / RENAMENX；nx=true 时目标已存在返回 renamed=false。
func (c *Client) Rename(instance InstanceConfig, key, newKey string, nx bool) (RenameResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return RenameResult{}, err
	}
	defer func() { _ = conn.Close() }()
	result := RenameResult{Key: key, NewKey: newKey, NX: nx}
	if nx {
		reply, err := conn.do("RENAMENX", key, newKey)
		if err != nil {
			return RenameResult{}, upstreamError(err, "RENAMENX 失败")
		}
		count, ok := asInt(reply)
		if !ok {
			return RenameResult{}, opErrorf(pluginapi.E_UPSTREAM, "RENAMENX 返回非整数")
		}
		if count == 1 {
			result.Renamed = true
			return result, nil
		}
		result.TargetExisted = true
		result.Hint = "目标键已存在，未覆盖（nx 默认开启）；要覆盖请传 nx:false 且 confirm:true"
		return result, nil
	}
	reply, err := conn.do("RENAME", key, newKey)
	if err != nil {
		return RenameResult{}, upstreamError(err, "RENAME 失败")
	}
	if text, ok := asString(reply); !ok || text != "OK" {
		return RenameResult{}, opErrorf(pluginapi.E_UPSTREAM, "RENAME 返回非 OK")
	}
	result.Renamed = true
	return result, nil
}

// Move 执行 MOVE：目标库已有同名键时返回 moved=false（不覆盖）。
func (c *Client) Move(instance InstanceConfig, key string, db int) (MoveResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return MoveResult{}, err
	}
	defer func() { _ = conn.Close() }()
	reply, err := conn.do("MOVE", key, strconv.Itoa(db))
	if err != nil {
		return MoveResult{}, upstreamError(err, "MOVE 失败")
	}
	count, ok := asInt(reply)
	if !ok {
		return MoveResult{}, opErrorf(pluginapi.E_UPSTREAM, "MOVE 返回非整数")
	}
	result := MoveResult{Key: key, DB: db, Moved: count == 1}
	if !result.Moved {
		result.Hint = "目标库已有同名键或源键不存在：MOVE 不覆盖；先确认键存在，或改用 redis.copy（replace:true + confirm:true）"
	}
	return result, nil
}

// Copy 执行 COPY：默认不覆盖；destDB<0 表示同库复制。
func (c *Client) Copy(instance InstanceConfig, key, dest string, destDB int, replace bool) (CopyResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return CopyResult{}, err
	}
	defer func() { _ = conn.Close() }()
	args := []string{"COPY", key, dest}
	if destDB >= 0 {
		args = append(args, "DB", strconv.Itoa(destDB))
	}
	if replace {
		args = append(args, "REPLACE")
	}
	reply, err := conn.do(args...)
	if err != nil {
		return CopyResult{}, upstreamError(err, "COPY 失败")
	}
	count, ok := asInt(reply)
	if !ok {
		return CopyResult{}, opErrorf(pluginapi.E_UPSTREAM, "COPY 返回非整数")
	}
	result := CopyResult{Key: key, Dest: dest, DestDB: destDB, Copied: count == 1, Replaced: replace}
	if !result.Copied {
		result.Hint = "目标键已存在：COPY 默认不覆盖；需要覆盖请传 replace:true 且 confirm:true"
	}
	return result, nil
}
