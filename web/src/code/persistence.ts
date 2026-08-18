import type { PersistedWorkspaceSession } from "./types";

const databaseName = "echo-code-editor";
const storeName = "workspace-sessions";

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, 1);
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains(storeName)) {
        request.result.createObjectStore(storeName);
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error("Unable to open editor storage"));
  });
}

export async function loadSession(workspaceId: string): Promise<PersistedWorkspaceSession | null> {
  const database = await openDatabase();
  try {
    return await new Promise((resolve, reject) => {
      const request = database.transaction(storeName).objectStore(storeName).get(workspaceId);
      request.onsuccess = () => resolve((request.result as PersistedWorkspaceSession | undefined) || null);
      request.onerror = () => reject(request.error);
    });
  } finally {
    database.close();
  }
}

export async function saveSession(workspaceId: string, session: PersistedWorkspaceSession): Promise<void> {
  const database = await openDatabase();
  try {
    await new Promise<void>((resolve, reject) => {
      const transaction = database.transaction(storeName, "readwrite");
      transaction.objectStore(storeName).put(session, workspaceId);
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
      transaction.onabort = () => reject(transaction.error || new Error("Editor session write was aborted"));
    });
  } finally {
    database.close();
  }
}

export async function clearSessions(): Promise<void> {
  const database = await openDatabase();
  try {
    await new Promise<void>((resolve, reject) => {
      const transaction = database.transaction(storeName, "readwrite");
      transaction.objectStore(storeName).clear();
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error);
    });
  } finally {
    database.close();
  }
}

export async function hasDirtySessions(): Promise<boolean> {
  const database = await openDatabase();
  try {
    return await new Promise<boolean>((resolve, reject) => {
      const request = database.transaction(storeName).objectStore(storeName).getAll();
      request.onsuccess = () => {
        const sessions = request.result as PersistedWorkspaceSession[];
        resolve(sessions.some((session) => session.tabs.some((tab) => tab.dirty)));
      };
      request.onerror = () => reject(request.error);
    });
  } finally {
    database.close();
  }
}
