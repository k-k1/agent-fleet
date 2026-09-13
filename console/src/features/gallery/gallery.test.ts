// The gallery's pure rules (ADR 0080), plus the pane-kind wiring that has no view of its
// own to fail in: a `gallery` content that the stored-layout validator rejects comes back
// from a reload as a blank terminal, and nothing on screen says why.
import { describe, expect, it } from "vitest";
import {
  PAGE_SIZE,
  breadcrumb,
  effectiveSort,
  focusIndex,
  galleryFolders,
  galleryImages,
  galleryTotals,
  parentPath,
  hasTimes,
  sortImages,
  visibleImages,
  type FsEntry,
} from "./gallery.ts";
import { validateStoredContent } from "../../layout/migrate.ts";
import { sameTarget } from "../../layout/ops.ts";
import { paneTitle } from "../panes/paneTitle.ts";
import type { PaneContent, View } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

const entry = (name: string, extra: Partial<FsEntry> = {}): FsEntry => ({ name, type: "file", size: 100, ...extra });

const view = (content: PaneContent): View => ({ id: "p1", session: null, content, wrap: null });

describe("フォルダの一覧から画像だけを取る", () => {
  it("画像だけを拾い、パスはフォルダ相対で組み立てる", () => {
    const images = galleryImages(
      [entry("a.png"), entry("notes.md"), entry("b.JPEG"), entry("c.webp"), entry("d.txt")],
      "repos/x/img",
    );
    expect(images.map((i) => i.name)).toEqual(["a.png", "b.JPEG", "c.webp"]);
    expect(images[0].path).toBe("repos/x/img/a.png");
  });

  it("フォルダは拡張子が画像でも出さない（P0 は 1 階層）", () => {
    expect(galleryImages([entry("shots.png", { type: "dir" })], "")).toEqual([]);
  });

  it("ルート直下ではフォルダ名を前に付けない", () => {
    expect(galleryImages([entry("a.png")], "")[0].path).toBe("a.png");
  });

  it("壊れた行（名前が無い・null）で落ちない", () => {
    const broken = [null, { type: "file" }, entry("a.png")] as unknown as FsEntry[];
    expect(galleryImages(broken, "d").map((i) => i.name)).toEqual(["a.png"]);
    expect(galleryImages(null, "d")).toEqual([]);
  });

  it("mtime は数値のときだけ持ち、0 や文字列は「無い」と同じに扱う", () => {
    const bad = [entry("a.png", { mtime: 0 }), entry("b.png", { mtime: "x" as unknown as number })];
    expect(galleryImages(bad, "").every((i) => i.mtime === undefined)).toBe(true);
    expect(galleryImages([entry("a.png", { mtime: 1757000000 })], "")[0].mtime).toBe(1757000000);
  });
});

describe("並び", () => {
  const withTimes = [
    { name: "old.png", path: "old.png", size: 1, mtime: 100 },
    { name: "new.png", path: "new.png", size: 1, mtime: 300 },
    { name: "mid.png", path: "mid.png", size: 1, mtime: 200 },
  ];

  it("既定は新しい順", () => {
    expect(sortImages(withTimes, "new").map((i) => i.name)).toEqual(["new.png", "mid.png", "old.png"]);
  });

  it("名前順は数字を数として比べる（image-2 が image-10 の前）", () => {
    const names = ["image-10.png", "image-2.png", "image-1.png"].map((name) => ({ name, path: name, size: 1 }));
    expect(sortImages(names, "name").map((i) => i.name)).toEqual(["image-1.png", "image-2.png", "image-10.png"]);
  });

  it("同じ秒に書かれた 2 枚は名前で決まる（更新のたびに入れ替わらない）", () => {
    const tie = [
      { name: "b.png", path: "b.png", size: 1, mtime: 100 },
      { name: "a.png", path: "a.png", size: 1, mtime: 100 },
    ];
    expect(sortImages(tie, "new").map((i) => i.name)).toEqual(["a.png", "b.png"]);
  });

  it("mtime を返さない Agent では新しい順が名前順に落ちる", () => {
    const noTimes = [
      { name: "b.png", path: "b.png", size: 1 },
      { name: "a.png", path: "a.png", size: 1 },
    ];
    expect(hasTimes(noTimes)).toBe(false);
    expect(effectiveSort(noTimes, "new")).toBe("name");
    expect(sortImages(noTimes, "new").map((i) => i.name)).toEqual(["a.png", "b.png"]);
  });

  it("1 枚でも mtime を欠くと名前順に落ちる（欠けた分が最古に見えるのを避ける）", () => {
    const mixed = [...withTimes, { name: "z.png", path: "z.png", size: 1 }];
    expect(hasTimes(mixed)).toBe(false);
    expect(effectiveSort(mixed, "new")).toBe("name");
  });

  it("元の配列を書き換えない", () => {
    const before = withTimes.map((i) => i.name);
    sortImages(withTimes, "new");
    expect(withTimes.map((i) => i.name)).toEqual(before);
  });
});

describe("打ち切りと合計", () => {
  const many = Array.from({ length: PAGE_SIZE + 12 }, (_, n) => ({
    name: `i${n}.png`,
    path: `i${n}.png`,
    size: 10,
  }));

  it("既定は 300 枚で打ち切り、「さらに表示」で続きが出る", () => {
    expect(visibleImages(many, PAGE_SIZE)).toHaveLength(PAGE_SIZE);
    expect(visibleImages(many, PAGE_SIZE * 2)).toHaveLength(many.length);
    expect(visibleImages(many, PAGE_SIZE)[PAGE_SIZE - 1].name).toBe(`i${PAGE_SIZE - 1}.png`);
  });

  it("合計は打ち切り後ではなくフォルダ全体", () => {
    expect(galleryTotals(many)).toEqual({ count: PAGE_SIZE + 12, bytes: (PAGE_SIZE + 12) * 10 });
    expect(galleryTotals([])).toEqual({ count: 0, bytes: 0 });
  });
});

describe("開いたときに拡大する 1 枚（galleryFocus）", () => {
  const images = [
    { name: "a.png", path: "d/a.png", size: 1 },
    { name: "b.png", path: "d/b.png", size: 1 },
  ];

  it("ファイル名でもフルパスでも当たる（呼ぶ側が別レーンなので両方受ける）", () => {
    expect(focusIndex(images, "b.png")).toBe(1);
    expect(focusIndex(images, "d/b.png")).toBe(1);
  });

  it("無い画像・未指定は -1", () => {
    expect(focusIndex(images, "gone.png")).toBe(-1);
    expect(focusIndex(images, undefined)).toBe(-1);
  });
});

describe("ペイン種別 gallery の定型", () => {
  it("保存値から復元でき、余計な欄は落ちる", () => {
    expect(
      validateStoredContent({ kind: "gallery", galleryPath: "repos/x/img", sort: "name", galleryFocus: "a.png", junk: 1 }),
    ).toEqual({ kind: "gallery", galleryPath: "repos/x/img", sort: "name", galleryFocus: "a.png" });
  });

  it("sort は 2 値だけ、無ければ落とす（既定は面が決める）", () => {
    expect(validateStoredContent({ kind: "gallery", galleryPath: "d", sort: "size" })).toEqual({
      kind: "gallery",
      galleryPath: "d",
    });
  });

  it("空白を含むふつうのフォルダ名は通す（弾きすぎない）", () => {
    expect(validateStoredContent({ kind: "gallery", galleryPath: "repos/my images" })).toEqual({
      kind: "gallery",
      galleryPath: "repos/my images",
    });
  });

  it("危ないパスは直さず弾く（空ターミナルに落ちる）", () => {
    for (const galleryPath of ["/etc", "../secrets", "a/../../b", "a/\u0000b", "x".repeat(513)]) {
      expect(validateStoredContent({ kind: "gallery", galleryPath })).toEqual({ kind: "terminal", chat: false });
    }
    expect(validateStoredContent({ kind: "gallery" })).toEqual({ kind: "terminal", chat: false });
  });

  it("galleryFocus と gallerySession は不正なら欄ごと落ち、フォルダは生き残る", () => {
    expect(
      validateStoredContent({ kind: "gallery", galleryPath: "d", galleryFocus: "../x.png", gallerySession: "no spaces" }),
    ).toEqual({ kind: "gallery", galleryPath: "d" });
    expect(validateStoredContent({ kind: "gallery", galleryPath: "d", gallerySession: "slot01" })).toEqual({
      kind: "gallery",
      galleryPath: "d",
      gallerySession: "slot01",
    });
  });

  it("同一判定はフォルダだけ（同じフォルダを 2 度開いても 2 枚にならない）", () => {
    const open = view({ kind: "gallery", galleryPath: "d", sort: "new" });
    expect(sameTarget(open, { content: { kind: "gallery", galleryPath: "d", sort: "name", galleryFocus: "a.png" } })).toBe(true);
    expect(sameTarget(open, { content: { kind: "gallery", galleryPath: "other" } })).toBe(false);
    expect(sameTarget(open, { content: { kind: "file", filePath: "d" } })).toBe(false);
  });

  it("タイトルはセッション名が解決できればそれ、無ければフォルダ名", () => {
    const content: PaneContent = { kind: "gallery", galleryPath: ".cache/agent-fleet/generated/uuid", gallerySession: "slot01" };
    const session = { name: "slot01", title: "絵を描く" } as Session;
    expect(paneTitle(view(content), null, { gallerySession: session })).toBe("生成した画像 — 絵を描く");
    expect(paneTitle(view(content), null)).toBe("uuid");
  });
});

describe("フォルダと、その行き来", () => {
  const entries: FsEntry[] = [
    { name: "b.png", type: "file", size: 10 },
    { name: "zz", type: "dir" },
    { name: "img10", type: "dir" },
    { name: "img9", type: "dir" },
    { name: "", type: "dir" },
  ];

  it("ディレクトリだけを名前順で拾う（数字は数として比べる）", () => {
    expect(galleryFolders(entries, "gen")).toEqual([
      { name: "img9", path: "gen/img9" },
      { name: "img10", path: "gen/img10" },
      { name: "zz", path: "gen/zz" },
    ]);
    // ルート直下ではパスに余計な / を付けない。
    expect(galleryFolders([{ name: "a", type: "dir" }], "")).toEqual([{ name: "a", path: "a" }]);
    expect(galleryFolders(null, "gen")).toEqual([]);
  });

  it("画像とフォルダは互いに混ざらない", () => {
    expect(galleryImages(entries, "gen").map((i) => i.name)).toEqual(["b.png"]);
  });

  it("上へはブラウズルート（空文字）まで戻れて、そこで止まる", () => {
    expect(parentPath("a/b/c")).toBe("a/b");
    expect(parentPath("a")).toBe("");
    // ルートには親が無い＝「上へ」を出さない、が null の意味。
    expect(parentPath("")).toBeNull();
  });

  it("パンくずは各段のパスを持ち、ルートは持たない（名前が無いため）", () => {
    expect(breadcrumb(".cache/agent-fleet/generated")).toEqual([
      { name: ".cache", path: ".cache" },
      { name: "agent-fleet", path: ".cache/agent-fleet" },
      { name: "generated", path: ".cache/agent-fleet/generated" },
    ]);
    expect(breadcrumb("")).toEqual([]);
  });

  it("ルート（空文字）は保存できる正しいギャラリー内容である", () => {
    // 「上へ」でたどり着ける状態なのに再読み込みで空ターミナルに化ける、が無いこと。
    expect(validateStoredContent({ kind: "gallery", galleryPath: "" })).toEqual({ kind: "gallery", galleryPath: "" });
    // 鍵ごと無いのは今までどおり拒む。
    expect(validateStoredContent({ kind: "gallery" })).toEqual({ kind: "terminal", chat: false });
  });
});
