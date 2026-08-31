package main

// セッション報告の**表示テキストと指示テキストの分離**（docs/log/28 P6・docs/log/30）。
//
// 報告カードには読み手が 2 人いる。利用者（Console のカードを読む）と、オペレーター
// アシスタント（同じ本文がプロンプトとして渡る）。従来はこの 2 つが 1 本の日本語文字列で、
// 「訳すとオペレーターへの指示文まで変わってしまう」ため i18n の対象外に置かれていた
// （docs/log/28 §4）。ここで両者を分ける:
//
//	表示 = 事実だけ（「応答が完了し、入力待ちになりました」）。カタログキー＋引数で保存し、
//	       Console が表示言語で描画する（notice と同じ ADR 0033 の流儀）。
//	指示 = 事実＋オペレーターへの行動指示。**保存せず**、プロンプトを組む瞬間に表示言語で
//	       生成する（reportPromptFor）。保存すると言語が固定され、分離した意味がなくなる。
//
// 分けたことで、カードから「get_session_output で確認して…」というモデル向けの指示が
// 消える。これは副作用ではなく改善で、利用者はそもそもその文を実行しない。
//
// 事実（fact）と指示（orders）を別関数にしてあるのは、表示が事実の**部分集合**である
// ことをコードの形で保証するため（片方だけ直して食い違うのを防ぐ）。

import (
	"strconv"
	"strings"
	"time"
)

// 報告カードの表示カタログキー（Console の ja/en カタログと 1 対 1）。
const (
	reportKeyAnswerReady       = "chat.report.answer_ready"
	reportKeyTurnFailed        = "chat.report.turn_failed"
	reportKeyTurnAborted       = "chat.report.turn_aborted"
	reportKeyTurnAbortedCapped = "chat.report.turn_aborted_capped"
	reportKeyQuestion          = "chat.report.question"
	reportKeyPlanApproval      = "chat.report.plan_approval"
	reportKeyPermission        = "chat.report.permission_request"
	reportKeyReopened          = "chat.report.reopened"
	reportKeyReopenCapped      = "chat.report.reopen_capped"
	reportKeyExit              = "chat.report.exit"
	reportKeyUnknown           = "chat.report.unknown"
)

// reportView is one report's event plus the arguments both renderers read. 引数を
// map[string]string にしてあるのは、そのまま NoticeArgs として保存され Console の
// カタログ描画へ渡るため（表示とプロンプトが同じ材料を見ることが分離の前提）。
type reportView struct {
	kind   string
	reason string
	args   map[string]string
}

func reportViewOf(m chatMessage) reportView {
	return reportView{kind: m.ReportKind, reason: m.ReportReason, args: m.NoticeArgs}
}

func (v reportView) arg(k string) string { return v.args[k] }

// resumeCapped: 自動再開の上限に達したか。attempts は「この報告を配ったあとの値」。
func (v reportView) resumeCapped() bool {
	n, _ := strconv.Atoi(v.arg("attempts"))
	return n > maxAutoResumeAttempts
}

// reportDisplayKey は表示カタログキー。exit の理由ラベルや訂正の対象時刻のような可変部は
// 引数（NoticeArgs）で渡す。
func (v reportView) displayKey() string {
	switch v.kind {
	case reportKindAnswerReady:
		switch v.reason {
		case reportReasonTurnFailed:
			return reportKeyTurnFailed
		case reportReasonTurnAborted:
			if v.resumeCapped() {
				return reportKeyTurnAbortedCapped
			}
			return reportKeyTurnAborted
		}
		return reportKeyAnswerReady
	case "question":
		return reportKeyQuestion
	case "plan-approval":
		return reportKeyPlanApproval
	case "permission-request":
		return reportKeyPermission
	case reportKindReopened:
		if v.reason == reportReasonReopenCapped {
			return reportKeyReopenCapped
		}
		return reportKeyReopened
	case "exit":
		return reportKeyExit
	}
	return reportKeyUnknown
}

// exitLabelFor renders an abnormal exit's reason. 未知の理由は生の値をそのまま出す
// （新しい理由コードが増えたときに空欄になるより、生でも見えている方がよい）。
func exitLabelFor(reason, lang string) string {
	if lang == "en" {
		switch reason {
		case "oom":
			return "OOM (killed — out of memory)"
		case "crashed":
			return "crashed"
		case "killed":
			return "force-killed (SIGKILL)"
		}
		return reason
	}
	switch reason {
	case "oom":
		return "OOM（メモリ不足で強制終了）"
	case "crashed":
		return "クラッシュ"
	case "killed":
		return "強制終了（SIGKILL）"
	}
	return reason
}

// reportFact は「何が起きたか」だけの一文。表示カードの本文であり、プロンプトの冒頭でもある。
// カタログの ja 文言と**同じ文**であること（Content は表示のフォールバックなので、食い違うと
// 旧レコードだけ別の文が出る）。
func (v reportView) fact(lang string) string {
	en := lang == "en"
	switch v.displayKey() {
	case reportKeyTurnFailed:
		if en {
			return "The turn ended with an error on the model/provider side and the session is back to waiting for input (no answer was generated)."
		}
		return "ターンがモデル／プロバイダ側のエラーで終了し、入力待ちに戻りました（応答は生成されていません）。"
	case reportKeyTurnAborted, reportKeyTurnAbortedCapped:
		var b strings.Builder
		if en {
			b.WriteString("The turn was cut off and the session is back to waiting for input " +
				"(a dropped connection, a temporary rate limit — something that clears on its own; the answer is unfinished). " +
				"Re-sending resumes it from where it stopped.")
		} else {
			b.WriteString("ターンが中断して入力待ちに戻りました（接続断や一時的なレート制限など、時間をおけば解消する原因で、回答は完成していません）。" +
				"再送すれば続きから走れる中断です。")
		}
		if v.resumeCapped() {
			if en {
				b.WriteString(" [The automatic-resume limit (" + strconv.Itoa(maxAutoResumeAttempts) + ") has been reached]")
			} else {
				b.WriteString("【自動再開の上限（" + strconv.Itoa(maxAutoResumeAttempts) + "回）に達しています】")
			}
		}
		return b.String()
	case reportKeyQuestion:
		if en {
			return "The session has stopped, presenting a question (a set of choices)."
		}
		return "質問（選択肢）を提示して停止しています。"
	case reportKeyPlanApproval:
		if en {
			return "The session has stopped, presenting a plan and waiting for approval."
		}
		return "プランを提示して承認待ちで停止しています。"
	case reportKeyPermission:
		if en {
			return "The session has stopped, waiting for permission to run a tool. Permission has to be granted from the Console."
		}
		return "ツール実行の許可待ちで停止しています。許可は Console から行う必要があります。"
	case reportKeyReopenCapped:
		if en {
			return "The completion reported earlier was premature, and the completion verdict keeps oscillating " +
				"(the correction limit of " + strconv.Itoa(instrReopenMax) + " has been reached, so there will be no further automatic corrections)."
		}
		return "先の完了報告は早計でしたが、完了判定が繰り返し揺れています" +
			"（訂正の上限 " + strconv.Itoa(instrReopenMax) + " 回に達したため、これ以上の自動訂正は行いません）。"
	case reportKeyReopened:
		if en {
			return "The completion reported earlier was premature — the session has gone on working since."
		}
		return "先の完了報告は早計でした — セッションはその後も作業を続けています。"
	case reportKeyExit:
		label := exitLabelFor(v.reason, lang)
		if en {
			return "The agent process exited abnormally: " + label + "."
		}
		return "エージェントプロセスが異常終了しました: " + label + "。"
	case reportKeyUnknown:
		if en {
			return "The state changed (" + v.kind + ")."
		}
		return "状態が変化しました（" + v.kind + "）。"
	}
	if en {
		return "The session answered and is now waiting for input."
	}
	return "応答が完了し、入力待ちになりました。"
}

// reportOrders はオペレーターへの行動指示。**表示には出さない**（利用者が実行する文ではない）
// ので、保存もしない — プロンプトを組む瞬間に表示言語で生成する。
// 自動走行・自動再開のトグルは配信時ではなく**この瞬間**に読む: 指示は「今どう動くべきか」で、
// 保留のまま設定が変わったなら新しい設定に従うのが正しい。
func (v reportView) orders(lang string) string {
	en := lang == "en"
	switch v.displayKey() {
	case reportKeyTurnFailed:
		if en {
			return "Read the error text with get_session_output, tell the user the cause (expired auth, no credit left, a rate limit, a bad model name …) and discuss what to do (change model, revisit the connection settings …). " +
				"Re-sending the same instruction before the cause is fixed gets the same result."
		}
		return "get_session_output でエラー本文を確認し、原因（認証切れ・残高不足・レート制限・モデル指定など）を利用者に伝えて、" +
			"対処（モデル変更・接続設定の見直しなど）を相談してください。" +
			"原因が解消しないうちに同じ指示を再送しても同じ結果になります。"
	case reportKeyTurnAbortedCapped:
		if en {
			return "Do not resume automatically any more: tell the user that the session keeps getting cut off, show what get_session_output reports as the last output, " +
				"and discuss what to do (a different model, revisiting the connection settings, splitting the work …)."
		}
		return "これ以上は自動で再開せず、中断が繰り返されている事実と get_session_output で見た直前の出力を利用者に伝えて、" +
			"対処（モデル変更・接続設定の見直し・作業の分割など）を相談してください。"
	case reportKeyTurnAborted:
		// 送信文の言語はセッション側に合わせる（表示言語ではない）。日本語で作業している
		// セッションへ英語を送ると、以降そのセッションの出力言語まで反転してしまい、
		// セッション単位の言語フィールドは存在しないので取り返しがつかない。
		if !chatAutoResumeEnabled() {
			if en {
				return "[Automatic resume on abort is OFF] Tell the user that the turn was cut off and summarize the last output, " +
					"confirm that resuming is fine, and then nudge the session on with send_to_session. " +
					"Write the message in the language the session itself has been working in (Japanese if it works in Japanese, English if English), " +
					"say only that it was cut off and should continue, and mix in no new instruction."
			}
			return "【中断時の自動再開 OFF】中断した事実と直前の出力の要点を利用者に伝え、" +
				"再開してよいか確認したうえで send_to_session で続行を促してください。" +
				"送信文はそのセッションが直前に使っている言語に合わせ（日本語で作業していれば日本語、英語なら英語）、" +
				"「中断したので続けてほしい」旨だけにして新しい指示を混ぜないでください。"
		}
		if en {
			return "[Automatic resume on abort is ON] Check the last output with get_session_output and resume the session with send_to_session, " +
				"saying only that it was cut off and should continue. " +
				"Write the message in the language the session itself has been working in " +
				"(Japanese if it works in Japanese, English if English; when you cannot tell, use the language of the original instruction). " +
				"Mix in no new instruction and no extra request. Mention to the user that you resumed it. " +
				"If you can read that it died in the middle of a destructive or irreversible operation (deletion, force push, sending data outside, added cost …), " +
				"do NOT resume automatically — ask the user."
		}
		return "【中断時の自動再開 ON】get_session_output で直前の出力を確認し、" +
			"send_to_session で「中断したので続けてほしい」旨だけを送って再開させてください。" +
			"送信文はそのセッションが直前に使っている言語に合わせてください" +
			"（日本語で作業していれば日本語、英語なら英語。判断がつかなければ最初の指示と同じ言語）。" +
			"新しい指示や追加の依頼は混ぜないこと。再開させたことは利用者にも一言共有してください。" +
			"ただし、破壊的・不可逆な操作（削除・強制 push・外部送信・コスト増等）の途中で落ちたと読み取れる場合は" +
			"自動で再開せず、利用者に確認してください。"
	case reportKeyQuestion:
		// 自動走行 (opt-in): the interim report itself carries the mode's marching
		// orders, so the operator needs no separate state — OFF asks the user first,
		// ON answers with the SESSION'S recommendation under explicit guardrails.
		if chatAutoPilotEnabled() {
			if en {
				return "[Autopilot is ON] Check the question and its options with get_session_status (and the context with get_session_output if you need it). " +
					"When the session's own recommendation is clear (a 'Recommended' label or a recommendation in the last output), " +
					"answer with that option via answer_session_question and share with the user which one you picked and why. " +
					"When no recommendation can be read, or when the choice could lead to a destructive or irreversible result " +
					"(deletion, overwriting, sending data outside, added cost …), do not answer automatically — put the options to the user and confirm. " +
					"This is an interim report; the instruction's completion report arrives separately."
			}
			return "【自動走行モード ON】get_session_status で質問と選択肢を、必要なら get_session_output で文脈を確認し、" +
				"セッションの推奨（『推奨』/『(Recommended)』等のラベルや直前の出力の推奨）が明確なら、" +
				"answer_session_question でその選択肢を回答し、どれを・なぜ選んだかを利用者に共有してください。" +
				"推奨が読み取れない場合や、選択が破壊的・不可逆な結果（削除・上書き・外部送信・コスト増等）に" +
				"つながり得る場合は自動回答せず、選択肢を利用者に提示して確認してください。" +
				"これは途中経過の報告で、指示の完了報告は別途届きます。"
		}
		if en {
			return "Check the question and its options with get_session_status, put the options to the user, confirm their intent, " +
				"and answer with answer_session_question (you may choose yourself only when the user has delegated the judgement in advance; the Console can answer too). " +
				"This is an interim report; the instruction's completion report arrives separately."
		}
		return "get_session_status で質問と選択肢を確認し、" +
			"利用者に選択肢を提示して意向を確認のうえ answer_session_question で回答してください" +
			"（利用者が事前に判断を任せている場合のみ自分で選択可。Console からも回答できます）。" +
			"これは途中経過の報告で、指示の完了報告は別途届きます。"
	case reportKeyPlanApproval:
		// 自動走行: drive the plan through review → feedback → approval (the user's
		// standing delegation is the mode toggle itself); OFF relays to the user.
		if chatAutoPilotEnabled() {
			if en {
				return "[Autopilot is ON] Read the plan with get_session_status and have another session review it " +
					"(you may create one in a suitable working copy of the same repository; instruct the review as read-only work). " +
					"If the review finds nothing, approve with respond_session_plan(approve) so execution starts; " +
					"if it raises points, ask for changes with respond_session_plan(reject, feedback=a summary of the points) and treat the revised plan the same way. " +
					"Share what you judged and how, every time (do not transcribe the plan body into the chat — write the session name and let the user open it in the mirror). " +
					"When the plan contains a destructive or irreversible operation (deletion, force push, sending data outside, added cost …), do not approve automatically — ask the user. " +
					"This is an interim report; the instruction's completion report arrives separately."
			}
			return "【自動走行モード ON】get_session_status でプラン本文を確認し、別セッションにプランのレビューをさせてください" +
				"（同リポジトリの適切な作業コピーで新規作成してよい。レビューは読み取り専用の作業として指示する）。" +
				"レビュー結果が問題なしなら respond_session_plan(approve) で承認して実行を開始させ、" +
				"指摘があれば respond_session_plan(reject, feedback=指摘の要約) で修正を求め、改訂プランも同様に扱ってください。" +
				"何をどう判断したかは毎回利用者に共有してください（プラン本文はチャットへ転記せず、" +
				"セッション名をそのまま書いてリンクで参照させる — 利用者はミラーで直接確認できます）。" +
				"プランに破壊的・不可逆な操作（削除・強制push・外部送信・コスト増等）が含まれる場合は自動承認せず、" +
				"利用者に確認してください。これは途中経過の報告で、指示の完了報告は別途届きます。"
		}
		if en {
			return "Do not transcribe the plan body into the chat — writing the session name makes it a link the user can open in the mirror (a one-line gist is enough). " +
				"Confirm the user's intent (approve / feedback for changes / a review in another session), then approve with respond_session_plan(approve) " +
				"or ask for changes with respond_session_plan(reject, feedback=the correction). The Console can do this too. " +
				"This is an interim report; the instruction's completion report arrives separately."
		}
		return "プラン本文はチャットへ転記しないでください — " +
			"セッション名をそのまま書けばリンクになり、利用者はミラーで直接確認できます（要点を一言添える程度で可）。" +
			"利用者の意向（承認／修正フィードバック／別セッションでのレビュー）を確認し、" +
			"承認は respond_session_plan(approve)、修正は respond_session_plan(reject, feedback=修正指示)。" +
			"Console からも操作できます。これは途中経過の報告で、指示の完了報告は別途届きます。"
	case reportKeyReopenCapped:
		if en {
			return "Do not wait for an automatic completion report for this instruction: check the current state with get_session_status / get_session_output, " +
				"then tell the user that the verdict is unstable and where the session actually stands."
		}
		return "この指示については自動の完了報告を待たず、get_session_status / get_session_output で現在の状態を確認したうえで、" +
			"判定が安定しない事実とセッションの現況を利用者に伝えてください。"
	case reportKeyReopened:
		// 補償（docs/log/51 §補償）。オペレーターは既に「完了した」と利用者へ伝えている
		// 可能性が高いので、まず取り消しを求め、次の完了報告を待つよう指示する。
		if en {
			return "If you already told the user it was done, take that back and tell them it is still in progress. " +
				"Send no further instruction and wait for this instruction's completion report to arrive again " +
				"(use get_session_status / get_session_output when you want to check where it stands)."
		}
		return "利用者に完了を伝えていた場合は取り消して、まだ作業中であることを伝えてください。" +
			"追加の指示は送らず、この指示の完了報告が改めて届くのを待ってください" +
			"（状況を確認したいときは get_session_status / get_session_output を使ってください）。"
	case reportKeyExit:
		if en {
			return "Tell the user what happened if it matters, and consider resuming or re-instructing."
		}
		return "必要なら状況を利用者に伝え、再開/再指示を検討してください。"
	}
	return "" // answer-ready（正常）と permission-request は事実だけで指示は要らない
}

// --- 付記（表示・プロンプトの両方に出る事実） -------------------------------------
//
// 付記は「本文の後ろに足す 1 文」で、出るかどうかは引数の有無で決まる（Console 側も同じ
// 判定で並べる）。ここに置くのは事実の部分だけで、オペレーターへの指示は prompt 側に足す。

// rateLimitResumeFact は利用上限で止まり自動再開が予約済みであることの一文（docs/log/47 §4-4）。
func rateLimitResumeFact(atMs int64, lang string) string {
	at := time.UnixMilli(atMs).Local()
	if lang == "en" {
		return "[Stopped by the usage limit] An automatic resume is booked for " + at.Format("Jan 2 15:04") +
			" (when the limit lifts), which will send this session a nudge to continue."
	}
	return "【利用上限による停止です】" + at.Format("1月2日 15:04") +
		"（上限が解ける時刻）に、このセッションへ続行を送る自動再開の予約が入っています。"
}

func rateLimitResumeOrders(lang string) string {
	if lang == "en" {
		return "Do not send a nudge to resume — tell the user that it stopped on the limit and when it is scheduled to resume."
	}
	return "再開を促す送信はせず、上限で止まったことと再開予定時刻を利用者に伝えてください。"
}

// foldFact は「この報告が指示 N 件ぶんの完了である」ことの一文（docs/log/51 §畳み込み）。
func foldFact(n int, ats, lang string) string {
	if lang == "en" {
		return "(This report is the completion of " + strconv.Itoa(n) + " instructions. Dispatched: " + ats + ")"
	}
	return "（この報告は指示 " + strconv.Itoa(n) + " 件ぶんの完了です。投入: " + ats + "）"
}

// reopenTargetFact は訂正の対象がどの報告かの一文（docs/log/51 §補償）。
func reopenTargetFact(atMs int64, lang string) string {
	at := time.UnixMilli(atMs).Local().Format("2006-01-02 15:04")
	if lang == "en" {
		return "(correcting the completion report of " + at + ")"
	}
	return "（訂正の対象: " + at + " の完了報告）"
}

func (v reportView) notesFact(lang string) string {
	var b strings.Builder
	if ms := argMs(v.arg("resume_at")); ms > 0 {
		b.WriteString(rateLimitResumeFact(ms, lang))
	}
	if n, _ := strconv.Atoi(v.arg("fold_n")); n >= 2 {
		b.WriteString(foldFact(n, v.arg("fold_ats"), lang))
	}
	if ms := argMs(v.arg("reopen_at")); ms > 0 {
		b.WriteString(reopenTargetFact(ms, lang))
	}
	return b.String()
}

func argMs(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// --- 2 人の読み手それぞれの本文 ---------------------------------------------------

// reportHeadline は「セッション「x」(s7) からの報告: 」の前置き。表示・プロンプト共通。
func reportHeadline(display, name, lang string) string {
	if lang == "en" {
		return "Report from session \"" + display + "\" (" + name + "): "
	}
	return "セッション「" + display + "」(" + name + ") からの報告: "
}

// reportDisplayText は報告カードの本文（事実だけ）。保存する Content はこの ja 版で、
// 表示は Console がカタログキー（displayKey）＋引数から描き直す（ADR 0033）。
func (v reportView) displayText(lang string) string {
	return reportHeadline(v.arg("display"), v.arg("name"), lang) + v.fact(lang) + v.notesFact(lang)
}

// reportHeadFor is the event line an operator reads: fact + marching orders, without the
// 「セッション「x」からの報告:」headline and without the notes. 1 つの kind/reason の文言を
// まとめて見たいところ（テストと、報告以外から文面を借りたいとき）の入口。
func reportHeadFor(kind, reason string, resumeAttempts int, lang string) string {
	v := reportView{kind: kind, reason: reason, args: map[string]string{
		"attempts": strconv.Itoa(resumeAttempts),
	}}
	return v.fact(lang) + v.orders(lang)
}

// reportPromptFor は provider へ渡す本文（事実＋オペレーターへの指示）。保存しない。
// P6 より前に書かれた報告（ReportKind が無い）は Content をそのまま使う — 当時の本文は
// 指示込みで書かれているので、それが正しい振る舞い。
func reportPromptFor(m chatMessage, lang string) string {
	if m.ReportKind == "" {
		return m.Content
	}
	v := reportViewOf(m)
	var b strings.Builder
	b.WriteString(reportHeadline(v.arg("display"), v.arg("name"), lang))
	b.WriteString(v.fact(lang))
	if o := v.orders(lang); o != "" {
		b.WriteString(o)
	}
	b.WriteString(v.notesFact(lang))
	if ms := argMs(v.arg("resume_at")); ms > 0 {
		b.WriteString(rateLimitResumeOrders(lang))
	}
	return b.String()
}
