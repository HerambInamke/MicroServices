"""Event mapping and API with a fake store; TEST_CLICKHOUSE_URL adds the real table and query tests."""

import os
import uuid
from datetime import datetime, timedelta, timezone
from decimal import Decimal

import httpx
import pytest
from fastapi.testclient import TestClient

from app import create_app
from ingest import TOPICS, CategoryLookup, backfill, rows


def event(kind, payload, days_ago=0):
    ts = (datetime.now(timezone.utc) - timedelta(days=days_ago)).isoformat()
    return {"eventId": str(uuid.uuid4()), "timestamp": ts, "sourceService": "t", "eventType": kind, "payload": payload}


def placed(order_id, wholesaler, items, days_ago=0):
    return event("OrderPlaced", {"orderId": order_id, "retailerId": "r1", "wholesalerId": wholesaler, "status": "placed",
                                 "total": "0", "currency": "INR", "items": items}, days_ago)


def status(order_id, wholesaler, new, days_ago=0):
    return event("OrderStatusChanged", {"orderId": order_id, "wholesalerId": wholesaler, "status": new, "total": "0"}, days_ago)


def paid(order_id, wholesaler, total, days_ago=0):
    return event("PaymentCompleted", {"orderId": order_id, "wholesalerId": wholesaler, "status": "paid", "total": total}, days_ago)


CATEGORIES = {1: 10, 2: 20}


def test_order_placed_becomes_one_row_per_line():
    r = rows(placed("o1", "w1", [{"productId": 1, "qty": 10, "unitPrice": "100.50"}, {"productId": 9, "qty": 2, "unitPrice": 5}]),
             lambda pid: CATEGORIES.get(pid, 0))
    assert [(x["line"], x["product_id"], x["category_id"], x["qty"], x["amount"]) for x in r] == [
        (0, 1, 10, 10, Decimal("1005.00")), (1, 9, 0, 2, Decimal("10"))]
    assert {x["order_id"] for x in r} == {"o1"} and r[0]["wholesaler_id"] == "w1"


def test_other_events_map_to_one_row():
    pay = rows(paid("o1", "w1", "1149.97"), lambda _: 0)[0]
    assert (pay["event_type"], pay["amount"], pay["status"]) == ("PaymentCompleted", Decimal("1149.97"), "paid")
    assert rows(status("o1", "w1", "cancelled"), lambda _: 0)[0]["amount"] == 0
    assert "product.changed" in TOPICS
    prod = rows(event("ProductCreated", {"productId": 5, "wholesalerId": "w1", "categoryId": 3}), lambda _: 0)[0]
    assert (prod["product_id"], prod["category_id"]) == (5, 3)
    piece = rows(event("PieceHandoffSigned", {"pieceId": "p1", "role": "retailer"}), lambda _: 0)[0]
    assert piece["product_id"] == 0 and '"pieceId": "p1"' in piece["metadata"]


class FakeStore:
    def seller_summary(self, wholesaler_id, days):
        return {"revenue": 1.0, "paidOrders": 1, "ordersByStatus": {}, "topProducts": [], "days": days}

    def trends(self, category_id, days):
        return [{"categoryId": category_id, "days": days}]


def test_summary_is_owner_or_admin_only():
    client = TestClient(create_app(FakeStore(), lambda _: 0))
    url = "/analytics/seller/w1/summary"
    assert client.get(url).status_code == 401
    assert client.get(url, headers={"X-User-Id": "w2", "X-User-Role": "wholesaler"}).status_code == 403
    body = client.get(url, params={"period": "7d"}, headers={"X-User-Id": "w1", "X-User-Role": "wholesaler"}).json()["data"]
    assert (body["days"], body["wholesalerId"], body["period"]) == (7, "w1", "7d")
    assert client.get(url, headers={"X-User-Id": "a1", "X-User-Role": "admin"}).status_code == 200
    assert client.get(url, params={"period": "1y"}, headers={"X-User-Id": "w1"}).status_code == 400


def test_trends_are_public_and_validated():
    client = TestClient(create_app(FakeStore(), lambda _: 0))
    assert client.get("/analytics/trends").json()["data"] == {"period": "90d", "weeks": [{"categoryId": None, "days": 90}]}
    assert client.get("/analytics/trends", params={"category": 2, "period": "365d"}).json()["data"]["weeks"][0] == {"categoryId": 2, "days": 365}
    assert client.get("/analytics/trends", params={"period": "7d"}).status_code == 400
    assert client.get("/analytics/trends", params={"category": 0}).status_code == 400


class BackfillStore:
    def __init__(self, lines):
        self.lines, self.inserted = lines, []

    def uncategorized(self):
        return self.lines

    def insert(self, rows):
        self.inserted += rows


def fake_catalog(req: httpx.Request) -> httpx.Response:
    pid = int(req.url.path.rsplit("/", 1)[1])
    if pid not in CATEGORIES:
        return httpx.Response(404, json={"data": None, "error": {"code": "NOT_FOUND"}})
    return httpx.Response(200, json={"data": {"id": pid, "categoryId": CATEGORIES[pid]}})


def test_backfill_refiles_category_zero_lines_and_leaves_deleted_products():
    lookup = CategoryLookup("http://catalog")
    lookup._http = httpx.Client(base_url="http://catalog", transport=httpx.MockTransport(fake_catalog))
    lines = rows(placed("o1", "w1", [{"productId": 1, "qty": 1, "unitPrice": 1}, {"productId": 9, "qty": 1, "unitPrice": 1}]), lambda _: 0)
    store = BackfillStore(lines)
    client = TestClient(create_app(store, lookup))
    assert client.post("/analytics/backfill").status_code == 401
    assert client.post("/analytics/backfill", headers={"X-User-Id": "w1", "X-User-Role": "wholesaler"}).status_code == 403
    res = client.post("/analytics/backfill", headers={"X-User-Id": "a1", "X-User-Role": "admin"})
    assert res.json()["data"] == {"updated": 1}
    # Product 1 gets its real category under the same (event_id, line); deleted product 9 is not re-inserted, so it stays 0.
    assert [(r["event_id"], r["line"], r["product_id"], r["category_id"]) for r in store.inserted] == [(lines[0]["event_id"], 0, 1, 10)]


@pytest.mark.skipif(not os.getenv("TEST_CLICKHOUSE_URL"), reason="TEST_CLICKHOUSE_URL not set")
def test_real_store_summary_and_trends():
    from store import ClickHouseStore

    store = ClickHouseStore.connect(os.environ["TEST_CLICKHOUSE_URL"], table=f"events_test_{uuid.uuid4().hex[:8]}")
    cat = lambda pid: CATEGORIES.get(pid, 0)  # noqa: E731
    try:
        store.ensure()
        store.ensure()
        o1 = placed("o1", "w1", [{"productId": 1, "qty": 10, "unitPrice": "100"}, {"productId": 2, "qty": 3, "unitPrice": "50"}])
        store.insert(rows(o1, cat))
        store.insert(rows(o1, cat))  # redelivered: counted once
        store.insert(rows(paid("o1", "w1", "1150.00"), cat))
        store.insert(rows(placed("o2", "w1", [{"productId": 2, "qty": 20, "unitPrice": "50"}]), cat))
        store.insert(rows(status("o2", "w1", "cancelled"), cat))
        store.insert(rows(placed("o3", "w1", [{"productId": 1, "qty": 99, "unitPrice": "1"}], days_ago=40), cat))  # outside 30d
        store.insert(rows(placed("o4", "w2", [{"productId": 1, "qty": 5, "unitPrice": "1"}]), cat))  # another seller

        s = store.seller_summary("w1", 30)
        assert s["revenue"] == 1150.0 and s["paidOrders"] == 1
        assert s["ordersByStatus"] == {"cancelled": 1, "paid": 1}
        assert s["topProducts"] == [{"productId": 2, "qty": 23}, {"productId": 1, "qty": 10}]
        assert store.seller_summary("w1", 90)["topProducts"][0] == {"productId": 1, "qty": 109}

        weeks = store.trends(None, 30)
        assert sum(w["qty"] for w in weeks if w["categoryId"] == 10) == 15
        assert {w["categoryId"] for w in store.trends(20, 30)} == {20}
    finally:
        store.client.command(f"DROP TABLE IF EXISTS {store.table}")


@pytest.mark.skipif(not os.getenv("TEST_CLICKHOUSE_URL"), reason="TEST_CLICKHOUSE_URL not set")
def test_real_store_backfill_replaces_category_zero_rows():
    from store import ClickHouseStore

    store = ClickHouseStore.connect(os.environ["TEST_CLICKHOUSE_URL"], table=f"events_test_{uuid.uuid4().hex[:8]}")
    try:
        store.ensure()
        # Catalog down at ingest: both lines land in category 0; product 9 was since deleted.
        store.insert(rows(placed("o1", "w1", [{"productId": 1, "qty": 4, "unitPrice": "1"}, {"productId": 9, "qty": 2, "unitPrice": "1"}]), lambda _: 0))
        assert len(store.uncategorized()) == 2

        assert backfill(store, lambda pid: CATEGORIES.get(pid, 0)) == 1
        assert [(r["product_id"], r["category_id"]) for r in store.uncategorized()] == [(9, 0)]
        assert {w["categoryId"]: w["qty"] for w in store.trends(None, 30)} == {0: 2, 10: 4}
    finally:
        store.client.command(f"DROP TABLE IF EXISTS {store.table}")
