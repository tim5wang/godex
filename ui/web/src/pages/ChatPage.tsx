import { ChatPage } from "../features/chat/ChatPage";

export { ChatPage };

/** Chat V2 entry: renders the same ChatPage with the v2 layout. */
export function ChatV2Page() {
  return <ChatPage view="v2" />;
}
