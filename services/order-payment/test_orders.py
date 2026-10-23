"""API tests against a real Postgres database (TEST_DB_URL), with Catalog, Kafka and Razorpay faked."""

import hashlib
import hmac
import json
import os
from decimal import Decimal

import psycopg
import pytest
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.engine import make_url
from sqlalchemy.orm import sessionmaker

from app import create_app
from clients import CatalogError, InventoryError, PaymentProviderError, Quote, envelope
from models import Base

WEBHOOK_SECRET = "whsec_test"


class FakeCatalog:
    # product_id -> (wholesaler_id, moq, unit_price)
    products = {1: ("w1", 10, Decimal("100.00")), 2: ("w1", 1, Decimal("49.99")), 3: ("w2", 1, Decimal("10.00"))}
    down = False

    def quote(self, product_id, qty):
        if self.down:
            raise CatalogError(502, "CATALOG_UNAVAILABLE", "could not reach the catalog")
        if product_id not in self.products:
            raise CatalogError(404, "PRODUCT_NOT_FOUND", "not found")
        wholesaler, moq, price = self.products[product_id]
        if qty < moq:
            raise CatalogError(422, "BELOW_MOQ", f"minimum order quantity for this product is {moq}")
        return Quote(product_id, wholesaler, qty, price, price * qty)


class FakeInventory:
    """Stock per product id; products not listed are untracked."""

    def __init__(self):
        self.stock, self.reservations, self.down = {}, {}, False

    def reserve(self, order_id, lines):
        if self.down:
            raise InventoryError(502, "INVENTORY_UNAVAILABLE", "could not reach inventory")
        for pid, qty in lines:
            if self.stock.get(pid, qty) < qty:
                raise InventoryError(409, "OUT_OF_STOCK", f"not enough stock for product {pid}")
        for pid, qty in lines:
            if pid in self.stock:
                self.stock[pid] -= qty
        self.reservations[order_id] = lines

    def release(self, order_id):
        for pid, qty in self.reservations.pop(order_id, []):
            if pid in self.stock:
                self.stock[pid] += qty


class FakePublisher:
    def __init__(self):
        self.events = []

    def publish(self, topic, event_type, key, payload):
        self.events.append((topic, envelope(event_type, payload)))

    def topics(self):
        return [t for t, _ in self.events]


class FakeRazorpay:
    key_id = "rzp_test_key"

    def __init__(self):
        self.created = 0
        self.fail = False

    def create_order(self, amount_paise, currency, receipt):
        if self.fail:
            raise PaymentProviderError("boom")
        self.created += 1
        return f"order_rzp_{self.created}"


@pytest.fixture(scope="session")
def engine():
    url = make_url(os.environ["TEST_DB_URL"])
    # Create the throwaway database if it doesn't exist yet.
    admin = url.set(drivername="postgresql", database="postgres").render_as_string(hide_password=False)
    with psycopg.connect(admin, autocommit=True) as conn:
        if not conn.execute("SELECT 1 FROM pg_database WHERE datname = %s", (url.database,)).fetchone():
            conn.execute(f'CREATE DATABASE "{url.database}"')
    eng = create_engine(url)
    yield eng
    eng.dispose()


@pytest.fixture
def ctx(engine):
    Base.metadata.drop_all(engine)
    Base.metadata.create_all(engine)
    publisher, razorpay = FakePublisher(), FakeRazorpay()
    catalog, inventory = FakeCatalog(), FakeInventory()
    app = create_app(sessionmaker(engine, expire_on_commit=False), catalog, publisher, razorpay, WEBHOOK_SECRET, inventory=inventory)
    client = TestClient(app)
    return {"client": client, "publisher": publisher, "razorpay": razorpay, "catalog": catalog, "inventory": inventory, "engine": engine}


def as_user(user_id, role):
    return {"x-user-id": user_id, "x-user-role": role}


RETAILER = as_user("r1", "retailer")
WHOLESALER = as_user("w1", "wholesaler")


def place(client, items=None, headers=RETAILER):
    return client.post("/orders", json={"items": items or [{"productId": 1, "qty": 10}, {"productId": 2, "qty": 3}]}, headers=headers)


def code(res):
    return res.json()["error"]["code"]


def test_place_order_prices_server_side_and_publishes(ctx):
    res = place(ctx["client"], [{"productId": 1, "qty": 10, "unitPrice": 0.01}, {"productId": 2, "qty": 3}])
    assert res.status_code == 201, res.text
    order = res.json()["data"]
    assert order["status"] == "placed"
    assert order["wholesalerId"] == "w1"
    assert order["retailerId"] == "r1"
    assert Decimal(str(order["total"])) == Decimal("1149.97")  # 10 x 100.00 + 3 x 49.99; client price ignored

    topic, event = ctx["publisher"].events[0]
    assert topic == "order.placed"
    # Required fields of the shared envelope in schemas/events.json.
    assert {"eventId", "timestamp", "sourceService", "eventType", "payload"} <= event.keys()
    assert event["sourceService"] == "order-payment"
    assert event["eventType"] == "OrderPlaced"
    assert event["payload"]["orderId"] == order["id"]
    assert event["payload"]["total"] == "1149.97"


@pytest.mark.parametrize(
    "items, status, err",
    [
        ([{"productId": 1, "qty": 9}], 422, "BELOW_MOQ"),
        ([{"productId": 404, "qty": 1}], 404, "PRODUCT_NOT_FOUND"),
        ([{"productId": 1, "qty": 10}, {"productId": 3, "qty": 1}], 422, "MIXED_WHOLESALERS"),
        ([{"productId": 2, "qty": 1}, {"productId": 2, "qty": 2}], 400, "DUPLICATE_ITEM"),
        ([{"productId": 2, "qty": 0}], 400, "VALIDATION_ERROR"),
        ([], 400, "VALIDATION_ERROR"),
    ],
)
def test_place_order_rejects(ctx, items, status, err):
    res = ctx["client"].post("/orders", json={"items": items}, headers=RETAILER)
    assert res.status_code == status, res.text
    assert code(res) == err
    assert ctx["publisher"].events == []


def test_place_order_requires_retailer_and_catalog(ctx):
    c = ctx["client"]
    assert place(c, headers={}).status_code == 401
    assert place(c, headers=WHOLESALER).status_code == 403
    ctx["catalog"].down = True
    res = place(c)
    assert res.status_code == 502 and code(res) == "CATALOG_UNAVAILABLE"


def test_stock_is_reserved_and_released_on_cancel(ctx):
    c, inventory = ctx["client"], ctx["inventory"]
    inventory.stock = {1: 15}
    first = place(c, [{"productId": 1, "qty": 10}])
    assert first.status_code == 201
    assert inventory.stock[1] == 5 and first.json()["data"]["id"] in inventory.reservations

    res = place(c, [{"productId": 1, "qty": 10}])
    assert (res.status_code, code(res)) == (409, "OUT_OF_STOCK")
    assert len(ctx["publisher"].events) == 1  # only the first order was published

    order_id = first.json()["data"]["id"]
    assert c.patch(f"/orders/{order_id}/status", json={"status": "cancelled"}, headers=WHOLESALER).status_code == 200
    assert inventory.stock[1] == 15
    assert place(c, [{"productId": 1, "qty": 10}]).status_code == 201


def test_inventory_down_places_nothing(ctx):
    ctx["inventory"].down = True
    res = place(ctx["client"])
    assert (res.status_code, code(res)) == (502, "INVENTORY_UNAVAILABLE")
    assert ctx["client"].get("/orders", headers=RETAILER).json()["meta"]["total"] == 0


def test_order_detail_visible_to_parties_only(ctx):
    c = ctx["client"]
    order_id = place(c).json()["data"]["id"]
    assert c.get(f"/orders/{order_id}", headers=RETAILER).status_code == 200
    assert c.get(f"/orders/{order_id}", headers=WHOLESALER).status_code == 200
    assert c.get(f"/orders/{order_id}", headers=as_user("admin1", "admin")).status_code == 200
    assert c.get(f"/orders/{order_id}", headers=as_user("r2", "retailer")).status_code == 403
    assert c.get("/orders/does-not-exist", headers=RETAILER).status_code == 404


def test_list_is_scoped_to_caller(ctx):
    c = ctx["client"]
    place(c)
    place(c)
    place(c, headers=as_user("r2", "retailer"))

    res = c.get("/orders?limit=1", headers=RETAILER)
    body = res.json()
    assert res.status_code == 200
    assert len(body["data"]) == 1 and body["meta"]["total"] == 2 and body["meta"]["hasMore"] is True
    assert c.get("/orders", headers=WHOLESALER).json()["meta"]["total"] == 3
    assert c.get("/orders?status=placed", headers=as_user("r2", "retailer")).json()["meta"]["total"] == 1
    assert c.get("/orders?retailerId=r2", headers=RETAILER).status_code == 403
    assert c.get("/orders?wholesalerId=w9", headers=WHOLESALER).status_code == 403
    assert c.get("/orders", headers=as_user("x", "weaver")).status_code == 403
    assert c.get("/orders?limit=500", headers=RETAILER).status_code == 400


def test_status_transitions(ctx):
    c, pub = ctx["client"], ctx["publisher"]
    order_id = place(c).json()["data"]["id"]
    patch = lambda status, headers=WHOLESALER: c.patch(f"/orders/{order_id}/status", json={"status": status}, headers=headers)

    assert patch("confirmed", RETAILER).status_code == 403
    assert patch("confirmed", as_user("w2", "wholesaler")).status_code == 403
    res = patch("shipped")
    assert res.status_code == 409 and code(res) == "INVALID_TRANSITION"
    assert patch("paid").status_code == 400  # paid is only set by the payment webhook

    res = patch("confirmed")
    assert res.status_code == 200 and res.json()["data"]["status"] == "confirmed"
    assert pub.topics()[-1] == "order.status.changed"
    assert pub.events[-1][1]["payload"]["previousStatus"] == "placed"
    assert patch("cancelled").status_code == 200
    assert patch("confirmed").status_code == 409


def sign(body: bytes) -> str:
    return hmac.new(WEBHOOK_SECRET.encode(), body, hashlib.sha256).hexdigest()


def webhook(client, rzp_order_id, amount, event="payment.captured", signature=None):
    body = json.dumps({
        "event": event,
        "payload": {"payment": {"entity": {"id": "pay_123", "order_id": rzp_order_id, "amount": amount}}},
    }).encode()
    return client.post("/payments/webhook", content=body, headers={"x-razorpay-signature": signature or sign(body), "content-type": "application/json"})


def test_payment_flow(ctx):
    c, pub, rzp = ctx["client"], ctx["publisher"], ctx["razorpay"]
    order_id = place(c).json()["data"]["id"]

    assert c.post(f"/orders/{order_id}/payment", headers=as_user("r2", "retailer")).status_code == 403
    res = c.post(f"/orders/{order_id}/payment", headers=RETAILER)
    assert res.status_code == 200, res.text
    pay = res.json()["data"]
    assert pay == {"orderId": order_id, "razorpayOrderId": "order_rzp_1", "amount": 114997, "currency": "INR", "keyId": "rzp_test_key"}
    # A second click reuses the open Razorpay order.
    assert c.post(f"/orders/{order_id}/payment", headers=RETAILER).json()["data"]["razorpayOrderId"] == "order_rzp_1"
    assert rzp.created == 1

    assert webhook(c, "order_rzp_1", 114997, signature="bad").status_code == 400
    assert webhook(c, "order_rzp_1", 1).json()["data"]["result"] == "ignored"  # amount mismatch
    assert c.get(f"/orders/{order_id}", headers=RETAILER).json()["data"]["status"] == "placed"

    res = webhook(c, "order_rzp_1", 114997)
    assert res.status_code == 200 and res.json()["data"]["result"] == "captured"
    assert c.get(f"/orders/{order_id}", headers=RETAILER).json()["data"]["status"] == "paid"
    assert pub.topics().count("payment.completed") == 1

    # Razorpay retries webhooks: a replay must not double-process.
    assert webhook(c, "order_rzp_1", 114997).json()["data"]["result"] == "duplicate"
    assert pub.topics().count("payment.completed") == 1

    res = c.post(f"/orders/{order_id}/payment", headers=RETAILER)
    assert res.status_code == 409 and code(res) == "ORDER_NOT_PAYABLE"
    assert c.patch(f"/orders/{order_id}/status", json={"status": "shipped"}, headers=WHOLESALER).status_code == 200


def test_failed_payment_allows_retry(ctx):
    c, rzp = ctx["client"], ctx["razorpay"]
    order_id = place(c).json()["data"]["id"]
    c.post(f"/orders/{order_id}/payment", headers=RETAILER)
    assert webhook(c, "order_rzp_1", 114997, event="payment.failed").json()["data"]["result"] == "failed"
    assert c.post(f"/orders/{order_id}/payment", headers=RETAILER).json()["data"]["razorpayOrderId"] == "order_rzp_2"
    rzp.fail = True
    other = place(c).json()["data"]["id"]
    res = c.post(f"/orders/{other}/payment", headers=RETAILER)
    assert res.status_code == 502 and code(res) == "PAYMENT_PROVIDER_ERROR"


def test_webhook_ignores_unknown_and_unrelated_events(ctx):
    c = ctx["client"]
    assert webhook(c, "order_unknown", 100).json()["data"]["result"] == "ignored"
    assert webhook(c, "order_unknown", 100, event="refund.created").json()["data"]["result"] == "ignored"


def test_payments_not_configured(engine):
    Base.metadata.create_all(engine)
    app = create_app(sessionmaker(engine), FakeCatalog(), FakePublisher(), razorpay=None, webhook_secret="", inventory=FakeInventory())
    c = TestClient(app)
    order_id = place(c).json()["data"]["id"]
    res = c.post(f"/orders/{order_id}/payment", headers=RETAILER)
    assert res.status_code == 503 and code(res) == "PAYMENTS_NOT_CONFIGURED"
    assert webhook(c, "x", 1).status_code == 503


def test_errors_use_envelope(ctx):
    res = ctx["client"].get("/nope")
    assert res.status_code == 404
    assert res.json() == {"data": None, "error": {"code": "NOT_FOUND", "message": "Not Found"}, "meta": {}}
