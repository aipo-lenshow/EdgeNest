// Module-level store(s) for bulk-delete loops, so the "deleting… (x/n)"
// progress survives leaving and re-entering a page (the loop + state live
// outside the React tree, not in the component that unmounts on route change).
// NOTE: a full browser refresh still resets this — the loop is in-memory JS,
// not server-tracked; surviving F5 would need a backend bulk-delete endpoint
// (see BUGLOG 0-17 option ②), which we deliberately didn't build.
//
// createBulkDeleteStore is a factory: each entity (inbounds, subscriptions, …)
// gets its own independent store, so they can each drive an in-page progress
// animation and never need to hold a confirm dialog open until the loop ends.

import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { api, call } from "../api/client";

export interface BulkDeleteState {
  busy: boolean;
  done: number;
  total: number;
  failed: number;
  firstErr: string;
}

export interface BulkDeleteStore {
  subscribe: (listener: () => void) => () => void;
  getState: () => BulkDeleteState;
  run: (ids: number[], qc: QueryClient) => Promise<void>;
}

const IDLE: BulkDeleteState = {
  busy: false,
  done: 0,
  total: 0,
  failed: 0,
  firstErr: "",
};

export function createBulkDeleteStore(opts: {
  deletePath: (id: number) => string;
  queryKey: QueryKey;
}): BulkDeleteStore {
  let state: BulkDeleteState = IDLE;
  const listeners = new Set<() => void>();

  function set(next: BulkDeleteState) {
    state = next;
    listeners.forEach((l) => l());
  }

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getState() {
      return state;
    },
    // Deletes each id sequentially, publishing progress to subscribers as it
    // goes, then invalidates the query so any mounted list refreshes. qc is the
    // app-global QueryClient, so invalidation works even if the page that
    // started the delete has since unmounted. Refuses to start a second run
    // while one is in flight.
    async run(ids, qc) {
      if (state.busy || ids.length === 0) return;
      set({ busy: true, done: 0, total: ids.length, failed: 0, firstErr: "" });
      let failed = 0;
      let firstErr = "";
      for (let i = 0; i < ids.length; i++) {
        try {
          await call(api.delete(opts.deletePath(ids[i])));
        } catch (e: any) {
          failed += 1;
          if (!firstErr) firstErr = e?.message ?? "delete failed";
        }
        set({ busy: true, done: i + 1, total: ids.length, failed, firstErr });
        qc.invalidateQueries({ queryKey: opts.queryKey });
      }
      set({ busy: false, done: ids.length, total: ids.length, failed, firstErr });
      qc.invalidateQueries({ queryKey: opts.queryKey });
    },
  };
}

export const inboundBulkDelete = createBulkDeleteStore({
  deletePath: (id) => `/inbounds/${id}`,
  queryKey: ["inbounds"],
});

export const subscriptionBulkDelete = createBulkDeleteStore({
  deletePath: (id) => `/subscriptions/${id}`,
  queryKey: ["subscriptions"],
});
