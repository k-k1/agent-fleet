// Memo attachment names. Stored names are paste-<n>.<ext> for an image and
// paste-<n>-<sanitized original> for any other file (SavePastedImageTo in
// workspace/agent/internal/sessionx/session_paste.go); the UI tells the two apart by name
// because a memo records nothing else about its files.
const IMAGE_NAME = /^paste-\d+\.(png|jpe?g|gif|webp)$/i;
const PREFIX = /^paste-\d+-/;

/** isMemoImageName tells an image attachment (thumbnail) from any other file (icon + name). */
export const isMemoImageName = (name: string): boolean => IMAGE_NAME.test(name);

/** memoAttachmentLabel is what a person should read for a stored attachment: the original
 *  file name for a non-image, the stored basename for an image (it has no other). */
export const memoAttachmentLabel = (name: string): string => (isMemoImageName(name) ? name : name.replace(PREFIX, "") || name);
