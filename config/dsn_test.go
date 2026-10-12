package config

import "testing"

// 本文件先于实现编写（用户要求「先做测试」）。
// 被测对象是**判断规则本身**——规则写错（例如用 strings.Contains(dsn, "test") 粗糙匹配）
// 会给出「安全」的错觉，比不加安全阀更危险。故表驱动测试必须先立起来。

// TestParseDSNDatabaseName 钉死「从 DSN 里精确取库名」这一规则。
//
// 最关键的一条是**密码含 test**：粗糙实现会把 `user:teST123@tcp(h:3306)/asset` 误判成
// 测试库，于是放心地连上生产库 —— 正好在最危险的时候给出安全错觉。必须真正解析
// `@tcp(...)` 之后、`?` 之前的那一段。
func TestParseDSNDatabaseName(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"标准 DSN", "user:pass@tcp(h:3306)/asset_test?charset=utf8mb4", "asset_test"},
		{"生产库名", "user:pass@tcp(h:3306)/asset?charset=utf8mb4", "asset"},
		{"密码含 test（绝不能被它误导）", "user:teST123@tcp(h:3306)/asset?charset=utf8mb4", "asset"},
		{"密码里含 @", "user:p@ss@tcp(h:3306)/asset_test", "asset_test"},
		// 上面那条**不足以**鉴别「用 Index 而非 LastIndex」：第一个 @ 之后是
		// `ss@tcp(h:3306)/asset_test`，其第一个 / 恰好仍落在正确位置，粗糙实现也能取对。
		// 密码里再含一个 / 才能真正鉴别——此时 Index 实现会取到 `word@tcp(h:3306)/asset_test`。
		{"密码含 @ 且含 /（只有从右取 @ 才对）", "user:p@ss/word@tcp(h:3306)/asset_test", "asset_test"},
		{"无参数", "user:pass@tcp(h:3306)/asset_test", "asset_test"},
		{"无库名", "user:pass@tcp(h:3306)/", ""},
		{"非 DSN", "not-a-dsn", ""},
		{"空串", "", ""},
	}
	for _, c := range cases {
		if got := ParseDSNDatabaseName(c.dsn); got != c.want {
			t.Errorf("%s: ParseDSNDatabaseName(%q) = %q, 期望 %q", c.name, c.dsn, got, c.want)
		}
	}
}

// TestIsTestDatabaseName 钉死「像不像测试库」：含 test（不区分大小写）。
func TestIsTestDatabaseName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"asset_test", true},
		{"asset_Test", true},
		{"test", true},
		{"TEST", true},
		{"asset", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsTestDatabaseName(c.in); got != c.want {
			t.Errorf("IsTestDatabaseName(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}
