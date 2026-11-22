package redisop

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 实例名：小写字母/数字开头，仅含小写字母、数字、点与连字符，≤64 字符。
var instanceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// 主机：允许主机名 / IPv4 / 带 zone 的 IPv6 字面量；禁止空白、控制字符和选项注入。
var hostRE = regexp.MustCompile(`^[A-Za-z0-9._:%-]+$`)

// 密码来源环境变量名。
var passwordEnvRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidateInstance 校验一条实例配置；名字非法、地址缺失或端口越界都返回明确错误。
func ValidateInstance(instance InstanceConfig) error {
	if err := ValidateInstanceName(instance.Name); err != nil {
		return err
	}
	if err := ValidateHost(instance.Host); err != nil {
		return fmt.Errorf("实例 %q 的 host 非法: %w", instance.Name, err)
	}
	if instance.Port < 1 || instance.Port > 65535 {
		return fmt.Errorf("实例 %q 的 port=%d 超出 [1, 65535]", instance.Name, instance.Port)
	}
	if instance.PasswordEnv != "" {
		if err := ValidatePasswordEnv(instance.PasswordEnv); err != nil {
			return fmt.Errorf("实例 %q 的 password_env 非法: %w", instance.Name, err)
		}
	}
	return nil
}

// ValidateInstanceName 校验实例名，配置解析与请求参数共用。
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("实例名不得为空")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("实例名含 NUL 字节")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("实例名不得含路径分隔符")
	}
	if !instanceNameRE.MatchString(name) {
		return fmt.Errorf("实例名非法：须以小写字母/数字开头，仅含小写字母、数字、. 与 -，且不超过 64 字符")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("实例名含 .. 段，禁止穿越")
	}
	return nil
}

// ValidateHost 校验主机，拒绝空白、控制字符与以 - 开头的选项注入。
func ValidateHost(host string) error {
	if host == "" {
		return fmt.Errorf("host 不得为空")
	}
	if len(host) > 253 {
		return fmt.Errorf("host 超过 253 字符")
	}
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("host 不得以 - 开头")
	}
	for _, r := range host {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("host 含控制字符")
		}
	}
	if !hostRE.MatchString(host) {
		return fmt.Errorf("host 含非法字符：仅允许字母、数字、. - _ : %%")
	}
	return nil
}

// ValidatePasswordEnv 校验密码环境变量名。
func ValidatePasswordEnv(name string) error {
	if name == "" {
		return fmt.Errorf("环境变量名不得为空")
	}
	if !passwordEnvRE.MatchString(name) {
		return fmt.Errorf("环境变量名 %q 非法：须以字母/下划线开头，仅含字母、数字、下划线", name)
	}
	return nil
}

// instancePassword 从实例声明的环境变量读取密码；未声明时返回空串。
// 返回的错误只包含变量名，不包含变量值。
func instancePassword(instance InstanceConfig) (string, error) {
	if instance.PasswordEnv == "" {
		return "", nil
	}
	password, ok := os.LookupEnv(instance.PasswordEnv)
	if !ok || password == "" {
		return "", opErrorf(pluginapi.E_INVALID,
			"实例 %q 的密码环境变量 %s 未设置或为空", instance.Name, instance.PasswordEnv)
	}
	return password, nil
}

// ParseInstanceSpec 解析附加实例条目 "<名称>=<主机>:<端口>"。
// IPv6 字面量必须写成 [::1]:6379，避免与端口分隔符歧义。
func ParseInstanceSpec(item string) (InstanceConfig, error) {
	name, address, ok := strings.Cut(strings.TrimSpace(item), "=")
	if !ok {
		return InstanceConfig{}, fmt.Errorf("%q 缺少 =；格式为 <名称>=<主机>:<端口>", item)
	}
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	host, portText, err := splitHostPort(address)
	if err != nil {
		return InstanceConfig{}, fmt.Errorf("%q 地址非法: %w", item, err)
	}
	port, err := parsePort(portText)
	if err != nil {
		return InstanceConfig{}, fmt.Errorf("%q 端口非法: %w", item, err)
	}
	instance := InstanceConfig{Name: name, Host: host, Port: port}
	if err := ValidateInstance(instance); err != nil {
		return InstanceConfig{}, err
	}
	return instance, nil
}

// ParsePasswordEnv 解析 "<名称>=<环境变量名>"。
func ParsePasswordEnv(item string) (string, string, error) {
	name, env, ok := strings.Cut(strings.TrimSpace(item), "=")
	if !ok {
		return "", "", fmt.Errorf("%q 缺少 =；格式为 <名称>=<环境变量名>", item)
	}
	name = strings.TrimSpace(name)
	env = strings.TrimSpace(env)
	if err := ValidateInstanceName(name); err != nil {
		return "", "", err
	}
	if err := ValidatePasswordEnv(env); err != nil {
		return "", "", err
	}
	return name, env, nil
}

// splitHostPort 把地址拆成 host 与 port；支持 [IPv6]:port 与 host:port。
func splitHostPort(address string) (string, string, error) {
	if strings.HasPrefix(address, "[") {
		end := strings.Index(address, "]")
		if end < 0 {
			return "", "", fmt.Errorf("IPv6 地址缺少右方括号")
		}
		host := address[1:end]
		rest := address[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("地址缺少端口")
		}
		return host, rest[1:], nil
	}
	index := strings.LastIndex(address, ":")
	if index < 0 {
		return "", "", fmt.Errorf("地址缺少端口，格式为 <主机>:<端口>")
	}
	return address[:index], address[index+1:], nil
}

// parsePort 解析端口文本并校验范围。
func parsePort(text string) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("端口为空")
	}
	value := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("端口必须是数字")
		}
		value = value*10 + int(r-'0')
		if value > 65535 {
			return 0, fmt.Errorf("端口超出 [1, 65535]")
		}
	}
	if value < 1 {
		return 0, fmt.Errorf("端口超出 [1, 65535]")
	}
	return value, nil
}

// validatePattern 校验 SCAN MATCH 模式：非空、≤256 字节、无 NUL。
func validatePattern(pattern string) *pluginapi.Error {
	if pattern == "" {
		return invalid("pattern 不能为空")
	}
	if len(pattern) > maxPatternLen {
		return invalid(fmt.Sprintf("pattern 超过 %d 字节上限", maxPatternLen))
	}
	if strings.ContainsRune(pattern, 0) {
		return invalid("pattern 含 NUL 字节")
	}
	return nil
}

// validateKey 校验单个键：非空、≤1024 字节、无 NUL。
func validateKey(key string) *pluginapi.Error {
	if key == "" {
		return invalid("key 不能为空")
	}
	if len(key) > maxKeyLen {
		return invalid(fmt.Sprintf("key 超过 %d 字节上限", maxKeyLen))
	}
	if strings.ContainsRune(key, 0) {
		return invalid("key 含 NUL 字节")
	}
	return nil
}
