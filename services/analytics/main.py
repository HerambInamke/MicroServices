import logging
import os
import sys
import threading
import time

import uvicorn

import ingest
from app import create_app
from store import ClickHouseStore

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("analytics")


def require_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        log.error("%s is required", name)
        sys.exit(1)
    return value


def connect(url: str) -> ClickHouseStore:
    """Retries because ClickHouse may still be starting when the container comes up."""
    for attempt in range(30):
        try:
            store = ClickHouseStore.connect(url)
            store.ensure()
            return store
        except Exception as e:  # noqa: BLE001 - any connection failure is retried
            log.info("waiting for clickhouse (%s): %s", attempt + 1, e)
            time.sleep(2)
    log.error("clickhouse unavailable")
    sys.exit(1)


def build_app():
    store = connect(require_env("CLICKHOUSE_URL"))
    # The consumer gets its own client: clickhouse-connect clients are not safe to share across threads.
    consumer_store = ClickHouseStore.connect(require_env("CLICKHOUSE_URL"))
    category_of = ingest.CategoryLookup(require_env("CATALOG_URL"))
    threading.Thread(target=ingest.run, args=(consumer_store, category_of, require_env("KAFKA_BROKERS")), daemon=True).start()
    return create_app(store)


if __name__ == "__main__":
    uvicorn.run(build_app(), host="0.0.0.0", port=int(os.getenv("PORT", "8091")))
