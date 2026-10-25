package core

// GitHookNames are the names git runs as hooks.
//
// Git looks a hook up BY NAME. A file called "setup-dev-env.sh" sitting in
// the hooks directory is never run by git, no matter how executable it is
// and no matter what it does.
//
// Counting it as a guard means reporting "this guard has never run even
// once" about a file git was never going to run - a CRITICAL accusation
// about a script that installs a development environment.
//
// The list is the one in githooks(5). A hook git does not know about is
// not a guard.
//
// ★ 它住在共享词汇这一层，是因为**两个地方需要同一个答案**：
// 检查器问「这个守卫到底跑不跑」，闸门问「这个答案有没有变」。
// 这份名单要是写两遍，两遍就会慢慢漂 —— 而漂移是看不见的：
// 闸门还在盯着一批检查器早就不看的文件，于是它报「没变化」，
// 而结论已经变了。症状和「一切正常」一模一样。
var GitHookNames = map[string]bool{
	"applypatch-msg": true, "pre-applypatch": true, "post-applypatch": true,
	"pre-commit": true, "pre-merge-commit": true,
	"prepare-commit-msg": true, "commit-msg": true, "post-commit": true,
	"pre-rebase": true, "post-checkout": true, "post-merge": true,
	"pre-push": true, "pre-receive": true, "update": true,
	"post-receive": true, "post-update": true, "push-to-checkout": true,
	"pre-auto-gc": true, "post-rewrite": true, "sendemail-validate": true,
	"fsmonitor-watchman": true, "reference-transaction": true,
	"post-index-change": true, "proc-receive": true,
	"p4-changelist": true, "p4-prepare-changelist": true,
	"p4-post-changelist": true, "p4-pre-submit": true,
}

// IsGitHookName reports whether git runs a hook by this name.
func IsGitHookName(name string) bool {
	return GitHookNames[name]
}
