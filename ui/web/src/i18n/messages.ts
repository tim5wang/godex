import type { Locale } from "../store/locale";
import { enCoreMessages } from "./messagesEnCore";
import { enProductMessages } from "./messagesEnProduct";
import { zhCoreMessages } from "./messagesZhCore";
import { zhProductMessages } from "./messagesZhProduct";

export const messages = {
  en: {
    ...enCoreMessages,
    ...enProductMessages,
  },
  zh: {
    ...zhCoreMessages,
    ...zhProductMessages,
  },
} as const satisfies Record<Locale, unknown>;
