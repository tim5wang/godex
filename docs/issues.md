# Issues to fix
- [x] tui 界面输入q 直接退出了，太容易触发，有 ctr + c退出就好了，不需要 vim的退出风格

- [x] tui 界面的回复，似乎在 web ui 看不到结果

- [x] deepseek 模型调用报错 API error: 400: {"error":{"message":"Invalid schema for function 'list_memory_candidates': null is not of type "object"","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}

- [x] 调模型还有错误 API error: 400: {"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API.","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}

- [x] im 的 approve消息完全看不出调什么tool，以及tool的主要参数

- [x] token成本有一些高，可以优化context组装，固定的内容放在前面，增加cache命中率，输出内容减少废话

- [x] todo_write 方法经常出错 Error: missing items argument
得排查看看是工具定义有问题，还是context对其说明不准确

- [x] todo_write 在 cli/tui/web/acp/im 上的可视化均需要做一些优化

- [x] acp客户端能看到 tool call日志，只有名称，似乎太简略了一些，比如bash实在不知道bash调了什么

- [x] 关于acp目前有一个问题： godex的vscode acp client弹出授权弹窗，allow  onece 或者 allow session点完之后，session无法继续下去（tool calls日志不变化了），等很久输入变成可发送状态，或者需要手动点stop，然后额外发送一个消息让agent继续下去

- [x] web ui上的上下文与找回显示 “当前已使用 197224 / 100000 token（约 100%）。”了还没自动触发上下文压缩， 随着工具调用，上下文增长的速度似乎太快了一些，考虑优化优化

- [x] 在 acp 和 tui 这样的典型编程入口场景，模型输出废话偏多，不想codex那样紧凑关键，考虑通过提示词优化优化

- [x] approve 已经和 channel或者对话框绑定了，还需要 /approve <session_id> 颇为不便，复制字符串比较麻烦，尤其是移动端客户

- [x] tui 有三个问题：1. approve时，看不到要approve什么权限，approve什么session_id; 2. tui 里的command记录会显示在末尾，导致要往上滚动才能看到agent回复； 3. tui里没办法鼠标划词复制文本

- [x] godex的默认(repl)命令行对话里，似乎不能支持权限审批，入口可以改一下， godex命令默认是tui实现，去掉godex tui命令，增加godex repl命令

- [x] godex用read_file 用着用着就改用bash了，是因为read_file 的 start_line 和 limit并不生效，无法完成代码阅读任务，在这里添乱呢

- [x] godex web ui 上的消息发送 steer 似乎无效（修复：inject 时补发 user_message_accepted 让 UI 立即显示；steer 消息注入时加【用户打断 · Steer】框架，让模型暂停当前任务优先处理新指令；修复 BuildAPIMessages 用 metadata 重建消息导致框架被丢弃的问题）

- [x] godex web ui chat界面的流式输出和工具调用日志前后顺序和时序不一致，在一轮交互结束后agent输出结果了才顺序一致（修复：chat store 在工具调用之后开始新的 assistant 文本段，流式输出与工具日志按真实时序交错）

- [x] godex web ui底部的 文件变动汇总，最好有一个每个文件的增减行数、总工增减行数信息（修复：ChangesCard 增加每个文件的 +N −M 与头部总增减行数）

- [x] godex 这个 session 一直卡在 bash 调用，无法通过“停止当前任务”按钮结束任务： web-57f1193df4f3c290，点了stop,再发送消息卡在了发送中（修复：bash 执行改为进程组管理，取消/超时杀掉整个进程树，Wait 不再被孙进程继承的管道卡死；恢复循环加护栏）

- [x] godex web ui chat界面上，滚动条会被模型输出强行拽到最新位置，我希望在拖动滚动条看历史消息时，不会被强制滚动打断, 并且工具日志展开后，json 背景是黑色，json key也是黑色，字符不可见（修复：自动滚动只在贴近底部时生效，上滑看历史不再被打断并提供 Jump to latest；CodeViewer 背景跟随明暗主题）

- [x] godex /compact 效果不好，压缩信息丢失太严重了，我270k上下文，一下压成了16k, 压缩结果丢弃了 agent每轮输出（修复：summary 的 verbatim 用户输入/agent 输出段不再被 6500-rune 总预算截尾（元数据段独立预算，保留最近 10 轮 assistant 输出），工具结果按 4000 runes 裁剪，大结果保留 transcript 引用）
