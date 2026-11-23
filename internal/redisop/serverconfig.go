package redisop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// maxmemoryValueRE 是 CONFIG SET maxmemory 允许的值形态：字节整数或 kb/mb/gb 后缀。
var maxmemoryValueRE = regexp.MustCompile(`(?i)^[0-9]+(kb|mb|gb)?$`)

// allowedMaxMemoryPolicies 是 Redis 全部 8 种淘汰策略（CONFIG SET maxmemory-policy 白名单）。
var allowedMaxMemoryPolicies = map[string]struct{}{
	"volatile-lru": {}, "allkeys-lru": {}, "volatile-lfu": {}, "allkeys-lfu": {},
	"volatile-random": {}, "allkeys-random": {}, "volatile-ttl": {}, "noeviction": {},
}

// serverConfigAllowedKeys 是 redis.serverConfig.set 接受的 JSON 参数（instance + 三个白名单参数）。
var serverConfigAllowedKeys = []string{"instance", "maxmemory", "maxmemory_policy", "timeout"}

// callServerConfigGet 处理 redis.serverConfig.get：只读三个白名单参数。
func callServerConfigGet(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p infoParams
	if perr := decodeStrictParams(raw, &p); perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, p.Instance)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.ServerConfigGet(instance)
	if err != nil {
		return nil, classify(err)
	}
	return encode(result)
}

// callServerConfigSet 处理 redis.serverConfig.set：参数级白名单 + 值合法性校验后下发 CONFIG SET。
//
// 白名单外参数分两类处理，都不静默忽略：
//   - 明确危险（dir / appendonly / requirepass / save …）→ E_UNSUPPORTED + 原因（deniedServerConfigReasons）；
//   - 未知 → E_INVALID + 允许清单。
func callServerConfigSet(raw json.RawMessage, instances []InstanceConfig, exec Executor) (json.RawMessage, *pluginapi.Error) {
	instanceName, patch, changed, perr := decodeServerConfigPatch(raw)
	if perr != nil {
		return nil, perr
	}
	instance, perr := findInstance(instances, instanceName)
	if perr != nil {
		return nil, perr
	}
	result, err := exec.ServerConfigSet(instance, patch)
	if err != nil {
		return nil, classify(err)
	}
	if len(result.Changed) == 0 {
		result.Changed = changed
	}
	if result.Changed == nil {
		result.Changed = []string{}
	}
	return encode(result)
}

// decodeServerConfigPatch 解析并校验 redis.serverConfig.set 的参数。
// 解析期用 map[string]json.RawMessage 承载异构参数（G-24 受控例外：就地收口为显式结构），
// 每个键都必须命中白名单或被显式拒绝。
func decodeServerConfigPatch(raw json.RawMessage) (string, ServerConfigPatch, []string, *pluginapi.Error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", ServerConfigPatch{}, nil, invalid("缺少必填参数对象")
	}
	if trimmed[0] != '{' {
		return "", ServerConfigPatch{}, nil, invalid("params 必须是 JSON 对象")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return "", ServerConfigPatch{}, nil, invalid("params 解析失败: " + err.Error())
	}
	// 固定顺序处理键，保证同样的输入总得到同一条错误（map 遍历随机）。
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	instanceName := ""
	patch := ServerConfigPatch{}
	changed := make([]string, 0, 3)
	for _, key := range keys {
		value := fields[key]
		switch key {
		case "instance":
			if err := strictJSON(value, &instanceName); err != nil {
				return "", ServerConfigPatch{}, nil, invalid("instance 必须是字符串: " + err.Error())
			}
			if instanceName != "" {
				if err := ValidateInstanceName(instanceName); err != nil {
					return "", ServerConfigPatch{}, nil, invalid(err.Error())
				}
			}
		case "maxmemory":
			text, perr := decodeConfigString(value, "maxmemory")
			if perr != nil {
				return "", ServerConfigPatch{}, nil, perr
			}
			if !maxmemoryValueRE.MatchString(text) {
				return "", ServerConfigPatch{}, nil, invalid(
					"maxmemory 必须是非负整数或带 kb/mb/gb 后缀（如 \"268435456\" / \"256mb\"），收到 " + strconv.Quote(text))
			}
			if patch.MaxMemory != nil {
				return "", ServerConfigPatch{}, nil, invalid("maxmemory 重复提交")
			}
			normalized := strings.ToLower(text)
			patch.MaxMemory = &normalized
			changed = append(changed, "maxmemory")
		case "maxmemory_policy":
			text, perr := decodeConfigString(value, "maxmemory_policy")
			if perr != nil {
				return "", ServerConfigPatch{}, nil, perr
			}
			if _, ok := allowedMaxMemoryPolicies[text]; !ok {
				return "", ServerConfigPatch{}, nil, invalid(
					"maxmemory_policy=" + text + " 非法：只允许 volatile-lru / allkeys-lru / volatile-lfu / allkeys-lfu / " +
						"volatile-random / allkeys-random / volatile-ttl / noeviction")
			}
			patch.MaxMemoryPolicy = &text
			changed = append(changed, "maxmemory_policy")
		case "timeout":
			var seconds int
			if err := strictJSON(value, &seconds); err != nil {
				return "", ServerConfigPatch{}, nil, invalid("timeout 必须是整数秒: " + err.Error())
			}
			if seconds < 0 || seconds > maxServerConfigTimeout {
				return "", ServerConfigPatch{}, nil, invalid(fmt.Sprintf(
					"timeout=%d 超出 [0, %d] 秒（客户端空闲断开时间，0 表示不主动断开）", seconds, maxServerConfigTimeout))
			}
			text := strconv.Itoa(seconds)
			patch.Timeout = &text
			changed = append(changed, "timeout")
		default:
			name := strings.ToLower(key)
			if reason, denied := deniedServerConfigReasons[name]; denied {
				return "", ServerConfigPatch{}, nil, &pluginapi.Error{
					Code: pluginapi.E_UNSUPPORTED,
					Message: "拒绝 CONFIG SET " + name + "：" + reason +
						"；本方法只允许 maxmemory / maxmemory_policy / timeout",
				}
			}
			return "", ServerConfigPatch{}, nil, invalid(fmt.Sprintf(
				"未知参数 %q：本方法只接受 %s（其它服务端参数请改 redis.conf 并重启 Redis）",
				key, strings.Join(serverConfigAllowedKeys, " / ")))
		}
	}
	if len(changed) == 0 {
		return "", ServerConfigPatch{}, nil, invalid(
			"至少提供一个要修改的参数：maxmemory / maxmemory_policy / timeout")
	}
	return instanceName, patch, changed, nil
}

// decodeConfigString 解码字符串类配置值。
func decodeConfigString(raw json.RawMessage, name string) (string, *pluginapi.Error) {
	var text string
	if err := strictJSON(raw, &text); err != nil {
		return "", invalid(name + " 必须是字符串: " + err.Error())
	}
	return text, nil
}

// ServerConfigGet 读取三个白名单参数；实例禁用 CONFIG（云托管常见）时返回可操作错误。
func (c *Client) ServerConfigGet(instance InstanceConfig) (ServerConfigResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return ServerConfigResult{}, err
	}
	defer func() { _ = conn.Close() }()

	config, err := readServerConfig(conn)
	if err != nil {
		return ServerConfigResult{}, err
	}
	return ServerConfigResult{Instance: instance.Name, Config: config}, nil
}

// ServerConfigSet 逐条下发 CONFIG SET 并回读三个参数；失败信息带参数名。
func (c *Client) ServerConfigSet(instance InstanceConfig, patch ServerConfigPatch) (ServerConfigSetResult, error) {
	conn, err := c.dial(instance)
	if err != nil {
		return ServerConfigSetResult{}, err
	}
	defer func() { _ = conn.Close() }()

	changed := make([]string, 0, 3)
	for _, item := range []struct {
		name  string
		value *string
	}{
		{"maxmemory", patch.MaxMemory},
		{"maxmemory-policy", patch.MaxMemoryPolicy},
		{"timeout", patch.Timeout},
	} {
		if item.value == nil {
			continue
		}
		if _, err := conn.do("CONFIG", "SET", item.name, *item.value); err != nil {
			return ServerConfigSetResult{}, configCommandError(err, "CONFIG SET "+item.name)
		}
		changed = append(changed, serverConfigJSONKey[item.name])
	}
	config, err := readServerConfig(conn)
	if err != nil {
		return ServerConfigSetResult{}, err
	}
	return ServerConfigSetResult{Instance: instance.Name, Changed: changed, Config: config}, nil
}

// readServerConfig 在已建好的连接上读取三个白名单参数（pipeline：3 条 CONFIG GET）。
func readServerConfig(conn *respConn) (ServerConfig, error) {
	for _, name := range serverConfigNames {
		if err := conn.send("CONFIG", "GET", name); err != nil {
			return ServerConfig{}, configCommandError(err, "CONFIG GET "+name)
		}
	}
	if err := conn.flush(); err != nil {
		return ServerConfig{}, configCommandError(err, "CONFIG GET")
	}
	values := make(map[string]string, len(serverConfigNames))
	for _, name := range serverConfigNames {
		reply, err := conn.readReply()
		if err != nil {
			return ServerConfig{}, configCommandError(err, "CONFIG GET "+name)
		}
		values[name] = configGetString(reply, name)
	}
	return ServerConfig{
		MaxMemory:       values["maxmemory"],
		MaxMemoryPolicy: values["maxmemory-policy"],
		Timeout:         values["timeout"],
	}, nil
}

// configCommandError 把 CONFIG 相关错误映射为可操作错误：禁用 CONFIG 的实例给 E_UNSUPPORTED。
func configCommandError(err error, action string) error {
	var opErr *OpError
	if errors.As(err, &opErr) {
		return opErr
	}
	message := err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "unknown command") || strings.Contains(lower, "disabled") ||
		strings.Contains(lower, "not allowed") || strings.Contains(lower, "no permissions") {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"该实例禁用了 CONFIG 命令（云托管 Redis 常见）：%s；请在服务商控制台调整，或改用 redis.conf", message)
	}
	return opErrorf(pluginapi.E_UPSTREAM, "%s 失败: %s", action, message)
}
