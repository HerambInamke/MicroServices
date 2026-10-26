FROM node:20-alpine

WORKDIR /app

COPY package*.json ./
RUN npm ci --omit=dev

COPY . .

ENV PORT=8093
EXPOSE 8093

USER node
CMD ["node", "server.js"]
