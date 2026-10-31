// Shared afterEach for both vitest projects (see vite.config.js).
//
// settings.ts debounces its ui-prefs save (600 ms) and retries its hydrate on a timer. A test that
// changes a setting leaves that timer behind; it then fires inside whichever test file runs next
// in the same worker, against that file's `api` mock (`missing apiJSON export`, or
// `Cannot read properties of undefined (reading 'then')`). Cancel them when each test ends.
//
// Cancelling silently, not failing: a change to any setting schedules a save, so a test that
// merely exercises a settings-backed component leaves one pending and is not at fault.
import { afterAll, afterEach, beforeEach } from "vitest";

const hooks = (key: string) =>
  (globalThis as any)[Symbol.for(key)] as Set<() => unknown> | undefined;
const cancelAll = () => hooks("af.prefsTimerHooks")?.forEach((cancel) => cancel());

// Cancelling also quiesces the sync module, so a timer armed after this point (an async tail of
// the test, an afterAll) is refused instead of outliving the file; beforeEach lifts it.
beforeEach(() => hooks("af.prefsTimerResumeHooks")?.forEach((resume) => resume()));
afterEach(cancelAll);
// End of file: cancel, then forget every instance. Nobody resumes a forgotten one, so a tail from
// this file can never arm a timer in a later file that shares the worker.
afterAll(() => {
  cancelAll();
  hooks("af.prefsTimerHooks")?.clear();
  hooks("af.prefsTimerResumeHooks")?.clear();
});
