"""Weaviate holds one object per registered design, with our CLIP vector (no Weaviate vectorizer)."""

from urllib.parse import urlparse

import weaviate
from weaviate.classes.config import Configure, DataType, Property, VectorDistances


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
                Property(name="wholesalerId", data_type=DataType.TEXT),
                Property(name="mediaId", data_type=DataType.TEXT),
                Property(name="referenceImageUrl", data_type=DataType.TEXT),
                Property(name="createdAt", data_type=DataType.DATE),
            ],
        )

    def close(self) -> None:
        self.client.close()
