# counterfeit-detection

Python/FastAPI + CLIP (open_clip ViT-B-32, baked into the image) · port **8088** · Weaviate.

A registry of design images tied to catalog products. Anyone can check a photo against it. A second
wholesaler cannot register a design that matches one already registered.

## Endpoints

| Method & path | Who |
|---|---|
| `POST /designs` `{productId, mediaId}` | the product's wholesaler (409 `DESIGN_CONFLICT` if another seller's design matches) |
| `POST /designs/match` (multipart image) → best matches with scores | logged-in user |
| `GET /designs`, `GET /designs/:id` | public |

## Configuration

`WEAVIATE_URL`, `CATALOG_URL`, `MEDIA_URL`, `PORT`, and `MATCH_THRESHOLD` (optional).

**Known limitation:** the default threshold of 0.9 is a placeholder. It has not been calibrated on real saree
photos yet: on synthetic patterns, CLIP could not separate designs reliably.

## Tests

```bash
pip install -r requirements-dev.txt && pytest -q          # fake embedder and store
TEST_WEAVIATE_URL=http://localhost:8080 pytest -q          # plus real Weaviate
TEST_CLIP=1 CALIBRATION_DIR=<photos> pytest -s test_calibration.py   # threshold calibration on <design>/<photo>.jpg
```
