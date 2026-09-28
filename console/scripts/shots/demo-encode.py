#!/usr/bin/env python3
"""Encode the demo recording's screencast frames into one animated WebP.

    python3 demo-encode.py <frames.json> <out.webp> [--fps 12] [--effort 100]

frames.json is what demo.mjs writes: {"frames": [{"file": "<png>", "t": <seconds>}], "end": <seconds>}.
Each frame is shown until the next one's timestamp, the last until "end".

Pillow's WebP writer is libwebp's WebPAnimEncoder, which stores only the rectangle that changed
since the previous frame: a UI recording is mostly still, so this is what keeps a 45-second
capture at a few megabytes. There is no ffmpeg or gifski in the workspace image, and Pillow is one
`pip install --user pillow` away.

Lossless, not lossy. Measured on these recordings: lossless is no larger (553 KB against 586 KB at
lossy q75 for the review scenario), and lossy leaves ghosts — a region that changes once and then
stays still keeps the lossy approximation of what was there before (a tapped finger stayed visible
with a per-channel error up to 26 until the next keyframe).
"""
import argparse
import hashlib
import json
import os
import resource
import sys

from PIL import Image


def pick(frames, end, fps):
    """Thin a variable-rate screencast to at most `fps` frames a second.

    Chromium sends a frame whenever the compositor draws, up to 60 a second during an animation.
    Within one 1/fps slot only the LAST frame is kept, shown from the slot's first timestamp: the
    state a transition settles on survives, and the in-between frames it drops were motion blur
    nobody sees at README size. Byte-identical neighbours are merged the same way."""
    slot = 1.0 / fps
    kept = []  # [file, t, digest]
    for f in sorted(frames, key=lambda f: f["t"]):
        with open(f["file"], "rb") as fh:
            digest = hashlib.sha1(fh.read()).hexdigest()
        if kept and (f["t"] - kept[-1][1] < slot or digest == kept[-1][2]):
            if digest != kept[-1][2]:
                kept[-1][0], kept[-1][2] = f["file"], digest
            continue
        kept.append([f["file"], f["t"], digest])
    durations = []
    for i, (_, t, _) in enumerate(kept):
        nxt = kept[i + 1][1] if i + 1 < len(kept) else end
        durations.append(max(20, round((nxt - t) * 1000)))
    return [k[0] for k in kept], durations


class ReleasingDurations(list):
    """The per-frame duration list, which also frees each frame once the encoder has taken it.

    Pillow materialises every appended image and keeps it loaded until save() returns — about
    4 MB per 1280x800 frame, so a few hundred frames would hold over a gigabyte on a shared,
    memory-capped host. Its encode loop reads duration[i] right after adding frame i, so that
    read is the point frame i can be closed. If a later Pillow reads durations at another moment,
    this only stops saving memory; the output does not change."""

    def __init__(self, values, images):
        super().__init__(values)
        self.images = images

    def __getitem__(self, i):
        v = super().__getitem__(i)
        if isinstance(i, int) and 0 < i < len(self.images):
            self.images[i].close()
        return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("frames")
    ap.add_argument("out")
    ap.add_argument("--fps", type=float, default=12)
    # For lossless WebP, "quality" is compression effort, not fidelity.
    ap.add_argument("--effort", type=int, default=100)
    a = ap.parse_args()

    with open(a.frames) as fh:
        spec = json.load(fh)
    files, durations = pick(spec["frames"], spec["end"], a.fps)
    if len(files) < 2:
        sys.exit("demo-encode: fewer than two frames — nothing was recorded")

    images = [Image.open(f) for f in files]
    images[0].save(
        a.out,
        save_all=True,
        append_images=images[1:],
        duration=ReleasingDurations(durations, images),
        loop=0,
        lossless=True,
        quality=a.effort,
        method=4,
        # Keyframes are full frames. A UI recording rarely needs one, and each costs as much as a
        # screenshot, so they are spaced ~8 s apart. libwebp requires kmin > kmax / 2.
        kmin=int(a.fps * 4) + 1,
        kmax=int(a.fps * 8),
    )
    for im in images:
        im.close()
    peak_mb = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss // 1024
    size_kb = os.path.getsize(a.out) // 1024
    print(f"[encode] {a.out}: {len(files)} frames, {sum(durations) / 1000:.1f}s, {size_kb} KB (peak RSS {peak_mb} MB)")


if __name__ == "__main__":
    main()
