export const enProductExtraMessages = {
    // Email + password sign-in (design §3.1/§3.5).
    auth: {
      title: "Sign in to GoDex",
      subtitle: "Use your account, create a tenant, or enter the shared token.",
      modeLogin: "Sign in",
      modeRegister: "Create account",
      modeToken: "Shared token",
      email: "Email",
      emailRequired: "Enter your email address.",
      password: "Password",
      passwordRequired: "Enter your password.",
      passwordHint: "At least 8 characters.",
      tenantName: "Tenant name",
      tenantNamePlaceholder: "Defaults to your email name",
      signIn: "Sign in",
      createAccount: "Create account and tenant",
      sharedToken: "Shared web token",
      sharedTokenHint: "The pre-existing single-operator credential. It cannot be logged out.",
      sharedTokenPlaceholder: "Paste the web token",
      useSharedToken: "Use shared token",
      tokenRequired: "Enter a token.",
      loginFailed: "Sign-in failed",
      registerFailed: "Sign-up failed",
      registerSuccess: "Account created",
      registerScopeNote: "This creates a new tenant that you own. It can forward to your own nodes; it cannot run agents on this server.",
      sessionExpired: "Your session expired. Please sign in again.",
      signOut: "Sign out",
      signedInAs: "Signed in as",
      tenantLabel: "Tenant",
    },
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
