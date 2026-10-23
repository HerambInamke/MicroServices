import logging
from typing import Any, Literal

from fastapi import Depends, FastAPI, Header, Query, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from starlette.exceptions import HTTPException as StarletteHTTPException

log = logging.getLogger("analytics")

DAYS = {"7d": 7, "30d": 30, "90d": 90, "365d": 365}


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str):
        self.status, self.code, self.message = status, code, message


def ok(data: Any) -> JSONResponse:
    return JSONResponse({"data": data, "error": None, "meta": {}})


def error(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse({"data": None, "error": {"code": code, "message": message}, "meta": {}}, status_code=status)


# Identity headers are set by the gateway after it verifies the JWT.
def caller(x_user_id: str | None = Header(None), x_user_role: str | None = Header(None)) -> tuple[str, str]:
    if not x_user_id:
        raise ApiError(401, "UNAUTHENTICATED", "authentication required")
    return x_user_id, x_user_role or ""


def create_app(store) -> FastAPI:
    app = FastAPI(title="analytics")

    @app.exception_handler(ApiError)
    async def _api_error(_: Request, e: ApiError):
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

    @app.get("/health")
    def health_check():
        return {"status": "ok", "service": "analytics"}

    @app.get("/analytics/seller/{wholesaler_id}/summary")
    def seller_summary(
        wholesaler_id: str,
        period: Literal["7d", "30d", "90d"] = "30d",
        who: tuple[str, str] = Depends(caller),
    ):
        user, role = who
        if role != "admin" and user != wholesaler_id:
            raise ApiError(403, "FORBIDDEN", "you can only see your own dashboard")
        return ok(store.seller_summary(wholesaler_id, DAYS[period]) | {"wholesalerId": wholesaler_id, "period": period})

    @app.get("/analytics/trends")
    def trends(category: int | None = Query(None, ge=1), period: Literal["30d", "90d", "365d"] = "90d"):
        return ok({"period": period, "weeks": store.trends(category, DAYS[period])})

    return app
