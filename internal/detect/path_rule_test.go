package detect

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 这一组用例守的是同一条规则的两半：
//
//	① 一条以斜杠开头的路径是「有根的」，在哪个平台上都是；
//	② 有根的路径不能被拼到仓库根上。
//
// 这条规则原来写在 internal/detect/guards.go 里（isAbsolutePath）。后来它被
// 提到了 internal/core（core.IsAbsolutePath）—— 因为闸门也得问它，而闸门问
// 的必须是同一个问法：两边问法不一样，就多一只看不见的眼睛。
//
// 提到 core 的同时，这个包里另外五处还在问 filepath.IsAbs：
//
//	ci_steps.go（两处）、detect.go、harness.go —— 这四处只问了 filepath.IsAbs，
//	所以答案不对；
//	resolve.go —— 那一处自己把规则写全了（filepath.IsAbs || 前导斜杠），
//	所以它本来就是对的，这次只是把它换成同一个问法。
//
// filepath.IsAbs 只认识「本平台的绝对路径」：在 Windows 上它对
// "/c/Users/x/.githooks" 说「不是」，因为那个写法既没有盘符也没有 UNC
// 前缀。
//
// 后果有两种，一种比另一种更坏：
//
//   - 不该拼的地方拼了。一个明明在仓外的路径被拼到仓库根上，变成「仓里的一
//     个文件」—— 工具于是拿一个仓外的文件去讲仓里的结论。
//   - 该停的地方没停。CI 里 cd 到一个有根的目录之后，后面的命令照样被算成
//     这个仓的检查。
//
// 负控：把 core.IsAbsolutePath 换回 filepath.IsAbs，下面每一条在 Windows 上
// 都会失败。POSIX 上它们照样过 —— filepath.IsAbs 在 POSIX 上本来就认这些
// 路径。这正是这件事以前没人发现的原因：写的人、测的人都在 POSIX 上，
// 只有 Windows 上才裂开。

// TestACdToARootedPathEndsTheScan 守的是「该停的地方要停」。
//
// 一个 CI workflow 里有这么一段：
//
//	cd /c/build
//	go test ./...
//
// "/c/build" 是有根的（MSYS 写法），它不在这个仓里。cd 到那儿之后的命令，
// 就不可能是这个仓的检查了 —— 而工具本来要说这句话：它报的是「CI 从来
// 不执行这个仓的检查」。
//
// 修之前（Windows 上）：filepath.IsAbs("/c/build") 说是「不是」，于是那个
// cd 被当成一个仓内的相对目录，后面的 "go test ./..." 被算到仓里一个叫
// "c/build" 的子目录上执行。结论照样是绿的，但目录是假的。
func TestACdToARootedPathEndsTheScan(t *testing.T) {
	const workflow = `jobs:
  build:
    steps:
      - run: |
          cd /c/build
          go test ./...
`

	for _, c := range ciCommands(workflow) {
		if strings.Contains(c.text, "go test") {
			t.Fatalf("a command after a cd to a rooted path must not be credited to this repository, got %+v", ciCommands(workflow))
		}
	}
}

// TestARootedPathIsNotInsideTheRepository 守的是「不该拼的地方不拼」。
//
// 一个仓外的路径拼到仓库根上之后，会变成一个「仓里的路径」—— 因为拼出来
// 的东西确实落在仓库根底下。工具于是拿着一份仓外的文件，去讲一条关于这
// 个仓的结论。
//
// 这不是理论上的：MSYS 写法（"/c/Users/x/repo/file.go"）在 Windows 上正是
// 修之前会被当成仓内路径的那种写法。
func TestARootedPathIsNotInsideTheRepository(t *testing.T) {
	dir := t.TempDir()

	const outside = "/c/Users/x/somewhere/file.go"
	rel, ok := repoRel(dir, dir, outside)
	if ok {
		t.Fatalf("repoRel(%q) = %q, true; want \"\", false - that path is not in this repository", outside, rel)
	}
}

// TestARootedScriptIsNotThisRepositorysHarness 守的是同一件事的另一半。
//
// 命令行里写出来的一个有根路径，指向的是仓外的脚本 —— 那是隔壁 checkout 的
// 东西，不是这个仓的 harness。修之前（Windows 上）它被拼到 base 底下，变成
// 「packages/core/c/other/repo/build.sh」这种仓里根本不存在、于是谁也查不
// 到的路径。
func TestARootedScriptIsNotThisRepositorysHarness(t *testing.T) {
	const token = "/c/Users/x/other-repo/build.sh"

	rel, ok := repoRelativePath("packages/core", token)
	if ok {
		t.Fatalf("repoRelativePath(%q) = %q, true; want \"\", false - that script is not in this repository", token, rel)
	}
}

// TestARootedCheckoutPathIsNotUsable 守的是 checkout 那一半。
//
// 一个有根的 checkout 路径把仓放在工作区外面 —— 工具读不到它的目录结构，
// 于是不该猜。POSIX 上 filepath.IsAbs 已经这么判了；Windows 上
// filepath.IsAbs 对 "/c/build" 说是「不是」，于是工具以为那是个仓内的子
// 目录，把命令放错了地方。
func TestARootedCheckoutPathIsNotUsable(t *testing.T) {
	for _, p := range []string{"/c/build", "/home/runner/work/repo"} {
		if usableCheckoutPath(p) {
			t.Errorf("usableCheckoutPath(%q) = true; want false - a rooted path leaves the workspace", p)
		}
	}
}

// TestARelativePathIsStillRelative pins the other half of the rule.
//
// 规则不是「什么都当有根」：仓内的相对路径照旧是相对的。加这一条是防
// 修过头 —— 一个把 "scripts/build.sh" 也判成有根的规则，会让工具看不见
// 整个仓的脚本。
func TestARelativePathIsStillRelative(t *testing.T) {
	if usableCheckoutPath("packages/core") != true {
		t.Error(`usableCheckoutPath("packages/core") = false; want true - that is a path inside the workspace`)
	}
	rel, ok := repoRelativePath("packages/core", "scripts/build.sh")
	if !ok || rel != "packages/core/scripts/build.sh" {
		t.Errorf(`repoRelativePath("packages/core", "scripts/build.sh") = %q, %v; want "packages/core/scripts/build.sh", true`, rel, ok)
	}
}

// TestRepoRelStillResolvesARepositoryRelativePath pins the same thing on
// the other function.
//
// 一条仓内路径要照旧解析成仓相对路径 —— 不能因为规则改了就什么都不认。
func TestRepoRelStillResolvesARepositoryRelativePath(t *testing.T) {
	dir := t.TempDir()
	rel, ok := repoRel(dir, dir, filepath.Join("scripts", "build.sh"))
	if !ok || rel != "scripts/build.sh" {
		t.Errorf(`repoRel(dir, dir, "scripts/build.sh") = %q, %v; want "scripts/build.sh", true`, rel, ok)
	}
}

// TestInstalledHookFilesStillJoinsBothNames 把搬家丢掉的那半个断言捡回来。
//
// 两条 hook 规则搬到 internal/core 的时候，连同它们的用例一起搬了 ——
// core 里那份钉的是「有哪些名字」，而 installedHookFiles 钉的是「这些名字
// 被接到哪个目录上」。后半句没人管了：core 的用例当然过（它根本不拼路径），
// 而 installedHookFiles 变成了一个没人直接问的函数。
//
// 顺序是有意义的：git 停在第一个存在的名字上，所以「先 plain 后 .exe」不是
// 一个可以随便改的排列。
func TestInstalledHookFilesStillJoinsBothNames(t *testing.T) {
	got := installedHookFiles("hooks", "pre-push")
	want := []string{filepath.Join("hooks", "pre-push")}
	if runtime.GOOS == "windows" {
		want = append(want, filepath.Join("hooks", "pre-push.exe"))
	}
	if len(got) != len(want) {
		t.Fatalf("installedHookFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("installedHookFiles = %v, want %v", got, want)
		}
	}
}
