import { useEffect, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Alert, App as AntApp, Button, Card, Empty, Input, InputNumber, Select, Space, Tag, Typography } from "antd";
import { PlusOutlined, SaveOutlined } from "@ant-design/icons";
import {
  createFlow,
  type FlowDefinition,
  type FlowSessionLane,
  type FlowSessionTrigger,
  type FlowSessionWorkflow,
  type FlowVersionView,
} from "../../lib/api";
import { showError } from "../../lib/notifications";

const { Text } = Typography;

interface Props {
  flowId: string;
  token: string | null;
  versions: FlowVersionView[];
  getCurrentDefinition: () => FlowDefinition | undefined;
  onSaved: () => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}

const emptySessionWorkflow = (): FlowSessionWorkflow => ({ triggers: [], lanes: [] });

function laneRequiresLatestWins(lane?: FlowSessionLane) {
  return lane?.cadence === "periodic" ||
    lane?.cadence === "hybrid" ||
    lane?.overload_policy === "drop_newest";
}

function nextVersionOf(versions: FlowVersionView[]) {
  return String(
    versions.reduce((max, row) => {
      const value = Number.parseInt(row.version, 10);
      return Number.isFinite(value) ? Math.max(max, value) : max;
    }, 0) + 1,
  );
}

export function SessionWorkflowTab({ flowId, token, versions, getCurrentDefinition, onSaved, t }: Props) {
  const { message } = AntApp.useApp();
  const latestDefinition = useMemo(
    () => [...versions].reverse().find((version) => version.definition)?.definition,
    [versions],
  );
  const currentDefinition = getCurrentDefinition() ?? latestDefinition;
  const [mode, setMode] = useState<"request" | "session">("request");
  const [workflow, setWorkflow] = useState<FlowSessionWorkflow>(emptySessionWorkflow);
  const nextVersion = useMemo(() => nextVersionOf(versions), [versions]);

  useEffect(() => {
    if (!currentDefinition) return;
    setMode(currentDefinition.execution_mode === "session" ? "session" : "request");
    setWorkflow(currentDefinition.session_workflow ?? emptySessionWorkflow());
    // Only reset when the persisted definition changes, not while editing rows.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [latestDefinition]);

  const nodeOptions = currentDefinition?.nodes.map((node) => ({
    value: node.id,
    label: `${node.id} (${node.kind})`,
  })) ?? [];
  const lanes = workflow.lanes ?? [];
  const saveMutation = useMutation({
    mutationFn: async () => {
      const base = getCurrentDefinition() ?? latestDefinition;
      if (!base) throw new Error(t("flows.noDefinition"));
      if (mode === "session") {
        if (workflow.triggers.length === 0) throw new Error(t("flows.sessionTriggerRequired"));
        if (workflow.triggers.some((trigger) =>
          !trigger.event_type.trim() ||
          trigger.event_type.trim().length > 128 ||
          !trigger.entry_node ||
          !base.nodes.some((node) => node.id === trigger.entry_node),
        )) {
          throw new Error(t("flows.sessionTriggerFieldsRequired"));
        }
        const eventTypes = workflow.triggers.map((trigger) => trigger.event_type.trim());
        if (new Set(eventTypes).size !== eventTypes.length) {
          throw new Error(t("flows.sessionDuplicateEventType"));
        }
        const laneIDs = lanes.map((lane) => lane.id.trim());
        if (lanes.some((lane) => !lane.id.trim() || lane.id.trim().length > 64 || !/^[A-Za-z0-9_.-]+$/.test(lane.id.trim()))) {
          throw new Error(t("flows.sessionLaneIdInvalid"));
        }
        if (new Set(laneIDs).size !== laneIDs.length) {
          throw new Error(t("flows.sessionDuplicateLaneId"));
        }
        const assignedLanes = workflow.triggers.flatMap((trigger) => trigger.lane_id ? [trigger.lane_id] : []);
        if (new Set(assignedLanes).size !== assignedLanes.length) {
          throw new Error(t("flows.sessionLaneAssignedTwice"));
        }
        if (lanes.some((lane) => !assignedLanes.includes(lane.id))) {
          throw new Error(t("flows.sessionLaneUnused"));
        }
        for (const trigger of workflow.triggers) {
          const lane = lanes.find((candidate) => candidate.id === trigger.lane_id);
          if (trigger.lane_id && !lane) {
            throw new Error(t("flows.sessionLaneUnknown"));
          }
          if (laneRequiresLatestWins(lane) && trigger.delivery !== "latest_wins") {
            throw new Error(t("flows.sessionLaneDelivery"));
          }
        }
      }
      const definition: FlowDefinition = {
        ...base,
        flow_id: flowId,
        version: nextVersion,
        status: "draft",
        execution_mode: mode,
      };
      if (mode === "session") {
        definition.session_workflow = {
          triggers: workflow.triggers,
          ...(lanes.length > 0 ? { lanes } : {}),
        };
      } else {
        delete definition.session_workflow;
      }
      return createFlow(token, {
        flow_id: flowId,
        version: nextVersion,
        status: "draft",
        definition,
      });
    },
    onSuccess: () => {
      message?.success(t("flows.sessionConfigSaved", { version: nextVersion }));
      onSaved();
    },
    onError: (err) => {
      if (message) showError(message, err, t("flows.saveFailed"));
    },
  });

  const addTrigger = () =>
    setWorkflow((current) => {
      const currentLanes = current.lanes ?? [];
      const lane = currentLanes.find(
        (candidate) => !current.triggers.some((trigger) => trigger.lane_id === candidate.id),
      );
      const existingEvents = new Set(current.triggers.map((trigger) => trigger.event_type.trim()));
      let suffix = current.triggers.length + 1;
      while (existingEvents.has(`event.${suffix}`)) suffix += 1;
      return {
        ...current,
        triggers: [
          ...current.triggers,
          {
            event_type: `event.${suffix}`,
            entry_node: nodeOptions[0]?.value ?? "",
            delivery: laneRequiresLatestWins(lane) ? "latest_wins" : "durable",
            ...(lane ? { lane_id: lane.id } : {}),
          },
        ],
      };
    });
  const addLane = () =>
    setWorkflow((current) => {
      const currentLanes = current.lanes ?? [];
      const existingIDs = new Set(currentLanes.map((lane) => lane.id));
      let suffix = currentLanes.length + 1;
      while (existingIDs.has(`lane-${suffix}`)) suffix += 1;
      const lane: FlowSessionLane = {
        id: `lane-${suffix}`,
        cadence: "event",
        deadline_ms: 1000,
        class: "standard",
        overload_policy: "coalesce_latest",
      };
      const firstUnassignedTrigger = current.triggers.findIndex((trigger) => !trigger.lane_id);
      return {
        ...current,
        lanes: [...currentLanes, lane],
        triggers: current.triggers.map((trigger, index) =>
          index === firstUnassignedTrigger ? { ...trigger, lane_id: lane.id } : trigger,
        ),
      };
    });
  const updateTrigger = (index: number, patch: Partial<FlowSessionTrigger>) =>
    setWorkflow((current) => ({
      ...current,
      triggers: current.triggers.map((trigger, i) => {
        if (i !== index) return trigger;
        const updated = { ...trigger, ...patch };
        const lane = (current.lanes ?? []).find((candidate) => candidate.id === updated.lane_id);
        if (laneRequiresLatestWins(lane)) updated.delivery = "latest_wins";
        return updated;
      }),
    }));
  const updateLane = (index: number, patch: Partial<FlowSessionLane>) =>
    setWorkflow((current) => {
      const currentLanes = current.lanes ?? [];
      const previousLane = currentLanes[index];
      const updatedLane = previousLane ? { ...previousLane, ...patch } : undefined;
      return {
        ...current,
        lanes: currentLanes.map((lane, i) => (i === index ? { ...lane, ...patch } : lane)),
        triggers: current.triggers.map((trigger) => {
          let updated = trigger;
          if (previousLane && patch.id && trigger.lane_id === previousLane.id) {
            updated = { ...updated, lane_id: patch.id };
          }
          if (updatedLane && updated.lane_id === updatedLane.id && laneRequiresLatestWins(updatedLane)) {
            updated = { ...updated, delivery: "latest_wins" };
          }
          return updated;
        }),
      };
    });

  if (!currentDefinition) {
    return <Empty description={t("flows.canvasEmpty")} />;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <Alert
        type="info"
        showIcon
        message={t("flows.sessionConfigIntro")}
        description={t("flows.sessionConfigSnapshotHint")}
      />
      <Space align="center">
        <Text strong>{t("flows.executionMode")}</Text>
        <Select
          value={mode}
          style={{ width: 180 }}
          options={[
            { value: "request", label: t("flows.requestMode") },
            { value: "session", label: t("flows.sessionMode") },
          ]}
          onChange={(value: "request" | "session") => setMode(value)}
        />
        <Tag color={mode === "session" ? "purple" : "blue"}>
          {mode === "session" ? "FlowSession" : "FlowRun"}
        </Tag>
      </Space>

      {mode === "session" ? (
        <>
          <Card
            size="small"
            title={t("flows.sessionTriggers")}
            extra={
              <Button size="small" icon={<PlusOutlined />} onClick={addTrigger}>
                {t("flows.sessionAddTrigger")}
              </Button>
            }
          >
            {workflow.triggers.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.sessionNoTriggers")} />
            ) : (
              <Space direction="vertical" style={{ width: "100%" }}>
                {workflow.triggers.map((trigger, index) => (
                  <Card key={index} size="small" style={{ background: "#fafafa" }}>
                    <Space wrap align="center">
                      <Input
                        aria-label={t("flows.sessionEventType")}
                        style={{ width: 170 }}
                        value={trigger.event_type}
                        placeholder="voice.asr.final"
                        onChange={(event) => updateTrigger(index, { event_type: event.target.value })}
                      />
                      <Select
                        aria-label={t("flows.sessionEntryNode")}
                        showSearch
                        style={{ width: 190 }}
                        value={trigger.entry_node || undefined}
                        placeholder={t("flows.sessionEntryNode")}
                        options={nodeOptions}
                        onChange={(entry_node) => updateTrigger(index, { entry_node })}
                      />
                        <Select
                          aria-label={t("flows.sessionDelivery")}
                          style={{ width: 145 }}
                          value={trigger.delivery ?? "durable"}
                        options={[
                          { value: "durable", label: "durable" },
                          { value: "latest_wins", label: "latest_wins" },
                        ]}
                        onChange={(delivery: FlowSessionTrigger["delivery"]) => updateTrigger(index, { delivery })}
                      />
                      <Select
                          aria-label={t("flows.sessionLane")}
                          allowClear
                          style={{ width: 150 }}
                          value={trigger.lane_id}
                          placeholder={t("flows.sessionNoLane")}
                          options={lanes
                            .filter((lane) =>
                              !workflow.triggers.some(
                                (other, otherIndex) =>
                                  otherIndex !== index && other.lane_id === lane.id,
                              ),
                            )
                            .map((lane) => ({ value: lane.id, label: lane.id }))}
                          onChange={(lane_id) => updateTrigger(index, { lane_id })}
                      />
                      <Button
                        size="small"
                        danger
                        onClick={() =>
                          setWorkflow((current) => ({
                            ...current,
                            triggers: current.triggers.filter((_, i) => i !== index),
                          }))
                        }
                      >
                        {t("flows.delete")}
                      </Button>
                    </Space>
                  </Card>
                ))}
              </Space>
            )}
          </Card>

          <Card
            size="small"
            title={t("flows.sessionLanes")}
            extra={
              <Button size="small" icon={<PlusOutlined />} onClick={addLane}>
                {t("flows.sessionAddLane")}
              </Button>
            }
          >
            {lanes.length === 0 ? (
              <Text type="secondary">{t("flows.sessionNoLanes")}</Text>
            ) : (
              <Space direction="vertical" style={{ width: "100%" }}>
                {lanes.map((lane, index) => (
                  <Card key={index} size="small" style={{ background: "#fafafa" }}>
                    <Space wrap align="center">
                      <Input
                        aria-label={t("flows.sessionLaneId")}
                        style={{ width: 115 }}
                        value={lane.id}
                        onChange={(event) => updateLane(index, { id: event.target.value })}
                      />
                      <Select
                        aria-label={t("flows.sessionCadence")}
                        style={{ width: 125 }}
                        value={lane.cadence ?? "event"}
                        options={[
                          { value: "event", label: "event" },
                          { value: "periodic", label: "periodic" },
                          { value: "hybrid", label: "hybrid" },
                        ]}
                        onChange={(cadence: FlowSessionLane["cadence"]) => {
                          const patch: Partial<FlowSessionLane> = { cadence };
                          if (cadence === "event") patch.interval_ms = undefined;
                          if (cadence !== "event" && !lane.interval_ms) patch.interval_ms = 1000;
                          updateLane(index, patch);
                        }}
                      />
                      <InputNumber
                        aria-label={t("flows.sessionInterval")}
                        min={10}
                        max={86400000}
                        style={{ width: 130 }}
                        disabled={(lane.cadence ?? "event") === "event"}
                        value={lane.interval_ms}
                        placeholder={t("flows.sessionInterval")}
                        onChange={(interval_ms) => updateLane(index, { interval_ms: interval_ms ?? undefined })}
                      />
                      <InputNumber
                        aria-label={t("flows.sessionDeadline")}
                        min={10}
                        max={86400000}
                        style={{ width: 130 }}
                        value={lane.deadline_ms}
                        onChange={(deadline_ms) => updateLane(index, { deadline_ms: deadline_ms ?? 1000 })}
                      />
                      <Select
                        aria-label={t("flows.sessionLaneClass")}
                        style={{ width: 125 }}
                        value={lane.class ?? "standard"}
                        options={["fast", "standard", "slow"].map((value) => ({ value, label: value }))}
                        onChange={(value: FlowSessionLane["class"]) => updateLane(index, { class: value })}
                      />
                      <Select
                        aria-label={t("flows.sessionOverload")}
                        style={{ width: 170 }}
                        value={lane.overload_policy ?? "coalesce_latest"}
                        options={[
                          { value: "coalesce_latest", label: "coalesce_latest" },
                          { value: "drop_newest", label: "drop_newest" },
                        ]}
                        onChange={(value: FlowSessionLane["overload_policy"]) =>
                          updateLane(index, { overload_policy: value })
                        }
                      />
                      <Button
                        size="small"
                        danger
                        onClick={() => {
                          const removedID = lanes[index]?.id;
                          setWorkflow((current) => ({
                            triggers: current.triggers.map((trigger) =>
                              trigger.lane_id === removedID ? { ...trigger, lane_id: undefined } : trigger,
                            ),
                            lanes: (current.lanes ?? []).filter((_, i) => i !== index),
                          }));
                        }}
                      >
                        {t("flows.delete")}
                      </Button>
                    </Space>
                  </Card>
                ))}
              </Space>
            )}
          </Card>
          <Alert
            type="warning"
            showIcon
            message={t("flows.sessionRuntimeLimits")}
            description={t("flows.sessionRuntimeLimitsHint")}
          />
        </>
      ) : (
        <Alert type="info" showIcon message={t("flows.requestModeHint")} />
      )}

      <Space>
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={saveMutation.isPending}
          onClick={() => saveMutation.mutate()}
        >
          {t("flows.sessionSaveAsVersion", { version: nextVersion })}
        </Button>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t("flows.sessionSaveHint")}
        </Text>
      </Space>
    </div>
  );
}
