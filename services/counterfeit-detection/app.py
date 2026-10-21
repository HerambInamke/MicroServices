import logging

from fastapi import FastAPI

log = logging.getLogger("counterfeit-detection")

# Cosine similarity at or above which a photo counts as the registered design.
# Set from test_calibration.py; override with MATCH_THRESHOLD after tuning on real photos.
DEFAULT_MATCH_THRESHOLD = 0.9


def create_app(store, embedder, threshold: float = DEFAULT_MATCH_THRESHOLD) -> FastAPI:
    app = FastAPI(title="counterfeit-detection")

    @app.get("/health")
    def health_check():
        return {"status": "ok", "service": "counterfeit-detection"}

    return app
