import { Segmented, Skeleton, Empty, Button, Drawer, Spin, Alert, Typography, Space } from "antd";
import { PlayCircleOutlined } from "@ant-design/icons";
import { useWorkDesktopStore, WORK_DESKTOP_SECTIONS, type WorkDesktopSection } from "./workDesktopStore";
import { useWorkDesktopData } from "./useWorkDesktopData";
import { WorkDesktopSectionBoard } from "./WorkDesktopSections";
import { WorkDesktopEditorModal } from "./WorkDesktopEditorModal";
import { InlineEdit } from "./workDesktopInline";
import { InfoHint } from "./workDesktopHints";
import { useConversationLayoutStore } from "../chat/layout/layoutStore";

/**
 * The work desktop's boards, as a pane inside the chat RIGHT DOCK.
 *
 * The fusion put the desktop's boards in the dock rather than giving the desktop
 * its own page: the dock already collapses, resizes, and has a phone layout, so
 * reusing it is what removes the need to build (and maintain) a second set of
 * mobile workarounds. The section switcher is a Segmented, not the desktop's old
 * Tabs bar, because the dock is narrow.
 *
 * One component owns the whole pane so the dock shell in ChatPageView stays small
 * — ChatPageView is already at its line budget, so anything that grows must live
 * outside it.
 */
export function WorkDesktopPanel() {
  const data = useWorkDesktopData();
  const section = useWorkDesktopStore((state) => state.section);
  const setSection = useWorkDesktopStore((state) => state.setSection);
  const briefOpen = useWorkDesktopStore((state) => state.briefOpen);
  const setBriefOpen = useWorkDesktopStore((state) => state.setBriefOpen);
  const { tr, contextsQuery, selected, briefQuery, invalidContext, readOnly, patchContextMutation } = data;
  // The desktop rail owns the editor modal whenever it is on screen; this copy exists
  // only for when it is not (the dock's workdesk tab opened while the rail still shows
  // the conversation directory). Rendering both unconditionally would stack two
  // identical dialogs on top of each other.
  const leftView = useConversationLayoutStore((state) => state.leftView);
  if (contextsQuery.isLoading) {
    return <div className="wd-dock-msg"><Skeleton active paragraph={{ rows: 6 }} /></div>;
  }
  if (contextsQuery.isError) {
    return (
      <div className="wd-dock-msg">
        <Alert
          type="error"
          showIcon
          title={tr("loadFailed")}
          action={<Button size="small" onClick={() => void contextsQuery.refetch()}>{tr("retry")}</Button>}
        />
      </div>
    );
  }

  const options = WORK_DESKTOP_SECTIONS.map((value) => ({ value, label: tr(sectionLabelKey(value)) }));

  return (
    <div className="wd-dock" data-testid="chat-v2-workdesk-panel">
      <div className="wd-dock-head">
        <Segmented
          size="small"
          block
          value={section}
          onChange={(value) => setSection(value as WorkDesktopSection)}
          options={options}
          data-testid="chat-v2-workdesk-sections"
        />
        {selected ? (
          // The desktop's name is edited in place, exactly as on the standalone page:
          // the title is the stream's identity, so it should not need a modal.
          <Space size={6} align="center" className="wd-dock-titlebar">
            <InlineEdit
              className="wd-title-edit"
              ariaLabel={tr("editTitle")}
              editIconLabel={tr("editTitle")}
              value={selected.title}
              disabled={readOnly}
              required
              render={(value) => <Typography.Text strong className="wd-dock-title" ellipsis={{ tooltip: value }}>{value}</Typography.Text>}
              onSave={(next) => patchContextMutation.mutateAsync({ id: selected.id, version: selected.version, title: next })}
            />
            {/* "Restore context" opens the brief: background, goal, checkpoint, next
                actions and evidence in one read, which is how you pick a stream back
                up after time away. The hover hint explains that the Agent maintains
                this memory as work happens. */}
            <InfoHint title={tr("maintain")} text={tr("maintainHint")} />
            <Button size="small" type="primary" icon={<PlayCircleOutlined aria-hidden="true" />} onClick={() => setBriefOpen(true)}>{tr("continueWork")}</Button>
          </Space>
        ) : null}
      </div>
      {invalidContext ? <Alert className="wd-notice" type="warning" showIcon title={tr("invalidContext")} /> : null}
      {readOnly ? <Alert className="wd-notice" type="warning" showIcon title={tr("readOnly")} /> : null}
      <div className="wd-dock-body">
        {selected || section === "attention"
          ? <WorkDesktopSectionBoard section={section} data={data} />
          : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noContexts")} />}
      </div>

      <Drawer
        zIndex={1300}
        styles={{ wrapper: { maxWidth: "100vw" } }}
        title={tr("briefTitle")}
        open={briefOpen}
        onClose={() => setBriefOpen(false)}
        size={600}
      >
        {briefQuery.isLoading ? <Spin /> : briefQuery.data ? briefBody(briefQuery.data) : <Alert type="error" showIcon title={tr("loadFailed")} />}
      </Drawer>

      {fallbackModal()}
    </div>
  );

  // Fallback modal: only when the desktop rail is NOT on screen (see the note above).
  function fallbackModal() {
    return leftView === "desktop" ? null : <WorkDesktopEditorModal data={data} />;
  }

  function briefBody(brief: NonNullable<typeof briefQuery.data>) {
    const checkpoint = brief.context.checkpoint;
    return <div className="wd-stack">
      <Alert type="info" showIcon title={tr("briefHint")} />
      <Typography.Title level={4}>{brief.context.title}</Typography.Title>
      <Typography.Text strong>{tr("goal")}</Typography.Text>
      <Typography.Paragraph>{brief.context.goal || tr("noItems")}</Typography.Paragraph>
      <Typography.Text type="secondary">{tr("basedOnRevision", { revision: checkpoint.revision })}</Typography.Text>
      {bullets(tr("completed"), checkpoint.completed)}
      {bullets(tr("inProgress"), checkpoint.in_progress)}
      {bullets(tr("next"), checkpoint.next_actions)}
      {bullets(tr("blockers"), checkpoint.blockers)}
      {bullets(tr("openQuestions"), checkpoint.open_questions)}
      <Alert type="warning" showIcon title={tr("currentUnknown")} description={tr("currentUnknownHint")} />
    </div>;
  }

  function bullets(title: string, items: string[]) {
    return <section className="wd-section">
      {title && <Typography.Text strong>{title}</Typography.Text>}
      {items.length ? <ul>{items.map((item, index) => <li key={index}>{item}</li>)}</ul> : <Typography.Paragraph type="secondary">{tr("noItems")}</Typography.Paragraph>}
    </section>;
  }
}

/** The Segmented label for a section: reuse the desktop's existing tab wording so
 *  the fusion does not invent a second vocabulary for the same boards. */
/** The Segmented label for a section. Short forms, because the dock is narrow and
 *  shows all eight sections at once. */
function sectionLabelKey(section: WorkDesktopSection): string {
  switch (section) {
    case "plan": return "sectionPlan";
    case "conversations": return "sectionConversations";
    case "delegations": return "sectionDelegations";
    case "knowledge": return "sectionKnowledge";
    case "timeline": return "sectionTimeline";
    case "resources": return "sectionResources";
    case "attention": return "sectionAttention";
    default: return "sectionOverview";
  }
}
