import logging
import os
import sys
import threading
import time

from sqlalchemy import create_engine, text
from sqlalchemy.orm import sessionmaker

from app import create_app, init_db
from clients import CatalogClient, ChatClient, InventoryClient, KafkaPublisher, RazorpayClient
from outbox import run_relay

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("order-payment")


def require_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        log.error("%s is required", name)
        sys.exit(1)
    return value


def connect(url: str):
    """Retries because Postgres may still be starting when the container comes up."""
    engine = create_engine(url, pool_pre_ping=True)
    for attempt in range(30):
        try:
            with engine.connect() as conn:
                conn.execute(text("SELECT 1"))
            return engine
        except Exception as e:  # noqa: BLE001 - any connection failure is retried
            log.info("waiting for database (%s): %s", attempt + 1, e)
            time.sleep(2)
    log.error("database unavailable")
    sys.exit(1)


def build_app():
    engine = connect(require_env("DB_URL"))
    init_db(engine)

    key_id, key_secret = os.getenv("RAZORPAY_KEY_ID"), os.getenv("RAZORPAY_KEY_SECRET")
    razorpay = RazorpayClient(key_id, key_secret) if key_id and key_secret else None
    if razorpay is None:
        log.warning("RAZORPAY_KEY_ID/RAZORPAY_KEY_SECRET not set: payment endpoints will return 503")

    sessions = sessionmaker(engine, expire_on_commit=False)
    kafka = KafkaPublisher(require_env("KAFKA_BROKERS"))
    threading.Thread(target=run_relay, args=(sessions, kafka.send), daemon=True).start()
    return create_app(
        sessions,
        catalog=CatalogClient(require_env("CATALOG_URL")),
        inventory=InventoryClient(require_env("INVENTORY_URL")),
        chat=ChatClient(require_env("CHAT_URL")),
        razorpay=razorpay,
        webhook_secret=os.getenv("RAZORPAY_WEBHOOK_SECRET", ""),
    )


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("PORT", 8084))
    uvicorn.run(build_app(), host="0.0.0.0", port=port)
