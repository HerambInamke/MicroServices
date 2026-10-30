"""Outbound calls: Wholesale Catalog (product), Media/Upload (reference image) and Kafka (design events)."""

import json
import logging
from datetime import datetime, timezone
from uuid import uuid4

import httpx

log = logging.getLogger("counterfeit-detection")


class UpstreamError(Exception):
    """A lookup failed. status/code/message map onto our response."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


class CatalogClient:
    def __init__(self, base_url: str, timeout: float = 5.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def product(self, product_id: int) -> dict:
        """The catalog's product view: wholesalerId, name, ..."""
        try:
            res = self._http.get(f"/products/{product_id}")
        except httpx.HTTPError as e:
            raise UpstreamError(502, "CATALOG_UNAVAILABLE", "could not reach the catalog") from e
        if res.status_code == 404:
            raise UpstreamError(404, "PRODUCT_NOT_FOUND", f"product {product_id} not found")
        if res.status_code != 200:
            raise UpstreamError(502, "CATALOG_UNAVAILABLE", "catalog returned an unexpected response")
        return res.json()["data"]


class MediaClient:
    def __init__(self, base_url: str, timeout: float = 10.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def _get(self, path: str) -> httpx.Response:
        try:
            res = self._http.get(path)
        except httpx.HTTPError as e:
            raise UpstreamError(502, "MEDIA_UNAVAILABLE", "could not reach media storage") from e
        if res.status_code == 404:
            raise UpstreamError(404, "MEDIA_NOT_FOUND", "media not found")
        if res.status_code != 200:
            raise UpstreamError(502, "MEDIA_UNAVAILABLE", "media storage returned an unexpected response")
        return res

    def get(self, media_id: str) -> dict:
        """{id, ownerId, contentType, type, url, ...}"""
        return self._get(f"/media/{media_id}").json()["data"]

    def content(self, media_id: str) -> bytes:
        return self._get(f"/media/{media_id}/content").content


class KafkaPublisher:
    """Publishes the shared event envelope from schemas/events.json."""

    def __init__(self, brokers: str):
        from confluent_kafka import Producer  # imported lazily so tests don't need librdkafka

        self._producer = Producer({"bootstrap.servers": brokers, "enable.idempotence": True})

    def publish(self, topic: str, event_type: str, key: str, payload: dict) -> None:
        # shortcut: published after the Weaviate write without an outbox, so an event is lost if Kafka is down at that moment; POST /search/reindex repairs Search.
        event = {
            "eventId": str(uuid4()),
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "sourceService": "counterfeit-detection",
            "eventType": event_type,
            "payload": payload,
        }
        try:
            self._producer.produce(topic, key=key.encode(), value=json.dumps(event).encode(), on_delivery=_on_delivery)
            if self._producer.flush(5) > 0:
                log.error("event %s for %s not delivered within timeout", event_type, key)
        except Exception:  # noqa: BLE001 - the design change is already stored; never fail the request over an event
            log.exception("could not publish %s for %s", event_type, key)


def _on_delivery(err, msg) -> None:
    if err is not None:
        log.error("kafka delivery failed for %s: %s", msg.topic(), err)
