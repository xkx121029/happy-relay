package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoLockedReturn 静态检查：持有互斥锁的函数里，所有 return 分支都必须先 Unlock。
//
// 这类 bug 不会编译报错、也不会在低并发下暴露，一旦命中就是整个服务卡死
// （Go 的 mutex 不可重入，缺一次 Unlock 就是自死锁）。用 AST 扫一遍比等人踩坑划算。
func TestNoLockedReturn(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("解析包失败：%v", err)
	}
	issues := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			file := f.Name.Name
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				continue
			}
			srcAll, err := os.ReadFile(f.Name.Name)
			if err != nil {
				t.Fatalf("读取源码失败：%v", err)
			}
			_ = srcAll
			ast.Inspect(f, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				// 找函数体里出现的 xxx.Lock()
				locks := map[string]bool{}
				ast.Inspect(fn.Body, func(m ast.Node) bool {
					if ce, ok := m.(*ast.CallExpr); ok {
						if sel, ok := ce.Fun.(*ast.SelectorExpr); ok {
							if id, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "Lock" {
								locks[id.Name] = true
							}
						}
					}
					return true
				})
				if len(locks) == 0 {
					return true
				}
				// 逐个 return 检查：返回前必须出现 Unlock
				ast.Inspect(fn.Body, func(m ast.Node) bool {
					ret, ok := m.(*ast.ReturnStmt)
					if !ok {
						return true
					}
					// 用位置粗略判断：return 之前 12 行内有没有 Unlock
					start := fset.Position(ret.Pos()).Offset
					begin := start - 400
					if begin < 0 {
						begin = 0
					}
					if begin > start {
						begin = 0
					}
					before := string(srcAll[begin:start])
					for lk := range locks {
						if !strings.Contains(before, lk+".Unlock()") {
							issues++
							t.Errorf("%s:%d 函数 %s 里 return 前未见 %s.Unlock()（可能持锁返回导致死锁）",
								file, fset.Position(ret.Pos()).Line, fn.Name.Name, lk)
						}
					}
					return true
				})
				return true
			})
		}
	}
	if issues == 0 {
		t.Log("未发现持锁返回的分支")
	}
}
