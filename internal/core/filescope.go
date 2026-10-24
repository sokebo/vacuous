package core

import (
	"path/filepath"
	"sort"
)

// FileScope 说清楚一次分析允许看哪些文件。
//
// 它是为了守住一条不变量：
//
//	**一次分析的结论，只能取决于它看的那份文件清单，
//	和这些文件在工作树里的内容。**
//
// 任何越过清单去读磁盘的动作，都会让结论取决于这个仓里**并不存在**的东西。
// 而守在闸门位置的那个东西看不见那些文件 —— 于是「没变化」和「结论变了」
// 会同时成立，两边都像样，谁也不说谎，于是这个谎没人拆穿。
//
// 实测过的那种形状：一个仓的 CI 不跑测试（真的），开发者在旁边放了一个
// 没提交的 workflow，那份 workflow 跑测试 —— 工具的结论从「CI 不跑测试」
// 变成「CI 跑测试」，而闸门报「没变化」。反过来，一个没提交的 hook 脚本
// 能凭空造出一条「守卫从来没被装上」的高危来。
//
// 范围里的路径都是仓库相对路径，用 / 分隔。
type FileScope struct {
	paths map[string]bool
	all   bool
}

// FileScopeOf 让分析只看给定的这些路径。
//
// 清单通常来自 git（被跟踪的文件），加上这一轮明确要一起看的未跟踪文件。
func FileScopeOf(paths []string) FileScope {
	s := FileScope{paths: make(map[string]bool, len(paths))}
	for _, p := range paths {
		if p == "" {
			continue
		}
		s.paths[filepath.ToSlash(p)] = true
	}
	return s
}

// FileScopeAll 让分析看整个磁盘。
//
// 它只给那些**明确**要「看全部」的场合用：一次临时检出的内容本身就是范围
// （反事实审计跑在某个提交的树上，那棵树就是它全部的世界），或者测试夹具
// 根本不是 git 仓、没有清单可言。
//
// 生产路径上不该出现它。出现了，就说明那里的结论又依赖上了清单之外的文件 ——
// 而那一类依赖正是这个类型存在的理由。
func FileScopeAll() FileScope {
	return FileScope{all: true}
}

// Allows 报告 rel 这个路径在不在这次分析的范围里。
func (s FileScope) Allows(rel string) bool {
	if s.all {
		return true
	}
	return s.paths[filepath.ToSlash(rel)]
}

// IsEverything 报告这个范围是不是「看整个磁盘」。
func (s FileScope) IsEverything() bool {
	return s.all
}

// Paths 列出范围里的路径，排好序。
//
// 「看整个磁盘」的范围没有路径可列 —— 磁盘上有什么，不是一份清单说得清的。
func (s FileScope) Paths() []string {
	if s.all {
		return nil
	}
	out := make([]string, 0, len(s.paths))
	for p := range s.paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
