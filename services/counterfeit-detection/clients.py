"""Outbound lookups: Wholesale Catalog (product owner) and Media/Upload (reference image)."""

import httpx


class UpstreamError(Exception):
    """A lookup failed. status/code/message map onto our response."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


class CatalogClient:
    def __init__(self, base_url: str, timeout: float = 5.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def product_owner(self, product_id: int) -> str:
        try:
            res = self._http.get(f"/products/{product_id}")
        except httpx.HTTPError as e:
            raise UpstreamError(502, "CATALOG_UNAVAILABLE", "could not reach the catalog") from e
        if res.status_code == 404:
            raise UpstreamError(404, "PRODUCT_NOT_FOUND", f"product {product_id} not found")
        if res.status_code != 200:
            raise UpstreamError(502, "CATALOG_UNAVAILABLE", "catalog returned an unexpected response")
        return res.json()["data"]["wholesalerId"]


class MediaClient:
    def __init__(self, base_url: str, timeout: float = 10.0):
        self._http = httpx.Client(base_url=base_url, timeout=timeout)

    def _get(self, path: str) -> httpx.Response:
        try:
            res = self._http.get(path)
        except httpx.HTTPError as e:
            raise UpstreamError(502, "MEDIA_UNAVAILABLE", "could not reach media storage") from e
        if res.status_code == 404:
            raise UpstreamError(404, "MEDIA_NOT_FOUND", "media not found")
        if res.status_code != 200:
            raise UpstreamError(502, "MEDIA_UNAVAILABLE", "media storage returned an unexpected response")
        return res

    def get(self, media_id: str) -> dict:
        """{id, ownerId, contentType, type, url, ...}"""
        return self._get(f"/media/{media_id}").json()["data"]

    def content(self, media_id: str) -> bytes:
        return self._get(f"/media/{media_id}/content").content
