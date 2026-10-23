import logging
import os
import sys
import threading
import time

import uvicorn
from elasticsearch import Elasticsearch

import consumer
from app import create_app
from clients import Source
from index import SearchIndex

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("search")


def require_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        log.error("%s is required", name)
        sys.exit(1)
    return value


def connect(url: str) -> SearchIndex:
    """Retries because Elasticsearch takes a while to start."""
    index = SearchIndex(Elasticsearch(url, request_timeout=10))
    for attempt in range(60):
        try:
            index.ensure()
            return index
        except Exception as e:  # noqa: BLE001 - any connection failure is retried
            log.info("waiting for elasticsearch (%s): %s", attempt + 1, e)
            time.sleep(2)
    log.error("elasticsearch unavailable")
    sys.exit(1)


def build_app():
    index = connect(require_env("ELASTICSEARCH_URL"))
    threading.Thread(target=consumer.run, args=(index, require_env("KAFKA_BROKERS")), daemon=True).start()
    return create_app(
        index,
        products=Source("catalog", require_env("CATALOG_URL"), "/products"),
        profiles=Source("business-profile", require_env("PROFILE_URL"), "/profiles"),
        designs=Source("counterfeit-detection", require_env("COUNTERFEIT_URL"), "/designs"),
    )


if __name__ == "__main__":
    uvicorn.run(build_app(), host="0.0.0.0", port=int(os.getenv("PORT", "8089")))
