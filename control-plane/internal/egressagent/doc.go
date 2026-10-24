// Package egressagent is the small agent on the egress gateway (ADR 0122, decision 4). It
// connects Squid to the control plane and does two things:
//
//   - Policy. It polls GET /internal/egress/policy and writes what Squid reads: the allow
//     file (one dstdomain entry per line) and the mode include (log-only or enforce). It
//     reloads Squid when a file's bytes changed. When the CP is unreachable the files
//     on disk stay as they are, so Squid keeps the last policy that worked. It also reloads
//     once at start: a previous run may have written the files and died before its reload.
//   - Log. It reads Squid's access log from a stored offset and POSTs each line to
//     /internal/egress as one event. The event id is the line's position (file identity
//     and byte offset), so a resend is stored once by the CP, and the offset is committed
//     only after a 2xx. Nothing is held only in memory: a crash re-reads from the last
//     committed offset.
//
// Run as `control-plane egress-agent`.
//
// # The Squid configuration this agent expects
//
// The agent owns the log format and the two generated files; the rest of squid.conf is
// the deployment's. The lines that tie them together (paths are the defaults):
//
//	acl af_allowed dstdomain "/etc/squid/af-allow.txt"
//	acl af_allowed_sni ssl::server_name "/etc/squid/af-allow.txt"
//	acl af_step1 at_step SslBump1
//	include /etc/squid/af-mode.conf
//	logformat af_egress af1 %tS %>a %<a %Ss %ssl::bump_mode %#ssl::>sni %#>rd
//	access_log stdio:/var/log/squid/af-egress.log af_egress
//	logfile_rotate 10
//
// The include sits where the ssl_bump and http_access decisions go; ModeLogOnly and
// ModeEnforce are its two contents.
//
// The log format, field by field (Squid writes "-" for a value it does not have):
//
//	af1               format version; a line with any other first field is not ours
//	%tS               when the connection started, seconds.milliseconds. Not %ts, which
//	                  is when the line was written: a tunnel is logged when it closes,
//	                  possibly after the workspace released its address, and attribution
//	                  joins on the time the workspace held it.
//	%>a               client (workspace) address: the source attribution joins on
//	%<a               address of the server Squid connected to
//	%Ss               Squid's request status, e.g. TCP_TUNNEL, TCP_DENIED
//	%ssl::bump_mode   splice, terminate, peek, ... for a TLS connection
//	%#ssl::>sni       TLS SNI, URL-encoded so a hostile SNI cannot add fields
//	%#>rd             request domain (Host header or CONNECT target), URL-encoded
//
// The agent tags each log file it reads with a random generation id in the extended
// attribute user.af-egress.gen, and an event's id is "<generation>@<offset>". It therefore
// needs write access to Squid's log files (run it as Squid's user, or in its group with the
// files group-writable) and a filesystem with user xattrs; it refuses to start otherwise.
//
// Rotation must be Squid's own (`squid -k rotate` with logfile_rotate >= 1, which renames
// the file to .0, .1, ...): the agent finds a rotated file by its identity and finishes
// it. copytruncate is not supported; the lines written between the copy and the truncate
// are never seen.
//
// None of this was run against a real Squid: the format codes and the ssl_bump lines are
// read from Squid's documentation, and the parser is tested with fixture lines.
package egressagent
