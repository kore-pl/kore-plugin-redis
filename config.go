package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore-plugin-redis/internal/plugincfg"
	"github.com/pengtang1024/kore-plugin-redis/internal/redisop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// embeddedInfo 是随二进制发布的内嵌清单：只含握手字段（manifest.json），不含会随打包回填的 size_bytes。
//
//go:embed manifest.json
var embeddedInfo []byte

// 环境变量兜底（配置文件不存在时才生效，优先级 file > env > default）。
const (
	// EnvHost 是主实例地址。
	EnvHost = "KORE_REDIS_HOST"
	// EnvPort 是主实例端口。
	EnvPort = "KORE_REDIS_PORT"
	// EnvPasswordEnv 是主实例密码所在的环境变量名（不是密码本身）。
	EnvPasswordEnv = "KORE_REDIS_PASSWORD_ENV"

	defaultHost = "127.0.0.1"
	defaultPort = 6379
	// defaultTimeoutS 是连接与单次命令的默认超时（秒）。
	defaultTimeoutS = 5
	// minTimeoutS / maxTimeoutS 是超时配置的允许区间（秒）。
	minTimeoutS = 1
	maxTimeoutS = 30
	// maxDB 是库号上限；Redis 默认 16 个库（0–15），服务端可通过 databases 调大。
	maxDB = 255
	// maxPasswordLen 防止异常大字段撑爆配置文件；密码内容本身不做字符集限制。
	maxPasswordLen = 1024
)

// manifest 是 info.json 中插件运行期真正需要的字段，其余字段归内核校验。
type manifest struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Protocol     string   `json:"protocol"`
	RequiresKore string   `json:"requires_kore"`
	Capabilities []string `json:"capabilities"`
}

// fileValues 是 <state_dir>/plugins/redis/config.json 的显式形状（G-24：不引入 object）。
//
// 主实例用 host/port/password_env 三字段描述；附加实例受配置契约「list 只存标量」
// 约束，用 "名称=主机:端口" 与 "名称=环境变量名" 两条标量列表表达。
type fileValues struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	PasswordEnv  string   `json:"password_env"`
	Password     string   `json:"password,omitempty"`
	DB           int      `json:"db"`
	TimeoutS     int      `json:"timeout_s"`
	Instances    []string `json:"instances"`
	PasswordEnvs []string `json:"password_envs"`
}

// config 是插件运行期配置：配置契约（config.json）+ 环境变量 + 内嵌清单。
type config struct {
	socket       string
	id           string
	token        string
	version      string
	requiresKore string
	capabilities []string
	startedAt    time.Time
	// stateDir 是插件状态目录（KORE_STATE_DIR），配置落盘为 <stateDir>/config.json。
	stateDir string
	// store 是配置文件的 0600 原子读写器。
	store *plugincfg.Store
	// values / source 是 config.get 的当前值与来源（file / env / default）。
	values fileValues
	source string
	// instances 是解析后的实例表；config.set 后在同一进程内替换，下一次调用即生效。
	instances []redisop.InstanceConfig
}

// loadConfig 读取配置契约文件与环境变量，并按 file > env > default 解析实例。
func loadConfig() (config, error) {
	var m manifest
	if err := json.Unmarshal(embeddedInfo, &m); err != nil {
		return config{}, fmt.Errorf("解析内嵌 manifest.json: %w", err)
	}
	if m.Protocol != pluginapi.Protocol {
		return config{}, fmt.Errorf("info.json.protocol=%q 与 SDK 协议 %q 不一致", m.Protocol, pluginapi.Protocol)
	}
	if err := plugincfg.ValidateSchema(redisSchema()); err != nil {
		return config{}, fmt.Errorf("config schema 非法: %w", err)
	}
	stateDir := strings.TrimSpace(os.Getenv("KORE_STATE_DIR"))
	store := plugincfg.NewStore(stateDir)
	values, source, err := resolveValues(store)
	if err != nil {
		return config{}, err
	}
	instances := buildInstances(values)
	cfg := config{
		socket:       os.Getenv("KORE_PLUGIN_SOCK"),
		id:           os.Getenv("KORE_PLUGIN_ID"),
		token:        os.Getenv("KORE_PLUGIN_TOKEN"),
		version:      m.Version,
		requiresKore: m.RequiresKore,
		capabilities: m.Capabilities,
		startedAt:    time.Now(),
		stateDir:     stateDir,
		store:        store,
		values:       values,
		source:       source,
		instances:    instances,
	}
	if cfg.id == "" {
		cfg.id = m.ID
	}
	if cfg.socket == "" {
		return config{}, fmt.Errorf("缺少环境变量 KORE_PLUGIN_SOCK")
	}
	return cfg, nil
}

// defaultValues 是代码默认值：本机 6379，无密码，默认库 0，超时 5s。
func defaultValues() fileValues {
	return fileValues{
		Host:         defaultHost,
		Port:         defaultPort,
		DB:           0,
		TimeoutS:     defaultTimeoutS,
		Instances:    []string{},
		PasswordEnvs: []string{},
	}
}

// resolveValues 实现优先级 config.json > KORE_REDIS_* > 代码默认值。
func resolveValues(store *plugincfg.Store) (fileValues, string, error) {
	if store.Exists() {
		raw, _, err := store.Read()
		if err != nil {
			return fileValues{}, "", err
		}
		var values fileValues
		if err := strictDecode(raw, &values); err != nil {
			return fileValues{}, "", fmt.Errorf("解析插件配置 %s: %w", store.Path(), err)
		}
		normalized, err := validateValues(values)
		if err != nil {
			return fileValues{}, "", fmt.Errorf("插件配置 %s 非法: %w", store.Path(), err)
		}
		return normalized, plugincfg.SourceFile, nil
	}

	values := defaultValues()
	source := plugincfg.SourceDefault
	if host := strings.TrimSpace(os.Getenv(EnvHost)); host != "" {
		values.Host = host
		source = plugincfg.SourceEnv
	}
	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return fileValues{}, "", fmt.Errorf("%s=%q 非法：必须是整数", EnvPort, raw)
		}
		values.Port = port
		source = plugincfg.SourceEnv
	}
	if env := strings.TrimSpace(os.Getenv(EnvPasswordEnv)); env != "" {
		values.PasswordEnv = env
		source = plugincfg.SourceEnv
	}
	normalized, err := validateValues(values)
	if err != nil {
		return fileValues{}, "", err
	}
	return normalized, source, nil
}

// validateValues 校验并归一化配置值；启动加载与 config.set 共用同一套规则。
func validateValues(values fileValues) (fileValues, error) {
	host := strings.TrimSpace(values.Host)
	if err := redisop.ValidateHost(host); err != nil {
		return fileValues{}, fmt.Errorf("主机地址非法: %w", err)
	}
	port := values.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return fileValues{}, fmt.Errorf("端口 %d 超出 [1, 65535]", values.Port)
	}
	passwordEnv := strings.TrimSpace(values.PasswordEnv)
	if passwordEnv != "" {
		if err := redisop.ValidatePasswordEnv(passwordEnv); err != nil {
			return fileValues{}, fmt.Errorf("密码环境变量非法: %w", err)
		}
	}
	password := values.Password
	if len(password) > maxPasswordLen {
		return fileValues{}, fmt.Errorf("密码超过 %d 字节上限；请确认填写内容", maxPasswordLen)
	}
	if strings.ContainsRune(password, 0) {
		return fileValues{}, fmt.Errorf("密码含 NUL 字节，无法作为连接凭据")
	}
	db := values.DB
	if db < 0 || db > maxDB {
		return fileValues{}, fmt.Errorf("库号 db=%d 超出 [0, %d]；Redis 默认只有 0–15", values.DB, maxDB)
	}
	timeoutS := values.TimeoutS
	if timeoutS == 0 {
		timeoutS = defaultTimeoutS
	}
	if timeoutS < minTimeoutS || timeoutS > maxTimeoutS {
		return fileValues{}, fmt.Errorf("超时 timeout_s=%d 超出 [%d, %d] 秒", values.TimeoutS, minTimeoutS, maxTimeoutS)
	}

	instances := make([]string, 0, len(values.Instances))
	seen := map[string]bool{"default": true}
	for index, item := range values.Instances {
		raw := strings.TrimSpace(item)
		if raw == "" {
			return fileValues{}, fmt.Errorf("第 %d 条附加实例为空；请填写 <名称>=<主机>:<端口> 或删除该行", index+1)
		}
		spec, err := redisop.ParseInstanceSpec(raw)
		if err != nil {
			return fileValues{}, fmt.Errorf("第 %d 条附加实例非法: %w", index+1, err)
		}
		if seen[spec.Name] {
			return fileValues{}, fmt.Errorf("第 %d 条附加实例名 %q 重复", index+1, spec.Name)
		}
		seen[spec.Name] = true
		instances = append(instances, raw)
	}

	envs := make(map[string]string, len(values.PasswordEnvs))
	envOrder := make([]string, 0, len(values.PasswordEnvs))
	for index, item := range values.PasswordEnvs {
		raw := strings.TrimSpace(item)
		if raw == "" {
			return fileValues{}, fmt.Errorf("第 %d 条密码环境变量为空；请填写 <名称>=<环境变量名> 或删除该行", index+1)
		}
		name, env, err := redisop.ParsePasswordEnv(raw)
		if err != nil {
			return fileValues{}, fmt.Errorf("第 %d 条密码环境变量非法: %w", index+1, err)
		}
		if !seen[name] {
			return fileValues{}, fmt.Errorf("第 %d 条密码环境变量指向未知实例 %q", index+1, name)
		}
		if _, duplicate := envs[name]; duplicate {
			return fileValues{}, fmt.Errorf("第 %d 条密码环境变量与前面的实例 %q 重复", index+1, name)
		}
		envs[name] = env
		envOrder = append(envOrder, raw)
	}
	if passwordEnv != "" && envs["default"] != "" {
		return fileValues{}, fmt.Errorf("主实例密码环境变量重复：password_env 与 password_envs 都设置了 default")
	}

	return fileValues{
		Host:         host,
		Port:         port,
		PasswordEnv:  passwordEnv,
		Password:     password,
		DB:           db,
		TimeoutS:     timeoutS,
		Instances:    instances,
		PasswordEnvs: envOrder,
	}, nil
}

// buildInstances 把配置值展开为实例表：主实例名固定 default，附加实例按列表顺序追加。
func buildInstances(values fileValues) []redisop.InstanceConfig {
	envs := make(map[string]string, len(values.PasswordEnvs))
	for _, item := range values.PasswordEnvs {
		name, env, err := redisop.ParsePasswordEnv(item)
		if err != nil {
			continue // validateValues 已保证合法
		}
		envs[name] = env
	}
	primaryPassword := values.PasswordEnv
	if primaryPassword == "" {
		primaryPassword = envs["default"]
	}
	instances := []redisop.InstanceConfig{{
		Name:        "default",
		Host:        values.Host,
		Port:        values.Port,
		PasswordEnv: primaryPassword,
		Password:    values.Password,
		DB:          values.DB,
		TimeoutS:    values.TimeoutS,
	}}
	for _, item := range values.Instances {
		spec, err := redisop.ParseInstanceSpec(item)
		if err != nil {
			continue // validateValues 已保证合法
		}
		spec.PasswordEnv = envs[spec.Name]
		// 库号与超时是连接级设置，对全部实例生效（附加实例只描述地址与密码来源）。
		spec.DB = values.DB
		spec.TimeoutS = values.TimeoutS
		instances = append(instances, spec)
	}
	return instances
}

// redisSchema 是 config.get 返回的表单描述。
func redisSchema() plugincfg.Schema {
	return plugincfg.Schema{
		Title: "Redis 连接设置",
		Fields: []plugincfg.Field{
			{
				Key:         "host",
				Label:       "Redis 地址",
				Type:        plugincfg.TypeString,
				Default:     json.RawMessage(`"` + defaultHost + `"`),
				Required:    true,
				Help:        "主机名或 IP，例：127.0.0.1、redis.example.com",
				Placeholder: defaultHost,
			},
			{
				Key:         "port",
				Label:       "端口",
				Type:        plugincfg.TypeInt,
				Default:     json.RawMessage(`6379`),
				Required:    true,
				Min:         intPointer(1),
				Max:         intPointer(65535),
				Help:        "Redis 服务端口，默认 6379",
				Placeholder: "6379",
			},
			{
				Key:         "password",
				Label:       "密码",
				Type:        plugincfg.TypeString,
				Default:     json.RawMessage(`""`),
				Required:    false,
				Sensitive:   true,
				Help:        "只保存在本机配置文件（0600），不回显、不写日志；留空则按下面的“密码环境变量”读取",
				Placeholder: "填写 Redis 密码；无密码留空",
			},
			{
				Key:         "password_env",
				Label:       "密码环境变量（高级）",
				Type:        plugincfg.TypeString,
				Default:     json.RawMessage(`""`),
				Required:    false,
				Help:        "密码也可从该环境变量读取，例：REDIS_PASSWORD；上方“密码”优先",
				Placeholder: "REDIS_PASSWORD",
			},
			{
				Key:         "db",
				Label:       "数据库编号",
				Type:        plugincfg.TypeInt,
				Default:     json.RawMessage(`0`),
				Required:    false,
				Min:         intPointer(0),
				Max:         intPointer(maxDB),
				Help:        "连接后选择的库号（0 为默认库）；Redis 默认只有 0–15，服务端 databases 决定上限",
				Placeholder: "0",
			},
			{
				Key:         "timeout_s",
				Label:       "连接超时（秒）",
				Type:        plugincfg.TypeInt,
				Default:     json.RawMessage(`5`),
				Required:    false,
				Min:         intPointer(minTimeoutS),
				Max:         intPointer(maxTimeoutS),
				Help:        "建立连接与单次命令的超时（1–30 秒），默认 5 秒",
				Placeholder: "5",
			},
			{
				Key:         "instances",
				Label:       "附加实例",
				Type:        plugincfg.TypeList,
				Item:        plugincfg.TypeString,
				Default:     json.RawMessage(`[]`),
				Required:    false,
				Help:        "每项一条 <名称>=<主机>:<端口>；例：cache=10.0.0.5:6380",
				Placeholder: "cache=10.0.0.5:6380",
			},
			{
				Key:         "password_envs",
				Label:       "附加实例密码环境变量",
				Type:        plugincfg.TypeList,
				Item:        plugincfg.TypeString,
				Default:     json.RawMessage(`[]`),
				Required:    false,
				Help:        "每项一条 <名称>=<环境变量名>，名称须与附加实例一致；例：cache=REDIS_CACHE_PASSWORD",
				Placeholder: "cache=REDIS_CACHE_PASSWORD",
			},
		},
	}
}

// intPointer 返回字段 min/max 需要的指针。
func intPointer(value int) *int { return &value }

// strictDecode 解码 JSON 并拒绝未知字段与尾随内容，避免拼错字段被静默忽略。
func strictDecode(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("JSON 对象之后存在多余内容")
	}
	return nil
}
