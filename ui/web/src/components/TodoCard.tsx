import { Card, Progress, Space, Tag, Typography } from "antd";
import { CheckCircleOutlined, ClockCircleOutlined, MinusCircleOutlined } from "@ant-design/icons";

import type { FeedItem } from "../lib/types";

interface TodoCardProps {
  item: FeedItem;
}

export function TodoCard({ item }: TodoCardProps) {
  const todos = item.todoItems ?? [];
  const total = item.todoStats?.total ?? todos.length;
  const completed = item.todoStats?.completed ?? todos.filter((todo) => todo.status === "completed").length;
  const percent = total > 0 ? Math.round((completed / total) * 100) : 0;

  return (
    <Card className="todo-card" size="small" title="Todo list" extra={<Tag color="processing">{completed}/{total}</Tag>}>
      <Space direction="vertical" size={10} style={{ width: "100%" }}>
        <Progress percent={percent} size="small" showInfo={false} />
        <div className="todo-card-list">
          {todos.map((todo, index) => (
            <div className="todo-card-row" key={`${todo.id ?? index}:${todo.content}`}>
              {todo.status === "completed" ? (
                <CheckCircleOutlined className="todo-card-icon todo-card-icon-completed" />
              ) : todo.status === "in_progress" ? (
                <ClockCircleOutlined className="todo-card-icon todo-card-icon-running" />
              ) : (
                <MinusCircleOutlined className="todo-card-icon" />
              )}
              <Typography.Text delete={todo.status === "completed"}>{todo.content}</Typography.Text>
              {todo.status === "in_progress" && todo.activeForm ? <Typography.Text type="secondary"> {todo.activeForm}</Typography.Text> : null}
            </div>
          ))}
        </div>
      </Space>
    </Card>
  );
}
