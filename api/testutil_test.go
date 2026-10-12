package api

import (
	"os"
	"path/filepath"
	"testing"

	"asset-mgr/config"
	"asset-mgr/store"
)

// 本文件是 api 包集成测试的**统一取库入口 + 安全阀**。
//
// 背景：生产机 10.0.0.120 上只有生产库（asset），没有独立测试库；而 store.New 会
// **自动执行表结构迁移（DDL）**——「跑一次测试」就等于「连生产库并执行 DDL」。所以所有
// 需要数据库的 api 用例都必须经这里取库，默认不连「不像测试库」的库。

// mustTestStore 取一个用于集成测试的 *store.Store 与其配置。
//
// ⚠️ 与 store 包的 testDSN 用**同一套安全阀**（config.ParseDSNDatabaseName +
// config.IsTestDatabaseName），规则只写一份，避免两个包给出不一致的「安全」结论：
//   - 显式 ASSET_MGR_TEST_DSN → 使用它（显式穿透，打印脱敏库名留痕）；
//   - 否则读 config.yaml，库名不含 "test" 就 Skip（宁可跳过，也不误连生产库）；
//   - 解析不出库名、读不到 config.yaml、连接失败 —— 一律 Skip（都是环境问题，不是用例失败）。
func mustTestStore(t *testing.T) (*store.Store, *config.Config) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "config.yaml"))
	if err != nil {
		t.Skipf("读不到 config.yaml，跳过需要数据库的集成测试: %v", err)
	}
	st, err := store.New(testDSN(t, cfg))
	if err != nil {
		t.Skipf("连接数据库失败，跳过需要数据库的集成测试: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, cfg
}

// testDSN 解析集成测试要连的 DSN，并施加安全阀（与 store/testDSN 同一规则）。
// 只打印库名，绝不打整条 DSN（含密码）。
func testDSN(t *testing.T, cfg *config.Config) string {
	t.Helper()
	if dsn := os.Getenv("ASSET_MGR_TEST_DSN"); dsn != "" {
		t.Logf("集成测试：使用 ASSET_MGR_TEST_DSN，目标库 %q", config.ParseDSNDatabaseName(dsn))
		return dsn
	}
	dsn := cfg.DSN()
	if dbName := config.ParseDSNDatabaseName(dsn); !config.IsTestDatabaseName(dbName) {
		t.Skipf("DSN 指向的库 %q 不像测试库（生产库？），已跳过需要数据库的集成测试。"+
			"如需运行，请设置 ASSET_MGR_TEST_DSN 指向一个独立测试库", dbName)
	}
	return dsn
}
