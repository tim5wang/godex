import { Alert, Button, Empty, Input, Segmented, Space, Spin, Tag, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useI18n } from "../../../i18n";
import type { ChatPageController } from "../ChatPage";
import { codexChatRoute } from "../sessionDriver";
import { codexStatus, codexThreads } from "./codexApi";
import "./codex.css";

export function CodexSessionsRail({ controller }: { controller: ChatPageController }) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [view, setView] = useState<"loaded" | "all">("loaded");
  useEffect(() => { const timer = setTimeout(() => setQuery(search), 250); return () => clearTimeout(timer); }, [search]);
  const token = controller.token || null;
  const status = useQuery({ queryKey: ["codex-status", token, controller.remoteNodeID], queryFn: () => codexStatus(token), retry: false, staleTime: 10_000 });
  const threads = useInfiniteQuery({
    queryKey: ["codex-threads", token, controller.remoteNodeID, query, view], initialPageParam: "",
    queryFn: ({ pageParam }) => codexThreads(token, pageParam, query, view), getNextPageParam: (page) => page.nextCursor || undefined,
    enabled: status.data?.ready === true, retry: false, staleTime: 10_000,
  });
  if (controller.v2LeftCollapsed) return <Button type="text" onClick={controller.v2ToggleLeft} aria-label={t("codex.expandSessions")}>C</Button>;
  const list = threads.data?.pages.flatMap((page) => page.data) ?? [];
  const groups = new Map<string, typeof list>();
  for (const thread of list) groups.set(thread.cwd, [...(groups.get(thread.cwd) ?? []), thread]);
  return (
    <div className="chat-v2-rail codex-sessions" data-testid="codex-sessions">
      <Space className="codex-rail-header">
        <Typography.Text strong>{t("codex.sessions")}</Typography.Text>
        <Button size="small" type="text" icon={<ReloadOutlined />} aria-label={t("codex.refreshConnection")} onClick={() => { void status.refetch(); void threads.refetch(); }} />
      </Space>
      <Segmented block size="small" aria-label={t("codex.scope")} value={view} onChange={setView} options={[{ label: t("codex.sharedSessions"), value: "loaded" }, { label: t("codex.allHistory"), value: "all" }]} />
      <Input.Search placeholder={t("codex.search")} value={search} onChange={(e) => setSearch(e.target.value)} allowClear />
      <div className="chat-v2-session-scroll codex-session-list">
        {status.isLoading ? <Spin /> : status.error ? <Alert type="error" showIcon title={status.error.message} /> : !status.data?.ready ? (
          <Alert type="warning" showIcon title={t("codex.runtimeNotReady")} description={t("codex.runtimeNotReadyHint")} />
        ) : threads.error ? <Alert type="error" showIcon title={threads.error.message} /> : threads.isLoading ? <Spin /> : (
          <>
            {[...groups].map(([cwd, items]) => (
              <section key={cwd} className="codex-workspace-group">
                <Typography.Text type="secondary" title={cwd} ellipsis>{cwd.split(/[\\/]/).filter(Boolean).pop() || cwd}</Typography.Text>
                {items.map((thread) => <button type="button" key={thread.id} className="codex-session-row" data-active={controller.routeSessionKey === thread.id} onClick={() => controller.navigate(codexChatRoute(thread.id, controller.remoteNodeID))}>
                  <span>{thread.name || thread.preview || thread.id}</span>
                  {thread.status?.type === "active" ? <Tag color="processing">{t("codex.sessionRunning")}</Tag> : thread.status?.type === "idle" ? <Tag color="green">{t("codex.shared")}</Tag> : thread.status?.type === "systemError" ? <Tag color="error">{t("codex.failed")}</Tag> : <Tag>{t("codex.history")}</Tag>}
                </button>)}
              </section>
            ))}
            {!list.length ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t(view === "loaded" ? "codex.emptyShared" : "codex.emptyHistory")} /> : null}
            {threads.hasNextPage ? <Button block type="text" loading={threads.isFetchingNextPage} onClick={() => void threads.fetchNextPage()}>{t("codex.moreSessions")}</Button> : null}
          </>
        )}
      </div>
    </div>
  );
}
