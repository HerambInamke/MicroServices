use std::sync::Arc;
use std::time::Duration;

use inventory_sync::{router, AppState, Catalog};
use redis::aio::ConnectionManager;

fn require_env(name: &str) -> String {
    std::env::var(name).ok().filter(|v| !v.is_empty()).unwrap_or_else(|| {
        eprintln!("{name} is required");
        std::process::exit(1);
    })
}

/// Retries because Redis may still be starting when the container comes up.
async fn connect(url: &str) -> ConnectionManager {
    let client = redis::Client::open(url).unwrap_or_else(|e| {
        eprintln!("invalid REDIS_URL: {e}");
        std::process::exit(1);
    });
    for attempt in 1..=30 {
        match ConnectionManager::new(client.clone()).await {
            Ok(conn) => return conn,
            Err(e) => eprintln!("waiting for redis ({attempt}): {e}"),
        }
        tokio::time::sleep(Duration::from_secs(2)).await;
    }
    eprintln!("redis unavailable");
    std::process::exit(1);
}

#[tokio::main]
async fn main() {
    let redis = connect(&require_env("REDIS_URL")).await;
    let state = Arc::new(AppState { redis, catalog: Catalog::http(require_env("CATALOG_URL")) });
    let port = std::env::var("PORT").unwrap_or_else(|_| "8090".into());
    let listener = tokio::net::TcpListener::bind(format!("0.0.0.0:{port}")).await.expect("bind");
    println!("inventory-sync listening on {port}");
    axum::serve(listener, router(state)).await.expect("server");
}
