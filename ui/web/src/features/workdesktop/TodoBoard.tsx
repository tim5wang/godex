import { useState } from "react";
import { Button, DatePicker, Empty, Input, Space, Tag, Tooltip } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { InlineEdit, InlineSelect } from "./workDesktopInline";
import type { WorkTodo, WorkTodoInput, WorkTodoStatus } from "../../lib/apiWorkContext";

const STATUS_COLOR: Record<WorkTodoStatus, string> = { todo: "default", doing: "processing", done: "success", blocked: "error" };

/**
 * TodoBoard renders a stream's plan items as compact bubbles.
 *
 * The desktop lives in the chat's right dock, which is far narrower than the old
 * standalone page, so the previous five-column grid (status | text | owner | due |
 * delete) had to go: at dock width those columns squeezed into unreadable strips.
 * Each item is now a bubble of two lines —
 *
 *   line 1: the plan text (what you read first)
 *   line 2: status pill · owner · due · delete
 *
 * — and line 2 wraps instead of compressing, so a narrow dock stacks the pieces.
 * The meta labels ("Owner" / "Due") are gone: a name and a date are self-evident
 * once shown, so they survive only as tooltips, where they still help the empty
 * ("—") state. Every field is still edited in place.
 *
 * A done item is marked by colour rather than a strikethrough: line-through
 * measurably hurts readability at this size, and the status pill already says
 * "Done" — the colour is the reinforcement, not the only signal.
 */
export function TodoBoard(props: {
  todos: WorkTodo[];
  readOnly: boolean;
  onCreate: (input: WorkTodoInput) => Promise<unknown> | unknown;
  onPatch: (todo: WorkTodo, patch: { text?: string; status?: WorkTodoStatus; owner?: string; due_at?: string }) => Promise<unknown> | unknown;
  onDelete: (todo: WorkTodo) => Promise<unknown> | unknown;
}) {
  const { todos, readOnly, onCreate, onPatch, onDelete } = props;
  const { t } = useI18n();
  const tr = (key: string) => t(`workDesktop.${key}`);
  const [newText, setNewText] = useState("");
  const [adding, setAdding] = useState(false);

  const submitNew = async () => {
    const text = newText.trim();
    if (!text) return;
    setAdding(true);
    try {
      await onCreate({ text });
      setNewText("");
    } finally {
      setAdding(false);
    }
  };

  const statusOptions = (["todo", "doing", "done", "blocked"] as WorkTodoStatus[]).map((value) => ({
    value,
    label: tr(`todoStatus.${value}`),
  }));

  return (
    <div className="wd-todos">
      {todos.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("todoEmpty")} /> : null}
      {todos.map((todo) => (
        <div key={todo.id} className={`wd-todo wd-todo-${todo.status}`}>
          {/* Line 1: the plan text on its own row, so the bubble reads top-down as
              "what" then "who / when" — the order it is actually scanned. */}
          <div className="wd-todo-text-row">
            <InlineEdit
              className="wd-todo-text"
              ariaLabel={`${tr("todoText")}: ${todo.text}`}
              value={todo.text}
              disabled={readOnly}
              required
              onSave={(next) => onPatch(todo, { text: next })}
            />
          </div>
          <div className="wd-todo-meta-row">
            <InlineSelect<WorkTodoStatus>
              ariaLabel={`${tr("todoStatus.todo")}: ${todo.text}`}
              className="wd-todo-status"
              value={todo.status}
              disabled={readOnly}
              options={statusOptions}
              render={(value) => <Tag color={STATUS_COLOR[value]}>{tr(`todoStatus.${value}`)}</Tag>}
              onSave={(next) => onPatch(todo, { status: next })}
            />
            {/* Owner and due carry no visible label: a name and a date read fine on
                their own, and a label each would eat two widths in the narrow dock.
                The label survives as a tooltip, which is where it still earns its
                place (the empty "—" state). */}
            <Tooltip title={tr("todoOwner")}>
              <span className="wd-todo-meta">
                <InlineEdit
                  ariaLabel={`${tr("todoOwner")}: ${todo.text}`}
                  value={todo.owner}
                  placeholder="—"
                  disabled={readOnly}
                  onSave={(next) => onPatch(todo, { owner: next })}
                />
              </span>
            </Tooltip>
            <Tooltip title={tr("todoDue")}>
              <span className="wd-todo-meta">
                <DatePicker
                  size="small"
                  variant="borderless"
                  className="wd-todo-due"
                  aria-label={`${tr("todoDue")}: ${todo.text}`}
                  disabled={readOnly}
                  value={todo.due_at ? dayjs(todo.due_at) : null}
                  placeholder="—"
                  onChange={(value) => void onPatch(todo, { due_at: value ? value.toISOString() : "" })}
                />
              </span>
            </Tooltip>
            {!readOnly ? (
              <Button
                className="wd-todo-delete"
                type="text"
                size="small"
                aria-label={`${tr("todoDeleted")}: ${todo.text}`}
                icon={<DeleteOutlined aria-hidden="true" />}
                onClick={() => void onDelete(todo)}
              />
            ) : null}
          </div>
        </div>
      ))}
      {!readOnly ? (
        <Space.Compact className="wd-todo-add">
          <Input
            aria-label={tr("todoText")}
            placeholder={tr("todoAdd")}
            value={newText}
            onChange={(event) => setNewText(event.target.value)}
            onPressEnter={() => void submitNew()}
            disabled={adding}
          />
          <Button icon={<PlusOutlined aria-hidden="true" />} loading={adding} onClick={() => void submitNew()}>
            {tr("todoAdd")}
          </Button>
        </Space.Compact>
      ) : null}
    </div>
  );
}
