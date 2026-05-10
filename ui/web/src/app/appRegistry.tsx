import { lazy, type ComponentType, type LazyExoticComponent, type ReactNode } from "react";
import {
  ApartmentOutlined,
  AppstoreOutlined,
  BulbOutlined,
  CommentOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  SettingOutlined,
} from "@ant-design/icons";
import { Navigate, Route, type RouteObject, useLocation } from "react-router-dom";
import { PageErrorBoundary } from "../components/PageErrorBoundary";

type PageModule = Record<string, ComponentType>;
type PageLoader = () => Promise<PageModule>;

export interface BuiltinAppEntry {
  id: string;
  navPath: string;
  routePaths: string[];
  icon: ReactNode;
  labelKey: string;
  load: PageLoader;
  preload: () => void;
  component: LazyExoticComponent<ComponentType>;
  isActive: (pathname: string) => boolean;
  shellClassName?: string;
  headerTitleKey?: string;
  headerSubtitleKey?: string;
}

const loadChatPage = () => import("../pages/ChatPage");
const loadAutomationPage = () => import("../pages/AutomationPage");
const loadNodesPage = () => import("../pages/NodesPage");
const loadNotesPage = () => import("../pages/NotesPage");
const loadSkillsPage = () => import("../pages/SkillsPage");
const loadMemoryPage = () => import("../pages/MemoryPage");
const loadSettingsPage = () => import("../pages/SettingsPage");

function pageComponent(loader: PageLoader, exportName: string) {
  return lazy(async () => ({ default: (await loader())[exportName] }));
}

function entry(options: Omit<BuiltinAppEntry, "preload">): BuiltinAppEntry {
  return {
    ...options,
    preload: () => void options.load(),
  };
}

export const builtinApps: BuiltinAppEntry[] = [
  entry({
    id: "chat",
    navPath: "/chat",
    routePaths: ["/", "/chat", "/chat/:channel/:sessionKey"],
    icon: <CommentOutlined />,
    labelKey: "app.nav.chat",
    load: loadChatPage,
    component: pageComponent(loadChatPage, "ChatPage"),
    isActive: (pathname) => pathname === "/" || pathname.startsWith("/chat"),
    shellClassName: "chat-shell",
    headerTitleKey: "app.chatWorkspace",
    headerSubtitleKey: "app.subtitle",
  }),
  entry({
    id: "automation",
    navPath: "/automation",
    routePaths: ["/automation"],
    icon: <AppstoreOutlined />,
    labelKey: "app.nav.automation",
    load: loadAutomationPage,
    component: pageComponent(loadAutomationPage, "AutomationPage"),
    isActive: (pathname) => pathname.startsWith("/automation"),
    headerSubtitleKey: "automation.pageSubtitle",
  }),
  entry({
    id: "nodes",
    navPath: "/nodes",
    routePaths: ["/nodes"],
    icon: <ApartmentOutlined />,
    labelKey: "app.nav.nodes",
    load: loadNodesPage,
    component: pageComponent(loadNodesPage, "NodesPage"),
    isActive: (pathname) => pathname.startsWith("/nodes"),
    headerSubtitleKey: "nodes.pageSubtitle",
  }),
  entry({
    id: "notes",
    navPath: "/notes",
    routePaths: ["/notes"],
    icon: <FileTextOutlined />,
    labelKey: "app.nav.notes",
    load: loadNotesPage,
    component: pageComponent(loadNotesPage, "NotesPage"),
    isActive: (pathname) => pathname.startsWith("/notes"),
    headerSubtitleKey: "notes.pageSubtitle",
  }),
  entry({
    id: "skills",
    navPath: "/skills",
    routePaths: ["/skills"],
    icon: <BulbOutlined />,
    labelKey: "app.nav.skills",
    load: loadSkillsPage,
    component: pageComponent(loadSkillsPage, "SkillsPage"),
    isActive: (pathname) => pathname.startsWith("/skills"),
    headerSubtitleKey: "skills.pageSubtitle",
  }),
  entry({
    id: "memory",
    navPath: "/memory",
    routePaths: ["/memory"],
    icon: <DatabaseOutlined />,
    labelKey: "app.nav.memory",
    load: loadMemoryPage,
    component: pageComponent(loadMemoryPage, "MemoryPage"),
    isActive: (pathname) => pathname.startsWith("/memory"),
    headerTitleKey: "memory.pageTitle",
    headerSubtitleKey: "memory.pageSubtitle",
  }),
  entry({
    id: "settings",
    navPath: "/settings",
    routePaths: ["/settings"],
    icon: <SettingOutlined />,
    labelKey: "app.nav.settings",
    load: loadSettingsPage,
    component: pageComponent(loadSettingsPage, "SettingsPage"),
    isActive: (pathname) => pathname.startsWith("/settings"),
    headerSubtitleKey: "settings.pageSubtitle",
  }),
];

export function activeBuiltinApp(pathname: string): BuiltinAppEntry {
  return builtinApps.find((app) => app.isActive(pathname)) ?? builtinApps[0];
}

export function builtinAppRoutes(): RouteObject[] {
  return builtinApps.flatMap((app) =>
    app.routePaths.map((path) => {
      const Component = app.component;
      return {
        path,
        element: (
          <PageErrorBoundary appName={app.id}>
            <Component />
          </PageErrorBoundary>
        ),
      };
    }),
  );
}

export function LegacyChatRedirect() {
  const location = useLocation();
  const sessionKey = location.pathname.split("/").filter(Boolean).at(-1) ?? "";
  const search = location.search || "";
  return <Navigate to={`/chat/web/${encodeURIComponent(sessionKey)}${search}`} replace />;
}

export function renderBuiltinAppRoutes() {
  return (
    <>
      {builtinAppRoutes().map((route) => (
        <Route key={route.path} path={route.path} element={route.element} />
      ))}
      <Route path="/chat/:sessionKey" element={<LegacyChatRedirect />} />
    </>
  );
}
