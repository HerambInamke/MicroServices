"""One Elasticsearch index holds products, wholesalers and designs, told apart by `type`."""

from elasticsearch import Elasticsearch, NotFoundError, helpers

MAPPINGS = {
    "properties": {
        "type": {"type": "keyword"},
        "id": {"type": "keyword"},
        "name": {"type": "text"},
        "suggest": {"type": "completion"},
        "description": {"type": "text"},
        "wholesalerId": {"type": "keyword"},
        "productId": {"type": "long"},
        "categoryId": {"type": "long"},
        "basePrice": {"type": "double"},
    }
}


class SearchIndex:
    def __init__(self, es: Elasticsearch, name: str = "search"):
        self.es, self.name = es, name

    def ensure(self) -> None:
        if not self.es.indices.exists(index=self.name):
            self.es.indices.create(index=self.name, mappings=MAPPINGS)

    def reset(self) -> None:
        self.es.indices.delete(index=self.name, ignore_unavailable=True)
        self.ensure()

    def upsert(self, doc: dict) -> None:
        self.es.index(index=self.name, id=f"{doc['type']}:{doc['id']}", document=_stored(doc))

    def delete(self, doc_type: str, doc_id) -> None:
        try:
            self.es.delete(index=self.name, id=f"{doc_type}:{doc_id}")
        except NotFoundError:
            pass

    def bulk(self, docs: list[dict]) -> None:
        actions = ({"_index": self.name, "_id": f"{d['type']}:{d['id']}", "_source": _stored(d)} for d in docs)
        helpers.bulk(self.es, actions, refresh="wait_for")

    def search(self, q: str, doc_type: str | None, page: int, limit: int) -> tuple[list[dict], int]:
        query = {"bool": {"must": {"multi_match": {"query": q, "fields": ["name^3", "description"], "fuzziness": "AUTO"}}}}
        if doc_type:
            query["bool"]["filter"] = {"term": {"type": doc_type}}
        res = self.es.search(index=self.name, query=query, from_=(page - 1) * limit, size=limit, source_excludes=["suggest"])
        return [h["_source"] for h in res["hits"]["hits"]], res["hits"]["total"]["value"]

    def suggest(self, q: str) -> list[str]:
        res = self.es.search(
            index=self.name,
            suggest={"s": {"prefix": q, "completion": {"field": "suggest", "size": 10, "skip_duplicates": True, "fuzzy": {"fuzziness": "AUTO"}}}},
            source=False,
        )
        return [o["text"] for o in res["suggest"]["s"][0]["options"]]


def _stored(doc: dict) -> dict:
    return doc | {"id": str(doc["id"]), "suggest": [doc["name"]]}


def product_doc(p: dict) -> dict:
    """From a catalog product view or a product.* event payload."""
    return {
        "type": "product",
        "id": p.get("productId", p.get("id")),
        "name": p["name"],
        "description": p.get("description") or "",
        "wholesalerId": p["wholesalerId"],
        "categoryId": p.get("categoryId"),
        "basePrice": float(p["basePrice"]) if p.get("basePrice") is not None else None,
    }


def wholesaler_doc(profile: dict) -> dict:
    return {
        "type": "wholesaler",
        "id": profile["userId"],
        "name": profile["businessName"],
        "description": profile.get("businessType") or "",
        "wholesalerId": profile["userId"],
    }


def design_doc(design: dict, product_names: dict) -> dict:
    """Designs have no text of their own, so they are found by their product's name."""
    return {
        "type": "design",
        "id": design["id"],
        "name": product_names.get(design["productId"], f"product {design['productId']}"),
        "description": "",
        "wholesalerId": design["wholesalerId"],
        "productId": design["productId"],
    }
