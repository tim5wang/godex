import { Suspense, useMemo, useState } from "react";
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
import { useResizableWidth } from "./hooks/useResizableWidth";
import { useTerminalShortcut } from "./hooks/useGlobalKey";
import { getMeta, listProviders } from "./lib/api";
import { useSettingsStore } from "./store/settings";
import { selectAppNavLayoutState, selectTaskCenterDrawerState, useLayoutStore } from "./store/layout";
import { TaskCenterDrawerContent } from "./features/tasks/TaskCenterDrawerContent";

export default function App() {
  const { locale, t } = useI18n();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const screens = Grid.useBreakpoint();
  // P1-e: Drawer open state is a workspace-level flag (SPEC §4.6). The
  // mobile AppNav hamburger and the chat-header <TaskCenterChip> both
  // set the same flag via openTaskCenterDrawer() so the panel doubles
  // as the PC task-center entry point and the mobile AppNav.
  const drawerOpen = useLayoutStore((state) => state.taskCenterDrawerOpen);
  const closeTaskCenterDrawer = useLayoutStore((state) => state.closeTaskCenterDrawer);
  const setMobileNavOpen = closeTaskCenterDrawer;
  // P0-B: AppNav collapsed state lives in the layout store (SPEC §3.2).
  const appNav = useLayoutStore(selectAppNavLayoutState);
  const toggleAppNav = useLayoutStore((state) => state.toggle);
  // P1-c: Task Center Drawer width envelope (SPEC §4.1.1).
  // The Drawer doubles as the mobile AppNav drawer and the PC task
  // center entry point. On mobile (<1024px) we always render full-
  // width (100vw) so the persisted width envelope only applies on
  // the PC layout. The setter is exposed on the window for the chip
  // click handler in P1-e.
  const taskCenterDrawer = useLayoutStore(selectTaskCenterDrawerState);
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
  const [taskCenterWidth, beginTaskCenterResize] = useResizableWidth({
    storageKey: "godex.taskCenterWidth",
    defaultWidth: taskCenterDrawer.width,
    min: taskCenterDrawer.min,
    max: taskCenterDrawer.max,
  });
  const activeApp = activeBuiltinApp(location.pathname);
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
  const shellClassName = ["godex-shell", activeApp.shellClassName].filter(Boolean).join(" ");
  const headerTitleKey = activeApp.headerTitleKey ?? activeApp.labelKey;
  const headerSubtitleKey = activeApp.headerSubtitleKey ?? "";
  const navItems = useMemo(
    () => builtinApps.map((app) => ({ key: app.navPath, icon: app.icon, label: t(app.labelKey), onMouseEnter: app.preload })),
    [t],
  );
  const selectedKey = activeApp.navPath;
  const menu = (
    <Menu
      mode={screens.lg ? "inline" : "vertical"}
      inlineCollapsed={screens.lg ? appNav.collapsed : false}
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
        <Layout className={shellClassName}>
          {screens.lg ? (
            <Layout.Sider
              className="godex-sider"
              width={appNav.collapsed ? appNav.iconOnlyWidth : navWidth}
              collapsed={appNav.collapsed}
              collapsedWidth={appNav.iconOnlyWidth}
              trigger={null}
            >
              <div className="godex-sider-top">
                <Brand compact={appNav.collapsed} />
                <Button
                  type="text"
                  size="small"
                  aria-label={appNav.collapsed ? "Expand navigation" : "Collapse navigation"}
                  title={appNav.collapsed ? "Expand" : "Collapse"}
                  onClick={() => toggleAppNav("appNav")}
                  icon={appNav.collapsed ? <MenuOutlined /> : <MenuOutlined />}
                />
              </div>
              {menu}
              {!appNav.collapsed ? <ResizeHandle label="Resize navigation" onPointerDown={beginNavResize} /> : null}
            </Layout.Sider>
          ) : null}
          <Layout>
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
                  {headerSubtitleKey ? (
                    <Typography.Text className="muted" ellipsis>
                      {t(headerSubtitleKey)}
                    </Typography.Text>
                  ) : null}
                </Space>
              </Space>
            </Layout.Header>
            <Layout.Content className="godex-content">
              {needsProviderSetup ? (
                <FirstRunProviderGuide
                  onOpenSettings={() => navigate("/settings")}
                  onRefresh={() => void queryClient.invalidateQueries({ queryKey: ["providers", token] })}
                />
              ) : (
                <Suspense fallback={<RouteLoadingFallback />}>
                  <Routes>{renderBuiltinAppRoutes()}</Routes>
                </Suspense>
              )}
            </Layout.Content>
          </Layout>
          {/* P1-c: Drawer doubles as mobile AppNav (left, full-screen) and
              PC Task Center entry point. On <1024px we ignore the
              taskCenterWidth envelope and let antd render full-width.
              P1-f: open state is driven by useLayoutStore.taskCenterDrawerOpen
              (see chat-header <TaskCenterChip>); the close handler is
              the same closeTaskCenterDrawer action.
              P1-g-2: when the chat workspace is mounted it provides a
              <TaskCenterPanel> bridge via React context, which the
              Drawer children surface here. Before the chat page mounts
              (e.g. on the home route or during a route transition) the
              bridge is null and we fall back to the AppNav <Menu>. */}
          <Drawer
            title={<Brand compact />}
            placement="left"
            width={screens.lg ? taskCenterWidth : "100vw"}
            open={drawerOpen}
            onClose={closeTaskCenterDrawer}
            data-testid="task-center-drawer"
            data-mode={screens.lg ? "pc-task-center" : "mobile-appnav"}
          >
            <TaskCenterDrawerContent fallback={menu} />
            {screens.lg ? (
              <ResizeHandle label="Resize task center" onPointerDown={beginTaskCenterResize} />
            ) : null}
          </Drawer>
        </Layout>
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
  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const version = metaQuery.data?.version?.version;
  return (
    <div className={compact ? "" : "godex-brand"} title={version ? `GoDex ${version}` : undefined}>
      <span className="godex-brand-mark">G</span>
      <span>{t("app.title")}</span>
      {version ? <Typography.Text type="secondary">{version}</Typography.Text> : null}
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
