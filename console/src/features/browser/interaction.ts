/** True for an event the browser generated from a person's input. A script-dispatched
 *  event (or any automation) reports false, and must not count as presence. Its own
 *  module only so a test can stand in for a trusted event: jsdom's isTrusted cannot be
 *  redefined. */
export function isHumanEvent(event: { isTrusted: boolean }): boolean {
  return event.isTrusted;
}
