package redisop

// noopExecutor 是骨架默认执行面：不连接任何 Redis 实例，只返回空结果。
// 四类故障注入测试提供自己的 exec，让 Call 走错误映射路径。
type noopExecutor struct{}

func (noopExecutor) Instances(defaultPrefix string) ([]Instance, error) { return nil, nil }

func (noopExecutor) Scan(prefix, policy string, limit, cursor int) ([]KeyEntry, error) {
	return nil, nil
}
