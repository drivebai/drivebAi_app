# DriveBai admin panel — multi-stage build.
#
# Stage 1 builds the Vue/Vite SPA. VITE_API_BASE_URL is consumed AT BUILD TIME
# (Vite inlines `import.meta.env.VITE_API_BASE_URL` into the bundle), so it
# MUST be provided as a build-arg or via .env.production at build time.
# `fly deploy --build-arg VITE_API_BASE_URL=…` is the supported override.
#
# Stage 2 serves the static build with nginx. SPA history-mode fallback rewrites
# any unknown path to /index.html so refreshing a deep link doesn't 404.

ARG VITE_API_BASE_URL=https://drivebai-api-team.fly.dev

# ----- Stage 1: build -----
FROM node:20-alpine AS build

WORKDIR /app

# Install deps first so layer caching benefits incremental rebuilds.
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY . .

# Make the build-arg visible to Vite. .env.production is also committed and
# carries the same default; this argument lets a one-off deploy point at
# a staging backend without editing the file.
ARG VITE_API_BASE_URL
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build

# ----- Stage 2: serve -----
FROM nginx:1.27-alpine AS runtime

# nginx config below sets up gzip + SPA fallback + long-cache for /assets.
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

# Fly's [http_service] expects 8080.
EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
