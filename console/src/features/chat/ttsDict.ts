// features/chat/ttsDict — テナント共通の読み仮名辞書（docs/log/24）。CP の SettingsStore に
// 管理者が置いた辞書（GET /api/tts/dict）をモジュール内にキャッシュし、effectiveDict() が
// ユーザー辞書（設定 ttsUserDict）と合成して返す。同じ表記はユーザー辞書が勝つ（上書き）。
// 適用はすべてクライアント側（startTts / startNarration / turnTts / ReaderView が使う）。

import { api } from "../../core/api/client.ts";
import { getSettings } from "../../lib/settings.ts";
import { parseUserDict, mergeDicts } from "./ttsText.ts";

let tenantPairs: [string, string][] = [];
let loaded = false;
let loading: Promise<void> | null = null;

// loadTenantDict は共通辞書を取得してキャッシュする。失敗（未ログイン・ネットワーク）は
// 静かに諦め、次の effectiveDict() が再挑戦する。
export function loadTenantDict(): Promise<void> {
  if (loaded) return Promise.resolve();
  if (loading) return loading;
  loading = api("api/tts/dict")
    .then((d) => {
      if (d && !d.error && typeof d.dict === "string") {
        tenantPairs = parseUserDict(d.dict);
        loaded = true;
      }
    })
    .catch(() => {})
    .finally(() => {
      loading = null;
    });
  return loading;
}

// setTenantDict は管理画面の保存直後にキャッシュを更新する（再フェッチ不要で即反映）。
export function setTenantDict(raw: string): void {
  tenantPairs = parseUserDict(raw);
  loaded = true;
}

// effectiveDict はユーザー辞書＋テナント共通辞書の合成（ユーザー優先・表記長降順）。
// 未ロードなら裏で取得を仕掛けつつ、その場はユーザー辞書だけで返す（次の読み上げから効く）。
export function effectiveDict(): [string, string][] {
  if (!loaded) void loadTenantDict();
  return mergeDicts(parseUserDict(getSettings().ttsUserDict), tenantPairs);
}

// 起動時に一度だけ先読みしておく（最初の読み上げから共通辞書が効くように）。未ログインの
// 起動タイミングで失敗しても effectiveDict() 経由で再挑戦する。
void loadTenantDict();
