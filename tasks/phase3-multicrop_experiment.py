"""Compare scoring schemes for multi-crop matching on the calibration designs."""
import io
import sys

sys.path.insert(0, "/app")
from PIL import Image  # noqa: E402

from embedder import ClipEmbedder  # noqa: E402
from test_calibration import TRANSFORMS, design, jpeg  # noqa: E402

clip = ClipEmbedder()


def views(img: Image.Image) -> list[list[float]]:
    w, h = img.size
    out = [img]
    for i in range(3):
        for j in range(3):
            x, y = int(i * w / 4), int(j * h / 4)
            out.append(img.crop((x, y, x + w // 2, y + h // 2)))
    return [clip.embed(jpeg(v)) for v in out]


def cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def chamfer(Q, R):
    return sum(max(cos(q, r) for r in R) for q in Q) / len(Q)


schemes = {
    "S0 whole-whole": lambda Q, R: cos(Q[0], R[0]),
    "S1 whole-vs-best-ref-view": lambda Q, R: max(cos(Q[0], r) for r in R),
    "S2 chamfer Q->R": chamfer,
    "S3 symmetric chamfer": lambda Q, R: (chamfer(Q, R) + chamfer(R, Q)) / 2,
}

def hue_hist(img: Image.Image, bins: int = 36) -> list[float]:
    # Hue survives brightness changes; low-saturation pixels (white fill, greys) have no stable hue.
    h = [0.0] * bins
    for hh, ss, vv in img.convert("HSV").resize((128, 128)).getdata():
        if ss > 60 and vv > 40:
            h[hh * bins // 256] += 1
    total = sum(h) or 1
    return [x / total for x in h]


def hue_sim(a, b):
    return sum(min(x, y) for x, y in zip(a, b))


seeds = range(8)
refs = {s: (views(design(s)), hue_hist(design(s))) for s in seeds}
photos = {(s, n): (views(t(design(s)).convert("RGB")), hue_hist(t(design(s)).convert("RGB"))) for s in seeds for n, t in TRANSFORMS.items()}

base = dict(schemes)
schemes = {f"{n} (CLIP)": (lambda f: lambda P, R: f(P[0], R[0]))(f) for n, f in base.items()}
schemes["S4 S3 x hue"] = lambda P, R: base["S3 symmetric chamfer"](P[0], R[0]) * hue_sim(P[1], R[1])
schemes["S5 S0 x hue"] = lambda P, R: base["S0 whole-whole"](P[0], R[0]) * hue_sim(P[1], R[1])
schemes["S6 hue only"] = lambda P, R: hue_sim(P[1], R[1])

for name, score in schemes.items():
    same = [(k, score(Q, refs[k[0]])) for k, Q in photos.items()]
    # Hard negatives: every transformed photo against every *other* design, plus reference pairs.
    diff = [((k, b), score(Q, refs[b])) for k, Q in photos.items() for b in seeds if b != k[0]]
    lo, hi = min(same, key=lambda x: x[1]), max(diff, key=lambda x: x[1])
    print(f"{name:28s} lowest same {lo[1]:.3f} {lo[0]}   highest diff {hi[1]:.3f} {hi[0]}   gap {lo[1] - hi[1]:+.3f}")
