"""Keeps the index in step with the catalog's product.changed and counterfeit-detection's design.changed events."""

import json
import logging

from index import design_doc, product_doc

log = logging.getLogger("search")

TOPICS = ["product.changed", "design.changed"]


def handle(index, event: dict) -> None:
    p = event["payload"]
    match event["eventType"]:
        case "ProductDeleted":
            index.delete("product", p["productId"])
        case "DesignRegistered":
            design = {"id": p["designId"], "productId": p["productId"], "wholesalerId": p["wholesalerId"]}
            index.upsert(design_doc(design, {p["productId"]: p["productName"]}))
        case "DesignDeleted":
            index.delete("design", p["designId"])
        case _:
            index.upsert(product_doc(p))


def run(index, brokers: str) -> None:
    """Blocking poll loop; main.py runs it in a daemon thread."""
    from confluent_kafka import Consumer  # imported lazily so tests don't need librdkafka

    consumer = Consumer({
        "bootstrap.servers": brokers, "group.id": "search", "auto.offset.reset": "earliest",
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
            handle(index, json.loads(msg.value()))
        except Exception:  # noqa: BLE001 - one bad event must not stop the loop
            log.exception("skipping event at %s[%s]@%s", msg.topic(), msg.partition(), msg.offset())
