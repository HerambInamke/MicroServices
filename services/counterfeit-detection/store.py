"""Weaviate holds one object per registered design, with our CLIP vector (no Weaviate vectorizer)."""

from datetime import datetime, timezone
from urllib.parse import urlparse

import weaviate
from weaviate.classes.config import Configure, DataType, Property, Tokenization, VectorDistances
from weaviate.classes.query import Filter, MetadataQuery


class WeaviateStore:
    def __init__(self, client: weaviate.WeaviateClient, collection: str = "Design"):
        self.client, self.name = client, collection

    @classmethod
    def connect(cls, url: str, collection: str = "Design", grpc_port: int = 50051) -> "WeaviateStore":
        u = urlparse(url)
        client = weaviate.connect_to_custom(
            http_host=u.hostname, http_port=u.port or 8080, http_secure=u.scheme == "https",
            grpc_host=u.hostname, grpc_port=grpc_port, grpc_secure=u.scheme == "https",
        )
        return cls(client, collection)

    def ensure_collection(self) -> None:
        if self.client.collections.exists(self.name):
            return
        self.client.collections.create(
            self.name,
            vectorizer_config=Configure.Vectorizer.none(),
            vector_index_config=Configure.VectorIndex.hnsw(distance_metric=VectorDistances.COSINE),
            properties=[
                Property(name="productId", data_type=DataType.INT),
                # FIELD tokenization so ids (uuids with hyphens) compare whole in filters.
                Property(name="wholesalerId", data_type=DataType.TEXT, tokenization=Tokenization.FIELD),
                Property(name="mediaId", data_type=DataType.TEXT, tokenization=Tokenization.FIELD),
                Property(name="referenceImageUrl", data_type=DataType.TEXT),
                Property(name="createdAt", data_type=DataType.DATE),
            ],
        )

    @property
    def _col(self):
        return self.client.collections.get(self.name)

    def insert(self, props: dict, vector: list[float]) -> dict:
        props = props | {"createdAt": datetime.now(timezone.utc)}
        design_id = self._col.data.insert(properties=props, vector=vector)
        return _view(design_id, props)

    def get(self, design_id: str) -> dict | None:
        obj = self._col.query.fetch_object_by_id(design_id)
        return obj and _view(obj.uuid, obj.properties)

    def list_page(self, page: int, limit: int) -> tuple[list[dict], int]:
        # shortcut: offset paging is unordered and capped by Weaviate's QUERY_MAXIMUM_RESULTS (10k); switch to the cursor API for larger registries.
        res = self._col.query.fetch_objects(limit=limit, offset=(page - 1) * limit)
        total = self._col.aggregate.over_all(total_count=True).total_count
        return [_view(o.uuid, o.properties) for o in res.objects], total

    def nearest(self, vector: list[float], limit: int, exclude_wholesaler: str | None = None) -> list[tuple[dict, float]]:
        """Closest designs with cosine similarity, best first."""
        filters = Filter.by_property("wholesalerId").not_equal(exclude_wholesaler) if exclude_wholesaler else None
        res = self._col.query.near_vector(vector, limit=limit, filters=filters, return_metadata=MetadataQuery(distance=True))
        return [(_view(o.uuid, o.properties), 1 - o.metadata.distance) for o in res.objects]

    def close(self) -> None:
        self.client.close()


def _view(design_id, props: dict) -> dict:
    return {
        "id": str(design_id),
        "productId": int(props["productId"]),
        "wholesalerId": props["wholesalerId"],
        "mediaId": props["mediaId"],
        "referenceImageUrl": props["referenceImageUrl"],
        "createdAt": props["createdAt"].isoformat(),
    }
