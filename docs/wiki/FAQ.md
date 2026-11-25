# 常见问题

## 插件没被内核加载？

先确认 `info.json.id` 与安装目录名一致、`protocol` 为 `kore.plugin.v1`、`requires_kore` 覆盖当前内核版本；再看 stderr 日志的握手错误。

## 方法调用报能力不足？

`info.json.capabilities` 是插件级授权面，`method_capabilities` 只收窄不放大。需要在管理界面重新确认能力，而不是在请求里自报权限。

## 结果被截断？

多数方法设了返回上限（条目数 / 字节数），返回体带 `truncated`。缩小 `limit` 或按游标 / 区间分批读取。

## 为什么没有后台定时采集？

G-19 禁止插件内 ticker / 轮询：定时统一由内核 scheduler 发任务，插件只在请求级做短采样，空闲时零 CPU。

## 发现漏洞怎么办？

不要开公开 issue 或贴 PoC。按 [SECURITY.md](https://github.com/kore-pl/kore-plugin-redis/blob/main/SECURITY.md) 的私密渠道报告，72 小时内确认收到。

## 还有问题？

到 [Issues](https://github.com/kore-pl/kore-plugin-redis/issues) 提问（非安全问题），或参与 [Discussions](https://github.com/kore-pl/kore-plugin-redis/discussions)。
