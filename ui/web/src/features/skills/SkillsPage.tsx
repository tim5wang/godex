import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Badge,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Progress,
  Segmented,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { DeleteOutlined, DownloadOutlined, EyeOutlined, ReloadOutlined } from "@ant-design/icons";
import { MarkdownContent } from "../../components/MarkdownContent";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  getActiveSessionSkills,
  getMeta,
  getSessionSkill,
  getPackageQuality,
  installPackage,
  installSessionSkill,
  listPackageCommands,
  listPackageRoles,
  listPackages,
  listPrompts,
  listSessionSkillSources,
  listSessionSkills,
  loadSessionSkill,
  normalizeSessionSkill,
  openSession,
  removePackage,
  removeSessionSkill,
  reinstallPackage,
  runPackageSmoke,
  unloadSessionSkill,
  expandSessionSkill,
} from "../../lib/api";
import { buildChatRoute } from "../../lib/chatRoutes";
import type { PackageCommandEntry, PackageEntry, PackageQuality, PackageQualityReport, PackageRoleEntry, PromptEntry, SkillActivation, SkillCatalogEntry, SkillSourceEntry, ToolStat } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";

type MarketFilter = "all" | "active" | "ready" | "needs_attention";
type SourceSort = "featured" | "name" | "trust" | "version" | "popularity";

function makeSessionKey() {
  return crypto.randomUUID();
}

export function SkillsPage() {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const defaultSessionKey = useSettingsStore((state) => state.defaultSessionKey);
  const setDefaultSessionKey = useSettingsStore((state) => state.setDefaultSessionKey);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [sourceSearch, setSourceSearch] = useState("");
  const [sourceCategory, setSourceCategory] = useState("all");
  const [sourceSort, setSourceSort] = useState<SourceSort>("featured");
  const [marketFilter, setMarketFilter] = useState<MarketFilter>("all");
  const [compatibility, setCompatibility] = useState("all");
  const [installing, setInstalling] = useState(false);
  const [installingPackage, setInstallingPackage] = useState(false);
  const [removingSkill, setRemovingSkill] = useState<string | null>(null);
  const [removingPackage, setRemovingPackage] = useState<string | null>(null);
  const [reinstallingPackage, setReinstallingPackage] = useState<string | null>(null);
  const [runningSmoke, setRunningSmoke] = useState<string | null>(null);
  const [busySkill, setBusySkill] = useState<string | null>(null);
  const [selectedSkillId, setSelectedSkillId] = useState<string | null>(null);
  const [installForm] = Form.useForm<{ source: string; name?: string }>();
  const [packageForm] = Form.useForm<{ source: string }>();
  const sessionKey = defaultSessionKey || "";

  useEffect(() => {
    if (!defaultSessionKey) {
      setDefaultSessionKey(makeSessionKey());
    }
  }, [defaultSessionKey, setDefaultSessionKey]);

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachSkills = !authRequired || !!token;
  const openQuery = useQuery({
    queryKey: ["skills-session-open", token, sessionKey],
    enabled: !!sessionKey && canReachSkills,
    queryFn: async () => openSession(token || null, { channel: "web", key: sessionKey }),
  });
  const sessionId = openQuery.data?.session_id ?? "";
  const catalogQuery = useQuery({
    queryKey: ["skills-catalog", token, sessionId],
    enabled: !!sessionId && canReachSkills,
    queryFn: async () => listSessionSkills(token || null, sessionId),
  });
  const activeQuery = useQuery({
    queryKey: ["skills-active", token, sessionId],
    enabled: !!sessionId && canReachSkills,
    queryFn: async () => getActiveSessionSkills(token || null, sessionId),
  });
  const sourcesQuery = useQuery({
    queryKey: ["skills-sources", token, sessionId, sourceSearch],
    enabled: !!sessionId && canReachSkills,
    queryFn: async () => listSessionSkillSources(token || null, sessionId, sourceSearch),
  });
  const trendingQuery = useQuery({
    queryKey: ["skills-sources-trending", token, sessionId],
    enabled: !!sessionId && canReachSkills,
    queryFn: async () => listSessionSkillSources(token || null, sessionId, "", { mode: "trending" }),
  });
  const detailQuery = useQuery({
    queryKey: ["skills-detail", token, sessionId, selectedSkillId],
    enabled: !!sessionId && !!selectedSkillId && canReachSkills,
    queryFn: async () => getSessionSkill(token || null, sessionId, selectedSkillId!),
  });
  const packageQualityQuery = useQuery({
    queryKey: ["packages-quality", token],
    enabled: canReachSkills,
    queryFn: async () => getPackageQuality(token || null),
  });
  const packagesQuery = useQuery({
    queryKey: ["packages", token],
    enabled: canReachSkills,
    queryFn: async () => listPackages(token || null),
  });
  const promptsQuery = useQuery({
    queryKey: ["prompts", token],
    enabled: canReachSkills,
    queryFn: async () => listPrompts(token || null, false),
  });
  const packageCommandsQuery = useQuery({
    queryKey: ["package-commands", token],
    enabled: canReachSkills,
    queryFn: async () => listPackageCommands(token || null, false),
  });
  const packageRolesQuery = useQuery({
    queryKey: ["package-roles", token],
    enabled: canReachSkills,
    queryFn: async () => listPackageRoles(token || null, false),
  });

  const catalog = catalogQuery.data ?? [];
  const active = activeQuery.data ?? [];
  const packages = packagesQuery.data ?? [];
  const prompts = promptsQuery.data ?? [];
  const packageCommands = packageCommandsQuery.data ?? [];
  const packageRoles = packageRolesQuery.data ?? [];
  const activeMap = useMemo(() => new Map(active.map((item) => [item.id, item])), [active]);
  const sources = sourceSearch.trim() ? sourcesQuery.data ?? [] : [...(trendingQuery.data ?? []), ...(sourcesQuery.data ?? [])];
  const filteredCatalog = useMemo(() => {
    const query = search.trim().toLowerCase();
    return catalog.filter((item) => {
      const activeItem = activeMap.get(item.id);
      if (query) {
        const haystack = [
          item.id,
          item.name,
          item.description,
          item.suite_id,
          ...(item.child_skill_ids ?? []),
          ...(item.categories ?? []),
          ...(item.sections ?? []),
        ].join(" ").toLowerCase();
        if (!haystack.includes(query)) {
          return false;
        }
      }
      if (compatibility !== "all" && item.compatibility?.status !== compatibility) {
        return false;
      }
      if (marketFilter === "active" && !activeItem) {
        return false;
      }
      if (marketFilter === "ready" && !skillReady(item)) {
        return false;
      }
      if (marketFilter === "needs_attention" && !skillNeedsAttention(item)) {
        return false;
      }
      return true;
    });
  }, [activeMap, catalog, compatibility, marketFilter, search]);
  const sourceCategories = useMemo(
    () => Array.from(new Set(sources.flatMap((item) => item.categories ?? []))).sort(),
    [sources],
  );
  const filteredSources = useMemo(() => {
    let items = sourceCategory === "all" ? sources : sources.filter((item) => item.categories?.includes(sourceCategory));
    items = [...items];
    if (sourceSort === "name") {
      items.sort((a, b) => a.name.localeCompare(b.name));
    } else if (sourceSort === "popularity") {
      items.sort((a, b) => (b.installs ?? 0) - (a.installs ?? 0));
    } else if (sourceSort === "trust") {
      items.sort((a, b) => trustRank(b.trust) - trustRank(a.trust));
    } else if (sourceSort === "version") {
      items.sort((a, b) => (b.version || "").localeCompare(a.version || ""));
    }
    return items;
  }, [sourceCategory, sourceSort, sources]);

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["skills-catalog", token, sessionId] }),
      queryClient.invalidateQueries({ queryKey: ["skills-detail", token, sessionId] }),
      queryClient.invalidateQueries({ queryKey: ["skills-active", token, sessionId] }),
      queryClient.invalidateQueries({ queryKey: ["skills-sources", token, sessionId] }),
      queryClient.invalidateQueries({ queryKey: ["skills-sources-trending", token, sessionId] }),
      queryClient.invalidateQueries({ queryKey: ["packages", token] }),
      queryClient.invalidateQueries({ queryKey: ["packages-quality", token] }),
      queryClient.invalidateQueries({ queryKey: ["prompts", token] }),
      queryClient.invalidateQueries({ queryKey: ["package-commands", token] }),
      queryClient.invalidateQueries({ queryKey: ["package-roles", token] }),
    ]);
  };

  const runAction = async (skillName: string, action: () => Promise<unknown>) => {
    setBusySkill(skillName);
    try {
      await action();
      await refresh();
      void message.success("Skill stack updated.");
    } catch (error) {
      showError(message, error, "Skill action failed.");
    } finally {
      setBusySkill(null);
    }
  };

  const installManual = async (values: { source: string; name?: string }) => {
    if (!sessionId || !values.source.trim()) {
      return;
    }
    setInstalling(true);
    try {
      const result = await installSessionSkill(token || null, sessionId, values.source.trim(), values.name?.trim() || undefined);
      void message.success(`Installed ${result.name}.`);
      installForm.resetFields();
      await refresh();
    } catch (error) {
      showError(message, error, "Skill install failed.");
    } finally {
      setInstalling(false);
    }
  };

  const installSource = async (item: SkillSourceEntry) => {
    const source = item.install_source || item.source;
    const name = item.install_name || item.skill_name || item.name;
    await runAction(`source:${item.id}`, async () => installSessionSkill(token || null, sessionId, source, name));
  };

  const removeInstalledSkill = async (name: string) => {
    const target = name.trim();
    if (!sessionId || !target) {
      return;
    }
    setRemovingSkill(target);
    try {
      const result = await removeSessionSkill(token || null, sessionId, target);
      void message.success(t("skills.removedSkill", { name: result.name || result.id }));
      await refresh();
    } catch (error) {
      showError(message, error, "Skill remove failed.");
    } finally {
      setRemovingSkill(null);
    }
  };

  const normalizeInstalledSkill = async (name: string) => {
    const target = name.trim();
    if (!sessionId || !target) {
      return;
    }
    await runAction(target, async () => normalizeSessionSkill(token || null, sessionId, target));
    void message.success(`Normalized ${target}.`);
  };

  const installPackageSource = async (values: { source: string }) => {
    const source = values.source.trim();
    if (!source) {
      return;
    }
    setInstallingPackage(true);
    try {
      const item = await installPackage(token || null, source);
      packageForm.resetFields();
      void message.success(`Installed package ${item.name}.`);
      await refresh();
    } catch (error) {
      showError(message, error, "Package install failed.");
    } finally {
      setInstallingPackage(false);
    }
  };

  const removeInstalledPackage = async (item: PackageEntry) => {
    setRemovingPackage(item.name);
    try {
      await removePackage(token || null, item.name);
      void message.success(`Removed package ${item.name}.`);
      await refresh();
    } catch (error) {
      showError(message, error, "Package remove failed.");
    } finally {
      setRemovingPackage(null);
    }
  };

  const reinstallInstalledPackage = async (item: PackageEntry) => {
    setReinstallingPackage(item.name);
    try {
      await reinstallPackage(token || null, item.name);
      void message.success(`Reinstalled package ${item.name}.`);
      await refresh();
    } catch (error) {
      showError(message, error, "Package reinstall failed.");
    } finally {
      setReinstallingPackage(null);
    }
  };

  const runInstalledPackageSmoke = async (item: PackageEntry, smokeName: string) => {
    const key = `${item.name}:${smokeName}`;
    setRunningSmoke(key);
    try {
      const run = await runPackageSmoke(token || null, item.name, smokeName, sessionId || undefined);
      if (run.pending_approval) {
        void message.warning(`Smoke ${smokeName} is waiting for approval.`);
      } else if (run.status === "passed") {
        void message.success(`Smoke ${smokeName} passed.`);
      } else {
        void message.warning(`Smoke ${smokeName} ${run.status}.`);
      }
      await refresh();
    } catch (error) {
      showError(message, error, "Package smoke failed.");
    } finally {
      setRunningSmoke(null);
    }
  };

  if (authRequired && !token) {
    return (
      <div className="page-pad">
        <Alert
          type="warning"
          showIcon
          message={
            <>
              This server requires `GODEX_WEB_TOKEN`. Open <Link to="/settings">Settings</Link> and paste the shared bearer token.
            </>
          }
        />
      </div>
    );
  }

  return (
    <div className="page-pad">
      <div className="page-action-row">
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={() => void refresh()}>Refresh</Button>
          <Button onClick={() => navigate(sessionKey ? buildChatRoute({ channel: "web", key: sessionKey }) : "/chat")}>{t("skills.openChat")}</Button>
        </Space>
      </div>

      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <Metric title="Catalog" value={catalog.length} />
        <Metric title="Active" value={active.length} />
        <Metric title="Sources" value={filteredSources.length} />
        <Metric title="Packages" value={packages.length} />
        <Metric title="Commands" value={packageCommands.length} />
        <Metric title="Roles" value={packageRoles.length} />
      </div>

      <SkillAnalyticsPanel
        loading={packageQualityQuery.isLoading}
        analytics={qualityToAnalytics(packageQualityQuery.data)}
        installedSkillCount={catalog.length}
        activeSkillCount={active.length}
        t={t}
      />

      <PackageQualityPanel report={packageQualityQuery.data} loading={packageQualityQuery.isLoading} />

      <Tabs
        items={[
          {
            key: "marketplace",
            label: "Marketplace",
            children: (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <Card title="Install from source">
                  <Form form={installForm} layout="inline" onFinish={installManual}>
                    <Form.Item name="source" rules={[{ required: true }]} style={{ flex: 1 }}>
                      <Input placeholder="skills/playwright-cli, https://github.com/org/repo, or org/repo" />
                    </Form.Item>
                    <Form.Item name="name">
                      <Input placeholder="optional name/subdir" />
                    </Form.Item>
                    <Button type="primary" icon={<DownloadOutlined />} htmlType="submit" loading={installing}>Install</Button>
                  </Form>
                </Card>
                <Card>
                  <div className="list-toolbar">
                    <Space wrap>
                      <Input.Search value={sourceSearch} onChange={(event) => setSourceSearch(event.target.value)} placeholder="Search sources" allowClear style={{ width: 260 }} />
                      <Segmented value={sourceCategory} onChange={(value) => setSourceCategory(String(value))} options={["all", ...sourceCategories]} />
                    </Space>
                    <Select value={sourceSort} onChange={setSourceSort} style={{ width: 160 }} options={[
                      { value: "featured", label: t("skills.sortFeatured") },
                      { value: "name", label: t("skills.sortName") },
                      { value: "trust", label: t("skills.sortTrust") },
                      { value: "version", label: t("skills.sortVersion") },
                      { value: "popularity", label: t("skills.sortPopularity") },
                    ]} />
                  </div>
                  <Table<SkillSourceEntry>
                    rowKey="id"
                    loading={sourcesQuery.isLoading || trendingQuery.isLoading}
                    dataSource={filteredSources}
                    pagination={{ pageSize: 8 }}
                    columns={[
                      { title: "Source", render: (_value, item) => <SkillSourceSummary item={item} /> },
                      { title: "Trust", dataIndex: "trust", render: (value) => value ? <Tag>{value}</Tag> : "-" },
                      { title: "Installs", dataIndex: "installs", render: (value) => typeof value === "number" ? value.toLocaleString() : "-" },
                      {
                        title: "Action",
                        render: (_value, item) => {
                          const installedName = installedSkillName(item);
                          if (item.installed) {
                            return (
                              <Popconfirm title={t("skills.removeConfirm")} onConfirm={() => void removeInstalledSkill(installedName)}>
                                <Button
                                  danger
                                  icon={<DeleteOutlined />}
                                  loading={removingSkill === installedName}
                                  disabled={!sessionId}
                                >
                                  {t("skills.remove")}
                                </Button>
                              </Popconfirm>
                            );
                          }
                          return (
                            <Button
                              type={!item.install_supported ? "default" : "primary"}
                              disabled={!item.install_supported || !sessionId}
                              loading={busySkill === `source:${item.id}`}
                              onClick={() => void installSource(item)}
                            >
                              {item.install_supported ? t("skills.install") : t("skills.unavailable")}
                            </Button>
                          );
                        },
                      },
                    ]}
                  />
                </Card>
              </Space>
            ),
          },
          {
            key: "catalog",
            label: "Catalog",
            children: (
              <Card>
                <div className="list-toolbar">
                  <Space wrap>
                    <Input.Search value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search skills, sections, or bundles" allowClear style={{ width: 300 }} />
                    <Select value={compatibility} onChange={setCompatibility} style={{ width: 220 }} options={[
                      { value: "all", label: t("skills.compatibilityAll") },
                      { value: "native_supported", label: t("skills.compatibilityNative") },
                      { value: "degraded_supported", label: t("skills.compatibilityDegraded") },
                      { value: "unsupported", label: t("skills.compatibilityUnsupported") },
                    ]} />
                  </Space>
                  <Segmented value={marketFilter} onChange={(value) => setMarketFilter(value as MarketFilter)} options={[
                    { value: "all", label: "All" },
                    { value: "active", label: "Active" },
                    { value: "ready", label: "Ready" },
                    { value: "needs_attention", label: "Needs attention" },
                  ]} />
                </div>
                <Table<SkillCatalogEntry>
                  rowKey="id"
                  loading={catalogQuery.isLoading}
                  dataSource={filteredCatalog}
                  pagination={{ pageSize: 8 }}
                  columns={[
                    { title: "Skill", render: (_value, item) => <SkillSummary item={item} active={activeMap.get(item.id)} /> },
                    { title: "Compatibility", render: (_value, item) => <Tag color={compatibilityColor(item.compatibility?.status)}>{item.compatibility?.status || "unknown"}</Tag> },
                    {
                      title: "Actions",
                      render: (_value, item) => {
                        const activeItem = activeMap.get(item.id);
                        const canNormalize = canRunNormalize(item);
                        return (
                          <Space wrap>
                            <Button aria-label="View skill details" icon={<EyeOutlined />} onClick={() => setSelectedSkillId(item.id)} />
                            {activeItem ? (
                              <Button loading={busySkill === item.id} onClick={() => void runAction(item.id, () => unloadSessionSkill(token || null, sessionId, item.id))}>Unload</Button>
                            ) : (
                              <Button type="primary" loading={busySkill === item.id} onClick={() => void runAction(item.id, () => loadSessionSkill(token || null, sessionId, item.id))}>Load</Button>
                            )}
                            {canNormalize ? (
                              <Popconfirm
                                title="Run LLM normalization for this skill?"
                                description="This may spend model tokens. Catalog, install, and chat never do this automatically."
                                onConfirm={() => void normalizeInstalledSkill(item.id)}
                              >
                                <Button loading={busySkill === item.id}>Normalize</Button>
                              </Popconfirm>
                            ) : <NormalizationTag item={item} />}
                            <Popconfirm title={t("skills.removeConfirm")} onConfirm={() => void removeInstalledSkill(item.id)}>
                              <Button
                                danger
                                icon={<DeleteOutlined />}
                                loading={removingSkill === item.id}
                                disabled={!sessionId}
                              >
                                {t("skills.remove")}
                              </Button>
                            </Popconfirm>
                          </Space>
                        );
                      },
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "active",
            label: <Badge count={active.length} offset={[10, 0]}>Active</Badge>,
            children: active.length === 0 ? (
              <Card><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No active skills yet." /></Card>
            ) : (
              <Table<SkillActivation>
                rowKey="id"
                dataSource={active}
                pagination={false}
                columns={[
                  { title: "Skill", render: (_value, item) => <ActiveSkillSummary item={item} /> },
                  { title: "Loaded sections", render: (_value, item) => <Space wrap>{(item.loaded_sections ?? []).map((section) => <Tag key={section}>{section}</Tag>)}</Space> },
                  { title: "Status", dataIndex: "status", render: (value) => <Tag>{value}</Tag> },
                  {
                    title: "Action",
                    render: (_value, item) => (
                      <Space wrap>
                        <Button onClick={() => void runAction(item.id, () => unloadSessionSkill(token || null, sessionId, item.id))}>Unload</Button>
                        <Popconfirm title={t("skills.removeConfirm")} onConfirm={() => void removeInstalledSkill(item.id)}>
                          <Button danger icon={<DeleteOutlined />} loading={removingSkill === item.id}>{t("skills.remove")}</Button>
                        </Popconfirm>
                      </Space>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: "packages",
            label: <Badge count={packages.length} offset={[10, 0]}>Packages</Badge>,
            children: (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <Card title="Install package">
                  <Form form={packageForm} layout="inline" onFinish={installPackageSource}>
                    <Form.Item name="source" rules={[{ required: true }]} style={{ flex: 1 }}>
                      <Input placeholder="/path/to/package, https://github.com/org/repo, or org/repo" />
                    </Form.Item>
                    <Button type="primary" icon={<DownloadOutlined />} htmlType="submit" loading={installingPackage}>Install package</Button>
                  </Form>
                </Card>
                <PackageTable
                  items={packages}
                  loading={packagesQuery.isLoading}
                  removingPackage={removingPackage}
                  reinstallingPackage={reinstallingPackage}
                  runningSmoke={runningSmoke}
                  onRemove={removeInstalledPackage}
                  onReinstall={reinstallInstalledPackage}
                  onRunSmoke={runInstalledPackageSmoke}
                />
              </Space>
            ),
          },
          {
            key: "prompts",
            label: <Badge count={prompts.length} offset={[10, 0]}>Prompts</Badge>,
            children: <PromptTable items={prompts} loading={promptsQuery.isLoading} />,
          },
          {
            key: "commands",
            label: <Badge count={packageCommands.length} offset={[10, 0]}>Commands</Badge>,
            children: <PackageCommandTable items={packageCommands} loading={packageCommandsQuery.isLoading} />,
          },
          {
            key: "roles",
            label: <Badge count={packageRoles.length} offset={[10, 0]}>Roles</Badge>,
            children: <PackageRoleTable items={packageRoles} loading={packageRolesQuery.isLoading} />,
          },
        ]}
      />

      <Drawer title={selectedSkillId || "Skill"} width={720} open={!!selectedSkillId} onClose={() => setSelectedSkillId(null)} destroyOnHidden>
        {detailQuery.data ? (
          <SkillDetail
            item={detailQuery.data}
            active={activeMap.get(detailQuery.data.id)}
            busy={busySkill === detailQuery.data.id}
            onLoad={() => void runAction(detailQuery.data!.id, () => loadSessionSkill(token || null, sessionId, detailQuery.data!.id))}
            onUnload={() => void runAction(detailQuery.data!.id, () => unloadSessionSkill(token || null, sessionId, detailQuery.data!.id))}
            onExpand={(sections) => void runAction(detailQuery.data!.id, () => expandSessionSkill(token || null, sessionId, detailQuery.data!.id, sections))}
            onSelectSkill={setSelectedSkillId}
          />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
        )}
      </Drawer>
    </div>
  );
}

type Translate = (key: string, vars?: Record<string, string | number>) => string;

type ToolStatRow = {
  name: string;
  total: number;
  success: number;
  failure: number;
  successRate: number;
};

type FailureReason = {
  reason: string;
  count: number;
};

type ToolAnalytics = {
  inspectedSessions: number;
  totalRuns: number;
  successRuns: number;
  failureRuns: number;
  successRate: number;
  byTool: ToolStatRow[];
  failureReasons: FailureReason[];
};

const emptyToolAnalytics: ToolAnalytics = {
  inspectedSessions: 0,
  totalRuns: 0,
  successRuns: 0,
  failureRuns: 0,
  successRate: 0,
  byTool: [],
  failureReasons: [],
};

function SkillAnalyticsPanel({
  analytics,
  activeSkillCount,
  installedSkillCount,
  loading,
  t,
}: {
  analytics: ToolAnalytics;
  activeSkillCount: number;
  installedSkillCount: number;
  loading: boolean;
  t: Translate;
}) {
  return (
    <Card
      className="skill-analytics-card"
      loading={loading}
      title={
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{t("skills.analyticsTitle")}</Typography.Text>
          <Typography.Text type="secondary">{t("skills.analyticsSubtitle", { count: analytics.inspectedSessions })}</Typography.Text>
        </Space>
      }
    >
      <div className="skill-analytics-layout">
        <div className="skill-analytics-metrics">
          <AnalyticsMetric title={t("skills.installedSkills")} value={installedSkillCount} />
          <AnalyticsMetric title={t("skills.activeSkills")} value={activeSkillCount} />
          <AnalyticsMetric title={t("skills.toolRuns")} value={analytics.totalRuns} />
          <div className="skill-analytics-rate">
            <Typography.Text type="secondary">{t("skills.successRate")}</Typography.Text>
            <Progress
              percent={analytics.successRate}
              size="small"
              status={analytics.failureRuns > 0 ? "normal" : "success"}
              format={(percent) => `${Math.round(percent ?? 0)}%`}
            />
            <Typography.Text type="secondary">
              {analytics.successRuns} / {analytics.totalRuns}
            </Typography.Text>
          </div>
        </div>
        <div className="skill-failure-panel">
          <Typography.Text strong>{t("skills.failureReasons")}</Typography.Text>
          {analytics.failureReasons.length === 0 ? (
            <Typography.Text type="secondary">{analytics.totalRuns ? t("skills.noFailureReasons") : t("skills.noToolRuns")}</Typography.Text>
          ) : (
            <Space direction="vertical" size={8} style={{ width: "100%" }}>
              {analytics.failureReasons.map((item) => (
                <div className="skill-failure-row" key={item.reason}>
                  <Typography.Text ellipsis title={item.reason}>{item.reason}</Typography.Text>
                  <Tag color="red">{item.count}</Tag>
                </div>
              ))}
            </Space>
          )}
        </div>
      </div>
      <Table<ToolStatRow>
        className="skill-tool-table"
        size="small"
        rowKey="name"
        dataSource={analytics.byTool}
        locale={{ emptyText: t("skills.noToolRuns") }}
        pagination={analytics.byTool.length > 6 ? { pageSize: 6, size: "small" } : false}
        columns={[
          { title: t("skills.toolName"), dataIndex: "name", render: (value) => <Typography.Text strong>{value}</Typography.Text> },
          { title: t("skills.runs"), dataIndex: "total", width: 96 },
          { title: t("skills.failures"), dataIndex: "failure", width: 96, render: (value) => value ? <Tag color="red">{value}</Tag> : <Tag color="green">0</Tag> },
          {
            title: t("skills.successRate"),
            dataIndex: "successRate",
            width: 180,
            render: (value: number) => <Progress percent={value} size="small" format={(percent) => `${Math.round(percent ?? 0)}%`} />,
          },
        ]}
      />
    </Card>
  );
}

function AnalyticsMetric({ title, value }: { title: string; value: number }) {
  return (
    <div className="skill-analytics-metric">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </div>
  );
}

function qualityToAnalytics(report?: PackageQualityReport): ToolAnalytics {
  if (!report?.tool_health) {
    return emptyToolAnalytics;
  }
  const toolHealth = report.tool_health;
  return {
    inspectedSessions: toolHealth.inspected_sessions,
    totalRuns: toolHealth.total_runs,
    successRuns: toolHealth.success_runs,
    failureRuns: toolHealth.failure_runs,
    successRate: toolHealth.success_rate,
    byTool: (toolHealth.by_tool ?? []).map((row: ToolStat) => ({
      name: row.name,
      total: row.total,
      success: row.success,
      failure: row.failure,
      successRate: row.success_rate,
    })),
    failureReasons: report.failure_reasons ?? [],
  };
}

function PackageQualityPanel({ report, loading }: { report?: PackageQualityReport; loading: boolean }) {
  const items = report?.packages ?? [];
  return (
    <Card
      title="Quality & Security"
      loading={loading}
      style={{ marginBottom: 16 }}
    >
      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <Metric title="Package risk" value={report?.high_risk_packages ?? 0} />
        <Metric title="Skills" value={report?.skill_count ?? 0} />
        <Metric title="Prompts" value={report?.prompt_count ?? 0} />
        <Metric title="Commands" value={report?.command_count ?? 0} />
      </div>
      <Table<PackageQuality>
        rowKey="name"
        size="small"
        dataSource={items}
        pagination={items.length > 6 ? { pageSize: 6, size: "small" } : false}
        columns={[
          {
            title: "Package",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                <Space wrap>
                  <Typography.Text strong>{item.name}</Typography.Text>
                  {item.version ? <Tag>{item.version}</Tag> : null}
                  <Tag color={riskColor(item.risk_level)}>{item.risk_level}</Tag>
                  <Tag>{item.score}</Tag>
                </Space>
                <Typography.Text type="secondary" copyable>{item.source}</Typography.Text>
              </Space>
            ),
          },
          {
            title: "Resources",
            render: (_value, item) => <ResourceCountTags counts={item.resource_counts} />,
          },
          {
            title: "App",
            render: (_value, item) => <PackageAppTag app={item.app} issues={item.app_issues} />,
          },
          {
            title: "Contract",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
                <Space wrap>
                  {item.command_diagnostics?.length ? <Tag>commands: {item.command_diagnostics.length}</Tag> : null}
                  {item.role_diagnostics?.length ? <Tag>roles: {item.role_diagnostics.length}</Tag> : null}
                </Space>
              </Space>
            ),
          },
          {
            title: "Issues",
            render: (_value, item) => {
              const issues = [
                ...(item.manifest_issues ?? []),
                ...(item.resource_issues ?? []),
                ...(item.permission_issues ?? []),
                ...(item.capability_issues ?? []),
                ...(item.tool_policy_issues ?? []),
                ...(item.app_issues ?? []),
                ...(item.command_diagnostics ?? []).flatMap((diag) => diag.issues ?? []),
                ...(item.role_diagnostics ?? []).flatMap((diag) => diag.issues ?? []),
                ...(item.smoke_checks ?? []).flatMap((check) => check.issues ?? []),
                ...(item.unknown_bundles ?? []).map((bundle) => `unknown bundle: ${bundle}`),
              ];
              return issues.length ? (
                <Space direction="vertical" size={2}>
                  {issues.slice(0, 3).map((issue) => <Typography.Text key={issue} type="danger">{issue}</Typography.Text>)}
                  {issues.length > 3 ? <Typography.Text type="secondary">+{issues.length - 3} more</Typography.Text> : null}
                </Space>
              ) : <Tag color="green">healthy</Tag>;
            },
          },
          {
            title: "Permissions",
            render: (_value, item) => item.permissions?.length ? (
              <Space wrap>{item.permissions.map((permission) => <Tag color="gold" key={permission}>{permission}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Health",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.install_health ? <Tag>{item.install_health}</Tag> : null}
                {item.reinstall_available_hint ? <Typography.Text type="secondary">{item.reinstall_available_hint}</Typography.Text> : null}
                {item.smoke_checks?.length ? (
                  <Space wrap>
                    {item.smoke_checks.map((check) => <Tag key={check.name} color={smokeColor(check.status)}>{check.name}: {check.status}</Tag>)}
                  </Space>
                ) : null}
              </Space>
            ),
          },
        ]}
      />
    </Card>
  );
}

function ResourceCountTags({ counts }: { counts: Record<string, number> }) {
  const entries = Object.entries(counts).filter(([, value]) => value > 0);
  return entries.length ? (
    <Space wrap>{entries.map(([key, value]) => <Tag key={key}>{key}: {value}</Tag>)}</Space>
  ) : <>-</>;
}

function riskColor(level: string) {
  if (level === "high") {
    return "red";
  }
  if (level === "medium") {
    return "gold";
  }
  return "green";
}

function smokeColor(status: string) {
  if (status === "passed" || status === "ready") {
    return "green";
  }
  if (status === "pending_approval" || status === "running") {
    return "gold";
  }
  return "red";
}

function SkillSummary({ item, active }: { item: SkillCatalogEntry; active?: SkillActivation }) {
  const childIDs = item.child_skill_ids ?? [];
  const visibleChildren = childIDs.slice(0, 8);
  const hiddenChildren = Math.max(0, (item.child_skill_count ?? childIDs.length) - visibleChildren.length);
  return (
    <Space direction="vertical" size={4}>
      <Space wrap>
        <Typography.Text strong>{item.name}</Typography.Text>
        {item.id !== item.name ? <Tag>{item.id}</Tag> : null}
        <SkillKindTag item={item} />
        {active ? <Tag color="green">active</Tag> : null}
        {item.version ? <Tag>{item.version}</Tag> : null}
        <NormalizationTag item={item} />
      </Space>
      <Typography.Text type="secondary">{item.description || "No description provided."}</Typography.Text>
      {item.skill_kind === "suite_root" ? (
        <Space direction="vertical" size={2}>
          <Typography.Text type="secondary">
            {item.child_skill_count ?? childIDs.length} child skills. Search by child id or open details to inspect the suite.
          </Typography.Text>
          <Space wrap>
            {visibleChildren.map((childID) => <Tag key={childID}>{childID}</Tag>)}
            {hiddenChildren > 0 ? <Tag>+{hiddenChildren} more</Tag> : null}
          </Space>
        </Space>
      ) : null}
      <Space wrap>{(item.categories ?? []).map((category) => <Tag key={category}>{category}</Tag>)}</Space>
    </Space>
  );
}

function ActiveSkillSummary({ item }: { item: SkillActivation }) {
  const childIDs = item.child_skill_ids ?? [];
  return (
    <Space direction="vertical" size={4}>
      <Space wrap>
        <Typography.Text strong>{item.name}</Typography.Text>
        {item.id !== item.name ? <Tag>{item.id}</Tag> : null}
        <SkillKindTag item={item} />
      </Space>
      <Typography.Text type="secondary">{item.description}</Typography.Text>
      {item.skill_kind === "suite_root" ? (
        <Typography.Text type="secondary">
          Loaded root suite. {item.child_skill_count ?? childIDs.length} child skills remain individually loadable by exact id.
        </Typography.Text>
      ) : null}
      {item.skill_kind === "child_skill" && item.suite_id ? (
        <Typography.Text type="secondary">Loaded child skill from {item.suite_id}.</Typography.Text>
      ) : null}
    </Space>
  );
}

function SkillSourceSummary({ item }: { item: SkillSourceEntry }) {
  return (
    <Space direction="vertical" size={4}>
      <Space wrap>
        <Typography.Text strong>{item.name}</Typography.Text>
        {item.origin ? <Tag>{item.origin}</Tag> : null}
        {item.installed ? <Tag color="green">installed</Tag> : null}
      </Space>
      <Typography.Text type="secondary">{item.summary}</Typography.Text>
      <Typography.Text type="secondary" copyable>{item.source}</Typography.Text>
      {item.install_reason ? <Alert type="warning" showIcon message={item.install_reason} /> : null}
    </Space>
  );
}

function SkillDetail({
  item,
  active,
  busy,
  onLoad,
  onUnload,
  onExpand,
  onSelectSkill,
}: {
  item: SkillCatalogEntry;
  active?: SkillActivation;
  busy: boolean;
  onLoad: () => void;
  onUnload: () => void;
  onExpand: (sections: string[]) => void;
  onSelectSkill: (id: string) => void;
}) {
  const loaded = new Set(active?.loaded_sections ?? []);
  const remaining = (item.sections ?? []).filter((section) => !loaded.has(section));
  const [sections, setSections] = useState<string[]>(remaining);
  const childIDs = item.child_skill_ids ?? [];
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Descriptions bordered column={1} size="small" items={[
        { key: "id", label: "ID", children: item.id },
        { key: "kind", label: "Kind", children: <SkillKindTag item={item} /> },
        ...(item.suite_id ? [{ key: "suite", label: "Suite", children: item.suite_id }] : []),
        ...(item.skill_kind === "suite_root" ? [{ key: "children", label: "Children", children: `${item.child_skill_count ?? childIDs.length} child skills` }] : []),
        { key: "compat", label: "Compatibility", children: item.compatibility?.status || "unknown" },
        { key: "normalization", label: "Normalization", children: <Space direction="vertical" size={2}><NormalizationTag item={item} /><Typography.Text type="secondary">{normalizationHelp(item)}</Typography.Text></Space> },
        { key: "path", label: "Path", children: item.path || "-" },
      ]} />
      <Typography.Paragraph>{item.description}</Typography.Paragraph>
      {item.skill_kind === "suite_root" ? (
        <Card size="small" title="Child skills">
          <Space direction="vertical" size={8} style={{ width: "100%" }}>
            <Typography.Text type="secondary">
              Child skills are not loaded automatically. Load the root for its overview, or load an exact child id for a specialist workflow.
            </Typography.Text>
            {item.child_skill_hint ? <Typography.Text type="secondary">{item.child_skill_hint}</Typography.Text> : null}
            <Space wrap>
              {childIDs.map((childID) => (
                <Button key={childID} size="small" onClick={() => onSelectSkill(childID)}>
                  {childID}
                </Button>
              ))}
            </Space>
          </Space>
        </Card>
      ) : null}
      {item.when_to_use?.length ? <MarkdownContent content={item.when_to_use.map((entry) => `- ${entry}`).join("\n")} /> : null}
      <Space wrap>{(item.recommended_bundles ?? []).map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}</Space>
      <Space wrap>
        {active ? <Button loading={busy} onClick={onUnload}>Unload</Button> : <Button type="primary" loading={busy} onClick={onLoad}>Load</Button>}
        {remaining.length ? (
          <>
            <Select mode="multiple" value={sections} onChange={setSections} style={{ minWidth: 260 }} options={remaining.map((value) => ({ value, label: value }))} />
            <Button disabled={sections.length === 0} loading={busy} onClick={() => onExpand(sections)}>Expand sections</Button>
          </>
        ) : null}
      </Space>
    </Space>
  );
}

function PackageTable({
  items,
  loading,
  removingPackage,
  reinstallingPackage,
  runningSmoke,
  onRemove,
  onReinstall,
  onRunSmoke,
}: {
  items: PackageEntry[];
  loading: boolean;
  removingPackage: string | null;
  reinstallingPackage: string | null;
  runningSmoke: string | null;
  onRemove: (item: PackageEntry) => void;
  onReinstall: (item: PackageEntry) => void;
  onRunSmoke: (item: PackageEntry, smokeName: string) => void;
}) {
  return (
    <Card title="Installed packages">
      <Table<PackageEntry>
        rowKey="name"
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 8 }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No packages installed yet.">
              <Typography.Text type="secondary">Use the install form above to add your first skill package.</Typography.Text>
            </Empty>
          ),
        }}
        columns={[
          {
            title: "Package",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                <Space wrap>
                  <Typography.Text strong>{item.name}</Typography.Text>
                  {item.version ? <Tag>{item.version}</Tag> : null}
                  <Tag color={item.trust === "local" ? "green" : "gold"}>{item.trust}</Tag>
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
                <Typography.Text type="secondary" copyable>{item.source}</Typography.Text>
              </Space>
            ),
          },
          {
            title: "Resources",
            render: (_value, item) => <ResourceTags resources={item.resources ?? {}} />,
          },
          {
            title: "App",
            render: (_value, item) => <PackageAppTag app={item.app} />,
          },
          {
            title: "Permissions",
            render: (_value, item) => item.permissions?.length ? (
              <Space wrap>{item.permissions.map((permission) => <Tag color="gold" key={permission}>{permission}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Quality",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
                {item.smoke_tests?.length ? (
                  <Space wrap>
                    {item.smoke_tests.map((smoke) => {
                      const key = `${item.name}:${smoke.name}`;
                      return (
                        <Button
                          key={key}
                          size="small"
                          loading={runningSmoke === key}
                          onClick={() => onRunSmoke(item, smoke.name)}
                        >
                          Smoke {smoke.name}
                        </Button>
                      );
                    })}
                  </Space>
                ) : null}
              </Space>
            ),
          },
          {
            title: "Installed",
            dataIndex: "installed_at",
            render: (value) => formatTime(value),
          },
          {
            title: "Action",
            width: 220,
            render: (_value, item) => (
              <Space wrap>
                <Button
                  icon={<ReloadOutlined />}
                  loading={reinstallingPackage === item.name}
                  onClick={() => onReinstall(item)}
                >
                  Reinstall
                </Button>
                <Popconfirm title={`Remove ${item.name}?`} onConfirm={() => onRemove(item)}>
                  <Button
                    danger
                    icon={<DeleteOutlined />}
                    loading={removingPackage === item.name}
                  >
                    Remove
                  </Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
    </Card>
  );
}

function PromptTable({ items, loading }: { items: PromptEntry[]; loading: boolean }) {
  return (
    <Card title="Prompt templates">
      <Table<PromptEntry>
        rowKey={(item) => `${item.package_name}:${item.name}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          { title: "Prompt", dataIndex: "name", render: (value) => <Typography.Text strong>{value}</Typography.Text> },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

function PackageCommandTable({ items, loading }: { items: PackageCommandEntry[]; loading: boolean }) {
  return (
    <Card title="Package commands">
      <Table<PackageCommandEntry>
        rowKey={(item) => `${item.package_name}:${item.namespace}:${item.name}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          {
            title: "Command",
            render: (_value, item) => (
              <Space direction="vertical" size={2}>
                <Space wrap>
                  <Typography.Text strong>/{item.namespace || item.package_name} {item.name}</Typography.Text>
                  {item.mode ? <Tag>{item.mode}</Tag> : null}
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
              </Space>
            ),
          },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          {
            title: "Roles",
            render: (_value, item) => item.roles?.length ? <Space wrap>{item.roles.map((role) => <Tag key={role}>{role}</Tag>)}</Space> : "-",
          },
          {
            title: "Bundles",
            render: (_value, item) => item.recommended_bundles?.length ? (
              <Space wrap>{item.recommended_bundles.map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Policy",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
              </Space>
            ),
          },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

function PackageRoleTable({ items, loading }: { items: PackageRoleEntry[]; loading: boolean }) {
  return (
    <Card title="Named subagent roles">
      <Table<PackageRoleEntry>
        rowKey={(item) => `${item.package_name}:${item.id}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          {
            title: "Role",
            render: (_value, item) => (
              <Space direction="vertical" size={2}>
                <Space wrap>
                  <Typography.Text strong>{item.name || item.id}</Typography.Text>
                  <Tag>{item.id}</Tag>
                  {item.write_enabled ? <Tag color="gold">write</Tag> : <Tag>read-only</Tag>}
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
              </Space>
            ),
          },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          {
            title: "Tools",
            render: (_value, item) => item.tools?.length ? <Space wrap>{item.tools.map((tool) => <Tag key={tool}>{tool}</Tag>)}</Space> : "-",
          },
          {
            title: "Bundles",
            render: (_value, item) => item.default_bundles?.length ? (
              <Space wrap>{item.default_bundles.map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Policy",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
              </Space>
            ),
          },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

function ResourceTags({ resources }: { resources: Record<string, string[]> }) {
  const entries = Object.entries(resources).filter(([, values]) => values.length > 0);
  if (!entries.length) {
    return <>-</>;
  }
  return (
    <Space wrap>
      {entries.map(([name, values]) => (
        <Tag key={name}>{name}: {values.length}</Tag>
      ))}
    </Space>
  );
}

function PackageAppTag({
  app,
  issues = [],
}: {
  app?: { kind?: string; id?: string; label?: string; config?: Record<string, unknown> };
  issues?: string[];
}) {
  if (!app?.id && !app?.kind && !app?.label) {
    return <>-</>;
  }
  const color = issues.length ? "orange" : "purple";
  return (
    <Space direction="vertical" size={4}>
      <Space wrap>
        <Tag color={color}>{app.kind || "builtin"}</Tag>
        {app.id ? <Tag color={color}>{app.id}</Tag> : null}
        {app.label ? <Tag>{app.label}</Tag> : null}
      </Space>
      {Object.keys(app.config ?? {}).length ? (
        <Typography.Text type="secondary">{Object.keys(app.config ?? {}).length} config keys</Typography.Text>
      ) : null}
    </Space>
  );
}

function Metric({ title, value }: { title: string; value: number }) {
  return (
    <Card size="small">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </Card>
  );
}

function formatTime(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function canRunNormalize(item: SkillCatalogEntry) {
  return Boolean(item.can_normalize && item.needs_normalization && !item.normalized);
}

function NormalizationTag({ item }: { item: SkillCatalogEntry }) {
  const status = item.normalization_status || (item.normalized ? "normalized" : item.needs_normalization ? "suggested" : "not_needed");
  switch (status) {
    case "normalized":
      return <Tooltip title="Structured metadata was produced by an explicit normalization step."><Tag color="green">normalized{item.normalization_source ? ` · ${item.normalization_source}` : ""}</Tag></Tooltip>;
    case "suggested":
      return <Tooltip title="This skill can be used as-is, but manual LLM normalization may improve catalog metadata."><Tag color="gold">normalize needed</Tag></Tooltip>;
    case "unavailable":
      return <Tooltip title="No fallback normalizer is configured for this session."><Tag color="orange">normalize unavailable</Tag></Tooltip>;
    case "not_needed":
      return <Tooltip title="The skill already has enough structured metadata; no LLM normalization is needed."><Tag color="blue">structured</Tag></Tooltip>;
    default:
      return <Tag>{status}</Tag>;
  }
}

function normalizationHelp(item: SkillCatalogEntry) {
  const status = item.normalization_status || (item.normalized ? "normalized" : item.needs_normalization ? "suggested" : "not_needed");
  if (status === "normalized") {
    return "Explicit normalization has already written structured metadata for this skill.";
  }
  if (status === "suggested") {
    return "Normalization is manual because it may spend model tokens. Install, catalog, and chat do not trigger it automatically.";
  }
  if (status === "unavailable") {
    return "The skill may benefit from normalization, but this runtime has no fallback normalizer configured.";
  }
  if (status === "not_needed") {
    return "The source skill is already structured enough for low-cost discovery.";
  }
  return status;
}

function SkillKindTag({ item }: { item: Pick<SkillCatalogEntry, "skill_kind" | "suite_id" | "child_skill_count"> }) {
  if (item.skill_kind === "suite_root") {
    return <Tag color="purple">suite root{item.child_skill_count ? ` · ${item.child_skill_count}` : ""}</Tag>;
  }
  if (item.skill_kind === "child_skill") {
    return <Tag color="geekblue">child{item.suite_id ? ` · ${item.suite_id}` : ""}</Tag>;
  }
  return <Tag>root</Tag>;
}

function skillNeedsAttention(item: SkillCatalogEntry) {
  return item.compatibility?.status === "unsupported" || Boolean(item.warnings?.length);
}

function skillReady(item: SkillCatalogEntry) {
  return item.compatibility?.status === "native_supported" || item.compatibility?.status === "degraded_supported";
}

function trustRank(trust?: string) {
  switch ((trust || "").toLowerCase()) {
    case "official":
      return 4;
    case "verified":
      return 3;
    case "community":
      return 2;
    default:
      return 1;
  }
}

function installedSkillName(item: SkillSourceEntry) {
  const pathName = item.installed_path?.split(/[\\/]/).filter(Boolean).pop();
  return pathName || item.install_name || item.skill_name || item.name;
}

function compatibilityColor(status?: string) {
  if (status === "native_supported") {
    return "green";
  }
  if (status === "degraded_supported") {
    return "gold";
  }
  if (status === "unsupported") {
    return "red";
  }
  return "default";
}
