import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import { CopyOutlined, DeleteOutlined, EditOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  createCronJob,
  deleteCronJob,
  getCronRunLogs,
  getHeartbeatLogs,
  getHeartbeatRule,
  getMeta,
  listCronJobs,
  runCronJob,
  testHeartbeat,
  updateCronJob,
  updateHeartbeatRule,
} from "../../lib/api";
import type { CronJob, CronRunLog, DeliveryTarget, HeartbeatRule, HeartbeatRunLog } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";

type CronFormValues = {
  id?: string;
  name?: string;
  message: string;
  scheduleType: "at" | "every" | "cron";
  at?: string;
  everySeconds?: number;
  cronExpr?: string;
  timezone?: string;
  sessionMode?: "shared" | "isolated";
  enabled?: boolean;
  deliveryKind?: "" | "session" | "channel";
  sessionId?: string;
  channel?: string;
  sessionKey?: string;
  recipient?: string;
};

type HeartbeatFormValues = {
  enabled: boolean;
  intervalSeconds: number;
  timezone?: string;
  activeHoursStart?: string;
  activeHoursEnd?: string;
  sessionMode?: "shared" | "isolated";
  promptOverride?: string;
  watchdogScript?: string;
  deliveryKind?: "" | "session" | "channel";
  sessionId?: string;
  channel?: string;
  sessionKey?: string;
  recipient?: string;
};

export function AutomationPage() {
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const [cronDrawerOpen, setCronDrawerOpen] = useState(false);
  const [editingCron, setEditingCron] = useState<CronJob | null>(null);
  const [selectedJobId, setSelectedJobId] = useState<string | null>(null);
  const [cronForm] = Form.useForm<CronFormValues>();
  const [heartbeatForm] = Form.useForm<HeartbeatFormValues>();

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachAutomation = !authRequired || !!token;

  const jobsQuery = useQuery({
    queryKey: ["automation-cron-jobs", token],
    enabled: canReachAutomation,
    queryFn: async () => listCronJobs(token || null),
  });
  const heartbeatQuery = useQuery({
    queryKey: ["automation-heartbeat", token],
    enabled: canReachAutomation,
    queryFn: async () => getHeartbeatRule(token || null),
  });
  const jobLogsQuery = useQuery({
    queryKey: ["automation-cron-logs", token, selectedJobId],
    enabled: canReachAutomation && !!selectedJobId,
    queryFn: async () => getCronRunLogs(token || null, selectedJobId!),
  });
  const heartbeatLogsQuery = useQuery({
    queryKey: ["automation-heartbeat-logs", token],
    enabled: canReachAutomation,
    queryFn: async () => getHeartbeatLogs(token || null),
  });

  const jobs = jobsQuery.data ?? [];
  const selectedJob = useMemo(() => jobs.find((job) => job.id === selectedJobId) ?? jobs[0] ?? null, [jobs, selectedJobId]);

  useEffect(() => {
    if (!selectedJobId && jobs[0]) {
      setSelectedJobId(jobs[0].id);
    }
  }, [jobs, selectedJobId]);

  useEffect(() => {
    if (heartbeatQuery.data) {
      heartbeatForm.setFieldsValue(heartbeatToForm(heartbeatQuery.data));
    }
  }, [heartbeatForm, heartbeatQuery.data]);

  const saveCronMutation = useMutation({
    mutationFn: async (values: CronFormValues) => {
      const payload = buildCronPayload(values);
      if (values.id) {
        return updateCronJob(token || null, values.id, payload);
      }
      return createCronJob(token || null, payload);
    },
    onSuccess: async (job) => {
      setCronDrawerOpen(false);
      setEditingCron(null);
      setSelectedJobId(job.id);
      void message.success(`Saved cron job ${job.id}.`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["automation-cron-jobs", token] }),
        queryClient.invalidateQueries({ queryKey: ["automation-cron-logs", token, job.id] }),
      ]);
    },
    onError: (error) => showError(message, error, "Failed to save cron job."),
  });

  const deleteCronMutation = useMutation({
    mutationFn: async (jobId: string) => deleteCronJob(token || null, jobId),
    onSuccess: async (_unused, jobId) => {
      void message.success(`Deleted cron job ${jobId}.`);
      setSelectedJobId(null);
      await queryClient.invalidateQueries({ queryKey: ["automation-cron-jobs", token] });
    },
    onError: (error) => showError(message, error, "Failed to delete cron job."),
  });

  const runCronMutation = useMutation({
    mutationFn: async (jobId: string) => runCronJob(token || null, jobId),
    onSuccess: async (run) => {
      void message.success(`Cron run finished with status ${run.status}.`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["automation-cron-jobs", token] }),
        queryClient.invalidateQueries({ queryKey: ["automation-cron-logs", token, run.job_id] }),
      ]);
    },
    onError: (error) => showError(message, error, "Failed to run cron job."),
  });

  const saveHeartbeatMutation = useMutation({
    mutationFn: async (values: HeartbeatFormValues) => updateHeartbeatRule(token || null, buildHeartbeatPayload(values)),
    onSuccess: async () => {
      void message.success("Saved heartbeat rule.");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["automation-heartbeat", token] }),
        queryClient.invalidateQueries({ queryKey: ["automation-heartbeat-logs", token] }),
      ]);
    },
    onError: (error) => showError(message, error, "Failed to save heartbeat rule."),
  });

  const testHeartbeatMutation = useMutation({
    mutationFn: async () => testHeartbeat(token || null),
    onSuccess: async (run) => {
      void message.success(`Heartbeat test finished with status ${run.status}.`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["automation-heartbeat", token] }),
        queryClient.invalidateQueries({ queryKey: ["automation-heartbeat-logs", token] }),
      ]);
    },
    onError: (error) => showError(message, error, "Failed to test heartbeat."),
  });

  const openCronDrawer = (job?: CronJob | null, duplicate = false) => {
    const values = job ? cronToForm(job) : defaultCronForm();
    if (duplicate) {
      values.id = undefined;
      values.name = values.name ? `${values.name} copy` : "";
    }
    setEditingCron(duplicate ? null : job ?? null);
    cronForm.setFieldsValue(values);
    setCronDrawerOpen(true);
  };

  if (authRequired && !token) {
    return (
      <div className="page-pad">
        <Alert type="warning" showIcon message="This server requires `GODEX_WEB_TOKEN`. Open Settings and save the bearer token first." />
      </div>
    );
  }

  return (
    <div className="page-pad">
      <div className="page-action-row">
        <Button
          icon={<ReloadOutlined />}
          aria-label="Refresh cron jobs"
          onClick={() => void queryClient.invalidateQueries({ queryKey: ["automation-cron-jobs", token] })}
        >
          Refresh
        </Button>
      </div>

      <Tabs
        items={[
          {
            key: "cron",
            label: "Cron jobs",
            children: (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <div className="stat-grid">
                  <Metric title="Total" value={jobs.length} />
                  <Metric title="Enabled" value={jobs.filter((job) => job.enabled).length} />
                  <Metric title="Issues" value={jobs.filter((job) => isIssueStatus(job.last_status)).length} tone="danger" />
                </div>
                <Card
                  title="Jobs"
                  extra={
                    <Button type="primary" icon={<PlusOutlined />} aria-label="Create cron job" onClick={() => openCronDrawer()}>
                      New job
                    </Button>
                  }
                >
                  <Table<CronJob>
                    rowKey="id"
                    size="middle"
                    loading={jobsQuery.isLoading}
                    dataSource={jobs}
                    pagination={{ pageSize: 8 }}
                    locale={{
                      emptyText: (
                        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No cron jobs yet.">
                          <Button type="primary" icon={<PlusOutlined />} aria-label="Create first cron job" onClick={() => openCronDrawer()}>
                            Create first cron job
                          </Button>
                        </Empty>
                      ),
                    }}
                    onRow={(job) => ({ onClick: () => setSelectedJobId(job.id) })}
                    columns={[
                      {
                        title: "Name",
                        dataIndex: "name",
                        render: (_value, job) => (
                          <Space direction="vertical" size={0}>
                            <Typography.Text strong>{job.name || job.id}</Typography.Text>
                            <Typography.Text type="secondary">{job.id}</Typography.Text>
                          </Space>
                        ),
                      },
                      { title: "Schedule", render: (_value, job) => renderCronSchedule(job) },
                      { title: "Next run", dataIndex: "next_run_at", render: formatTime },
                      { title: "Status", dataIndex: "last_status", render: (value) => <Tag color={isIssueStatus(value) ? "red" : "green"}>{value || "pending"}</Tag> },
                      {
                        title: "Actions",
                        render: (_value, job) => (
                          <Space wrap onClick={(event) => event.stopPropagation()}>
                            <Button size="small" aria-label="Edit cron job" icon={<EditOutlined />} onClick={() => openCronDrawer(job)} />
                            <Button size="small" aria-label="Duplicate cron job" icon={<CopyOutlined />} onClick={() => openCronDrawer(job, true)} />
                            <Button
                              size="small"
                              aria-label="Run cron job now"
                              icon={<PlayCircleOutlined />}
                              loading={runCronMutation.variables === job.id && runCronMutation.isPending}
                              onClick={() => runCronMutation.mutate(job.id)}
                            />
                            <Popconfirm title="Delete this cron job?" onConfirm={() => deleteCronMutation.mutate(job.id)}>
                              <Button size="small" danger aria-label="Delete cron job" icon={<DeleteOutlined />} />
                            </Popconfirm>
                          </Space>
                        ),
                      },
                    ]}
                  />
                </Card>
                <RunLogsCard title={selectedJob ? `Runs for ${selectedJob.name || selectedJob.id}` : "Run logs"} runs={jobLogsQuery.data ?? []} loading={jobLogsQuery.isLoading} />
              </Space>
            ),
          },
          {
            key: "heartbeat",
            label: "Heartbeat",
            children: (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <Card
                  title="Heartbeat rule"
                  extra={
                    <Button
                      icon={<PlayCircleOutlined />}
                      loading={testHeartbeatMutation.isPending}
                      onClick={() => testHeartbeatMutation.mutate()}
                    >
                      Test now
                    </Button>
                  }
                >
                  <HeartbeatForm form={heartbeatForm} onFinish={(values) => saveHeartbeatMutation.mutate(values)} saving={saveHeartbeatMutation.isPending} />
                </Card>
                <RunLogsCard title="Heartbeat runs" runs={heartbeatLogsQuery.data ?? []} loading={heartbeatLogsQuery.isLoading} heartbeat />
              </Space>
            ),
          },
        ]}
      />

      <Drawer
        title={editingCron ? `Edit ${editingCron.id}` : "Create cron job"}
        width={640}
        open={cronDrawerOpen}
        onClose={() => setCronDrawerOpen(false)}
        destroyOnHidden
      >
        <CronForm form={cronForm} onFinish={(values) => saveCronMutation.mutate(values)} saving={saveCronMutation.isPending} />
      </Drawer>
    </div>
  );
}

function CronForm({
  form,
  onFinish,
  saving,
}: {
  form: ReturnType<typeof Form.useForm<CronFormValues>>[0];
  onFinish: (values: CronFormValues) => void;
  saving: boolean;
}) {
  const scheduleType = Form.useWatch("scheduleType", form) ?? "every";
  const deliveryKind = Form.useWatch("deliveryKind", form) ?? "";
  return (
    <Form form={form} layout="vertical" onFinish={onFinish} initialValues={defaultCronForm()}>
      <Form.Item name="id" hidden><Input /></Form.Item>
      <Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item>
      <Form.Item name="name" label="Name"><Input placeholder="Daily digest" /></Form.Item>
      <Form.Item name="message" label="Message" rules={[{ required: true, message: "Message is required." }]}><Input.TextArea rows={5} /></Form.Item>
      <Form.Item name="timezone" label="Timezone"><Input placeholder="Asia/Shanghai" /></Form.Item>
      <Form.Item name="sessionMode" label="Session mode"><Select options={[{ value: "shared" }, { value: "isolated" }]} /></Form.Item>
      <Form.Item name="scheduleType" label="Schedule type"><Select options={[{ value: "at" }, { value: "every" }, { value: "cron" }]} /></Form.Item>
      {scheduleType === "at" ? <Form.Item name="at" label="Run at"><Input type="datetime-local" /></Form.Item> : null}
      {scheduleType === "every" ? <Form.Item name="everySeconds" label="Every seconds"><InputNumber min={1} style={{ width: "100%" }} /></Form.Item> : null}
      {scheduleType === "cron" ? <Form.Item name="cronExpr" label="Cron expression"><Input placeholder="0 9 * * *" /></Form.Item> : null}
      <DeliveryTargetFields deliveryKind={deliveryKind} />
      <Button block type="primary" htmlType="submit" loading={saving}>Save cron job</Button>
    </Form>
  );
}

function HeartbeatForm({
  form,
  onFinish,
  saving,
}: {
  form: ReturnType<typeof Form.useForm<HeartbeatFormValues>>[0];
  onFinish: (values: HeartbeatFormValues) => void;
  saving: boolean;
}) {
  const deliveryKind = Form.useWatch("deliveryKind", form) ?? "";
  return (
    <Form form={form} layout="vertical" onFinish={onFinish} initialValues={defaultHeartbeatForm()}>
      <Form.Item name="enabled" label="Enabled" valuePropName="checked"><Switch /></Form.Item>
      <Form.Item name="intervalSeconds" label="Interval seconds"><InputNumber min={1} style={{ width: "100%" }} /></Form.Item>
      <Form.Item name="timezone" label="Timezone"><Input placeholder="Asia/Shanghai" /></Form.Item>
      <Space.Compact style={{ width: "100%" }}>
        <Form.Item name="activeHoursStart" label="Active from" style={{ width: "50%" }}><Input placeholder="09:00" /></Form.Item>
        <Form.Item name="activeHoursEnd" label="Active until" style={{ width: "50%" }}><Input placeholder="18:00" /></Form.Item>
      </Space.Compact>
      <Form.Item name="sessionMode" label="Session mode"><Select options={[{ value: "shared" }, { value: "isolated" }]} /></Form.Item>
      <Form.Item name="promptOverride" label="Prompt override"><Input.TextArea rows={4} /></Form.Item>
      <Form.Item name="watchdogScript" label="Watchdog script" extra="Optional pre-run shell script: exit 0 runs the agent, non-zero skips this tick.">
        <Input placeholder="/path/to/watchdog.sh" />
      </Form.Item>
      <DeliveryTargetFields deliveryKind={deliveryKind} />
      <Button type="primary" htmlType="submit" loading={saving}>Save heartbeat</Button>
    </Form>
  );
}

function DeliveryTargetFields({ deliveryKind }: { deliveryKind: string }) {
  return (
    <>
      <Form.Item name="deliveryKind" label="Delivery target">
        <Select
          options={[
            { value: "", label: "Default" },
            { value: "session", label: "Session" },
            { value: "channel", label: "Channel" },
          ]}
        />
      </Form.Item>
      {deliveryKind === "session" ? <Form.Item name="sessionId" label="Session ID"><Input /></Form.Item> : null}
      {deliveryKind === "channel" ? (
        <>
          <Form.Item name="channel" label="Channel"><Input placeholder="weixin / feishu / web" /></Form.Item>
          <Form.Item name="sessionKey" label="Session key"><Input /></Form.Item>
          <Form.Item name="recipient" label="Recipient"><Input /></Form.Item>
        </>
      ) : null}
    </>
  );
}

function Metric({ title, value, tone }: { title: string; value: number; tone?: "danger" }) {
  return (
    <Card size="small">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} type={tone === "danger" && value > 0 ? "danger" : undefined} style={{ margin: 0 }}>
        {value}
      </Typography.Title>
    </Card>
  );
}

function RunLogsCard({ title, runs, loading, heartbeat = false }: { title: string; runs: Array<CronRunLog | HeartbeatRunLog>; loading: boolean; heartbeat?: boolean }) {
  return (
    <Card title={title}>
      {runs.length === 0 && !loading ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      ) : (
        <Table
          rowKey="id"
          size="small"
          loading={loading}
          dataSource={runs}
          pagination={{ pageSize: 6 }}
          columns={[
            { title: heartbeat ? "Rule" : "Job", dataIndex: heartbeat ? "rule_id" : "job_id" },
            { title: "Status", dataIndex: "status", render: (value) => <Tag color={value === "error" || value === "delivery_blocked" ? "red" : "green"}>{value}</Tag> },
            { title: "Started", dataIndex: "started_at", render: formatTime },
            { title: "Finished", dataIndex: "finished_at", render: formatTime },
            { title: "Error", dataIndex: "error", render: (value) => value || "-" },
            ...(heartbeat
              ? [{ title: "Watchdog output", dataIndex: "watchdog_output", ellipsis: true, render: (value?: string) => value?.trim() ? value : "-" }]
              : []),
          ]}
        />
      )}
    </Card>
  );
}

function defaultCronForm(): CronFormValues {
  return {
    enabled: true,
    message: "",
    scheduleType: "every",
    everySeconds: 3600,
    cronExpr: "0 9 * * *",
    timezone: "Asia/Shanghai",
    sessionMode: "shared",
    deliveryKind: "",
  };
}

function defaultHeartbeatForm(): HeartbeatFormValues {
  return {
    enabled: false,
    intervalSeconds: 1800,
    timezone: "Asia/Shanghai",
    sessionMode: "shared",
    watchdogScript: "",
    deliveryKind: "",
  };
}

function cronToForm(job: CronJob): CronFormValues {
  const target = job.delivery_target ?? {};
  return {
    id: job.id,
    name: job.name,
    message: job.message,
    scheduleType: (job.schedule?.type as CronFormValues["scheduleType"]) || "every",
    at: job.schedule?.at ? toDatetimeLocal(job.schedule.at) : undefined,
    everySeconds: job.schedule?.every_seconds || 3600,
    cronExpr: job.schedule?.cron_expr || "0 9 * * *",
    timezone: job.timezone || "Asia/Shanghai",
    sessionMode: (job.session_mode as CronFormValues["sessionMode"]) || "shared",
    enabled: job.enabled,
    deliveryKind: (target.kind as CronFormValues["deliveryKind"]) || "",
    sessionId: target.session_id,
    channel: target.channel,
    sessionKey: target.session_key,
    recipient: target.recipient,
  };
}

function heartbeatToForm(rule: HeartbeatRule): HeartbeatFormValues {
  const target = rule.delivery_target ?? {};
  return {
    enabled: rule.enabled,
    intervalSeconds: rule.interval_seconds || 1800,
    timezone: rule.timezone || "Asia/Shanghai",
    activeHoursStart: rule.active_hours_start,
    activeHoursEnd: rule.active_hours_end,
    sessionMode: (rule.session_mode as HeartbeatFormValues["sessionMode"]) || "shared",
    promptOverride: rule.prompt_override,
    watchdogScript: rule.watchdog_script,
    deliveryKind: (target.kind as HeartbeatFormValues["deliveryKind"]) || "",
    sessionId: target.session_id,
    channel: target.channel,
    sessionKey: target.session_key,
    recipient: target.recipient,
  };
}

function buildDeliveryTarget(values: { deliveryKind?: string; sessionId?: string; channel?: string; sessionKey?: string; recipient?: string }): DeliveryTarget | undefined {
  if (values.deliveryKind === "session" && values.sessionId?.trim()) {
    return { kind: "session", session_id: values.sessionId.trim() };
  }
  if (values.deliveryKind === "channel" && values.channel?.trim()) {
    return {
      kind: "channel",
      channel: values.channel.trim(),
      session_key: values.sessionKey?.trim() || undefined,
      recipient: values.recipient?.trim() || undefined,
    };
  }
  return undefined;
}

function buildCronPayload(values: CronFormValues) {
  return {
    name: values.name?.trim() || undefined,
    message: values.message.trim(),
    timezone: values.timezone?.trim() || "Asia/Shanghai",
    schedule: {
      type: values.scheduleType,
      at: values.scheduleType === "at" && values.at ? new Date(values.at).toISOString() : undefined,
      every_seconds: values.scheduleType === "every" ? Number(values.everySeconds || 0) : undefined,
      cron_expr: values.scheduleType === "cron" ? values.cronExpr?.trim() : undefined,
    },
    session_mode: values.sessionMode || "shared",
    delivery_target: buildDeliveryTarget(values),
    enabled: values.enabled ?? true,
  };
}

function buildHeartbeatPayload(values: HeartbeatFormValues) {
  return {
    enabled: values.enabled,
    interval_seconds: Number(values.intervalSeconds || 0),
    timezone: values.timezone?.trim() || "Asia/Shanghai",
    active_hours_start: values.activeHoursStart?.trim() || undefined,
    active_hours_end: values.activeHoursEnd?.trim() || undefined,
    session_mode: values.sessionMode || "shared",
    prompt_override: values.promptOverride?.trim() || undefined,
    watchdog_script: values.watchdogScript?.trim() || undefined,
    delivery_target: buildDeliveryTarget(values),
  };
}

function renderCronSchedule(job: CronJob) {
  switch (job.schedule?.type) {
    case "at":
      return `at ${formatTime(job.schedule.at)}`;
    case "every":
      return `every ${job.schedule.every_seconds ?? 0}s`;
    case "cron":
      return job.schedule.cron_expr || "cron";
    default:
      return "-";
  }
}

function formatTime(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function toDatetimeLocal(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
  return local.toISOString().slice(0, 16);
}

function isIssueStatus(status?: string) {
  return status === "error" || status === "delivery_blocked";
}
