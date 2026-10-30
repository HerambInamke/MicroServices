"""Register / get / match designs with Catalog, Media, the embedder and Weaviate faked."""

import math
import os
import uuid
from datetime import datetime, timezone

import pytest
from fastapi.testclient import TestClient

from app import create_app
from clients import UpstreamError

PNG = b"\x89PNG\r\n\x1a\n"
THRESHOLD = 0.9


def img(tag: str) -> bytes:
    return PNG + tag.encode()


# Image tag -> embedding. "red" and "red-crop" are the same design photographed differently.
VECTORS = {
    "red": [1.0, 0.0, 0.0],
    "red-crop": [0.95, 0.31, 0.0],  # cos ~0.95 with red
    "blue": [0.0, 1.0, 0.0],
    "green": [0.0, 0.0, 1.0],
    "teal": [0.0, 0.6, 0.8],  # cos 0.6 with blue, 0.8 with green
}


def unit(v):
    n = math.sqrt(sum(x * x for x in v))
    return [x / n for x in v]


class FakeEmbedder:
    def embed(self, data: bytes) -> list[float]:
        tag = data[len(PNG):].decode(errors="replace")
        if tag not in VECTORS:
            raise OSError("cannot identify image file")
        return unit(VECTORS[tag])


class FakeStore:
    def __init__(self):
        self.rows = {}

    def insert(self, props, vector):
        design_id = str(uuid.uuid4())
        self.rows[design_id] = (props | {"id": design_id, "createdAt": datetime.now(timezone.utc).isoformat()}, vector)
        return self.rows[design_id][0]

    def get(self, design_id):
        row = self.rows.get(design_id)
        return row and row[0]

    def delete(self, design_id):
        self.rows.pop(design_id, None)

    def list_page(self, page, limit):
        rows = [d for d, _ in self.rows.values()]
        return rows[(page - 1) * limit : page * limit], len(rows)

    def nearest(self, vector, limit, exclude_wholesaler=None):
        hits = [(d, sum(a * b for a, b in zip(v, vector))) for d, v in self.rows.values() if d["wholesalerId"] != exclude_wholesaler]
        return sorted(hits, key=lambda h: -h[1])[:limit]


class FakeCatalog:
    products = {1: "w1", 2: "w2", 3: "w1"}
    down = False

    def product(self, product_id):
        if self.down:
            raise UpstreamError(502, "CATALOG_UNAVAILABLE", "could not reach the catalog")
        if product_id not in self.products:
            raise UpstreamError(404, "PRODUCT_NOT_FOUND", "not found")
        return {"id": product_id, "wholesalerId": self.products[product_id], "name": f"Saree {product_id}"}


class FakePublisher:
    def __init__(self):
        self.events = []

    def publish(self, topic, event_type, key, payload):
        self.events.append((topic, event_type, key, payload))


class FakeMedia:
    def __init__(self):
        self.files = {}

    def add(self, owner, tag, kind="image"):
        media_id = str(uuid.uuid4())
        self.files[media_id] = ({"id": media_id, "ownerId": owner, "type": kind, "url": f"/api/v1/media/{media_id}/content"}, img(tag))
        return media_id

    def get(self, media_id):
        if media_id not in self.files:
            raise UpstreamError(404, "MEDIA_NOT_FOUND", "media not found")
        return self.files[media_id][0]

    def content(self, media_id):
        return self.files[media_id][1]


@pytest.fixture
def ctx():
    store, catalog, media, events = FakeStore(), FakeCatalog(), FakeMedia(), FakePublisher()
    client = TestClient(create_app(store, FakeEmbedder(), catalog, media, events, THRESHOLD))
    return client, store, catalog, media, events


def as_user(uid, role):
    return {"X-User-Id": uid, "X-User-Role": role}


W1, W2, R1 = as_user("w1", "wholesaler"), as_user("w2", "wholesaler"), as_user("r1", "retailer")


def register(client, headers, product_id, media_id):
    return client.post("/designs", json={"productId": product_id, "mediaId": media_id}, headers=headers)


def test_register_and_get(ctx):
    client, _, _, media, events = ctx
    mid = media.add("w1", "red")
    res = register(client, W1, 1, mid)
    assert res.status_code == 201, res.json()
    d = res.json()["data"]
    assert (d["productId"], d["wholesalerId"], d["mediaId"]) == (1, "w1", mid)
    assert d["referenceImageUrl"] == f"/api/v1/media/{mid}/content"
    # Search indexes the design under its product's name.
    assert events.events == [("design.registered", "DesignRegistered", d["id"],
                              {"designId": d["id"], "productId": 1, "productName": "Saree 1", "wholesalerId": "w1"})]

    got = client.get(f"/designs/{d['id']}")
    assert got.status_code == 200
    assert got.json()["data"] == d


def test_delete_owner_or_admin_only(ctx):
    client, _, _, media, events = ctx
    d = register(client, W1, 1, media.add("w1", "red")).json()["data"]
    assert client.delete(f"/designs/{d['id']}").status_code == 401
    assert client.delete(f"/designs/{d['id']}", headers=W2).status_code == 403
    assert client.delete(f"/designs/{uuid.uuid4()}", headers=W1).status_code == 404
    assert client.delete("/designs/not-a-uuid", headers=W1).status_code == 404
    assert [e[0] for e in events.events] == ["design.registered"]  # refusals publish nothing

    res = client.delete(f"/designs/{d['id']}", headers=W1)
    assert res.status_code == 200
    assert res.json()["data"] == {"id": d["id"], "deleted": True}
    assert events.events[-1] == ("design.deleted", "DesignDeleted", d["id"], {"designId": d["id"]})
    assert client.get(f"/designs/{d['id']}").status_code == 404
    # The same image can now be registered by someone else.
    assert register(client, W2, 2, media.add("w2", "red")).status_code == 201

    d2 = register(client, W1, 1, media.add("w1", "blue")).json()["data"]
    assert client.delete(f"/designs/{d2['id']}", headers=as_user("a1", "admin")).status_code == 200


def test_get_unknown_and_malformed_ids_are_404(ctx):
    client = ctx[0]
    assert client.get(f"/designs/{uuid.uuid4()}").json()["error"]["code"] == "DESIGN_NOT_FOUND"
    assert client.get("/designs/not-a-uuid").status_code == 404


@pytest.mark.parametrize(
    "headers,product_id,owner,kind,status,code",
    [
        ({}, 1, "w1", "image", 401, "UNAUTHENTICATED"),
        (R1, 1, "r1", "image", 403, "FORBIDDEN"),  # retailer
        (W1, 2, "w1", "image", 403, "FORBIDDEN"),  # someone else's product
        (W1, 1, "w2", "image", 403, "FORBIDDEN"),  # someone else's media
        (W1, 1, "w1", "document", 422, "NOT_AN_IMAGE"),  # a PDF
        (W1, 99, "w1", "image", 404, "PRODUCT_NOT_FOUND"),
    ],
)
def test_register_refusals(ctx, headers, product_id, owner, kind, status, code):
    client, store, _, media, _ = ctx
    res = register(client, headers, product_id, media.add(owner, "red", kind))
    assert (res.status_code, res.json()["error"]["code"]) == (status, code)
    assert store.rows == {}


def test_register_unknown_media_and_catalog_down(ctx):
    client, _, catalog, media, _ = ctx
    assert register(client, W1, 1, str(uuid.uuid4())).json()["error"]["code"] == "MEDIA_NOT_FOUND"
    assert register(client, W1, 1, "../../health").status_code == 400  # never reaches the media URL
    catalog.down = True
    res = register(client, W1, 1, media.add("w1", "red"))
    assert (res.status_code, res.json()["error"]["code"]) == (502, "CATALOG_UNAVAILABLE")


def test_conflict_with_another_wholesaler_but_not_with_yourself(ctx):
    client, _, _, media, _ = ctx
    assert register(client, W1, 1, media.add("w1", "red")).status_code == 201
    assert register(client, W1, 3, media.add("w1", "red-crop")).status_code == 201  # own duplicate is fine

    res = register(client, W2, 2, media.add("w2", "red-crop"))
    assert (res.status_code, res.json()["error"]["code"]) == (409, "DESIGN_CONFLICT")
    assert register(client, W2, 2, media.add("w2", "blue")).status_code == 201


def test_list_designs_pages(ctx):
    client, _, _, media, _ = ctx
    for tag in ("red", "blue", "green"):
        register(client, W1 if tag != "blue" else W2, 1 if tag != "blue" else 2, media.add("w1" if tag != "blue" else "w2", tag))
    body = client.get("/designs", params={"limit": 2}).json()
    assert len(body["data"]) == 2 and body["meta"] == {"page": 1, "limit": 2, "total": 3, "hasMore": True}
    assert client.get("/designs", params={"limit": 101}).status_code == 400


def test_undecodable_reference_is_422(ctx):
    client, _, _, media, _ = ctx
    res = register(client, W1, 1, media.add("w1", "garbage"))
    assert (res.status_code, res.json()["error"]["code"]) == (422, "INVALID_IMAGE")


def match(client, headers, data, **form):
    return client.post("/designs/match", files={"file": ("photo.png", data, "image/png")}, data=form, headers=headers)


def test_match_orders_results_and_sets_flag(ctx):
    client, _, _, media, _ = ctx
    red = register(client, W1, 1, media.add("w1", "red")).json()["data"]
    blue = register(client, W2, 2, media.add("w2", "blue")).json()["data"]
    green = register(client, W1, 3, media.add("w1", "green")).json()["data"]

    body = match(client, R1, img("red-crop")).json()["data"]
    assert body["match"] is True and body["threshold"] == THRESHOLD
    assert body["results"][0]["design"]["id"] == red["id"]
    assert body["results"][0]["similarity"] >= THRESHOLD

    body = match(client, R1, img("teal"), limit="2").json()["data"]
    assert body["match"] is False  # best is green at 0.8
    assert [r["design"]["id"] for r in body["results"]] == [green["id"], blue["id"]]
    assert body["results"][0]["similarity"] > body["results"][1]["similarity"]


def test_match_empty_registry(ctx):
    body = match(ctx[0], R1, img("red")).json()["data"]
    assert body == {"match": False, "threshold": THRESHOLD, "results": []}


def test_match_refusals(ctx):
    client = ctx[0]
    assert match(client, {}, img("red")).status_code == 401
    assert match(client, R1, b"%PDF-1.4 ...").json()["error"]["code"] == "UNSUPPORTED_TYPE"
    assert match(client, R1, PNG + b"\0" * (10 << 20)).status_code == 413
    assert match(client, R1, img("red"), limit="11").status_code == 400
    assert match(client, R1, img("garbage")).status_code == 422


@pytest.mark.skipif(not os.getenv("TEST_WEAVIATE_URL"), reason="TEST_WEAVIATE_URL not set")
def test_weaviate_store_insert_get_nearest():
    from store import WeaviateStore

    name = f"DesignTest{uuid.uuid4().hex[:8]}"
    store = WeaviateStore.connect(os.environ["TEST_WEAVIATE_URL"], collection=name)
    try:
        store.ensure_collection()
        base = {"productId": 1, "mediaId": str(uuid.uuid4()), "referenceImageUrl": "/x"}
        a = store.insert(base | {"wholesalerId": "11111111-aaaa-bbbb-cccc-000000000001"}, unit(VECTORS["red"]))
        b = store.insert(base | {"wholesalerId": "11111111-aaaa-bbbb-cccc-000000000002"}, unit(VECTORS["blue"]))
        assert store.get(a["id"]) == a
        assert store.get(str(uuid.uuid4())) is None
        rows, total = store.list_page(1, 1)
        assert total == 2 and len(rows) == 1 and rows[0]["id"] in (a["id"], b["id"])

        hits = store.nearest(unit(VECTORS["red-crop"]), 2)
        assert [d["id"] for d, _ in hits] == [a["id"], b["id"]]
        assert hits[0][1] == pytest.approx(0.95, abs=0.01)
        # Hyphenated ids must be excluded whole, not token by token.
        assert [d["id"] for d, _ in store.nearest(unit(VECTORS["red"]), 2, exclude_wholesaler=a["wholesalerId"])] == [b["id"]]
        store.delete(a["id"])
        assert store.get(a["id"]) is None
    finally:
        store.client.collections.delete(name)
        store.close()
