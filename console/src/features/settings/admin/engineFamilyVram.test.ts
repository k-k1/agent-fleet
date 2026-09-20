// 族ごとの実測 VRAM（ADR 0094 決定 8）。表そのものは数字の羅列なので、試験が押さえるのは
// 「引けること」と「測っていないものには何も返さないこと」——後者がこの表の要点で、推定値を
// 実測として出さないためにある。測っていないものは 2 種類ある: 測っていない族と、測っていない
// **ビルド**。`vram_mib` は合計の下限を上書きするので、後者は GPU の段を誤らせる。
import { describe, expect, it } from "vitest";
import { FAMILY_VRAM_MEASURED, familyVramMeasurement } from "./engineFamilyVram.ts";

const fp8 = ["image/diffusion_models/qwen_image_edit_2509_fp8_e4m3fn.safetensors"];

describe("族の実測 VRAM", () => {
  it("2509 は実測値と測定条件をひと組で返す", () => {
    const m = familyVramMeasurement("qwen-image-edit-2509", fp8);
    // 実測（開発配備・L4 24GB）: vram_total 23,659,151,360 B − vram_free 1,783,934,774 B。
    expect(m?.mib).toBe(20862);
    expect(m?.size).toBe("1024x1024");
    expect(m?.batch).toBe(1);
    expect(m?.inputs).toBe(1);
    expect(m?.file).toBe("qwen_image_edit_2509_fp8_e4m3fn.safetensors");
    expect(m?.card).toBe("L4 24GB");
  });

  it("鍵でも素のファイル名でも合う（取り込みの画面は名前、行は S3 の鍵）", () => {
    expect(familyVramMeasurement("qwen-image-edit-2509", ["qwen_image_edit_2509_fp8_e4m3fn.safetensors"])?.mib).toBe(20862);
  });

  it("base_model の綴りは行と同じ扱いで引ける（前後の空白・大小）", () => {
    expect(familyVramMeasurement(" QWEN-IMAGE-EDIT-2509 ", fp8)?.mib).toBe(20862);
  });

  it("2511 も実測値と測定条件をひと組で返す", () => {
    const m = familyVramMeasurement("qwen-image-edit-2511", ["qwen_image_edit_2511_fp8mixed.safetensors"]);
    // 実測（開発配備・L4 24GB・40 秒間隔の標本のピーク）: vram_total 23,659,151,360 B、
    // 使用 20,974 MiB。🔴 同じ走行を L40S 48GB で測ると 28,358 MiB＝ファイル合計そのままで、
    // 退避が起きない。この欄の値は「苦しいカードで測った量」であって、カードが変われば別の数。
    expect(m?.mib).toBe(20974);
    expect(m?.file).toBe("qwen_image_edit_2511_fp8mixed.safetensors");
    expect(m?.size).toBe("1024x1024");
    expect(m?.batch).toBe(1);
    expect(m?.inputs).toBe(1);
    expect(m?.card).toBe("L4 24GB");
  });

  // 🔴 これが表の主張そのもの。測っていない族に「たぶんこれくらい」を返した時点で、
  // vram_mib の意味が「誰かが測った数字」から「誰かが書いた数字」に変わる。
  it("測っていない族には何も返さない", () => {
    expect(familyVramMeasurement("sdxl", fp8)).toBeNull();
    expect(familyVramMeasurement("anima", ["animaCatTower_v10.safetensors"])).toBeNull();
    expect(familyVramMeasurement("", fp8)).toBeNull();
    expect(familyVramMeasurement(undefined, fp8)).toBeNull();
  });

  // 2511 も 2509 と同じ契約に従う: 族が合っても別ビルドには返さない。
  it("2511 でも別のビルドには返さない", () => {
    expect(familyVramMeasurement("qwen-image-edit-2511", ["qwen_image_edit_2511_bf16.safetensors"])).toBeNull();
    expect(familyVramMeasurement("qwen-image-edit-2511", [])).toBeNull();
  });

  // 🔴 同じ族の別ビルドにも返さない。`vram_mib` は合計の下限を**上書きする**ので、bf16
  //（40.86 GB・L4 に載らない）の行に fp8 の 20,862 を入れると、梯子は L4 の段を通して OOM
  // になる。逆に Q4 変換に入れれば過大申告で一段大きい箱を買う——この機能が防ぐはずの費用。
  it("同じ族でも別のビルドには返さない", () => {
    expect(familyVramMeasurement("qwen-image-edit-2509", ["image/diffusion_models/qwen_image_edit_2509_bf16.safetensors"])).toBeNull();
    expect(familyVramMeasurement("qwen-image-edit-2509", ["Qwen-Image-Edit-2509-Q4_K_M.gguf"])).toBeNull();
    expect(familyVramMeasurement("qwen-image-edit-2509", [])).toBeNull();
    expect(familyVramMeasurement("qwen-image-edit-2509", [undefined])).toBeNull();
  });

  // 🔴 族はエンジンが語彙を返さない行では自由入力なので、任意の文字列がここに来る。素の
  // オブジェクトだと `constructor` が関数を返し、`?? null` は効かず、描画が .mib で落ちる。
  it("Object.prototype の名前でも落ちない", () => {
    for (const name of ["constructor", "toString", "valueOf", "__proto__", "hasOwnProperty"]) {
      expect(familyVramMeasurement(name, fp8)).toBeNull();
    }
  });

  // 表に足すときは測定条件も一緒に、という約束を機械側でも保つ（条件の無い数字は
  // 比べようがない——寸法・batch・ビルドで VRAM は動く）。
  //
  // 🔴 カードもその 1 つで、しかも注記ではない。ComfyUI は苦しいときしか退避しないので、
  // 広いカードで測ると同じ走行が別の数になる（2511 は L4 で 20,974・L40S で 28,358＝ファイル
  // 合計そのまま）。この欄は vram_mib に入る値なので、カードを伏せた測定値は一段大きい箱を
  // 買い続けさせる。
  it("すべての行が測定条件を持つ", () => {
    for (const [family, m] of FAMILY_VRAM_MEASURED) {
      expect(m.mib, family).toBeGreaterThan(0);
      expect(m.size, family).toMatch(/^\d+x\d+$/);
      expect(m.batch, family).toBeGreaterThan(0);
      expect(m.inputs, family).toBeGreaterThanOrEqual(0);
      expect(m.file, family).not.toBe("");
      expect(m.card, family).not.toBe("");
    }
  });
});
