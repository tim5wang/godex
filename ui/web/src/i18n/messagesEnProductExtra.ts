export const enProductExtraMessages = {
    usage: {
      // Tab labels
      overview: "Overview",
      apiKeys: "API Keys",
      modelMappings: "Model Mappings",
      summary: "Summary",
      sessions: "Sessions",
      dailyCalls: "Daily Calls",
      cacheStats: "Cache Stats",

      // Filter bar
      filter: {
        allKeys: "All keys",
        allModels: "All models",
        hourly: "Hourly",
        daily: "Daily",
        proxyKeys: "Proxy Keys",
        systemEntries: "System Entries",
      },

      // Overview dashboard
      overviewTab: {
        totalCalls: "Total Calls",
        totalTokens: "Total Tokens (In + Out)",
        totalCredits: "Total Credits",
        errorRate: "Error Rate",
        cacheSaved: "Cache Saved",
        cacheHitRate: "Cache Hit Rate",
        noData: "No data available",
      },

      // Token trend chart
      tokenChart: {
        title: "Token Usage vs Cache Savings",
        inputTokens: "Input Tokens",
        outputTokens: "Output Tokens",
        cacheRead: "Cache Read (Saved)",
      },

      // Credit trend chart
      creditChart: {
        title: "Credit Consumption",
        credits: "Credits",
        cumulative: "Cumulative",
      },

      // Error rate chart
      errorChart: {
        title: "Call Volume & Error Rate",
        callCount: "Call Count",
        errorRate: "Error Rate",
      },

      // Session panel
      sessionPanel: {
        title: "Session Usage",
        searchPlaceholder: "Search session ID...",
        filterByKey: "Filter by key",
        sessionId: "Session ID",
        calls: "Calls",
        inputTokens: "Input Tokens",
        outputTokens: "Output Tokens",
        credits: "Credits",
        firstCall: "First Call",
        lastCall: "Last Call",
        models: "Models",
        modelBreakdown: "Model Breakdown",
        model: "Model",
      },

      // Cache stats
      cachePanel: {
        title: "Cache Performance by Model",
        today: "Today",
        last7Days: "Last 7 Days",
        last30Days: "Last 30 Days",
        allTime: "All Time",
        totalCalls: "Total Calls",
        inputTokens: "Input Tokens",
        cacheRead: "Cache Read",
        hitRate: "Hit Rate",
        tokensSaved: "Tokens Saved",
      },

      // Summary table
      summaryTable: {
        period: "Period",
        keyId: "Key ID",
        calls: "Calls",
        errors: "Errors",
        input: "Input",
        output: "Output",
        cacheR: "Cache R",
        cacheW: "Cache W",
        billable: "Billable",
        credits: "Credits",
      },

      // Calls table
      callsTable: {
        time: "Time",
        key: "Key",
        model: "Model",
        target: "Target",
        source: "Source",
        in: "In",
        out: "Out",
        cacheR: "Cache R",
        cacheW: "Cache W",
        weight: "Weight",
        credits: "Credits",
        status: "Status",
      },
    },
    settings: {
      configSections: "Configuration",
      configSectionGroups: {
        core: "Models & Agents",
        tools: "Tools",
        channels: "Channels",
        automation: "Automation",
        system: "System & Storage",
      },
      configSavedState: "All changes saved",
      configLoading: "Loading configuration…",
    },
} as const;
