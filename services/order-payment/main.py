from fastapi import FastAPI
import os

app = FastAPI(title="Order & Payment Service", description="Provenance Order/Payment API")

@app.get("/health")
def health_check():
    return {"status": "ok", "service": "order-payment"}

if __name__ == "__main__":
    import uvicorn
    port = int(os.getenv("PORT", 8084))
    uvicorn.run(app, host="0.0.0.0", port=port)