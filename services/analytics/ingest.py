"""Kafka events -> events_log rows. Order lines become one row each so product and category queries stay flat."""

import json
import logging
from datetime import datetime
from decimal import Decimal

import httpx

log = logging.getLogger("analytics")

TOPICS = [
    "order.placed", "order.status.changed", "payment.completed",
    "piece.registered", "piece.handoff.signed",
    "product.changed",
]


def rows(event: dict, category_of) -> list[dict]:
    """category_of(productId) -> categoryId or 0 when unknown."""
    p, kind = event["payload"], event["eventType"]
    base = {
        "event_id": event["eventId"], "line": 0, "event_type": kind,
        "ts": datetime.fromisoformat(event["timestamp"].replace("Z", "+00:00")),
        "order_id": "", "product_id": 0, "wholesaler_id": "", "category_id": 0,
        "status": "", "qty": 0, "amount": Decimal("0"), "metadata": json.dumps(p, default=str),
    }
    if kind == "OrderPlaced":
        return [
            base | {
                "line": i, "order_id": p["orderId"], "wholesaler_id": p["wholesalerId"], "status": p["status"],
                "product_id": int(item["productId"]), "category_id": category_of(int(item["productId"])),
                "qty": int(item["qty"]), "amount": Decimal(str(item["unitPrice"])) * int(item["qty"]),
            }
            for i, item in enumerate(p["items"])
        ]
    if kind in ("OrderStatusChanged", "PaymentCompleted"):
        amount = Decimal(str(p["total"])) if kind == "PaymentCompleted" else Decimal("0")
        return [base | {"order_id": p["orderId"], "wholesaler_id": p["wholesalerId"], "status": p["status"], "amount": amount}]
    if kind.startswith("Product"):
        return [base | {"product_id": int(p["productId"]), "wholesaler_id": p.get("wholesalerId") or "", "category_id": int(p.get("categoryId") or 0)}]
    # Piece events: kept for the record; productId may be absent.
    return [base | {"product_id": int(p.get("productId") or 0)}]


class CategoryLookup:
    """productId -> categoryId from the catalog, cached; a product's category rarely changes."""

    def __init__(self, base_url: str):
        self._http = httpx.Client(base_url=base_url, timeout=5.0)
        self._cache: dict[int, int] = {}

    def __call__(self, product_id: int) -> int:
        if product_id not in self._cache:
            try:
                res = self._http.get(f"/products/{product_id}")
                res.raise_for_status()
                self._cache[product_id] = int(res.json()["data"].get("categoryId") or 0)
            except (httpx.HTTPError, ValueError, KeyError):
                # shortcut: an unreachable catalog files the line under category 0 for good; add a backfill job if trends need to be exact.
                log.warning("no category for product %s", product_id)
                return 0
        return self._cache[product_id]


def run(store, category_of, brokers: str) -> None:
    """Blocking poll loop; main.py runs it in a daemon thread."""
    from confluent_kafka import Consumer  # imported lazily so tests don't need librdkafka

    consumer = Consumer({
        "bootstrap.servers": brokers, "group.id": "analytics", "auto.offset.reset": "earliest",
        # Topics are auto-created by the first producer; notice them in seconds, not the 5-minute default.
        "topic.metadata.refresh.interval.ms": 10000,
    })
    consumer.subscribe(TOPICS)
    while True:
        msg = consumer.poll(1.0)
        if msg is None:
            continue
        if msg.error():
            log.warning("kafka: %s", msg.error())
            continue
        try:
            store.insert(rows(json.loads(msg.value()), category_of))
        except Exception:  # noqa: BLE001 - one bad event must not stop the loop
            log.exception("skipping event at %s[%s]@%s", msg.topic(), msg.partition(), msg.offset())
