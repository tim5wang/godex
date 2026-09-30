export const zhProductExtraMessages = {
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
