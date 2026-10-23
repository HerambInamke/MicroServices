import os

import pytest
from fastapi.testclient import TestClient

from app import create_app


class FakeStore:
    pass


class FakeEmbedder:
    def embed(self, data: bytes) -> list[float]:
        return [1.0] + [0.0] * 511


def test_health():
    client = TestClient(create_app(FakeStore(), FakeEmbedder(), None, None))
    res = client.get("/health")
    assert res.status_code == 200
    assert res.json() == {"status": "ok", "service": "counterfeit-detection"}


@pytest.mark.skipif(not os.getenv("TEST_WEAVIATE_URL"), reason="TEST_WEAVIATE_URL not set")
def test_ensure_collection_is_idempotent():
    import uuid

    from store import WeaviateStore

    name = f"DesignTest{uuid.uuid4().hex[:8]}"
    store = WeaviateStore.connect(os.environ["TEST_WEAVIATE_URL"], collection=name)
    try:
        store.ensure_collection()
        store.ensure_collection()
        assert store.client.collections.exists(name)
    finally:
        store.client.collections.delete(name)
        store.close()
