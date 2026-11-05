import { Segmented } from "antd";
import { useI18n } from "../../../i18n";
import { APIError, getSnapshot, openSession } from "../../../lib/api";
import { buildChatRouteForSession } from "../../../lib/chatRoutes";
import { useSettingsStore } from "../../../store/settings";
import { SessionsRail } from "./SessionsRail";
import { WorkDesktopRail } from "../../workdesktop/WorkDesktopRail";
import type { ChatPageController } from "../ChatPage";

/**
 * The chat page's left column, which has TWO views:
 *
 *  - "directory": the conversation list (SessionsRail), grouped by workspace.
 *  - "desktop":   the work desktop list (WorkDesktopRail), i.e. the long-running
 *                 work streams. This is the fusion: the standalone desktop page
 *                 became one view of the rail instead of its own page, so the
 *                 desktop inherits the chat's layout and its mobile handling
 *                 rather than maintaining a parallel set of layout rules.
 *
 * Only the left column changes between views. The centre column is always the
 * chat, which is the point — the aircraft-carrier chat stays untouched.
 *
 * Extracted from ChatPageView because that file is at its line budget: anything
 * that grows there must live outside it.
 */
export function ChatLeftRail({ controller }: { controller: ChatPageController }) {
  const { t } = useI18n();
  // Read directly rather than off the controller: clearing the credential is a
  // settings-store action that only this rail's 403 message needs.
  const clearToken = useSettingsStore((state) => state.clearToken);
  const collapsed = controller.v2LeftCollapsed;
  const view = controller.v2LeftView;

  const viewSwitcher = (
    <Segmented
      size="small"
      value={view}
      onChange={(value) => controller.v2SetLeftView(value as "directory" | "desktop")}
      options={[
        { value: "directory", label: t("workDesktop.viewDirectory") },
        { value: "desktop", label: t("workDesktop.viewDesktop") },
      ]}
      data-testid="chat-v2-left-view"
    />
  );

  return (
    <div className="chat-v2-left" data-collapsed={collapsed ? "true" : "false"} data-left-view={view}>
      {!collapsed ? (
        <div className="chat-v2-left-resize" onPointerDown={controller.beginV2LeftResize} role="separator" aria-label="Resize left panel" />
      ) : null}
      {view === "desktop" ? (
        <WorkDesktopRail collapsed={collapsed} viewSwitcher={viewSwitcher} onToggleCollapsed={() => controller.v2ToggleLeft()} />
      ) : (
        <SessionsRail
          collapsed={collapsed}
          viewSwitcher={viewSwitcher}
          sessions={controller.filteredSessions}
          loading={controller.sessionsQuery.isLoading}
          error={controller.sessionsQuery.isError}
          // A 403 here is a stable fact, not a hiccup: this credential may not use
          // this instance's own workspace. Distinguished from a transient load
          // failure so the rail can explain it instead of offering a retry that
          // would fail identically forever.
          refused={controller.sessionsQuery.error instanceof APIError && controller.sessionsQuery.error.status === 403}
          onPickNode={() => controller.navigate("/nodes")}
          onUseSharedToken={() => clearToken()}
          onRetry={() => void controller.queryClient.invalidateQueries({ queryKey: ["sessions", controller.token, controller.remoteNodeID] })}
          activeSessionId={controller.openQuery.data?.session_id ?? ""}
          searchQuery={controller.v2SessionSearch ?? ""}
          deletingSessionId={controller.deleteSessionMutation.variables?.session_id ?? ""}
          onSearchChange={controller.setV2SessionSearch}
          skillsCatalog={controller.skillsCatalogQuery.data ?? []}
          skillsLoading={controller.skillsCatalogQuery.isLoading}
          templates={controller.templatesQuery.data ?? []}
          templatesLoading={controller.templatesQuery.isLoading}
          nodes={controller.nodesQuery.data ?? []}
          nodesLoading={controller.nodesQuery.isLoading}
          onCreate={(workspaceDir, template, skills, execMode) => controller.createSession(false, workspaceDir, template, skills, execMode)}
          onPrefetch={(session) => {
            // Warm the target session before the click so a cold-session load (the
            // open POST reads session files from disk, multi-second) happens on
            // hover instead of after navigation. Fire-and-forget: a failed prefetch
            // is harmless — the real click path retries.
            const locator = session.locator;
            const meta = locator.metadata ?? {};
            const query: Record<string, string> = {};
            if (locator.user_id) query.user_id = locator.user_id;
            if (meta.project_dir) query.workspace_dir = meta.project_dir;
            if (meta.mode) query.mode = meta.mode;
            if (meta.template) query.template = meta.template;
            if (meta.requested_skills) query.skills = meta.requested_skills;
            const warm = async () => {
              const opened = await controller.queryClient.fetchQuery({
                queryKey: ["session-open", controller.token, locator.channel || "web", locator.key, locator.user_id],
                queryFn: () => openSession(controller.token || null, { channel: locator.channel || "web", key: locator.key, ...(locator.user_id ? { user_id: locator.user_id } : {}), ...(Object.keys(query).length > 0 ? { metadata: query } : {}) }),
                staleTime: 30 * 1000,
              });
              if (opened?.session_id) {
                await controller.queryClient.prefetchQuery({
                  queryKey: ["snapshot", controller.token, opened.session_id],
                  queryFn: () => getSnapshot(controller.token || null, opened.session_id),
                  staleTime: 10 * 1000,
                });
              }
            };
            void warm().catch(() => {});
          }}
          onSelect={(session) => {
            controller.navigate(buildChatRouteForSession(session));
          }}
          onDelete={(session) => controller.deleteSessionMutation.mutate(session)}
          renamingSessionId={controller.renameSessionMutation.variables?.sessionId ?? ""}
          onRename={(session, title) => controller.renameSessionMutation.mutate({ sessionId: session.session_id, title })}
          onToggleCollapsed={() => controller.v2ToggleLeft()}
        />
      )}
    </div>
  );
}
