// Chat/assistant feature endpoints. Implementations live in src/api.ts during
// the transition (shared with the frozen console); absorbed at swap (P8).
export {
  chatList,
  chatCreate,
  chatGet,
  chatRename,
  chatSetAgent,
  chatSuggestTitle,
  chatSuggestReplies,
  chatDelete,
  chatSetLock,
  chatSend,
  chatStream,
  chatStop,
  chatCompact,
  chatSetPlan,
  chatRefreshPlan,
  chatPasteImage,
  assistantList,
  assistantGet,
  assistantCreate,
  assistantUpdate,
  assistantDelete,
  askAssistant,
} from "../../core/api/client.ts";
export type { ChatCreateOpts, ChatStreamHandlers } from "../../core/api/client.ts";
