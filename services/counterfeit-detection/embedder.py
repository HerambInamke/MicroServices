"""Image -> CLIP embedding. The weights are baked into the Docker image (see Dockerfile)."""

import io

import open_clip
import torch
from PIL import Image

MODEL, PRETRAINED = "ViT-B-32", "laion2b_s34b_b79k"

# Refuse decompression bombs: a 10 MiB upload can still claim billions of pixels.
Image.MAX_IMAGE_PIXELS = 40_000_000


class ClipEmbedder:
    def __init__(self):
        self.model, _, self.preprocess = open_clip.create_model_and_transforms(MODEL, pretrained=PRETRAINED)
        self.model.eval()

    def embed(self, data: bytes) -> list[float]:
        """Unit-length 512-d vector, so cosine similarity is a plain dot product."""
        image = Image.open(io.BytesIO(data)).convert("RGB")
        with torch.no_grad():
            v = self.model.encode_image(self.preprocess(image).unsqueeze(0))
            v = v / v.norm(dim=-1, keepdim=True)
        return v[0].tolist()
