"""Outbound integrations: Wholesale Catalog (pricing), Chat (agreed offers), Razorpay (payments) and Kafka (events)."""

import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal
from urllib.parse import quote
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


class ChatError(Exception):
    """Chat has no such offer or could not be reached."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


@dataclass(frozen=True)
class Offer:
    id: str
    product_id: str
    qty: int
    unit_price: Decimal
    status: str
    wholesaler_id: str
    retailer_id: str


class ChatClient:
    """Internal Chat endpoint; reached directly on the compose network, never through the gateway."""

    def __init__(self, base_url: str, timeout: float = 5.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def offer(self, offer_id: str) -> Offer:
        try:
            res = self._http.get(f"/chat/offers/{quote(offer_id, safe='')}")
        except httpx.HTTPError as e:
            raise ChatError(502, "CHAT_UNAVAILABLE", "could not reach chat") from e
        if res.status_code == 404:
            raise ChatError(404, "OFFER_NOT_FOUND", f"offer {offer_id} not found")
        if res.status_code != 200:
            raise ChatError(502, "CHAT_UNAVAILABLE", "chat returned an unexpected response")
        d = json.loads(res.text, parse_float=Decimal)["data"]
        return Offer(d["id"], d["productId"], int(d["qty"]), Decimal(d["unitPrice"]), d["status"], d["wholesalerId"], d["retailerId"])


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
    """Sends outbox rows (the shared envelope from schemas/events.json) for outbox.relay_once."""

    def __init__(self, brokers: str):
        from confluent_kafka import Producer  # imported lazily so tests don't need librdkafka

        self._producer = Producer({"bootstrap.servers": brokers, "enable.idempotence": True})

    def send(self, topic: str, key: str, value: str) -> bool:
        """True once Kafka acknowledged the message. One at a time, so the outbox order is kept."""
        failed = []
        try:
            self._producer.produce(topic, key=key.encode(), value=value.encode(),
                                   on_delivery=lambda err, msg: err is not None and failed.append(err))
            if self._producer.flush(5) > 0:
                # Drop it from the local queue: the outbox row stays and is resent, so it must not pile up here too.
                self._producer.purge()
                self._producer.flush(0)
                log.warning("kafka did not acknowledge %s for %s in time; will retry", topic, key)
                return False
        except Exception:  # noqa: BLE001 - e.g. local queue full; the outbox row stays and is retried
            log.exception("could not send %s for %s", topic, key)
            return False
        if failed:
            log.warning("kafka delivery failed for %s %s: %s; will retry", topic, key, failed[0])
            return False
        return True


def envelope(event_type: str, payload: dict) -> dict:
    return {
        "eventId": str(uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "sourceService": "order-payment",
        "eventType": event_type,
        "payload": payload,
    }
