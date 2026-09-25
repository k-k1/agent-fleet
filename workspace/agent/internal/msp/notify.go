package msp

// DeclaredNotification reports whether the stable-surface schema declares this notification,
// and names the Go type of its params.
//
// It is a decode map, never an allow-list. Measured on 1.3.0-R3401.1, the host emits
// `session/started` before the `session/start` response although that bundle does not declare
// it (1.4.0-R4161.1 declares it and `session/closed`). A dispatcher that refused an undeclared
// method would reject real traffic on the first call of every session against an older host,
// and a newer host can outrun the bundle the same way. Drop what you cannot decode; do not
// treat it as a protocol error.
func DeclaredNotification(method string) (paramsType string, declared bool) {
	t, ok := notificationParams[method]
	return t, ok
}

// DeclaredNotifications returns every notification name the schema declares. The drift test
// uses it; a driver uses DeclaredNotification.
func DeclaredNotifications() []string {
	out := make([]string, 0, len(notificationParams))
	for k := range notificationParams {
		out = append(out, k)
	}
	return out
}
