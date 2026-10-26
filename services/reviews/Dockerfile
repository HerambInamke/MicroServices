FROM node:20-alpine

WORKDIR /app

COPY package*.json ./
RUN npm ci --omit=dev

COPY . .

ENV PORT=8094
EXPOSE 8094

USER node
CMD ["node", "server.js"]
