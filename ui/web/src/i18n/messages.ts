import type { Locale } from "../store/locale";
import { enCoreMessages } from "./messagesEnCore";
import { enProductMessages } from "./messagesEnProduct";
import { zhCoreMessages } from "./messagesZhCore";
import { zhProductMessages } from "./messagesZhProduct";

export const messages = {
  en: {
    ...enCoreMessages,
    ...enProductMessages,
    settings: { ...enCoreMessages.settings, ...enProductMessages.settings },
  },
  zh: {
    ...zhCoreMessages,
    ...zhProductMessages,
    settings: { ...zhCoreMessages.settings, ...zhProductMessages.settings },
  },
} as const satisfies Record<Locale, unknown>;
