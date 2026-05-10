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
