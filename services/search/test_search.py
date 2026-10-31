"""Search API with a fake index and sources; TEST_ES_URL adds the real Elasticsearch tests."""

import os
import uuid

import pytest
from fastapi.testclient import TestClient

import consumer
from app import create_app
from clients import UpstreamError


class FakeIndex:
    def __init__(self):
        self.docs = {}
        self.resets = 0

    def upsert(self, doc):
        self.docs[(doc["type"], str(doc["id"]))] = doc

    def delete(self, doc_type, doc_id):
        self.docs.pop((doc_type, str(doc_id)), None)

    def reset(self):
        self.resets += 1
        self.docs = {}

    def bulk(self, docs):
        for d in docs:
            self.upsert(d)

    def search(self, q, doc_type, page, limit):
        hits = [d for d in self.docs.values() if q.lower() in d["name"].lower() and doc_type in (None, d["type"])]
        return hits[(page - 1) * limit : page * limit], len(hits)

    def suggest(self, q):
        return [d["name"] for d in self.docs.values() if d["name"].lower().startswith(q.lower())]


class FakeSource:
    def __init__(self, name, rows):
        self.name, self.rows, self.down = name, rows, False

    def all(self):
        if self.down:
            raise UpstreamError(self.name)
        return self.rows


PRODUCTS = [{"id": 1, "name": "Kanjeevaram Silk", "description": "silk", "wholesalerId": "w1", "categoryId": 3, "basePrice": 2500}]
PROFILES = [{"userId": "w1", "businessName": "Smoke Silks", "businessType": "wholesaler"}]
DESIGNS = [{"id": "d1", "productId": 1, "wholesalerId": "w1"}]
ADMIN = {"X-User-Id": "a1", "X-User-Role": "admin"}


@pytest.fixture
def ctx():
    index = FakeIndex()
    sources = [FakeSource("catalog", PRODUCTS), FakeSource("business-profile", PROFILES), FakeSource("counterfeit-detection", DESIGNS)]
    return TestClient(create_app(index, *sources)), index, sources


def test_reindex_is_admin_only_and_indexes_every_source(ctx):
    client, index, _ = ctx
    assert client.post("/search/reindex").status_code == 401
    assert client.post("/search/reindex", headers={"X-User-Id": "w1", "X-User-Role": "wholesaler"}).status_code == 403

    res = client.post("/search/reindex", headers=ADMIN)
    assert res.json()["data"] == {"products": 1, "wholesalers": 1, "designs": 1}
    assert index.docs[("design", "d1")]["name"] == "Kanjeevaram Silk"  # found by its product's name
    assert index.docs[("wholesaler", "w1")]["name"] == "Smoke Silks"


def test_reindex_source_down_keeps_the_index(ctx):
    client, index, sources = ctx
    index.upsert({"type": "product", "id": 9, "name": "kept"})
    sources[2].down = True
    res = client.post("/search/reindex", headers=ADMIN)
    assert (res.status_code, res.json()["error"]["code"]) == (502, "UPSTREAM_UNAVAILABLE")
    assert index.resets == 0 and ("product", "9") in index.docs


def test_search_filters_pages_and_validates(ctx):
    client, _, _ = ctx
    client.post("/search/reindex", headers=ADMIN)
    body = client.get("/search", params={"q": "kanjeevaram"}).json()
    assert {d["type"] for d in body["data"]} == {"product", "design"}
    assert body["meta"] == {"page": 1, "limit": 20, "total": 2, "hasMore": False}
    assert [d["type"] for d in client.get("/search", params={"q": "kanjeevaram", "type": "design"}).json()["data"]] == ["design"]
    assert client.get("/search", params={"q": "kanjeevaram", "limit": 1}).json()["meta"]["hasMore"] is True

    assert client.get("/search").status_code == 400
    assert client.get("/search", params={"q": "  "}).status_code == 400
    assert client.get("/search", params={"q": "x", "type": "order"}).status_code == 400
    assert client.get("/search/suggest", params={"q": "smo"}).json()["data"] == ["Smoke Silks"]


def test_consumer_handles_product_events():
    assert "product.changed" in consumer.TOPICS  # one topic keyed by product id keeps a product's events in order
    index = FakeIndex()
    payload = {"productId": 7, "wholesalerId": "w1", "name": "Banarasi", "description": None, "categoryId": 1, "basePrice": 99.5, "moq": 1}
    consumer.handle(index, {"eventType": "ProductCreated", "payload": payload})
    assert index.docs[("product", "7")]["basePrice"] == 99.5
    consumer.handle(index, {"eventType": "ProductUpdated", "payload": payload | {"name": "Banarasi Silk"}})
    assert index.docs[("product", "7")]["name"] == "Banarasi Silk"
    consumer.handle(index, {"eventType": "ProductDeleted", "payload": {"productId": 7}})
    assert index.docs == {}


def test_consumer_handles_design_events():
    index = FakeIndex()
    payload = {"designId": "d9", "productId": 7, "productName": "Banarasi", "wholesalerId": "w1"}
    consumer.handle(index, {"eventType": "DesignRegistered", "payload": payload})
    assert index.docs[("design", "d9")] == {"type": "design", "id": "d9", "name": "Banarasi", "description": "",
                                           "wholesalerId": "w1", "productId": 7}
    consumer.handle(index, {"eventType": "DesignDeleted", "payload": {"designId": "d9"}})
    assert index.docs == {}


@pytest.mark.skipif(not os.getenv("TEST_ES_URL"), reason="TEST_ES_URL not set")
def test_real_index_fuzzy_search_and_suggest():
    from elasticsearch import Elasticsearch

    from index import SearchIndex, product_doc, wholesaler_doc

    index = SearchIndex(Elasticsearch(os.environ["TEST_ES_URL"]), name=f"search-test-{uuid.uuid4().hex[:8]}")
    try:
        index.ensure()
        index.ensure()
        index.bulk([product_doc(PRODUCTS[0]), wholesaler_doc(PROFILES[0])])
        hits, total = index.search("kanjivaram", None, 1, 10)  # typo
        assert total == 1 and hits[0]["id"] == "1" and "suggest" not in hits[0]
        assert index.search("silks", "wholesaler", 1, 10)[0][0]["name"] == "Smoke Silks"
        assert index.suggest("kanj") == ["Kanjeevaram Silk"]

        index.upsert(product_doc({**PRODUCTS[0], "id": 2, "name": "Paithani"}))
        index.delete("product", 2)
        index.delete("product", 2)  # already gone: no error
        index.es.indices.refresh(index=index.name)
        assert index.search("paithani", None, 1, 10)[1] == 0
    finally:
        index.es.indices.delete(index=index.name)
