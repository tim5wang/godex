import { APIError } from "./api";

type MessageApi = {
  error: (content: string) => unknown;
};

export function showError(messageApi: MessageApi, error: unknown, fallback: string) {
  const text = error instanceof APIError ? error.message : error instanceof Error ? error.message : fallback;
  void messageApi.error(text);
}
