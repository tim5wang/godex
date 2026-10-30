import { useState } from "react";
import { Button, DatePicker, Empty, Input, Space, Tag } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { InlineEdit, InlineSelect } from "./workDesktopInline";
import type { WorkTodo, WorkTodoInput, WorkTodoStatus } from "../../lib/apiWorkContext";

const STATUS_COLOR: Record<WorkTodoStatus, string> = { todo: "default", doing: "processing", done: "success", blocked: "error" };

/**
 * TodoBoard renders a stream's plan items. Every field is edited in place
 * (status, text, owner, due) so maintaining the plan never pulls the user out of
 * the desktop; the only modal-free add is the inline new-item row at the bottom.
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
          <InlineSelect<WorkTodoStatus>
            ariaLabel={`${tr("todoStatus.todo")}: ${todo.text}`}
            className="wd-todo-status"
            value={todo.status}
            disabled={readOnly}
            options={statusOptions}
            render={(value) => <Tag color={STATUS_COLOR[value]}>{tr(`todoStatus.${value}`)}</Tag>}
            onSave={(next) => onPatch(todo, { status: next })}
          />
          <InlineEdit
            className="wd-todo-text"
            ariaLabel={`${tr("todoText")}: ${todo.text}`}
            value={todo.text}
            disabled={readOnly}
            required
            onSave={(next) => onPatch(todo, { text: next })}
          />
          <span className="wd-todo-meta">
            <span className="wd-muted">{tr("todoOwner")}</span>
            <InlineEdit
              ariaLabel={`${tr("todoOwner")}: ${todo.text}`}
              value={todo.owner}
              placeholder="—"
              disabled={readOnly}
              onSave={(next) => onPatch(todo, { owner: next })}
            />
          </span>
          <span className="wd-todo-meta">
            <span className="wd-muted">{tr("todoDue")}</span>
            <DatePicker
              size="small"
              variant="borderless"
              aria-label={`${tr("todoDue")}: ${todo.text}`}
              disabled={readOnly}
              value={todo.due_at ? dayjs(todo.due_at) : null}
              placeholder="—"
              onChange={(value) => void onPatch(todo, { due_at: value ? value.toISOString() : "" })}
            />
          </span>
          {!readOnly ? (
            <Button
              type="text"
              size="small"
              aria-label={`${tr("todoDeleted")}: ${todo.text}`}
              icon={<DeleteOutlined aria-hidden="true" />}
              onClick={() => void onDelete(todo)}
            />
          ) : null}
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
