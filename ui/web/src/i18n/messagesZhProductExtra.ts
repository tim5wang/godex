export const zhProductExtraMessages = {
    // 邮箱+密码登录（设计 §3.1/§3.5）
    auth: {
      title: "登录 GoDex",
      subtitle: "用你的账号登录、创建租户，或直接填共享令牌。",
      modeLogin: "登录",
      modeRegister: "注册",
      modeToken: "共享令牌",
      email: "邮箱",
      emailRequired: "请输入邮箱。",
      password: "密码",
      passwordRequired: "请输入密码。",
      passwordHint: "至少 8 个字符。",
      tenantName: "租户名称",
      tenantNamePlaceholder: "默认取邮箱前缀",
      signIn: "登录",
      createAccount: "创建账号与租户",
      sharedToken: "共享 Web 令牌",
      sharedTokenHint: "既有的单操作员凭据。它无法「登出」。",
      sharedTokenPlaceholder: "粘贴 web token",
      useSharedToken: "使用共享令牌",
      tokenRequired: "请输入令牌。",
      loginFailed: "登录失败",
      registerFailed: "注册失败",
      registerSuccess: "账号已创建",
      registerScopeNote: "这会创建一个归你所有的租户：它可以转发到你自己的节点，但不能在本服务器上运行 agent。",
      sessionExpired: "登录已过期，请重新登录。",
      signOut: "退出登录",
      signedInAs: "当前登录",
      tenantLabel: "租户",
    },
    usage: {
      // Tab labels
      overview: "概览",
      apiKeys: "API 密钥",
      modelMappings: "模型映射",
      summary: "汇总",
      sessions: "会话",
      dailyCalls: "每日调用",
      cacheStats: "缓存统计",

      // Filter bar
      filter: {
        allKeys: "所有密钥",
        allModels: "所有模型",
        hourly: "按小时",
        daily: "按天",
        proxyKeys: "代理密钥",
        systemEntries: "系统条目",
      },

      // Overview dashboard
      overviewTab: {
        totalCalls: "总调用次数",
        totalTokens: "总 Token（输入 + 输出）",
        totalCredits: "总 Credits",
        errorRate: "错误率",
        cacheSaved: "缓存节省",
        cacheHitRate: "缓存命中率",
        noData: "暂无数据",
      },

      // Token trend chart
      tokenChart: {
        title: "Token 用量 vs 缓存节省",
        inputTokens: "输入 Token",
        outputTokens: "输出 Token",
        cacheRead: "缓存读取（节省）",
      },

      // Credit trend chart
      creditChart: {
        title: "Credits 消耗",
        credits: "Credits",
        cumulative: "累计",
      },

      // Error rate chart
      errorChart: {
        title: "调用量与错误率",
        callCount: "调用次数",
        errorRate: "错误率",
      },

      // Session panel
      sessionPanel: {
        title: "会话用量",
        searchPlaceholder: "搜索会话 ID...",
        filterByKey: "按密钥筛选",
        sessionId: "会话 ID",
        calls: "调用次数",
        inputTokens: "输入 Token",
        outputTokens: "输出 Token",
        credits: "Credits",
        firstCall: "首次调用",
        lastCall: "最后调用",
        models: "模型",
        modelBreakdown: "模型细分",
        model: "模型",
      },

      // Cache stats
      cachePanel: {
        title: "按模型的缓存性能",
        today: "今天",
        last7Days: "最近 7 天",
        last30Days: "最近 30 天",
        allTime: "全部",
        totalCalls: "总调用",
        inputTokens: "输入 Token",
        cacheRead: "缓存读取",
        hitRate: "命中率",
        tokensSaved: "节省 Token",
      },

      // Summary table
      summaryTable: {
        period: "时间段",
        keyId: "密钥 ID",
        calls: "调用",
        errors: "错误",
        input: "输入",
        output: "输出",
        cacheR: "缓存 R",
        cacheW: "缓存 W",
        billable: "计费",
        credits: "Credits",
      },

      // Calls table
      callsTable: {
        time: "时间",
        key: "密钥",
        model: "模型",
        target: "目标",
        source: "来源",
        in: "输入",
        out: "输出",
        cacheR: "缓存 R",
        cacheW: "缓存 W",
        weight: "权重",
        credits: "Credits",
        status: "状态",
      },
    },
    settings: {
      configSections: "配置分类",
      configSectionGroups: {
        core: "模型与 Agent",
        tools: "工具",
        channels: "消息通道",
        automation: "自动化",
        system: "系统与存储",
      },
      configSavedState: "配置已保存",
      configLoading: "正在加载配置…",
    },
} as const;
