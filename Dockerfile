FROM node:20-bookworm-slim AS deps
WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --omit=dev

FROM node:20-bookworm-slim AS runtime
WORKDIR /app

ENV NODE_ENV=production

COPY --from=deps /app/node_modules ./node_modules
COPY . .

RUN mkdir -p /app/data && chown -R node:node /app

# Add node user to docker group (GID 999 is common for docker group)
# This allows the container to access the Docker socket
RUN groupadd -g 999 docker || true && usermod -aG docker node

USER node
EXPOSE 3000

CMD ["node", "src/index.js"]
