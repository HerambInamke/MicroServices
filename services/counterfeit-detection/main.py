import logging
import os
import sys
import time

import uvicorn

from app import DEFAULT_MATCH_THRESHOLD, create_app
from clients import CatalogClient, MediaClient
from embedder import ClipEmbedder
from store import WeaviateStore

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("counterfeit-detection")


def require_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        log.error("%s is required", name)
        sys.exit(1)
    return value


def connect(url: str) -> WeaviateStore:
    """Retries because Weaviate may still be starting when the container comes up."""
    for attempt in range(30):
        try:
            store = WeaviateStore.connect(url)
            store.ensure_collection()
            return store
        except Exception as e:  # noqa: BLE001 - any connection failure is retried
            log.info("waiting for weaviate (%s): %s", attempt + 1, e)
            time.sleep(2)
    log.error("weaviate unavailable")
    sys.exit(1)


def build_app():
    store = connect(require_env("WEAVIATE_URL"))
    threshold = float(os.getenv("MATCH_THRESHOLD") or DEFAULT_MATCH_THRESHOLD)
    catalog, media = CatalogClient(require_env("CATALOG_URL")), MediaClient(require_env("MEDIA_URL"))
    return create_app(store, ClipEmbedder(), catalog, media, threshold)


if __name__ == "__main__":
    uvicorn.run(build_app(), host="0.0.0.0", port=int(os.getenv("PORT", "8088")))
