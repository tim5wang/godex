import type { Locale } from "../store/locale";
import { enCoreMessages } from "./messagesEnCore";
import { enProductMessages } from "./messagesEnProduct";
import { zhCoreMessages } from "./messagesZhCore";
import { zhProductMessages } from "./messagesZhProduct";
import { workDesktopMessages } from "../features/workdesktop/workDesktopMessages";
import { codexMessages } from "./messagesCodex";

export const messages = {
  en: {
    ...enCoreMessages,
    ...enProductMessages,
    workDesktop: workDesktopMessages.en,
    codex: codexMessages.en,
    settings: { ...enCoreMessages.settings, ...enProductMessages.settings },
  },
  zh: {
    ...zhCoreMessages,
    ...zhProductMessages,
    workDesktop: workDesktopMessages.zh,
    codex: codexMessages.zh,
    settings: { ...zhCoreMessages.settings, ...zhProductMessages.settings },
  },
} as const satisfies Record<Locale, unknown>;
