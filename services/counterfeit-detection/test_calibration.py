"""Does CLIP tell "the same design, photographed differently" apart from "a different design"?

Opt-in (loads the real model): TEST_CLIP=1 CALIBRATION_DIR=/photos pytest -s test_calibration.py
MATCH_THRESHOLD must sit between the two groups. The procedural designs below are kept for
tasks/phase3-multicrop_experiment.py and the unit-length check.
"""

import io
import os
import random

import pytest
from PIL import Image, ImageDraw, ImageEnhance

from app import DEFAULT_MATCH_THRESHOLD

pytestmark = pytest.mark.skipif(not os.getenv("TEST_CLIP"), reason="TEST_CLIP not set; skipping real-model calibration")


def design(seed: int, size: int = 448) -> Image.Image:
    rng = random.Random(seed)
    color = lambda: tuple(rng.randrange(256) for _ in range(3))  # noqa: E731
    img = Image.new("RGB", (size, size), color())
    d = ImageDraw.Draw(img)
    step = rng.choice([40, 56, 64])
    motif, fill, outline = rng.choice(["ellipse", "diamond", "flower"]), color(), color()
    for y in range(0, size, step):
        for x in range((y // step % 2) * step // 2, size, step):
            r = step // 3
            if motif == "ellipse":
                d.ellipse([x - r, y - r // 2, x + r, y + r // 2], fill=fill, outline=outline, width=3)
            elif motif == "diamond":
                d.polygon([(x, y - r), (x + r, y), (x, y + r), (x - r, y)], fill=fill, outline=outline)
            else:
                for dx, dy in [(-1, 0), (1, 0), (0, -1), (0, 1)]:
                    d.ellipse([x + dx * r // 2 - r // 3, y + dy * r // 2 - r // 3, x + dx * r // 2 + r // 3, y + dy * r // 2 + r // 3], fill=fill)
    band, band_color = rng.randrange(40, 90), color()
    d.rectangle([0, size - band, size, size], fill=band_color)
    for x in range(0, size, 24):
        d.line([(x, size - band), (x + 12, size)], fill=color(), width=4)
    return img


def jpeg(img: Image.Image, quality: int = 90) -> bytes:
    buf = io.BytesIO()
    img.save(buf, "JPEG", quality=quality)
    return buf.getvalue()


# Ways a shopper's photo of the same piece differs from the registered reference.
TRANSFORMS = {
    "crop": lambda im: im.crop((40, 40, 408, 408)),
    "rotate": lambda im: im.rotate(12, expand=False, fillcolor=(255, 255, 255)),
    "recompress": lambda im: Image.open(io.BytesIO(jpeg(im, 35))),
    "brighter": lambda im: ImageEnhance.Brightness(im).enhance(1.35),
    "half": lambda im: im.crop((0, 0, 448, 300)),
}


def cos(a: list[float], b: list[float]) -> float:
    return sum(x * y for x, y in zip(a, b))


@pytest.mark.skipif(not os.getenv("CALIBRATION_DIR"), reason="CALIBRATION_DIR not set")
def test_threshold_separates_real_photos():
    """CALIBRATION_DIR/<design>/<photo>.jpg: every pair in one folder is the same design, across folders different.

    The procedural designs above can't calibrate this: CLIP and DINOv2 both ignore colour, which is
    most of what separates them (see tasks/phase3-todo.md), so the threshold comes from real photos.
    """
    from embedder import ClipEmbedder

    clip = ClipEmbedder()
    root = os.environ["CALIBRATION_DIR"]
    photos = {
        (d, f): clip.embed(open(os.path.join(root, d, f), "rb").read())
        for d in sorted(os.listdir(root)) if os.path.isdir(os.path.join(root, d))
        for f in sorted(os.listdir(os.path.join(root, d)))
    }
    pairs = [(a, b) for a in photos for b in photos if a < b]
    same = {(a, b): cos(photos[a], photos[b]) for a, b in pairs if a[0] == b[0]}
    diff = {(a, b): cos(photos[a], photos[b]) for a, b in pairs if a[0] != b[0]}
    assert same and diff, "need at least 2 design folders with 2+ photos each"

    lowest_same, highest_diff = min(same.items(), key=lambda kv: kv[1]), max(diff.items(), key=lambda kv: kv[1])
    print(f"\nlowest same-design similarity: {lowest_same}\nhighest different-design similarity: {highest_diff}")
    assert lowest_same[1] >= DEFAULT_MATCH_THRESHOLD > highest_diff[1]


def test_embedding_is_unit_length():
    from embedder import ClipEmbedder

    v = ClipEmbedder().embed(jpeg(design(0)))
    assert len(v) == 512
    assert abs(cos(v, v) - 1) < 1e-4
