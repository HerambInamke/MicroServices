FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Bake the CLIP weights into the image so the service starts without network access.
ENV HF_HOME=/models
COPY embedder.py .
RUN python -c "from embedder import ClipEmbedder; ClipEmbedder()"
ENV HF_HUB_OFFLINE=1

COPY . .

ENV PORT=8088
EXPOSE 8088

CMD ["python", "main.py"]
