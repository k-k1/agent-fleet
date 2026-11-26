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
  close(): void;
}

export const useSpaceLaunch = create<SpaceLaunchStore>((set) => ({
  request: null,
  open: (request) => set({ request }),
  close: () => set({ request: null }),
}));
