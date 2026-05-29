# Git Revert vs Reset for Public Branches

**Category:** workflow
**Created:** 2026-05-29

## 核心规则

对已 push 到 origin 的 commit，**用 `git revert` 而不是 `git reset --hard`** 来撤销。

## 正确流程

```bash
# 1. 确保问题 commit 在当前 HEAD
git reset --hard <problematic_commit>

# 2. 用 revert 创建反向 commit
git revert <problematic_commit> --no-edit

# 3. 正常 push，不需要 force
git push origin main
```

## 为什么不用 reset --hard

- `git reset --hard` 只改本地 HEAD，origin/main 不变
- 要同步需要 `git push --force-with-lease`，有覆盖他人 commit 的风险
- revert 创建新 commit，可以正常 push，历史清晰可追溯

## 注意事项

- revert 后保留原始 commit 历史
- 如需恢复被 revert 的功能，可以 `git revert <revert_commit>`
- 对未 push 的 commit，`git reset --hard` 是安全的

## 本次案例

commit `6f94560` "llm cache optimize" 改坏了 tool 过滤逻辑，导致 `../anc` session 无法正常调用工具。用 revert 创建 `21abce2` 回滚。
