// Per-session composer draft persistence.
//
// The unsent input text plus any attached files (images) must survive tab
// switches, app reloads, and session switches. Text is tiny, so it lives in
// localStorage; file payloads are binary and would overflow localStorage, so
// they live in IndexedDB keyed by a per-file random key referenced from the
// localStorage metadata.

const LS_PREFIX = "godex.composer.draft";
const DB_NAME = "godex-composer-draft";
const DB_STORE = "files";
const DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1000; // drop stale drafts after a week

interface DraftFileMeta {
  key: string;
  name: string;
  size: number;
  type: string;
  lastModified: number;
}

export interface ComposerDraft {
  text: string;
  files: DraftFileMeta[];
  updatedAt: number;
}

function lsKey(scope: string) {
  return `${LS_PREFIX}:${scope}`;
}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(DB_STORE)) {
        db.createObjectStore(DB_STORE);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error("indexedDB open failed"));
  });
}

async function dbPut(key: string, blob: Blob): Promise<void> {
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(DB_STORE, "readwrite");
      tx.objectStore(DB_STORE).put(blob, key);
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error ?? new Error("indexedDB put failed"));
    });
  } finally {
    db.close();
  }
}

async function dbGet(key: string): Promise<Blob | undefined> {
  const db = await openDB();
  try {
    return await new Promise<Blob | undefined>((resolve, reject) => {
      const tx = db.transaction(DB_STORE, "readonly");
      const req = tx.objectStore(DB_STORE).get(key);
      req.onsuccess = () => resolve(req.result as Blob | undefined);
      req.onerror = () => reject(req.error ?? new Error("indexedDB get failed"));
    });
  } finally {
    db.close();
  }
}

async function dbDelete(key: string): Promise<void> {
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(DB_STORE, "readwrite");
      tx.objectStore(DB_STORE).delete(key);
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error ?? new Error("indexedDB delete failed"));
    });
  } finally {
    db.close();
  }
}

/** Persist the current unsent draft. Best-effort: failures are swallowed so a
 *  full storage does not break typing. */
export async function saveDraft(scope: string, text: string, files: File[]): Promise<void> {
  if (!scope) return;
  try {
    const metas: DraftFileMeta[] = [];
    for (const file of files) {
      const key = `${scope}:${file.name}:${file.size}:${file.lastModified}:${Date.now()}:${Math.random().toString(36).slice(2)}`;
      await dbPut(key, file);
      metas.push({ key, name: file.name, size: file.size, type: file.type, lastModified: file.lastModified });
    }
    const draft: ComposerDraft = { text, files: metas, updatedAt: Date.now() };
    window.localStorage.setItem(lsKey(scope), JSON.stringify(draft));
  } catch {
    // Best-effort persistence; never break the input flow.
  }
}

/** Restore a previously saved draft for the given scope. Returns null when no
 *  draft exists (or it has expired). Files are reconstructed from IndexedDB. */
export async function loadDraft(scope: string): Promise<ComposerDraft | null> {
  if (!scope) return null;
  try {
    const raw = window.localStorage.getItem(lsKey(scope));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as ComposerDraft;
    if (!parsed || typeof parsed.text !== "string") return null;
    if (Date.now() - parsed.updatedAt > DRAFT_TTL_MS) {
      void clearDraft(scope);
      return null;
    }
    const files: DraftFileMeta[] = [];
    for (const meta of parsed.files ?? []) {
      const blob = await dbGet(meta.key);
      if (blob) {
        files.push(meta);
      } else {
        void dbDelete(meta.key);
      }
    }
    return { text: parsed.text, files, updatedAt: parsed.updatedAt };
  } catch {
    return null;
  }
}

/** Reconstruct real File objects from restored draft metadata. */
export function draftFilesToFiles(metas: DraftFileMeta[], blobs: Blob[]): File[] {
  return metas.map((meta, index) => new File([blobs[index]], meta.name, { type: meta.type, lastModified: meta.lastModified }));
}

/** Restore the actual File objects for a draft's file metadata. */
export async function loadDraftFiles(metas: DraftFileMeta[]): Promise<File[]> {
  const blobs = await Promise.all(metas.map((meta) => dbGet(meta.key)));
  const files: File[] = [];
  for (let index = 0; index < metas.length; index++) {
    const blob = blobs[index];
    if (blob) {
      const meta = metas[index];
      files.push(new File([blob], meta.name, { type: meta.type, lastModified: meta.lastModified }));
    } else {
      void dbDelete(metas[index].key);
    }
  }
  return files;
}

/** Drop a saved draft (after submit, on clear, or when expired). */
export async function clearDraft(scope: string): Promise<void> {
  if (!scope) return;
  try {
    const raw = window.localStorage.getItem(lsKey(scope));
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as ComposerDraft;
        for (const meta of parsed.files ?? []) {
          void dbDelete(meta.key);
        }
      } catch {
        // ignore malformed metadata
      }
      window.localStorage.removeItem(lsKey(scope));
    }
  } catch {
    // best-effort
  }
}

/** Stable fingerprint of the current composer content used to skip redundant
 *  writes. Derived from text plus each file's identity (name/size/lastModified),
 *  not file content, so it stays cheap for large images. */
export function draftSignature(text: string, files: File[]): string {
  const filePart = files
    .map((file) => `${file.name}:${file.size}:${file.lastModified}`)
    .sort()
    .join("|");
  return `${text}|${filePart}`;
}
