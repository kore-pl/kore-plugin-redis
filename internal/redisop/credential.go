package redisop

import "os"

// 本文件是 ADR-0026 / §3.5 #8 的凭据解析入口：优先内核注入，缺失才回落插件 config。
//
// 为什么要这么改：内核按约定给插件进程注入 REDISCLI_AUTH（与 redis-cli 同名，运维直觉
// 一致），一处配置即可让插件与命令行工具共用凭据；插件 config 里的口令从此是**兜底**，
// 既保持老部署可用，又避免同一口令在 config.json / 环境变量 / 内核三处漂移。

const (
	// KernelPasswordEnv 是内核注入的凭据环境变量；存在即优先于插件 config 口令。
	KernelPasswordEnv = "REDISCLI_AUTH"
	// DefaultInstanceName 是配置契约描述的默认实例名（只有它接受内核注入的凭据）。
	DefaultInstanceName = "default"

	// CredentialSourceKernelEnv / PluginConfig / None 是对外诊断字段 credential_source 的三态。
	CredentialSourceKernelEnv    = "kernel_env"
	CredentialSourcePluginConfig = "plugin_config"
	CredentialSourceNone         = "none"
)

// readConfigPassword 是读取插件 config 口令的唯一入口；单测可替换并计数，
// 用来断言「内核注入 env 存在时零次读取 config 口令」。
var readConfigPassword = func(instance InstanceConfig) string { return instance.Password }

// resolveCredential 返回连接口令与来源：
//   - kernel_env：默认实例 + 内核注入的 REDISCLI_AUTH 存在 → 直接用，**不再读 config 口令**；
//   - plugin_config：env 缺失时回落插件 config（password，其次 password_env），向后兼容；
//   - none：两者都没有（无密码 Redis 是合法配置，真正的认证失败由服务端回 E_UPSTREAM）。
//
// 内核注入只作用于默认实例：多实例的其它实例各有自己的地址与口令来源，
// 把默认实例的凭据发给别的实例等于拿错钥匙开别人的门。
func resolveCredential(instance InstanceConfig) (string, string) {
	if instance.Name == DefaultInstanceName {
		if password, ok := os.LookupEnv(KernelPasswordEnv); ok && password != "" {
			return password, CredentialSourceKernelEnv
		}
	}
	if password := readConfigPassword(instance); password != "" {
		return password, CredentialSourcePluginConfig
	}
	if instance.PasswordEnv != "" && instance.PasswordEnv != KernelPasswordEnv {
		if password, ok := os.LookupEnv(instance.PasswordEnv); ok && password != "" {
			return password, CredentialSourcePluginConfig
		}
	}
	return "", CredentialSourceNone
}

// credentialSource 只取来源标签（redis.instances 的诊断字段用）。
func credentialSource(instance InstanceConfig) string {
	_, source := resolveCredential(instance)
	return source
}
