import { create } from "zustand";

/** How the stored credential was obtained. It matters for what the UI can offer:
 *  a session token can be revoked with logout, the shared web token cannot. */
export type AuthMode = "none" | "session" | "web-token";

interface SettingsState {
  /** token is the ACTIVE credential sent on every request. It holds either a
   *  login session token (gxs_...) or the shared web token, depending on how the
   *  operator signed in. Kept as one field so every existing call site keeps
   *  working unchanged: the request layer only ever needs "the credential". */
  token: string;
  authMode: AuthMode;
  defaultSessionKey: string;
  setToken: (token: string) => void;
  setSessionToken: (token: string) => void;
  setDefaultSessionKey: (key: string) => void;
  clearToken: () => void;
  clearAll: () => void;
}

const tokenKey = "godex:web:token";
const authModeKey = "godex:web:auth-mode";
const defaultSessionKeyKey = "godex:web:default-session-key";

const storedToken = typeof window === "undefined" ? "" : window.localStorage.getItem(tokenKey) ?? "";
const storedAuthMode = typeof window === "undefined" ? "" : window.localStorage.getItem(authModeKey) ?? "";
const storedDefaultSessionKey = typeof window === "undefined" ? "" : window.localStorage.getItem(defaultSessionKeyKey) ?? "";

function persistToken(token: string, mode: AuthMode) {
  window.localStorage.setItem(tokenKey, token);
  window.localStorage.setItem(authModeKey, mode);
}

export const useSettingsStore = create<SettingsState>((set) => ({
  token: storedToken,
  // A credential stored before this field existed is treated as the web token:
  // that is what the only previous sign-in method produced, so existing
  // deployments keep working without a migration step.
  authMode: (storedAuthMode as AuthMode) || (storedToken ? "web-token" : "none"),
  defaultSessionKey: storedDefaultSessionKey,
  setToken: (token) => {
    persistToken(token, token ? "web-token" : "none");
    set({ token, authMode: token ? "web-token" : "none" });
  },
  setSessionToken: (token) => {
    persistToken(token, token ? "session" : "none");
    set({ token, authMode: token ? "session" : "none" });
  },
  setDefaultSessionKey: (defaultSessionKey) => {
    window.localStorage.setItem(defaultSessionKeyKey, defaultSessionKey);
    set({ defaultSessionKey });
  },
  clearToken: () => {
    window.localStorage.removeItem(tokenKey);
    window.localStorage.removeItem(authModeKey);
    set({ token: "", authMode: "none" });
  },
  clearAll: () => {
    window.localStorage.removeItem(tokenKey);
    window.localStorage.removeItem(authModeKey);
    window.localStorage.removeItem(defaultSessionKeyKey);
    set({ token: "", authMode: "none", defaultSessionKey: "" });
  },
}));
