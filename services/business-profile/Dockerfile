FROM node:20-alpine

WORKDIR /app

COPY package*.json ./
# Use --omit=dev for production dependencies. In local docker-compose, npm install is sufficient.
RUN npm install

COPY . .

ENV PORT=8082
EXPOSE 8082

CMD ["npm", "start"]