import { Form, Input, Modal, Select, Space, Button, Typography, Card, Alert, Tag } from "antd";
import { useEffect } from "react";
import { useWorkDesktopStore, type EditorValues } from "./workDesktopStore";
import type { WorkDesktopData } from "./useWorkDesktopData";
import type { AgentTemplate } from "../../lib/apiAgent";

const nonBlank = { required: true, whitespace: true };
const resourceKinds = ["web", "directory", "repository", "note", "document", "dashboard", "service", "model", "artifact"];

/**
 * The desktop's editor modal (create desktop / delegate / link conversation /
 * add fact / add resource / add relation / reject a fact).
 *
 * It is rendered once, by the dock, and driven by the shared store's `editor`
 * field — the triggers live in both the left rail and the right dock, so the
 * pending editor cannot be local state in either place.
 */
export function WorkDesktopEditorModal({ data }: { data: WorkDesktopData }) {
  const editor = useWorkDesktopStore((state) => state.editor);
  const reviewTargetId = useWorkDesktopStore((state) => state.reviewTargetId);
  const delegateSeed = useWorkDesktopStore((state) => state.delegateSeed);
  const closeEditor = useWorkDesktopStore((state) => state.closeEditor);
  const [form] = Form.useForm<EditorValues>();
  const { tr, trLabel, contexts, contextId, selected, facts, projectsQuery, templatesQuery } = data;

  // Pre-fill the goal when the delegate editor was opened FROM an inbox row. Only the goal:
  // the acceptance criteria stay empty on purpose, because "what counts as done" is the
  // human's input and the form already refuses to submit without it. setFieldsValue (not
  // initialValue) because the seed arrives after the modal mounts.
  useEffect(() => {
    if (editor === "delegate" && delegateSeed) {
      form.setFieldsValue({ title: delegateSeed.title });
    }
  }, [editor, delegateSeed, form]);

  // The reviewed fact is looked up by id so the store stays plain serialisable
  // state (a whole fact object in the store would go stale after any refetch).
  const reviewTarget = editor === "review" ? facts.find((item) => item.id === reviewTargetId) : undefined;

  const title = editor === "create" ? tr("newContext")
    : editor === "delegate" ? tr("delegate")
      : editor === "session" ? tr("newSession")
        : editor === "fact" ? tr("addFact")
          : editor === "resource" ? tr("addResource")
            : editor === "relation" ? tr("addRelation")
              : tr("reviewReject");
  const submitLabel = editor === "create" ? tr("create") : editor === "delegate" ? tr("delegate") : editor === "review" ? tr("reviewReject") : tr("save");

  return (
    <Modal zIndex={1300} title={title} open={!!editor} onCancel={closeEditor} footer={null} width={680} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={(values) => { data.submitEditor(values); form.resetFields(); }}>
        {editor === "create" && <>
          <Alert className="wd-store" type="info" showIcon title={tr("createHint")} />
          <Form.Item label={tr("contextName")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus /></Form.Item>
          <Form.Item label={tr("background")} name="background"><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item label={tr("goal")} name="goal" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={2} maxLength={2000} /></Form.Item>
        </>}
        {editor === "delegate" && <>
          <Form.Item label={tr("delegateTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus placeholder={tr("delegateTitleHint")} /></Form.Item>
          <Form.Item label={tr("delegateDescription")} name="description"><Input.TextArea rows={3} maxLength={6000} /></Form.Item>
          <Form.Item label={tr("delegateAcceptance")} name="acceptance" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={6000} /></Form.Item>
          <Form.Item label={tr("delegateProject")} name="project"><Select allowClear options={(projectsQuery.data?.projects ?? []).map((project) => ({ value: project.id, label: project.name }))} /></Form.Item>
          <Form.Item label={tr("delegateWorkDir")} name="workDir"><Input maxLength={400} /></Form.Item>
          <Form.Item label={tr("delegateTemplate")} name="template"><Select allowClear options={(templatesQuery.data ?? []).map((tpl: AgentTemplate) => ({ value: tpl.id, label: tpl.name }))} /></Form.Item>
          {selected && <Card size="small" title={tr("contextPack")}><Typography.Paragraph type="secondary">{tr("packHint")}</Typography.Paragraph><Typography.Text strong>{selected.title}</Typography.Text><Typography.Paragraph>{selected.checkpoint.summary}</Typography.Paragraph><Tag>{tr("revision", { revision: selected.checkpoint.revision })}</Tag></Card>}
        </>}
        {editor === "session" && <>
          <Form.Item label={tr("topicName")} name="topic" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus /></Form.Item>
          <Form.Item label={tr("workspaceDir")} name="workspaceDir" extra={tr("workspaceDirHint")}><Input maxLength={400} /></Form.Item>
          <Form.Item label={tr("sessionIdLabel")} name="sessionId" extra={tr("sessionIdHint")}><Input maxLength={200} /></Form.Item>
        </>}
        {editor === "fact" && <>
          <Form.Item label={tr("factTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={200} autoFocus /></Form.Item>
          <Form.Item label={tr("factContent")} name="content" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={4000} /></Form.Item>
          <Form.Item label={tr("factKindLabel")} name="factKind" initialValue="fact"><Select options={["fact", "decision", "trace", "method", "warning", "preference"].map((value) => ({ value, label: tr(`factKind.${value}`) }))} /></Form.Item>
          <Form.Item label={tr("factScopeLabel")} name="scope" extra={tr("governanceHint")}><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSubjectRef")} name="subject"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factPredicate")} name="predicate"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSource")} name="source" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={600} /></Form.Item>
          <Form.Item label={tr("factSourceExcerpt")} name="sourceExcerpt"><Input.TextArea rows={2} maxLength={2000} /></Form.Item>
          <Form.Item label={tr("factObserved")} name="observedAt" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={60} placeholder="2026-10-30T09:00:00Z" /></Form.Item>
          <Form.Item label={tr("factValidUntil")} name="validUntil"><Input maxLength={60} placeholder="2026-12-01T09:00:00Z" /></Form.Item>
          <Form.Item label={tr("factProvenance")} name="provenance"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("factSupersedes")} name="supersedes"><Input maxLength={200} /></Form.Item>
        </>}
        {editor === "resource" && <>
          <Alert className="wd-store" type="info" showIcon title={tr("sourceBoundary")} />
          <Form.Item label={tr("resourceTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={200} autoFocus /></Form.Item>
          <Form.Item label={tr("resourceKindLabel")} name="resourceKind" initialValue="web"><Select options={resourceKinds.map((kind) => ({ value: kind, label: trLabel("kind", kind) }))} /></Form.Item>
          <Form.Item label={tr("resourceLocator")} name="locator" rules={[{ ...nonBlank, message: tr("required") }]} extra={tr("resourceLocatorHint")}><Input maxLength={1000} /></Form.Item>
          <Form.Item label={tr("resourceNode")} name="node"><Input maxLength={200} /></Form.Item>
          <Form.Item label={tr("resourcePurpose")} name="purpose"><Input.TextArea rows={2} maxLength={1000} /></Form.Item>
        </>}
        {editor === "relation" && <>
          <Form.Item label={tr("relationTarget")} name="target" rules={[{ ...nonBlank, message: tr("required") }]}><Select options={contexts.filter((item) => item.id !== contextId).map((item) => ({ value: item.id, label: item.title }))} /></Form.Item>
          <Form.Item label={tr("relationKind")} name="relationKind" initialValue="related_to"><Select options={[{ value: "related_to", label: tr("relationKind.related_to") }, { value: "depends_on", label: tr("relationKind.depends_on") }]} /></Form.Item>
          <Form.Item label={tr("relationCondition")} name="condition" extra={tr("relationHint")}><Input.TextArea rows={2} maxLength={1000} /></Form.Item>
        </>}
        {editor === "review" && <>
          {reviewTarget && <Typography.Paragraph><Typography.Text strong>{reviewTarget.title}</Typography.Text></Typography.Paragraph>}
          <Form.Item label={tr("reviewReason")} name="reason" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={2000} placeholder={tr("reviewReasonPlaceholder")} /></Form.Item>
        </>}
        <Space style={{ marginTop: 20 }}>
          <Button type="primary" htmlType="submit" loading={data.createMutation.isPending || data.delegationMutation.isPending || data.factMutation.isPending || data.reviewMutation.isPending || data.patchContextMutation.isPending}>{submitLabel}</Button>
          <Button onClick={closeEditor}>{tr("cancel")}</Button>
        </Space>
      </Form>
    </Modal>
  );
}
