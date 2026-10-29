import type { Locale } from "../store/locale";
import { enCoreMessages } from "./messagesEnCore";
import { enProductMessages } from "./messagesEnProduct";
import { zhCoreMessages } from "./messagesZhCore";
import { zhProductMessages } from "./messagesZhProduct";
import { workDesktopMessages } from "../features/workdesktop/workDesktopMessages";

export const messages = {
  en: {
    ...enCoreMessages,
    ...enProductMessages,
    workDesktop: workDesktopMessages.en,
    settings: { ...enCoreMessages.settings, ...enProductMessages.settings },
  },
  zh: {
    ...zhCoreMessages,
    ...zhProductMessages,
    workDesktop: workDesktopMessages.zh,
    settings: { ...zhCoreMessages.settings, ...zhProductMessages.settings },
  },
} as const satisfies Record<Locale, unknown>;
