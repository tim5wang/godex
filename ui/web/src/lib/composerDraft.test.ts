import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearDraft, draftSignature, loadDraft, loadDraftFiles, saveDraft } from "./composerDraft";

/** Minimal in-memory localStorage + IndexedDB substitute so the module's
 *  storage layer can be exercised in Node without a browser or a dependency.
 *  Callbacks (req.onsuccess / tx.oncomplete) are fired synchronously to keep
 *  the async chains short. */
function installFakeBrowserStorage() {
  const store = new Map<string, string>();
  const storage = {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => store.clear(),
    key: (index: number) => [...store.keys()][index] ?? null,
    get length() {
      return store.size;
    },
  };
  (globalThis as Record<string, unknown>).localStorage = storage;
  // composerDraft.ts reads window.localStorage; in the Node test env there is
  // no window, so alias it to globalThis so the module sees the fake storage.
  (globalThis as Record<string, unknown>).window = globalThis;

  const blobs = new Map<string, Blob>();
  // Fire completion callbacks on a microtask so the real module's
  // `tx.oncomplete = ...` assignment (which happens after `.put()`/`.get()`)
  // is in place before the callback runs.
  const later = (fn: () => void) => queueMicrotask(fn);
  const fakeDB = {
    objectStoreNames: {
      contains: () => true,
    },
    createObjectStore: () => {},
    transaction: (_store: string, mode: string) => {
      const tx: Record<string, unknown> = { oncomplete: null, onerror: null };
      const objectStore = {
        put: (value: Blob, key: string) => {
          blobs.set(key, value);
          later(() => {
            if (typeof tx.oncomplete === "function") tx.oncomplete();
          });
          return { onsuccess: null, onerror: null };
        },
        get: (key: string) => {
          const req: Record<string, unknown> = { result: blobs.get(key), onsuccess: null, onerror: null };
          later(() => {
            if (typeof req.onsuccess === "function") req.onsuccess();
          });
          return req;
        },
        delete: (key: string) => {
          blobs.delete(key);
          later(() => {
            if (typeof tx.oncomplete === "function") tx.oncomplete();
          });
          return { onsuccess: null, onerror: null };
        },
      };
      (tx as { objectStore: unknown }).objectStore = (_name: string) => objectStore;
      return tx;
    },
    close: () => {},
  };
  const req: Record<string, unknown> = { result: fakeDB, onupgradeneeded: null, onsuccess: null, onerror: null, error: undefined };
  (globalThis as Record<string, unknown>).indexedDB = {
    open: () => {
      later(() => {
        if (typeof req.onupgradeneeded === "function") req.onupgradeneeded();
        if (typeof req.onsuccess === "function") req.onsuccess();
      });
      return req;
    },
  };
  return { blobs, storage };
}

function flush(): Promise<void> {
  // The fake store resolves transaction callbacks synchronously via
  // oncomplete; give the async chains a chance to settle.
  return Promise.resolve();
}

describe("composerDraft", () => {
  let blobs: Map<string, Blob>;

  beforeEach(() => {
    const env = installFakeBrowserStorage();
    blobs = env.blobs;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("draftSignature is stable for identical content", () => {
    const f1 = new File(["a"], "x.png", { type: "image/png", lastModified: 111 });
    const f2 = new File(["a"], "x.png", { type: "image/png", lastModified: 111 });
    expect(draftSignature("hi", [f1])).toBe(draftSignature("hi", [f2]));
  });

  it("draftSignature changes when text changes", () => {
    const f = new File(["a"], "x.png", { type: "image/png", lastModified: 111 });
    expect(draftSignature("hi", [f])).not.toBe(draftSignature("hi!", [f]));
  });

  it("round-trips text and file attachments", async () => {
    const scope = "session:s1";
    const file = new File(["img-bytes"], "photo.png", { type: "image/png", lastModified: 1234 });
    await saveDraft(scope, "hello draft", [file]);
    await flush();

    const draft = await loadDraft(scope);
    expect(draft).not.toBeNull();
    expect(draft?.text).toBe("hello draft");
    expect(draft?.files).toHaveLength(1);

    const restored = await loadDraftFiles(draft!.files);
    expect(restored).toHaveLength(1);
    expect(restored[0].name).toBe("photo.png");
    expect(restored[0].type).toBe("image/png");
    expect(restored[0].size).toBe("img-bytes".length);
  });

  it("loadDraft returns null when nothing was saved", async () => {
    expect(await loadDraft("session:missing")).toBeNull();
  });

  it("clearDraft removes text and file blobs", async () => {
    const scope = "session:s2";
    const file = new File(["x"], "a.txt", { type: "text/plain" });
    await saveDraft(scope, "draft", [file]);
    await flush();
    expect(await loadDraft(scope)).not.toBeNull();

    await clearDraft(scope);
    await flush();
    expect(await loadDraft(scope)).toBeNull();
    expect(blobs.size).toBe(0);
  });

  it("scopes are isolated", async () => {
    await saveDraft("session:a", "draft-a", []);
    await saveDraft("session:b", "draft-b", []);
    await flush();
    expect((await loadDraft("session:a"))?.text).toBe("draft-a");
    expect((await loadDraft("session:b"))?.text).toBe("draft-b");
    await clearDraft("session:a");
    await flush();
    expect(await loadDraft("session:a")).toBeNull();
    expect((await loadDraft("session:b"))?.text).toBe("draft-b");
  });
});
