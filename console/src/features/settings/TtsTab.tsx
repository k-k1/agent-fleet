import type { ReactNode } from "react";
import {
  useSettings,
  setSetting,
  VOICEVOX_ZUNDAMON,
  TTS_SPEEDS,
  TTS_CACHE_SIZES,
  TTS_PROVIDERS,
  TTS_POLLY_VOICES,
} from "../../lib/settings.ts";
import { Choice, OnOff } from "./controls.tsx";

// TtsTab — 音声読み上げ（TTS, docs/24 + ADR0013）の設定タブ。もとは AgentsTab の
// 「セッション」グループにあったが、項目が増えて他のエージェント設定を圧迫したため分離。
// すべてクライアント側の設定（settings store）なので、ワークスペースの起動状態に依らず
// 表示・変更できる。
export function TtsTab() {
  const s = useSettings();
  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">音声読み上げ</h4>
        <Row label="音声読み上げ">
          <OnOff value={s.ttsEnabled} onChange={(v) => setSetting("ttsEnabled", v)} />
        </Row>
        {s.ttsEnabled && (
          <>
            <Row label="音声エンジン">
              <Choice
                value={s.ttsProvider}
                options={TTS_PROVIDERS}
                onChange={(v) => setSetting("ttsProvider", v)}
              />
            </Row>
            <p className="muted ds-note">
              「自動」は日本語をずんだもん（VOICEVOX）で読み、エンジンが起動していない間や日本語以外は
              AWS Polly に自動で切り替えます（次の文からずんだもんに復帰）。「Polly」は常に Polly で読みます。
            </p>
            {s.ttsProvider !== "polly" && (
              <Row label="話者（ずんだもん）">
                <Choice
                  value={s.ttsVoiceVoicevox}
                  options={VOICEVOX_ZUNDAMON}
                  onChange={(v) => setSetting("ttsVoiceVoicevox", v)}
                />
              </Row>
            )}
            {s.ttsProvider !== "voicevox" && (
              <Row label="話者（Polly）">
                <Choice
                  value={s.ttsVoicePolly}
                  options={TTS_POLLY_VOICES}
                  onChange={(v) => setSetting("ttsVoicePolly", v)}
                />
              </Row>
            )}
            <Row label="セッションごとに声を変える">
              <OnOff value={s.ttsVoicePerSession} onChange={(v) => setSetting("ttsVoicePerSession", v)} />
            </Row>
            <p className="muted ds-note">
              セッション名から話者（VOICEVOX 標準の 14 キャラ／Polly 3 声）を自動で割り当てます。
              同じセッションは常に同じ声になり、複数セッションの読み上げ・音声通知を声で聞き分けられます。
              アシスタント・チャットや朗読ビューは上で選んだ話者のままです。
            </p>
            <Row label="内容で感情を変える">
              <OnOff value={s.ttsEmotion} onChange={(v) => setSetting("ttsEmotion", v)} />
            </Row>
            <p className="muted ds-note">
              エラー・失敗を含む文はツンツン、成功・完了を含む文はあまあまのスタイルで読みます（文ごとに判定）。
              スタイルを持つ話者（ずんだもん・四国めたん・九州そら・玄野武宏・白上虎太郎など）のときだけ効き、
              Polly には影響しません。
            </p>
            <Row label="読み上げ速度">
              <Choice value={s.ttsSpeed} options={TTS_SPEEDS} onChange={(v) => setSetting("ttsSpeed", v)} />
            </Row>
            <Row label="音声キャッシュ">
              <Choice value={s.ttsCacheSec} options={TTS_CACHE_SIZES} onChange={(v) => setSetting("ttsCacheSec", v)} />
            </Row>
            <p className="muted ds-note">
              一度読み上げた文言の音声をメモリに保持し、同じ文言の再読み上げを待ちなしで再生します。
              上限は合計の再生時間で、超えた分は古いものから消えます（ページを再読み込みしても消えます）。
            </p>
            <Row label="新しい回答を自動で読み上げ">
              <OnOff value={s.ttsAutoReadMirror} onChange={(v) => setSetting("ttsAutoReadMirror", v)} />
            </Row>
            <p className="muted ds-note">
              アクティブなペインのチャットに新しい回答が届いたら、自動でカラオケ・ハイライト付きで読み上げます。
              読み上げ中に次の回答が届いたら、終わってから順番に読みます（見ていないセッションは
              「セッションの音声通知」が担当）。
            </p>
            {s.ttsAutoReadMirror && (
              <>
                <Row label="開いている全ペインで読む">
                  <OnOff value={s.ttsAutoReadAllPanes} onChange={(v) => setSetting("ttsAutoReadAllPanes", v)} />
                </Row>
                <p className="muted ds-note">
                  アクティブなペインだけでなく、開いているすべてのチャットペインの新着回答（確認・質問も）を
                  読み上げます。複数ペインの回答は 1 本の音声に順番に並びます。「セッションごとに声を変える」と
                  組み合わせると、どのセッションの回答かを声で聞き分けられます。ペインで読むセッションには
                  「セッションの音声通知」の短い告知を重ねません。
                </p>
              </>
            )}
            <Row label="確認・質問を読み上げる">
              <OnOff value={s.ttsReadPending} onChange={(v) => setSetting("ttsReadPending", v)} />
            </Row>
            <p className="muted ds-note">
              アクティブなペインのセッションが確認待ち（質問カード・プラン承認・許可要求）になったら、
              質問文と選択肢を読み上げます。選択肢は画面の短いラベルではなく説明文の方を読みます。
              画面を見ていなくても、何を聞かれているかが音声で分かります。
            </p>
            <Row label="長い回答は要約して読む">
              <OnOff value={s.ttsSummaryRead} onChange={(v) => setSetting("ttsSummaryRead", v)} />
            </Row>
            <p className="muted ds-note">
              自動読み上げのとき、長い回答（目安 500 字超）は AI が 2 文に要約してそれだけを読みます
              （生成に数秒かかります。要約にはアシスタント・チャットを使うため、ワークスペースの起動が必要です）。
              フル本文はターンの「読み上げ」ボタンでいつでも聞けます。要約に失敗したときは全文を読みます。
            </p>
            <Row label="コード片を省略して読む">
              <OnOff value={s.ttsAbbrevCode} onChange={(v) => setSetting("ttsAbbrevCode", v)} />
            </Row>
            <p className="muted ds-note">
              バッククォートのコード片を全部読まずに省略します。コミットハッシュ等は頭 2 文字＋
              「なんとか」等のフィラー語（例: e79853e → e7 ふがふが）、camelCase やパスは頭の一語＋フィラー
              （3 語以上は末尾の一語も。例: ttsAutoReadMirror → tts なんとか Mirror）。短い語・空白を含む
              コマンド・読み仮名辞書に載せた表記はそのまま読みます。バッククォートで括られていない
              裸のハッシュ・UUID も、地の文の中から見つけて同じように省略します（英単語や長い数値は
              誤検知しないよう 16 進らしいものだけ）。
            </p>
            <Row label="英語をカタカナ読み">
              <OnOff value={s.ttsEnglishKana} onChange={(v) => setSetting("ttsEnglishKana", v)} />
            </Row>
            <p className="muted ds-note">
              英単語をカタカナ英語に変換して、ずんだもんの声のまま「それっぽく」読みます（CMU 発音辞書ベースの音写。
              定着した和製カタカナ＝コーヒー等ではなく音写＝カフィー等になります）。AWS サービスや開発用語
              （EC2→イーシーツー, Dao→ダオ, nginx 等）は専用辞書で補正、辞書外の一般語は綴りのまま。
            </p>
            <div className="ds-userdict-block">
              <span className="ds-label">読み仮名辞書</span>
              <textarea
                className="ds-userdict"
                value={s.ttsUserDict}
                onChange={(e) => setSetting("ttsUserDict", e.target.value)}
                rows={4}
                spellCheck={false}
                placeholder={"表記=読み（1行に1件）\n例）GPT-4=ジーピーティーフォー\n神=かみ"}
              />
              <p className="muted ds-note">
                読み上げ前に、テキスト中の「表記」を指定した「読み」に置き換えます。英語・日本語・記号どれでも可。
                1 行に 1 件「表記=読み」、# 始まりはコメント。長い表記から優先し、「英語をカタカナ読み」よりも先に当たります。
                管理者が設定したテナント共通辞書がある場合はそれも一緒に適用され、同じ表記はここでの指定が優先されます。
              </p>
            </div>
          </>
        )}
        <p className="muted ds-note">
          エージェントの回答を音声で読み上げます。回答が届くと文ごとに順次再生します。
          ずんだもんの合成には VOICEVOX エンジンが必要です（未起動のときは Polly があれば代読、
          どちらも無ければ無音になります）。
          {s.ttsEnabled && <> 音声引用：VOICEVOX：ずんだもん。</>}
        </p>
      </section>
      <section className="ds-group">
        <h4 className="ds-title">音声通知</h4>
        <Row label="セッションの音声通知">
          <OnOff value={s.ttsSessionNotify} onChange={(v) => setSetting("ttsSessionNotify", v)} />
        </Row>
        <p className="muted ds-note">
          バックグラウンドのセッションが回答／確認を返したら、セッション名を添えて短く音声でお知らせします
          （複数同時でも順番に読み上げ）。ブラウザ通知に音声を足すもので、Console のタブが見えている間だけ有効です。
        </p>
      </section>
    </div>
  );
}

// A labeled settings row (mirrors DisplayTab / AgentsTab の Row).
function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}
