# 0111. A folder downloads as one zip: built into a temp file, opened beneath a root fd, every limit refused with 413

English | [日本語](0111-folder-zip-download.ja.md)

- Status: **accepted** (2026-10-07). Built (Agent, Control Plane route, Console menus on both folder
  surfaces). Verified by Go and Console tests only — **not driven in a real browser, on iOS or on
  Android**; see "Not verified".
- Tracking: #1829
- Follow-ups: #1844
- Related: [0080](0080-image-gallery-pane.md) (the gallery's folder card and its menu; the
  `GET /fs/images` walk whose bounds this reuses) /
  [0063](0063-document-preview.md) (`api/fs/download` is the raw-bytes door for one file) /
  [0046](0046-drawio-viewer.md) (the viewers read bytes through that door) /
  [0081](0081-image-generation-pane.md) (generated folders are the common thing to download)

## Context

A single file can be downloaded from the Files tree (`api/fs/download`); a whole folder means one
file at a time. The folder menus in the Files tree and in the image gallery had no such item.

A folder export is a different animal from one file. It is the first endpoint that reads an
unbounded number of files for one request, so it has to be bounded on every axis; it reads files
on behalf of a walk that is not atomic, so it has to say what a "successful" archive promises; and
the file downloads' defence (`openat2` with `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`,
`fs_fd_linux.go`) is per file, whereas `GET /fs/images` walks with plain `os.Open` and only skips
symlinks at listing time (`fs_images.go`) — fine for a listing of names, not for reading bytes.

## Decisions

### 1. The archive is built into a temp file first, then served with `http.ServeContent`

`GET /fs/download-zip?path=<folder>` walks, builds a `zip.Store` archive into an **unlinked temp
file**, and serves that. Every failure — a limit, a hostile name, a file that changed under the
walk — is a plain JSON error **before the first body byte**, and a success carries a
`Content-Length`. The streaming alternative has to choose, once the 200 is on the wire, between a
truncated zip that looks finished and a cut connection; and `control-plane/proxy.go` relays the
body with `io.Copy` and ignores its error, so a cut agent could read as a normal end. With a
declared `Content-Length` the browser sees a short body and fails the download (pinned by
`TestFSDownloadZipCutUpstreamIsAnErrorNotAShortArchive`).

- **Where.** `<AgentStateDir>/zip-export/` (`~/.local/state/agent-fleet`): the home volume on every
  runtime, not `/tmp` (a tmpfs on ecs-ec2, an `emptyDir` with a size limit on kubernetes —
  `docs/build/07-security.md` §7.2). It is under the browse denylist, so an archive in progress
  cannot itself be downloaded. Mode 0700.
- **Cleanup.** The file is unlinked immediately after it is created, so closing the descriptor —
  on every return path, and on a crash after that point — frees it. A sweep of `zip-*` files runs
  before each create, under the slot below, for the one window a crash can leave (between create
  and unlink).
- **Quota.** The payload cap plus the entry overhead the name/entry caps bound (`zipOutputCap`);
  a write past it stops the build with 413, `ENOSPC` is 507.
- **Range.** Each request builds its own archive, so a byte range of one is not a range of the
  next. `Range` and `If-Range` are dropped before `ServeContent`, and no validator (`ETag`,
  `Last-Modified`) is sent, so a browser cannot resume against different bytes.
- **`Content-Type: application/zip`**, `Cache-Control: private, no-store`, and
  `Content-Disposition` with an ASCII `filename=` fallback plus `filename*=UTF-8''…`.
- **Compression: none** (`zip.Store`). Pictures and existing archives do not recompress, and there
  are no compression workers to bound.

### 2. Everything is opened beneath a trusted root fd, and only there

The root fd is the browse root (or the scratch / staged-docs root for an absolute path under it),
opened with `O_NOFOLLOW`. The start folder, every child folder and every file is then opened
relative to it with `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)`, **at the moment it is used**,
so a path swapped for a symlink after the walk fails the open instead of reading through it
(`TestZipSwapAfterWalk…`, five swaps).

- **A symlink anywhere in the start path** (including the final component) is refused: 400
  `symlink_not_allowed`. No alias is resolved for the user.
- **A symlink or special file inside the folder** (FIFO, socket, device) is left out and **counted**;
  the check reports the count. A FIFO is never opened, so it cannot block the walk.
- **The `.codex/generated_images` single-image exception is not inherited.** The path resolver is
  its own (`resolveZipRoot`), not `resolveFDReadPath`: the exception is a reader for one announced
  image, not a licence to enumerate a private state tree. Such a path is 403 `denied`.
- **The denylist is applied by the browse-relative path of each child and prunes before
  descending** (`.local` is exportable and omits `.local/share/agent-fleet`). The start's own
  path is checked too. Because no symlink is ever followed, the browse-relative spelling is the
  real one — the alias problem `imagesDenyBase` solves for listings cannot arise.
- **A root itself is not exportable** (browse root, scratch root, staged-docs root): 400. "Download
  my home" is never what a menu press meant and is the one request that always hits a limit.
  A folder *inside* the scratch or docs roots is allowed (read-only roots, no denylist).

### 3. What goes into the archive

- Entry names are `<folder>/…`: one top-level folder, so extracting does not spill into the
  current directory. Entry names are `/`-separated and relative.
- Empty folders are kept (`name/` entries). All file types, hidden files included, regardless of
  the gallery's image filter.
- File permission bits (`& 0777`) and mtime are kept; nothing else (no owner, no xattrs).
- **`.git` and `node_modules` below the start folder are left out**, by name, at any depth. A start
  folder that **is** one of them is exported in full: the user named it. The check lists the names
  actually left out, and the item's tooltip says so before the press.
- Denylisted folders under the browse root are left out (they appear in the same list).
- **A name that cannot be stored fails the whole export with 422 `unsafe_name`** and names the
  offender: invalid UTF-8, a backslash, a control character, an empty/`.`/`..` component, an
  absolute path, a leading drive prefix (`C:`), a duplicate. It is never rewritten into another
  name and never dropped — either would be a silent change to what the user asked for.
  (A colon elsewhere in a name is legal and common, and is kept.)

### 4. What "successful" promises: not a snapshot, never short

The walk's sizes are listing-time values and only an early-rejection optimisation. The build reads
each file once, when its turn comes. So:

| Event after the walk | Result |
|---|---|
| a file grows | the bytes read are packed; the **byte cap is enforced on bytes read**, 413 if exceeded |
| a file shrinks | the bytes read are packed |
| a file is replaced by another regular file | the new file's bytes are packed |
| a file or folder vanishes, or becomes a symlink, a FIFO or a folder | 409 `changed_during_zip` ("try again") |
| a file cannot be read (`EACCES`, I/O error) | 403 `denied` / 500 `read_failed` for the whole request |

The archive is therefore **not a point-in-time snapshot** (different files are from different
moments, and a file being written may be caught mid-write), and it is **never a silently shorter
one**: anything selected either goes in or fails the request. The documented exclusions of
decision 3 are the only omissions.

### 5. Every axis is bounded, and one slot is held for the whole request

| Bound | Value | At it |
|---|---|---|
| files | 20 000 | 413 |
| folders | 4 000 | 413 |
| depth | 32 | 413 |
| entries looked at (kept or not) | 100 000 | 413 |
| summed entry-name bytes | 4 MiB | 413 |
| bytes read | 512 MiB | 413 |
| bytes written (payload cap + overhead) | derived | 413 |
| walk + build time | 45 s | 413 |
| transfer time | 15 min (write deadline) | connection closed |
| concurrent builds, Agent-wide | 1 (waits 2 s) | 503 `zip_busy` |

Measured on 2026-10-07 on a warm local disk in a workspace (the Go tests' walk, not a deployment):
the check of 20 000 files of 1 KiB took 0.2 s, building their 23 MB archive 0.8 s, and one 300 MiB
file 1.2 s. The ceilings are therefore set for cold network storage, not for that case, and **how
close a cold EFS or EBS volume comes to 45 s is not measured**. The 45 s is below the ALB's 60 s idle timeout: the build is silent until it finishes, and the
first byte must be out before the ALB gives up. A directory is read in batches of 256, one
directory fd is open at a time, and copies use one 64 KiB buffer. `zip.Writer` keeps every
entry's central record in memory; 24 000 entries is the bound on that. Over-limit folders are
refused with 413 and a message naming the limit, never truncated.

The **slot is taken before the walk and released after the transfer and the temp file's close** (a
`defer`), so a slow client downloading its archive is exactly the case in which a second build
cannot pile on. A request that cannot get the slot within 2 s answers 503; a client that left
while waiting releases nothing. The transfer deadline is set through `http.ResponseController`,
which reaches the connection only because `httpx.Gzip`'s wrapper now implements `Unwrap`.

### 6. The Console checks first, then downloads with a plain anchor

A bare `<a href download>` cannot show a 413, 503, 401, 403, 404 or a stopped workspace, the
fetch-based session-expiry notice does not run for it, and an expired session may redirect the tab
to the login page. So a press does:

1. `GET api/fs/download-zip?path=…&check=1` over `fetch` — the same route and the same walk, no
   build. It answers `{name, files, dirs, bytes, excluded[], skipped}` or the error envelope. A
   refusal becomes a toast with the localized `err.<code>` text plus the server's detail (which
   limit).
2. On success, a hidden same-origin anchor with `download` is clicked and a toast says what is in
   it and what was left out. The browser streams the file to disk; the Console never reads the zip
   into a Blob.

Rejected: a "prepare, then fetch a token" API (server state to expire and clean for no gain over
check-then-download) and reading the response into a Blob (memory, and no progress for the
browser's own download manager). The residual race — the folder grows between the check and the
download — is a failed download in the browser, not a short archive. The check costs one extra
walk (stats only, bounded the same way); it takes the same slot.

The tenant is captured before the check and the URL built for it; a switch while the check is out
drops the result silently.

### 7. Touch: an explicit long-press on both folder surfaces

iOS Safari raises no `contextmenu` on a long press of a plain element, so neither the tree's folder
rows nor the gallery's folder cards had a touch way to the menu. `useLongPressMenu` (on
`createLongPress`) opens the same menu after 500 ms, cancels on movement over 10 px, `touchend` and
`touchcancel`, ignores a mouse `pointerdown`, and swallows the lift's click so the press does not
also open the folder. Android may deliver a native `contextmenu` for the same press; a
touch-derived one marks the click as swallowed, a mouse right click does not. The "Up" card has no
menu and no long-press. Only folder rows/cards are wired; file rows are unchanged.

### 8. Audit: none, for now

Ordinary filesystem GETs are not audited (`control-plane/proxy.go`); memory export is the exception
because it carries personal data out of the environment. A folder export is a bulk read of the
member's own workspace by the member, through the same session as every file read, and the Agent's
access log already records the request. Whether a start-time or completion-time record is wanted
is left open (Follow-ups on the Status line).

## Consequences

- A folder under the limits downloads as `<folder>.zip` from either surface, by right-click, the
  menu key or a touch long-press. A folder over any limit says which, instead of failing quietly.
- The Agent gains one route and one 512 MiB-bounded temp file at a time on the home volume; the
  Control Plane gains one allowlist line and relays headers and body as for any download.
- `httpx.Gzip`'s writer can be unwrapped. Nothing else about compression changes (an archive is
  not compressible content).
- The exclusions are by name and apply at any depth; a project that keeps real source in a
  directory called `node_modules` must select that directory itself.

## Not verified

Go: the walk, every cap's boundary, the swaps, symlinks, special files, hostile names, slot
release (success, refusal, vanished client, busy), temp cleanup, headers, and the relay (headers,
exact bytes, URL encoding, a cut upstream, a vanished browser, no-identity and foreign-tenant
refusals). Console: the URL, the check/download/toast logic, the menu items and the long-press
state machine under jsdom.

**Not verified in a real browser, on iOS or on Android**: that the programmatic anchor click after
an `await` is accepted as a download (it should be: it is a same-origin `download` link, not a
popup), that a long press raises no native callout or selection that competes with it, and that
the saved file opens in the platform's unzip. jsdom cannot prove a native download.
