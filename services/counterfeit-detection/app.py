import logging
from dataclasses import dataclass
from typing import Any
from uuid import UUID

from fastapi import Depends, FastAPI, File, Form, Header, Query, Request, UploadFile
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from PIL import Image
from pydantic import BaseModel, Field
from starlette.exceptions import HTTPException as StarletteHTTPException

from clients import UpstreamError

log = logging.getLogger("counterfeit-detection")

# Cosine similarity at or above which a photo counts as the registered design.
# shortcut: placeholder, synthetic calibration could not separate designs; set from test_threshold_separates_real_photos once real saree photos exist (MATCH_THRESHOLD overrides).
DEFAULT_MATCH_THRESHOLD = 0.9

MAX_PHOTO_BYTES = 10 << 20  # same cap as Media/Upload


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str):
        self.status, self.code, self.message = status, code, message


def ok(data: Any, status: int = 200, meta: dict | None = None) -> JSONResponse:
    return JSONResponse({"data": data, "error": None, "meta": meta or {}}, status_code=status)


def error(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse({"data": None, "error": {"code": code, "message": message}, "meta": {}}, status_code=status)


@dataclass(frozen=True)
class Caller:
    id: str
    role: str


# Identity headers are set by the gateway after it verifies the JWT.
def caller(x_user_id: str | None = Header(None), x_user_role: str | None = Header(None)) -> Caller:
    if not x_user_id:
        raise ApiError(401, "UNAUTHENTICATED", "authentication required")
    return Caller(x_user_id, x_user_role or "")


def is_image(data: bytes) -> bool:
    """jpeg, png or webp by magic bytes, never the client's content type."""
    return data[:3] == b"\xff\xd8\xff" or data[:8] == b"\x89PNG\r\n\x1a\n" or (data[:4] == b"RIFF" and data[8:12] == b"WEBP")


class RegisterDesign(BaseModel):
    productId: int = Field(gt=0)
    mediaId: UUID


def create_app(store, embedder, catalog, media, threshold: float = DEFAULT_MATCH_THRESHOLD) -> FastAPI:
    app = FastAPI(title="counterfeit-detection")

    @app.exception_handler(ApiError)
    async def _api_error(_: Request, e: ApiError):
        return error(e.status, e.code, e.message)

    @app.exception_handler(UpstreamError)
    async def _upstream(_: Request, e: UpstreamError):
        return error(e.status, e.code, e.message)

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

    def embed(data: bytes) -> list[float]:
        try:
            return embedder.embed(data)
        except (OSError, Image.DecompressionBombError) as e:  # PIL's "cannot identify image" is an OSError
            raise ApiError(422, "INVALID_IMAGE", "the image could not be decoded") from e

    @app.get("/health")
    def health_check():
        return {"status": "ok", "service": "counterfeit-detection"}

    @app.post("/designs")
    def register_design(req: RegisterDesign, who: Caller = Depends(caller)):
        if who.role != "wholesaler":
            raise ApiError(403, "FORBIDDEN", "only wholesalers can register designs")
        if catalog.product_owner(req.productId) != who.id:
            raise ApiError(403, "FORBIDDEN", "you can only register designs for your own products")
        media_id = str(req.mediaId)
        meta = media.get(media_id)
        if meta["ownerId"] != who.id:
            raise ApiError(403, "FORBIDDEN", "you can only register your own images")
        if meta["type"] != "image":
            raise ApiError(422, "NOT_AN_IMAGE", "a design reference must be an image")

        vector = embed(media.content(media_id))
        # Refuse someone else's design so a counterfeiter cannot register it first; own duplicates are fine.
        clash = store.nearest(vector, 1, exclude_wholesaler=who.id)
        if clash and clash[0][1] >= threshold:
            raise ApiError(409, "DESIGN_CONFLICT", f"this image matches design {clash[0][0]['id']} of another wholesaler")

        design = store.insert(
            {"productId": req.productId, "wholesalerId": who.id, "mediaId": media_id, "referenceImageUrl": meta["url"]},
            vector,
        )
        return ok(design, 201)

    @app.post("/designs/match")
    def match_photo(file: UploadFile = File(...), limit: int = Form(5, ge=1, le=10), who: Caller = Depends(caller)):
        data = file.file.read(MAX_PHOTO_BYTES + 1)
        if len(data) > MAX_PHOTO_BYTES:
            raise ApiError(413, "FILE_TOO_LARGE", "photo must be at most 10 MiB")
        if not is_image(data):
            raise ApiError(415, "UNSUPPORTED_TYPE", "photo must be a jpeg, png or webp image")

        hits = store.nearest(embed(data), limit)
        results = [{"design": d, "similarity": round(s, 4)} for d, s in hits]
        return ok({"match": bool(hits) and hits[0][1] >= threshold, "threshold": threshold, "results": results})

    @app.get("/designs")
    def list_designs(page: int = Query(1, ge=1), limit: int = Query(20, ge=1, le=100)):
        designs, total = store.list_page(page, limit)
        return ok(designs, meta={"page": page, "limit": limit, "total": total, "hasMore": page * limit < total})

    @app.get("/designs/{design_id}")
    def get_design(design_id: str):
        try:
            UUID(design_id)
        except ValueError:
            raise ApiError(404, "DESIGN_NOT_FOUND", "design not found") from None
        design = store.get(design_id)
        if design is None:
            raise ApiError(404, "DESIGN_NOT_FOUND", "design not found")
        return ok(design)

    return app
