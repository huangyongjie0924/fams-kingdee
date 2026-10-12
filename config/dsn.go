package config

import "strings"

// 本文件是「测试安全阀」的判定规则（供 store / api 两个包的集成测试共用）。
//
// 背景：生产机 10.0.0.120 上只有生产库（asset），没有独立测试库；而 store.New 会
// **自动执行表结构迁移（DDL）**——「跑一次测试」就等于「连生产库并执行 DDL」。所以测试默认
// 不允许连「不像测试库」的库。规则放 config 包：解析 DSN 本就是配置层职责，且两个包都用，
// 放一处避免规则漂移。
//
// 这两个函数也可用于将来启动时告警「你连的可能是生产库」。

// ParseDSNDatabaseName 从 MySQL DSN 中解析出数据库名。
//
// DSN 形态：`user:pass@tcp(host:port)/dbname?params`。
//
// ⚠️ 绝不能退化成 `strings.Contains(dsn, "test")` 这类粗糙匹配：密码里出现 test
// （如 `user:teST123@tcp(...)/asset`）会把生产库误判成测试库，正好在最危险的时候给出
// 「安全」的错觉。必须精确取 `@tcp(...)` 之后、`?` 之前的那一段。
//
// 解析不出时返回空串（不 panic）。空串经 IsTestDatabaseName 判定为「非测试库」→ 调用方 Skip。
func ParseDSNDatabaseName(dsn string) string {
	// 用 LastIndex 而非 Index：密码本身可能含 '@'（如 `user:p@ss@tcp(...)`），
	// 从右往左取到的才是「地址+库名」段的起点。
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return ""
	}
	rest := dsn[at+1:] // 形如 tcp(h:3306)/asset_test?charset=utf8mb4
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	db := rest[slash+1:]
	if q := strings.Index(db, "?"); q >= 0 {
		db = db[:q]
	}
	return db
}

// IsTestDatabaseName 判断库名是否像测试库：含 "test"（不区分大小写）。
//
// 只对**已解析出的库名**调用（不要对整条 DSN 调用——见 ParseDSNDatabaseName 的警告）。
func IsTestDatabaseName(name string) bool {
	return strings.Contains(strings.ToLower(name), "test")
}
