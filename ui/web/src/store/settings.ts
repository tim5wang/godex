import { create } from "zustand";

interface SettingsState {
  token: string;
  defaultSessionKey: string;
  setToken: (token: string) => void;
  setDefaultSessionKey: (key: string) => void;
  clearToken: () => void;
  clearAll: () => void;
}

const tokenKey = "godex:web:token";
const defaultSessionKeyKey = "godex:web:default-session-key";

const storedToken = typeof window === "undefined" ? "" : window.localStorage.getItem(tokenKey) ?? "";
const storedDefaultSessionKey = typeof window === "undefined" ? "" : window.localStorage.getItem(defaultSessionKeyKey) ?? "";

export const useSettingsStore = create<SettingsState>((set) => ({
  token: storedToken,
  defaultSessionKey: storedDefaultSessionKey,
  setToken: (token) => {
    window.localStorage.setItem(tokenKey, token);
    set({ token });
  },
  setDefaultSessionKey: (defaultSessionKey) => {
    window.localStorage.setItem(defaultSessionKeyKey, defaultSessionKey);
    set({ defaultSessionKey });
  },
  clearToken: () => {
    window.localStorage.removeItem(tokenKey);
    set({ token: "" });
  },
  clearAll: () => {
    window.localStorage.removeItem(tokenKey);
    window.localStorage.removeItem(defaultSessionKeyKey);
    set({ token: "", defaultSessionKey: "" });
  },
}));
