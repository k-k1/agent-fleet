package main

// session_wiring.go — `internal/sessionx` の外向き依存（sessionx → main）を 1 箇所で配線する。
//
// 逆向き（main → sessionx）は別名として alias_session.go にある。**2 枚に分けてある**のは、
// エイリアスがウェーブ境界で丸ごと剥がれて消えるのに対し、配線は残るため
// （sessionx が errcodes.go や fs.go を引く関係そのものは回収しても消えない）。
//
// 名前は姉妹家系（git_wiring.go / mcp_wiring.go / memory_wiring.go）に揃えて
// 「家系名＋_wiring」にしてある。移送先が `internal/sessionx` なのは、`internal/session`
// が**モデルのリーフ**（agents 配下 7 つを含む 16 パッケージが import している）で
// 合流させると循環するため。配線ファイルの名前は移送先ではなく家系を指す。
//
// 🔥 **配線に既定値を置かない。** 未配線は `sessionx.Configure` が panic で落とす。
// 零値が一番危ないのは値型で、`MaxUploadBytes` が 0 なら**あらゆるアップロードが
// 「大きすぎます」で落ち**、11 本のエラーコードが空なら **Console へ `""` が届いて
// i18n が解決できず、生の developer メッセージが露出する**（静かに壊れる形）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"

func init() { sessionx.Configure(sessionDeps()) }

// sessionDeps は本番の配線一式。**sessionx 側の網羅検査（internal/sessionx/deps_test.go）は
// 作り物を使う**ので、ここが唯一「本物の値」を書く場所である。
//
// 🔥 だから「入れ替え」を止められるのもここだけである: 11 本のエラーコードは全部
// `string`、9 本のうち 2 本（BrowseRoot / ToolchainShellPrefix）は同じ `func() string` で、
// **取り違えても型検査も reflect の網羅検査も鳴らない**。session_wiring_test.go が
// 本物と 1 本ずつ突き合わせる。
func sessionDeps() sessionx.Deps {
	return sessionx.Deps{
		EnvOr:            envOr,
		FirstNonEmpty:    firstNonEmpty,
		SplitFrontmatter: splitFrontmatter,

		BrowseRoot:     browseRoot,
		MaxUploadBytes: maxUploadBytes,

		IsSvnRepo:       isSvnRepo,
		RepoJobsRunning: repoJobsRunning,

		FinalizeSessionUsage:  finalizeSessionUsage,
		MaybeFoldSessionUsage: maybeFoldSessionUsage,

		RemoveTerminalHistory: removeTerminalHistory,

		ToolchainShellPrefix: toolchainShellPrefix,

		// mcpConvID は mcp_wiring.go が実行中に書き換える var なので、
		// **値ではなく読み取り関数**で渡す（値で渡すと承認プロンプトが
		// 配線した瞬間の会話 ID で固まる）。
		MCPConvID:       func() string { return mcpConvID },
		RunOperatorTurn: runOperatorTurn,

		ErrCodeAgentNotConnected:      errCodeAgentNotConnected,
		ErrCodeChatConversationNotFnd: errCodeChatConversationNotFnd,
		ErrCodeForkAtUnsupported:      errCodeForkAtUnsupported,
		ErrCodeForkBadAnchor:          errCodeForkBadAnchor,
		ErrCodeForkMissingDir:         errCodeForkMissingDir,
		ErrCodeForkUnsupportedKind:    errCodeForkUnsupportedKind,
		ErrCodeLocked:                 errCodeLocked,
		ErrCodePasteTooLarge:          errCodePasteTooLarge,
		ErrCodePasteUnsupportedAgent:  errCodePasteUnsupportedAgent,
		ErrCodePasteUnsupportedKind:   errCodePasteUnsupportedKind,
		ErrCodeTitleFeatureDisabled:   errCodeTitleFeatureDisabled,
		ErrCodeTitleNoContent:         errCodeTitleNoContent,
	}
}
