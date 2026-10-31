import hashlib
import hmac
import json
import logging
import os
from dataclasses import dataclass, replace
from decimal import Decimal
from typing import Any, Literal
from uuid import uuid4

from alembic import command
from alembic.config import Config
from fastapi import Depends, FastAPI, Header, Query, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session, sessionmaker
from starlette.exceptions import HTTPException as StarletteHTTPException

from clients import CatalogError, ChatError, InventoryError, PaymentProviderError
from models import Order, OrderItem, Payment
from outbox import enqueue

log = logging.getLogger("order-payment")

PAYABLE = {"placed", "confirmed"}
# Transitions a wholesaler may make. "paid" is only ever set by a verified Razorpay webhook.
WHOLESALER_TRANSITIONS = {
    "placed": {"confirmed", "cancelled"},
    "confirmed": {"cancelled"},
    "paid": {"shipped"},
    "shipped": {"delivered"},
}


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


def money(d: Decimal) -> float:
    return float(d)


def order_view(o: Order) -> dict:
    return {
        "id": o.id,
        "retailerId": o.retailer_id,
        "wholesalerId": o.wholesaler_id,
        "status": o.status,
        "total": money(o.total),
        "currency": o.currency,
        "items": [{"productId": i.product_id, "qty": i.qty, "unitPrice": money(i.unit_price)} for i in o.items],
        "createdAt": o.created_at.isoformat(),
        "updatedAt": o.updated_at.isoformat(),
    }


class OrderLine(BaseModel):
    productId: int = Field(gt=0)
    qty: int = Field(gt=0, le=1_000_000)
    offerId: str | None = Field(None, min_length=1, max_length=64)


class PlaceOrder(BaseModel):
    items: list[OrderLine] = Field(min_length=1, max_length=50)


class StatusChange(BaseModel):
    status: Literal["confirmed", "cancelled", "shipped", "delivered"]


def create_app(session_factory: sessionmaker, catalog, razorpay=None, webhook_secret: str = "", *, inventory, chat) -> FastAPI:
    """razorpay/webhook_secret may be absent: payment endpoints then answer 503 instead of failing.
    inventory is required: an order is never placed without a stock decision. chat reads accepted offers.
    Events go to the outbox in the same transaction as the change (see outbox.py)."""
    app = FastAPI(title="Order & Payment Service", description="Provenance Order/Payment API")

    def db():
        with session_factory() as session:
            yield session

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
        return {"status": "ok", "service": "order-payment"}

    def load_order(session: Session, order_id: str, lock: bool = False) -> Order:
        stmt = select(Order).where(Order.id == order_id)
        if lock:
            stmt = stmt.with_for_update()
        order = session.scalars(stmt).first()
        if order is None:
            raise ApiError(404, "ORDER_NOT_FOUND", "order not found")
        return order

    def event_payload(o: Order) -> dict:
        return {"orderId": o.id, "retailerId": o.retailer_id, "wholesalerId": o.wholesaler_id,
                "status": o.status, "total": str(o.total), "currency": o.currency}

    @app.post("/orders")
    def place_order(req: PlaceOrder, who: Caller = Depends(caller), session: Session = Depends(db)):
        if who.role != "retailer":
            raise ApiError(403, "FORBIDDEN", "only retailers can place orders")
        product_ids = [line.productId for line in req.items]
        if len(set(product_ids)) != len(product_ids):
            raise ApiError(400, "DUPLICATE_ITEM", "each product may appear only once per order")

        try:
            quotes = [catalog.quote(line.productId, line.qty) for line in req.items]
        except CatalogError as e:
            raise ApiError(e.status, e.code, e.message) from e
        wholesalers = {q.wholesaler_id for q in quotes}
        if len(wholesalers) != 1:
            raise ApiError(422, "MIXED_WHOLESALERS", "all items in an order must come from one wholesaler")
        wholesaler_id = wholesalers.pop()

        # A line with an accepted chat offer is priced at the offer instead of the catalog tier.
        for i, line in enumerate(req.items):
            if line.offerId is None:
                continue
            try:
                offer = chat.offer(line.offerId)
            except ChatError as e:
                raise ApiError(e.status, e.code, e.message) from e
            if offer.retailer_id != who.id:
                raise ApiError(403, "FORBIDDEN", "this offer was made to another buyer")
            if offer.status != "accepted":
                raise ApiError(409, "OFFER_NOT_ACCEPTED", "only an accepted offer can price an order")
            if (offer.wholesaler_id, offer.product_id, offer.qty) != (wholesaler_id, str(line.productId), line.qty):
                raise ApiError(422, "OFFER_MISMATCH", "the offer is for a different seller, product or quantity")
            quotes[i] = replace(quotes[i], unit_price=offer.unit_price, total=offer.unit_price * line.qty)

        order = Order(
            id=str(uuid4()),  # inventory keys the reservation by it, before the row exists
            retailer_id=who.id,
            wholesaler_id=wholesaler_id,
            status="placed",
            total=sum((q.total for q in quotes), Decimal("0")),
            items=[OrderItem(product_id=q.product_id, qty=q.qty, unit_price=q.unit_price, offer_id=line.offerId)
                   for q, line in zip(quotes, req.items)],
        )
        try:
            inventory.reserve(order.id, [(q.product_id, q.qty) for q in quotes])
        except InventoryError as e:
            raise ApiError(e.status, e.code, e.message) from e
        session.add(order)
        try:
            session.flush()  # applies column defaults (currency) that the event reads
            enqueue(session, "order.placed", "OrderPlaced", order.id, event_payload(order) | {
                "items": [{"productId": q.product_id, "qty": q.qty, "unitPrice": str(q.unit_price)} for q in quotes]
            })
            session.commit()
        except IntegrityError as e:
            # The UNIQUE offer_id is the only constraint a new order can break: the offer already priced an order.
            inventory.release(order.id)
            raise ApiError(409, "OFFER_ALREADY_USED", "this offer has already been used for an order") from e
        except Exception:
            inventory.release(order.id)
            raise
        return ok(order_view(order), 201)

    @app.get("/orders/{order_id}")
    def get_order(order_id: str, who: Caller = Depends(caller), session: Session = Depends(db)):
        order = load_order(session, order_id)
        if who.role != "admin" and who.id not in (order.retailer_id, order.wholesaler_id):
            raise ApiError(403, "FORBIDDEN", "you are not a party to this order")
        return ok(order_view(order))

    @app.get("/orders")
    def list_orders(
        retailerId: str | None = None,
        wholesalerId: str | None = None,
        status: str | None = None,
        page: int = Query(1, ge=1),
        limit: int = Query(20, ge=1, le=100),
        who: Caller = Depends(caller),
        session: Session = Depends(db),
    ):
        # Non-admins only ever see their own side of the market.
        if who.role == "retailer":
            if retailerId not in (None, who.id):
                raise ApiError(403, "FORBIDDEN", "retailers can only list their own orders")
            retailerId = who.id
        elif who.role == "wholesaler":
            if wholesalerId not in (None, who.id):
                raise ApiError(403, "FORBIDDEN", "wholesalers can only list their own orders")
            wholesalerId = who.id
        elif who.role != "admin":
            raise ApiError(403, "FORBIDDEN", "only retailers, wholesalers and admins can list orders")

        stmt = select(Order)
        if retailerId:
            stmt = stmt.where(Order.retailer_id == retailerId)
        if wholesalerId:
            stmt = stmt.where(Order.wholesaler_id == wholesalerId)
        if status:
            stmt = stmt.where(Order.status == status)
        total = session.scalar(select(func.count()).select_from(stmt.subquery()))
        orders = session.scalars(stmt.order_by(Order.created_at.desc(), Order.id).offset((page - 1) * limit).limit(limit)).all()
        return ok([order_view(o) for o in orders], meta={"page": page, "limit": limit, "total": total, "hasMore": page * limit < total})

    @app.patch("/orders/{order_id}/status")
    def change_status(order_id: str, req: StatusChange, who: Caller = Depends(caller), session: Session = Depends(db)):
        order = load_order(session, order_id, lock=True)
        if who.id != order.wholesaler_id:
            raise ApiError(403, "FORBIDDEN", "only the order's wholesaler can change its status")
        if req.status not in WHOLESALER_TRANSITIONS.get(order.status, set()):
            raise ApiError(409, "INVALID_TRANSITION", f"cannot move an order from {order.status} to {req.status}")
        previous = order.status
        order.status = req.status
        enqueue(session, "order.status.changed", "OrderStatusChanged", order.id, event_payload(order) | {"previousStatus": previous})
        session.commit()
        if order.status == "cancelled":
            inventory.release(order.id)
        return ok(order_view(order))

    @app.post("/orders/{order_id}/payment")
    def start_payment(order_id: str, who: Caller = Depends(caller), session: Session = Depends(db)):
        if razorpay is None:
            raise ApiError(503, "PAYMENTS_NOT_CONFIGURED", "payments are not configured on this server")
        order = load_order(session, order_id, lock=True)
        if who.id != order.retailer_id:
            raise ApiError(403, "FORBIDDEN", "only the buyer can pay for this order")
        if order.status not in PAYABLE:
            raise ApiError(409, "ORDER_NOT_PAYABLE", f"an order that is {order.status} cannot be paid")

        amount_paise = int(order.total * 100)
        # Reuse an open attempt so a double-click doesn't create two Razorpay orders.
        payment = session.scalars(
            select(Payment).where(Payment.order_id == order.id, Payment.status == "created").order_by(Payment.created_at.desc())
        ).first()
        if payment is None:
            try:
                rzp_order_id = razorpay.create_order(amount_paise, order.currency, order.id)
            except PaymentProviderError as e:
                log.error("razorpay create_order failed: %s", e)
                raise ApiError(502, "PAYMENT_PROVIDER_ERROR", "could not start the payment, try again") from e
            payment = Payment(order_id=order.id, razorpay_order_id=rzp_order_id, status="created", amount_paise=amount_paise)
            session.add(payment)
            session.commit()
        return ok({"orderId": order.id, "razorpayOrderId": payment.razorpay_order_id, "amount": payment.amount_paise,
                   "currency": order.currency, "keyId": razorpay.key_id})

    def apply_webhook(event: dict, session: Session) -> str:
        entity = event.get("payload", {}).get("payment", {}).get("entity", {})
        rzp_order_id, rzp_payment_id, amount = entity.get("order_id"), entity.get("id"), entity.get("amount")
        payment = session.scalars(select(Payment).where(Payment.razorpay_order_id == rzp_order_id).with_for_update()).first() if rzp_order_id else None
        if payment is None:
            log.warning("webhook for unknown razorpay order %s", rzp_order_id)
            return "ignored"
        if payment.status == "captured":
            return "duplicate"

        if event.get("event") == "payment.failed":
            payment.status = "failed"
            session.commit()
            return "failed"

        if amount != payment.amount_paise:
            log.error("webhook amount %s does not match expected %s for %s", amount, payment.amount_paise, rzp_order_id)
            return "ignored"
        order = load_order(session, payment.order_id, lock=True)
        payment.status = "captured"
        payment.razorpay_payment_id = rzp_payment_id
        if order.status in PAYABLE:
            order.status = "paid"
        else:
            # e.g. cancelled while the buyer was paying: money taken but order not live, needs a manual refund.
            log.error("payment captured for order %s in status %s; refund required", order.id, order.status)
        enqueue(session, "payment.completed", "PaymentCompleted", order.id,
                event_payload(order) | {"razorpayPaymentId": rzp_payment_id, "amountPaise": amount})
        session.commit()
        return "captured"

    @app.post("/payments/webhook")
    async def razorpay_webhook(request: Request, x_razorpay_signature: str = Header(""), session: Session = Depends(db)):
        if not webhook_secret:
            raise ApiError(503, "PAYMENTS_NOT_CONFIGURED", "payments are not configured on this server")
        raw = await request.body()
        expected = hmac.new(webhook_secret.encode(), raw, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(expected, x_razorpay_signature):
            raise ApiError(400, "INVALID_SIGNATURE", "webhook signature does not match")
        try:
            event = json.loads(raw)
        except ValueError as e:
            raise ApiError(400, "INVALID_BODY", "webhook body must be JSON") from e
        if event.get("event") not in ("payment.captured", "order.paid", "payment.failed"):
            return ok({"result": "ignored"})
        result = await run_in_threadpool(apply_webhook, event, session)
        return ok({"result": result})

    return app


def init_db(engine) -> None:
    """Applies pending Alembic migrations (migrations/versions) on start."""
    cfg = Config(os.path.join(os.path.dirname(os.path.abspath(__file__)), "alembic.ini"))
    with engine.begin() as conn:
        cfg.attributes["connection"] = conn
        command.upgrade(cfg, "head")
