import { useEffect, useState, type ReactNode } from "react";
import {
  useSettings,
  setSetting,
  setSettings,
  VOICEVOX_ZUNDAMON,
  TTS_SPEEDS,
  TTS_CACHE_SIZES,
  TTS_PROVIDERS,
  TTS_POLLY_VOICES,
  TTS_WORK_READ_MODES,
  TTS_RESET,
  type TtsCharConf,
} from "../../lib/settings.ts";
import { voiceCharacters, isDefaultVoice, previewVoice } from "../chat/tts.ts";
import { loadSpeakers, speakersCatalog } from "../chat/ttsSpeakers.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { Choice, OnOff } from "./controls.tsx";

// TtsTab — 音声読み上げ（TTS, docs/24 + ADR0013）の設定タブ。もとは AgentsTab から分離した
// 1 セクションだったが、項目が増えて関心事（声の選択・読むタイミング・テキスト加工・性能）が
// フラットに混在したため、「声＝何で読むか」「自動読み上げ＝いつ読むか」「読み方＝どう読むか」
// 「詳細」のグループに分けている。すべてクライアント側の設定（settings store）なので、
// ワークスペースの起動状態に依らず表示・変更できる。
// 「音声通知」だけは読み上げ本体（ttsEnabled）と独立に効くため、トグルの外＝最後に置く。
export function TtsTab() {
  const s = useSettings();
  const confirm = useConfirm();
  // リセット＝音声読み上げ設定を「初期状態」(TTS_RESET = DEFAULTS の TTS キー) に戻す。キャラは
  // ttsVoicePool: {} ＝標準 14 キャラのスタートで、新規ユーザーの初期状態と揃う。読み仮名辞書は
  // ユーザーが打ち込んだ内容なので消さない（TTS_RESET に含めていない）。多数のキーを一度に書くため
  // setSettings（バッチ）で 1 レンダー・1 保存にまとめる。
  const resetTts = async () => {
    if (!(await confirm({
      title: "音声読み上げ設定をリセット",
      body: "音声読み上げのすべての設定を初期状態に戻します（読み仮名辞書は残ります）。よろしいですか？",
      confirmLabel: "リセット",
    }))) return;
    setSettings(TTS_RESET);
  };
  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">音声読み上げ</h4>
        <Row label="音声読み上げ">
          <OnOff value={s.ttsEnabled} onChange={(v) => setSetting("ttsEnabled", v)} />
        </Row>
        <p className="muted ds-note">
          エージェントの回答を音声で読み上げます。回答が届くと文ごとに順次再生します。
          ずんだもんの合成には VOICEVOX エンジンが必要です（未起動のときは Polly があれば代読、
          どちらも無ければ無音になります）。
          {s.ttsEnabled && <> 音声引用：VOICEVOX：ずんだもん。</>}
        </p>
        <Row label="バックグラウンドでは音量を下げる">
          <OnOff value={s.ttsQuietWhenHidden} onChange={(v) => setSetting("ttsQuietWhenHidden", v)} />
        </Row>
        <p className="muted ds-note">
          別タブへ切り替えたときやブラウザを最小化したとき、読み上げ音量を35%へ下げます。
          Console が見える状態へ戻ると通常音量へ滑らかに戻ります。
        </p>
        <Row label="ペイン位置に合わせて左右へ振る">
          <OnOff value={s.ttsStereoByPane} onChange={(v) => setSetting("ttsStereoByPane", v)} />
        </Row>
        <p className="muted ds-note">
          読み上げ音声をペインの横位置に合わせてステレオ配置します。左右端でも音は片側へ振り切らず、
          通知やファイル朗読などペインに属さない音声は中央で再生します。
        </p>
      </section>
      {s.ttsEnabled && (
        <>
          <section className="ds-group">
            <h4 className="ds-title">声</h4>
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
            {s.ttsProvider !== "polly" && (
              <>
                <div className="ds-row">
                  <span className="ds-label">キャラクター</span>
                </div>
                <CharList />
                <p className="muted ds-note">
                  セッションに割り当てるキャラと、キャラごとの基準スタイル・速度を選べます（朗読ビューの
                  声の選択肢もここで有効にしたキャラになります）。▶ で試聴。一覧は VOICEVOX エンジンから
                  取得するので、エンジンにいるキャラ・スタイルがすべて選べます。速度の「既定」は上の
                  「読み上げ速度」に従います。
                </p>
              </>
            )}
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
          </section>
          <section className="ds-group">
            <h4 className="ds-title">自動読み上げ</h4>
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
                <Row label="作業過程を小声で読む">
                  <Choice
                    value={s.ttsWorkRead}
                    options={TTS_WORK_READ_MODES}
                    onChange={(v) => setSetting("ttsWorkRead", v)}
                  />
                </Row>
                <p className="muted ds-note">
                  ツール実行で途中経過と確定した応答だけを小声で読み、最終回答は通常の声へ戻します。
                  同じキャラに対応スタイルが無い場合や Polly では、同じ声の音量を下げて読みます。
                </p>
                <Row label="開いている全ペインで読む">
                  <OnOff value={s.ttsAutoReadAllPanes} onChange={(v) => setSetting("ttsAutoReadAllPanes", v)} />
                </Row>
                <p className="muted ds-note">
                  アクティブなペインだけでなく、開いているすべてのチャットペインの新着回答（確認・質問も）を
                  読み上げます。複数ペインの回答は 1 本の音声に順番に並びます。「セッションごとに声を変える」と
                  組み合わせると、どのセッションの回答かを声で聞き分けられます。ペインで読むセッションには
                  「セッションの音声通知」の短い告知を重ねません。
                </p>
                <Row label="長い回答は要約して読む">
                  <OnOff value={s.ttsSummaryRead} onChange={(v) => setSetting("ttsSummaryRead", v)} />
                </Row>
                <p className="muted ds-note">
                  長い回答（目安 500 字超）は AI が 2 文に要約してそれだけを読みます
                  （生成に数秒かかります。要約にはアシスタント・チャットを使うため、ワークスペースの起動が必要です）。
                  フル本文はターンの「読み上げ」ボタンでいつでも聞けます。要約に失敗したときは全文を読みます。
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
          </section>
          <section className="ds-group">
            <h4 className="ds-title">読み方</h4>
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
            <Row label="助詞のあとで一呼吸">
              <OnOff value={s.ttsParticlePause} onChange={(v) => setSetting("ttsParticlePause", v)} />
            </Row>
            <p className="muted ds-note">
              「を・は・で・に・と」の直後に漢字が続くところで、読点ひとつぶんの小さな間を入れて読みます
              （例:「神は細部に宿る」→「神は、細部に、宿る」）。文の切れ目の一拍より短い「息継ぎ」で、
              語の切れ目が聞き取りやすくなります。
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
          </section>
          <section className="ds-group">
            <h4 className="ds-title">詳細</h4>
            <Row label="音声キャッシュ">
              <Choice value={s.ttsCacheSec} options={TTS_CACHE_SIZES} onChange={(v) => setSetting("ttsCacheSec", v)} />
            </Row>
            <p className="muted ds-note">
              一度読み上げた文言の音声をメモリに保持し、同じ文言の再読み上げを待ちなしで再生します。
              上限は合計の再生時間で、超えた分は古いものから消えます（ページを再読み込みしても消えます）。
            </p>
          </section>
        </>
      )}
      <section className="ds-group">
        <h4 className="ds-title">音声通知</h4>
        <Row label="セッションの音声通知">
          <OnOff value={s.ttsSessionNotify} onChange={(v) => setSetting("ttsSessionNotify", v)} />
        </Row>
        <p className="muted ds-note">
          バックグラウンドのセッションが回答／確認を返したら、セッション名を添えて短く音声でお知らせします
          （複数同時でも順番に読み上げ）。ブラウザ通知に音声を足すもので、Console のタブが見えている間だけ有効です。
          上の「音声読み上げ」がオフでも独立して使えます（声・速度などの設定は共通）。
        </p>
        <Row label="制限リセットの通知">
          <OnOff value={s.usageResetNotify} onChange={(v) => setSetting("usageResetNotify", v)} />
        </Row>
        <p className="muted ds-note">
          Claude／Codex の利用制限（5時間・週次）に当たっていた枠がリセットされたら、「利用を再開できます」と
          ブラウザ通知でお知らせします（「音声読み上げ」がオンなら音声も）。制限に当たっていない通常のリセットでは
          鳴りません。WsBar の使用状況チップが取得している値を使うので、Console を開いている間に検知します
          （閉じている間に起きたリセットは、次に開いたとき 1 度だけ通知）。
        </p>
      </section>
      <section className="ds-group ds-reset">
        <Button variant="ghost" icon="discard" onClick={resetTts}>
          設定を初期状態にリセット
        </Button>
        <p className="muted ds-note">
          このタブの音声読み上げ設定をすべて初期状態（音声読み上げ・音声通知はオフ、ほかはおすすめの
          初期値）に戻します。読み仮名辞書は消えません。
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

// CharList — キャラクター設定（docs/24）。使用の ON/OFF・基準スタイル・キャラ別速度・試聴。
// 一覧はエンジン実カタログ（GET /api/tts/speakers）駆動で、取得できるまで（エンジン停止中
// 含む）は既定 14 キャラの静的フォールバックを表示する（スタイルはノーマルのみ）。
function CharList() {
  const s = useSettings();
  const [, setLoaded] = useState(false);
  useEffect(() => {
    let alive = true;
    void loadSpeakers().then((l) => alive && l && setLoaded(true));
    return () => {
      alive = false;
    };
  }, []);
  const chars = voiceCharacters();
  const live = !!speakersCatalog(); // エンジン実カタログか（false = 静的フォールバック）
  const pool = s.ttsVoicePool || {};
  const patch = (name: string, p: TtsCharConf) => setSetting("ttsVoicePool", { ...pool, [name]: { ...pool[name], ...p } });
  return (
    <div className="ds-charlist">
      {chars.map((c) => {
        const conf = pool[c.name];
        const use = conf?.use ?? isDefaultVoice(c.name);
        const style = conf?.style && c.styles.some((st) => st.id === conf.style) ? conf.style : c.profile.base;
        return (
          <div key={c.name} className={"ds-char" + (use ? "" : " off")}>
            <label className="ds-char-use" title={use ? "セッション割り当てから外す" : "セッション割り当てに使う"}>
              <input type="checkbox" checked={use} onChange={(e) => patch(c.name, { use: e.target.checked })} />
              <span className="ds-char-name">{c.name}</span>
            </label>
            <select
              value={style}
              disabled={!use || c.styles.length < 2}
              title="基準スタイル（ノーマル以外を選ぶと感情の読み分けは行いません）"
              onChange={(e) => patch(c.name, { style: e.target.value })}
            >
              {c.styles.map((st) => (
                <option key={st.id} value={st.id}>
                  {st.name}
                </option>
              ))}
            </select>
            <select
              value={conf?.speed ?? 0}
              disabled={!use}
              title="このキャラの読み上げ速度（既定 = 全体の設定に従う）"
              onChange={(e) => patch(c.name, { speed: Number(e.target.value) || undefined })}
            >
              <option value={0}>既定</option>
              {TTS_SPEEDS.map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="ds-char-play"
              title="この声で試聴"
              onClick={() => previewVoice(c.name, style, conf?.speed)}
            >
              <Icon name="unmute" />
            </button>
          </div>
        );
      })}
      {!live && (
        <p className="muted ds-note">
          （VOICEVOX エンジンに接続できないため既定の一覧を表示しています。エンジン起動中は全キャラ・
          全スタイルから選べます）
        </p>
      )}
    </div>
  );
}
