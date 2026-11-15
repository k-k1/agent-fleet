import { useMemo, useRef } from "react";

// useActiveFlow tracks the device sign-in a card is showing. A poll whose flow is no longer the
// active one must stop and change nothing: the Agent installs a staged sign-in on the poll that
// sees it finish, so a poll left running after Cancel would replace the login the member chose
// to keep as soon as the code was approved somewhere.
export function useActiveFlow() {
  const active = useRef<string | null>(null);
  return useMemo(
    () => ({
      begin: (id: string) => {
        active.current = id;
      },
      end: () => {
        active.current = null;
      },
      is: (id: string) => active.current === id,
    }),
    [],
  );
}
