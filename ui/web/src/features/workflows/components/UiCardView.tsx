import { Button, Card, Form, Input, Select, Space, Typography } from "antd";
import { SendOutlined } from "@ant-design/icons";
import { MarkdownContent } from "../../../components/MarkdownContent";

const { Paragraph } = Typography;

/** Structured interactive card emitted by the ui_card tool (Step 4). */
export type UiCardField = {
  name: string;
  label?: string;
  type?: "text" | "textarea" | "select" | "number";
  required?: boolean;
  placeholder?: string;
  options?: Array<{ label: string; value: string }>;
};

export type UiCardAction = {
  id?: string;
  label: string;
  kind?: "message" | "command" | "approve" | "url";
  value?: string;
};

export type UiCardData = {
  kind: "form" | "button_group" | "card";
  title?: string;
  content?: string;
  fields?: UiCardField[];
  actions?: UiCardAction[];
};

type UiCardLabels = {
  cardTitle: string;
  cardSubmit: string;
  cardFieldRequired: string;
};

const DEFAULT_LABELS: UiCardLabels = {
  cardTitle: "Agent card",
  cardSubmit: "Submit",
  cardFieldRequired: "This field is required",
};

/**
 * UiCardView renders a structured interactive card emitted by the ui_card
 * tool: a JSON-Schema-like form, a button group, or a markdown card.
 * Submissions are forwarded via onSubmitCard so the host decides how to send
 * the structured input back to the agent.
 *
 * Standalone / embeddable: `labels` is optional and defaults to English, so
 * third-party UIs can drop this component in without godex's i18n system.
 */
export function UiCardView({
  card,
  submitting = false,
  onSubmitCard,
  labels,
}: {
  card: UiCardData;
  submitting?: boolean;
  onSubmitCard: (action: string) => void;
  labels?: Partial<UiCardLabels>;
}) {
  const [form] = Form.useForm<Record<string, string>>();
  const t = { ...DEFAULT_LABELS, ...labels };

  if (card.kind === "card" || (!card.fields?.length && !card.actions?.length)) {
    return (
      <Card size="small" title={card.title || t.cardTitle}>
        {card.content ? <MarkdownContent content={card.content} /> : null}
      </Card>
    );
  }

  if (card.kind === "button_group" || (card.actions?.length && !card.fields?.length)) {
    return (
      <Card size="small" title={card.title || t.cardTitle}>
        {card.content ? <Paragraph type="secondary">{card.content}</Paragraph> : null}
        <Space wrap>
          {(card.actions ?? []).map((action) => (
            <Button
              key={action.id ?? action.label}
              type="primary"
              ghost
              loading={submitting}
              onClick={() => onSubmitCard(action.value ?? action.label)}
            >
              {action.label}
            </Button>
          ))}
        </Space>
      </Card>
    );
  }

  // Form kind: render fields from the schema.
  return (
    <Card size="small" title={card.title || t.cardTitle}>
      {card.content ? <Paragraph type="secondary">{card.content}</Paragraph> : null}
      <Form form={form} layout="vertical" onFinish={(values) => onSubmitCard(JSON.stringify(values))}>
        {(card.fields ?? []).map((field) => (
          <Form.Item
            key={field.name}
            name={field.name}
            label={field.label || field.name}
            rules={field.required ? [{ required: true, message: t.cardFieldRequired }] : []}
          >
            {field.type === "textarea" ? (
              <Input.TextArea rows={3} placeholder={field.placeholder} />
            ) : field.type === "select" ? (
              <Select
                placeholder={field.placeholder}
                options={(field.options ?? []).map((option) => ({ label: option.label, value: option.value }))}
              />
            ) : field.type === "number" ? (
              <Input type="number" placeholder={field.placeholder} />
            ) : (
              <Input placeholder={field.placeholder} />
            )}
          </Form.Item>
        ))}
        <Button type="primary" htmlType="submit" icon={<SendOutlined />} loading={submitting}>
          {t.cardSubmit}
        </Button>
      </Form>
    </Card>
  );
}
