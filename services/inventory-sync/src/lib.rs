//! Inventory Sync: per-warehouse stock in Redis (`stock:{productId}:{warehouseId}`, `warehouses:{productId}`).
//! Stock changes run as Lua scripts so check-and-change is atomic.

use std::collections::HashMap;
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use axum::extract::rejection::JsonRejection;
use axum::extract::{Path, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use redis::aio::ConnectionManager;
use redis::{AsyncCommands, Script};
use serde::Deserialize;
use serde_json::{json, Value};

const MAX_QTY: i64 = 1_000_000;

pub struct ApiError {
    status: StatusCode,
    code: &'static str,
    message: String,
}

fn err(status: StatusCode, code: &'static str, message: impl Into<String>) -> ApiError {
    ApiError { status, code, message: message.into() }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        let body = json!({"data": null, "error": {"code": self.code, "message": self.message}, "meta": {}});
        (self.status, Json(body)).into_response()
    }
}

impl From<redis::RedisError> for ApiError {
    fn from(e: redis::RedisError) -> Self {
        eprintln!("redis error: {e}");
        err(StatusCode::SERVICE_UNAVAILABLE, "STORE_UNAVAILABLE", "inventory store is unavailable")
    }
}

impl From<JsonRejection> for ApiError {
    fn from(e: JsonRejection) -> Self {
        err(StatusCode::BAD_REQUEST, "VALIDATION_ERROR", e.body_text())
    }
}

fn ok(data: Value) -> Response {
    Json(json!({"data": data, "error": null, "meta": {}})).into_response()
}

/// Who owns a product. `Fake` is for tests.
pub enum Catalog {
    Http { client: reqwest::Client, base: String },
    Fake(HashMap<u64, String>),
}

impl Catalog {
    pub fn http(base: String) -> Self {
        let client = reqwest::Client::builder().timeout(Duration::from_secs(5)).build().expect("http client");
        Catalog::Http { client, base }
    }

    async fn owner(&self, product_id: u64) -> Result<String, ApiError> {
        let not_found = || err(StatusCode::NOT_FOUND, "PRODUCT_NOT_FOUND", format!("product {product_id} not found"));
        let down = || err(StatusCode::BAD_GATEWAY, "CATALOG_UNAVAILABLE", "could not reach the catalog");
        match self {
            Catalog::Fake(owners) => owners.get(&product_id).cloned().ok_or_else(not_found),
            Catalog::Http { client, base } => {
                let res = client.get(format!("{base}/products/{product_id}")).send().await.map_err(|_| down())?;
                if res.status() == StatusCode::NOT_FOUND {
                    return Err(not_found());
                }
                if !res.status().is_success() {
                    return Err(down());
                }
                let body: Value = res.json().await.map_err(|_| down())?;
                body["data"]["wholesalerId"].as_str().map(str::to_owned).ok_or_else(down)
            }
        }
    }
}

pub struct AppState {
    pub redis: ConnectionManager,
    pub catalog: Catalog,
}

type Shared = State<Arc<AppState>>;

fn script(cell: &'static OnceLock<Script>, src: &str) -> &'static Script {
    cell.get_or_init(|| Script::new(src))
}

static ADJUST: OnceLock<Script> = OnceLock::new();
static RESERVE: OnceLock<Script> = OnceLock::new();
static RELEASE: OnceLock<Script> = OnceLock::new();

pub fn router(state: Arc<AppState>) -> Router {
    Router::new()
        .route("/health", get(|| async { Json(json!({"status": "ok", "service": "inventory-sync"})) }))
        // Internal: called by Order/Payment on the compose network; the gateway does not route these.
        .route("/inventory/reserve", post(reserve))
        .route("/inventory/release", post(release))
        .route("/inventory/:product_id", get(get_stock).patch(adjust))
        .with_state(state)
}

fn product_id(raw: &str) -> Result<u64, ApiError> {
    raw.parse::<u64>()
        .ok()
        .filter(|id| *id > 0)
        .ok_or_else(|| err(StatusCode::NOT_FOUND, "PRODUCT_NOT_FOUND", "product not found"))
}

async fn stock_view(redis: &mut ConnectionManager, id: u64) -> Result<Value, ApiError> {
    let mut warehouses: Vec<String> = redis.smembers(format!("warehouses:{id}")).await?;
    warehouses.sort();
    let qtys: Vec<i64> = if warehouses.is_empty() {
        vec![]
    } else {
        let keys: Vec<String> = warehouses.iter().map(|w| format!("stock:{id}:{w}")).collect();
        let raw: Vec<Option<i64>> = redis.mget(keys).await?;
        raw.into_iter().map(|q| q.unwrap_or(0)).collect()
    };
    let rows: Vec<Value> = warehouses.iter().zip(&qtys).map(|(w, q)| json!({"warehouseId": w, "qty": q})).collect();
    Ok(json!({"productId": id, "tracked": !warehouses.is_empty(), "total": qtys.iter().sum::<i64>(), "warehouses": rows}))
}

async fn get_stock(State(state): Shared, Path(raw): Path<String>) -> Result<Response, ApiError> {
    let id = product_id(&raw)?;
    Ok(ok(stock_view(&mut state.redis.clone(), id).await?))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Adjust {
    warehouse_id: String,
    delta: i64,
}

async fn adjust(
    State(state): Shared,
    Path(raw): Path<String>,
    headers: HeaderMap,
    body: Result<Json<Adjust>, JsonRejection>,
) -> Result<Response, ApiError> {
    // Identity headers are set by the gateway after it verifies the JWT.
    let header = |name: &str| headers.get(name).and_then(|v| v.to_str().ok()).unwrap_or("").to_owned();
    let (user, role) = (header("x-user-id"), header("x-user-role"));
    if user.is_empty() {
        return Err(err(StatusCode::UNAUTHORIZED, "UNAUTHENTICATED", "authentication required"));
    }
    if role != "wholesaler" {
        return Err(err(StatusCode::FORBIDDEN, "FORBIDDEN", "only wholesalers can change stock"));
    }
    let id = product_id(&raw)?;
    let Json(req) = body?;
    let valid_warehouse = !req.warehouse_id.is_empty()
        && req.warehouse_id.len() <= 64
        && req.warehouse_id.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_');
    if !valid_warehouse {
        return Err(err(StatusCode::BAD_REQUEST, "VALIDATION_ERROR", "warehouseId must be 1-64 letters, digits, - or _"));
    }
    if req.delta == 0 || req.delta.abs() > MAX_QTY {
        return Err(err(StatusCode::BAD_REQUEST, "VALIDATION_ERROR", format!("delta must be non-zero and at most {MAX_QTY} either way")));
    }
    if state.catalog.owner(id).await? != user {
        return Err(err(StatusCode::FORBIDDEN, "FORBIDDEN", "you can only change stock of your own products"));
    }

    let mut redis = state.redis.clone();
    let result: String = script(&ADJUST, include_str!("adjust.lua"))
        .arg(id)
        .arg(&req.warehouse_id)
        .arg(req.delta)
        .invoke_async(&mut redis)
        .await?;
    if result == "insufficient" {
        return Err(err(StatusCode::CONFLICT, "INSUFFICIENT_STOCK", "stock cannot go below zero"));
    }
    Ok(ok(stock_view(&mut redis, id).await?))
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Line {
    product_id: u64,
    qty: i64,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Reserve {
    order_id: String,
    items: Vec<Line>,
}

fn valid_order_id(id: &str) -> Result<(), ApiError> {
    if id.is_empty() || id.len() > 100 {
        return Err(err(StatusCode::BAD_REQUEST, "VALIDATION_ERROR", "orderId must be 1-100 characters"));
    }
    Ok(())
}

// Only Order/Payment calls these, directly and without an identity. Anything carrying gateway identity
// headers came in from outside, so refuse it even if a gateway rule ever lets it through.
fn internal_only(headers: &HeaderMap) -> Result<(), ApiError> {
    if headers.contains_key("x-user-id") || headers.contains_key("x-user-role") {
        return Err(err(StatusCode::FORBIDDEN, "FORBIDDEN", "internal endpoint"));
    }
    Ok(())
}

async fn reserve(State(state): Shared, headers: HeaderMap, body: Result<Json<Reserve>, JsonRejection>) -> Result<Response, ApiError> {
    internal_only(&headers)?;
    let Json(req) = body?;
    valid_order_id(&req.order_id)?;
    if req.items.is_empty() || req.items.len() > 50 || req.items.iter().any(|l| l.qty < 1 || l.qty > MAX_QTY) {
        return Err(err(StatusCode::BAD_REQUEST, "VALIDATION_ERROR", "1-50 items, each with qty between 1 and 1000000"));
    }
    let mut invocation = script(&RESERVE, include_str!("reserve.lua")).prepare_invoke();
    invocation.arg(&req.order_id);
    for line in &req.items {
        invocation.arg(line.product_id).arg(line.qty);
    }
    let result: String = invocation.invoke_async(&mut state.redis.clone()).await?;
    match result.strip_prefix("short:") {
        Some(pid) => Err(err(StatusCode::CONFLICT, "OUT_OF_STOCK", format!("not enough stock for product {pid}"))),
        None => Ok(ok(json!({"orderId": req.order_id, "reserved": true}))),
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Release {
    order_id: String,
}

async fn release(State(state): Shared, headers: HeaderMap, body: Result<Json<Release>, JsonRejection>) -> Result<Response, ApiError> {
    internal_only(&headers)?;
    let Json(req) = body?;
    valid_order_id(&req.order_id)?;
    let result: String = script(&RELEASE, include_str!("release.lua"))
        .arg(&req.order_id)
        .invoke_async(&mut state.redis.clone())
        .await?;
    Ok(ok(json!({"orderId": req.order_id, "released": result == "released"})))
}
