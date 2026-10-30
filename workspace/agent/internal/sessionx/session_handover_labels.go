package sessionx

// The handover file's own words: headings, bullet labels and fixed sentences written by
// renderHandover. The conversation text and the conversation's labels are not here (the latter
// follow the same lang through transcript.MarkdownOptions.Lang). The language is the one the
// export uses: uiprefs.Locale(), "ja" or "en"; RenderHandover maps anything else to "en".

type handoverLabels struct {
	Title, Source, Agent, Folder, Branch, Moved, Earlier, Instructions string
	Intro                                                              string

	Stopped, CutOff, LastUser, LastAssistant, Question string
	Queued, QueuedAttachment                           string
	Discarded                                          string
	DiscardReasons                                     map[string]string // agents.Discard* → words
	Peers, PeerFrom                                    string            // PeerFrom: format of from, intent

	WorkingCopy, NotGit, CommitsAhead, None string

	Changed, NoneRecorded, EditsFmt, SubagentOnly string

	Threads, ChildFmt, ProposalFmt, ThreadsNote string

	Conversation string
	// CutMarker ends an assistant text the size rule shortened, so the target knows the rest
	// existed.
	CutMarker string
}

var handoverLabelsEn = handoverLabels{
	Title:   "Handover: %s",
	Source:  "Source session",
	Agent:   "Agent",
	Folder:  "Working folder",
	Branch:  "Branch",
	Moved:   "Moved",
	Earlier: "Earlier history (same folder as this file)",
	Instructions: "Project instructions the source's agent followed and yours does not load " +
		"(read it yourself)",
	Intro: "This file is the whole record of the source session: work from it, " +
		"and do not open the source's own session files or its CLI's state directories.",

	Stopped:          "Where it stopped",
	CutOff:           "The last turn was cut off by the agent's usage limit.",
	LastUser:         "Last user message:",
	LastAssistant:    "Last assistant text:",
	Question:         "Unanswered question:",
	Queued:           "Instructions queued but never delivered:",
	QueuedAttachment: "attachment",
	Discarded:        "Inputs a stop threw away (kept so they could be taken back, never delivered):",
	DiscardReasons: map[string]string{
		"second_stop":   "second stop",
		"discard_queue": "queue discarded",
		"first_stop":    "first stop, before it was sent",
	},
	Peers:    "Peer messages waiting for the source:",
	PeerFrom: "from %s (intent: %s)",

	WorkingCopy:  "Working copy",
	NotGit:       "(not a git working copy, or git could not be read)",
	CommitsAhead: "Commits ahead of upstream",
	None:         "(none)",

	Changed:      "Changed files",
	NoneRecorded: "(none recorded)",
	EditsFmt:     ", %d edits",
	SubagentOnly: ", by a subagent only",
	Threads:      "Open threads",
	ChildFmt:     "- child session `%s`: %s\n",
	ProposalFmt:  "- pending handoff proposal: `%s`\n",
	ThreadsNote:  "These still belong to the source session; their reports go to the source, not here.",
	CutMarker:    "_[… cut for size: the rest of this message is not in this file]_",
	Conversation: "Conversation",
}

var handoverLabelsJa = handoverLabels{
	Title:   "引き継ぎ: %s",
	Source:  "元のセッション",
	Agent:   "エージェント",
	Folder:  "作業フォルダ",
	Branch:  "ブランチ",
	Moved:   "移行日時",
	Earlier: "これより前の履歴（このファイルと同じフォルダ）",
	Instructions: "元のエージェントが従っていて、あなたのエージェントが読み込まないプロジェクト指示" +
		"（自分で読むこと）",
	Intro: "このファイルは元のセッションの記録のすべてです。これをもとに作業し、" +
		"元のセッションのファイルやその CLI の状態ディレクトリは開かないでください。",

	Stopped:          "中断した時点",
	CutOff:           "最後のターンはエージェントの利用上限で打ち切られました。",
	LastUser:         "最後のユーザー発言:",
	LastAssistant:    "最後のアシスタント発言:",
	Question:         "未回答の質問:",
	Queued:           "キューに入ったまま届かなかった指示:",
	QueuedAttachment: "添付",
	Discarded:        "停止で破棄された入力（取り戻せるよう保管されていたもの。届いていません）:",
	DiscardReasons: map[string]string{
		"second_stop":   "2 回目の停止",
		"discard_queue": "キューの破棄",
		"first_stop":    "1 回目の停止（未送信）",
	},
	Peers:    "元のセッション宛てに待っているピアのメッセージ:",
	PeerFrom: "送信元 %s（意図: %s）",

	WorkingCopy:  "作業コピー",
	NotGit:       "（git の作業コピーではないか、git を読めませんでした）",
	CommitsAhead: "upstream より先のコミット",
	None:         "（なし）",

	Changed:      "変更されたファイル",
	NoneRecorded: "（記録なし）",
	EditsFmt:     "、%d 回編集",
	SubagentOnly: "、サブエージェントのみ",
	Threads:      "未完了のスレッド",
	ChildFmt:     "- 子セッション `%s`: %s\n",
	ProposalFmt:  "- 保留中の引き継ぎ提案: `%s`\n",
	ThreadsNote:  "これらは引き続き元のセッションのものです。報告は元のセッションに届き、こちらには届きません。",
	Conversation: "会話",
	CutMarker:    "_[… サイズのため省略: このメッセージの残りはこのファイルにありません]_",
}

func handoverLabelsFor(lang string) handoverLabels {
	if lang == "ja" {
		return handoverLabelsJa
	}
	return handoverLabelsEn
}
