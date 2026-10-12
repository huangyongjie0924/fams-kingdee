package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// employeeWriteFiles 扫「可能写 employee 表」的目录（覆盖将来新增文件）；
// 显式排除云之家同步文件——它是 open_id 的合法写入者，不在本守卫范围内。
// 取舍参照 store/category_guard_test.go:13-18 排除 masterdata.go 的先例。
func employeeWriteFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{".", "../syncer", "../api", "../cmd"} {
		matched, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("列举 %s 失败：%v", dir, err)
		}
		for _, f := range matched {
			if strings.HasSuffix(f, "_test.go") {
				continue // 测试夹具不算生产写路径
			}
			if strings.Contains(filepath.Base(f), "yunzhijia") {
				continue // 合法写入者
			}
			files = append(files, f)
		}
	}
	return files
}

// TestNoEmployeeWriteTargetsOpenID 钉死「任何写 employee 的语句都不得把 open_id 当目标列」。
// 断言**写入目标列**，不是「文件里不出现 open_id」——后者会误报（读 open_id 无害）。
func TestNoEmployeeWriteTargetsOpenID(t *testing.T) {
	for _, f := range employeeWriteFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", f, err)
		}
		norm := strings.ToLower(strings.Join(strings.Fields(string(b)), " "))
		for _, prefix := range []string{"update employee set ", "insert into employee ("} {
			for idx := strings.Index(norm, prefix); idx >= 0; {
				start := idx + len(prefix)
				end := start
				for end < len(norm) && !strings.HasPrefix(norm[end:], " where ") &&
					!strings.HasPrefix(norm[end:], " values") && norm[end] != ')' {
					end++
				}
				cols := norm[start:end]
				if strings.Contains(cols, "open_id") {
					t.Fatalf("%s：写 employee 的列清单含 open_id——employee.open_id 是本地维护列，"+
						"会被外部同步覆盖（见 docs/云之家待办通知集成方案.md §6.7）\n列清单片段：%s", f, cols)
				}
				next := strings.Index(norm[start:], prefix)
				if next < 0 {
					break
				}
				idx = start + next
			}
		}
	}
}

// TestGuardCoversKingdeeSyncFiles 断言守卫的扫描集**确实覆盖**星瀚同步链路的关键文件。
//
// 为什么需要它：employeeWriteFiles 的排除规则是**按文件名模式**（basename 含 "yunzhijia"）的。
// 若这些关键文件因命名原因落进排除集，守卫会**静默失效**——而守卫失效本身不会让任何测试变红。
// 本测试把「守卫失效」变成一次显式的红：只要关键星瀚文件不再被扫描，立刻报错。
func TestGuardCoversKingdeeSyncFiles(t *testing.T) {
	// 星瀚同步链路的必经文件（写 employee 的都在这里）
	critical := []string{"orgsync.go", "sync.go", "../syncer/org.go", "../syncer/syncer.go"}

	scanned := map[string]bool{}
	for _, f := range employeeWriteFiles(t) {
		scanned[filepath.Clean(f)] = true
	}
	for _, c := range critical {
		if !scanned[filepath.Clean(c)] {
			t.Fatalf("守卫未覆盖关键星瀚同步文件 %s——它被排除集吃掉了，"+
				"employee.open_id 的守卫已静默失效（见 docs/云之家待办通知集成方案.md §6.7）", c)
		}
	}
}
