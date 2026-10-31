"""API tests against a real Postgres database (TEST_DB_URL), with Catalog, Kafka and Razorpay faked."""

import hashlib
import hmac
import json
import os
from concurrent.futures import ThreadPoolExecutor
from decimal import Decimal

import psycopg
import pytest
from alembic.autogenerate import compare_metadata
from alembic.migration import MigrationContext
from fastapi.testclient import TestClient
from sqlalchemy import create_engine, select, text
from sqlalchemy.engine import make_url
from sqlalchemy.orm import sessionmaker

from app import create_app, init_db
from clients import CatalogError, ChatError, InventoryError, Offer, PaymentProviderError, Quote
from models import Base, OutboxEvent
from outbox import RELAY_LOCK, relay_once

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


class FakeChat:
    """Offers by id, as chat's internal endpoint returns them."""

    def __init__(self):
        self.offers, self.down = {}, False

    def offer(self, offer_id):
        if self.down:
            raise ChatError(502, "CHAT_UNAVAILABLE", "could not reach chat")
        if offer_id not in self.offers:
            raise ChatError(404, "OFFER_NOT_FOUND", "not found")
        return self.offers[offer_id]


class Outbox:
    """Reads the events the app committed to the outbox table, oldest first."""

    def __init__(self, engine):
        self.sessions = sessionmaker(engine)

    @property
    def events(self):
        with self.sessions() as s:
            return [(r.topic, json.loads(r.event)) for r in s.scalars(select(OutboxEvent).order_by(OutboxEvent.id))]

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
    razorpay, catalog, inventory, chat = FakeRazorpay(), FakeCatalog(), FakeInventory(), FakeChat()
    app = create_app(sessionmaker(engine, expire_on_commit=False), catalog, razorpay, WEBHOOK_SECRET, inventory=inventory, chat=chat)
    client = TestClient(app)
    return {"client": client, "outbox": Outbox(engine), "razorpay": razorpay, "catalog": catalog, "inventory": inventory,
            "chat": chat, "engine": engine}


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

    topic, event = ctx["outbox"].events[0]
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
    assert ctx["outbox"].events == []


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
    assert len(ctx["outbox"].events) == 1  # only the first order was published

    order_id = first.json()["data"]["id"]
    assert c.patch(f"/orders/{order_id}/status", json={"status": "cancelled"}, headers=WHOLESALER).status_code == 200
    assert inventory.stock[1] == 15
    assert place(c, [{"productId": 1, "qty": 10}]).status_code == 201


def accepted_offer(offer_id="o1", product_id="1", qty=10, price="80.50", status="accepted", wholesaler="w1", retailer="r1"):
    return Offer(offer_id, product_id, qty, Decimal(price), status, wholesaler, retailer)


def test_accepted_offer_sets_line_price_once(ctx):
    c, inventory = ctx["client"], ctx["inventory"]
    ctx["chat"].offers["o1"] = accepted_offer()
    res = place(c, [{"productId": 1, "qty": 10, "offerId": "o1"}, {"productId": 2, "qty": 3}])
    assert res.status_code == 201, res.text
    order = res.json()["data"]
    assert [i["unitPrice"] for i in order["items"]] == [80.5, 49.99]  # offer price, catalog price
    assert Decimal(str(order["total"])) == Decimal("954.97")  # 10 x 80.50 + 3 x 49.99
    assert ctx["outbox"].events[0][1]["payload"]["total"] == "954.97"

    inventory.stock[1] = 0  # reuse is refused as such, not as out of stock
    res = place(c, [{"productId": 1, "qty": 10, "offerId": "o1"}])
    assert (res.status_code, code(res)) == (409, "OFFER_ALREADY_USED")
    assert len(inventory.reservations) == 1  # the refused order's stock was released
    assert len(ctx["outbox"].events) == 1


def test_concurrent_orders_cannot_share_an_offer(ctx):
    ctx["chat"].offers["o1"] = accepted_offer()
    with ThreadPoolExecutor(2) as pool:
        results = list(pool.map(lambda _: place(ctx["client"], [{"productId": 1, "qty": 10, "offerId": "o1"}]), range(2)))
    assert sorted(r.status_code for r in results) == [201, 409]


@pytest.mark.parametrize(
    "offer, status, err",
    [
        (None, 404, "OFFER_NOT_FOUND"),
        (accepted_offer(retailer="r2"), 403, "FORBIDDEN"),
        (accepted_offer(status="open"), 409, "OFFER_NOT_ACCEPTED"),
        (accepted_offer(status="countered"), 409, "OFFER_NOT_ACCEPTED"),
        (accepted_offer(product_id="2"), 422, "OFFER_MISMATCH"),
        (accepted_offer(qty=20), 422, "OFFER_MISMATCH"),
        (accepted_offer(wholesaler="w2"), 422, "OFFER_MISMATCH"),
    ],
)
def test_place_order_rejects_bad_offer(ctx, offer, status, err):
    if offer:
        ctx["chat"].offers["o1"] = offer
    res = place(ctx["client"], [{"productId": 1, "qty": 10, "offerId": "o1"}])
    assert (res.status_code, code(res)) == (status, err), res.text
    assert ctx["inventory"].reservations == {} and ctx["outbox"].events == []


def test_chat_down_places_nothing(ctx):
    ctx["chat"].down = True
    res = place(ctx["client"], [{"productId": 1, "qty": 10, "offerId": "o1"}])
    assert (res.status_code, code(res)) == (502, "CHAT_UNAVAILABLE")
    assert place(ctx["client"]).status_code == 201  # lines without an offer never ask chat


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
    c, pub = ctx["client"], ctx["outbox"]
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
    c, pub, rzp = ctx["client"], ctx["outbox"], ctx["razorpay"]
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
    app = create_app(sessionmaker(engine), FakeCatalog(), razorpay=None, webhook_secret="", inventory=FakeInventory(), chat=FakeChat())
    c = TestClient(app)
    order_id = place(c).json()["data"]["id"]
    res = c.post(f"/orders/{order_id}/payment", headers=RETAILER)
    assert res.status_code == 503 and code(res) == "PAYMENTS_NOT_CONFIGURED"
    assert webhook(c, "x", 1).status_code == 503


class FakeKafka:
    """send() fails while down; records what Kafka acknowledged."""

    def __init__(self):
        self.down, self.sent = False, []

    def send(self, topic, key, value):
        if self.down:
            return False
        self.sent.append((topic, key, json.loads(value)))
        return True


def test_relay_survives_kafka_outage_in_order(ctx):
    c, outbox, kafka = ctx["client"], ctx["outbox"], FakeKafka()
    sessions = sessionmaker(ctx["engine"])
    order_id = place(c).json()["data"]["id"]
    c.patch(f"/orders/{order_id}/status", json={"status": "confirmed"}, headers=WHOLESALER)
    written = outbox.events

    kafka.down = True
    assert relay_once(sessions, kafka.send) == 0
    assert outbox.events == written  # nothing lost while Kafka is down

    kafka.down = False
    assert relay_once(sessions, kafka.send) == 2
    assert [(t, e) for t, _, e in kafka.sent] == written  # same order, same eventIds
    assert kafka.sent[0][1] == order_id  # keyed by order, so one order's events share a partition
    assert outbox.events == []
    assert relay_once(sessions, kafka.send) == 0


def test_relay_stops_at_first_failure_so_events_never_overtake(ctx):
    c, outbox = ctx["client"], ctx["outbox"]
    sessions = sessionmaker(ctx["engine"])
    place(c), place(c)
    first, second = outbox.events
    calls = []
    flaky = lambda topic, key, value: calls.append(key) or len(calls) > 1  # first send fails
    assert relay_once(sessions, flaky) == 0
    assert calls == [first[1]["payload"]["orderId"]]  # the second was not tried ahead of the first
    assert outbox.events == [first, second]


def test_only_one_relay_runs_at_a_time(ctx):
    place(ctx["client"])
    kafka, sessions = FakeKafka(), sessionmaker(ctx["engine"])
    with sessions() as other:
        assert other.scalar(text("SELECT pg_try_advisory_xact_lock(:k)"), {"k": RELAY_LOCK})
        assert relay_once(sessions, kafka.send) == 0  # another replica holds the relay
    assert relay_once(sessions, kafka.send) == 1


def test_errors_use_envelope(ctx):
    res = ctx["client"].get("/nope")
    assert res.status_code == 404
    assert res.json() == {"data": None, "error": {"code": "NOT_FOUND", "message": "Not Found"}, "meta": {}}


def reset_schema(engine):
    Base.metadata.drop_all(engine)
    with engine.begin() as conn:
        conn.execute(text("DROP TABLE IF EXISTS alembic_version"))


def test_migrations_match_models(engine):
    reset_schema(engine)
    init_db(engine)
    init_db(engine)  # a second start is a no-op
    with engine.connect() as conn:
        assert compare_metadata(MigrationContext.configure(conn), Base.metadata) == []


def test_migrations_adopt_create_all_database(engine):
    reset_schema(engine)
    Base.metadata.create_all(engine)  # a database from before migrations, which had no offer_id yet
    with engine.begin() as conn:
        conn.execute(text("ALTER TABLE order_items DROP COLUMN offer_id"))
    init_db(engine)
    with engine.connect() as conn:
        assert conn.execute(text("SELECT version_num FROM alembic_version")).scalar_one() == "0002"
        assert compare_metadata(MigrationContext.configure(conn), Base.metadata) == []
