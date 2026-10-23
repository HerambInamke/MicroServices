import logging
from typing import Any, Literal

from fastapi import Depends, FastAPI, Header, Query, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from starlette.exceptions import HTTPException as StarletteHTTPException

from clients import UpstreamError
from index import design_doc, product_doc, wholesaler_doc

log = logging.getLogger("search")


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str):
        self.status, self.code, self.message = status, code, message


def ok(data: Any, status: int = 200, meta: dict | None = None) -> JSONResponse:
    return JSONResponse({"data": data, "error": None, "meta": meta or {}}, status_code=status)


def error(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse({"data": None, "error": {"code": code, "message": message}, "meta": {}}, status_code=status)


# Identity headers are set by the gateway after it verifies the JWT.
def admin(x_user_id: str | None = Header(None), x_user_role: str | None = Header(None)) -> str:
    if not x_user_id:
        raise ApiError(401, "UNAUTHENTICATED", "authentication required")
    if x_user_role != "admin":
        raise ApiError(403, "FORBIDDEN", "only admins can reindex")
    return x_user_id


def query_text(q: str = Query(..., max_length=200)) -> str:
    if not q.strip():
        raise ApiError(400, "VALIDATION_ERROR", "q must not be blank")
    return q.strip()


def create_app(index, products, profiles, designs) -> FastAPI:
    """products/profiles/designs are clients.Source for reindexing."""
    app = FastAPI(title="search")

    @app.exception_handler(ApiError)
    async def _api_error(_: Request, e: ApiError):
        return error(e.status, e.code, e.message)

    @app.exception_handler(UpstreamError)
    async def _upstream(_: Request, e: UpstreamError):
        return error(502, "UPSTREAM_UNAVAILABLE", f"could not read from {e.service}")

    @app.exception_handler(RequestValidationError)
    async def _validation(_: Request, e: RequestValidationError):
        msg = "; ".join(f"{'.'.join(str(p) for p in err['loc'])}: {err['msg']}" for err in e.errors())
        return error(400, "VALIDATION_ERROR", msg)

    @app.exception_handler(StarletteHTTPException)
    async def _http(_: Request, e: StarletteHTTPException):
        return error(e.status_code, "NOT_FOUND" if e.status_code == 404 else "HTTP_ERROR", str(e.detail))

    @app.exception_handler(Exception)
    async def _unexpected(_: Request, e: Exception):
        log.exception("unhandled error")
        return error(500, "INTERNAL", "something went wrong")

    @app.get("/health")
    def health_check():
        return {"status": "ok", "service": "search"}

    @app.get("/search")
    def search(
        q: str = Depends(query_text),
        type: Literal["product", "wholesaler", "design"] | None = None,
        page: int = Query(1, ge=1, le=100),
        limit: int = Query(20, ge=1, le=100),
    ):
        hits, total = index.search(q, type, page, limit)
        return ok(hits, meta={"page": page, "limit": limit, "total": total, "hasMore": page * limit < total})

    @app.get("/search/suggest")
    def suggest(q: str = Depends(query_text)):
        return ok(index.suggest(q))

    @app.post("/search/reindex")
    def reindex(_: str = Depends(admin)):
        # Read everything first so a source outage leaves the current index untouched.
        product_rows, profile_rows, design_rows = products.all(), profiles.all(), designs.all()
        names = {p["id"]: p["name"] for p in product_rows}
        docs = (
            [product_doc(p) for p in product_rows]
            + [wholesaler_doc(p) for p in profile_rows]
            + [design_doc(d, names) for d in design_rows]
        )
        index.reset()
        index.bulk(docs)
        return ok({"products": len(product_rows), "wholesalers": len(profile_rows), "designs": len(design_rows)})

    return app
