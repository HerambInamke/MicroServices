"""Reads every page of a source service's public list endpoint for reindexing."""

import httpx


class UpstreamError(Exception):
    def __init__(self, service: str):
        super().__init__(service)
        self.service = service


class Source:
    def __init__(self, name: str, base_url: str, path: str, timeout: float = 10.0):
        self.name, self.path = name, path
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def all(self) -> list[dict]:
        items, page = [], 1
        while True:
            try:
                res = self._http.get(self.path, params={"page": page, "limit": 100})
                res.raise_for_status()
                body = res.json()
            except (httpx.HTTPError, ValueError) as e:
                raise UpstreamError(self.name) from e
            items += body["data"]
            if not body["meta"].get("hasMore"):
                return items
            page += 1
