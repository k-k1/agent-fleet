package sessionx

// endRouteTokens ends a session's route gateway tokens at once (ADR 0128 decision 7, A2). Every
// lifecycle exit that ends a runtime or changes the selection calls it: halt (and with it every
// stop, delete, move and route-switch stop), archive, recreate, the trash, an orphan's kill, a
// driver switch, the route switch's store and rollback, and the list poll that finds a pane
// gone. Without it a token would wait for its next call's check or the gateway's expiry; the
// caller binding already refuses a call from outside the session's process tree, so this
// closes the window in which a still-running descendant of an ended pane could use it.
func endRouteTokens(name string) { deps.EndRouteTokens(name) }
