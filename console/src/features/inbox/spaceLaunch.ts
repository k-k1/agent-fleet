// The inbox request a space launch is open for. The dialog is hosted at the inbox's top level
// (SpaceLaunchHost), not under the request's card: a card unmounts when the request changes under
// it (the sender withdraws it, another tab takes it), and the dialog with it would drop the text
// and files the member was editing. The request travels here and as props, never through the
// global launch seed, which any other launch may read.
import { create } from "zustand";

export interface SpaceLaunchRequest {
  id: string;
  body: string;
}

interface SpaceLaunchStore {
  request: SpaceLaunchRequest | null;
  open(r: SpaceLaunchRequest): void;
  /** Closes the dialog. With `only`, just when that request is still the open one: a late
   *  completion of an earlier launch (its inbox was replaced by a tenant switch) must not close
   *  the dialog the member has since opened for another request. */
  close(only?: SpaceLaunchRequest): void;
}

export const useSpaceLaunch = create<SpaceLaunchStore>((set) => ({
  request: null,
  open: (request) => set({ request }),
  close: (only) => set((s) => (only && s.request !== only ? s : { request: null })),
}));
