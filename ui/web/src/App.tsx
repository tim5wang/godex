import { Suspense, useLayoutEffect, useMemo, useState } from "react";
import { Routes, useLocation, useNavigate } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  ConfigProvider,
  Drawer,
  Grid,
  Layout,
  Menu,
  Space,
  Spin,
  Typography,
  theme,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import enUS from "antd/locale/en_US";
import zhCN from "antd/locale/zh_CN";
import { MenuOutlined } from "@ant-design/icons";
import { activeBuiltinApp, builtinApps, renderBuiltinAppRoutes } from "./app/appRegistry";
import { useI18n } from "./i18n";
import { ResizeHandle } from "./components/ResizeHandle";
import { WorkspaceShell, buildWorkspaceShellClassName } from "./components/workspace/WorkspaceShell";
import { useResizableWidth } from "./hooks/useResizableWidth";
import { useTerminalShortcut } from "./hooks/useGlobalKey";
import { getMeta, listProviders } from "./lib/api";
import { useSettingsStore } from "./store/settings";
import { useLayoutStore } from "./store/layout";
import {
  LAYOUT_STORAGE_KEY,
  applyLayoutSnapshot,
  clearPersistedLayoutSnapshot,
  readPersistedLayoutSnapshot,
  serializeLayoutSnapshot,
  writePersistedLayoutSnapshot,
} from "./store/layoutPersistence";

export default function App() {
  const { locale, t } = useI18n();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const screens = Grid.useBreakpoint();
  // The legacy layout drawer flag is now only used for the mobile
  // AppNav drawer. PC TaskCenter lives inside ChatPage's shared
  // inspector panel, not in an App-level Drawer.
  const drawerOpen = useLayoutStore((state) => state.taskCenterDrawerOpen);
  const closeTaskCenterDrawer = useLayoutStore((state) => state.closeTaskCenterDrawer);
  const openTaskCenterDrawer = useLayoutStore((state) => state.openTaskCenterDrawer);
  const setMobileNavOpen = (open: boolean) => {
    if (open) {
      openTaskCenterDrawer();
    } else {
      closeTaskCenterDrawer();
    }
  };
  // P0-B: AppNav collapsed state lives in the layout store (SPEC §3.2).
  const appNavCollapsed = useLayoutStore((state) => state.panels.appNav.collapsed);
  const appNavWidth = useLayoutStore((state) => state.panels.appNav.width ?? 200);
  const APP_NAV_ICON_WIDTH = 48;
  const toggleAppNav = useLayoutStore((state) => state.toggle);
  // P1-c: Task Center Drawer width envelope (SPEC §4.1.1).
  // The Drawer doubles as the mobile AppNav drawer and the PC task
  // center entry point. On mobile (<1024px) we always render full-
  // width (100vw) so the persisted width envelope only applies on
  // the PC layout. The setter is exposed on the window for the chip
  // click handler in P1-e.
  // P4 / T8 (SPEC §3.1 + §4.6): hydrate the layout store from
  // localStorage on first mount, subscribe to subsequent changes
  // and write them back, and listen for the cross-tab `storage`
  // event so multiple tabs stay in sync. We hand-roll the
  // persistence layer (see store/layoutPersistence.ts) instead of
  // adopting zustand/middleware/persist so the snapshot shape
  // stays under our control and the cross-tab path is a single
  // dispatch. The hydrate call uses a one-shot effect that runs
  // *before* the first paint, so the user never sees the default
  // snapshot flicker into a hydrated one.
  useLayoutEffect(() => {
    // P4 / T8 (SPEC §3.1 + §4.6): hydrate the layout store from
    // localStorage on first mount, subscribe to subsequent changes
    // and write them back, and listen for the cross-tab `storage`
    // event so multiple tabs stay in sync.
    const persisted = readPersistedLayoutSnapshot();
    if (persisted) {
      useLayoutStore.setState(applyLayoutSnapshot(persisted));
    }
    const unsubscribe = useLayoutStore.subscribe((state) => {
      // Defer to microtask to avoid sync I/O during commit phase.
      queueMicrotask(() => {
        writePersistedLayoutSnapshot(serializeLayoutSnapshot(state));
      });
    });
    const onStorage = (event: StorageEvent) => {
      if (event.key !== LAYOUT_STORAGE_KEY) return;
      if (!event.newValue) {
        useLayoutStore.getState().reset();
        return;
      }
      const incoming = JSON.parse(event.newValue) as Parameters<typeof applyLayoutSnapshot>[0];
      useLayoutStore.setState(applyLayoutSnapshot(incoming));
    };
    window.addEventListener("storage", onStorage);
    return () => {
      unsubscribe();
      window.removeEventListener("storage", onStorage);
    };
  }, []);

  // P3 / T7 (SPEC §4.4): Ctrl/Cmd + ` toggles the terminal panel
  // visibility (PC only — mobile surfaces the terminal via the
  // secondary tab bar, not a keyboard shortcut). The hook is
  // wired into the layout store's toggle action so the change
  // persists with the rest of the workspace state.
  const toggleTerminal = useLayoutStore((state) => state.toggle);
  useTerminalShortcut(
    () => toggleTerminal("terminal"),
    { active: screens.lg },
  );
  const [navWidth, beginNavResize] = useResizableWidth({
    storageKey: "godex.navWidth",
    defaultWidth: 228,
    min: 176,
    max: 360,
  });
  const activeApp = activeBuiltinApp(location.pathname);
  // Memoize route elements so App re-renders do not recreate
  // <PageErrorBoundary> wrappers, which could trigger React Router
  // to unmount/remount lazy-loaded route components.
  const memoizedRoutes = useMemo(() => renderBuiltinAppRoutes(), []);
  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const providersQuery = useQuery({
    queryKey: ["providers", token],
    enabled: !(metaQuery.data?.auth_required ?? false) || token.trim().length > 0,
    queryFn: () => listProviders(token || null),
  });
  const needsProviderSetup =
    activeApp.id !== "settings" &&
    providersQuery.isSuccess &&
    !providersQuery.data.providers.some((provider) => provider.has_credential || provider.token_present);
  const shellClassName = buildWorkspaceShellClassName(activeApp.shellClassName);
  const headerTitleKey = activeApp.headerTitleKey ?? activeApp.labelKey;
  const headerSubtitleKey = activeApp.headerSubtitleKey ?? "";
  const workspaceSubtitle = activeApp.id === "chat" && metaQuery.data?.workspace_dir
    ? [metaQuery.data.workspace_dir, metaQuery.data.model, metaQuery.data.version?.version].filter(Boolean).join(" · ")
    : "";
  const headerSubtitle = workspaceSubtitle || (headerSubtitleKey ? t(headerSubtitleKey) : "");
  const navItems = useMemo(
    () => builtinApps.map((app) => ({ key: app.navPath, icon: app.icon, label: t(app.labelKey), onMouseEnter: app.preload })),
    [t],
  );
  const selectedKey = activeApp.navPath;
  const menu = (
    <Menu
      mode={screens.lg ? "inline" : "vertical"}
      inlineCollapsed={screens.lg ? appNavCollapsed : false}
      selectedKeys={[selectedKey]}
      items={navItems}
      onClick={(info) => {
        navigate(info.key);
        setMobileNavOpen(false);
      }}
    />
  );

  return (
    <ConfigProvider
      locale={locale === "zh" ? zhCN : enUS}
      theme={{
        algorithm: theme.defaultAlgorithm,
        token: {
          colorPrimary: "#0f766e",
          borderRadius: 8,
          colorInfo: "#2563eb",
          colorWarning: "#b45309",
          colorError: "#b42318",
          fontFamily:
            'Inter, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", "Microsoft YaHei", system-ui, sans-serif',
        },
      }}
    >
      <AntApp>
        <WorkspaceShell
          shellClassName={shellClassName}
          appNav={screens.lg ? (
            <Layout.Sider
              className="godex-sider"
              width={appNavCollapsed ? APP_NAV_ICON_WIDTH : navWidth}
              collapsed={appNavCollapsed}
              collapsedWidth={APP_NAV_ICON_WIDTH}
              trigger={null}
            >
              <div className="godex-sider-top">
                <Brand compact={appNavCollapsed} />
                <Button
                  type="text"
                  size="small"
                  aria-label={appNavCollapsed ? "Expand navigation" : "Collapse navigation"}
                  title={appNavCollapsed ? "Expand" : "Collapse"}
                  onClick={() => toggleAppNav("appNav")}
                  icon={appNavCollapsed ? <MenuOutlined /> : <MenuOutlined />}
                />
              </div>
              {menu}
              {!appNavCollapsed ? <ResizeHandle label="Resize navigation" onPointerDown={beginNavResize} /> : null}
            </Layout.Sider>
          ) : null}
          header={(
            <Layout.Header className="godex-header">
              <Space size={12}>
                {!screens.lg ? (
                  <Button
                    type="text"
                    icon={<MenuOutlined />}
                    aria-label="Open navigation"
                    onClick={() => setMobileNavOpen(true)}
                  />
                ) : null}
                <Space direction="vertical" size={0} className="godex-header-title">
                  <Typography.Text strong>{t(headerTitleKey)}</Typography.Text>
                  {headerSubtitle ? (
                    <Typography.Text className="muted" ellipsis>
                      {headerSubtitle}
                    </Typography.Text>
                  ) : null}
                </Space>
              </Space>
            </Layout.Header>
          )}
          content={(
            <Layout.Content className="godex-content">
              {needsProviderSetup ? (
                <FirstRunProviderGuide
                  onOpenSettings={() => navigate("/settings")}
                  onRefresh={() => void queryClient.invalidateQueries({ queryKey: ["providers", token] })}
                />
              ) : (
                <Suspense fallback={<RouteLoadingFallback />}>
                  <Routes>{memoizedRoutes}</Routes>
                </Suspense>
              )}
            </Layout.Content>
          )}
          drawer={(
            <>
              {!screens.lg ? (
                <Drawer
                  title={<Brand compact />}
                  placement="left"
                  width="100vw"
                  open={drawerOpen}
                  onClose={closeTaskCenterDrawer}
                  data-testid="mobile-appnav-drawer"
                  data-mode="mobile-appnav"
                >
                  {menu}
                </Drawer>
              ) : null}
            </>
          )}
        />
      </AntApp>
    </ConfigProvider>
  );
}

function FirstRunProviderGuide(props: { onOpenSettings: () => void; onRefresh: () => void }) {
  return (
    <main className="page-shell">
      <Card>
        <Space direction="vertical" size={16} style={{ width: "100%" }}>
          <Alert
            type="info"
            showIcon
            message="Configure a model provider to start using GoDex."
            description="GoDex can open before credentials are ready, but chat turns need at least one provider with an API key or OAuth token."
          />
          <Space wrap>
            <Button type="primary" onClick={props.onOpenSettings}>
              Configure provider
            </Button>
            <Button onClick={props.onRefresh}>Recheck providers</Button>
          </Space>
        </Space>
      </Card>
    </main>
  );
}

function Brand({ compact = false }: { compact?: boolean }) {
  const { t } = useI18n();
  return (
    <div className={compact ? "" : "godex-brand"}>
      <span className="godex-brand-mark">G</span>
      <span>{t("app.title")}</span>
    </div>
  );
}

function RouteLoadingFallback() {
  const { t } = useI18n();
  return (
    <div style={{ display: "grid", minHeight: 260, placeItems: "center" }}>
      <Space direction="vertical" align="center">
        <Spin />
        <Typography.Text type="secondary">{t("app.loading")}</Typography.Text>
      </Space>
    </div>
  );
}
