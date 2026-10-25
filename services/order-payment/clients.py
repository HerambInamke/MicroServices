"""Outbound integrations: Wholesale Catalog (pricing), Razorpay (payments) and Kafka (events)."""

import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal
from uuid import uuid4

import httpx

log = logging.getLogger("order-payment")


class CatalogError(Exception):
    """Catalog rejected the line or could not be reached. status/code/message map onto our response."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


@dataclass(frozen=True)
class Quote:
    product_id: int
    wholesaler_id: str
    qty: int
    unit_price: Decimal
    total: Decimal


class CatalogClient:
    def __init__(self, base_url: str, timeout: float = 5.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def quote(self, product_id: int, qty: int) -> Quote:
        try:
            res = self._http.get(f"/products/{product_id}/quote", params={"qty": qty})
        except httpx.HTTPError as e:
            raise CatalogError(502, "CATALOG_UNAVAILABLE", "could not reach the catalog") from e
        # Parse money as Decimal, never float.
        body = json.loads(res.text, parse_float=Decimal) if res.text else {}
        if res.status_code == 404:
            raise CatalogError(404, "PRODUCT_NOT_FOUND", f"product {product_id} not found")
        if res.status_code == 422:
            raise CatalogError(422, "BELOW_MOQ", body.get("error", {}).get("message", "below minimum order quantity"))
        if res.status_code != 200:
            raise CatalogError(502, "CATALOG_UNAVAILABLE", "catalog returned an unexpected response")
        d = body["data"]
        return Quote(int(d["productId"]), d["wholesalerId"], int(d["qty"]), Decimal(d["unitPrice"]), Decimal(d["total"]))


class InventoryError(Exception):
    """Inventory refused the reservation or could not be reached."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


class InventoryClient:
    """Internal Inventory Sync endpoints; reached directly on the compose network, never through the gateway."""

    def __init__(self, base_url: str, timeout: float = 5.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def reserve(self, order_id: str, lines: list[tuple[int, int]]) -> None:
        items = [{"productId": pid, "qty": qty} for pid, qty in lines]
        try:
            res = self._http.post("/inventory/reserve", json={"orderId": order_id, "items": items})
        except httpx.HTTPError as e:
            raise InventoryError(502, "INVENTORY_UNAVAILABLE", "could not reach inventory") from e
        if res.status_code == 409:
            raise InventoryError(409, "OUT_OF_STOCK", res.json()["error"]["message"])
        if res.status_code != 200:
            raise InventoryError(502, "INVENTORY_UNAVAILABLE", "inventory returned an unexpected response")

    def release(self, order_id: str) -> None:
        """Best effort: the caller's own change is already decided, so failures are only logged."""
        try:
            self._http.post("/inventory/release", json={"orderId": order_id}).raise_for_status()
        except httpx.HTTPError:
            log.exception("could not release stock for order %s; it stays reserved", order_id)


class PaymentProviderError(Exception):
    pass


class RazorpayClient:
    API = "https://api.razorpay.com/v1"

    def __init__(self, key_id: str, key_secret: str, timeout: float = 10.0):
        self.key_id = key_id
        self._http = httpx.Client(base_url=self.API, auth=(key_id, key_secret), timeout=timeout)

    def create_order(self, amount_paise: int, currency: str, receipt: str) -> str:
        try:
            res = self._http.post("/orders", json={"amount": amount_paise, "currency": currency, "receipt": receipt})
            res.raise_for_status()
            return res.json()["id"]
        except (httpx.HTTPError, KeyError, ValueError) as e:
            raise PaymentProviderError(str(e)) from e


class KafkaPublisher:
    """Publishes the shared event envelope from schemas/events.json."""

    def __init__(self, brokers: str):
        from confluent_kafka import Producer  # imported lazily so tests don't need librdkafka

        self._producer = Producer({"bootstrap.servers": brokers, "enable.idempotence": True})

    def publish(self, topic: str, event_type: str, key: str, payload: dict) -> None:
        # shortcut: published after the DB commit without an outbox, so an event is lost if Kafka is down at that moment; add a transactional outbox before consumers depend on it.
        event = envelope(event_type, payload)
        try:
            self._producer.produce(topic, key=key.encode(), value=json.dumps(event, default=str).encode(), on_delivery=_on_delivery)
            if self._producer.flush(5) > 0:
                log.error("event %s for %s not delivered within timeout", event_type, key)
        except Exception:  # noqa: BLE001 - the DB change is already committed; never fail the request over an event
            log.exception("could not publish %s for %s", event_type, key)


def envelope(event_type: str, payload: dict) -> dict:
    return {
        "eventId": str(uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "sourceService": "order-payment",
        "eventType": event_type,
        "payload": payload,
    }


def _on_delivery(err, msg) -> None:
    if err is not None:
        log.error("kafka delivery failed for %s: %s", msg.topic(), err)
