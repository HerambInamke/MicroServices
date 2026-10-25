//! API tests against a real Redis (TEST_REDIS_URL, e.g. redis://redis:6379/15); skipped when unset.
//! Every test uses fresh product and order ids, so tests can share the database and run in parallel.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use axum::body::Body;
use axum::http::{Request, StatusCode};
use axum::Router;
use http_body_util::BodyExt;
use inventory_sync::{router, AppState, Catalog};
use redis::aio::ConnectionManager;
use serde_json::{json, Value};
use tower::ServiceExt;

static NEXT: AtomicU64 = AtomicU64::new(0);

fn fresh() -> u64 {
    let base = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_micros() as u64 % 1_000_000_000_000;
    base * 100 + NEXT.fetch_add(1, Ordering::Relaxed) % 100
}

async fn app_with(catalog: Catalog) -> Option<Router> {
    let url = std::env::var("TEST_REDIS_URL").ok()?;
    let redis = ConnectionManager::new(redis::Client::open(url).unwrap()).await.unwrap();
    Some(router(Arc::new(AppState { redis, catalog })))
}

async fn app(owners: &[(u64, &str)]) -> Option<Router> {
    app_with(Catalog::Fake(owners.iter().map(|(id, o)| (*id, o.to_string())).collect::<HashMap<_, _>>())).await
}

async fn call(app: &Router, method: &str, uri: &str, user: Option<(&str, &str)>, body: Option<Value>) -> (StatusCode, Value) {
    let mut req = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some((id, role)) = user {
        req = req.header("x-user-id", id).header("x-user-role", role);
    }
    let req = req.body(body.map_or(Body::empty(), |b| Body::from(b.to_string()))).unwrap();
    let res = app.clone().oneshot(req).await.unwrap();
    let status = res.status();
    let bytes = res.into_body().collect().await.unwrap().to_bytes();
    (status, serde_json::from_slice(&bytes).unwrap_or(Value::Null))
}

const W1: Option<(&str, &str)> = Some(("w1", "wholesaler"));

async fn stock(app: &Router, product: u64, warehouse: &str, delta: i64) -> (StatusCode, Value) {
    call(app, "PATCH", &format!("/inventory/{product}"), W1, Some(json!({"warehouseId": warehouse, "delta": delta}))).await
}

async fn reserve(app: &Router, order: &str, items: Value) -> (StatusCode, Value) {
    call(app, "POST", "/inventory/reserve", None, Some(json!({"orderId": order, "items": items}))).await
}

#[tokio::test]
async fn untracked_then_adjusted_stock() {
    let p = fresh();
    let Some(app) = app(&[(p, "w1")]).await else { return };
    let (status, body) = call(&app, "GET", &format!("/inventory/{p}"), None, None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["data"], json!({"productId": p, "tracked": false, "total": 0, "warehouses": []}));

    assert_eq!(stock(&app, p, "surat", 5).await.0, StatusCode::OK);
    let (_, body) = stock(&app, p, "mumbai", 3).await;
    assert_eq!(body["data"]["total"], 8);
    assert_eq!(body["data"]["warehouses"], json!([{"warehouseId": "mumbai", "qty": 3}, {"warehouseId": "surat", "qty": 5}]));

    let (status, body) = stock(&app, p, "surat", -6).await;
    assert_eq!((status, body["error"]["code"].as_str()), (StatusCode::CONFLICT, Some("INSUFFICIENT_STOCK")));
    assert_eq!(stock(&app, p, "surat", -5).await.1["data"]["total"], 3);
}

#[tokio::test]
async fn adjust_refusals() {
    let p = fresh();
    let Some(app) = app(&[(p, "w1")]).await else { return };
    let uri = format!("/inventory/{p}");
    let body = Some(json!({"warehouseId": "surat", "delta": 1}));
    assert_eq!(call(&app, "PATCH", &uri, None, body.clone()).await.0, StatusCode::UNAUTHORIZED);
    assert_eq!(call(&app, "PATCH", &uri, Some(("r1", "retailer")), body.clone()).await.0, StatusCode::FORBIDDEN);
    assert_eq!(call(&app, "PATCH", &uri, Some(("w2", "wholesaler")), body.clone()).await.0, StatusCode::FORBIDDEN);
    assert_eq!(call(&app, "PATCH", &format!("/inventory/{}", fresh()), W1, body.clone()).await.0, StatusCode::NOT_FOUND);
    assert_eq!(call(&app, "PATCH", "/inventory/abc", W1, body.clone()).await.0, StatusCode::NOT_FOUND);
    assert_eq!(stock(&app, p, "surat", 0).await.0, StatusCode::BAD_REQUEST);
    assert_eq!(stock(&app, p, "../x", 1).await.0, StatusCode::BAD_REQUEST);
    assert_eq!(call(&app, "PATCH", &uri, W1, Some(json!({"delta": 1}))).await.0, StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn catalog_down_is_502() {
    let Some(app) = app_with(Catalog::http("http://127.0.0.1:1".into())).await else { return };
    let (status, body) = stock(&app, fresh(), "surat", 1).await;
    assert_eq!((status, body["error"]["code"].as_str()), (StatusCode::BAD_GATEWAY, Some("CATALOG_UNAVAILABLE")));
}

#[tokio::test]
async fn reserve_is_all_or_nothing_across_warehouses_and_idempotent() {
    let (a, b, untracked) = (fresh(), fresh(), fresh());
    let Some(app) = app(&[(a, "w1"), (b, "w1")]).await else { return };
    stock(&app, a, "surat", 4).await;
    stock(&app, a, "mumbai", 4).await;
    stock(&app, b, "surat", 1).await;

    // b is short, so nothing of a is taken either.
    let (status, body) = reserve(&app, &format!("o{a}-1"), json!([{"productId": a, "qty": 6}, {"productId": b, "qty": 2}])).await;
    assert_eq!((status, body["error"]["code"].as_str()), (StatusCode::CONFLICT, Some("OUT_OF_STOCK")));
    assert!(body["error"]["message"].as_str().unwrap().contains(&b.to_string()));
    assert_eq!(call(&app, "GET", &format!("/inventory/{a}"), None, None).await.1["data"]["total"], 8);

    let order = format!("o{a}-2");
    let items = json!([{"productId": a, "qty": 6}, {"productId": untracked, "qty": 1000}]);
    assert_eq!(reserve(&app, &order, items.clone()).await.0, StatusCode::OK);
    assert_eq!(reserve(&app, &order, items).await.0, StatusCode::OK); // retry counts once
    let (_, body) = call(&app, "GET", &format!("/inventory/{a}"), None, None).await;
    assert_eq!(body["data"]["warehouses"], json!([{"warehouseId": "mumbai", "qty": 0}, {"warehouseId": "surat", "qty": 2}]));

    let release = |o: String| {
        let app = app.clone();
        async move { call(&app, "POST", "/inventory/release", None, Some(json!({"orderId": o}))).await.1["data"]["released"].clone() }
    };
    assert_eq!(release(order.clone()).await, json!(true));
    assert_eq!(release(order).await, json!(false)); // already released
    assert_eq!(call(&app, "GET", &format!("/inventory/{a}"), None, None).await.1["data"]["total"], 8);
}

#[tokio::test]
async fn concurrent_reserves_never_oversell() {
    let p = fresh();
    let Some(app) = app(&[(p, "w1")]).await else { return };
    stock(&app, p, "surat", 10).await;
    let tasks: Vec<_> = (0..25)
        .map(|i| {
            let app = app.clone();
            tokio::spawn(async move { reserve(&app, &format!("c{p}-{i}"), json!([{"productId": p, "qty": 1}])).await.0 })
        })
        .collect();
    let mut won = 0;
    for t in tasks {
        if t.await.unwrap() == StatusCode::OK {
            won += 1;
        }
    }
    assert_eq!(won, 10);
    assert_eq!(call(&app, "GET", &format!("/inventory/{p}"), None, None).await.1["data"]["total"], 0);
}

#[tokio::test]
async fn reserve_validation() {
    let Some(app) = app(&[]).await else { return };
    assert_eq!(reserve(&app, "", json!([{"productId": 1, "qty": 1}])).await.0, StatusCode::BAD_REQUEST);
    assert_eq!(reserve(&app, "o1", json!([])).await.0, StatusCode::BAD_REQUEST);
    assert_eq!(reserve(&app, "o1", json!([{"productId": 1, "qty": 0}])).await.0, StatusCode::BAD_REQUEST);
    assert_eq!(call(&app, "POST", "/inventory/reserve", None, Some(json!({"nope": 1}))).await.0, StatusCode::BAD_REQUEST);
}
